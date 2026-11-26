package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunInstallKeepsEditedSkillAndWarns(t *testing.T) {
	root := t.TempDir()
	if code := runInstall([]string{"--root", root}); code != 0 {
		t.Fatalf("first install exit %d", code)
	}
	target := filepath.Join(root, ".claude", "skills", "diagram", "SKILL.md")
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	edited := append(body, []byte("LOCAL EDIT CANARY\n")...)
	if err := os.WriteFile(target, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := captureHook(t, "", func() int { return runInstall([]string{"--root", root}) })
	if code != 0 {
		t.Errorf("reinstall exit %d", code)
	}
	if !strings.Contains(stderr, "warning: kept .claude/skills/diagram/SKILL.md") {
		t.Errorf("stderr = %q, want a warning naming the kept file", stderr)
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, edited) {
		t.Error("reinstall overwrote the local edit")
	}
	if code := runInstall([]string{"--root", root, "--force"}); code != 0 {
		t.Fatalf("--force exit %d", code)
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, body) {
		t.Error("--force did not restore the bundled copy")
	}
}
