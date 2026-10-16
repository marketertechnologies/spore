package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodeWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not available: %v", err)
	}
	root := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-q", "--allow-empty", "-m", "init"},
		{"worktree", "add", "-q", "-b", "wt/alpha", filepath.Join(root, ".worktrees", "alpha")},
	} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	wsPath := filepath.Join(root, ".spore", "spore.code-workspace")

	var out, errb bytes.Buffer
	if rc := code([]string{"workspace", "--print"}, filepath.Join(root, ".worktrees", "alpha"), &out, &errb); rc != 0 {
		t.Fatalf("--print rc=%d stderr=%s", rc, errb.String())
	}
	if !strings.Contains(out.String(), `"name": "proj"`) || !strings.Contains(out.String(), `"../.worktrees/alpha"`) {
		t.Errorf("--print output:\n%s", out.String())
	}
	if _, err := os.Stat(wsPath); !os.IsNotExist(err) {
		t.Errorf("--print wrote the file: %v", err)
	}

	out.Reset()
	if rc := code([]string{"workspace"}, root, &out, &errb); rc != 0 {
		t.Fatalf("write rc=%d stderr=%s", rc, errb.String())
	}
	if strings.TrimSpace(out.String()) != wsPath {
		t.Errorf("stdout = %q, want %q", out.String(), wsPath)
	}
	if _, err := os.Stat(wsPath); err != nil {
		t.Errorf("workspace not written: %v", err)
	}

	if rc := code([]string{"bogus"}, root, &out, &errb); rc != 2 {
		t.Errorf("unknown subcommand rc=%d, want 2", rc)
	}
}
