package task

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A failed listing is an error, not "no sessions": teardown must not
// report a clean kill while the slug's sessions keep running.
func TestMatchingSlugSessionsListFailure(t *testing.T) {
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not on PATH")
	}
	project := t.TempDir()
	tasks := filepath.Join(project, "tasks")
	if names, err := matchingSlugSessions(tasks, project, "s1", true); err != nil || len(names) != 0 {
		t.Fatalf("no server = %v, %v; want none, nil", names, err)
	}
	bin := t.TempDir()
	shim := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = list-sessions ]; then echo 'server exited unexpectedly' >&2; exit 1; fi\nexec %s \"$@\"\n", realTmux)
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if names, err := matchingSlugSessions(tasks, project, "s1", true); err == nil {
		t.Errorf("failing listing = %v, nil; want an error", names)
	}
}

func TestTmuxNoServer(t *testing.T) {
	for msg, want := range map[string]bool{
		"no server running on /tmp/tmux-1000/default":                            true,
		"error connecting to /tmp/tmux-1000/default (No such file or directory)": true,
		"error connecting to /tmp/tmux-1000/default (Connection refused)":        true,
		"error connecting to /tmp/tmux-1000/default (Permission denied)":         false,
		"server exited unexpectedly":                                             false,
		"":                                                                       false,
	} {
		if got := TmuxNoServer(msg); got != want {
			t.Errorf("TmuxNoServer(%q) = %v, want %v", msg, got, want)
		}
	}
}

// An unreachable socket (EACCES) is a failed listing, not an empty one.
func TestListSessionsPermissionDenied(t *testing.T) {
	tmuxShim(t, "list-sessions", "error connecting to /tmp/tmux-1000/default (Permission denied)")
	if names, err := ListSessions(); err == nil {
		t.Errorf("ListSessions = %v, nil; want an error", names)
	}
}

// Reap reports a failed listing, so the pass does not record the slug
// as reaped while its sessions run on.
func TestReapListFailure(t *testing.T) {
	project := t.TempDir()
	tmuxShim(t, "list-sessions", "server exited unexpectedly")
	if err := Reap(filepath.Join(project, "tasks"), project, "s1"); err == nil {
		t.Error("Reap with a failing listing = nil; want an error")
	}
}

// tmuxShim puts a tmux on PATH that fails sub with msg on stderr and
// passes every other call to the real tmux.
func tmuxShim(t *testing.T, sub, msg string) {
	t.Helper()
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not on PATH")
	}
	bin := t.TempDir()
	shim := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = %s ]; then echo '%s' >&2; exit 1; fi\nexec %s \"$@\"\n", sub, msg, realTmux)
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
