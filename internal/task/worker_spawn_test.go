package task

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/versality/spore/internal/task/frontmatter"
)

func writeProjectTOML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if content != "" {
		if err := os.WriteFile(filepath.Join(dir, "spore.toml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestWorkerLaunchCommandIsolationOff(t *testing.T) {
	t.Setenv(AgentBinaryEnv, "sleep 30")
	restore := stubLookPath(func(string) (string, error) {
		t.Fatal("lookPath called while isolation off")
		return "", nil
	})
	defer restore()

	// No spore.toml at all -> isolation defaults off.
	dir := writeProjectTOML(t, "")
	got, err := workerLaunchCommand(dir, "x", frontmatter.Meta{}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "sleep 30" {
		t.Fatalf("got %q, want unchanged %q", got, "sleep 30")
	}
}

func TestWorkerLaunchCommandIsolationOn(t *testing.T) {
	t.Setenv(AgentBinaryEnv, "sleep 30")
	restore := stubLookPath(func(string) (string, error) {
		return "/nix/store/x/bin/pasta", nil
	})
	defer restore()

	dir := writeProjectTOML(t, "[worker]\nisolate_network = true\n")
	got, err := workerLaunchCommand(dir, "x", frontmatter.Meta{}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Isolation on injects the sandbox-bypass env through the same seam so
	// claude survives pasta's uid-0 userns mapping, and carries the port-
	// forward disable flags so same-port workers stay off the host.
	want := "pasta --config-net -T none -U none -t none -u none -- env IS_SANDBOX=1 sleep 30"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWorkerLaunchCommandIsolationOnPastaMissing(t *testing.T) {
	t.Setenv(AgentBinaryEnv, "sleep 30")
	restore := stubLookPath(func(string) (string, error) {
		return "", os.ErrNotExist
	})
	defer restore()

	dir := writeProjectTOML(t, "[worker]\nisolate_network = true\n")
	if _, err := workerLaunchCommand(dir, "x", frontmatter.Meta{}, ""); err == nil {
		t.Fatal("want fail-loud error when pasta missing, got nil")
	}
}
