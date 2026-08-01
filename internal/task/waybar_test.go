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
	if chip.Text != "1/2/0/1/0/0" {
		t.Errorf("text = %q, want 1/2/0/1/0/0", chip.Text)
	}
	if chip.Class != "blocked" {
		t.Errorf("class = %q, want blocked", chip.Class)
	}
	if !strings.Contains(chip.Tooltip, "escalated:0") {
		t.Errorf("tooltip = %q, want escalated:0 substring", chip.Tooltip)
	}
	if !strings.Contains(chip.Tooltip, "ready:0") {
		t.Errorf("tooltip = %q, want ready:0 substring", chip.Tooltip)
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
	if chip.Text != "0/1/0/1/1/0" {
		t.Errorf("text = %q, want 0/1/0/1/1/0", chip.Text)
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
	if chip.Text != "0/1/0/0/0/0" {
		t.Errorf("text = %q, want 0/1/0/0/0/0", chip.Text)
	}
	if chip.Class != "active" {
		t.Errorf("class = %q, want active", chip.Class)
	}
}

// TestWaybarReadyClass: a ready marker on an active slug (with no
// escalation or blocked task in sight) flips class to "ready", bumps
// the sixth text field, and tooltips the count. Marker layout matches
// roledrive.writeReadyMarker.
func TestWaybarReadyClass(t *testing.T) {
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
	write("shipping", "active")

	stateDir := filepath.Join(RoleTaskDir(projectRoot, "shipping"), "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "ready"), nil, 0o644); err != nil {
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
	if chip.Text != "0/1/0/0/0/1" {
		t.Errorf("text = %q, want 0/1/0/0/0/1", chip.Text)
	}
	if chip.Class != "ready" {
		t.Errorf("class = %q, want ready (must beat active)", chip.Class)
	}
	if !strings.Contains(chip.Tooltip, "ready:1") {
		t.Errorf("tooltip = %q, want ready:1 substring", chip.Tooltip)
	}
}

// TestWaybarEscalatedBeatsReady: a slug with both ready and
// escalated markers (e.g. operator force-approved after an
// escalation, role-loop wrote ready, but the escalation marker
// somehow survived) keeps class "escalated". Failure outranks
// success-to-claim.
func TestWaybarEscalatedBeatsReady(t *testing.T) {
	projectRoot := t.TempDir()
	tasksDir := filepath.Join(projectRoot, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nstatus: active\nslug: only\n---\n"
	if err := os.WriteFile(filepath.Join(tasksDir, "only.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(RoleTaskDir(projectRoot, "only"), "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "ready"), nil, 0o644); err != nil {
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
	if chip.Class != "escalated" {
		t.Errorf("class = %q, want escalated (must beat ready)", chip.Class)
	}
}

// TestWaybarBlockedBeatsReady: blocked status outranks the ready
// marker; precedence is escalated > blocked > ready > active > idle.
func TestWaybarBlockedBeatsReady(t *testing.T) {
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
	write("shipping", "active")
	write("stuck", "blocked")

	stateDir := filepath.Join(RoleTaskDir(projectRoot, "shipping"), "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "ready"), nil, 0o644); err != nil {
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
	if chip.Class != "blocked" {
		t.Errorf("class = %q, want blocked (must beat ready)", chip.Class)
	}
}
