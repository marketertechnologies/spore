package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunConfigCheck(t *testing.T) {
	dir := t.TempDir()
	if code := runConfig([]string{"check", "--root", dir}); code != 0 {
		t.Errorf("no spore.toml: exit %d, want 0", code)
	}
	if err := os.WriteFile(filepath.Join(dir, "spore.toml"), []byte("[fleet]\nmax_workers = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runConfig([]string{"check", "--root", dir}); code != 0 {
		t.Errorf("clean spore.toml: exit %d, want 0", code)
	}
	if err := os.WriteFile(filepath.Join(dir, "spore.toml"), []byte("[fleett]\nmax_workers = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runConfig([]string{"check", "--root", dir}); code != 1 {
		t.Errorf("unknown section: exit %d, want 1", code)
	}
}
