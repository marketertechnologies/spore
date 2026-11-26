package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrLockHeld is what a lock helper returns when another process holds
// the lock. Any other error means the lock could not be taken at all.
var ErrLockHeld = errors.New("lock held")

// FlockNB takes a non-blocking exclusive flock on path, creating it
// and its parent. The returned func releases it.
func FlockNB(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLockHeld
		}
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// ReconcileLockPath is the per-project lock a fleet reconcile pass and
// `spore task start` hold while they create branches, worktrees and
// sessions. Keyed on the main repo, so a worktree cwd shares it.
func ReconcileLockPath(projectRoot string) (string, error) {
	dir, err := StateDirForProject(projectRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "fleet", "reconcile.lock"), nil
}

// LockReconcile takes the project's reconcile lock, polling for up to
// wait while another holder has it (0 means one try). Returns
// ErrLockHeld when the wait runs out.
func LockReconcile(projectRoot string, wait time.Duration) (func(), error) {
	path, err := ReconcileLockPath(projectRoot)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		unlock, err := FlockNB(path)
		if !errors.Is(err, ErrLockHeld) || !time.Now().Before(deadline) {
			return unlock, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
