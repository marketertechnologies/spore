package migrations

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestCodexConsumerPrep(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "proj")
	writeFile(t, filepath.Join(home, ".config/systemd/user/spore-fleet-reconcile-tasks-proj.path"),
		"[Path]\nPathChanged="+proj+"/tasks\nUnit=spore-fleet-reconcile-proj.service\n")
	writeFile(t, filepath.Join(home, ".config/spore/coordinator-role.md"), "# shared role\n")
	writeFile(t, filepath.Join(proj, "spec/seeds/CLAUDE.md"), "seeds rules\n")
	writeFile(t, filepath.Join(proj, "bootstrap/coordinator/role.md"), "# role\n")

	out := runCodexPrep(t, home)

	if _, err := os.Stat(filepath.Join(proj, "spec/seeds/AGENTS.md")); !os.IsNotExist(err) {
		t.Errorf("consumer AGENTS.md written, want consumer repo untouched (err=%v)", err)
	}
	if got := readFile(t, filepath.Join(proj, "bootstrap/coordinator/role.md")); got != "# role\n" {
		t.Errorf("consumer role.md changed: %q", got)
	}
	shared := readFile(t, filepath.Join(home, ".config/spore/coordinator-role.md"))
	if !strings.Contains(shared, "\n## Codex coordinator\n") {
		t.Errorf("shared role missing Codex section:\n%s", shared)
	}
	if strings.TrimSpace(out) != "spore migrate 003: appended Codex coordinator section to "+filepath.Join(home, ".config/spore/coordinator-role.md") {
		t.Errorf("first run output:\n%s", out)
	}

	out = runCodexPrep(t, home)
	if strings.TrimSpace(out) != "spore migrate 003: host already prepared for Codex" {
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
