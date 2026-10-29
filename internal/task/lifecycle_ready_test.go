package task

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDoneClearsReadyMarker asserts task.Done removes the
// state/ready marker the role-loop dropped at PhaseDone, while
// preserving the rest of `.spore/<slug>/` (spec.md, summary.md,
// responses, reviews) as historical record.
func TestDoneClearsReadyMarker(t *testing.T) {
	tasksDir := t.TempDir()
	projectRoot := filepath.Dir(tasksDir)
	taskPath := filepath.Join(tasksDir, "x.md")
	if err := os.WriteFile(taskPath, []byte("---\nstatus: active\nslug: x\ntitle: X\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	roleTaskDir := RoleTaskDir(projectRoot, "x")
	stateDir := filepath.Join(roleTaskDir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	readyMarker := filepath.Join(stateDir, "ready")
	if err := os.WriteFile(readyMarker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roleTaskDir, "spec.md"), []byte("spec"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roleTaskDir, "summary.md"), []byte("summary"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Done(tasksDir, "x", false); err != nil {
		t.Fatalf("Done: %v", err)
	}

	if _, err := os.Stat(readyMarker); !os.IsNotExist(err) {
		t.Errorf("ready marker should be removed after Done, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(roleTaskDir, "spec.md")); err != nil {
		t.Errorf("spec.md should survive Done: %v", err)
	}
	if _, err := os.Stat(filepath.Join(roleTaskDir, "summary.md")); err != nil {
		t.Errorf("summary.md should survive Done: %v", err)
	}
}

// TestDoneNoReadyMarkerIsNoOp asserts task.Done does not error when
// the state/ready marker is absent (common case: task was never
// driven through the role-loop). Cleanup is best-effort.
func TestDoneNoReadyMarkerIsNoOp(t *testing.T) {
	tasksDir := t.TempDir()
	taskPath := filepath.Join(tasksDir, "x.md")
	if err := os.WriteFile(taskPath, []byte("---\nstatus: active\nslug: x\ntitle: X\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Done(tasksDir, "x", false); err != nil {
		t.Fatalf("Done without ready marker: %v", err)
	}
}
