package fleet

import (
	"fmt"
	"os"
	"os/exec"
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
	_ = os.Unsetenv("TMUX")
	_ = os.Unsetenv("TMUX_PANE")
	// Hermetic env: a coordinator/worker shell or the deployed host
	// leaks WT_SESSION_KIND (flips block-authorization gates) and
	// SPORE_MATTER_* (rewires the matter loader) into `go test`. Clear
	// them so the suite passes regardless of where it runs.
	_ = os.Unsetenv("WT_SESSION_KIND")
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "SPORE_MATTER_") {
			if i := strings.IndexByte(kv, '='); i > 0 {
				_ = os.Unsetenv(kv[:i])
			}
		}
	}
	_ = exec.Command("tmux", "-L", testTmuxSocket, "new-session", "-d", "-s", "keepalive", "sleep 86400").Run()
	code := m.Run()
	_ = exec.Command("tmux", "-L", testTmuxSocket, "kill-server").Run()
	_ = os.RemoveAll(tmpdir)
	os.Exit(code)
}
