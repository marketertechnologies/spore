package task

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWaybar(t *testing.T) {
	dir := t.TempDir()
	write := func(slug, status string) {
		content := "---\nstatus: " + status + "\nslug: " + slug + "\n---\n"
		if err := os.WriteFile(filepath.Join(dir, slug+".md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a", "draft")
	write("b", "active")
	write("c", "active")
	write("d", "blocked")
	write("e", "done")

	out, err := Waybar(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	var chip WaybarChip
	if err := json.Unmarshal(out, &chip); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if chip.Text != "1/2/0/1/0" {
		t.Errorf("text = %q, want 1/2/0/1/0", chip.Text)
	}
	if chip.Class != "blocked" {
		t.Errorf("class = %q, want blocked", chip.Class)
	}
	if !strings.Contains(chip.Tooltip, "escalated:0") {
		t.Errorf("tooltip = %q, want escalated:0 substring", chip.Tooltip)
	}
}

// TestWaybarEscalatedClass: with an escalation marker on disk for an
// active slug, the chip flips class to "escalated" (winning over
// blocked + active), bumps the fifth text field, and tooltips the
// count. Marker layout matches roledrive.writeEscalationMarker.
func TestWaybarEscalatedClass(t *testing.T) {
	projectRoot := t.TempDir()
	tasksDir := filepath.Join(projectRoot, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(slug, status string) {
		content := "---\nstatus: " + status + "\nslug: " + slug + "\n---\n"
		if err := os.WriteFile(filepath.Join(tasksDir, slug+".md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("hot", "active")
	write("cold", "blocked")

	stateDir := filepath.Join(RoleTaskDir(projectRoot, "hot"), "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "escalated-A-3"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := Waybar(tasksDir, projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	var chip WaybarChip
	if err := json.Unmarshal(out, &chip); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if chip.Text != "0/1/0/1/1" {
		t.Errorf("text = %q, want 0/1/0/1/1", chip.Text)
	}
	if chip.Class != "escalated" {
		t.Errorf("class = %q, want escalated (must beat blocked)", chip.Class)
	}
	if !strings.Contains(chip.Tooltip, "escalated:1") {
		t.Errorf("tooltip = %q, want escalated:1 substring", chip.Tooltip)
	}
}

// TestWaybarNoMarkerUnchanged: projectRoot supplied but no markers on
// disk -> escalated stays zero, class falls through to blocked/active,
// fifth field is "0".
func TestWaybarNoMarkerUnchanged(t *testing.T) {
	projectRoot := t.TempDir()
	tasksDir := filepath.Join(projectRoot, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nstatus: active\nslug: only\n---\n"
	if err := os.WriteFile(filepath.Join(tasksDir, "only.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := Waybar(tasksDir, projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	var chip WaybarChip
	if err := json.Unmarshal(out, &chip); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if chip.Text != "0/1/0/0/0" {
		t.Errorf("text = %q, want 0/1/0/0/0", chip.Text)
	}
	if chip.Class != "active" {
		t.Errorf("class = %q, want active", chip.Class)
	}
}
