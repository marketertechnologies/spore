package task

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func writeSporeToml(t *testing.T, repo, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "spore.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationBaseReadsFleetBase(t *testing.T) {
	repo := t.TempDir()
	writeSporeToml(t, repo, "[fleet]\nbase = \"release-1.2\"\n")
	if got := IntegrationBase(repo); got != "release-1.2" {
		t.Fatalf("IntegrationBase = %q, want release-1.2", got)
	}
}

func TestIntegrationBaseDefaultsToMain(t *testing.T) {
	// No spore.toml at all.
	if got := IntegrationBase(t.TempDir()); got != DefaultBase {
		t.Fatalf("IntegrationBase (no file) = %q, want %q", got, DefaultBase)
	}
	// spore.toml without a [fleet] base key.
	repo := t.TempDir()
	writeSporeToml(t, repo, "[coordinator]\nbrief = \"x\"\n")
	if got := IntegrationBase(repo); got != DefaultBase {
		t.Fatalf("IntegrationBase (no key) = %q, want %q", got, DefaultBase)
	}
}

// TestUnmergedCommitsNoLocalMain is the ROC-25 regression: in a repo
// that integrates on a feature branch with no local main or master, the
// old code fell back to `master` unconditionally and ran
// `git rev-list master..wt/<slug>`, which exits 128. It must now resolve
// the configured base and count commits without error.
func TestUnmergedCommitsNoLocalMain(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "release-9")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	// Worker branch with one commit ahead of the base.
	runGit(t, repo, "checkout", "-q", "-b", "wt/some-slug")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "work")
	runGit(t, repo, "checkout", "-q", "release-9")
	// No local main and no master exist; only the feature branch.
	writeSporeToml(t, repo, "[fleet]\nbase = \"release-9\"\n")

	n, err := UnmergedCommits(repo, "wt/some-slug")
	if err != nil {
		t.Fatalf("UnmergedCommits errored (the 128 regression): %v", err)
	}
	if n != 1 {
		t.Fatalf("UnmergedCommits = %d, want 1", n)
	}
}

// TestUnmergedCommitsUnresolvableBaseReturnsZero proves the fail-safe:
// when no base ref resolves at all, return 0 unmerged rather than
// running git against a missing ref (exit 128).
func TestUnmergedCommitsUnresolvableBaseReturnsZero(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "only-branch")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	runGit(t, repo, "checkout", "-q", "-b", "wt/some-slug")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "work")
	runGit(t, repo, "checkout", "-q", "only-branch")
	// base points at a branch that does not exist; no main/master/origin.
	writeSporeToml(t, repo, "[fleet]\nbase = \"does-not-exist\"\n")

	n, err := UnmergedCommits(repo, "wt/some-slug")
	if err != nil {
		t.Fatalf("UnmergedCommits errored on unresolvable base: %v", err)
	}
	if n != 0 {
		t.Fatalf("UnmergedCommits = %d, want 0 (no base resolves)", n)
	}
}
