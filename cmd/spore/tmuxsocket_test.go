package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// testTmuxSocket: see internal/task/tmuxsocket_test.go for the
// socket-isolation rationale. Same name "default" so test calls and
// production calls (which never pass -L) target the same socket file
// inside the per-process TMUX_TMPDIR.
const testTmuxSocket = "default"

func TestMain(m *testing.M) {
	tmpdir, err := os.MkdirTemp("", "spore-tmux-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: mkdtemp:", err)
		os.Exit(2)
	}
	if err := os.Setenv("TMUX_TMPDIR", tmpdir); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: setenv:", err)
		os.Exit(2)
	}
	_ = os.Unsetenv("TMUX")
	// A coordinator swap must not fall back to the host's launcher.
	_ = os.Setenv("SPORE_COORDINATOR_HOST_LAUNCHER", filepath.Join(tmpdir, "no-host-launcher"))
	_ = os.Unsetenv("TMUX_PANE")
	// Unset, the socket falls back to the host's live broker path.
	_ = os.Setenv("SPORE_SECRETS_SOCKET", filepath.Join(tmpdir, "no-broker.sock"))
	// Ledger writes and other state from a test must never reach the
	// host's real state dir.
	if err := os.Setenv("XDG_STATE_HOME", filepath.Join(tmpdir, "state")); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: setenv:", err)
		os.Exit(2)
	}
	_ = exec.Command("tmux", "-L", testTmuxSocket, "new-session", "-d", "-s", "keepalive", "sleep 86400").Run()
	code := m.Run()
	_ = exec.Command("tmux", "-L", testTmuxSocket, "kill-server").Run()
	_ = os.RemoveAll(tmpdir)
	os.Exit(code)
}
