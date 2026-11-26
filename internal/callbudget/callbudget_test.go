package callbudget

import (
	"errors"
	"os/exec"
	"testing"
	"time"
)

func TestCommandKillsHungCallAndChildren(t *testing.T) {
	t.Setenv(TimeoutEnv, "200ms")
	start := time.Now()
	// The child sleep holds stdout open; only a group kill (or
	// WaitDelay) lets Output return.
	_, err := Command("sh", "-c", "sleep 30 & sleep 30").Output()
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("hung call took %s, want ~200ms", took)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("err = %v, want the killed call's exit error", err)
	}
}

func TestCommandFastCallUnaffected(t *testing.T) {
	out, err := Command("sh", "-c", "echo ok").Output()
	if err != nil || string(out) != "ok\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestTimeoutEnv(t *testing.T) {
	t.Setenv(TimeoutEnv, "")
	if Timeout() != DefaultTimeout {
		t.Errorf("default = %s", Timeout())
	}
	t.Setenv(TimeoutEnv, "45s")
	if Timeout() != 45*time.Second {
		t.Errorf("override = %s", Timeout())
	}
	t.Setenv(TimeoutEnv, "nope")
	if Timeout() != DefaultTimeout {
		t.Errorf("bad value = %s, want default", Timeout())
	}
}

// Abort is permanent for the process, so this test runs last in file
// order and nothing after it may use Command.
func TestAbortKillsInFlightCalls(t *testing.T) {
	t.Setenv(TimeoutEnv, "1m")
	done := make(chan error, 1)
	go func() { done <- Command("sleep", "30").Run() }()
	time.Sleep(100 * time.Millisecond)
	Abort()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("aborted call reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Abort did not kill the in-flight call")
	}
}
