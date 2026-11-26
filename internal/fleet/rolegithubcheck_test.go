package fleet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/versality/spore/internal/task"
)

func (f *mirrorFixture) configureCheck(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, "spore.toml"), []byte("[fleet.roles.engineer]\ncheck = \"just check\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// remoteAtCalls makes the fake gh log origin's branch head on every
// call; the returned func pairs each call with that head.
func (f *mirrorFixture) remoteAtCalls(t *testing.T) func() [][2]string {
	t.Helper()
	t.Setenv("FAKE_GH_REMOTE", f.bare)
	return func() [][2]string {
		var out [][2]string
		for _, l := range strings.Split(f.log(t), "\n") {
			if a, ok := strings.CutPrefix(l, "ARGS: "); ok {
				out = append(out, [2]string{a, ""})
			} else if r, ok := strings.CutPrefix(l, "REMOTE: "); ok && len(out) > 0 {
				out[len(out)-1][1] = r
			}
		}
		return out
	}
}

// With an engineer check configured, an engineer round reaches GitHub
// (push, PR, reply) only once the check recorded a result for the
// worktree HEAD; a pass at an older commit does not count. The push
// lands before the reply.
func TestGitHubMirrorHoldsEngineerRoundUntilCheckPasses(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	f.configureCheck(t)
	remote := f.remoteAtCalls(t)
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

	writeLegacyReview(t, f.root, f.slug, task.ReviewerA, 1, task.Review{Verdict: task.VerdictRequestChanges, Summary: "needs tests", Prefix: "R", Comments: []task.ReviewComment{task.Ungraded("add a test")}})
	f.tick(t)
	before := len(f.calls(t))
	writeEng(2)(t, f.root, f.slug)
	touchMarker(t, task.CheckPassPath(f.root, f.slug, 2, head1))
	head2 := f.commit(t, "two")
	f.tick(t)
	if st, got := f.state(t), f.calls(t); len(got) != before || f.bareHead(t) != head1 || st.posted("engineer-round-2.json") {
		t.Fatalf("round 2 mirrored without a result at HEAD: %v", got[before:])
	}
	touchMarker(t, task.CheckPassPath(f.root, f.slug, 2, head2))
	f.tick(t)
	got := remote()[before:]
	if len(got) != 1 || got[0] != [2]string{"pr comment 12 --body-file -", head2} || f.bareHead(t) != head2 {
		t.Fatalf("round 2 after pass: calls (with origin head) %v, want the reply after %s was pushed", got, head2)
	}
}

// A round the check gave up on is still mirrored, so the PR records
// it, but a later round stays held until its own check records a
// result. An unposted give-up round is held with it: its push would
// carry the later, unchecked HEAD.
func TestGitHubMirrorPostsGaveUpRound(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	f.configureCheck(t)
	reject := task.Review{Verdict: task.VerdictRequestChanges, Summary: "again", Prefix: "R", Comments: []task.ReviewComment{task.Ungraded("fix")}}
	writeEng(1)(t, f.root, f.slug)
	head1 := runGit(t, f.wt, "rev-parse", "HEAD")
	touchMarker(t, task.CheckFailedPath(f.root, f.slug, 1, head1))
	f.tick(t)
	if st := f.state(t); !st.posted("engineer-round-1.json") || st.PRNumber != 12 || f.bareHead(t) != head1 {
		t.Fatalf("given-up round 1 not mirrored: posted %v, calls %v", st.Posted, f.calls(t))
	}
	writeLegacyReview(t, f.root, f.slug, task.ReviewerA, 1, reject)
	f.tick(t)

	head2 := f.commit(t, "two")
	writeEng(2)(t, f.root, f.slug)
	touchMarker(t, task.CheckFailedPath(f.root, f.slug, 2, head2))
	f.tick(t)
	if st := f.state(t); !st.posted("engineer-round-2.json") || f.bareHead(t) != head2 {
		t.Fatalf("given-up round 2 not mirrored: posted %v, origin head %s (head2 %s)", st.Posted, f.bareHead(t), head2)
	}

	// Round 3 gives up with no tick in between, then round 4 is
	// written: both wait for round 4's result.
	writeLegacyReview(t, f.root, f.slug, task.ReviewerA, 2, reject)
	f.tick(t)
	head3 := f.commit(t, "three")
	writeEng(3)(t, f.root, f.slug)
	touchMarker(t, task.CheckFailedPath(f.root, f.slug, 3, head3))
	mustWriteReview(t, f.root, f.slug, task.ReviewerA, 3, task.Review{Verdict: task.VerdictApprove, Summary: "ok", Prefix: "R"})
	writeLegacyReview(t, f.root, f.slug, task.ReviewerB, 1, reject)
	head4 := f.commit(t, "four")
	writeEng(4)(t, f.root, f.slug)
	f.tick(t)
	if st := f.state(t); st.posted("engineer-round-3.json") || st.posted("A/round-3.json") || f.bareHead(t) != head2 {
		t.Fatalf("held rounds leaked: posted %v, origin head %s (head2 %s)", st.Posted, f.bareHead(t), head2)
	}
	touchMarker(t, task.CheckPassPath(f.root, f.slug, 4, head4))
	f.tick(t)
	st := f.state(t)
	for _, name := range []string{"engineer-round-3.json", "A/round-3.json", "B/round-1.json", "engineer-round-4.json"} {
		if !st.posted(name) {
			t.Errorf("%s not posted after round 4 passed: %v", name, st.Posted)
		}
	}
	if f.bareHead(t) != head4 {
		t.Errorf("origin head = %s, want %s", f.bareHead(t), head4)
	}
}

// A fix committed after a failed check is what the reviewer reads once
// the re-run passes. The mirror pushes it before posting the review,
// and leaves the engineer's later, unchecked revision commit local.
func TestGitHubMirrorPushesReviewedCommitBeforeReview(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	f.configureCheck(t)
	remote := f.remoteAtCalls(t)
	writeEng(1)(t, f.root, f.slug)
	head1 := runGit(t, f.wt, "rev-parse", "HEAD")
	touchMarker(t, task.CheckPassPath(f.root, f.slug, 1, head1))
	f.tick(t)
	fixed := f.commit(t, "fix")
	f.tick(t)
	if f.bareHead(t) != head1 {
		t.Fatalf("unchecked fix pushed")
	}
	touchMarker(t, task.CheckPassPath(f.root, f.slug, 1, fixed))
	writeLegacyReview(t, f.root, f.slug, task.ReviewerA, 1, task.Review{Verdict: task.VerdictRequestChanges, Summary: "needs tests", Prefix: "R", Comments: []task.ReviewComment{task.Ungraded("add a test")}})
	f.commit(t, "revision")
	before := len(f.calls(t))
	f.tick(t)
	got := remote()[before:]
	if len(got) != 1 || got[0] != [2]string{reviewCall, fixed} || f.bareHead(t) != fixed {
		t.Fatalf("review calls (with origin head) %v, origin head %s; want the review after %s was pushed", got, f.bareHead(t), fixed)
	}
}

// With no check configured a review pushes nothing: origin moves only
// with engineer rounds, as before the gate existed.
func TestGitHubMirrorNoCheckReviewDoesNotPush(t *testing.T) {
	f := newMirrorFixture(t, "draft")
	writeEng(1)(t, f.root, f.slug)
	f.tick(t)
	head1 := f.bareHead(t)
	f.commit(t, "revision")
	writeLegacyReview(t, f.root, f.slug, task.ReviewerA, 1, task.Review{Verdict: task.VerdictRequestChanges, Summary: "needs tests", Prefix: "R", Comments: []task.ReviewComment{task.Ungraded("add a test")}})
	f.tick(t)
	if st := f.state(t); !st.posted("A/round-1.json") || f.bareHead(t) != head1 {
		t.Fatalf("posted %v, origin head %s, want %s", st.Posted, f.bareHead(t), head1)
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
		writeLegacyReview(t, f.root, f.slug, task.ReviewerA, r, reject)
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
