package fleet

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/versality/spore/internal/task"
)

func markEscalatedA(t *testing.T, root, slug string) {
	t.Helper()
	stateDir := filepath.Join(task.RoleTaskDir(root, slug), "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "escalated-A-3"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Editing the final verdict to approve while its high/medium comments
// stay used to be bounced by the round rule and silently restart A.
func TestForceApproveKeepsCommentsAndMovesOn(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	escalateA(t, root, slug)
	markEscalatedA(t, root, slug)

	plain := filepath.Join(task.RoleTaskDir(root, slug), "plain.json")
	if err := os.WriteFile(plain, []byte(strings.Replace(mustRead(t, task.ReviewPath(root, slug, task.ReviewerA, 3)), `"request_changes"`, `"approve"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := task.ParseReview(plain, []byte(mustRead(t, plain)), task.ReviewerA, 3, false); err == nil {
		t.Fatal("a hand-flipped approve with a medium still validates; the override field would be pointless")
	}

	if _, err := ForceApproveReview(root, tasksDir, slug, task.ReviewerA, ""); err == nil {
		t.Fatal("force-approve without a reason succeeded")
	}
	round, err := ForceApproveReview(root, tasksDir, slug, task.ReviewerA, "edge case accepted for now")
	if err != nil {
		t.Fatal(err)
	}
	if round != 3 {
		t.Fatalf("round = %d, want 3", round)
	}
	rev, err := task.ReadReview(root, slug, task.ReviewerA, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !rev.Forced() || len(rev.Comments) != 2 {
		t.Fatalf("review = %+v, want forced approve with both comments kept", rev)
	}
	snap, err := DeriveSnapshot(root, slug)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Phase != PhaseReviewB {
		t.Fatalf("phase = %s, want %s", snap.Phase, PhaseReviewB)
	}
	if _, err := ForceApproveReview(root, tasksDir, slug, task.ReviewerA, "again"); err == nil {
		t.Fatal("force-approve of an approve succeeded")
	}
}

func TestForceApproveRefusedWhileDriveHoldsLoop(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	escalateA(t, root, slug)
	unlock, ok := lockRoleDrive(root, slug)
	if !ok {
		t.Fatal("lock")
	}
	defer unlock()
	if _, err := ForceApproveReview(root, tasksDir, slug, task.ReviewerA, "ok"); err == nil || !strings.Contains(err.Error(), "retry") {
		t.Fatalf("err = %v, want a retry refusal", err)
	}
	rev, _ := task.ReadReview(root, slug, task.ReviewerA, 3)
	if rev.Verdict != task.VerdictRequestChanges {
		t.Fatalf("verdict changed under a held lock: %s", rev.Verdict)
	}
}

// Fix pass posted while A3 was request_changes, then the operator
// force-approves A3: the engineer round answering B1 is round 5, and
// the mirror must post it after B1.
func TestMirrorEventsForceApproveAfterFixPass(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	escalateA(t, root, slug)
	markEscalatedA(t, root, slug)
	writeEng(4)(t, root, slug)
	if _, err := ForceApproveReview(root, tasksDir, slug, task.ReviewerA, "accepted"); err != nil {
		t.Fatal(err)
	}
	writeGraded(task.ReviewerB, 1, task.VerdictRequestChanges, lowNit)(t, root, slug)
	writeEng(5)(t, root, slug)
	snap, _ := DeriveSnapshot(root, slug)
	evs, err := mirrorEvents(root, slug, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"engineer-round-1.json", "A/round-1.json", "engineer-round-2.json", "A/round-2.json",
		"engineer-round-3.json", "A/round-3.json", "override-A-3", "engineer-round-4.json",
		"B/round-1.json", "engineer-round-5.json",
	}
	if got := eventNames(evs); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v\nwant     %v", got, want)
	}
}

func TestDriveRoleLoopSkipsWhileLocked(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", t.TempDir())
	slug := "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	panes := stubPanes(t)
	unlock, ok := lockRoleDrive(root, slug)
	if !ok {
		t.Fatal("lock")
	}
	snap, err := DriveRoleLoop(root, tasksDir, slug)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Phase != PhaseEngineerInitial || len(panes.spawns) != 0 {
		t.Fatalf("snap = %+v spawns = %v, want a report-only tick", snap, panes.spawns)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestForceApproveInvalidResultLeavesFile(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	writeEng(1)(t, root, slug)
	path := task.ReviewPath(root, slug, task.ReviewerA, 1)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{"author":"A","verdict":"request_changes","summary":"s","comments":[{"severity":"high","category":"correctness","text":"x"}]}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ForceApproveReview(root, tasksDir, slug, task.ReviewerA, "ok"); err == nil || !strings.Contains(err.Error(), "left as it was") {
		t.Fatalf("err = %v, want a validation refusal", err)
	}
	if got := mustRead(t, path); got != raw {
		t.Fatalf("file changed after a refused override:\n%s", got)
	}
}

func TestGitHubMirrorPostsOverrideReason(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	escalateA(t, f.root, f.slug)
	for range 2 {
		f.tick(t)
	}
	if _, err := ForceApproveReview(f.root, f.tasksDir, f.slug, task.ReviewerA, "edge case accepted for now"); err != nil {
		t.Fatal(err)
	}
	f.tick(t)
	if !strings.Contains(f.log(t), "round 3).**\n\nedge case accepted for now") || !strings.Contains(f.log(t), "**Operator force-approved") {
		t.Fatalf("override comment missing:\n%s", f.log(t))
	}
	if s := f.state(t); !s.posted("override-A-3") {
		t.Fatal("override event not recorded as posted")
	}
}
