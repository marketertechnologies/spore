package task

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/versality/spore/internal/task/frontmatter"
)

func TestWorkerAgentCommandCodexUsesEffortPolicy(t *testing.T) {
	t.Setenv("SPORE_AGENT_BINARY", "")
	t.Setenv(CodexBinEnv, "")
	m := frontmatter.Meta{
		Agent: "codex",
		Extra: map[string]string{
			"effort": "very-high",
			"model":  "gpt-5.5",
		},
	}
	got, err := workerAgentCommand(m)
	if err != nil {
		t.Fatalf("workerAgentCommand: %v", err)
	}
	want := "codex --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust --no-alt-screen --disable apps -m gpt-5.5 -c 'model_reasoning_effort=\"xhigh\"' -c 'project_doc_fallback_filenames=[\"CLAUDE.md\"]'"
	if got != want {
		t.Errorf("command = %q want %q", got, want)
	}
}

func TestWorkerAgentCommandOverrideIsClaudeOnly(t *testing.T) {
	t.Setenv("SPORE_AGENT_BINARY", "/usr/local/bin/spore-worker-brief")
	t.Setenv(CodexBinEnv, "")
	t.Setenv(CodexModelEnv, "")
	for _, agent := range []string{"", "claude", "claude-code"} {
		got, err := workerAgentCommand(frontmatter.Meta{Agent: agent})
		if err != nil {
			t.Fatalf("workerAgentCommand(%q): %v", agent, err)
		}
		if got != "/usr/local/bin/spore-worker-brief" {
			t.Errorf("agent %q: command = %q want override", agent, got)
		}
	}
	got, err := workerAgentCommand(frontmatter.Meta{Agent: "codex"})
	if err != nil {
		t.Fatalf("workerAgentCommand: %v", err)
	}
	if !strings.HasPrefix(got, "codex --dangerously-bypass-approvals-and-sandbox ") {
		t.Errorf("codex command = %q, want codex argv (override must not apply)", got)
	}
}

func TestWorkerAgentCommandCodexBin(t *testing.T) {
	t.Setenv(CodexBinEnv, "/opt/codex/bin/codex")
	got, err := workerAgentCommand(frontmatter.Meta{Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "/opt/codex/bin/codex --dangerously-bypass-approvals-and-sandbox ") {
		t.Errorf("command = %q, want SPORE_CODEX_BIN as argv0", got)
	}
}

// The codex worker must start on the brief, `--` first so its leading
// `---` is not parsed as a flag, with a pre-start note ahead of it.
func TestCodexWorkerShellCommandPassesBrief(t *testing.T) {
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	brief := "---\nslug: x\n---\nDo the thing.\n"
	if err := os.WriteFile(filepath.Join(wt, "tasks", "x.md"), []byte(brief), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(prelude, rel string) string {
		t.Helper()
		script := codexWorkerShellCommand(prelude, `printf '<%s>'`, rel)
		cmd := exec.Command("sh", "-c", script)
		cmd.Dir = wt
		cmd.Env = append(os.Environ(), PreStartNoteVar+"=")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("sh: %v", err)
		}
		return string(out)
	}
	if got, want := run("", briefRelPath("tasks", "x")), "<-->"+"<"+strings.TrimSuffix(brief, "\n")+">"; got != want {
		t.Errorf("brief run = %q want %q", got, want)
	}
	if got := run("", "tasks/missing.md"); got != "<>" {
		t.Errorf("no-brief run = %q want bare exec", got)
	}
	got := run(PreStartNoteVar+"=hook-failed; ", briefRelPath("tasks", "x"))
	if !strings.HasPrefix(got, "<--><hook-failed\n\n---\nslug: x") {
		t.Errorf("note run = %q, want note then brief", got)
	}
}
