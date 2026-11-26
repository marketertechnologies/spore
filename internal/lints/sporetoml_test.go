package lints

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSporeTOMLWarnsOnUnknownKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "spore.toml"), []byte("[lint]\nroot_line_limt = 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	issues, err := SporeTOML{}.Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || !issues[0].Warn || issues[0].Path != "spore.toml" {
		t.Fatalf("issues = %+v, want one warning on spore.toml", issues)
	}
	found := false
	for _, l := range Default() {
		if l.Name() == "spore-toml" {
			found = true
		}
	}
	if !found {
		t.Error("spore-toml is not in Default()")
	}
}
