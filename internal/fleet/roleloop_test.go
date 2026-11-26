package fleet

import (
	"testing"

	"github.com/versality/spore/internal/task"
)

// stepFn mutates the artifact tree to advance the role loop by one
// step. Used to script the state-machine walk in tests.
type stepFn func(t *testing.T, root, slug string)

func writeEng(round int) stepFn {
	return func(t *testing.T, root, slug string) {
		t.Helper()
		if err := task.WriteEngineerResponse(root, slug, round, task.EngineerResponse{
			Addressed: []string{"placeholder"},
		}); err != nil {
			t.Fatalf("WriteEngineerResponse round=%d: %v", round, err)
		}
	}
}

func writeRev(instance task.ReviewerInstance, round int, verdict string) stepFn {
	return func(t *testing.T, root, slug string) {
		t.Helper()
		if err := task.WriteReview(root, slug, instance, round, task.Review{
			Verdict: verdict,
			Summary: "test verdict",
		}); err != nil {
			t.Fatalf("WriteReview %s round=%d: %v", instance, round, err)
		}
	}
}

func TestDeriveSnapshotHappyPath(t *testing.T) {
	root := t.TempDir()
	slug := "demo"

	check := func(stepIdx int, want Snapshot) {
		t.Helper()
		got, err := DeriveSnapshot(root, slug)
		if err != nil {
			t.Fatalf("step %d: DeriveSnapshot: %v", stepIdx, err)
		}
		got.Slug = "" // do not assert on slug in each row
		if got != want {
			t.Errorf("step %d: snapshot mismatch\n want: %+v\n got:  %+v", stepIdx, want, got)
		}
	}

	// 0. Fresh tree, no artifacts: engineer-initial.
	check(0, Snapshot{Phase: PhaseEngineerInitial})

	// 1. Engineer ships initial response.
	writeEng(1)(t, root, slug)
	check(1, Snapshot{
		Phase:           PhaseReviewA,
		EngineerRound:   1,
		CurrentReviewer: task.ReviewerA,
	})

	// 2. A approves round 1 -> review-B (B not yet spawned).
	writeRev(task.ReviewerA, 1, task.VerdictApprove)(t, root, slug)
	check(2, Snapshot{
		Phase:           PhaseReviewB,
		EngineerRound:   1,
		CurrentReviewer: task.ReviewerB,
	})

	// 3. B approves round 1 -> done.
	writeRev(task.ReviewerB, 1, task.VerdictApprove)(t, root, slug)
	check(3, Snapshot{
		Phase:           PhaseDone,
		EngineerRound:   1,
		CurrentReviewer: task.ReviewerB,
		ReviewerRound:   1,
		LastVerdict:     task.VerdictApprove,
	})
}

func TestDeriveSnapshotReviseLoop(t *testing.T) {
	root := t.TempDir()
	slug := "revise"

	writeEng(1)(t, root, slug)
	writeRev(task.ReviewerA, 1, task.VerdictRequestChanges)(t, root, slug)

	got, err := DeriveSnapshot(root, slug)
	if err != nil {
		t.Fatalf("DeriveSnapshot: %v", err)
	}
	want := Snapshot{
		Slug:            slug,
		Phase:           PhaseEngineerReviseA,
		EngineerRound:   1,
		CurrentReviewer: task.ReviewerA,
		ReviewerRound:   1,
		LastVerdict:     task.VerdictRequestChanges,
	}
	if got != want {
		t.Errorf("after A round 1 request_changes\n want: %+v\n got:  %+v", want, got)
	}

	// Engineer responds (round 2): back to review-A waiting on A round 2.
	writeEng(2)(t, root, slug)
	got, _ = DeriveSnapshot(root, slug)
	want = Snapshot{
		Slug:            slug,
		Phase:           PhaseReviewA,
		EngineerRound:   2,
		CurrentReviewer: task.ReviewerA,
		ReviewerRound:   1,
		LastVerdict:     task.VerdictRequestChanges,
	}
	if got != want {
		t.Errorf("after engineer round 2\n want: %+v\n got:  %+v", want, got)
	}

	// A round 2 approves -> review-B (engineer is still at round 2; B
	// hasn't run yet).
	writeRev(task.ReviewerA, 2, task.VerdictApprove)(t, root, slug)
	got, _ = DeriveSnapshot(root, slug)
	want = Snapshot{
		Slug:            slug,
		Phase:           PhaseReviewB,
		EngineerRound:   2,
		CurrentReviewer: task.ReviewerB,
	}
	if got != want {
		t.Errorf("after A round 2 approve\n want: %+v\n got:  %+v", want, got)
	}

	// B requests changes round 1.
	writeRev(task.ReviewerB, 1, task.VerdictRequestChanges)(t, root, slug)
	got, _ = DeriveSnapshot(root, slug)
	want = Snapshot{
		Slug:            slug,
		Phase:           PhaseEngineerReviseB,
		EngineerRound:   2,
		CurrentReviewer: task.ReviewerB,
		ReviewerRound:   1,
		LastVerdict:     task.VerdictRequestChanges,
	}
	if got != want {
		t.Errorf("after B round 1 request_changes\n want: %+v\n got:  %+v", want, got)
	}

	// Engineer round 3 (revising B), then B approves round 2 -> done.
	writeEng(3)(t, root, slug)
	writeRev(task.ReviewerB, 2, task.VerdictApprove)(t, root, slug)
	got, _ = DeriveSnapshot(root, slug)
	want = Snapshot{
		Slug:            slug,
		Phase:           PhaseDone,
		EngineerRound:   3,
		CurrentReviewer: task.ReviewerB,
		ReviewerRound:   2,
		LastVerdict:     task.VerdictApprove,
	}
	if got != want {
		t.Errorf("after B round 2 approve\n want: %+v\n got:  %+v", want, got)
	}
}

// TestDeriveSnapshotEscalateAtRound3A drives the negative path called
// out in the spec: a reviewer A that never approves; the loop must
// land in PhaseEscalated on the round-3 request_changes verdict and
// stay there.
func TestDeriveSnapshotEscalateAtRound3A(t *testing.T) {
	root := t.TempDir()
	slug := "escalate-a"

	writeEng(1)(t, root, slug)
	writeRev(task.ReviewerA, 1, task.VerdictRequestChanges)(t, root, slug)
	writeEng(2)(t, root, slug)
	writeRev(task.ReviewerA, 2, task.VerdictRequestChanges)(t, root, slug)
	writeEng(3)(t, root, slug)
	writeRev(task.ReviewerA, 3, task.VerdictRequestChanges)(t, root, slug)

	got, err := DeriveSnapshot(root, slug)
	if err != nil {
		t.Fatalf("DeriveSnapshot: %v", err)
	}
	want := Snapshot{
		Slug:            slug,
		Phase:           PhaseEscalated,
		EngineerRound:   3,
		CurrentReviewer: task.ReviewerA,
		ReviewerRound:   3,
		LastVerdict:     task.VerdictRequestChanges,
		// The final request_changes owes one engineer fix pass.
		EscalationFixPending: true,
	}
	if got != want {
		t.Fatalf("escalation snapshot mismatch\n want: %+v\n got:  %+v", want, got)
	}

	// Engineer can keep writing but phase stays escalated until A is
	// reset (or operator force-approves) - the loop should not be
	// fooled into looking like review-A round 4 is pending.
	writeEng(4)(t, root, slug)
	got, _ = DeriveSnapshot(root, slug)
	if got.Phase != PhaseEscalated || got.EscalationFixPending {
		t.Errorf("after engineer round 4: phase %s, fix pending %v; want %s, false", got.Phase, got.EscalationFixPending, PhaseEscalated)
	}
}

// TestDeriveSnapshotEscalateAtRound3B verifies the same cap applies
// independently to B after A has cleared.
func TestDeriveSnapshotEscalateAtRound3B(t *testing.T) {
	root := t.TempDir()
	slug := "escalate-b"

	writeEng(1)(t, root, slug)
	writeRev(task.ReviewerA, 1, task.VerdictApprove)(t, root, slug)
	writeRev(task.ReviewerB, 1, task.VerdictRequestChanges)(t, root, slug)
	writeEng(2)(t, root, slug)
	writeRev(task.ReviewerB, 2, task.VerdictRequestChanges)(t, root, slug)
	writeEng(3)(t, root, slug)
	writeRev(task.ReviewerB, 3, task.VerdictRequestChanges)(t, root, slug)

	got, err := DeriveSnapshot(root, slug)
	if err != nil {
		t.Fatalf("DeriveSnapshot: %v", err)
	}
	want := Snapshot{
		Slug:                 slug,
		Phase:                PhaseEscalated,
		EngineerRound:        3,
		CurrentReviewer:      task.ReviewerB,
		ReviewerRound:        3,
		LastVerdict:          task.VerdictRequestChanges,
		EscalationFixPending: true,
	}
	if got != want {
		t.Errorf("B escalation snapshot mismatch\n want: %+v\n got:  %+v", want, got)
	}
}
