package fleet

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/versality/spore/internal/task"
)

// errLockHeld is what a lock helper returns when another process holds
// the lock. Any other error means the lock could not be taken at all.
var errLockHeld = errors.New("lock held")

// lockGitHubState takes a non-blocking flock beside the state file so
// two drives never post the same event twice.
func lockGitHubState(statePath string) (func(), error) {
	return flockNB(statePath + ".lock")
}

// lockRoleDrive serialises drive ticks and `spore task reopen` /
// `force-approve` on one slug, so none of them acts on a half-moved
// tree. The lock file sits outside state/, which reopen archives.
func lockRoleDrive(projectRoot, slug string) (func(), error) {
	return flockNB(filepath.Join(task.RoleTaskDir(projectRoot, slug), "drive.lock"))
}

func flockNB(path string) (func(), error) {
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
			return nil, errLockHeld
		}
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// lockRefusal words a failed lock for the operator: retry when another
// process holds it, the underlying error otherwise.
func lockRefusal(op, slug, holder string, err error) error {
	if errors.Is(err, errLockHeld) {
		return fmt.Errorf("%s %s: %s; retry", op, slug, holder)
	}
	return fmt.Errorf("%s %s: lock: %w", op, slug, err)
}
