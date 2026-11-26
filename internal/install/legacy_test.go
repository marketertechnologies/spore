package install

import (
	"os"
	"path/filepath"
	"testing"

	spore "github.com/versality/spore"
)

func TestInstallReplacesLegacyBundledCopyWithoutManifest(t *testing.T) {
	old := []byte("# a skill body spore shipped before the manifest\n")
	prev := legacyBundled["diagram/SKILL.md"]
	legacyBundled["diagram/SKILL.md"] = append(append([]string{}, prev...), sha256Hex(old))
	t.Cleanup(func() { legacyBundled["diagram/SKILL.md"] = prev })

	root := t.TempDir()
	target := filepath.Join(root, ".claude", "skills", "diagram", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, old, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Install(root, spore.BundledSkills, "bootstrap/skills", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Kept) != 0 {
		t.Errorf("Kept=%v, want the legacy copy replaced", res.Kept)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude", "skills", ManifestName)); err != nil {
		t.Errorf("manifest not written: %v", err)
	}
}
