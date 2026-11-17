package task

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/versality/spore/internal/task/frontmatter"
)

// Given [fleet] pre_start, when a homogeneous worker's launch command
// runs, then the hook runs before the agent, as it does for loop panes.
func TestWorkerLaunchRunsPreStart(t *testing.T) {
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock not on PATH")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(AgentBinaryEnv, "printf agent-ran")
	flag := filepath.Join(t.TempDir(), "hook-ran")
	dir := writeProjectTOML(t, "[fleet]\npre_start = \"touch "+flag+"\"\n")

	cmd, err := workerLaunchCommand(dir, "feat", frontmatter.Meta{}, "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", "-c", cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("run %q: %v: %s", cmd, err, out)
	}
	if _, err := os.Stat(flag); err != nil {
		t.Errorf("pre_start hook never ran for a worker: %v (command %q)", err, cmd)
	}
	if string(out) != "agent-ran" {
		t.Errorf("agent output = %q, want %q", out, "agent-ran")
	}
}

// Given pre_start and isolate_network, when a worker's launch command
// is built, then the hook runs inside the network namespace with the
// agent, so services it starts are on the loopback the agent sees.
func TestWorkerLaunchRunsPreStartInsideNetns(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(AgentBinaryEnv, "sleep 30")
	defer stubLookPath(func(string) (string, error) { return "/nix/store/x/bin/pasta", nil })()
	dir := writeProjectTOML(t, "[worker]\nisolate_network = true\n[fleet]\npre_start = \"bin/up\"\n")

	cmd, err := workerLaunchCommand(dir, "feat", frontmatter.Meta{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cmd, "pasta ") || !strings.Contains(cmd, "flock") {
		t.Errorf("command %q: want the pre-start hook (flock) inside the pasta wrapper", cmd)
	}
	if !strings.Contains(cmd, "env IS_SANDBOX=1 sleep 30") {
		t.Errorf("command %q: want IS_SANDBOX=1 on the agent", cmd)
	}
}

// Given no pre_start, the worker launch command is unchanged.
func TestWorkerLaunchWithoutPreStartUnchanged(t *testing.T) {
	t.Setenv(AgentBinaryEnv, "sleep 30")
	defer stubLookPath(func(string) (string, error) { return "/nix/store/x/bin/pasta", nil })()
	dir := writeProjectTOML(t, "[worker]\nisolate_network = true\n")
	got, err := workerLaunchCommand(dir, "feat", frontmatter.Meta{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := "pasta --config-net -T none -U none -t none -u none -- env IS_SANDBOX=1 sleep 30"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Given isolate_network and a codex task, when the worker's launch
// command is built, then codex reads its brief inside the namespace.
func TestWorkerLaunchCodexInsideNetns(t *testing.T) {
	t.Setenv(AgentBinaryEnv, "")
	t.Setenv(CodexBinEnv, "codex")
	defer stubLookPath(func(string) (string, error) { return "/nix/store/x/bin/pasta", nil })()
	dir := writeProjectTOML(t, "[worker]\nisolate_network = true\n")

	got, err := workerLaunchCommand(dir, "feat", frontmatter.Meta{Agent: "codex"}, "tasks/feat.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "exec "+netnsWrapPrefix+"codex ") {
		t.Errorf("command %q: want codex exec'd through the pasta wrapper", got)
	}
	if !strings.Contains(got, "tasks/feat.md") {
		t.Errorf("command %q: want the brief path", got)
	}
}

// Given isolate_network without pasta on PATH, when a worker session
// is ensured, then it fails loud naming isolate_network and starts no
// tmux session.
func TestEnsureRefusesIsolatedWorkerWithoutPasta(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv(AgentBinaryEnv, "sleep 30")
	defer stubLookPath(func(string) (string, error) { return "", exec.ErrNotFound })()
	repo := writeProjectTOML(t, "[worker]\nisolate_network = true\n")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := gitCmd(repo, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	tasksDir := filepath.Join(repo, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "iso.md"), []byte("---\nstatus: active\nslug: iso\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Ensure(tasksDir, "iso")
	if err == nil || !strings.Contains(err.Error(), "isolate_network") {
		t.Fatalf("Ensure err = %v, want an isolate_network error", err)
	}
}
