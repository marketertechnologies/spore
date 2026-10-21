package task

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeSporeToml(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "spore.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPreStart(t *testing.T) {
	root := t.TempDir()
	if got := LoadPreStart(root); got != "" {
		t.Errorf("missing spore.toml: got %q", got)
	}
	writeSporeToml(t, root, "[fleet]\npre_start = \"  bin/with-services up  \"\n")
	if got := LoadPreStart(root); got != "bin/with-services up" {
		t.Errorf("got %q, want trimmed command", got)
	}
}

func TestWorkerShellCommandUnsetIsAgent(t *testing.T) {
	if got := workerShellCommand(PreStartPrelude("/p", "s", ""), "claude-code"); got != "claude-code" {
		t.Errorf("unset pre_start changed the agent command: %q", got)
	}
}

func TestWorkerShellCommandWrapsAgent(t *testing.T) {
	cmd := `echo 'it''s' && true`
	got := workerShellCommand(PreStartPrelude("/p", "feat", cmd), "claude-code")
	for _, want := range []string{
		"mkdir -p /p/.spore/feat/state && flock /p/.spore/feat/state/pre-start.lock sh -c ",
		shellQuote(cmd),
		`exec claude-code ${spore_pre_start_note:+"$spore_pre_start_note"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
}

func requireFlock(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock not on PATH")
	}
}

func TestWorkerShellCommandRuns(t *testing.T) {
	requireFlock(t)
	cases := []struct {
		name, cmd, want string
	}{
		{"success", `printf "%s" 'it'"'"'s' > ran`, ""},
		{"failure", `echo 'quote'"'"'d' >/dev/null; exit 3`, `spore: pre-start hook failed (exit 3): echo 'quote'"'"'d' >/dev/null; exit 3. Services may be down.`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			script := workerShellCommand(PreStartPrelude(root, "feat", c.cmd), "printf %s")
			cmd := exec.Command("sh", "-c", script)
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("run: %v: %s", err, out)
			}
			if string(out) != c.want {
				t.Errorf("agent got %q, want %q", out, c.want)
			}
			if c.name == "success" {
				if b, err := os.ReadFile(filepath.Join(root, "ran")); err != nil || string(b) != "it's" {
					t.Errorf("pre_start did not run in cwd: %q, %v", b, err)
				}
			}
			if _, err := os.Stat(PreStartLockPath(root, "feat")); err != nil {
				t.Errorf("lock file: %v", err)
			}
		})
	}
}
