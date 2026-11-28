package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/versality/spore/internal/events"
	"github.com/versality/spore/internal/report"
)

func stubReport(t *testing.T, now time.Time) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	pl, pf, pa, pn := reportLive, reportFleetEnabled, reportAlerts, reportNow
	t.Cleanup(func() { reportLive, reportFleetEnabled, reportAlerts, reportNow = pl, pf, pa, pn })
	reportLive = func(root string) *report.Live {
		if root == "" {
			return &report.Live{Err: "no root"}
		}
		return &report.Live{TasksByStatus: map[string][]string{"active": {"a"}}, Coordinator: true}
	}
	reportFleetEnabled = func() (bool, error) { return true, nil }
	reportAlerts = func() []string { return nil }
	reportNow = func() time.Time { return now }
}

func TestReportSinceAndAll(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	stubReport(t, now)
	_ = events.Append("one", events.Event{TS: now.Add(-30 * time.Hour), Kind: events.TaskDone, Slug: "old"})
	_ = events.Append("one", events.Event{TS: now.Add(-2 * time.Hour), Kind: events.TaskDone, Slug: "fresh"})
	_ = events.Append("two", events.Event{TS: now.Add(-1 * time.Hour), Kind: events.PROpened, Slug: "p", Fields: map[string]string{"url": "https://github.com/o/r/pull/3"}})
	events.NoteRoot("one", "/srv/one")

	var b bytes.Buffer
	if err := runReport([]string{"--all"}, &b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"== one", "== two", "done (1): fresh", "pull/3 (p)", "tasks: 1 active", "tasks: unknown (no root)", "fleet enabled"} {
		if !strings.Contains(out, want) {
			t.Errorf("--all lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "old") {
		t.Errorf("event outside the 12h window shown:\n%s", out)
	}

	b.Reset()
	if err := runReport([]string{"--project", "one", "--since", "2d", "--full"}, &b); err != nil {
		t.Fatal(err)
	}
	if out := b.String(); !strings.Contains(out, "old") || strings.Contains(out, "== two") || !strings.Contains(out, "(48h)") {
		t.Errorf("--project one --since 2d:\n%s", out)
	}
}

func TestReportRejectsBadFlags(t *testing.T) {
	stubReport(t, time.Now())
	for _, args := range [][]string{{"--since", "soon"}, {"--all", "--project", "x"}, {"extra"}} {
		if err := runReport(args, &bytes.Buffer{}); err == nil {
			t.Errorf("runReport(%v) accepted", args)
		}
	}
}
