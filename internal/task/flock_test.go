package task

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A running reconcile pass holds the project lock; Start waits for it
// instead of racing the pass on `git worktree add`, and gives up with a
// retry hint when the pass outlives the wait.
func TestStartWaitsForReconcileLock(t *testing.T) {
	root := t.TempDir()
	tasksDir := filepath.Join(root, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(tasksDir, "x.md")
	if err := os.WriteFile(taskPath, []byte("---\nstatus: draft\nslug: x\ntitle: X\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unlock, err := LockReconcile(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockReconcile(root, 0); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("second LockReconcile = %v, want ErrLockHeld", err)
	}

	prev := startLockWait
	startLockWait = 200 * time.Millisecond
	t.Cleanup(func() { startLockWait = prev })
	_, err = Start(tasksDir, "x")
	if err == nil || !strings.Contains(err.Error(), "retry") {
		t.Fatalf("Start under a held lock = %v, want a retry error", err)
	}
	if raw, _ := os.ReadFile(taskPath); !strings.Contains(string(raw), "status: draft") {
		t.Errorf("Start flipped status without the lock:\n%s", raw)
	}

	unlock()
	again, err := LockReconcile(root, time.Second)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again()
}
