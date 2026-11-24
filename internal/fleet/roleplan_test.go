package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/versality/spore/internal/task"
	"github.com/versality/spore/internal/task/frontmatter"
)

func TestAgentArgvMatchesBareInvocation(t *testing.T) {
	for _, agent := range []string{"claude", "claude --model x", "codex", "/opt/codex --foo", "sleep 30"} {
		_, bare := agentInvocations(agent)
		if got := strings.Join(agentArgv(agent), " "); strings.ReplaceAll(bare, "'", "") != got {
			t.Errorf("agentArgv(%q) = %q, live pane runs %q", agent, got, bare)
		}
	}
}

func TestPlanCodexSlotArgvMatchesSpawn(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(task.CodexModelEnv, "")
	root := newGitRepoFor(t, "demo-project")
	if err := os.WriteFile(filepath.Join(root, "spore.toml"), []byte("[fleet.reviewers.B]\nagent = \"codex\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tasks", "feature-x.md"), []byte("---\nstatus: active\nmodel: gpt-x\neffort: low\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := ReviewerSpec(root, "feature-x", task.ReviewerB)
	plan, err := spec.Plan()
	if err != nil {
		t.Fatal(err)
	}
	want, err := task.CodexAgentArgs(frontmatter.Meta{Extra: map[string]string{"model": "gpt-x", "effort": "low"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(plan.Argv, " ") != strings.Join(want, " ") {
		t.Fatalf("plan argv = %q, want %q", plan.Argv, want)
	}
	cmd, _ := task.CodexAgentCommand(frontmatter.Meta{Extra: map[string]string{"model": "gpt-x", "effort": "low"}})
	if sh := shellOf(t, spec); !strings.Contains(sh, cmd) {
		t.Errorf("live pane does not run %q: %s", cmd, sh)
	}
}
