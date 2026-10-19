package task

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerSessionArgsOmitAutoConnectIDE(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, "spore.toml"), []byte("[editor]\nauto_connect_ide = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_AUTO_CONNECT_IDE", "1")

	args, err := workerSessionArgs(root, "feature-x", "s", filepath.Join(root, ".worktrees", "feature-x"))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if strings.Contains(a, "CLAUDE_CODE_AUTO_CONNECT_IDE") {
			t.Errorf("worker spawn carries the auto-connect env: %v", args)
		}
	}
}
