package fleet

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/versality/spore/internal/task"
)

func TestRoleSpawnSpecEngineerArgs(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	spec := EngineerSpec(root, "feature-x")
	args, err := spec.TmuxArgs()
	if err != nil {
		t.Fatalf("TmuxArgs: %v", err)
	}

	wantSession := "spore-role/" + filepath.Base(root) + "/feature-x/engineer"
	if v := flagValue(args, "-s"); v != wantSession {
		t.Errorf("session = %q, want %q", v, wantSession)
	}
	if v := flagValue(args, "-c"); v != filepath.Join(root, ".worktrees", "feature-x") {
		t.Errorf("cwd = %q, want engineer worktree", v)
	}
	if e := envValue(args, "SPORE_ROLE"); e != "engineer" {
		t.Errorf("SPORE_ROLE = %q, want engineer", e)
	}
	if e := envValue(args, "SPORE_REVIEWER_INSTANCE"); e != "" {
		t.Errorf("SPORE_REVIEWER_INSTANCE set on engineer pane: %q", e)
	}
	if e := envValue(args, "SPORE_TASK_DIR"); e != task.RoleTaskDir(root, "feature-x") {
		t.Errorf("SPORE_TASK_DIR = %q, want %q", e, task.RoleTaskDir(root, "feature-x"))
	}
}

func TestRoleSpawnSpecReviewerArgs(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	for _, instance := range []task.ReviewerInstance{task.ReviewerA, task.ReviewerB} {
		spec := ReviewerSpec(root, "feature-x", instance)
		args, err := spec.TmuxArgs()
		if err != nil {
			t.Fatalf("TmuxArgs %s: %v", instance, err)
		}
		wantSession := "spore-role/" + filepath.Base(root) + "/feature-x/reviewer-" + string(instance)
		if v := flagValue(args, "-s"); v != wantSession {
			t.Errorf("%s session = %q, want %q", instance, v, wantSession)
		}
		if v := flagValue(args, "-c"); v != filepath.Join(root, ".worktrees", "feature-x") {
			t.Errorf("%s cwd = %q, want task worktree", instance, v)
		}
		if e := envValue(args, "SPORE_ROLE"); e != "reviewer" {
			t.Errorf("%s SPORE_ROLE = %q, want reviewer", instance, e)
		}
		if e := envValue(args, "SPORE_REVIEWER_INSTANCE"); e != string(instance) {
			t.Errorf("%s SPORE_REVIEWER_INSTANCE = %q, want %q", instance, e, instance)
		}
	}
}

func TestRoleSpawnSpecInjectsAccountTier(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	toml := "[fleet]\naccount_tier = \"max\"\n"
	if err := os.WriteFile(filepath.Join(root, "spore.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, spec := range []RoleSpawnSpec{
		EngineerSpec(root, "feature-x"),
		ReviewerSpec(root, "feature-x", task.ReviewerA),
	} {
		args, err := spec.TmuxArgs()
		if err != nil {
			t.Fatalf("TmuxArgs %s: %v", spec.Role, err)
		}
		if e := envValue(args, "SPORE_ACCOUNT_TIER"); e != "max" {
			t.Errorf("%s SPORE_ACCOUNT_TIER = %q, want max", spec.Role, e)
		}
	}
}

func TestRoleSpawnSpecOmitsTierWithoutKnob(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	args, err := EngineerSpec(root, "feature-x").TmuxArgs()
	if err != nil {
		t.Fatalf("TmuxArgs: %v", err)
	}
	if e := envValue(args, "SPORE_ACCOUNT_TIER"); e != "" {
		t.Errorf("SPORE_ACCOUNT_TIER = %q, want unset without the knob", e)
	}
}

func TestRoleSpawnSpecRejectsBadRole(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	spec := RoleSpawnSpec{ProjectRoot: root, Slug: "feature-x", Role: "qa"}
	if _, err := spec.TmuxArgs(); err == nil {
		t.Fatal("expected error on unknown role, got nil")
	}
}

func TestRoleSpawnSpecRejectsMissingInstance(t *testing.T) {
	root := newGitRepoFor(t, "demo-project")
	spec := RoleSpawnSpec{ProjectRoot: root, Slug: "feature-x", Role: "reviewer"}
	if _, err := spec.TmuxArgs(); err == nil {
		t.Fatal("expected error on reviewer with empty instance, got nil")
	}
}

// flagValue returns the argument following the named flag in argv, or
// the empty string when the flag is absent.
func flagValue(argv []string, flag string) string {
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	return ""
}

// envValue returns the value of KEY in `-e KEY=value` pairs, or the
// empty string when absent.
func envValue(argv []string, key string) string {
	prefix := key + "="
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] != "-e" {
			continue
		}
		if strings.HasPrefix(argv[i+1], prefix) {
			return strings.TrimPrefix(argv[i+1], prefix)
		}
	}
	return ""
}

// newGitRepoFor returns a tmpdir initialised as a git repo and
// renamed to projectName, so task.ProjectName can resolve the
// project basename via `git rev-parse --git-common-dir`. The rename
// keeps the repo basename stable across tmpdir randomness.
func newGitRepoFor(t *testing.T, projectName string) string {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, projectName)
	if err := exec.Command("mkdir", "-p", root).Run(); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return root
}
