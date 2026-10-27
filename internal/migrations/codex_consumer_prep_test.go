package migrations

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const staleRole = `# Coordinator role

## Deltas

- **CODEOWNERS** in place.
- **No token-monitor Stop hook installed yet**. The shared coordinator
  protocol still applies, but the auto-fire infrastructure
  isn't wired in this project.
- **GitHub Packages PAT** is required.

## Tail
`

const cleanRole = `# Coordinator role

## Deltas

- **CODEOWNERS** in place.
- **GitHub Packages PAT** is required.

## Tail
`

func runCodexPrep(t *testing.T, home string) string {
	t.Helper()
	script, err := filepath.Abs("../../bootstrap/migrations/003-codex-consumer-prep.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-eu", script)
	cmd.Dir = home
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("migration 003: %v\n%s", err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestCodexConsumerPrep(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	home := t.TempDir()
	proj := filepath.Join(home, "proj")
	writeFile(t, filepath.Join(home, ".config/systemd/user/spore-fleet-reconcile-tasks-proj.path"),
		"[Path]\nPathChanged="+proj+"/tasks\nUnit=spore-fleet-reconcile-proj.service\n")
	writeFile(t, filepath.Join(home, ".config/spore/coordinator-role.md"), "# shared role\n")

	writeFile(t, filepath.Join(proj, "CLAUDE.md"), "root\n")
	writeFile(t, filepath.Join(proj, "spec/seeds/CLAUDE.md"), "seeds rules\n")
	writeFile(t, filepath.Join(proj, "lib/CLAUDE.md"), "lib rules\n")
	writeFile(t, filepath.Join(proj, "lib/AGENTS.md"), "hand-written\n")
	writeFile(t, filepath.Join(proj, "vendor/x/CLAUDE.md"), "vendored\n")
	writeFile(t, filepath.Join(proj, "bootstrap/coordinator/role.md"), staleRole)
	gitRun(t, proj, "init", "-q")
	gitRun(t, proj, "add", "-A")
	gitRun(t, proj, "commit", "-qm", "init")
	writeFile(t, filepath.Join(proj, "untracked/CLAUDE.md"), "untracked\n")

	out := runCodexPrep(t, home)

	if got := readFile(t, filepath.Join(proj, "spec/seeds/AGENTS.md")); got != "seeds rules\n" {
		t.Errorf("seeds AGENTS.md = %q, want exact CLAUDE.md copy", got)
	}
	if got := readFile(t, filepath.Join(proj, "lib/AGENTS.md")); got != "hand-written\n" {
		t.Errorf("existing lib/AGENTS.md overwritten: %q", got)
	}
	for _, rel := range []string{"AGENTS.md", "vendor/x/AGENTS.md", "untracked/AGENTS.md"} {
		if _, err := os.Stat(filepath.Join(proj, rel)); !os.IsNotExist(err) {
			t.Errorf("%s created, want skipped (err=%v)", rel, err)
		}
	}
	if got := readFile(t, filepath.Join(proj, "bootstrap/coordinator/role.md")); got != cleanRole {
		t.Errorf("role.md after migration:\n%s\nwant:\n%s", got, cleanRole)
	}
	shared := readFile(t, filepath.Join(home, ".config/spore/coordinator-role.md"))
	if !strings.Contains(shared, "\n## Codex coordinator\n") {
		t.Errorf("shared role missing Codex section:\n%s", shared)
	}
	if n := strings.Count(out, "spore migrate 003:"); n != 3 {
		t.Errorf("first run printed %d change lines, want 3:\n%s", n, out)
	}

	out = runCodexPrep(t, home)
	if strings.TrimSpace(out) != "spore migrate 003: fleet consumers already prepared for Codex" {
		t.Errorf("second run not a no-op:\n%s", out)
	}
	if got := readFile(t, filepath.Join(home, ".config/spore/coordinator-role.md")); got != shared {
		t.Errorf("second run changed shared role")
	}
}

func TestCodexConsumerPrep_noFleetUnits(t *testing.T) {
	home := t.TempDir()
	out := runCodexPrep(t, home)
	if !strings.Contains(out, "already prepared") {
		t.Errorf("empty host output = %q", out)
	}
}

func TestCodexConsumerPrep_sectionMatchesBundledRole(t *testing.T) {
	script := readFile(t, "../../bootstrap/migrations/003-codex-consumer-prep.sh")
	start := strings.Index(script, "<<'EOF'\n")
	end := strings.Index(script, "\nEOF\n")
	if start < 0 || end < start {
		t.Fatal("codex_section heredoc not found in migration 003")
	}
	section := script[start+len("<<'EOF'\n") : end+1]
	role := readFile(t, "../../bootstrap/coordinator/role.md")
	if !strings.Contains(role, section) {
		t.Errorf("bootstrap/coordinator/role.md lacks migration 003's Codex section verbatim:\n%s", section)
	}
}
