// Package report renders `spore report`: a short plain-text digest of
// each project's events ledger plus live state, meant to be read
// first thing in the morning. The default view stays under one screen
// (counts and the items that need attention); full adds every event.
package report

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/versality/spore/internal/events"
)

// Live is a project's current state, gathered by the caller so the
// renderer stays free of tmux and git.
type Live struct {
	// TasksByStatus maps a status (active, paused, ...) to its slugs.
	TasksByStatus map[string][]string
	Workers       []string
	RoleLoops     []string
	Coordinator   bool
	Escalated     []string
	Ready         []string
	// Err is set when live state could not be read (no project root
	// recorded, tasks dir unreadable).
	Err string
}

// Project is one project's section.
type Project struct {
	Name   string
	Events []events.Event
	Live   *Live
}

// Header is the host-wide part above the project sections.
type Header struct {
	Since, Now   time.Time
	FleetEnabled *bool
	Alerts       []string
}

// statusOrder fixes the order task statuses print in.
var statusOrder = []string{"active", "blocked", "paused", "draft", "done"}

// Render writes the report. full adds each project's event log.
func Render(w io.Writer, h Header, ps []Project, full bool) {
	fmt.Fprintf(w, "spore report: %s to %s (%s)\n", stamp(h.Since), stamp(h.Now), span(h.Now.Sub(h.Since)))
	var host []string
	if h.FleetEnabled != nil {
		if *h.FleetEnabled {
			host = append(host, "fleet enabled")
		} else {
			host = append(host, "fleet DISABLED (kill-switch off)")
		}
	}
	if len(h.Alerts) == 0 {
		host = append(host, "no active alerts")
	}
	if len(host) > 0 {
		fmt.Fprintln(w, strings.Join(host, ", "))
	}
	for _, a := range h.Alerts {
		fmt.Fprintf(w, "ALERT: %s\n", a)
	}
	if len(ps) == 0 {
		fmt.Fprintln(w, "\nno projects with a ledger")
		return
	}
	for _, p := range ps {
		fmt.Fprintln(w)
		renderProject(w, p, full)
	}
}

func renderProject(w io.Writer, p Project, full bool) {
	fmt.Fprintf(w, "== %s\n", p.Name)
	s := summarize(p.Events)

	if l := p.Live; l != nil && l.Err == "" {
		var parts []string
		for _, st := range orderedStatuses(l.TasksByStatus) {
			parts = append(parts, fmt.Sprintf("%d %s", len(l.TasksByStatus[st]), st))
		}
		if len(parts) == 0 {
			parts = []string{"none"}
		}
		fmt.Fprintf(w, "tasks: %s\n", strings.Join(parts, ", "))
	} else if l != nil {
		fmt.Fprintf(w, "tasks: unknown (%s)\n", l.Err)
	}

	line(w, "done", s.done)
	line(w, "started", s.started)
	if len(s.prs) > 0 {
		fmt.Fprintf(w, "PRs opened (%d):\n", len(s.prs))
		for _, pr := range s.prs {
			fmt.Fprintf(w, "  %s\n", pr)
		}
	}

	if s.redactions > 0 {
		fmt.Fprintf(w, "redactions: %d (%s)\n", s.redactions, s.redactionClasses)
	}

	var attn []string
	if l := p.Live; l != nil {
		for _, slug := range l.Escalated {
			attn = append(attn, "escalated role loop: "+slug)
		}
		if l.Err == "" && !l.Coordinator {
			attn = append(attn, "coordinator not running")
		}
		for _, slug := range l.TasksByStatus["blocked"] {
			attn = append(attn, "blocked: "+slug)
		}
	}
	attn = append(attn, s.attention...)
	if len(attn) > 0 {
		fmt.Fprintln(w, "attention:")
		for _, a := range attn {
			fmt.Fprintf(w, "  %s\n", a)
		}
	}

	if l := p.Live; l != nil && l.Err == "" {
		var run []string
		if l.Coordinator {
			run = append(run, "coordinator")
		}
		for _, s := range l.Workers {
			run = append(run, "worker "+s)
		}
		for _, s := range l.RoleLoops {
			run = append(run, "role loop "+s)
		}
		if len(run) == 0 {
			run = []string{"nothing"}
		}
		fmt.Fprintf(w, "running: %s\n", strings.Join(run, ", "))
		line(w, "ready for review", l.Ready)
	}
	if len(s.roleNotes) > 0 {
		fmt.Fprintf(w, "role loops: %s\n", strings.Join(s.roleNotes, "; "))
	}
	if s.respawns != "" {
		fmt.Fprintf(w, "respawns: %s\n", s.respawns)
	}
	if len(p.Events) == 0 {
		fmt.Fprintln(w, "no events in the window")
	}

	if full && len(p.Events) > 0 {
		fmt.Fprintln(w, "events:")
		for _, e := range p.Events {
			fmt.Fprintf(w, "  %s\n", formatEvent(e))
		}
	}
}

type summary struct {
	done, started []string
	prs           []string
	attention     []string
	roleNotes     []string
	respawns      string
	// redactions sums the redacted matches in text posted to GitHub;
	// redactionClasses breaks it down by pattern class.
	redactions       int
	redactionClasses string
}

func summarize(evs []events.Event) summary {
	var s summary
	alerts := map[string]int{}
	alertLast := map[string]events.Event{}
	var alertKinds []string
	var reconcileFailed int
	var lastFailure events.Event
	skipped := map[string]bool{}
	respawns := map[string]int{}
	phases := map[string]string{}
	verdicts := map[string][]string{}
	var roleSlugs []string
	redacted := map[string]int{}

	for _, e := range evs {
		switch e.Kind {
		case events.TaskDone:
			s.done = appendUnique(s.done, e.Slug)
		case events.TaskStart, events.WorkerSpawn:
			s.started = appendUnique(s.started, e.Slug)
		case events.PROpened:
			pr := e.Fields["url"]
			if e.Slug != "" {
				pr += " (" + e.Slug + ")"
			}
			s.prs = appendUnique(s.prs, pr)
		case events.WorkerCrashLoop:
			s.attention = append(s.attention, fmt.Sprintf("crash loop, respawn stopped: worker %s (%s)", e.Slug, clock(e.TS)))
		case events.CoordinatorCrash:
			s.attention = append(s.attention, fmt.Sprintf("crash loop, respawn stopped: coordinator (%s)", clock(e.TS)))
		case events.RoleCrashLoop:
			s.attention = append(s.attention, fmt.Sprintf("crash loop, respawn stopped: %s %s (%s)", e.Slug, e.Fields["role"], clock(e.TS)))
		case events.Alert:
			k := e.Fields["error"]
			if k == "" {
				k = e.Detail
			}
			if alerts[k] == 0 {
				alertKinds = append(alertKinds, k)
			}
			alerts[k]++
			alertLast[k] = e
		case events.ReconcileFailed:
			reconcileFailed++
			lastFailure = e
		case events.ReconcileSkipped:
			skipped[e.Detail] = true
		case events.WorkerRespawn:
			respawns["worker"]++
		case events.CoordinatorRespawn:
			respawns["coordinator"]++
		case events.RoleRespawn:
			respawns["role"]++
		case events.RolePhase:
			if _, ok := phases[e.Slug]; !ok {
				roleSlugs = appendUnique(roleSlugs, e.Slug)
			}
			phases[e.Slug] = e.Detail
		case events.Redaction:
			n, err := strconv.Atoi(e.Fields["count"])
			if err != nil || n < 1 {
				n = 1
			}
			redacted[e.Fields["class"]] += n
			s.redactions += n
		case events.RoleVerdict:
			roleSlugs = appendUnique(roleSlugs, e.Slug)
			v := e.Fields["verdict"]
			if v == "request_changes" {
				v = "changes"
			}
			verdicts[e.Slug] = append(verdicts[e.Slug], e.Fields["reviewer"]+e.Fields["round"]+" "+v)
		}
	}
	var classes []string
	for c := range redacted {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	for i, c := range classes {
		classes[i] = fmt.Sprintf("%d %s", redacted[c], c)
	}
	s.redactionClasses = strings.Join(classes, ", ")
	for _, k := range alertKinds {
		last := alertLast[k]
		a := fmt.Sprintf("alert %s x%d (last %s", k, alerts[k], clock(last.TS))
		if r := last.Fields["reset"]; r != "" {
			a += ", resets " + r
		}
		s.attention = append(s.attention, a+")")
	}
	if reconcileFailed > 0 {
		s.attention = append(s.attention, fmt.Sprintf("reconcile failed x%d (last %s: %s)", reconcileFailed, clock(lastFailure.TS), lastFailure.Detail))
	}
	var skips []string
	for d := range skipped {
		skips = append(skips, d)
	}
	sort.Strings(skips)
	for _, d := range skips {
		s.attention = append(s.attention, "reconcile skipped: "+d)
	}
	for _, slug := range roleSlugs {
		note := slug
		if ph := phases[slug]; ph != "" {
			note += " now " + ph
		}
		if v := verdicts[slug]; len(v) > 0 {
			note += " (" + strings.Join(v, ", ") + ")"
		}
		s.roleNotes = append(s.roleNotes, note)
	}
	var rs []string
	for _, k := range []string{"worker", "coordinator", "role"} {
		if n := respawns[k]; n > 0 {
			rs = append(rs, fmt.Sprintf("%s %d", k, n))
		}
	}
	s.respawns = strings.Join(rs, ", ")
	return s
}

func formatEvent(e events.Event) string {
	b := clock(e.TS) + " " + e.Kind
	if e.Slug != "" {
		b += " " + e.Slug
	}
	if e.Detail != "" {
		b += ": " + e.Detail
	}
	var keys []string
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b += " " + k + "=" + e.Fields[k]
	}
	return b
}

func orderedStatuses(m map[string][]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, st := range statusOrder {
		if len(m[st]) > 0 {
			out = append(out, st)
			seen[st] = true
		}
	}
	var rest []string
	for st, v := range m {
		if !seen[st] && len(v) > 0 {
			rest = append(rest, st)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// line prints "label: a, b, c" with at most six names, so one busy
// night does not push the attention block off the screen.
func line(w io.Writer, label string, items []string) {
	if len(items) == 0 {
		return
	}
	shown := items
	more := ""
	if len(shown) > 6 {
		shown, more = shown[:6], fmt.Sprintf(" (+%d more)", len(items)-6)
	}
	fmt.Fprintf(w, "%s (%d): %s%s\n", label, len(items), strings.Join(shown, ", "), more)
}

func appendUnique(xs []string, x string) []string {
	if x == "" {
		return xs
	}
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

func stamp(t time.Time) string { return t.Local().Format("Mon 15:04") }

func clock(t time.Time) string { return t.Local().Format("15:04") }

func span(d time.Duration) string {
	if d >= time.Hour && d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return d.Round(time.Minute).String()
}
