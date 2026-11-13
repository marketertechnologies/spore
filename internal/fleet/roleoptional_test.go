package fleet

import (
	"reflect"
	"strings"
	"testing"

	"github.com/versality/spore/internal/task"
)

var (
	lowComment = task.ReviewComment{Severity: task.SeverityLow, Category: "nit", Text: "rename foo"}
	medEdge    = task.ReviewComment{Severity: task.SeverityMedium, Category: "edge-case", Text: "empty list", Reason: "rare"}
	highBug    = task.ReviewComment{Severity: task.SeverityHigh, Category: "correctness", Text: "nil deref", Reason: "every run"}
)

func writeGraded(instance task.ReviewerInstance, round int, verdict string, comments ...task.ReviewComment) stepFn {
	return func(t *testing.T, root, slug string) {
		t.Helper()
		mustWriteReview(t, root, slug, instance, round, task.Review{Verdict: verdict, Summary: "s", Comments: comments})
	}
}

type phaseRow struct {
	step  stepFn
	phase Phase
	inst  task.ReviewerInstance
	round int
}

func walkPhases(t *testing.T, rows []phaseRow) {
	t.Helper()
	root, slug := t.TempDir(), "demo"
	for i, r := range rows {
		r.step(t, root, slug)
		snap, err := DeriveSnapshot(root, slug)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if snap.Phase != r.phase || snap.CurrentReviewer != r.inst || snap.ReviewerRound != r.round {
			t.Fatalf("step %d: got %s %s round %d, want %s %s round %d",
				i, snap.Phase, snap.CurrentReviewer, snap.ReviewerRound, r.phase, r.inst, r.round)
		}
	}
}

func TestDerivePhaseOptionalPassA(t *testing.T) {
	A, B := task.ReviewerA, task.ReviewerB
	walkPhases(t, []phaseRow{
		{writeEng(1), PhaseReviewA, A, 0},
		{writeGraded(A, 1, task.VerdictApprove, lowComment, medEdge), PhaseEngineerOptionalA, A, 1},
		{writeEng(2), PhaseReviewB, B, 0},
		{writeGraded(B, 1, task.VerdictApprove), PhaseDone, B, 1},
	})
}

func TestDerivePhaseOptionalPassB(t *testing.T) {
	A, B := task.ReviewerA, task.ReviewerB
	walkPhases(t, []phaseRow{
		{writeEng(1), PhaseReviewA, A, 0},
		{writeGraded(A, 1, task.VerdictApprove), PhaseReviewB, B, 0},
		{writeGraded(B, 1, task.VerdictApprove, lowComment), PhaseEngineerOptionalB, B, 1},
		{writeEng(2), PhaseDone, B, 1},
	})
}

// An approve with only plain strings keeps the pre-grading behaviour:
// no optional pass.
func TestDerivePhaseUngradedApproveSkipsPass(t *testing.T) {
	A, B := task.ReviewerA, task.ReviewerB
	walkPhases(t, []phaseRow{
		{writeEng(1), PhaseReviewA, A, 0},
		{writeGraded(A, 1, task.VerdictApprove, task.Ungraded("consider X")), PhaseReviewB, B, 0},
		{writeGraded(B, 1, task.VerdictApprove, task.Ungraded("nit")), PhaseDone, B, 1},
	})
}

// Request-changes rounds, an approve with lows, then B with its own
// revise and optional pass: engineer rounds stay paired.
func TestDerivePhaseMixedRounds(t *testing.T) {
	A, B := task.ReviewerA, task.ReviewerB
	walkPhases(t, []phaseRow{
		{writeEng(1), PhaseReviewA, A, 0},
		{writeGraded(A, 1, task.VerdictRequestChanges, highBug, lowComment), PhaseEngineerReviseA, A, 1},
		{writeEng(2), PhaseReviewA, A, 1},
		{writeGraded(A, 2, task.VerdictApprove, lowComment), PhaseEngineerOptionalA, A, 2},
		{writeEng(3), PhaseReviewB, B, 0},
		{writeGraded(B, 1, task.VerdictRequestChanges, highBug), PhaseEngineerReviseB, B, 1},
		{writeEng(4), PhaseReviewB, B, 1},
		{writeGraded(B, 2, task.VerdictApprove, medEdge), PhaseEngineerOptionalB, B, 2},
		{writeEng(5), PhaseDone, B, 2},
	})
}

// Round 3 with only low comments is an approve by the grade rule, so
// the cap never fires: optional pass, then review-B.
func TestDerivePhaseRoundThreeLowsApprove(t *testing.T) {
	A, B := task.ReviewerA, task.ReviewerB
	walkPhases(t, []phaseRow{
		{writeEng(1), PhaseReviewA, A, 0},
		{writeGraded(A, 1, task.VerdictRequestChanges, highBug), PhaseEngineerReviseA, A, 1},
		{writeEng(2), PhaseReviewA, A, 1},
		{writeGraded(A, 2, task.VerdictRequestChanges, highBug), PhaseEngineerReviseA, A, 2},
		{writeEng(3), PhaseReviewA, A, 2},
		{writeGraded(A, 3, task.VerdictApprove, lowComment, lowComment), PhaseEngineerOptionalA, A, 3},
		{writeEng(4), PhaseReviewB, B, 0},
	})
	root := t.TempDir()
	if err := task.WriteReview(root, "x", A, 1, task.Review{Verdict: task.VerdictRequestChanges, Summary: "s",
		Comments: []task.ReviewComment{lowComment}}); err == nil {
		t.Fatal("request_changes with only lows must be rejected")
	}
}

func TestMirrorEventsPairOptionalPass(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	writeEng(1)(t, root, slug)
	writeGraded(task.ReviewerA, 1, task.VerdictApprove, lowComment)(t, root, slug)
	snap, _ := DeriveSnapshot(root, slug)
	evs, err := mirrorEvents(root, slug, snap)
	if err != nil {
		t.Fatal(err)
	}
	if got := eventNames(evs); !reflect.DeepEqual(got, []string{"engineer-round-1.json", "A/round-1.json"}) {
		t.Fatalf("before optional response: %v", got)
	}

	writeEng(2)(t, root, slug)
	writeGraded(task.ReviewerB, 1, task.VerdictApprove, lowComment)(t, root, slug)
	snap, _ = DeriveSnapshot(root, slug)
	if snap.Phase != PhaseEngineerOptionalB {
		t.Fatalf("phase = %s", snap.Phase)
	}
	evs, _ = mirrorEvents(root, slug, snap)
	want := []string{"engineer-round-1.json", "A/round-1.json", "engineer-round-2.json", "B/round-1.json"}
	if got := eventNames(evs); !reflect.DeepEqual(got, want) {
		t.Fatalf("no ready before B's optional pass: got %v, want %v", got, want)
	}
	if !evs[2].optional {
		t.Errorf("engineer-round-2 not marked optional")
	}

	writeEng(3)(t, root, slug)
	snap, _ = DeriveSnapshot(root, slug)
	evs, _ = mirrorEvents(root, slug, snap)
	want = append(want, "engineer-round-3.json", eventReady)
	if got := eventNames(evs); !reflect.DeepEqual(got, want) {
		t.Fatalf("after B optional pass: got %v, want %v", got, want)
	}
	if !evs[4].optional {
		t.Errorf("engineer-round-3 not marked optional")
	}
}

func eventNames(evs []mirrorEvent) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.name)
	}
	return out
}

func TestDriveWakesEngineerForOptionalPass(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", t.TempDir())
	slug := "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	panes := stubPanes(t)
	outcome := wakeDelivered
	calls := stubWakeRole(t, &outcome)

	writeEng(1)(t, root, slug)
	writeGraded(task.ReviewerA, 1, task.VerdictApprove, lowComment)(t, root, slug)
	snap, err := DriveRoleLoop(root, tasksDir, slug)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Phase != PhaseEngineerOptionalA {
		t.Fatalf("phase = %s", snap.Phase)
	}
	if len(*calls) != 1 || !strings.HasSuffix((*calls)[0].session, "/demo/engineer") {
		t.Fatalf("wakes = %+v, want one engineer wake", *calls)
	}
	msg := (*calls)[0].msg
	for _, s := range []string{"APPROVED at round 1", "optional", "You may decline", "No re-review follows", "engineer-round-2.json"} {
		if !strings.Contains(msg, s) {
			t.Errorf("wake msg missing %q: %s", s, msg)
		}
	}
	for _, s := range panes.spawns {
		if strings.Contains(s, "reviewer") {
			t.Errorf("optional pass spawned a reviewer pane %s", s)
		}
	}
}

// An approve with no comments must not wake the engineer.
func TestDriveApproveWithoutCommentsSkipsEngineer(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", t.TempDir())
	slug := "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	stubPanes(t)
	outcome := wakeDelivered
	calls := stubWakeRole(t, &outcome)

	writeEng(1)(t, root, slug)
	writeGraded(task.ReviewerA, 1, task.VerdictApprove)(t, root, slug)
	snap, err := DriveRoleLoop(root, tasksDir, slug)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Phase != PhaseReviewB {
		t.Fatalf("phase = %s, want review-B", snap.Phase)
	}
	for _, c := range *calls {
		if strings.HasSuffix(c.session, "/engineer") {
			t.Errorf("engineer woken on a comment-less approve: %+v", c)
		}
	}
}
