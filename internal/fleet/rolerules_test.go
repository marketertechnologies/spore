package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/versality/spore/internal/task"
)

func TestSpawnAppendsProjectRulesForTheBody(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := newGitRepoFor(t, "demo-project")
	spec := ReviewerSpec(root, "feature-x", task.ReviewerA)
	plan, err := spec.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if plan.Body != "reviewer-code-review.md" || plan.Slot.Mode != ReviewerModeCodeReview || plan.Slot.Effort != DefaultReviewEffort {
		t.Fatalf("plan = %+v", plan)
	}
	bare, err := ResolveRoleBody(root, plan.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := shellOf(t, spec); !strings.Contains(got, bare) {
		t.Fatalf("no rules: pane should read the plain body %s; got %s", bare, got)
	}

	dir, err := RoleRulesDir(root, plan.Body)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"20-second.md": "Second rule.\n",
		"10-first.md":  "\nFirst rule.\n\n",
		"notes.txt":    "not a rule",
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path, err := ResolveRoleBodyWithRules(root, plan.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := shellOf(t, spec); !strings.Contains(got, path) {
		t.Fatalf("pane should read the composed body %s; got %s", path, got)
	}
	composed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := os.ReadFile(bare)
	want := strings.TrimRight(string(base), "\n") + "\n\n## Project rules\n\nFirst rule.\n\nSecond rule.\n"
	if string(composed) != want {
		t.Errorf("composed tail = %q", string(composed)[len(base)-1:])
	}

	eng, err := ResolveRoleBodyWithRules(root, "engineer.md")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(eng); strings.Contains(string(b), "Project rules") {
		t.Error("reviewer rules leaked into the engineer body")
	}
}

func shellOf(t *testing.T, spec RoleSpawnSpec) string {
	t.Helper()
	args, err := spec.TmuxArgs()
	if err != nil {
		t.Fatal(err)
	}
	return args[len(args)-1]
}
