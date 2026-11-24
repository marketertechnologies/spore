package fleet

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/versality/spore/internal/task"
)

func checkGateFixture(t *testing.T) (root, tasksDir, sha string, f *fakePanes, calls *[]wakeCall) {
	t.Helper()
	root, _ = livenessFixture(t)
	if out, err := exec.Command("git", "-C", root, "-c", "user.email=t@example.com", "-c", "user.name=t",
		"commit", "-q", "--allow-empty", "-m", "init").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	tasksDir = setupRoleLoopDriveFixture(t, root, "demo")
	if err := os.WriteFile(filepath.Join(root, "spore.toml"),
		[]byte("[fleet.roles.engineer]\ncheck = \"just check\"\ncheck_timeout_seconds = 60\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := task.HeadSHA(task.TaskWorktree(root, "demo"))
	if err != nil {
		t.Fatal(err)
	}
	f = stubPanes(t)
	f.now = time.Now()
	outcome := wakeDelivered
	calls = stubWakeRole(t, &outcome)
	return root, tasksDir, sha, f, calls
}

func touchMarker(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sessionWakes(calls []wakeCall, suffix string) []wakeCall {
	var out []wakeCall
	for _, c := range calls {
		if strings.HasSuffix(c.session, suffix) {
			out = append(out, c)
		}
	}
	return out
}

// With a check configured, reviewer A is not spawned for a response
// without a pass marker; the engineer is nudged only once the check
// timeout has passed, and a pass marker releases the reviewer.
func TestDriveHoldsReviewerUntilCheckPasses(t *testing.T) {
	root, tasksDir, sha, f, calls := checkGateFixture(t)
	writeEng(1)(t, root, "demo")
	revA, _ := ReviewerSpec(root, "demo", task.ReviewerA).SessionName()

	snap, err := DriveRoleLoop(root, tasksDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Phase != PhaseReviewA || snap.Check != task.CheckPending || snap.CheckSHA != sha {
		t.Fatalf("snap = %+v, want review-A with check pending at %s", snap, sha)
	}
	if f.panes[revA] != nil {
		t.Fatal("reviewer A spawned while the check is pending")
	}
	if len(*calls) != 0 {
		t.Fatalf("wakes inside the check timeout: %+v", *calls)
	}

	f.now = f.now.Add(60*time.Second + checkHoldGrace + time.Second)
	if _, err := DriveRoleLoop(root, tasksDir, "demo"); err != nil {
		t.Fatal(err)
	}
	eng := sessionWakes(*calls, "/demo/engineer")
	if len(eng) != 1 || !strings.Contains(eng[0].msg, "Reviewers are held") || !strings.Contains(eng[0].msg, "spore hooks engineer-check") || !strings.Contains(eng[0].msg, "up to 60s") {
		t.Fatalf("engineer wakes = %+v, want one check-held nudge", eng)
	}
	if f.panes[revA] != nil {
		t.Fatal("reviewer A spawned while the check is pending")
	}

	touchMarker(t, task.CheckPassPath(root, "demo", 1, sha))
	snap, err = DriveRoleLoop(root, tasksDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Check != task.CheckPassed || f.panes[revA] == nil {
		t.Fatalf("check = %q, reviewer A spawned = %v; want passed and spawned", snap.Check, f.panes[revA] != nil)
	}
}

// A reviewer with a verdict on record is kept alive but not woken
// while the engineer's revision is unchecked; a give-up marker hands
// off with the failure named in the wake.
func TestDriveReviewerWakeCarriesCheckResult(t *testing.T) {
	root, tasksDir, sha, f, calls := checkGateFixture(t)
	walkPhases(t, root, "demo", []phaseRow{
		{writeEng(1), PhaseReviewA, task.ReviewerA, 0},
		{writeGraded(task.ReviewerA, 1, task.VerdictRequestChanges, highBug), PhaseEngineerReviseA, task.ReviewerA, 1},
		{writeEng(2), PhaseReviewA, task.ReviewerA, 1},
	})
	revA, _ := ReviewerSpec(root, "demo", task.ReviewerA).SessionName()

	if _, err := DriveRoleLoop(root, tasksDir, "demo"); err != nil {
		t.Fatal(err)
	}
	if f.panes[revA] == nil {
		t.Fatal("reviewer A with a verdict on record was not kept alive")
	}
	if w := sessionWakes(*calls, "/reviewer-A"); len(w) != 0 {
		t.Fatalf("reviewer woken while the check is pending: %+v", w)
	}

	touchMarker(t, task.CheckFailedPath(root, "demo", 2, sha))
	snap, err := DriveRoleLoop(root, tasksDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Check != task.CheckFailed {
		t.Fatalf("check = %q, want failed", snap.Check)
	}
	w := sessionWakes(*calls, "/reviewer-A")
	if len(w) != 1 || !strings.Contains(w[0].msg, "review round 2") || !strings.Contains(w[0].msg, "`just check`) still failed") {
		t.Fatalf("reviewer wakes = %+v, want one wake naming the failed check", w)
	}
}

// A fresh reviewer gets a one-off wake when the response it starts on
// was handed off after a give-up; a pass needs no wake.
func TestDriveFreshReviewerToldOfFailedCheck(t *testing.T) {
	root, tasksDir, sha, _, calls := checkGateFixture(t)
	writeEng(1)(t, root, "demo")
	touchMarker(t, task.CheckFailedPath(root, "demo", 1, sha))
	if _, err := DriveRoleLoop(root, tasksDir, "demo"); err != nil {
		t.Fatal(err)
	}
	w := sessionWakes(*calls, "/reviewer-A")
	if len(w) != 1 || !strings.Contains(w[0].msg, "still failed") {
		t.Fatalf("reviewer wakes = %+v, want one failed-check note", w)
	}
}

func TestDriveWithoutCheckIgnoresGate(t *testing.T) {
	root, tasksDir, _, f, _ := checkGateFixture(t)
	if err := os.Remove(filepath.Join(root, "spore.toml")); err != nil {
		t.Fatal(err)
	}
	writeEng(1)(t, root, "demo")
	snap, err := DriveRoleLoop(root, tasksDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	revA, _ := ReviewerSpec(root, "demo", task.ReviewerA).SessionName()
	if snap.Check != task.CheckOff || f.panes[revA] == nil {
		t.Fatalf("check = %q, reviewer spawned = %v; want off and spawned", snap.Check, f.panes[revA] != nil)
	}
}

// A pass marker on the revision releases the live reviewer with a wake
// telling it the suite already passed.
func TestDriveReviewerWakeNamesPassedCheck(t *testing.T) {
	root, tasksDir, sha, _, calls := checkGateFixture(t)
	walkPhases(t, root, "demo", []phaseRow{
		{writeEng(1), PhaseReviewA, task.ReviewerA, 0},
		{writeGraded(task.ReviewerA, 1, task.VerdictRequestChanges, highBug), PhaseEngineerReviseA, task.ReviewerA, 1},
		{writeEng(2), PhaseReviewA, task.ReviewerA, 1},
	})
	touchMarker(t, task.CheckPassPath(root, "demo", 2, sha))
	snap, err := DriveRoleLoop(root, tasksDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Check != task.CheckPassed {
		t.Fatalf("check = %q, want passed", snap.Check)
	}
	w := sessionWakes(*calls, "/reviewer-A")
	if len(w) != 1 || !strings.Contains(w[0].msg, "`just check`) passed on HEAD") || !strings.Contains(w[0].msg, "do not re-run the full suite") {
		t.Fatalf("reviewer wakes = %+v, want one wake naming the passed check", w)
	}
}
