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
