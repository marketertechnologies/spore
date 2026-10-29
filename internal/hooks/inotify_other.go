//go:build !linux

package hooks

import (
	"errors"
	"time"
)

func initPlatformWatcher(dir string) (inboxWaiter, error) {
	return nil, errors.New("inotify only supported on linux")
}

func newPlatformWatcher(dir string, timeout time.Duration) (inboxWaiter, error) {
	return initPlatformWatcher(dir)
}
