package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartRefusesActive(t *testing.T) {
	tasksDir := t.TempDir()
	taskPath := filepath.Join(tasksDir, "x.md")
	if err := os.WriteFile(taskPath, []byte("---\nstatus: active\nslug: x\ntitle: X\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(tasksDir, "x"); err == nil {
		t.Fatal("Start on active task should error, got nil")
	}
}

func TestStartRefusesDone(t *testing.T) {
	tasksDir := t.TempDir()
	taskPath := filepath.Join(tasksDir, "x.md")
	if err := os.WriteFile(taskPath, []byte("---\nstatus: done\nslug: x\ntitle: X\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(tasksDir, "x"); err == nil {
		t.Fatal("Start on done task should error, got nil")
	}
}

func TestStartRefusesRoleLoop(t *testing.T) {
	tasksDir := t.TempDir()
	taskPath := filepath.Join(tasksDir, "x.md")
	if err := os.WriteFile(taskPath, []byte("---\nstatus: draft\nslug: x\ntitle: X\nloop: role\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Start(tasksDir, "x")
	if err == nil {
		t.Fatal("Start on loop: role task should error, got nil")
	}
	if !strings.Contains(err.Error(), "loop: role") {
		t.Errorf("error %q should name the loop: role opt-in", err)
	}
	// The refusal must leave the file untouched (no status flip).
	raw, readErr := os.ReadFile(taskPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(raw), "status: draft") {
		t.Errorf("task file mutated by refused Start:\n%s", raw)
	}
}
