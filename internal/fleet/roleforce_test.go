package fleet

import (
	"encoding/json"
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

// escalateAWithFix walks A to an escalation at round 3 and lands the
// engineer's fix pass (round 4), the state force-approve accepts.
func escalateAWithFix(t *testing.T, root, slug string) {
	t.Helper()
	escalateA(t, root, slug)
	markEscalatedA(t, root, slug)
	writeEng(4)(t, root, slug)
}

// Editing the final verdict to approve while its high/medium comments
// stay used to be bounced by the round rule and silently restart A.
func TestForceApproveKeepsCommentsAndMovesOn(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	escalateAWithFix(t, root, slug)

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
	if snap.Phase != PhaseReviewB || !snap.AFixLanded {
		t.Fatalf("snap = %+v, want %s with the fix pass counted", snap, PhaseReviewB)
	}
	if _, err := os.Stat(task.ReviewPath(root, slug, task.ReviewerA, 3) + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
	if _, err := ForceApproveReview(root, tasksDir, slug, task.ReviewerA, "again"); err == nil {
		t.Fatal("force-approve of an approve succeeded")
	}
}

// Forcing a verdict the engineer already answered (or is answering)
// would shift the engineer-round pairing onto B's verdicts.
func TestForceApproveRefusedOutsideEscalation(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	writeEng(1)(t, root, slug)
	writeGraded(task.ReviewerA, 1, task.VerdictRequestChanges, highBug)(t, root, slug)
	path := task.ReviewPath(root, slug, task.ReviewerA, 1)
	before := mustRead(t, path)
	for _, step := range []stepFn{nil, writeEng(2)} {
		if step != nil {
			step(t, root, slug)
		}
		if _, err := ForceApproveReview(root, tasksDir, slug, task.ReviewerA, "ok"); err == nil || !strings.Contains(err.Error(), "escalated final verdict") {
			t.Fatalf("err = %v, want an escalation-only refusal", err)
		}
		if mustRead(t, path) != before {
			t.Fatal("verdict changed by a refused force-approve")
		}
	}
}

// With the fix pass still pending, B's round 1 could land first and the
// mtime check would then pair the late fix pass with B.
func TestForceApproveRefusedWhileFixPending(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	escalateA(t, root, slug)
	markEscalatedA(t, root, slug)
	if _, err := ForceApproveReview(root, tasksDir, slug, task.ReviewerA, "ok"); err == nil || !strings.Contains(err.Error(), "engineer-round-4.json") {
		t.Fatalf("err = %v, want a fix-pending refusal", err)
	}
	writeEng(4)(t, root, slug)
	if _, err := ForceApproveReview(root, tasksDir, slug, task.ReviewerA, "ok"); err != nil {
		t.Fatalf("after the fix pass: %v", err)
	}
}

func TestForceApproveRefusedWhileDriveHoldsLoop(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	escalateAWithFix(t, root, slug)
	unlock, err := lockRoleDrive(root, slug)
	if err != nil {
		t.Fatal(err)
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

// A reviewer that writes operator_override itself must not approve past
// its own high/medium comments.
func TestReviewerWrittenOverrideNotCounted(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	setupRoleLoopDriveFixture(t, root, slug)
	escalateAWithFix(t, root, slug)
	path := task.ReviewPath(root, slug, task.ReviewerA, 3)
	var doc map[string]any
	if err := json.Unmarshal([]byte(mustRead(t, path)), &doc); err != nil {
		t.Fatal(err)
	}
	doc["verdict"], doc["operator_override"] = task.VerdictApprove, "self-approved"
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := task.ReadReview(root, slug, task.ReviewerA, 3); err == nil || !strings.Contains(err.Error(), "force-approve") {
		t.Fatalf("err = %v, want the self-written override rejected", err)
	}
	snap, err := DeriveSnapshot(root, slug)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Phase == PhaseReviewB {
		t.Fatal("a reviewer-written override moved the loop to review B")
	}
}

// Fix pass posted while A3 was request_changes, then the operator
// force-approves A3: the engineer round answering B1 is round 5, and
// the mirror must post it after B1. The order must hold after the
// Done tick clears the escalation markers.
func TestMirrorEventsForceApproveAfterFixPass(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	escalateAWithFix(t, root, slug)
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
	for _, e := range evs {
		if e.kind == eventOverride && e.note != "accepted" {
			t.Fatalf("override note = %q, want the reason captured on the event", e.note)
		}
	}

	writeGraded(task.ReviewerB, 2, task.VerdictApprove)(t, root, slug)
	if err := clearEscalationMarkers(root, slug); err != nil {
		t.Fatal(err)
	}
	snap, _ = DeriveSnapshot(root, slug)
	if snap.Phase != PhaseDone {
		t.Fatalf("phase = %s, want %s", snap.Phase, PhaseDone)
	}
	evs, err = mirrorEvents(root, slug, snap)
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, "B/round-2.json", eventReady)
	if got := eventNames(evs); !reflect.DeepEqual(got, want) {
		t.Fatalf("after Done cleared the markers:\nevents = %v\nwant     %v", got, want)
	}
}

func TestDriveRoleLoopSkipsWhileLocked(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", t.TempDir())
	slug := "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	panes := stubPanes(t)
	unlock, err := lockRoleDrive(root, slug)
	if err != nil {
		t.Fatal(err)
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

// A drive.lock that cannot be opened is an error, not a held lock: a
// silent report-only tick would stall the loop forever.
func TestDriveRoleLoopLockErrorSurfaces(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", t.TempDir())
	slug := "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	stubPanes(t)
	if err := os.MkdirAll(filepath.Join(task.RoleTaskDir(root, slug), "drive.lock"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := DriveRoleLoop(root, tasksDir, slug); err == nil {
		t.Fatal("drive with an unopenable lock succeeded")
	}
	if _, err := ForceApproveReview(root, tasksDir, slug, task.ReviewerA, "ok"); err == nil || strings.Contains(err.Error(), "retry") {
		t.Fatalf("err = %v, want the lock error, not a retry", err)
	}
}

func TestGitHubMirrorPostsOverrideReason(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	escalateA(t, f.root, f.slug)
	writeEng(4)(t, f.root, f.slug)
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

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// escalateBWithFix walks B to an escalation at round 3 and lands the
// engineer's fix pass (round 4).
func escalateBWithFix(t *testing.T, root, slug string) {
	t.Helper()
	for _, step := range []stepFn{
		writeEng(1),
		writeGraded(task.ReviewerA, 1, task.VerdictApprove),
		writeGraded(task.ReviewerB, 1, task.VerdictRequestChanges, highBug),
		writeEng(2),
		writeGraded(task.ReviewerB, 2, task.VerdictRequestChanges, medEdge),
		writeEng(3),
		writeGraded(task.ReviewerB, 3, task.VerdictRequestChanges, medEdge),
		writeEng(4),
	} {
		step(t, root, slug)
	}
}

// B's fix pass must stay in the mirror after B is forced, and the
// forced verdict must not read as a plain approve.
func TestMirrorForceApproveBKeepsFixPass(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	escalateBWithFix(t, root, slug)
	if _, err := ForceApproveReview(root, tasksDir, slug, task.ReviewerB, "ship it"); err != nil {
		t.Fatal(err)
	}
	snap, _ := DeriveSnapshot(root, slug)
	if snap.Phase != PhaseDone {
		t.Fatalf("phase = %s, want %s", snap.Phase, PhaseDone)
	}
	evs, err := mirrorEvents(root, slug, snap)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"engineer-round-1.json", "A/round-1.json", "B/round-1.json", "engineer-round-2.json",
		"B/round-2.json", "engineer-round-3.json", "B/round-3.json", "override-B-3",
		"engineer-round-4.json", eventReady,
	}
	if got := eventNames(evs); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v\nwant     %v", got, want)
	}
	rev, err := task.ReadReview(root, slug, task.ReviewerB, 3)
	if err != nil {
		t.Fatal(err)
	}
	if body := reviewBody(task.ReviewerB, 3, rev); !strings.Contains(body, "force-approved by the operator") {
		t.Fatalf("forced verdict body hides the override:\n%s", body)
	}
	if line := reviewOutcomeLine(root, slug); !strings.Contains(line, "force-approved by the operator at round 3") {
		t.Fatalf("outcome line = %q, want the override shown", line)
	}
}

// A failed write must not leave the override recorded: the live
// reviewer could then write a self-approval that validates.
func TestForceApproveFailedWriteDropsRecord(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	escalateAWithFix(t, root, slug)
	dir := filepath.Dir(task.ReviewPath(root, slug, task.ReviewerA, 3))
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if _, err := ForceApproveReview(root, tasksDir, slug, task.ReviewerA, "ok"); err == nil {
		t.Fatal("force-approve into a read-only dir succeeded")
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := task.ReviewPath(root, slug, task.ReviewerA, 3)
	var doc map[string]any
	if err := json.Unmarshal([]byte(mustRead(t, path)), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["verdict"] != task.VerdictRequestChanges {
		t.Fatalf("verdict = %v after a failed write", doc["verdict"])
	}
	doc["verdict"], doc["operator_override"] = task.VerdictApprove, "self-approved"
	b, _ := json.Marshal(doc)
	if _, err := task.ParseReviewAt(root, slug, task.ReviewerA, 3, b); err == nil {
		t.Fatal("override record survived a failed force-approve")
	}
}

// After a force-approve the loop is no longer escalated, the summary
// shows the override, and the still-live reviewer cannot replace the
// forced file with its own override.
func TestForceApproveClearsEscalationAndBindsContent(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	escalateAWithFix(t, root, slug)
	if _, err := ForceApproveReview(root, tasksDir, slug, task.ReviewerA, "edge case accepted"); err != nil {
		t.Fatal(err)
	}
	if esc, err := task.IsEscalated(root, slug); err != nil || esc {
		t.Fatalf("IsEscalated = %v, %v after force-approve; want false", esc, err)
	}
	if snap, _ := DeriveSnapshot(root, slug); snap.Phase != PhaseReviewB || !snap.AFixLanded {
		t.Fatalf("snap = %+v, want %s with the fix pass counted", snap, PhaseReviewB)
	}
	sum, err := buildSummary(root, slug, "fp")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sum, "### round 3 -> approve (request changes, force-approved by the operator)\n\nOperator override: edge case accepted") {
		t.Fatalf("summary hides the override:\n%s", sum)
	}

	path := task.ReviewPath(root, slug, task.ReviewerA, 3)
	var doc map[string]any
	if err := json.Unmarshal([]byte(mustRead(t, path)), &doc); err != nil {
		t.Fatal(err)
	}
	doc["operator_override"], doc["comments"] = "reviewer rewrite", []any{}
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := task.ReadReview(root, slug, task.ReviewerA, 3); err == nil {
		t.Fatal("a reviewer rewrite of the forced file validated")
	}
}
