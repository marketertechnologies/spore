package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartRefusesRoleLoop(t *testing.T) {
	assertStartRefusesLoop(t, "role")
}

func TestStartRefusesAnyLoop(t *testing.T) {
	assertStartRefusesLoop(t, "dev")
}

func assertStartRefusesLoop(t *testing.T, loop string) {
	t.Helper()
	tasksDir := t.TempDir()
	taskPath := filepath.Join(tasksDir, "x.md")
	orig := "---\nstatus: draft\nslug: x\ntitle: X\nloop: " + loop + "\n---\n"
	if err := os.WriteFile(taskPath, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	session, err := Start(tasksDir, "x")
	if err == nil {
		t.Fatalf("Start on loop: %s task should error, got session %q", loop, session)
	}
	if !strings.Contains(err.Error(), "loop: "+loop) {
		t.Errorf("error %q should name the loop: %s opt-in", err, loop)
	}
	if session != "" {
		t.Errorf("session = %q, want none", session)
	}
	raw, readErr := os.ReadFile(taskPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(raw) != orig {
		t.Errorf("task file mutated by refused Start:\n%s", raw)
	}
}
