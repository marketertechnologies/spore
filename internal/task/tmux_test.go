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
