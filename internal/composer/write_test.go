package composer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWrite_StaleCleanup(t *testing.T) {
	root := t.TempDir()
	rulesDir := filepath.Join(root, ".claude", "rules")
	stale := filepath.Join(rulesDir, "old", "gone.md")
	hand := filepath.Join(rulesDir, "hand.md")
	writeFile(t, stale, "---\npaths:\n  - \"x/**\"\n---\n\n<!-- generated from rules/consumers/split.txt; edit fragments, not this file. -->\n\nold\n")
	writeFile(t, hand, "---\npaths: [\"y/**\"]\n---\n\n# Hand-written rule\n")
	linked := filepath.Join(rulesDir, "linked.md")
	if err := os.Symlink(stale, linked); err != nil {
		t.Fatal(err)
	}

	r, err := Render(splitPool, filepath.Join(splitPool, "consumers", "split.txt"), Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	res, err := Write(root, r)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	wantWritten := []string{
		".claude/rules/area/frontend.md",
		".claude/rules/area/graphql.md",
		"AGENTS.md",
		"CLAUDE.md",
	}
	if !reflect.DeepEqual(res.Written, wantWritten) {
		t.Fatalf("Written = %v, want %v", res.Written, wantWritten)
	}
	if !reflect.DeepEqual(res.Removed, []string{".claude/rules/old/gone.md"}) {
		t.Fatalf("Removed = %v", res.Removed)
	}
	if _, err := os.Stat(filepath.Dir(stale)); !os.IsNotExist(err) {
		t.Fatalf("empty stale dir not pruned: %v", err)
	}
	if _, err := os.Stat(hand); err != nil {
		t.Fatalf("hand-written rule removed: %v", err)
	}
	if _, err := os.Lstat(linked); err != nil {
		t.Fatalf("symlinked rule removed: %v", err)
	}

	again, err := Write(root, r)
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if len(again.Written) != 0 || len(again.Removed) != 0 {
		t.Fatalf("second Write not idempotent: %+v", again)
	}

	files, _ := r.Files()
	left, err := StaleRuleFiles(root, files)
	if err != nil || len(left) != 0 {
		t.Fatalf("StaleRuleFiles after Write = %v, %v", left, err)
	}
}

func TestWrite_RejectsEscapingTarget(t *testing.T) {
	rulesDir, consumersDir := setupPool(t)
	consumer := filepath.Join(consumersDir, "c.txt")
	writeFile(t, consumer, "# target: ../CLAUDE.md\ncore/a\n")
	r, err := Render(rulesDir, consumer, Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if _, err := Write(t.TempDir(), r); err == nil {
		t.Fatal("Write: want error for a target outside the root")
	}
}

func TestWrite_RefusesSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	claude := filepath.Join(root, "CLAUDE.md")
	writeFile(t, claude, "# real instructions\n")
	if err := os.Symlink("CLAUDE.md", filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	r, err := Render(splitPool, filepath.Join(splitPool, "consumers", "import.txt"), Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if _, err := Write(root, r); err == nil || !strings.Contains(err.Error(), "AGENTS.md is a symlink") {
		t.Fatalf("Write: err = %v, want symlink refusal", err)
	}
	if b, _ := os.ReadFile(claude); string(b) != "# real instructions\n" {
		t.Fatalf("CLAUDE.md changed despite the refusal: %q", b)
	}
}

func TestWrite_KeepsUnrelatedEmptyRuleDirs(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, ".claude", "rules", "mine")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".claude", "rules", "old", "gone.md"), "<!-- generated from x -->\nold\n")
	r, err := Render(splitPool, filepath.Join(splitPool, "consumers", "split.txt"), Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if _, err := Write(root, r); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("user-made empty dir pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "rules", "old")); !os.IsNotExist(err) {
		t.Fatalf("emptied stale dir kept: %v", err)
	}
}

func TestWrite_NeedsTargets(t *testing.T) {
	rulesDir, consumersDir := setupPool(t)
	consumer := filepath.Join(consumersDir, "c.txt")
	writeFile(t, consumer, "core/a\n")
	r, err := Render(rulesDir, consumer, Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	root := t.TempDir()
	if _, err := Write(root, r); err == nil || !strings.Contains(err.Error(), "# target:") {
		t.Fatalf("Write: err = %v, want missing-target error", err)
	}
	if _, err := os.Stat(filepath.Join(root, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("CLAUDE.md written for a target-less consumer: %v", err)
	}
}
