package fleet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	// A coordinator swap must not fall back to the host's launcher.
	_ = os.Setenv("SPORE_COORDINATOR_HOST_LAUNCHER", filepath.Join(tmpdir, "no-host-launcher"))
	// Role spawns materialize bundled bodies under the state dir;
	// keep them off the host's real one.
	if err := os.Setenv("XDG_STATE_HOME", filepath.Join(tmpdir, "state")); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: setenv:", err)
		os.Exit(2)
	}
	_ = os.Unsetenv("TMUX")
	_ = os.Unsetenv("TMUX_PANE")
	// Run from an agent pane, the inherited SPORE_* / WT_* env names
	// the live coordinator inbox, launcher and project; a test pass
	// must never deliver to or spawn through them.
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "SPORE_") || strings.HasPrefix(k, "WT_") {
			_ = os.Unsetenv(k)
		}
	}
	if err := os.Setenv("SPORE_COORDINATOR_STATE_DIR", filepath.Join(tmpdir, "coordinator")); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: setenv:", err)
		os.Exit(2)
	}
	// On a host running the secrets broker the mirror would otherwise
	// route its gh calls to the live socket instead of the fake gh.
	_ = os.Setenv("SPORE_SECRETS_SOCKET", filepath.Join(tmpdir, "no-broker.sock"))
	_ = exec.Command("tmux", "-L", testTmuxSocket, "new-session", "-d", "-s", "keepalive", "sleep 86400").Run()
	code := m.Run()
	_ = exec.Command("tmux", "-L", testTmuxSocket, "kill-server").Run()
	_ = os.RemoveAll(tmpdir)
	os.Exit(code)
}
