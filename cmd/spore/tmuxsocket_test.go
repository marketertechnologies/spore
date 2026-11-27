package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/versality/spore/internal/ghapp"
	"github.com/versality/spore/internal/secrets"
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
	// Tests that want a broker start their own; the host's must stay unreachable.
	_ = os.Setenv("SPORE_SECRETS_SOCKET", filepath.Join(tmpdir, "no-broker.sock"))
	// A coordinator swap must not fall back to the host's launcher.
	_ = os.Setenv("SPORE_COORDINATOR_HOST_LAUNCHER", filepath.Join(tmpdir, "no-host-launcher"))
	_ = os.Unsetenv("TMUX_PANE")
	// Ledger writes and other state from a test must never reach the
	// host's real state dir.
	if err := os.Setenv("XDG_STATE_HOME", filepath.Join(tmpdir, "state")); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: setenv:", err)
		os.Exit(2)
	}
	// The host may run the secrets broker: keep its socket and marker
	// out of tests that exercise the direct App key path.
	_ = os.Setenv("SPORE_SECRETS_SOCKET", filepath.Join(tmpdir, "no-broker.sock"))
	ghapp.BrokerHostMarker = filepath.Join(tmpdir, "no-broker-marker")
	_ = exec.Command("tmux", "-L", testTmuxSocket, "new-session", "-d", "-s", "keepalive", "sleep 86400").Run()
	code := m.Run()
	_ = exec.Command("tmux", "-L", testTmuxSocket, "kill-server").Run()
	_ = os.RemoveAll(tmpdir)
	os.Exit(code)
}

// Without TestMain's override, a test that reaches for the broker
// would hit the host's live socket and its real GitHub App.
func TestHostBrokerUnreachable(t *testing.T) {
	p := secrets.SocketPath()
	if p == secrets.DefaultSocketPath {
		t.Fatalf("broker socket is the host default %s", p)
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatalf("broker socket %s exists", p)
	}
}
