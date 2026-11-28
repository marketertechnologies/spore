package task

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadEngineerCheck(t *testing.T) {
	root := t.TempDir()
	if got := LoadEngineerCheck(root); got != (EngineerCheck{TimeoutSeconds: DefaultEngineerCheckTimeoutSeconds}) {
		t.Errorf("missing spore.toml: got %+v", got)
	}
	writeSporeToml(t, root, "[fleet]\ncheck = \"wrong section\"\n[fleet.roles.engineer]\ncheck = \" just check \"\ncheck_timeout_seconds = 300\n")
	if got := LoadEngineerCheck(root); got != (EngineerCheck{Cmd: "just check", TimeoutSeconds: 300}) {
		t.Errorf("got %+v", got)
	}
	writeSporeToml(t, root, "[fleet.roles.engineer]\ncheck = \"x\"\ncheck_timeout_seconds = 0\n")
	if got := LoadEngineerCheck(root).TimeoutSeconds; got != DefaultEngineerCheckTimeoutSeconds {
		t.Errorf("zero timeout: got %d, want default", got)
	}
	writeSporeToml(t, root, "[fleet.roles.engineer]\ncheck = \"x\"\ncheck_timeout_seconds = 7200\n")
	if got := LoadEngineerCheck(root).TimeoutSeconds; got != MaxEngineerCheckTimeoutSeconds {
		t.Errorf("timeout past the hook timeout: got %d, want %d", got, MaxEngineerCheckTimeoutSeconds)
	}
	if DefaultEngineerCheckTimeoutSeconds >= EngineerCheckHookTimeout {
		t.Errorf("default check timeout %d must stay under the hook timeout %d", DefaultEngineerCheckTimeoutSeconds, EngineerCheckHookTimeout)
	}
}

func TestLatestCheckedSHA(t *testing.T) {
	root := t.TempDir()
	if err := WriteEngineerResponse(root, "demo", 1, EngineerResponse{Addressed: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	resp, err := os.Stat(EngineerResponsePath(root, "demo", 1))
	if err != nil {
		t.Fatal(err)
	}
	mark := func(path string, age time.Duration) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		at := resp.ModTime().Add(age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := LatestCheckedSHA(root, "demo", 1); err != nil || got != "" {
		t.Fatalf("no markers: got %q, %v", got, err)
	}
	mark(CheckPassPath(root, "demo", 1, "stale"), -time.Minute)
	mark(CheckAttemptPath(root, "demo", 1, "attempt", 1), 3*time.Minute)
	mark(CheckPassPath(root, "demo", 12, "otherround"), 3*time.Minute)
	if got, _ := LatestCheckedSHA(root, "demo", 1); got != "" {
		t.Fatalf("stale, attempt or other-round marker counted: %q", got)
	}
	mark(CheckFailedPath(root, "demo", 1, "gaveup"), time.Minute)
	mark(CheckPassPath(root, "demo", 1, "passed"), 2*time.Minute)
	if got, _ := LatestCheckedSHA(root, "demo", 1); got != "passed" {
		t.Fatalf("got %q, want the newest marker's sha", got)
	}
	mark(CheckFailedPath(root, "demo", 1, "gaveup"), 4*time.Minute)
	if got, _ := LatestCheckedSHA(root, "demo", 1); got != "gaveup" {
		t.Fatalf("got %q, want the newer give-up's sha", got)
	}
}
