package task

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/versality/spore/internal/task/frontmatter"
)

const agentsTOML = `[coordinator]
driver = "claude"

[agents.coordinator]
model = "opus"
effort = "max"

[agents.worker]
model = "sonnet"

[agents.reviewer]
model = "opus"
effort = "high"

[agents.reviewer.B]
effort = "low"

[agents.enginer]
model = "x"

[agents.engineer]
modle = "y"
`

func TestParseAgentsConfig(t *testing.T) {
	cfg, warnings, err := ParseAgentsConfig(agentsTOML)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		role, instance string
		want           AgentSettings
	}{
		{AgentRoleCoordinator, "", AgentSettings{"opus", "max"}},
		{AgentRoleWorker, "", AgentSettings{Model: "sonnet"}},
		{AgentRoleEngineer, "", AgentSettings{}},
		{AgentRoleReviewer, "A", AgentSettings{"opus", "high"}},
		{AgentRoleReviewer, "B", AgentSettings{"opus", "low"}},
	}
	for _, c := range cases {
		if got := cfg.For(c.role, c.instance); got != c.want {
			t.Errorf("For(%s, %q) = %+v, want %+v", c.role, c.instance, got, c.want)
		}
	}
	if len(warnings) != 2 ||
		!strings.Contains(warnings[0], "unknown section [agents.enginer]") ||
		!strings.Contains(warnings[1], `unknown key "modle" in [agents.engineer]`) {
		t.Errorf("warnings = %q", warnings)
	}
}

func TestParseAgentsConfigRejectsBadEffort(t *testing.T) {
	_, _, err := ParseAgentsConfig("[agents.worker]\neffort = \"huge\"\n")
	if err == nil || !strings.Contains(err.Error(), `effort "huge"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadAgentsConfigWarnsOnce(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "spore.toml"), []byte("[agents.workr]\nmodel = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	old := agentsWarnings
	agentsWarnings = &buf
	t.Cleanup(func() { agentsWarnings = old })
	for range 2 {
		if _, err := LoadAgentsConfig(root); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Count(buf.String(), "unknown section [agents.workr]"); got != 1 {
		t.Errorf("warning printed %d times: %q", got, buf.String())
	}
}

func writeAgentsProject(t *testing.T, toml string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "spore.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestWorkerAgentSettingsTaskBeatsConfig(t *testing.T) {
	root := writeAgentsProject(t, "[agents.worker]\nmodel = \"sonnet\"\neffort = \"high\"\n")
	got, err := WorkerAgentSettings(root, frontmatter.Meta{Extra: map[string]string{"effort": "very-high"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := (AgentSettings{"sonnet", "very-high"}); got != want {
		t.Errorf("settings = %+v, want %+v", got, want)
	}
}

func TestWorkerAgentCommandClaudeFlags(t *testing.T) {
	t.Setenv(AgentBinaryEnv, "")
	m := frontmatter.Meta{}
	s := AgentSettings{Model: "opus", Effort: "very-high"}
	got, err := workerAgentCommand(m, s)
	if err != nil {
		t.Fatal(err)
	}
	if want := "claude-code --model opus --effort xhigh"; got != want {
		t.Errorf("command = %q, want %q", got, want)
	}
	wantEnv := []string{"-e", "SPORE_AGENT_MODEL=opus", "-e", "SPORE_AGENT_EFFORT=xhigh"}
	if env := workerSettingsEnv(m, s); !reflect.DeepEqual(env, wantEnv) {
		t.Errorf("env = %q, want %q", env, wantEnv)
	}

	t.Setenv(AgentBinaryEnv, "/usr/local/bin/spore-worker-brief")
	if got, _ := workerAgentCommand(m, s); got != "/usr/local/bin/spore-worker-brief" {
		t.Errorf("launcher command = %q, want it bare (it reads the env)", got)
	}
	if env := workerSettingsEnv(frontmatter.Meta{Agent: "codex"}, s); env != nil {
		t.Errorf("codex worker env = %q, want none", env)
	}
}

func TestWorkerAgentCommandUnsetKeepsArgv(t *testing.T) {
	t.Setenv(AgentBinaryEnv, "")
	if got, _ := workerAgentCommand(frontmatter.Meta{}, AgentSettings{}); got != defaultAgentBinary {
		t.Errorf("command = %q, want %q", got, defaultAgentBinary)
	}
	if env := workerSettingsEnv(frontmatter.Meta{}, AgentSettings{}); env != nil {
		t.Errorf("env = %q, want none", env)
	}
}

func TestCodexEffectivePrecedence(t *testing.T) {
	t.Setenv(CodexModelEnv, "env-model")
	light := frontmatter.Meta{Extra: map[string]string{"complexity": "light"}}
	cases := []struct {
		name string
		s    AgentSettings
		want AgentSettings
	}{
		{"unset falls back to complexity and env", AgentSettings{}, AgentSettings{"env-model", "medium"}},
		{"config beats both", AgentSettings{"gpt-5.5", "xhigh"}, AgentSettings{"gpt-5.5", "xhigh"}},
	}
	for _, c := range cases {
		got, err := CodexEffective(light, c.s)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	if _, err := CodexFlags(AgentSettings{Effort: "max"}); err == nil {
		t.Error("codex effort max: want an error")
	}
}

// spore-worker-brief turns SPORE_AGENT_MODEL / SPORE_AGENT_EFFORT into
// claude flags ahead of the brief.
func TestWorkerBriefLauncherPassesSettings(t *testing.T) {
	script, err := filepath.Abs("../../bootstrap/handover/spore-worker-brief.sh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "stub-agent")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nfor a in \"$@\"; do printf '<%s>' \"$a\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks", "x.md"), []byte("brief"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(extra ...string) string {
		cmd := exec.Command("bash", script)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), append([]string{"SPORE_TASK_SLUG=x", "SPORE_WORKER_AGENT=" + stub}, extra...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return string(out)
	}
	if got, want := run("SPORE_AGENT_MODEL=", "SPORE_AGENT_EFFORT="), "<--dangerously-skip-permissions><--><brief>"; got != want {
		t.Errorf("unset: %q, want %q", got, want)
	}
	if got, want := run("SPORE_AGENT_MODEL=opus", "SPORE_AGENT_EFFORT=max"), "<--dangerously-skip-permissions><--model><opus><--effort><max><--><brief>"; got != want {
		t.Errorf("set: %q, want %q", got, want)
	}
}
