package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// AlertKind names an account-wide failure that stops every pane on the
// host: one notice per incident, not one per pane.
type AlertKind string

const (
	AlertUsageLimit AlertKind = "usage-limit"
	AlertLogin      AlertKind = "login"
)

// alertCooldown is how long a raised alert stays claimed even when a
// turn succeeds meanwhile. A per-minute 429 clears within seconds and
// would otherwise re-alert on every burst.
const alertCooldown = 15 * time.Minute

// StopFailureInput is the part of Claude Code's StopFailure payload
// spore reads. Error is the matcher value: rate_limit,
// authentication_failed, server_error, ...
type StopFailureInput struct {
	Error                string `json:"error"`
	LastAssistantMessage string `json:"last_assistant_message"`
}

// Alert is one detected failure, as raised from a pane.
type Alert struct {
	Kind    AlertKind
	Project string
	PaneID  string
	Session string
	Detail  string
}

// FailedPane is the record a pane leaves when its turn ended on an
// alert; `spore fleet nudge` resumes the panes it lists.
type FailedPane struct {
	Kind    AlertKind `json:"kind"`
	PaneID  string    `json:"pane"`
	Session string    `json:"session"`
	Project string    `json:"project"`
	Ts      time.Time `json:"ts"`

	path string
}

// Seams for tests: the operator display and the tmux session lookup.
var (
	displayToOperator = tmuxDisplayAll
	paneSession       = tmuxPaneSession
	alertNow          = time.Now
)

func alertKindFor(errType string) AlertKind {
	switch errType {
	case "rate_limit":
		return AlertUsageLimit
	case "authentication_failed", "oauth_org_not_allowed":
		return AlertLogin
	}
	return ""
}

// StopFailure handles Claude Code's StopFailure hook (a turn ended in
// an API error; Stop hooks do not run). It puts the turn's inbox
// messages back for redelivery and, for a usage limit or an expired
// login, raises a deduplicated alert. No-op outside spore panes.
func StopFailure(in StopFailureInput) error {
	project := os.Getenv("WT_PROJECT")
	if project == "" {
		return nil
	}
	var errs []error
	if inbox := os.Getenv("SPORE_TASK_INBOX"); inbox != "" {
		errs = append(errs, RequeueInflight(inbox))
	}
	kind := alertKindFor(in.Error)
	if kind == "" {
		return errors.Join(errs...)
	}
	a := Alert{Kind: kind, Project: project, PaneID: os.Getenv("TMUX_PANE"), Detail: in.LastAssistantMessage}
	if a.PaneID != "" {
		a.Session = paneSession(a.PaneID)
	}
	if a.Session == "" {
		a.Session = os.Getenv("SPORE_TASK_SLUG")
	}
	errs = append(errs, RaiseAlert(a))
	return errors.Join(errs...)
}

// AlertsDir is <XDG_STATE_HOME or ~/.local/state>/spore/alerts. It is
// host-wide: the login and the usage limit belong to the account, not
// to a project.
func AlertsDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "spore", "alerts")
}

// RaiseAlert records the failed pane and sends each notice once per
// incident: to the project's coordinator inbox (claim
// `<kind>@<project>`) and to the operator (claim `<kind>`, shown on
// every attached tmux client and kept for `spore fleet status`). The
// claims are O_EXCL files, so concurrent panes race safely.
func RaiseAlert(a Alert) error {
	dir := AlertsDir()
	if dir == "" {
		return errors.New("alerts: no state dir")
	}
	if err := os.MkdirAll(filepath.Join(dir, "panes"), 0o700); err != nil {
		return err
	}
	now := alertNow()
	var errs []error
	if a.PaneID != "" {
		errs = append(errs, RecordFailedPane(FailedPane{
			Kind: a.Kind, PaneID: a.PaneID, Session: a.Session, Project: a.Project, Ts: now,
		}))
	}
	body := alertBody(a)
	if ok, err := claimAlert(filepath.Join(dir, string(a.Kind)+"@"+a.Project), body); err != nil {
		errs = append(errs, err)
	} else if ok {
		errs = append(errs, NotifyCoordinatorEvent(a.Project, "spore-alert", body))
	}
	if ok, err := claimAlert(filepath.Join(dir, string(a.Kind)), now.Format(time.RFC3339)+" "+body); err != nil {
		errs = append(errs, err)
	} else if ok {
		errs = append(errs, displayToOperator("spore: "+body))
	}
	return errors.Join(errs...)
}

// ClearAlerts runs after a turn that succeeded: the pane drops its
// failed record, and alert claims past the cooldown are released so the
// next incident alerts again. A success anywhere means the account
// works again, so every claim is released, not just this project's.
func ClearAlerts() error {
	if os.Getenv("WT_PROJECT") == "" {
		return nil
	}
	dir := AlertsDir()
	if dir == "" {
		return nil
	}
	if id := os.Getenv("TMUX_PANE"); id != "" {
		_ = os.Remove(failedPanePath(dir, id))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	now := alertNow()
	for _, e := range entries {
		if e.IsDir() || !isAlertClaim(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < alertCooldown {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
	return nil
}

func isAlertClaim(name string) bool {
	kind, _, _ := strings.Cut(name, "@")
	return kind == string(AlertUsageLimit) || kind == string(AlertLogin)
}

// ActiveAlerts returns the host-level alert lines (time, then body)
// that are still claimed.
func ActiveAlerts() []string {
	dir := AlertsDir()
	var out []string
	for _, k := range []AlertKind{AlertLogin, AlertUsageLimit} {
		if b, err := os.ReadFile(filepath.Join(dir, string(k))); err == nil {
			out = append(out, strings.TrimSpace(string(b)))
		}
	}
	return out
}

// FailedPanes lists the recorded failed panes, oldest first.
func FailedPanes() ([]FailedPane, error) {
	dir := filepath.Join(AlertsDir(), "panes")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []FailedPane
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var fp FailedPane
		if json.Unmarshal(b, &fp) != nil || fp.PaneID == "" {
			_ = os.Remove(p)
			continue
		}
		fp.path = p
		out = append(out, fp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ts.Before(out[j].Ts) })
	return out, nil
}

// Forget drops the pane's failed record.
func (fp FailedPane) Forget() error {
	if fp.path == "" {
		return nil
	}
	if err := os.Remove(fp.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func failedPanePath(dir, paneID string) string {
	return filepath.Join(dir, "panes", strings.TrimPrefix(paneID, "%")+".json")
}

// RecordFailedPane writes (or replaces) the pane's failed record.
func RecordFailedPane(fp FailedPane) error {
	dir := AlertsDir()
	if err := os.MkdirAll(filepath.Join(dir, "panes"), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(fp)
	if err != nil {
		return err
	}
	dst := failedPanePath(dir, fp.PaneID)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// claimAlert creates path exclusively with body. false means another
// pane already holds the claim for this incident.
func claimAlert(path, body string) (bool, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, err
	}
	_, werr := f.WriteString(body + "\n")
	return true, errors.Join(werr, f.Close())
}

// resetRe pulls the reset time out of Claude Code's limit banners
// ("resets 2:15pm (Europe/Oslo)", "limit resets 2:15pm").
var resetRe = regexp.MustCompile(`(?i)\bresets?\s+(?:at\s+)?([^·\n.;]+)`)

func resetTime(detail string) string {
	m := resetRe.FindStringSubmatch(detail)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

const nudgeHint = "`spore fleet nudge`"

func alertBody(a Alert) string {
	pane := a.Session
	if pane == "" {
		pane = a.Project
	}
	detail := oneLine(strings.TrimSpace(a.Detail))
	if len(detail) > 200 {
		detail = detail[:200] + "..."
	}
	if detail == "" {
		detail = "no detail"
	}
	switch a.Kind {
	case AlertLogin:
		return fmt.Sprintf("login expired (pane %s: %s). Every Claude Code pane on this host shares the login, so all of them are stopped. "+
			"Recover: run /login once in any Claude Code pane, then run %s to resume every stopped pane. Do not restart panes: they keep their context.",
			pane, detail, nudgeHint)
	default:
		if r := resetTime(a.Detail); r != "" {
			return fmt.Sprintf("usage limit reached (pane %s: %s). Resets %s: Claude Code resumes panes that show a reset time on its own shortly after. "+
				"Run %s for any pane still stopped after that.", pane, detail, r, nudgeHint)
		}
		return fmt.Sprintf("usage limit reached (pane %s: %s). No reset time given, so Claude Code will not retry: "+
			"once the limit clears, run %s to resume every stopped pane.", pane, detail, nudgeHint)
	}
}

// tmuxDisplayAll shows msg on every client attached to the pane's tmux
// server until a key is pressed. The operator sees it in whatever
// window they are in, even when the coordinator is stopped too.
func tmuxDisplayAll(msg string) error {
	out, err := exec.Command("tmux", "list-clients", "-F", "#{client_name}").Output()
	if err != nil {
		return nil
	}
	msg = strings.ReplaceAll(msg, "#", "##")
	var errs []error
	for _, c := range strings.Fields(string(out)) {
		if err := exec.Command("tmux", "display-message", "-c", c, "-d", "0", msg).Run(); err != nil {
			errs = append(errs, fmt.Errorf("tmux display-message -c %s: %w", c, err))
		}
	}
	return errors.Join(errs...)
}

func tmuxPaneSession(paneID string) string {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", paneID, "#{session_name}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
