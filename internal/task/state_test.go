package task

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestStateDirXDG(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-spore-test")

	got, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	want := filepath.Join("/tmp/xdg-spore-test", "spore", filepath.Base(dir))
	if got != want {
		t.Errorf("StateDir = %q, want %q", got, want)
	}
}

func TestStateDirHomeFallback(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/tmp/home-spore-test")

	got, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	want := filepath.Join("/tmp/home-spore-test", ".local", "state", "spore", filepath.Base(dir))
	if got != want {
		t.Errorf("StateDir = %q, want %q", got, want)
	}
}

func TestStateDirNoHomeNoXDG(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")

	if _, err := StateDir(); err == nil {
		t.Fatal("StateDir: expected error when both HOME and XDG_STATE_HOME are empty, got nil")
	}
}

func TestCountUnreadInbox(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	n, _, err := CountUnreadInbox("foo")
	if err != nil {
		t.Fatalf("CountUnreadInbox (no dir): %v", err)
	}
	if n != 0 {
		t.Errorf("empty inbox = %d, want 0", n)
	}

	inbox, _ := InboxDir("foo")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "1.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "2.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inbox, "notes.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(inbox, "read"), 0o755); err != nil {
		t.Fatal(err)
	}

	n, _, err = CountUnreadInbox("foo")
	if err != nil {
		t.Fatalf("CountUnreadInbox: %v", err)
	}
	if n != 2 {
		t.Errorf("unread = %d, want 2", n)
	}
}

func TestInboxDir(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-spore-test")

	got, err := InboxDir("foo")
	if err != nil {
		t.Fatalf("InboxDir: %v", err)
	}
	want := filepath.Join("/tmp/xdg-spore-test", "spore", filepath.Base(dir), "foo", "inbox")
	if got != want {
		t.Errorf("InboxDir = %q, want %q", got, want)
	}
}

func TestInboxDirForProjectUsesProjectRootNotWorkerCwd(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	worker := filepath.Join(root, ".worktrees", "alpha")
	if err := os.MkdirAll(worker, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(worker)
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-spore-test")

	got, err := InboxDirForProject(root, "alpha")
	if err != nil {
		t.Fatalf("InboxDirForProject: %v", err)
	}
	want := filepath.Join("/tmp/xdg-spore-test", "spore", "project", "alpha", "inbox")
	if got != want {
		t.Errorf("InboxDirForProject = %q, want %q", got, want)
	}
}

func TestProjectNameResolvesMainRepoFromWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	parent := t.TempDir()
	main := filepath.Join(parent, "marketercom")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitRepo(t, main)
	worktree := filepath.Join(main, ".worktrees", "wt-slug-xyz")
	if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, main, "worktree", "add", "-q", worktree, "-b", "wt/slug")

	got, err := ProjectName(worktree)
	if err != nil {
		t.Fatalf("ProjectName(worktree): %v", err)
	}
	if got != "marketercom" {
		t.Errorf("ProjectName(worktree) = %q, want %q", got, "marketercom")
	}

	gotMain, err := ProjectName(main)
	if err != nil {
		t.Fatalf("ProjectName(main): %v", err)
	}
	if gotMain != "marketercom" {
		t.Errorf("ProjectName(main) = %q, want %q", gotMain, "marketercom")
	}

	t.Chdir(worktree)
	gotCwd, err := ProjectName("")
	if err != nil {
		t.Fatalf("ProjectName(\"\"): %v", err)
	}
	if gotCwd != "marketercom" {
		t.Errorf("ProjectName(\"\") from worktree cwd = %q, want %q", gotCwd, "marketercom")
	}
}

func TestStateDirForProjectFromWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	parent := t.TempDir()
	main := filepath.Join(parent, "marketercom")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitRepo(t, main)
	worktree := filepath.Join(main, ".worktrees", "wt-slug-xyz")
	if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, main, "worktree", "add", "-q", worktree, "-b", "wt/slug")

	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-spore-test")
	got, err := StateDirForProject(worktree)
	if err != nil {
		t.Fatalf("StateDirForProject(worktree): %v", err)
	}
	want := filepath.Join("/tmp/xdg-spore-test", "spore", "marketercom")
	if got != want {
		t.Errorf("StateDirForProject(worktree) = %q, want %q", got, want)
	}
}

func gitInitRepo(t *testing.T, repo string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		mustGit(t, repo, args...)
	}
}

func mustGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	full := append([]string{"-C", repo}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestCoordinatorInboxDirForProject(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-spore-test")
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", "")

	got, err := CoordinatorInboxDirForProject(root)
	if err != nil {
		t.Fatalf("CoordinatorInboxDirForProject: %v", err)
	}
	want := filepath.Join("/tmp/xdg-spore-test", "spore", "coordinator", "project", "inbox")
	if got != want {
		t.Errorf("CoordinatorInboxDirForProject = %q, want %q", got, want)
	}
}

func TestDefaultBranchPrefersMain(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")

	got, err := defaultBranch(repo)
	if err != nil {
		t.Fatalf("defaultBranch: %v", err)
	}
	if got != "main" {
		t.Errorf("defaultBranch = %q, want main", got)
	}
}

func TestDefaultBranchFallsBackToMaster(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "master")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")

	got, err := defaultBranch(repo)
	if err != nil {
		t.Fatalf("defaultBranch: %v", err)
	}
	if got != "master" {
		t.Errorf("defaultBranch = %q, want master", got)
	}
}

func TestDefaultBranchFallsBackToOriginHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	// Bare upstream with HEAD on development.
	upstream := t.TempDir()
	runGit(t, upstream, "init", "-q", "--bare", "-b", "development")
	// Local clone: no local main/master, only development.
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "development")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, repo, "remote", "add", "origin", upstream)
	runGit(t, repo, "push", "-q", "-u", "origin", "development")
	runGit(t, repo, "remote", "set-head", "origin", "development")

	got, err := defaultBranch(repo)
	if err != nil {
		t.Fatalf("defaultBranch: %v", err)
	}
	if got != "development" {
		t.Errorf("defaultBranch = %q, want development", got)
	}
}

func TestDefaultBranchFailsWhenNoCandidate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "feature")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")

	_, err := defaultBranch(repo)
	if err == nil {
		t.Fatal("defaultBranch should fail when no candidate resolves")
	}
}

func TestUnmergedCommitsResolvesAgainstOriginHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	upstream := t.TempDir()
	runGit(t, upstream, "init", "-q", "--bare", "-b", "development")
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "development")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, repo, "remote", "add", "origin", upstream)
	runGit(t, repo, "push", "-q", "-u", "origin", "development")
	runGit(t, repo, "remote", "set-head", "origin", "development")
	runGit(t, repo, "checkout", "-q", "-b", "wt/x")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "feature-1")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "feature-2")
	runGit(t, repo, "checkout", "-q", "development")

	n, err := UnmergedCommits(repo, "wt/x")
	if err != nil {
		t.Fatalf("UnmergedCommits: %v", err)
	}
	if n != 2 {
		t.Errorf("UnmergedCommits = %d, want 2", n)
	}
}
