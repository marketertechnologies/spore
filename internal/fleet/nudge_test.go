package fleet

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/versality/spore/internal/hooks"
)

type nudgeStub struct {
	panes    map[string]string
	sessions []string
	screens  map[string]string
	sent     []string
	noPrompt map[string]bool
}

func stubNudge(t *testing.T, s *nudgeStub) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	prevP, prevS, prevC, prevSend := nudgePaneSession, nudgeSessions, nudgeCapture, nudgeSend
	nudgePaneSession = func(id string) (string, bool) { v, ok := s.panes[id]; return v, ok }
	nudgeSessions = func() ([]string, error) { return s.sessions, nil }
	nudgeCapture = func(name string) (string, error) {
		if v, ok := s.screens[name]; ok {
			return v, nil
		}
		return "", errors.New("no such session")
	}
	nudgeSend = func(target, msg string) (bool, error) {
		if s.noPrompt[target] {
			return false, nil
		}
		s.sent = append(s.sent, target+": "+msg)
		return true, nil
	}
	t.Cleanup(func() { nudgePaneSession, nudgeSessions, nudgeCapture, nudgeSend = prevP, prevS, prevC, prevSend })
}

func record(t *testing.T, id, session string) {
	t.Helper()
	err := hooks.RecordFailedPane(hooks.FailedPane{Kind: hooks.AlertLogin, PaneID: id, Session: session, Project: "p", Ts: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
}

const idleScreen = "previous output\n\n────────\n❯ \n────────\n  status line"

func loggedOutScreen() string {
	return "earlier work\n  Login expired \u00b7 Please run /login\n\n────────\n❯ \n────────\n  status line"
}

// P1.3: after one /login, a single command resumes every stopped pane:
// recorded panes, plus panes without the hook whose screen shows the
// error. Healthy panes, panes still retrying and the operator's pilot
// session are left alone.
func TestNudge_ResumesEveryStoppedPane(t *testing.T) {
	s := &nudgeStub{
		panes: map[string]string{"%1": "spore/p/coordinator", "%2": "spore-role/p/t/engineer"},
		sessions: []string{
			"spore/p/coordinator", "spore-role/p/t/engineer", "spore/p/w1", "spore/p/healthy",
			"spore/p/retrying", "spore/pilot/op", "other",
		},
		screens: map[string]string{
			"spore/p/w1":       loggedOutScreen(),
			"spore/p/healthy":  idleScreen,
			"spore/p/retrying": "  API Error: 401 OAuth token has expired \u00b7 Retrying in 17s\n────────\n❯ \n────────",
			"spore/pilot/op":   loggedOutScreen(),
			"other":            loggedOutScreen(),
		},
	}
	stubNudge(t, s)
	record(t, "%1", "spore/p/coordinator")
	record(t, "%2", "spore-role/p/t/engineer")
	record(t, "%9", "spore/p/gone")

	res, err := Nudge(false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"spore/p/coordinator", "spore-role/p/t/engineer", "spore/p/w1"}
	if strings.Join(res.Nudged, ",") != strings.Join(want, ",") {
		t.Errorf("nudged %v, want %v", res.Nudged, want)
	}
	if len(s.sent) != 3 || !strings.HasPrefix(s.sent[0], "%1: ") || !strings.HasPrefix(s.sent[2], "spore/p/w1: ") {
		t.Errorf("sent: %v", s.sent)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "gone") {
		t.Errorf("skipped: %v", res.Skipped)
	}
	left, err := hooks.FailedPanes()
	if err != nil || len(left) != 0 {
		t.Errorf("records left after nudge: %+v %v", left, err)
	}
}

func TestNudge_KeepsRecordWhenPaneHasNoPrompt(t *testing.T) {
	s := &nudgeStub{panes: map[string]string{"%1": "spore/p/w1"}, noPrompt: map[string]bool{"%1": true}}
	stubNudge(t, s)
	record(t, "%1", "spore/p/w1")
	res, err := Nudge(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nudged) != 0 || len(res.Skipped) != 1 {
		t.Errorf("result: %+v", res)
	}
	if left, _ := hooks.FailedPanes(); len(left) != 1 {
		t.Errorf("record dropped although the nudge was not delivered: %+v", left)
	}
}

func TestNudge_DryRunSendsNothing(t *testing.T) {
	s := &nudgeStub{panes: map[string]string{"%1": "spore/p/w1"}}
	stubNudge(t, s)
	record(t, "%1", "spore/p/w1")
	res, err := Nudge(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nudged) != 1 || len(s.sent) != 0 {
		t.Errorf("dry run: %+v sent=%v", res, s.sent)
	}
	if left, _ := hooks.FailedPanes(); len(left) != 1 {
		t.Error("dry run dropped the record")
	}
}
