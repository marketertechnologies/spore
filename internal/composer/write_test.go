package composer

import (
	"os"
	"path/filepath"
	"reflect"
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
