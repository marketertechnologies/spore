package task

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirtyWorktreeNoteCapsAndSkipsBrief(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	if got := dirtyWorktreeNote(repo, "tasks/x.md"); got != "" {
		t.Errorf("clean worktree note = %q, want empty", got)
	}
	if err := os.MkdirAll(filepath.Join(repo, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "tasks", "x.md"), []byte("brief\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := dirtyWorktreeNote(repo, "tasks/x.md"); got != "" {
		t.Errorf("brief-only note = %q, want empty", got)
	}
	for i := range dirtyNoteMaxPaths + 3 {
		if err := os.WriteFile(filepath.Join(repo, fmt.Sprintf("f%02d.txt", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := dirtyWorktreeNote(repo, "tasks/x.md")
	for _, want := range []string{"uncommitted changes", "(23 paths)", "f00.txt", "f19.txt", "(and 3 more)"} {
		if !strings.Contains(got, want) {
			t.Errorf("note missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "f20.txt") || strings.Contains(got, "tasks/x.md") {
		t.Errorf("note lists capped or brief paths:\n%s", got)
	}
}

func TestEnsureRespawnOnDirtyWorktreeTellsWorker(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("tmux not available: %v", err)
	}
	repo := filepath.Join(t.TempDir(), "dirtyproj")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tasksDir := filepath.Join(repo, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "dn.md"), []byte("---\nstatus: active\nslug: dn\ntitle: DN\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-q", "-m", "init")

	promptFile := filepath.Join(t.TempDir(), "prompt")
	agent := filepath.Join(t.TempDir(), "agent")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"${1:-<none>}\" > %s.tmp && mv %s.tmp %s\nexec sleep 30\n", promptFile, promptFile, promptFile)
	if err := os.WriteFile(agent, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(AgentBinaryEnv, agent)

	session, err := Ensure(tasksDir, "dn")
	if err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", testTmuxSocket, "kill-session", "-t", "="+session).Run() })
	if got := waitPrompt(t, promptFile); got != "<none>" {
		t.Errorf("fresh worktree prompt = %q, want no note", got)
	}

	if err := exec.Command("tmux", "-L", testTmuxSocket, "kill-session", "-t", "="+session).Run(); err != nil {
		t.Fatalf("kill worker: %v", err)
	}
	_ = os.Remove(promptFile)
	worktree := filepath.Join(repo, ".worktrees", "dn")
	if err := os.WriteFile(filepath.Join(worktree, "tracked.txt"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "wip.go"), []byte("package wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Ensure(tasksDir, "dn"); err != nil {
		t.Fatalf("respawn Ensure: %v", err)
	}
	got := waitPrompt(t, promptFile)
	for _, want := range []string{"uncommitted changes", "tracked.txt", "wip.go"} {
		if !strings.Contains(got, want) {
			t.Errorf("respawn prompt missing %q:\n%s", want, got)
		}
	}
}

func waitPrompt(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			return string(b)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("agent never wrote %s", path)
	return ""
}
