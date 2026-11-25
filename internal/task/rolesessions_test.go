package task

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// spawnRoleSessions starts the engineer and reviewer panes the role
// loop would run for slug, plus one for a sibling slug sharing its
// prefix, on the package's isolated tmux server (see TestMain).
func spawnRoleSessions(t *testing.T, repo, slug string) (own []string, sibling string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("tmux not available: %v", err)
	}
	project := filepath.Base(repo)
	own = []string{
		"spore-role/" + project + "/" + slug + "/engineer",
		"spore-role/" + project + "/" + slug + "/reviewer-A",
	}
	sibling = "spore-role/" + project + "/" + slug + "-two/engineer"
	for _, name := range append(append([]string{}, own...), sibling) {
		if out, err := exec.Command("tmux", "-L", testTmuxSocket, "new-session", "-d", "-s", name, "sleep 30").CombinedOutput(); err != nil {
			t.Fatalf("tmux new-session %q: %v: %s", name, err, out)
		}
		t.Cleanup(func() { _ = exec.Command("tmux", "-L", testTmuxSocket, "kill-session", "-t", name).Run() })
	}
	return own, sibling
}

func assertRoleSessionsReaped(t *testing.T, own []string, sibling string) {
	t.Helper()
	for _, name := range own {
		if exec.Command("tmux", "-L", testTmuxSocket, "has-session", "-t", "="+name).Run() == nil {
			t.Errorf("role session %q still alive", name)
		}
	}
	if exec.Command("tmux", "-L", testTmuxSocket, "has-session", "-t", "="+sibling).Run() != nil {
		t.Errorf("sibling role session %q was killed", sibling)
	}
}

func TestDoneKillsRoleSessions(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	tasksDir := filepath.Join(repo, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "demo.md"), []byte("---\nstatus: active\nslug: demo\ntitle: Demo\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	own, sibling := spawnRoleSessions(t, repo, "demo")

	if err := Done(tasksDir, "demo", false); err != nil {
		t.Fatalf("Done: %v", err)
	}
	assertRoleSessionsReaped(t, own, sibling)
}

func TestMergeKillsRoleSessions(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	configureOrigin(t, repo)
	runGit(t, repo, "checkout", "-q", "-b", "wt/demo")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "feat: demo")
	runGit(t, repo, "checkout", "-q", "main")
	tasksDir := filepath.Join(repo, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	own, sibling := spawnRoleSessions(t, repo, "demo")

	if err := Merge(tasksDir, "demo"); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	assertRoleSessionsReaped(t, own, sibling)
}
