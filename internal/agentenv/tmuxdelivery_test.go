package agentenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// coldTmuxSocket is the -L socket name this test passes. The matching
// TMUX_TMPDIR override below puts its socket file in a private
// directory, so the server started here is a cold one that no other
// tmux server and no operator session shares.
//
// Both names are kept short on purpose. A unix socket path cannot
// exceed 108 bytes, and the directory tmux builds it under is
// <TMUX_TMPDIR>/tmux-<uid>/<name>. t.TempDir() spells the full test
// name into its path, which already overflows that limit on a
// continuous-integration runner whose temp root is itself long.
const coldTmuxSocket = "cold"

// TestTmuxArgsReachAColdServersPane pins the delivery both spawn sites
// depend on, under the condition a continuous-integration runner
// creates: no tmux server is running yet, so the first new-session
// starts one. A warm server and a cold one take different paths inside
// tmux, and only the warm one is exercised by the spawn tests in
// internal/fleet and internal/task, whose TestMain keeps a server
// alive for the whole run.
func TestTmuxArgsReachAColdServersPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("tmux not available: %v", err)
	}
	dir, err := os.MkdirTemp("", "sptmx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("TMUX_TMPDIR", dir)

	seed := TmuxArgs([]Entry{{Key: "AGENTENV_PROBE", Value: "bar"}})
	if len(seed) != 2 {
		t.Fatalf("TmuxArgs should render one entry as two arguments, got %v", seed)
	}
	start := exec.Command("tmux", "-L", coldTmuxSocket,
		"new-session", "-d", "-s", "cold",
		"-e", "SPORE_TASK_SLUG=probe", seed[0], seed[1], "sleep 60")
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("cold new-session: %v: %s", err, strings.TrimSpace(string(out)))
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", coldTmuxSocket, "kill-server").Run()
	})

	out, err := exec.Command("tmux", "-L", coldTmuxSocket, "list-panes", "-t", "cold", "-F", "#{pane_pid}").Output()
	if err != nil {
		t.Fatalf("list-panes: %v", err)
	}
	pid := strings.TrimSpace(string(out))

	// The same wait the spawn tests use: tmux answers new-session once
	// the pane process is forked, which can be before it has reached
	// execve, and /proc/<pid>/environ is empty until then.
	deadline := time.Now().Add(10 * time.Second)
	for {
		env := environOf(pid)
		if env["SPORE_TASK_SLUG"] != "" {
			if got := env["AGENTENV_PROBE"]; got != "bar" {
				t.Fatalf("AGENTENV_PROBE in a cold server's pane: want %q, got %q", "bar", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane %s never exposed a spawned environment: %d variables", pid, len(env))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func environOf(pid string) map[string]string {
	env := map[string]string{}
	raw, err := os.ReadFile(filepath.Join("/proc", pid, "environ"))
	if err != nil {
		return env
	}
	for _, kv := range strings.Split(string(raw), "\x00") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}
