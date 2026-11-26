package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/versality/spore/internal/task"
)

func walkToDone(t *testing.T, root, slug string) {
	t.Helper()
	for _, step := range []stepFn{
		writeEng(1),
		writeRev(task.ReviewerA, 1, task.VerdictApprove),
		writeRev(task.ReviewerB, 1, task.VerdictApprove),
	} {
		step(t, root, slug)
	}
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("want %s: %v", path, err)
	}
}

func TestReopenRoleLoopStartsNewCycle(t *testing.T) {
	root := t.TempDir()
	slug := "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	walkToDone(t, root, slug)
	dir := task.RoleTaskDir(root, slug)
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state/ready", "state/woken-B-1-123", "state/pre-start.lock", "summary.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cycle, err := ReopenRoleLoop(root, tasksDir, slug, "  drop the extra service  \n")
	if err != nil {
		t.Fatal(err)
	}
	if cycle != 2 {
		t.Fatalf("cycle = %d, want 2", cycle)
	}
	snap, err := DeriveSnapshot(root, slug)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Phase != PhaseEngineerInitial {
		t.Fatalf("phase = %s, want %s", snap.Phase, PhaseEngineerInitial)
	}
	for _, rel := range []string{
		"cycles/1/responses/engineer-round-1.json",
		"cycles/1/reviews/A/round-1.json",
		"cycles/1/reviews/B/round-1.json",
		"cycles/1/state/ready",
		"cycles/1/state/woken-B-1-123",
		"cycles/1/summary.md",
		"state/pre-start.lock",
		"reviews/A",
		"reviews/B",
		"responses",
	} {
		mustExist(t, filepath.Join(dir, rel))
	}
	if ready, _ := task.IsReady(root, slug); ready {
		t.Fatal("ready marker survived the reopen")
	}
	spec, err := task.ReadSpec(root, slug)
	if err != nil {
		t.Fatal(err)
	}
	if want := "pre-existing\n\n## Operator reopen (cycle 2)\n\ndrop the extra service\n"; string(spec) != want {
		t.Fatalf("spec = %q, want %q", spec, want)
	}

	walkToDone(t, root, slug)
	cycle, err = ReopenRoleLoop(root, tasksDir, slug, "and rename it")
	if err != nil {
		t.Fatal(err)
	}
	if cycle != 3 {
		t.Fatalf("second reopen cycle = %d, want 3", cycle)
	}
	mustExist(t, filepath.Join(dir, "cycles/2/reviews/B/round-1.json"))
	spec, _ = task.ReadSpec(root, slug)
	if !strings.HasSuffix(string(spec), "## Operator reopen (cycle 2)\n\ndrop the extra service\n\n## Operator reopen (cycle 3)\n\nand rename it\n") {
		t.Fatalf("spec after second reopen = %q", spec)
	}
}

func TestReopenRoleLoopAfterEscalation(t *testing.T) {
	root := t.TempDir()
	slug := "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	writeEng(1)(t, root, slug)
	for r := 1; r <= MaxReviewerRounds; r++ {
		if r > 1 {
			writeEng(r)(t, root, slug)
		}
		writeRev(task.ReviewerA, r, task.VerdictRequestChanges)(t, root, slug)
	}
	if _, err := ReopenRoleLoop(root, tasksDir, slug, "narrow the scope to X"); err != nil {
		t.Fatal(err)
	}
	snap, err := DeriveSnapshot(root, slug)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Phase != PhaseEngineerInitial {
		t.Fatalf("phase = %s", snap.Phase)
	}
}

func TestReopenRoleLoopRefuses(t *testing.T) {
	root := t.TempDir()
	slug := "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	writeEng(1)(t, root, slug)

	if _, err := ReopenRoleLoop(root, tasksDir, slug, "x"); err == nil || !strings.Contains(err.Error(), "phase review-A") {
		t.Fatalf("mid-loop reopen err = %v", err)
	}
	if err := CheckReopenable(root, tasksDir, slug); err == nil || !strings.Contains(err.Error(), "phase review-A") {
		t.Fatalf("mid-loop CheckReopenable err = %v", err)
	}
	walkToDone(t, root, slug)
	if _, err := ReopenRoleLoop(root, tasksDir, slug, " \n"); err == nil || !strings.Contains(err.Error(), "note is required") {
		t.Fatalf("empty note err = %v", err)
	}
	if err := CheckReopenable(root, tasksDir, slug); err != nil {
		t.Fatalf("done loop CheckReopenable err = %v", err)
	}
	writeRoleLoopTask(t, tasksDir, slug, "paused")
	if _, err := ReopenRoleLoop(root, tasksDir, slug, "x"); err == nil || !strings.Contains(err.Error(), "want active") {
		t.Fatalf("paused task err = %v", err)
	}
	if _, err := os.Stat(cyclesDir(root, slug)); !os.IsNotExist(err) {
		t.Fatalf("refused reopen archived something: %v", err)
	}
}

func TestGitHubMirrorReopenRedraftsAndLabelsCycle(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	walkToDone(t, f.root, f.slug)
	for range 2 {
		f.tick(t)
	}
	if !f.state(t).Ready {
		t.Fatal("cycle 1 not marked ready")
	}
	before := len(f.calls(t))

	if _, err := ReopenRoleLoop(f.root, f.tasksDir, f.slug, "drop the extra service"); err != nil {
		t.Fatal(err)
	}
	s := f.state(t)
	if s.PRNumber != 12 || s.Ready || len(s.Posted) != 0 || s.Reopen == nil || s.Reopen.Cycle != 2 {
		t.Fatalf("state after reopen = %+v", s)
	}
	f.tick(t)
	f.tick(t)
	got := f.calls(t)[before:]
	want := []string{
		"pr view 12 --json isDraft --jq .isDraft",
		"pr ready 12 --undo",
		"pr comment 12 --body-file -",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("reopen calls:\n got %q\nwant %q", got, want)
	}
	if !strings.Contains(f.log(t), "**Reopened by the operator (cycle 2)**\n\ndrop the extra service") {
		t.Fatalf("reopen comment missing:\n%s", f.log(t))
	}

	writeEng(1)(t, f.root, f.slug)
	writeLegacyReview(t, f.root, f.slug, task.ReviewerA, 1, task.Review{Verdict: task.VerdictRequestChanges, Summary: "still there", Prefix: "R", Comments: []task.ReviewComment{task.Ungraded("remove it")}})
	writeEng(2)(t, f.root, f.slug)
	mustWriteReview(t, f.root, f.slug, task.ReviewerA, 2, task.Review{Verdict: task.VerdictApprove, Summary: "gone", Prefix: "R"})
	mustWriteReview(t, f.root, f.slug, task.ReviewerB, 1, task.Review{Verdict: task.VerdictApprove, Summary: "ship it", Prefix: "C"})
	for range 3 {
		f.tick(t)
	}
	log := f.log(t)
	for _, s := range []string{
		"**Requirements review (cycle 2, round 1): request changes**",
		"**Engineer (cycle 2, round 2)**",
		"**Code review (cycle 2, round 1): approve**",
	} {
		if !strings.Contains(log, s) {
			t.Errorf("gh log missing %q:\n%s", s, log)
		}
	}
	readies := 0
	for _, c := range f.calls(t) {
		if c == "pr ready 12" {
			readies++
		}
	}
	if readies != 2 {
		t.Fatalf("pr ready calls = %d, want 2 (one per cycle): %q", readies, f.calls(t))
	}
	if !f.state(t).Ready {
		t.Fatal("cycle 2 ready flag not set")
	}
}

// A mirror holding github.json used to fail reopen only after the
// cycle was archived, leaving a loop that could neither run the new
// cycle on the PR nor be reopened again.
func TestReopenRoleLoopLockedLeavesTreeUntouched(t *testing.T) {
	root := t.TempDir()
	slug := "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	walkToDone(t, root, slug)
	unlock, err := lockGitHubState(githubStatePath(root, slug))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ReopenRoleLoop(root, tasksDir, slug, "note")
	unlock()
	if err == nil || !strings.Contains(err.Error(), "retry") {
		t.Fatalf("err = %v, want a retry refusal", err)
	}
	if _, err := os.Stat(filepath.Join(task.RoleTaskDir(root, slug), "cycles")); !os.IsNotExist(err) {
		t.Fatalf("cycles/ exists after a refused reopen: %v", err)
	}
	if spec, _ := task.ReadSpec(root, slug); strings.Contains(string(spec), "Operator reopen") {
		t.Fatal("spec.md changed after a refused reopen")
	}
	if cycle, err := ReopenRoleLoop(root, tasksDir, slug, "note"); err != nil || cycle != 2 {
		t.Fatalf("retry: cycle %d err %v, want cycle 2", cycle, err)
	}
}
