package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/versality/spore/internal/evals"
	"github.com/versality/spore/internal/task"
)

func TestEvalFlagThenDismiss(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := filepath.Join(t.TempDir(), "demo-project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := task.WriteReview(root, "s", task.ReviewerB, 1, task.Review{Verdict: task.VerdictApprove, Summary: "0 blockers, 1 nit", Comments: []string{"[nit] rename x"}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPORE_PROJECT_ROOT", root)

	if code := runEval([]string{"flag", "s", "B", "1", "1", "--why", "x is the spec's name"}); code != 0 {
		t.Fatalf("flag exit %d", code)
	}
	st, err := evals.OpenStore(root)
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.Candidate("s.invalid.B1.op.c1")
	if err != nil {
		t.Fatal(err)
	}
	if c.Why != "x is the spec's name" {
		t.Errorf("why = %q", c.Why)
	}
	if code := runEval([]string{"dismiss", "s.invalid.B1.op.c1"}); code != 0 {
		t.Fatalf("dismiss exit %d", code)
	}
	if cs, _ := st.Candidates(); len(cs) != 0 {
		t.Errorf("dismissed candidate still open: %+v", cs)
	}
	if code := runEval([]string{"run"}); code != 1 {
		t.Errorf("run with no cases exit %d, want 1", code)
	}
	if code := runEval([]string{"bogus"}); code != 2 {
		t.Errorf("unknown subcommand exit %d, want 2", code)
	}
}
