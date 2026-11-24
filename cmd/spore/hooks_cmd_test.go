package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHooksNotifyCoordinatorNoArgsUsesEnv(t *testing.T) {
	state := t.TempDir()
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", state)
	t.Setenv("WT_PROJECT", "project")
	t.Setenv("SPORE_TASK_INBOX", filepath.Join(t.TempDir(), "worker", "inbox"))

	if code := runHooksNotifyCoordinator(nil); code != 0 {
		t.Fatalf("runHooksNotifyCoordinator(nil) = %d, want 0", code)
	}
	entries, err := os.ReadDir(filepath.Join(state, "project", "inbox"))
	if err != nil {
		t.Fatalf("read coordinator inbox: %v", err)
	}
	found := false
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			found = true
		}
	}
	if !found {
		t.Fatal("notify-coordinator env mode did not write a json poke")
	}
}

func TestHooksWatchInboxNoArgsNoEnvSilentNoOp(t *testing.T) {
	t.Setenv("SPORE_TASK_INBOX", "")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	code := runHooksWatchInbox(nil)
	w.Close()
	stderr, _ := io.ReadAll(r)

	if code != 0 {
		t.Fatalf("runHooksWatchInbox(nil) = %d, want 0 with no slug and no SPORE_TASK_INBOX", code)
	}
	if len(stderr) != 0 {
		t.Fatalf("runHooksWatchInbox(nil) wrote stderr %q, want empty", stderr)
	}
}

func runHooksSettingsIn(t *testing.T, dir string) (int, string) {
	t.Helper()
	t.Chdir(dir)
	in, err := os.CreateTemp(t.TempDir(), "in")
	if err != nil {
		t.Fatal(err)
	}
	in.WriteString(`{"events":{}}`)
	in.Seek(0, io.SeekStart)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origIn, origOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = in, w
	code := runHooksSettings()
	os.Stdin, os.Stdout = origIn, origOut
	w.Close()
	out, _ := io.ReadAll(r)
	return code, string(out)
}

func TestHooksSettingsMergesSporeTomlPermissions(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	toml := "[permissions]\ndefault_deny = false\ndeny = [\"Bash(terraform apply *)\"]\n"
	if err := os.WriteFile(filepath.Join(root, "spore.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	code, out := runHooksSettingsIn(t, sub)
	if code != 0 {
		t.Fatalf("runHooksSettings = %d, want 0", code)
	}
	var parsed struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	want := []string{"SendMessage", "Bash(terraform apply *)"}
	if !reflect.DeepEqual(parsed.Permissions.Deny, want) {
		t.Fatalf("deny = %q, want %q", parsed.Permissions.Deny, want)
	}

	if err := os.WriteFile(filepath.Join(root, "spore.toml"), []byte("[permissions]\nallow = [\"x\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := runHooksSettingsIn(t, sub); code != 1 {
		t.Fatalf("runHooksSettings with unknown [permissions] key = %d, want 1", code)
	}
}
