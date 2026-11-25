package fleet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/versality/spore/internal/task"
)

func (f *mirrorFixture) configureCheck(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, "spore.toml"), []byte("[fleet.roles.engineer]\ncheck = \"just check\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// With an engineer check configured, an engineer round reaches GitHub
// (push and PR reply) only once its pass marker exists for the
// worktree HEAD; a give-up or a stale pass keeps the round off GitHub.
func TestGitHubMirrorHoldsEngineerRoundUntilCheckPasses(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	f.configureCheck(t)
	writeEng(1)(t, f.root, f.slug)
	f.tick(t)
	if c := f.calls(t); c != nil {
		t.Fatalf("round 1 mirrored before its check passed: %v", c)
	}
	if _, err := exec.Command("git", "-C", f.bare, "rev-parse", "--verify", "-q", "refs/heads/wt/demo").Output(); err == nil {
		t.Fatal("branch pushed before the check passed")
	}
	head1 := runGit(t, f.wt, "rev-parse", "HEAD")
	touchMarker(t, task.CheckPassPath(f.root, f.slug, 1, head1))
	f.tick(t)
	if f.bareHead(t) != head1 || f.state(t).PRNumber != 12 {
		t.Fatalf("round 1 not mirrored after the check passed: %v", f.calls(t))
	}

	mustWriteReview(t, f.root, f.slug, task.ReviewerA, 1, task.Review{Verdict: task.VerdictRequestChanges, Summary: "needs tests", Prefix: "R", Comments: []task.ReviewComment{task.Ungraded("add a test")}})
	f.tick(t)
	before := len(f.calls(t))
	writeEng(2)(t, f.root, f.slug)
	touchMarker(t, task.CheckPassPath(f.root, f.slug, 2, head1))
	head2 := f.commit(t, "two")
	f.tick(t)
	touchMarker(t, task.CheckFailedPath(f.root, f.slug, 2, head2))
	f.tick(t)
	if got := f.calls(t); len(got) != before || f.bareHead(t) != head1 {
		t.Fatalf("round 2 mirrored without a pass at HEAD: %v", got[before:])
	}
	if st := f.state(t); st.posted("engineer-round-2.json") {
		t.Fatal("round 2 marked posted while held")
	}
	touchMarker(t, task.CheckPassPath(f.root, f.slug, 2, head2))
	f.tick(t)
	if got := f.calls(t); len(got) != before+1 || got[before] != "pr comment 12 --body-file -" || f.bareHead(t) != head2 {
		t.Fatalf("round 2 after pass: calls %v, head %s", got[before:], f.bareHead(t))
	}
}

// A round the check gave up on stays off GitHub when the engineer
// writes the next round: posting it would push the new, unchecked
// HEAD. Both land once the next round passes at HEAD.
func TestGitHubMirrorHoldsGaveUpRoundUntilLaterRoundPasses(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	f.configureCheck(t)
	reject := task.Review{Verdict: task.VerdictRequestChanges, Summary: "again", Prefix: "R", Comments: []task.ReviewComment{task.Ungraded("fix")}}
	writeEng(1)(t, f.root, f.slug)
	head1 := runGit(t, f.wt, "rev-parse", "HEAD")
	touchMarker(t, task.CheckPassPath(f.root, f.slug, 1, head1))
	f.tick(t)
	mustWriteReview(t, f.root, f.slug, task.ReviewerA, 1, reject)
	f.tick(t)

	head2 := f.commit(t, "two")
	writeEng(2)(t, f.root, f.slug)
	touchMarker(t, task.CheckFailedPath(f.root, f.slug, 2, head2))
	f.tick(t)
	mustWriteReview(t, f.root, f.slug, task.ReviewerA, 2, reject)
	f.tick(t)
	head3 := f.commit(t, "three")
	writeEng(3)(t, f.root, f.slug)
	f.tick(t)
	if st := f.state(t); st.posted("engineer-round-2.json") || st.posted("A/round-2.json") || f.bareHead(t) != head1 {
		t.Fatalf("held rounds leaked: posted %v, origin head %s (head1 %s)", st.Posted, f.bareHead(t), head1)
	}

	touchMarker(t, task.CheckPassPath(f.root, f.slug, 3, head3))
	f.tick(t)
	st := f.state(t)
	for _, name := range []string{"engineer-round-2.json", "A/round-2.json", "engineer-round-3.json"} {
		if !st.posted(name) {
			t.Errorf("%s not posted after round 3 passed: %v", name, st.Posted)
		}
	}
	if f.bareHead(t) != head3 {
		t.Errorf("origin head = %s, want %s", f.bareHead(t), head3)
	}
}

// A give-up on the round the reviewer escalates on never gets a later
// passing round, so the escalated loop releases the hold: the round,
// the final verdict and the escalation notice reach the PR.
func TestGitHubMirrorEscalatedGiveUpReleasesHold(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	f.configureCheck(t)
	reject := task.Review{Verdict: task.VerdictRequestChanges, Summary: "again", Prefix: "R", Comments: []task.ReviewComment{task.Ungraded("fix")}}
	for r := 1; r <= MaxReviewerRounds; r++ {
		head := f.commit(t, fmt.Sprintf("r%d", r))
		writeEng(r)(t, f.root, f.slug)
		if r < MaxReviewerRounds {
			touchMarker(t, task.CheckPassPath(f.root, f.slug, r, head))
		} else {
			touchMarker(t, task.CheckFailedPath(f.root, f.slug, r, head))
		}
		f.tick(t)
		mustWriteReview(t, f.root, f.slug, task.ReviewerA, r, reject)
	}
	if snap := f.tick(t); snap.Phase != PhaseEscalated {
		t.Fatalf("phase = %s, want escalated", snap.Phase)
	}
	last := fmt.Sprintf("engineer-round-%d.json", MaxReviewerRounds)
	st := f.state(t)
	for _, name := range []string{last, fmt.Sprintf("A/round-%d.json", MaxReviewerRounds), fmt.Sprintf("escalated-A-%d", MaxReviewerRounds)} {
		if !st.posted(name) {
			t.Errorf("%s not posted while escalated: %v", name, st.Posted)
		}
	}

	// A fix-pass commit moves HEAD off the give-up: hold again.
	f.commit(t, "fix")
	writeEng(MaxReviewerRounds+1)(t, f.root, f.slug)
	f.tick(t)
	if st := f.state(t); st.posted(fmt.Sprintf("engineer-round-%d.json", MaxReviewerRounds+1)) {
		t.Errorf("unchecked fix pass posted: %v", st.Posted)
	}
}
