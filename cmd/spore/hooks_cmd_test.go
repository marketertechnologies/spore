package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHooksNotifyCoordinatorNoArgsUsesEnv(t *testing.T) {
	state := t.TempDir()
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", state)
	t.Setenv("WT_PROJECT", "project")
	t.Setenv("SPORE_TASK_INBOX", filepath.Join(t.TempDir(), "worker", "inbox"))

	if code := runHooksNotifyCoordinator(nil); code != 0 {
		t.Fatalf("runHooksNotifyCoordinator(nil) = %d, want 0", code)
	}
	entries, err := os.ReadDir(filepath.Join(state, "project", "inbox"))
	if err != nil {
		t.Fatalf("read coordinator inbox: %v", err)
	}
	found := false
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			found = true
		}
	}
	if !found {
		t.Fatal("notify-coordinator env mode did not write a json poke")
	}
}

func TestHooksWatchInboxNoArgsNoEnvSilentNoOp(t *testing.T) {
	t.Setenv("SPORE_TASK_INBOX", "")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	code := runHooksWatchInbox(nil)
	w.Close()
	stderr, _ := io.ReadAll(r)

	if code != 0 {
		t.Fatalf("runHooksWatchInbox(nil) = %d, want 0 with no slug and no SPORE_TASK_INBOX", code)
	}
	if len(stderr) != 0 {
		t.Fatalf("runHooksWatchInbox(nil) wrote stderr %q, want empty", stderr)
	}
}

func TestHooksSettingsLeavesOutManagedBindings(t *testing.T) {
	managed := filepath.Join(t.TempDir(), "50-spore.json")
	body := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/nix/store/x/bin/spore fleet replenish-hook"}]}]}}`
	if err := os.WriteFile(managed, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	in := `{"events":{"Stop":[{"command":"spore fleet replenish-hook","timeout":30},{"command":"bin/project-stop"}]}}`

	stdinR, stdinW, _ := os.Pipe()
	stdoutR, stdoutW, _ := os.Pipe()
	stderrR, stderrW, _ := os.Pipe()
	origIn, origOut, origErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = stdinR, stdoutW, stderrW
	defer func() { os.Stdin, os.Stdout, os.Stderr = origIn, origOut, origErr }()
	_, _ = io.WriteString(stdinW, in)
	stdinW.Close()

	code := runHooksSettings([]string{"--managed-file", managed})
	stdoutW.Close()
	stderrW.Close()
	out, _ := io.ReadAll(stdoutR)
	errOut, _ := io.ReadAll(stderrR)

	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errOut)
	}
	if strings.Contains(string(out), "replenish-hook") || !strings.Contains(string(out), "bin/project-stop") {
		t.Errorf("settings output:\n%s", out)
	}
	if !strings.Contains(string(errOut), "Stop: spore fleet replenish-hook runs from "+managed) {
		t.Errorf("stderr = %q", errOut)
	}
}
