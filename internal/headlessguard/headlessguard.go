// Package headlessguard refuses to launch an agent (claude, codex, ...)
// when the spawn would be headless: stdin is not a TTY and $TMUX is
// unset. A headless agent has no pane the operator can attach to, no
// way to observe its TUI, and orphans to PID 1 the moment its launching
// shell exits. The kernel always spawns agents inside `tmux
// new-session`, so $TMUX is set in the normal path; this guard is the
// backstop for a manual or misconfigured launch that would otherwise
// produce an unobservable, unattachable session.
package headlessguard

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// Env is the spawn context the guard decides on. It is split out so the
// decision is a pure function the tests can drive without a real TTY or
// tmux server.
type Env struct {
	// TMUX is the value of the $TMUX environment variable. Non-empty
	// means the process is already inside a tmux pane.
	TMUX string
	// StdinIsTTY reports whether stdin is an interactive terminal: an
	// operator typed the launch by hand.
	StdinIsTTY bool
}

// Allowed reports whether the spawn may proceed. It is refused only when
// the process is neither inside tmux nor attached to an interactive
// terminal -- the headless case.
func (e Env) Allowed() bool {
	return e.TMUX != "" || e.StdinIsTTY
}

// CurrentEnv reads the live spawn context from the process environment
// and stdin.
func CurrentEnv() Env {
	return Env{
		TMUX:       os.Getenv("TMUX"),
		StdinIsTTY: isTerminal(os.Stdin),
	}
}

// RefusalMessage is the stderr block printed when a spawn is refused.
func RefusalMessage(bin string) string {
	return fmt.Sprintf(`spore guard: REFUSING headless %s spawn.
  stdin is not a TTY and $TMUX is unset, so this process would have no
  attachable pane and would orphan to PID 1 on its launcher's exit. All
  agent sessions must run inside tmux. Wrap the call in
    tmux new-session -d -s "<name>" '... spore guard %s ...'
  or run it from an interactive shell.
`, bin, bin)
}

// BackstopAccountTier seeds SPORE_ACCOUNT_TIER from WT_ACCOUNT_TIER when
// the former is unset and the latter is present, mirroring the inline
// derivation in the supervisor loop. The guard is the inner wrapper
// every agent spawn re-execs through, so a tier set only as
// WT_ACCOUNT_TIER still reaches the token-monitor Stop hook (which reads
// SPORE_ACCOUNT_TIER). An already-set SPORE_ACCOUNT_TIER is preserved so
// a host that opted out of max tier keeps its choice.
func BackstopAccountTier() {
	if os.Getenv("SPORE_ACCOUNT_TIER") == "" {
		if v := os.Getenv("WT_ACCOUNT_TIER"); v != "" {
			_ = os.Setenv("SPORE_ACCOUNT_TIER", v)
		}
	}
}

// ioctlTCGETS is the Linux request that reads a terminal's attributes.
// A real TTY answers it; /dev/null and pipes (both not terminals, the
// first a character device that would fool a ModeCharDevice check)
// return ENOTTY. The constant is 0x5401 on every Linux arch spore
// targets (x86_64, aarch64).
const ioctlTCGETS = 0x5401

func isTerminal(f *os.File) bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		f.Fd(),
		ioctlTCGETS,
		uintptr(unsafe.Pointer(&termios)),
	)
	return errno == 0
}
