package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/versality/spore/internal/task"
)

func TestStampReviewedSHAsSkipsVerdictsOlderThanHead(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, ".worktrees", "demo")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@example.invalid")
	runGit(t, wt, "init", "-q")
	runGit(t, wt, "commit", "-q", "--allow-empty", "-m", "c")
	head := runGit(t, wt, "rev-parse", "HEAD")

	for r, v := range []string{task.VerdictRequestChanges, task.VerdictApprove} {
		if err := task.WriteReview(root, "demo", task.ReviewerA, r+1, task.Review{Verdict: v, Summary: "s", Comments: []string{"[blocker] x"}}); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(task.ReviewPath(root, "demo", task.ReviewerA, 1), old, old); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(task.ReviewPath(root, "demo", task.ReviewerA, 2), future, future); err != nil {
		t.Fatal(err)
	}

	stampReviewedSHAs(root, "demo")

	if _, err := os.Stat(task.ReviewedSHAPath(root, "demo", task.ReviewerA, 1)); err == nil {
		t.Error("stamped a verdict older than the worktree HEAD")
	}
	b, err := os.ReadFile(task.ReviewedSHAPath(root, "demo", task.ReviewerA, 2))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != head {
		t.Errorf("stamp = %q, want %s", b, head)
	}
}
