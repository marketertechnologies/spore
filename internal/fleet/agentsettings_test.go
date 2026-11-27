package fleet

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	spore "github.com/versality/spore"
	"github.com/versality/spore/internal/task"
)

func writeAgentsRoleTask(t *testing.T, root, toml, frontmatter string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	fm := "---\nstatus: active\nslug: feature-x\nloop: role\n" + frontmatter + "---\nbody\n"
	if err := os.WriteFile(filepath.Join(root, "tasks", "feature-x.md"), []byte(fm), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "spore.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
}

func roleCommand(t *testing.T, spec RoleSpawnSpec) string {
	t.Helper()
	args, err := spec.TmuxArgs()
	if err != nil {
		t.Fatalf("%s TmuxArgs: %v", spec.Role, err)
	}
	return args[len(args)-1]
}

const roleAgentsTOML = `[agents.engineer]
model = "opus"
effort = "high"

[agents.reviewer]
model = "sonnet"

[agents.reviewer.B]
effort = "max"
`

func TestRoleSpawnPassesAgentSettings(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := newGitRepoFor(t, "demo-project")
	writeAgentsRoleTask(t, root, roleAgentsTOML, "")
	cases := []struct {
		spec RoleSpawnSpec
		want string
	}{
		{EngineerSpec(root, "feature-x"), "exec claude --dangerously-skip-permissions ${SPORE_SANDBOX_SETTINGS:+--settings \"$SPORE_SANDBOX_SETTINGS\"} '--model' 'opus' '--effort' 'high' -- "},
		{ReviewerSpec(root, "feature-x", task.ReviewerA), "exec claude --dangerously-skip-permissions ${SPORE_SANDBOX_SETTINGS:+--settings \"$SPORE_SANDBOX_SETTINGS\"} '--model' 'sonnet' -- "},
		{ReviewerSpec(root, "feature-x", task.ReviewerB), "exec claude --dangerously-skip-permissions ${SPORE_SANDBOX_SETTINGS:+--settings \"$SPORE_SANDBOX_SETTINGS\"} '--model' 'sonnet' '--effort' 'max' -- "},
	}
	for _, c := range cases {
		if cmd := roleCommand(t, c.spec); !strings.Contains(cmd, c.want) {
			t.Errorf("%s %s command = %q, want %q", c.spec.Role, c.spec.ReviewerInstance, cmd, c.want)
		}
	}
}

// The task frontmatter beats [agents.<role>], but only for panes on
// the task's own agent: a codex reviewer slot on a claude task keeps
// its config so a claude model id never reaches codex.
func TestRoleSpawnTaskOverride(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(task.CodexBinEnv, "")
	t.Setenv(task.CodexModelEnv, "")
	root := newGitRepoFor(t, "demo-project")
	toml := roleAgentsTOML + "\n[fleet.reviewers.A]\nagent = \"codex\"\n"
	writeAgentsRoleTask(t, root, toml, "model: fable\neffort: low\n")

	if cmd := roleCommand(t, EngineerSpec(root, "feature-x")); !strings.Contains(cmd, "'--model' 'fable' '--effort' 'low'") {
		t.Errorf("engineer command = %q, want the task's model and effort", cmd)
	}
	cmd := roleCommand(t, ReviewerSpec(root, "feature-x", task.ReviewerA))
	if !strings.Contains(cmd, "-m sonnet") || strings.Contains(cmd, "fable") {
		t.Errorf("codex reviewer command = %q, want config model sonnet and no task model", cmd)
	}
}

func TestRoleSpawnCodexEffortMaxFails(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := newGitRepoFor(t, "demo-project")
	writeAgentsRoleTask(t, root, "[agents.engineer]\neffort = \"max\"\n", "agent: codex\n")
	if _, err := EngineerSpec(root, "feature-x").TmuxArgs(); err == nil || !strings.Contains(err.Error(), "max") {
		t.Fatalf("err = %v, want codex to reject effort max", err)
	}
}

func TestTaskAgentLines(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := newGitRepoFor(t, "demo-project")
	writeAgentsRoleTask(t, root, roleAgentsTOML, "")
	lines, err := TaskAgentLines(root, "", "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range lines {
		got = append(got, l.Name+": "+l.String())
	}
	want := []string{
		"engineer: claude model=opus effort=high",
		"reviewer-A: claude model=sonnet effort=default",
		"reviewer-B: claude model=sonnet effort=max",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}

	t.Setenv(task.CodexModelEnv, "")
	if err := os.WriteFile(filepath.Join(root, "tasks", "plain.md"), []byte("---\nstatus: active\nslug: plain\nagent: codex\ncomplexity: light\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "spore.toml"), []byte("[agents.worker]\nmodel = \"gpt-5.5\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err = TaskAgentLines(root, "", "plain")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].Name+": "+lines[0].String() != "worker: codex model=gpt-5.5 effort=medium" {
		t.Errorf("worker lines = %+v", lines)
	}
}

// Status names each role pane's body file: an override with the branch
// its checkout is on, a bundled body with the spore version, or the
// error a broken override spawns with.
func TestTaskRoleBodyLines(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := newGitRepoFor(t, "demo-project")
	runGit(t, root, "symbolic-ref", "HEAD", "refs/heads/feature-roles")
	writeAgentsRoleTask(t, root, "[fleet.reviewers.B]\nmode = \"code-review\"\n", "")
	dir := filepath.Join(root, "bootstrap", "roles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "engineer.md"), []byte("custom engineer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reviewer.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := TaskRoleBodyLines(root, "", "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range lines {
		got = append(got, l.Name+": "+l.Source)
	}
	want := []string{
		"engineer: " + filepath.Join(dir, "engineer.md") + " (project override, checkout on branch feature-roles)",
		"reviewer-A: error: rolespawn: project role body " + filepath.Join(dir, "reviewer.md") + " is empty",
		"reviewer-B: bundled reviewer-code-review.md (spore " + spore.Version() + ")",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lines =\n%q\nwant\n%q", got, want)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "spore")); err == nil {
		t.Error("status materialized a bundled body")
	}

	if err := os.WriteFile(filepath.Join(root, "tasks", "plain.md"), []byte("---\nstatus: active\nslug: plain\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if lines, err := TaskRoleBodyLines(root, "", "plain"); err != nil || lines != nil {
		t.Errorf("plain task: lines = %v, err %v", lines, err)
	}
}

func TestCoordinatorAgentsTable(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("SPORE_COORDINATOR_MODEL", "")
	t.Setenv("SPORE_COORDINATOR_PROVIDER", "")
	t.Setenv(CoordinatorEffortEnv, "")
	root := newGitRepoFor(t, "demo-project")
	toml := "[coordinator]\ndriver = \"claude\"\nmodel = \"sonnet\"\n\n[agents.coordinator]\nmodel = \"opus\"\neffort = \"very-high\"\n"
	if err := os.WriteFile(filepath.Join(root, "spore.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadCoordinatorConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "opus" || cfg.Effort != "very-high" {
		t.Fatalf("cfg = %+v, want [agents.coordinator] model and effort", cfg)
	}
	args, err := coordinatorSessionArgs(root, "s", "/role.md", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := envValue(args, CoordinatorEffortEnv); got != "xhigh" {
		t.Errorf("%s = %q, want xhigh", CoordinatorEffortEnv, got)
	}
	cmd, err := coordinatorShellCommand("claude", "/role.md", coordinatorSettings(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, "exec claude --dangerously-skip-permissions ${SPORE_SANDBOX_SETTINGS:+--settings \"$SPORE_SANDBOX_SETTINGS\"} '--model' 'opus' '--effort' 'xhigh' -- ") {
		t.Errorf("claude command = %q", cmd)
	}
	cmd, err = coordinatorShellCommand("codex", "/role.md", task.AgentSettings{Model: "gpt-5.5", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, `'-m' 'gpt-5.5' '-c' 'model_reasoning_effort="high"' -- `) {
		t.Errorf("codex command = %q", cmd)
	}
	line, err := CoordinatorAgentLine(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := line.String(); got != "claude model=opus effort=xhigh" {
		t.Errorf("status line = %q", got)
	}
}

func TestCoordinatorUnsetAgentsKeepsArgs(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(CoordinatorEffortEnv, "")
	root := newGitRepoFor(t, "demo-project")
	args, err := coordinatorSessionArgs(root, "s", "/role.md", CoordinatorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if hasEnvArg(args, CoordinatorEffortEnv) {
		t.Errorf("unset effort leaked into the session env: %q", args)
	}
}

// A swap to the other driver drops effort (codex has no max); a swap
// that keeps the configured driver keeps it.
func TestDriverOverrideEffort(t *testing.T) {
	base := CoordinatorConfig{Model: "opus", Effort: "max"}
	if got := applyDriverOverride(base, CoordinatorConfig{Driver: "codex"}); got.Effort != "" || got.Model != "" {
		t.Errorf("to codex = %+v, want model and effort cleared", got)
	}
	if got := applyDriverOverride(base, CoordinatorConfig{Driver: "claude", Model: "fable"}); got.Effort != "max" || got.Model != "fable" {
		t.Errorf("same driver = %+v, want effort kept", got)
	}
}

// The launch shim is bash; pin that it hands claude the effort.
func TestCoordinatorLaunchShimClaudeEffort(t *testing.T) {
	raw, err := os.ReadFile("../../bootstrap/handover/spore-coordinator-launch.sh")
	if err != nil {
		t.Fatal(err)
	}
	if want := `args+=(--effort "$effort")`; !strings.Contains(string(raw), want) {
		t.Errorf("spore-coordinator-launch.sh claude args missing %s", want)
	}
}
