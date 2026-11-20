package fleet

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/versality/spore/internal/task"
)

var (
	lowNit  = task.ReviewComment{Severity: task.SeverityLow, Category: "nit", Text: "rename foo"}
	medEdge = task.ReviewComment{Severity: task.SeverityMedium, Category: "edge-case", Text: "empty list", Reason: "plausible", Trigger: "empty tasks/ dir"}
	highBug = task.ReviewComment{Severity: task.SeverityHigh, Category: "correctness", Text: "nil deref", Reason: "every run", Trigger: "any run"}
	askWhy  = task.ReviewComment{Severity: task.SeverityQuestion, Category: "spec", Text: "is the cap intended?"}
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

func TestReviewBodyNumberedIDs(t *testing.T) {
	rev := task.Review{
		Verdict: task.VerdictRequestChanges,
		Summary: "one bug",
		Prefix:  "R",
		Comments: []task.ReviewComment{
			{Severity: "high", Category: "security", Text: "token logged", Reason: "leaks creds", Trigger: "any login"},
			medEdge,
			{Severity: "medium", Category: "correctness", Where: "a.go:3", Text: "off by one", Reason: "every page", Trigger: "page 2",
				Suggestion: "--- a/a.go\n+++ b/a.go\n@@ -3 +3 @@\n-i <= n\n+i < n\n"},
			lowNit,
			{Severity: "low", Category: "nit", Where: "b.go:1", Text: "name", Suggestion: "x := ```\n"},
			askWhy,
		},
	}
	got := reviewBody(task.ReviewerA, 2, rev)
	want := "**Requirements review (round 2): request changes**\n\none bug\n\n" +
		"- **R2.1** | high | security - token logged (leaks creds)\n  Trigger: any login\n" +
		"- **R2.2** | medium | edge-case - empty list (plausible)\n  Trigger: empty tasks/ dir\n" +
		"- **R2.3** | medium | correctness | `a.go:3` - off by one (every page)\n  Trigger: page 2\n\n" +
		"  ```diff\n  --- a/a.go\n  +++ b/a.go\n  @@ -3 +3 @@\n  -i <= n\n  +i < n\n  ```\n" +
		"- **R2.4** | low | nit - rename foo\n" +
		"- **R2.5** | low | nit | `b.go:1` - name\n\n  ````\n  x := ```\n  ````\n" +
		"- **R2.6** | question | spec - is the cap intended?\n"
	if got != want {
		t.Errorf("body:\n%s\nwant:\n%s", got, want)
	}
}

func TestReviewBodyCodeReviewLabel(t *testing.T) {
	rev := task.Review{Verdict: task.VerdictRequestChanges, Summary: "s", Prefix: "D", Comments: []task.ReviewComment{highBug}}
	got := reviewBody(task.ReviewerB, 1, rev)
	if !strings.HasPrefix(got, "**Code review 2 (round 1): request changes**") || !strings.Contains(got, "- **D1.1** | high") {
		t.Errorf("body = %q", got)
	}
}

// Verdicts written before prefixes (and older unsorted ones) render in
// file order, numbered with the instance letter.
func TestReviewBodyOldFilesWithoutPrefix(t *testing.T) {
	rev := task.Review{Verdict: task.VerdictApprove, Summary: "ok",
		Comments: []task.ReviewComment{task.Ungraded("b first"), task.Ungraded("a second")}}
	got := reviewBody(task.ReviewerB, 1, rev)
	want := "**Reviewer B (round 1): approve**\n\nok\n\n- **B1.1** b first\n- **B1.2** a second\n"
	if got != want {
		t.Errorf("body:\n%q\nwant:\n%q", got, want)
	}
	unsorted := task.Review{Verdict: task.VerdictRequestChanges, Comments: []task.ReviewComment{
		lowNit, task.Ungraded("plain"), highBug,
	}}
	got = renderReviewComments(task.ReviewerA, 1, unsorted)
	want = "- **A1.1** | low | nit - rename foo\n- **A1.2** | ungraded | plain\n- **A1.3** | high | correctness - nil deref (every run)\n  Trigger: any run\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEngineerResponseOutcomeTable(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	mustWriteReview(t, root, slug, task.ReviewerA, 1, task.Review{Verdict: task.VerdictRequestChanges, Summary: "s", Prefix: "R",
		Comments: []task.ReviewComment{highBug, medEdge, lowNit, askWhy}})
	resp := task.EngineerResponse{
		Addressed: []string{"R1.1: guarded the session", "bumped VERSION"},
		Pushback:  []string{"R1.3 rename churns callers"},
		Notes:     "ready for round 2",
		Followups: []string{"retry service: separate PR"},
		Outcomes: []task.CommentOutcome{
			{Reviewer: "A", Round: 1, Comment: 3, Outcome: task.OutcomeAnswered, Detail: "yes, the spec caps retries at 3"},
			{Reviewer: "A", Round: 1, Comment: 0, Outcome: task.OutcomeFixed, Detail: "guard nil session", SHA: "abc1234"},
			{Reviewer: "A", Round: 1, Comment: 2, Outcome: task.OutcomeDeclined, Detail: "rename churns | callers"},
			{Reviewer: "A", Round: 1, Comment: 1, Outcome: task.OutcomePushedBack},
			{Reviewer: "B", Round: 2, Comment: 0, Outcome: task.OutcomeFixed},
		},
	}
	var b strings.Builder
	writeEngineerResponse(&b, root, slug, resp)
	want := "| ID | Grade | Outcome | Details |\n| --- | --- | --- | --- |\n" +
		"| R1.1 | high | fixed | `abc1234` guard nil session |\n" +
		"| R1.2 | medium | pushed back |  |\n" +
		"| R1.3 | low | declined | rename churns \\| callers |\n" +
		"| R1.4 | question | answered | yes, the spec caps retries at 3 |\n" +
		"| B2.1 | ? | fixed |  |\n\n" +
		"Addressed:\n- bumped VERSION\n\n" +
		"Notes: ready for round 2\n\n" +
		"Proposed follow-ups:\n- retry service: separate PR\n\n"
	if got := b.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// An engineer response from before outcomes renders its lists as before.
func TestEngineerResponseWithoutOutcomes(t *testing.T) {
	var b strings.Builder
	writeEngineerResponse(&b, t.TempDir(), "demo", task.EngineerResponse{Addressed: []string{"A r1 #0 -> fixed"}, Pushback: []string{"no"}})
	if got, want := b.String(), "Addressed:\n- A r1 #0 -> fixed\n\nPushback:\n- no\n\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDeriveSnapshotSeverityCounts(t *testing.T) {
	root, slug := t.TempDir(), "demo"
	snap := walkPhases(t, root, slug, []phaseRow{
		{writeEng(1), PhaseReviewA, task.ReviewerA, 0},
		{writeGraded(task.ReviewerA, 1, task.VerdictRequestChanges, highBug, medEdge, lowNit), PhaseEngineerReviseA, task.ReviewerA, 1},
	})
	if got, want := snap.LastCounts, (task.GradeCounts{HighMedium: 2, Low: 1}); got != want {
		t.Errorf("counts = %+v, want %+v", got, want)
	}
}

// R1 mixed -> R2 medium -> R3 lows-only approve -> review-B, never
// escalated; B's R1 low costs a round, its R2 lows-only approve
// reaches done.
func TestDerivePhaseLowsCostOnlyRoundOne(t *testing.T) {
	A, B := task.ReviewerA, task.ReviewerB
	root, slug := t.TempDir(), "demo"
	walkPhases(t, root, slug, []phaseRow{
		{writeEng(1), PhaseReviewA, A, 0},
		{writeGraded(A, 1, task.VerdictRequestChanges, highBug, lowNit), PhaseEngineerReviseA, A, 1},
		{writeEng(2), PhaseReviewA, A, 1},
		{writeGraded(A, 2, task.VerdictRequestChanges, medEdge, lowNit), PhaseEngineerReviseA, A, 2},
		{writeEng(3), PhaseReviewA, A, 2},
		{writeGraded(A, 3, task.VerdictApprove, lowNit, askWhy), PhaseReviewB, B, 0},
		{writeGraded(B, 1, task.VerdictRequestChanges, lowNit), PhaseEngineerReviseB, B, 1},
		{writeEng(4), PhaseReviewB, B, 1},
		{writeGraded(B, 2, task.VerdictApprove, lowNit, askWhy), PhaseDone, B, 2},
	})
}

// A round-2 approve carrying lows and a question hands over to B and
// does not wake the engineer.
func TestDriveRoundTwoLowsApproveNoEngineerWake(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", t.TempDir())
	slug := "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	walkPhases(t, root, slug, []phaseRow{
		{writeEng(1), PhaseReviewA, task.ReviewerA, 0},
		{writeGraded(task.ReviewerA, 1, task.VerdictRequestChanges, lowNit), PhaseEngineerReviseA, task.ReviewerA, 1},
		{writeEng(2), PhaseReviewA, task.ReviewerA, 1},
		{writeGraded(task.ReviewerA, 2, task.VerdictApprove, lowNit, askWhy), PhaseReviewB, task.ReviewerB, 0},
	})
	stubPanes(t)
	outcome := wakeDelivered
	calls := stubWakeRole(t, &outcome)
	snap, err := DriveRoleLoop(root, tasksDir, slug)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Phase != PhaseReviewB {
		t.Fatalf("phase = %s, want %s", snap.Phase, PhaseReviewB)
	}
	for _, c := range *calls {
		if strings.HasSuffix(c.session, "/engineer") {
			t.Errorf("engineer woken on a lows-only approve: %+v", c)
		}
	}
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
		{writeGraded(task.ReviewerA, 2, task.VerdictRequestChanges, medEdge), PhaseEngineerReviseA, task.ReviewerA, 2},
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

// Operator force-approves A's final verdict BEFORE the fix pass
// lands: B's first request_changes is answered by the next engineer
// response; the loop must not wait for an extra round.
func TestDerivePhaseForceApproveBeforeFixPass(t *testing.T) {
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
	// The B revision lands strictly after B's verdict, whatever the
	// filesystem's timestamp granularity.
	engAfterB1 := func(t *testing.T, root, slug string) {
		writeEng(4)(t, root, slug)
		b1, err := os.Stat(task.ReviewPath(root, slug, B, 1))
		if err != nil {
			t.Fatal(err)
		}
		later := b1.ModTime().Add(time.Second)
		if err := os.Chtimes(task.EngineerResponsePath(root, slug, 4), later, later); err != nil {
			t.Fatal(err)
		}
	}
	walkPhases(t, root, slug, []phaseRow{
		{writeGraded(task.ReviewerA, 3, task.VerdictApprove), PhaseReviewB, B, 0},
		{writeGraded(B, 1, task.VerdictRequestChanges, lowNit), PhaseEngineerReviseB, B, 1},
		{engAfterB1, PhaseReviewB, B, 1},
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
	writeGraded(task.ReviewerB, 1, task.VerdictRequestChanges, lowNit)(t, root, slug)
	writeEng(2)(t, root, slug)
	writeGraded(task.ReviewerB, 2, task.VerdictRequestChanges, medEdge)(t, root, slug)
	writeEng(3)(t, root, slug)
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
	for _, s := range []string{`round 1 verdict "approve"`, "in round 1 any comment (high, medium, low or question) requires", `"request_changes"`} {
		if !strings.Contains(msg, s) {
			t.Errorf("msg missing %q:\n%s", s, msg)
		}
	}
}

// A fully ungraded (legacy) final verdict escalates with a fix-pass
// wake that does not mention grades it never had.
func TestDriveEscalationFixPassUngradedMessage(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", t.TempDir())
	slug := "demo"
	tasksDir := setupRoleLoopDriveFixture(t, root, slug)
	for i := 1; i <= MaxReviewerRounds; i++ {
		writeEng(i)(t, root, slug)
		writeGraded(task.ReviewerA, i, task.VerdictRequestChanges, task.Ungraded("fix it"))(t, root, slug)
	}
	stubPanes(t)
	outcome := wakeDelivered
	calls := stubWakeRole(t, &outcome)
	if _, err := DriveRoleLoop(root, tasksDir, slug); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("wakes = %+v, want one", *calls)
	}
	msg := (*calls)[0].msg
	if !strings.Contains(msg, "address its comments in one fix pass") || strings.Contains(msg, "high and medium") {
		t.Errorf("ungraded fix-pass msg = %s", msg)
	}
}
