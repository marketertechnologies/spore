// Package callbudget bounds the external calls (git, tmux, matter
// hooks) a fleet reconcile pass makes, so one hung call cannot hold
// the pass, and with it every later pass of the oneshot unit, forever.
package callbudget

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// TimeoutEnv overrides DefaultTimeout with a Go duration ("90s").
const TimeoutEnv = "SPORE_FLEET_CALL_TIMEOUT"

// DefaultTimeout is the per-call budget. Calls with their own,
// tighter deadline (the origin fetch) keep it.
const DefaultTimeout = 2 * time.Minute

// waitDelay is how long Wait keeps reading a killed call's pipes: a
// grandchild (ssh under git) may hold them open after the kill.
const waitDelay = 5 * time.Second

// Timeout returns the per-call budget: TimeoutEnv when it parses to a
// positive duration, else DefaultTimeout.
func Timeout() time.Duration {
	if v := os.Getenv(TimeoutEnv); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		fmt.Fprintf(os.Stderr, "spore: %s=%q: want a positive Go duration; using %s\n", TimeoutEnv, v, DefaultTimeout)
	}
	return DefaultTimeout
}

var root, abort = context.WithCancel(context.Background())

// Context returns a context that expires after Timeout, or at Abort.
func Context() (context.Context, context.CancelFunc) {
	return context.WithTimeout(root, Timeout())
}

// Abort kills every call in flight and fails every later one at once.
// For a process giving up on its pass: it is not undone.
func Abort() { abort() }

// Command is exec.Command with the per-call budget. The call runs in
// its own process group, and the whole group is killed when the
// budget runs out. Not for interactive commands: the new process group
// is never the terminal's foreground group.
func Command(name string, args ...string) *exec.Cmd {
	ctx, cancel := Context()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		defer cancel()
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = waitDelay
	return cmd
}
