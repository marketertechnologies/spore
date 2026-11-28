package fleet

import (
	"strings"
	"testing"

	"github.com/versality/spore/internal/task"
)

type tellStub struct {
	live      map[string]bool
	delivered bool
	sent      []string
	sentTo    []string
}

func stubTell(t *testing.T, s *tellStub) {
	t.Helper()
	prevHas, prevSend := tellHasSession, tellSend
	tellHasSession = func(name string) bool { return s.live[name] }
	tellSend = func(session, msg string) (bool, error) {
		s.sentTo = append(s.sentTo, session)
		s.sent = append(s.sent, msg)
		return s.delivered, nil
	}
	t.Cleanup(func() { tellHasSession, tellSend = prevHas, prevSend })
}

func tellFixture(t *testing.T) (root, slug string) {
	t.Helper()
	root = newGitRepoFor(t, "demo-project")
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", t.TempDir())
	slug = "demo"
	if err := task.EnsureLegacyMarker(root, slug); err != nil {
		t.Fatal(err)
	}
	return root, slug
}

func TestTellRoleReachesEngineer(t *testing.T) {
	root, slug := tellFixture(t)
	eng, _ := EngineerSpec(root, slug).SessionName()
	s := &tellStub{live: map[string]bool{eng: true}, delivered: true}
	stubTell(t, s)

	got, err := TellRole(root, slug, "use the v2 API")
	if err != nil {
		t.Fatalf("TellRole: %v", err)
	}
	if got != eng || len(s.sentTo) != 1 || s.sentTo[0] != eng {
		t.Fatalf("delivered to %q (sends %v), want %q", got, s.sentTo, eng)
	}
	if !strings.Contains(s.sent[0], "use the v2 API") {
		t.Errorf("sent %q, want the message body", s.sent[0])
	}
}

func TestTellRoleReachesActiveReviewer(t *testing.T) {
	root, slug := tellFixture(t)
	if err := task.WriteEngineerResponse(root, slug, 1, task.EngineerResponse{}); err != nil {
		t.Fatal(err)
	}
	eng, _ := EngineerSpec(root, slug).SessionName()
	rev, _ := ReviewerSpec(root, slug, task.ReviewerA).SessionName()
	s := &tellStub{live: map[string]bool{eng: true, rev: true}, delivered: true}
	stubTell(t, s)

	got, err := TellRole(root, slug, "focus on the parser")
	if err != nil {
		t.Fatalf("TellRole: %v", err)
	}
	if got != rev {
		t.Fatalf("delivered to %q, want reviewer %q", got, rev)
	}
}

func TestTellRoleErrorsWithoutPane(t *testing.T) {
	root, slug := tellFixture(t)
	s := &tellStub{live: map[string]bool{}, delivered: true}
	stubTell(t, s)

	_, err := TellRole(root, slug, "hello")
	if err == nil || !strings.Contains(err.Error(), "not delivered") {
		t.Fatalf("err = %v, want a not-delivered error", err)
	}
	if len(s.sent) != 0 {
		t.Errorf("sent %v to a missing pane", s.sent)
	}
}

func TestTellRoleErrorsWhenPromptNotUp(t *testing.T) {
	root, slug := tellFixture(t)
	eng, _ := EngineerSpec(root, slug).SessionName()
	s := &tellStub{live: map[string]bool{eng: true}, delivered: false}
	stubTell(t, s)

	if _, err := TellRole(root, slug, "hello"); err == nil || !strings.Contains(err.Error(), "not delivered") {
		t.Fatalf("err = %v, want a not-delivered error", err)
	}
}

func TestTellRoleFlattensAndLeadsWithText(t *testing.T) {
	root, slug := tellFixture(t)
	eng, _ := EngineerSpec(root, slug).SessionName()
	s := &tellStub{live: map[string]bool{eng: true}, delivered: true}
	stubTell(t, s)

	if _, err := TellRole(root, slug, "line one\nline two;"); err != nil {
		t.Fatalf("TellRole: %v", err)
	}
	want := "line one line two;" + tellSuffix
	if s.sent[0] != want {
		t.Errorf("sent %q, want %q", s.sent[0], want)
	}
}

func TestTellRoleFallsBackToEngineerWhenReviewerDown(t *testing.T) {
	root, slug := tellFixture(t)
	if err := task.WriteEngineerResponse(root, slug, 1, task.EngineerResponse{}); err != nil {
		t.Fatal(err)
	}
	eng, _ := EngineerSpec(root, slug).SessionName()
	s := &tellStub{live: map[string]bool{eng: true}, delivered: true}
	stubTell(t, s)

	got, err := TellRole(root, slug, "hello")
	if err != nil {
		t.Fatalf("TellRole: %v", err)
	}
	if got != eng {
		t.Fatalf("delivered to %q, want engineer %q", got, eng)
	}
}

func TestTellRoleRefusesWhileDriveHoldsLock(t *testing.T) {
	root, slug := tellFixture(t)
	eng, _ := EngineerSpec(root, slug).SessionName()
	s := &tellStub{live: map[string]bool{eng: true}, delivered: true}
	stubTell(t, s)
	prev := tellLockWait
	tellLockWait = 0
	t.Cleanup(func() { tellLockWait = prev })

	unlock, err := lockRoleDrive(root, slug)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := TellRole(root, slug, "hello"); err == nil || !strings.Contains(err.Error(), "retry") {
		t.Fatalf("err = %v, want a retry refusal", err)
	}
	if len(s.sent) != 0 {
		t.Errorf("sent %v while the drive held the lock", s.sent)
	}
}
