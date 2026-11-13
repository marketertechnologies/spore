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

func TestActivateWritesAgentOverridingExisting(t *testing.T) {
	tasksDir := t.TempDir()
	taskPath := filepath.Join(tasksDir, "x.md")
	if err := os.WriteFile(taskPath, []byte("---\nstatus: draft\nslug: x\ntitle: X\nagent: claude\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := activate(tasksDir, "x", StartOptions{Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Agent != "codex" || m.Status != "active" {
		t.Errorf("meta = agent %q status %q, want codex active", m.Agent, m.Status)
	}
	raw, _ := os.ReadFile(taskPath)
	if !strings.Contains(string(raw), "agent: codex") || strings.Contains(string(raw), "agent: claude") {
		t.Errorf("task file agent not replaced:\n%s", raw)
	}
}

func TestActivateKeepsAgentWithoutOption(t *testing.T) {
	tasksDir := t.TempDir()
	taskPath := filepath.Join(tasksDir, "x.md")
	if err := os.WriteFile(taskPath, []byte("---\nstatus: paused\nslug: x\ntitle: X\nagent: codex\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := activate(tasksDir, "x", StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Agent != "codex" {
		t.Errorf("agent = %q, want codex kept", m.Agent)
	}
}

func TestStartRejectsUnknownAgent(t *testing.T) {
	tasksDir := t.TempDir()
	taskPath := filepath.Join(tasksDir, "x.md")
	orig := "---\nstatus: draft\nslug: x\ntitle: X\n---\n"
	if err := os.WriteFile(taskPath, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := StartWithOptions(tasksDir, "x", StartOptions{Agent: "gemini"})
	if err == nil || !strings.Contains(err.Error(), "want claude or codex") {
		t.Fatalf("err = %v, want agent validation error", err)
	}
	raw, _ := os.ReadFile(taskPath)
	if string(raw) != orig {
		t.Errorf("task file mutated by refused Start:\n%s", raw)
	}
}
