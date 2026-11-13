package fleet

import (
	"strings"
	"testing"

	"github.com/versality/spore/internal/task"
)

func TestReviewBodyGradedBlockingFirst(t *testing.T) {
	rev := task.Review{
		Verdict: task.VerdictRequestChanges,
		Summary: "one blocker",
		Comments: []task.ReviewComment{
			{Severity: "low", Category: "nit", Text: "rename foo"},
			{Severity: "medium", Category: "edge-case", Text: "empty list", Reason: "rare"},
			{Severity: "medium", Category: "correctness", Where: "a.go:3", Text: "off by one", Reason: "every page"},
			{Severity: "high", Category: "security", Text: "token logged", Reason: "leaks creds"},
		},
	}
	got := reviewBody(task.ReviewerA, 2, rev)
	want := "**Review #1 (round 2): request changes**\n\none blocker\n\n" +
		"- **high** / security / blocking - token logged (leaks creds)\n" +
		"- **medium** / correctness / blocking / `a.go:3` - off by one (every page)\n" +
		"- **medium** / edge-case - empty list (rare)\n" +
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

func TestReviewBodyUngradedInGradedReviewBlocks(t *testing.T) {
	rev := task.Review{Verdict: task.VerdictRequestChanges, Comments: []task.ReviewComment{
		{Severity: "low", Category: "nit", Text: "nit"},
		task.Ungraded("plain"),
	}}
	got := renderReviewComments(rev)
	want := "- **ungraded** / blocking - plain\n- **low** / nit - nit\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDeriveSnapshotCountsBlocking(t *testing.T) {
	root := t.TempDir()
	slug := "demo"
	if err := task.EnsureLegacyMarker(root, slug); err != nil {
		t.Fatal(err)
	}
	if err := task.WriteEngineerResponse(root, slug, 1, task.EngineerResponse{}); err != nil {
		t.Fatal(err)
	}
	mustWriteReview(t, root, slug, task.ReviewerA, 1, task.Review{Verdict: task.VerdictRequestChanges, Summary: "s",
		Comments: []task.ReviewComment{
			{Severity: "high", Category: "correctness", Text: "x", Reason: "r"},
			{Severity: "medium", Category: "edge-case", Text: "y", Reason: "r"},
			{Severity: "low", Category: "nit", Text: "z"},
		}})
	snap, err := DeriveSnapshot(root, slug)
	if err != nil {
		t.Fatal(err)
	}
	if snap.LastBlocking != 1 || snap.LastNonBlocking != 2 {
		t.Errorf("counts = %d blocking, %d non-blocking; want 1, 2", snap.LastBlocking, snap.LastNonBlocking)
	}
}

// A grade/verdict mismatch is bounced like any other invalid file, and
// the wake names the broken rule.
func TestBounceNamesVerdictRule(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", t.TempDir())
	slug := "demo"
	if err := task.EnsureLegacyMarker(root, slug); err != nil {
		t.Fatal(err)
	}
	path := task.ReviewPath(root, slug, task.ReviewerA, 1)
	writeRaw(t, path, `{"author":"A","verdict":"request_changes","summary":"s","comments":[{"severity":"low","category":"edge-case","text":"rare race"}]}`)

	outcome := wakeNoSession
	calls := stubWakeRole(t, &outcome)
	if err := bounceInvalidRoleFiles(root, slug); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %+v, want one wake", *calls)
	}
	msg := (*calls)[0].msg
	for _, s := range []string{`verdict "request_changes" but no comment is blocking`, "medium security/correctness/spec/data-loss", `must be "approve"`} {
		if !strings.Contains(msg, s) {
			t.Errorf("msg missing %q:\n%s", s, msg)
		}
	}
}
