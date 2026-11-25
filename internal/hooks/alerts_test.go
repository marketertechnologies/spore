package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type alertEnv struct {
	coordState string
	shown      *[]string
}

func setupAlerts(t *testing.T, project string) alertEnv {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	coord := t.TempDir()
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", coord)
	t.Setenv("WT_PROJECT", project)
	t.Setenv("SPORE_TASK_INBOX", "")
	t.Setenv("TMUX_PANE", "")
	var shown []string
	prevD, prevS, prevN := displayToOperator, paneSession, alertNow
	displayToOperator = func(msg string) error { shown = append(shown, msg); return nil }
	paneSession = func(id string) string {
		return "spore/" + os.Getenv("WT_PROJECT") + "/pane" + strings.TrimPrefix(id, "%")
	}
	alertNow = time.Now
	t.Cleanup(func() { displayToOperator, paneSession, alertNow = prevD, prevS, prevN })
	return alertEnv{coordState: coord, shown: &shown}
}

func coordinatorNotices(t *testing.T, coordState, project string) []string {
	t.Helper()
	var out []string
	for _, f := range pendingJSON(t, filepath.Join(coordState, project, "inbox")) {
		_, source, body, err := readTellFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, source+": "+body)
	}
	return out
}

const limitBanner = "You've hit your limit · resets 2:15pm (Europe/Oslo)"

func TestStopFailure_UsageLimitAlertsOncePerIncident(t *testing.T) {
	env := setupAlerts(t, "proj")
	for _, pane := range []string{"%1", "%2", "%3"} {
		t.Setenv("TMUX_PANE", pane)
		if err := StopFailure(StopFailureInput{Error: "rate_limit", LastAssistantMessage: limitBanner}); err != nil {
			t.Fatal(err)
		}
	}
	notices := coordinatorNotices(t, env.coordState, "proj")
	if len(notices) != 1 {
		t.Fatalf("coordinator notices: %d, want 1: %v", len(notices), notices)
	}
	if !strings.HasPrefix(notices[0], "spore-alert: usage limit reached (pane spore/proj/pane1") || !strings.Contains(notices[0], "Resets 2:15pm (Europe/Oslo)") {
		t.Errorf("notice: %q", notices[0])
	}
	if len(*env.shown) != 1 || !strings.Contains((*env.shown)[0], "usage limit reached") {
		t.Errorf("operator display: %v", *env.shown)
	}
	panes, err := FailedPanes()
	if err != nil || len(panes) != 3 {
		t.Fatalf("failed panes: %v %v", panes, err)
	}
	if active := ActiveAlerts(); len(active) != 1 || !strings.Contains(active[0], "usage limit") {
		t.Errorf("active alerts: %v", active)
	}
}

func TestStopFailure_PlainRateLimitSaysNoReset(t *testing.T) {
	env := setupAlerts(t, "proj")
	msg := "API Error: Server is temporarily limiting requests (not your usage limit)"
	if err := StopFailure(StopFailureInput{Error: "rate_limit", LastAssistantMessage: msg}); err != nil {
		t.Fatal(err)
	}
	n := coordinatorNotices(t, env.coordState, "proj")
	if len(n) != 1 || !strings.Contains(n[0], "No reset time given") || !strings.Contains(n[0], "spore fleet nudge") {
		t.Errorf("notice: %v", n)
	}
}

func TestStopFailure_LoginAlertCarriesRunbookAndRequeuesInbox(t *testing.T) {
	env := setupAlerts(t, "proj")
	inbox := filepath.Join(t.TempDir(), "inbox")
	if err := ensureInbox(inbox); err != nil {
		t.Fatal(err)
	}
	writeTell(t, filepath.Join(inbox, "100.json"), `{"body":"P1M-TELL"}`)
	if _, err := claimInbox(inbox, "inflight"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPORE_TASK_INBOX", inbox)
	t.Setenv("TMUX_PANE", "%7")

	err := StopFailure(StopFailureInput{Error: "authentication_failed",
		LastAssistantMessage: "Failed to authenticate. API Error: 401 OAuth token has expired."})
	if err != nil {
		t.Fatal(err)
	}
	n := coordinatorNotices(t, env.coordState, "proj")
	if len(n) != 1 || !strings.Contains(n[0], "login expired") || !strings.Contains(n[0], "/login") || !strings.Contains(n[0], "spore fleet nudge") {
		t.Errorf("notice: %v", n)
	}
	if got := pendingJSON(t, inbox); len(got) != 1 || filepath.Base(got[0]) != "redeliver-100.json" {
		t.Errorf("inbox after failed turn: %v", got)
	}
	panes, _ := FailedPanes()
	if len(panes) != 1 || panes[0].Kind != AlertLogin || panes[0].PaneID != "%7" || panes[0].Session != "spore/proj/pane7" {
		t.Errorf("failed panes: %+v", panes)
	}
}

func TestStopFailure_OtherErrorsRequeueWithoutAlert(t *testing.T) {
	env := setupAlerts(t, "proj")
	inbox := filepath.Join(t.TempDir(), "inbox")
	if err := ensureInbox(inbox); err != nil {
		t.Fatal(err)
	}
	writeTell(t, filepath.Join(inbox, "100.json"), `{"body":"x"}`)
	if _, err := claimInbox(inbox, "inflight"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPORE_TASK_INBOX", inbox)
	if err := StopFailure(StopFailureInput{Error: "server_error"}); err != nil {
		t.Fatal(err)
	}
	if got := pendingJSON(t, inbox); len(got) != 1 {
		t.Errorf("not requeued: %v", got)
	}
	if n := coordinatorNotices(t, env.coordState, "proj"); len(n) != 0 || len(*env.shown) != 0 {
		t.Errorf("unexpected alert: %v %v", n, *env.shown)
	}
}

func TestStopFailure_NoopOutsideSpore(t *testing.T) {
	env := setupAlerts(t, "")
	if err := StopFailure(StopFailureInput{Error: "rate_limit"}); err != nil {
		t.Fatal(err)
	}
	if len(*env.shown) != 0 {
		t.Errorf("alerted outside a spore pane: %v", *env.shown)
	}
	if _, err := os.Stat(AlertsDir()); !os.IsNotExist(err) {
		t.Errorf("alerts dir created outside a spore pane: %v", err)
	}
}

// Each project's coordinator hears about the incident; the operator
// hears once per host.
func TestStopFailure_SecondProjectNotifiesItsCoordinatorOnly(t *testing.T) {
	env := setupAlerts(t, "a")
	if err := StopFailure(StopFailureInput{Error: "rate_limit", LastAssistantMessage: limitBanner}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_PROJECT", "b")
	if err := StopFailure(StopFailureInput{Error: "rate_limit", LastAssistantMessage: limitBanner}); err != nil {
		t.Fatal(err)
	}
	if len(coordinatorNotices(t, env.coordState, "a")) != 1 || len(coordinatorNotices(t, env.coordState, "b")) != 1 {
		t.Error("each project coordinator should get exactly one notice")
	}
	if len(*env.shown) != 1 {
		t.Errorf("operator display: %d, want 1", len(*env.shown))
	}
}

func TestClearAlerts_ReleasesClaimsAfterCooldown(t *testing.T) {
	env := setupAlerts(t, "proj")
	t.Setenv("TMUX_PANE", "%1")
	raise := func() {
		t.Helper()
		if err := StopFailure(StopFailureInput{Error: "rate_limit"}); err != nil {
			t.Fatal(err)
		}
	}
	raise()

	if err := ClearAlerts(); err != nil {
		t.Fatal(err)
	}
	if panes, _ := FailedPanes(); len(panes) != 0 {
		t.Errorf("own failed record kept after a successful turn: %+v", panes)
	}
	raise()
	if len(*env.shown) != 1 {
		t.Fatalf("re-alerted inside the cooldown: %v", *env.shown)
	}

	alertNow = func() time.Time { return time.Now().Add(alertCooldown + time.Minute) }
	if err := ClearAlerts(); err != nil {
		t.Fatal(err)
	}
	if active := ActiveAlerts(); len(active) != 0 {
		t.Errorf("claims kept past the cooldown: %v", active)
	}
	raise()
	if len(*env.shown) != 2 {
		t.Errorf("new incident not alerted: %v", *env.shown)
	}
	if n := coordinatorNotices(t, env.coordState, "proj"); len(n) != 2 {
		t.Errorf("coordinator notices: %d, want 2", len(n))
	}
}

func TestResetTime(t *testing.T) {
	for in, want := range map[string]string{
		limitBanner: "2:15pm (Europe/Oslo)",
		"Usage limit reached - limit resets 2:15pm":                  "2:15pm",
		"API Error: Request rejected (429) · rate limit. Try later.": "",
	} {
		if got := resetTime(in); got != want {
			t.Errorf("resetTime(%q) = %q, want %q", in, got, want)
		}
	}
}
