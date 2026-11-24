package evals

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/versality/spore/internal/task"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// fixture is a synthetic project: main with one commit, wt/demo one
// commit ahead, and a .spore/demo/ tree where A requested changes,
// the engineer pushed back, A approved, then B requested changes.
type fixture struct {
	root, head, base string
	store            Store
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, root, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(root, "a.txt"), "one\n")
	gitT(t, root, "add", ".")
	gitT(t, root, "commit", "-q", "-m", "base")
	base := gitT(t, root, "rev-parse", "HEAD")
	gitT(t, root, "checkout", "-q", "-b", "wt/demo")
	writeFile(t, filepath.Join(root, "a.txt"), "two\n")
	gitT(t, root, "commit", "-qam", "change")
	head := gitT(t, root, "rev-parse", "HEAD")
	gitT(t, root, "checkout", "-q", "main")

	slug := "demo"
	if err := task.WriteSpec(root, slug, []byte("# demo spec\n")); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(task.WriteEngineerResponse(root, slug, 1, task.EngineerResponse{}))
	must(task.WriteReview(root, slug, task.ReviewerA, 1, task.Review{Verdict: task.VerdictRequestChanges, Summary: "1 blocker", Comments: []string{"[blocker] x.go:1: rename breaks callers"}}))
	must(task.WriteEngineerResponse(root, slug, 2, task.EngineerResponse{Pushback: []string{"rename -> no callers exist", "skipped: [nit] wording"}}))
	must(task.WriteReview(root, slug, task.ReviewerA, 2, task.Review{Verdict: task.VerdictApprove, Summary: "0 blockers"}))
	must(task.WriteReview(root, slug, task.ReviewerB, 1, task.Review{Verdict: task.VerdictRequestChanges, Summary: "1 blocker, 1 nit", Comments: []string{"[blocker] spec step 2 unmet", "[nit] typo"}}))
	must(task.WriteEngineerResponse(root, slug, 3, task.EngineerResponse{Addressed: []string{"step 2"}}))

	st, err := OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{root: root, head: head, base: base, store: st}
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ids(cs []Candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func TestCandidatesDetectMissAndPushback(t *testing.T) {
	f := newFixture(t)
	cs, err := f.store.Candidates()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"demo.invalid.A1.eng2.p1", "demo.miss.A2.B1"}
	if got := ids(cs); !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	inv, miss := cs[0], cs[1]
	if inv.Reviewer != task.ReviewerA || inv.Round != 1 || inv.Trigger != 1 || inv.Source != SourceEngineer {
		t.Errorf("invalid candidate = %+v", inv)
	}
	if miss.Reviewer != task.ReviewerA || miss.Round != 2 || miss.Trigger != 2 || miss.Source != "reviewer:B" {
		t.Errorf("miss candidate = %+v", miss)
	}
	if !reflect.DeepEqual(miss.Evidence, []string{"[blocker] spec step 2 unmet"}) {
		t.Errorf("miss evidence = %v, want only the blocker", miss.Evidence)
	}
	if !strings.Contains(miss.DraftExpect(), "spec step 2 unmet") {
		t.Errorf("draft expect = %q", miss.DraftExpect())
	}
}

func TestFlagBecomesCandidate(t *testing.T) {
	f := newFixture(t)
	id, err := f.store.AddFlag("demo", task.ReviewerB, 1, "typo", "cosmetic, already fixed upstream")
	if err != nil {
		t.Fatal(err)
	}
	if id != "demo.invalid.B1.op.c2" {
		t.Fatalf("flag id = %q", id)
	}
	c, err := f.store.Candidate(id)
	if err != nil {
		t.Fatal(err)
	}
	if c.Source != SourceOperator || c.Trigger != 2 || c.Evidence[0] != "[nit] typo" {
		t.Errorf("flag candidate = %+v", c)
	}
	if !strings.Contains(c.DraftExpect(), "cosmetic, already fixed upstream") {
		t.Errorf("draft expect drops --why: %q", c.DraftExpect())
	}
	if _, err := f.store.AddFlag("demo", task.ReviewerB, 1, "9", ""); err == nil {
		t.Error("out-of-range comment ref accepted")
	}
	if _, err := f.store.AddFlag("demo", task.ReviewerB, 1, "[", ""); err == nil {
		t.Error("ambiguous comment ref accepted")
	}
}

func TestDismissHidesCandidate(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Dismiss("demo.miss.A2.B1"); err != nil {
		t.Fatal(err)
	}
	cs, err := f.store.Candidates()
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(cs); !reflect.DeepEqual(got, []string{"demo.invalid.A1.eng2.p1"}) {
		t.Fatalf("after dismiss: %v", got)
	}
	if err := f.store.Dismiss("demo.miss.A2.B1"); err == nil {
		t.Error("dismissing a closed candidate succeeded")
	}
}

func TestConfirmWritesCaseFromStamp(t *testing.T) {
	f := newFixture(t)
	writeFile(t, task.ReviewedSHAPath(f.root, "demo", task.ReviewerA, 2), f.head+"\n")
	c, err := f.store.Confirm(context.Background(), "demo.miss.A2.B1", ConfirmOptions{ConfirmedBy: "op"})
	if err != nil {
		t.Fatal(err)
	}
	if c.HeadSHA != f.head || c.BaseSHA != f.base || c.BaseBranch != "main" || c.Round != 2 || c.Reviewer != "A" {
		t.Errorf("case = %+v", c)
	}
	if !strings.HasPrefix(c.Dir, f.store.CasesDir()) {
		t.Errorf("case dir %s outside host store %s", c.Dir, f.store.CasesDir())
	}
	for _, rel := range []string{"case.toml", "spec.md", "original-verdict.json", "head.bundle",
		"responses/engineer-round-1.json", "responses/engineer-round-2.json", "reviews/A/round-1.json"} {
		if _, err := os.Stat(filepath.Join(c.Dir, rel)); err != nil {
			t.Errorf("case dir missing %s: %v", rel, err)
		}
	}
	for _, rel := range []string{"responses/engineer-round-3.json", "reviews/A/round-2.json", "reviews/B"} {
		if _, err := os.Stat(filepath.Join(c.Dir, rel)); err == nil {
			t.Errorf("case dir leaks %s past the replayed round", rel)
		}
	}
	loaded, err := LoadCase(c.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Expect != c.Expect || loaded.HeadSHA != c.HeadSHA {
		t.Errorf("reloaded case differs: %+v", loaded)
	}
	cs, _ := f.store.Candidates()
	if got := ids(cs); !reflect.DeepEqual(got, []string{"demo.invalid.A1.eng2.p1"}) {
		t.Errorf("confirmed candidate still open: %v", got)
	}
}

func TestConfirmRecoversSHAFromBranchByMtime(t *testing.T) {
	f := newFixture(t)
	verdict := task.ReviewPath(f.root, "demo", task.ReviewerA, 1)
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(verdict, future, future); err != nil {
		t.Fatal(err)
	}
	c, err := f.store.Confirm(context.Background(), "demo.invalid.A1.eng2.p1", ConfirmOptions{Expect: "no rename comment"})
	if err != nil {
		t.Fatal(err)
	}
	if c.HeadSHA != f.head || c.Expect != "no rename comment" {
		t.Errorf("case = %+v", c)
	}
	past := time.Unix(1000, 0)
	if err := os.Chtimes(task.ReviewPath(f.root, "demo", task.ReviewerA, 2), past, past); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Confirm(context.Background(), "demo.miss.A2.B1", ConfirmOptions{}); err == nil {
		t.Error("verdict older than every branch commit recovered a sha")
	}
}

func TestConfirmKernelUsesRelativeRepo(t *testing.T) {
	f := newFixture(t)
	c, err := f.store.Confirm(context.Background(), "demo.miss.A2.B1", ConfirmOptions{HeadSHA: f.head, Kernel: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.Repo != "." || !strings.HasPrefix(c.Dir, filepath.Join(f.root, "evals", "cases")) {
		t.Fatalf("kernel case = %+v", c)
	}
	loaded, err := LoadCase(c.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RepoRoot() != f.root {
		t.Errorf("RepoRoot = %s, want %s", loaded.RepoRoot(), f.root)
	}
}

func TestCaseRoundtrip(t *testing.T) {
	now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	c := Case{ID: "x.miss.A1.B1", Kind: KindMiss, Project: "p", SourceSlug: "x", Reviewer: "A", Round: 1,
		Created: now, Repo: "/r", BaseBranch: "main", BaseSHA: "b", HeadSHA: "h",
		Expect: "a \"quoted\" defect\nover two lines", Source: "reviewer:B", ConfirmedAt: now, Rule: "r.md"}
	got, err := ParseCase(c.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("roundtrip:\n got %+v\nwant %+v", got, c)
	}
	if _, err := ParseCase([]byte("id = \"x\"\nbogus = \"y\"\n")); err == nil {
		t.Error("unknown key accepted")
	}
}
