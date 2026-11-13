package fleet

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/versality/spore/internal/task"
)

var (
	lowNit  = task.ReviewComment{Severity: task.SeverityLow, Category: "nit", Text: "rename foo"}
	medEdge = task.ReviewComment{Severity: task.SeverityMedium, Category: "edge-case", Text: "empty list", Reason: "plausible"}
	highBug = task.ReviewComment{Severity: task.SeverityHigh, Category: "correctness", Text: "nil deref", Reason: "every run"}
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

func walkPhases(t *testing.T, root, slug string, rows []phaseRow) Snapshot {
	t.Helper()
	var snap Snapshot
	for i, r := range rows {
		r.step(t, root, slug)
		var err error
		snap, err = DeriveSnapshot(root, slug)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if snap.Phase != r.phase || snap.CurrentReviewer != r.inst || snap.ReviewerRound != r.round {
			t.Fatalf("step %d: got %s %s round %d, want %s %s round %d",
				i, snap.Phase, snap.CurrentReviewer, snap.ReviewerRound, r.phase, r.inst, r.round)
		}
	}
	return snap
}

func TestReviewBodyGradedBySeverity(t *testing.T) {
	rev := task.Review{
		Verdict: task.VerdictRequestChanges,
		Summary: "one bug",
		Comments: []task.ReviewComment{
			lowNit,
			medEdge,
			{Severity: "medium", Category: "correctness", Where: "a.go:3", Text: "off by one", Reason: "every page"},
			{Severity: "high", Category: "security", Text: "token logged", Reason: "leaks creds"},
		},
	}
	got := reviewBody(task.ReviewerA, 2, rev)
	want := "**Review #1 (round 2): request changes**\n\none bug\n\n" +
		"- **high** / security - token logged (leaks creds)\n" +
		"- **medium** / edge-case - empty list (plausible)\n" +
		"- **medium** / correctness / `a.go:3` - off by one (every page)\n" +
		"- **low** / nit - rename foo\n"
	if got != want {
		t.Errorf("body:\n%s\nwant:\n%s", got, want)
	}
}

func TestReviewBodyLegacyStringsUnchanged(t *testing.T) {
	rev := task.Review{Verdict: task.VerdictApprove, Summary: "ok",
		Comments: []task.ReviewComment{task.Ungraded("b first"), task.Ungraded("a second")}}
	got := reviewBody(task.ReviewerB, 1, rev)
	want := "**Review #2 (round 1): approve**\n\nok\n\n- b first\n- a second\n"
	if got != want {
		t.Errorf("body:\n%q\nwant:\n%q", got, want)
	}
}

func TestReviewBodyUngradedInGradedReview(t *testing.T) {
	rev := task.Review{Verdict: task.VerdictRequestChanges, Comments: []task.ReviewComment{
		lowNit, task.Ungraded("plain"), highBug,
	}}
	got := renderReviewComments(rev)
	want := "- **high** / correctness - nil deref (every run)\n- **ungraded** - plain\n- **low** / nit - rename foo\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDeriveSnapshotSeverityCounts(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	snap := walkPhases(t, root, slug, []phaseRow{
		{writeEng(1), PhaseReviewA, task.ReviewerA, 0},
		{writeGraded(task.ReviewerA, 1, task.VerdictRequestChanges, highBug, medEdge, lowNit), PhaseEngineerReviseA, task.ReviewerA, 1},
	})
	if snap.LastHighMedium != 2 || snap.LastLow != 1 || snap.LastUngraded != 0 {
		t.Errorf("counts = %d high/medium, %d low, %d ungraded; want 2, 1, 0", snap.LastHighMedium, snap.LastLow, snap.LastUngraded)
	}
}

// R1 mixed -> R2 lows -> R3 lows-only approve -> review-B, never
// escalated; B approving at R3 with lows reaches done.
func TestDerivePhaseFinalRoundLowsApprove(t *testing.T) {
	A, B := task.ReviewerA, task.ReviewerB
	root, slug := t.TempDir(), "demo"
	walkPhases(t, root, slug, []phaseRow{
		{writeEng(1), PhaseReviewA, A, 0},
		{writeGraded(A, 1, task.VerdictRequestChanges, highBug, lowNit), PhaseEngineerReviseA, A, 1},
		{writeEng(2), PhaseReviewA, A, 1},
		{writeGraded(A, 2, task.VerdictRequestChanges, lowNit), PhaseEngineerReviseA, A, 2},
		{writeEng(3), PhaseReviewA, A, 2},
		{writeGraded(A, 3, task.VerdictApprove, lowNit, lowNit), PhaseReviewB, B, 0},
		{writeGraded(B, 1, task.VerdictRequestChanges, lowNit), PhaseEngineerReviseB, B, 1},
		{writeEng(4), PhaseReviewB, B, 1},
		{writeGraded(B, 2, task.VerdictRequestChanges, medEdge), PhaseEngineerReviseB, B, 2},
		{writeEng(5), PhaseReviewB, B, 2},
		{writeGraded(B, 3, task.VerdictApprove, lowNit), PhaseDone, B, 3},
	})
}

// A legacy all-string tree derives exactly as before grading.
func TestDerivePhaseLegacyStringsUnchanged(t *testing.T) {
	A, B := task.ReviewerA, task.ReviewerB
	root, slug := t.TempDir(), "demo"
	walkPhases(t, root, slug, []phaseRow{
		{writeEng(1), PhaseReviewA, A, 0},
		{writeGraded(A, 1, task.VerdictApprove, task.Ungraded("consider X")), PhaseReviewB, B, 0},
		{writeGraded(B, 1, task.VerdictRequestChanges, task.Ungraded("fix Y")), PhaseEngineerReviseB, B, 1},
		{writeEng(2), PhaseReviewB, B, 1},
		{writeGraded(B, 2, task.VerdictApprove, task.Ungraded("nit")), PhaseDone, B, 2},
	})
}

func escalateA(t *testing.T, root, slug string) {
	t.Helper()
	walkPhases(t, root, slug, []phaseRow{
		{writeEng(1), PhaseReviewA, task.ReviewerA, 0},
		{writeGraded(task.ReviewerA, 1, task.VerdictRequestChanges, highBug), PhaseEngineerReviseA, task.ReviewerA, 1},
		{writeEng(2), PhaseReviewA, task.ReviewerA, 1},
		{writeGraded(task.ReviewerA, 2, task.VerdictRequestChanges, lowNit), PhaseEngineerReviseA, task.ReviewerA, 2},
		{writeEng(3), PhaseReviewA, task.ReviewerA, 2},
		{writeGraded(task.ReviewerA, 3, task.VerdictRequestChanges, medEdge, lowNit), PhaseEscalated, task.ReviewerA, 3},
	})
}

// R3 medium escalates; the engineer is woken once for a fix pass, its
// response keeps the loop escalated, and no reviewer is spawned or
// woken.
func TestDriveEscalationFixPass(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", t.TempDir())
	slug := "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	escalateA(t, root, slug)
	panes := stubPanes(t)
	outcome := wakeDelivered
	calls := stubWakeRole(t, &outcome)

	snap, err := DriveRoleLoop(root, tasksDir, slug)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Phase != PhaseEscalated || !snap.EscalationFixPending {
		t.Fatalf("snap = %+v, want escalated with fix pending", snap)
	}
	if len(*calls) != 1 || !strings.HasSuffix((*calls)[0].session, "/demo/engineer") {
		t.Fatalf("wakes = %+v, want one engineer wake", *calls)
	}
	msg := (*calls)[0].msg
	for _, s := range []string{"round 3, the final round", "escalated to the operator", "high and medium", "engineer-round-4.json", "No re-review follows"} {
		if !strings.Contains(msg, s) {
			t.Errorf("wake msg missing %q: %s", s, msg)
		}
	}

	writeEng(4)(t, root, slug)
	snap, err = DriveRoleLoop(root, tasksDir, slug)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Phase != PhaseEscalated || snap.EscalationFixPending {
		t.Fatalf("after fix pass snap = %+v, want escalated, no fix pending", snap)
	}
	if len(*calls) != 1 {
		t.Errorf("extra wakes after the fix pass: %+v", (*calls)[1:])
	}
	for _, s := range panes.spawns {
		if strings.Contains(s, "reviewer") {
			t.Errorf("escalation spawned a reviewer pane %s", s)
		}
	}
}

// Operator force-approves A's final verdict after the fix pass: the
// fix response belongs to A, so B's first request_changes still waits
// for a fresh engineer round.
func TestDerivePhaseForceApproveAfterFixPass(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	escalateA(t, root, slug)
	stateDir := filepath.Join(task.RoleTaskDir(root, slug), "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "escalated-A-3"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	B := task.ReviewerB
	walkPhases(t, root, slug, []phaseRow{
		{writeEng(4), PhaseEscalated, task.ReviewerA, 3},
		{writeGraded(task.ReviewerA, 3, task.VerdictApprove), PhaseReviewB, B, 0},
		{writeGraded(B, 1, task.VerdictRequestChanges, lowNit), PhaseEngineerReviseB, B, 1},
		{writeEng(5), PhaseReviewB, B, 1},
	})
}

func TestMirrorEventsEscalationPostsFixPass(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	escalateA(t, root, slug)
	snap, _ := DeriveSnapshot(root, slug)
	evs, err := mirrorEvents(root, slug, snap)
	if err != nil {
		t.Fatal(err)
	}
	base := []string{"engineer-round-1.json", "A/round-1.json", "engineer-round-2.json", "A/round-2.json", "engineer-round-3.json", "A/round-3.json"}
	if got, want := eventNames(evs), append(append([]string{}, base...), "escalated-A-3"); !reflect.DeepEqual(got, want) {
		t.Fatalf("before fix pass: %v, want %v", got, want)
	}
	writeEng(4)(t, root, slug)
	snap, _ = DeriveSnapshot(root, slug)
	evs, _ = mirrorEvents(root, slug, snap)
	if got, want := eventNames(evs), append(append([]string{}, base...), "engineer-round-4.json", "escalated-A-3"); !reflect.DeepEqual(got, want) {
		t.Fatalf("after fix pass: %v, want %v", got, want)
	}
}

func TestMirrorEventsFinalRoundLowsReady(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	writeEng(1)(t, root, slug)
	writeGraded(task.ReviewerA, 1, task.VerdictApprove)(t, root, slug)
	for r := 1; r <= 2; r++ {
		writeGraded(task.ReviewerB, r, task.VerdictRequestChanges, lowNit)(t, root, slug)
		writeEng(r+1)(t, root, slug)
	}
	writeGraded(task.ReviewerB, 3, task.VerdictApprove, lowNit)(t, root, slug)
	snap, _ := DeriveSnapshot(root, slug)
	evs, _ := mirrorEvents(root, slug, snap)
	got := eventNames(evs)
	if got[len(got)-1] != eventReady || got[len(got)-2] != "B/round-3.json" {
		t.Fatalf("events = %v, want B/round-3 then ready", got)
	}
}

func eventNames(evs []mirrorEvent) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.name)
	}
	return out
}

// A grade/verdict mismatch is bounced like any other invalid file, and
// the wake names the broken rule and round.
func TestBounceNamesVerdictRule(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", t.TempDir())
	slug := "demo"
	if err := task.EnsureLegacyMarker(root, slug); err != nil {
		t.Fatal(err)
	}
	path := task.ReviewPath(root, slug, task.ReviewerA, 1)
	writeRaw(t, path, `{"author":"A","verdict":"approve","summary":"s","comments":[{"severity":"low","category":"edge-case","text":"rare race"}]}`)

	outcome := wakeNoSession
	calls := stubWakeRole(t, &outcome)
	if err := bounceInvalidRoleFiles(root, slug); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %+v, want one wake", *calls)
	}
	msg := (*calls)[0].msg
	for _, s := range []string{`round 1 verdict "approve"`, "in rounds 1-2 any comment (high, medium or low) requires", `"request_changes"`} {
		if !strings.Contains(msg, s) {
			t.Errorf("msg missing %q:\n%s", s, msg)
		}
	}
}
