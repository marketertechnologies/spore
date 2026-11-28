package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/versality/spore/internal/events"
)

func TestRenderDigest(t *testing.T) {
	now := time.Date(2026, 11, 26, 7, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return now.Add(time.Duration(-h) * time.Hour) }
	evs := []events.Event{
		{TS: at(11), Kind: events.TaskStart, Slug: "alpha"},
		{TS: at(10), Kind: events.WorkerSpawn, Slug: "beta"},
		{TS: at(9), Kind: events.WorkerRespawn, Slug: "beta"},
		{TS: at(9), Kind: events.WorkerCrashLoop, Slug: "beta"},
		{TS: at(8), Kind: events.Alert, Fields: map[string]string{"error": "rate_limit", "reset": "2:15pm"}},
		{TS: at(7), Kind: events.Alert, Fields: map[string]string{"error": "rate_limit"}},
		{TS: at(6), Kind: events.ReconcileFailed, Detail: "scan tasks: boom"},
		{TS: at(6), Kind: events.ReconcileSkipped, Detail: "fleet disabled"},
		{TS: at(5), Kind: events.RolePhase, Slug: "gamma", Detail: "review-A"},
		{TS: at(4), Kind: events.RoleVerdict, Slug: "gamma", Fields: map[string]string{"reviewer": "A", "round": "1", "verdict": "request_changes"}},
		{TS: at(3), Kind: events.RolePhase, Slug: "gamma", Detail: "engineer-revise-A"},
		{TS: at(2), Kind: events.PROpened, Slug: "gamma", Fields: map[string]string{"url": "https://github.com/o/r/pull/9"}},
		{TS: at(2), Kind: events.Redaction, Slug: "gamma", Fields: map[string]string{"class": "token", "where": "--body", "count": "2"}},
		{TS: at(2), Kind: events.Redaction, Slug: "gamma", Fields: map[string]string{"class": "home_path", "where": "stdin", "count": "1"}},
		{TS: at(1), Kind: events.Redaction, Fields: map[string]string{"class": "token", "where": "stdin", "count": "1"}},
		{TS: at(1), Kind: events.TaskDone, Slug: "alpha"},
	}
	on := false
	h := Header{Since: at(12), Now: now, FleetEnabled: &on, Alerts: []string{"usage limit reached"}}
	live := &Live{
		TasksByStatus: map[string][]string{"active": {"beta", "gamma"}, "blocked": {"delta"}, "draft": {"x"}},
		Workers:       []string{"beta"},
		RoleLoops:     []string{"gamma"},
		Escalated:     []string{"gamma"},
	}
	var b bytes.Buffer
	Render(&b, h, []Project{{Name: "proj", Events: evs, Live: live}}, false)
	out := b.String()
	for _, want := range []string{
		"(12h)",
		"fleet DISABLED",
		"ALERT: usage limit reached",
		"== proj",
		"tasks: 2 active, 1 blocked, 1 draft",
		"done (1): alpha",
		"started (2): alpha, beta",
		"https://github.com/o/r/pull/9 (gamma)",
		"redactions: 4 (1 home_path, 3 token)",
		"escalated role loop: gamma",
		"coordinator not running",
		"blocked: delta",
		"crash loop, respawn stopped: worker beta",
		"alert rate_limit x2",
		"reconcile failed x1",
		"scan tasks: boom",
		"reconcile skipped: fleet disabled",
		"running: worker beta, role loop gamma",
		"gamma now engineer-revise-A (A1 changes)",
		"respawns: worker 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "events:") {
		t.Errorf("default view lists every event:\n%s", out)
	}
	if n := strings.Count(out, "\n"); n > 30 {
		t.Errorf("default view is %d lines, want under one screen", n)
	}

	b.Reset()
	Render(&b, h, []Project{{Name: "proj", Events: evs, Live: live}}, true)
	if !strings.Contains(b.String(), "events:") || strings.Count(b.String(), "worker.spawn beta") != 1 {
		t.Errorf("full view lacks the event log:\n%s", b.String())
	}
}

func TestRenderEmptyAndUnknownLive(t *testing.T) {
	now := time.Now()
	var b bytes.Buffer
	Render(&b, Header{Since: now.Add(-time.Hour), Now: now}, []Project{{Name: "p", Live: &Live{Err: "no project root recorded"}}}, false)
	out := b.String()
	for _, want := range []string{"no active alerts", "tasks: unknown (no project root recorded)", "no events in the window"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "coordinator not running") {
		t.Errorf("claims the coordinator is down without live state:\n%s", out)
	}
}

func TestLineCapsNames(t *testing.T) {
	var b bytes.Buffer
	line(&b, "done", []string{"a", "b", "c", "d", "e", "f", "g", "h"})
	if got := b.String(); got != "done (8): a, b, c, d, e, f (+2 more)\n" {
		t.Errorf("line = %q", got)
	}
}
