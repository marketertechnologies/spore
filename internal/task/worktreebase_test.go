package task

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newBaseRepo returns a repo with main pushed to a bare origin, plus
// the commit of a second change pushed to origin only, so local main
// is one commit behind origin/main.
func newBaseRepo(t *testing.T) (repo, localHead, originHead string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	repo = t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	remote := configureOrigin(t, repo)

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, t.TempDir(), "clone", "-q", "-b", "main", remote, other)
	runGit(t, other, "config", "user.email", "test@example.com")
	runGit(t, other, "config", "user.name", "Test")
	runGit(t, other, "commit", "-q", "--allow-empty", "-m", "upstream")
	runGit(t, other, "push", "-q", "origin", "main")

	localHead = gitOutput(t, repo, "rev-parse", "HEAD")
	originHead = gitOutput(t, other, "rev-parse", "HEAD")
	if err := os.MkdirAll(filepath.Join(repo, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo, localHead, originHead
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	fn()
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

func ensureDemo(t *testing.T, repo string) string {
	t.Helper()
	wt, err := EnsureWorktree(filepath.Join(repo, "tasks"), "demo")
	if err != nil {
		t.Fatalf("EnsureWorktree: %v", err)
	}
	return wt
}

func TestWorktreeFreshBaseFromOrigin(t *testing.T) {
	repo, _, originHead := newBaseRepo(t)
	ensureDemo(t, repo)
	if got := gitOutput(t, repo, "rev-parse", "wt/demo"); got != originHead {
		t.Errorf("wt/demo = %s, want origin head %s", got, originHead)
	}
	cmd := exec.Command("git", "-C", repo, "config", "--get", "branch.wt/demo.merge")
	if out, err := cmd.Output(); err == nil {
		t.Errorf("wt/demo tracks %s, want no upstream", strings.TrimSpace(string(out)))
	}
}

func TestWorktreeFreshBaseUsesOriginHEAD(t *testing.T) {
	repo, _, _ := newBaseRepo(t)
	trunk := gitOutput(t, repo, "commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "trunk")
	runGit(t, repo, "push", "-q", "origin", trunk+":refs/heads/trunk")
	runGit(t, repo, "fetch", "-q", "origin")
	runGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	ensureDemo(t, repo)
	if got := gitOutput(t, repo, "rev-parse", "wt/demo"); got != trunk {
		t.Errorf("wt/demo = %s, want origin/trunk %s", got, trunk)
	}
}

func TestWorktreeOfflineFallsBackToHead(t *testing.T) {
	repo, localHead, _ := newBaseRepo(t)
	runGit(t, repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	stderr := captureStderr(t, func() { ensureDemo(t, repo) })
	if got := gitOutput(t, repo, "rev-parse", "wt/demo"); got != localHead {
		t.Errorf("wt/demo = %s, want local HEAD %s", got, localHead)
	}
	if !strings.Contains(stderr, "branching from HEAD") || strings.Count(strings.TrimSpace(stderr), "\n") != 0 {
		t.Errorf("stderr = %q, want one fallback warning line", stderr)
	}
}

func TestWorktreeNoRemoteFallsBackToHead(t *testing.T) {
	repo, localHead, _ := newBaseRepo(t)
	runGit(t, repo, "remote", "remove", "origin")
	stderr := captureStderr(t, func() { ensureDemo(t, repo) })
	if got := gitOutput(t, repo, "rev-parse", "wt/demo"); got != localHead {
		t.Errorf("wt/demo = %s, want local HEAD %s", got, localHead)
	}
	if !strings.Contains(stderr, "no origin remote") {
		t.Errorf("stderr = %q, want no-remote warning", stderr)
	}
}

func TestWorktreeHeadOptOut(t *testing.T) {
	repo, localHead, _ := newBaseRepo(t)
	toml := "[fleet]\nworktree_base = \"head\"\n"
	if err := os.WriteFile(filepath.Join(repo, "spore.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LoadWorktreeBase(repo); got != WorktreeBaseHead {
		t.Fatalf("LoadWorktreeBase = %q, want head", got)
	}
	ensureDemo(t, repo)
	if got := gitOutput(t, repo, "rev-parse", "wt/demo"); got != localHead {
		t.Errorf("wt/demo = %s, want local HEAD %s", got, localHead)
	}
}

func TestWorktreeReusesExistingBranch(t *testing.T) {
	repo, localHead, _ := newBaseRepo(t)
	runGit(t, repo, "branch", "wt/demo")
	ensureDemo(t, repo)
	if got := gitOutput(t, repo, "rev-parse", "wt/demo"); got != localHead {
		t.Errorf("wt/demo = %s, want untouched %s", got, localHead)
	}
}

func TestLoadWorktreeBaseDefault(t *testing.T) {
	if got := LoadWorktreeBase(t.TempDir()); got != WorktreeBaseFresh {
		t.Errorf("LoadWorktreeBase without spore.toml = %q, want fresh", got)
	}
}

func TestWorktreeIncludeCopiesIgnoredMatches(t *testing.T) {
	repo, _, _ := newBaseRepo(t)
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", ".env\n.env.local\nconfig/master.key\nnode_modules/\nsecrets/\n.worktrees/\n")
	write("config/app.yml", "tracked\n")
	runGit(t, repo, "add", ".gitignore", "config/app.yml")
	runGit(t, repo, "commit", "-q", "-m", "ignore")
	runGit(t, repo, "push", "-q", "-f", "origin", "main")
	write(".worktreeinclude", "# secrets\n.env*\n/config/master.key\nsecrets/*.pem\n!.env.local\n")
	write(".env", "A=1\n")
	write(".env.local", "B=2\n")
	write("config/master.key", "key\n")
	write("node_modules/pkg/.env", "nested\n")
	write("secrets/a.pem", "pem\n")
	write("secrets/b.txt", "txt\n")
	write("notes.txt", "untracked, not ignored\n")

	wt := ensureDemo(t, repo)
	for _, rel := range []string{".env", "config/master.key", "secrets/a.pem"} {
		got, err := os.ReadFile(filepath.Join(wt, rel))
		if err != nil {
			t.Errorf("%s not copied: %v", rel, err)
			continue
		}
		want, _ := os.ReadFile(filepath.Join(repo, rel))
		if string(got) != string(want) {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if info, err := os.Stat(filepath.Join(wt, ".env")); err == nil && info.Mode().Perm() != 0o600 {
		t.Errorf(".env mode = %v, want 0600", info.Mode().Perm())
	}
	for _, rel := range []string{".env.local", "node_modules/pkg/.env", "secrets/b.txt", "notes.txt", ".worktreeinclude"} {
		if _, err := os.Stat(filepath.Join(wt, rel)); err == nil {
			t.Errorf("%s copied, want skipped", rel)
		}
	}
}

func TestIncludePatternMatches(t *testing.T) {
	cases := []struct {
		pat, rel string
		isDir    bool
		want     bool
	}{
		{".env", ".env", false, true},
		{".env", "app/.env", false, true},
		{"/.env", "app/.env", false, false},
		{"config/*.key", "config/master.key", false, true},
		{"config/*.key", "app/config/master.key", false, false},
		{"storage/", "storage", true, true},
		{"storage/", "storage", false, false},
		{"storage/", "storage/x.db", false, true},
		{"vendor", "vendor/a/b", false, true},
	}
	for _, c := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, WorktreeIncludeFile), []byte(c.pat+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		pats, err := readWorktreeInclude(dir)
		if err != nil || len(pats) != 1 {
			t.Fatalf("readWorktreeInclude(%q) = %v, %v", c.pat, pats, err)
		}
		if got := pats[0].matches(c.rel, c.isDir); got != c.want {
			t.Errorf("%q matches %q (dir=%v) = %v, want %v", c.pat, c.rel, c.isDir, got, c.want)
		}
	}
}

func TestRemoteDefaultBranchFallsBackToMaster(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "master")
	if got := remoteDefaultBranch(repo); got != "main" {
		t.Errorf("remoteDefaultBranch on unborn master = %q, want main", got)
	}
	runGit(t, repo, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-q", "--allow-empty", "-m", "init")
	if got := remoteDefaultBranch(repo); got != "master" {
		t.Errorf("remoteDefaultBranch with only master = %q, want master", got)
	}
}
