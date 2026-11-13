package fleet

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/versality/spore/internal/task"
)

// Phase is the current step in a task's role-loop. Derived from the
// artifacts on disk under <projectRoot>/.spore/<slug>/.
type Phase string

const (
	// PhaseEngineerInitial: no engineer response yet. Coordinator
	// should spawn the engineer pane (or wait for the existing one to
	// write its first response).
	PhaseEngineerInitial Phase = "engineer-initial"

	// PhaseReviewA: engineer has shipped a response; A has not
	// written a verdict for it yet. The A pane (still alive if mid-
	// loop, else freshly spawned) owns the next file write.
	PhaseReviewA Phase = "review-A"

	// PhaseEngineerReviseA: A's latest verdict is request_changes and
	// the engineer has not responded to it. The engineer owns the
	// next file write.
	PhaseEngineerReviseA Phase = "engineer-revise-A"

	// PhaseReviewB: A approved; B is either not yet spawned (no B
	// verdict on disk) or alive and processing the engineer's latest
	// response. The B pane owns the next file write.
	PhaseReviewB Phase = "review-B"

	// PhaseEngineerReviseB: B's latest verdict is request_changes and
	// the engineer has not responded to it.
	PhaseEngineerReviseB Phase = "engineer-revise-B"

	// PhaseDone: B approved. The branch is ready for the operator.
	PhaseDone Phase = "done"

	// PhaseEscalated: the current reviewer issued request_changes at
	// round MaxReviewerRounds (only possible with a high or medium
	// comment). The engineer gets one fix pass at those comments (see
	// Snapshot.EscalationFixPending); no re-review follows, and the
	// loop stays here for the operator to clarify spec, force-approve,
	// kill the task, or hand back for another loop.
	PhaseEscalated Phase = "escalated"
)

// MaxReviewerRounds caps each reviewer phase. A request_changes
// verdict at this round escalates instead of looping for round N+1.
const MaxReviewerRounds = task.FinalReviewRound

// Snapshot is the current view of a task's role-loop state.
type Snapshot struct {
	Slug string
	// Phase is the derived loop step.
	Phase Phase
	// EngineerRound is the highest engineer-round-N.json present
	// (== count, since rounds are dense). Zero before the engineer
	// has shipped anything.
	EngineerRound int
	// CurrentReviewer is the reviewer instance whose phase we are
	// in. Empty in PhaseEngineerInitial.
	CurrentReviewer task.ReviewerInstance
	// ReviewerRound is the number of verdicts the current reviewer
	// has written. Zero before the first verdict of the phase.
	ReviewerRound int
	// LastVerdict is the verdict on the latest reviewer round in
	// the current phase, or empty when none has been written yet.
	LastVerdict string
	// LastHighMedium, LastLow and LastUngraded count the comments on
	// that verdict by grade. All zero with no verdict.
	LastHighMedium, LastLow, LastUngraded int
	// EscalationFixPending is true in PhaseEscalated until the
	// engineer writes its one fix-pass response to the final verdict.
	EscalationFixPending bool
}

// DeriveSnapshot reads `<projectRoot>/.spore/<slug>/` and returns the
// implied state. A missing tree returns a PhaseEngineerInitial
// snapshot (no error), so callers can drive the loop forward without
// first preparing the tree. Files that fail validation are not
// counted: a bad file must neither wedge the loop nor skew the round
// math. DriveRoleLoop bounces them back to their author.
func DeriveSnapshot(projectRoot, slug string) (Snapshot, error) {
	engRounds, err := validEngineerRounds(projectRoot, slug)
	if err != nil {
		return Snapshot{}, err
	}
	aRounds, err := task.ListReviewRounds(projectRoot, slug, task.ReviewerA)
	if err != nil {
		return Snapshot{}, err
	}
	bRounds, err := task.ListReviewRounds(projectRoot, slug, task.ReviewerB)
	if err != nil {
		return Snapshot{}, err
	}
	aReviews, err := loadReviews(projectRoot, slug, task.ReviewerA, aRounds)
	if err != nil {
		return Snapshot{}, err
	}
	bReviews, err := loadReviews(projectRoot, slug, task.ReviewerB, bRounds)
	if err != nil {
		return Snapshot{}, err
	}

	snap := Snapshot{Slug: slug, EngineerRound: 0}
	if len(engRounds) > 0 {
		snap.EngineerRound = engRounds[len(engRounds)-1]
	}
	aFixOwed, err := escalationMarked(projectRoot, slug, task.ReviewerA)
	if err != nil {
		return Snapshot{}, err
	}
	var expected int
	snap.Phase, snap.CurrentReviewer, snap.ReviewerRound, snap.LastVerdict, expected =
		derivePhase(len(engRounds), aReviews, bReviews, aFixOwed)
	if snap.LastVerdict != "" {
		revs := aReviews
		if snap.CurrentReviewer == task.ReviewerB {
			revs = bReviews
		}
		snap.LastHighMedium, snap.LastLow, snap.LastUngraded = revs[len(revs)-1].SeverityCounts()
	}
	snap.EscalationFixPending = snap.Phase == PhaseEscalated && len(engRounds) < expected
	return snap, nil
}

func loadReviews(projectRoot, slug string, instance task.ReviewerInstance, rounds []int) ([]task.Review, error) {
	out := make([]task.Review, 0, len(rounds))
	for _, r := range rounds {
		rev, err := task.ReadReview(projectRoot, slug, instance, r)
		if errors.Is(err, task.ErrInvalidRoleFile) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s round %d: %w", instance, r, err)
		}
		out = append(out, rev)
	}
	return out, nil
}

func validEngineerRounds(projectRoot, slug string) ([]int, error) {
	rounds, err := task.ListEngineerRounds(projectRoot, slug)
	if err != nil {
		return nil, err
	}
	out := rounds[:0]
	for _, r := range rounds {
		_, err := task.ReadEngineerResponse(projectRoot, slug, r)
		if errors.Is(err, task.ErrInvalidRoleFile) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read engineer round %d: %w", r, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// escalationMarked reports whether the driver ever escalated inst
// (an `escalated-<inst>-*` marker in state/). The engineer was then
// woken for a fix pass, which owes one engineer round even after the
// operator force-approves the final verdict.
func escalationMarked(projectRoot, slug string, inst task.ReviewerInstance) (bool, error) {
	m, err := filepath.Glob(filepath.Join(task.RoleTaskDir(projectRoot, slug), "state", "escalated-"+string(inst)+"-*"))
	return len(m) > 0, err
}

// derivePhase is the pure state-machine kernel. It takes the counts
// of engineer responses and the ordered reviewer verdicts; it
// returns the derived phase plus per-status fields, and the number
// of engineer rounds the verdicts so far call for (one initial, one
// per request_changes, including the final-round one that escalates).
// aFixOwed adds a round when A escalated and the operator then
// overwrote the final verdict to approve: the escalation fix pass
// still answered it.
func derivePhase(engCount int, aRev, bRev []task.Review, aFixOwed bool) (Phase, task.ReviewerInstance, int, string, int) {
	if engCount == 0 {
		return PhaseEngineerInitial, "", 0, "", 1
	}

	// Pre-A: engineer shipped initial, A has not written anything.
	if len(aRev) == 0 {
		return PhaseReviewA, task.ReviewerA, 0, "", 1
	}

	lastA := aRev[len(aRev)-1]
	expected := 1 + countRequestChanges(aRev)
	if lastA.Verdict == task.VerdictApprove {
		if aFixOwed {
			expected++
		}
		return derivePhaseB(engCount, expected, bRev)
	}

	// A's latest verdict is request_changes. At the final round the
	// loop escalates whether or not the engineer's fix pass landed.
	if len(aRev) >= MaxReviewerRounds {
		return PhaseEscalated, task.ReviewerA, len(aRev), lastA.Verdict, expected
	}
	if engCount >= expected {
		// Engineer has responded to the latest A verdict; A owns the
		// next write.
		return PhaseReviewA, task.ReviewerA, len(aRev), lastA.Verdict, expected
	}
	return PhaseEngineerReviseA, task.ReviewerA, len(aRev), lastA.Verdict, expected
}

// derivePhaseB continues after A approved; afterA is the engineer
// round count the A phase accounts for.
func derivePhaseB(engCount, afterA int, bRev []task.Review) (Phase, task.ReviewerInstance, int, string, int) {
	if len(bRev) == 0 {
		// A approved, B not yet spawned/written. Either way, B owns
		// the next write; no LastVerdict for the B phase.
		return PhaseReviewB, task.ReviewerB, 0, "", afterA
	}
	lastB := bRev[len(bRev)-1]
	expected := afterA + countRequestChanges(bRev)
	if lastB.Verdict == task.VerdictApprove {
		return PhaseDone, task.ReviewerB, len(bRev), lastB.Verdict, expected
	}
	// last B is request_changes
	if len(bRev) >= MaxReviewerRounds {
		return PhaseEscalated, task.ReviewerB, len(bRev), lastB.Verdict, expected
	}
	if engCount >= expected {
		return PhaseReviewB, task.ReviewerB, len(bRev), lastB.Verdict, expected
	}
	return PhaseEngineerReviseB, task.ReviewerB, len(bRev), lastB.Verdict, expected
}

// countRequestChanges returns how many verdicts in revs are
// request_changes.
func countRequestChanges(revs []task.Review) int {
	n := 0
	for _, r := range revs {
		if r.Verdict == task.VerdictRequestChanges {
			n++
		}
	}
	return n
}
