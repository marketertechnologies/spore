package install_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spore "github.com/versality/spore"
	"github.com/versality/spore/internal/install"
)

func TestInstallDropsSkillsWithCorrectModes(t *testing.T) {
	root := t.TempDir()
	res, err := install.Install(root, spore.BundledSkills, "bootstrap/skills", install.Options{})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(res.Written) == 0 {
		t.Fatal("Install wrote 0 files; want > 0")
	}

	skillSkillMd := filepath.Join(root, ".claude", "skills", "spore-bootstrap", "SKILL.md")
	body, err := os.ReadFile(skillSkillMd)
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if !bytes.Contains(body, []byte("name: spore-bootstrap")) {
		t.Errorf("SKILL.md missing frontmatter; got first 80 bytes: %q", body[:min(80, len(body))])
	}

	diagramScript := filepath.Join(root, ".claude", "skills", "diagram", "diagram")
	info, err := os.Stat(diagramScript)
	if err != nil {
		t.Fatalf("stat diagram script: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("diagram script not executable: mode=%v", info.Mode().Perm())
	}

	skillReadme := filepath.Join(root, ".claude", "skills", "spore-bootstrap", "README.md")
	info, err = os.Stat(skillReadme)
	if err != nil {
		t.Fatalf("stat README.md: %v", err)
	}
	if info.Mode().Perm()&0o100 != 0 {
		t.Errorf("README.md unexpectedly executable: mode=%v", info.Mode().Perm())
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	root := t.TempDir()
	first, err := install.Install(root, spore.BundledSkills, "bootstrap/skills", install.Options{})
	if err != nil {
		t.Fatalf("Install #1: %v", err)
	}
	second, err := install.Install(root, spore.BundledSkills, "bootstrap/skills", install.Options{})
	if err != nil {
		t.Fatalf("Install #2: %v", err)
	}
	if len(second.Written) != 0 {
		t.Errorf("Install #2 Written=%d; want 0 (idempotent)", len(second.Written))
	}
	if len(second.Skipped) != len(first.Written) {
		t.Errorf("Install #2 Skipped=%d; want %d", len(second.Skipped), len(first.Written))
	}
}

func TestInstallReplacesUntouchedOldCopy(t *testing.T) {
	root := t.TempDir()
	if _, err := install.Install(root, spore.BundledSkills, "bootstrap/skills", install.Options{}); err != nil {
		t.Fatalf("Install #1: %v", err)
	}
	dir := filepath.Join(root, ".claude", "skills")
	target := filepath.Join(dir, "spore-bootstrap", "SKILL.md")
	old := []byte("# the previous bundled version\n")
	if err := os.WriteFile(target, old, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(old)
	manifest := filepath.Join(dir, install.ManifestName)
	m, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("manifest missing after install: %v", err)
	}
	lines := strings.Split(string(m), "\n")
	for i, l := range lines {
		if strings.HasSuffix(l, "  spore-bootstrap/SKILL.md") {
			lines[i] = hex.EncodeToString(sum[:]) + "  spore-bootstrap/SKILL.md"
		}
	}
	if err := os.WriteFile(manifest, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := install.Install(root, spore.BundledSkills, "bootstrap/skills", install.Options{})
	if err != nil {
		t.Fatalf("Install #2: %v", err)
	}
	if len(res.Written) != 1 || res.Written[0] != target || len(res.Kept) != 0 {
		t.Errorf("Written=%v Kept=%v, want only %s rewritten", res.Written, res.Kept, target)
	}
	if body, _ := os.ReadFile(target); !bytes.Contains(body, []byte("name: spore-bootstrap")) {
		t.Errorf("old copy not replaced with the bundled one")
	}
}

func TestInstallKeepsLocalEditUnlessForced(t *testing.T) {
	root := t.TempDir()
	if _, err := install.Install(root, spore.BundledSkills, "bootstrap/skills", install.Options{}); err != nil {
		t.Fatalf("Install #1: %v", err)
	}
	target := filepath.Join(root, ".agents", "skills", "diagram", "SKILL.md")
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	edited := append(body, []byte("LOCAL EDIT CANARY\n")...)
	if err := os.WriteFile(target, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		res, err := install.Install(root, spore.BundledSkills, "bootstrap/skills", install.Options{})
		if err != nil {
			t.Fatalf("Install: %v", err)
		}
		if len(res.Kept) != 1 || res.Kept[0] != target || len(res.Written) != 0 {
			t.Fatalf("run %d: Kept=%v Written=%v, want only %s kept", i, res.Kept, res.Written, target)
		}
		if got, _ := os.ReadFile(target); !bytes.Equal(got, edited) {
			t.Fatalf("run %d: local edit overwritten", i)
		}
	}
	res, err := install.Install(root, spore.BundledSkills, "bootstrap/skills", install.Options{Force: true})
	if err != nil {
		t.Fatalf("Install --force: %v", err)
	}
	if len(res.Written) != 1 || len(res.Kept) != 0 {
		t.Errorf("force: Written=%v Kept=%v, want the edit overwritten", res.Written, res.Kept)
	}
	if got, _ := os.ReadFile(target); !bytes.Equal(got, body) {
		t.Errorf("force did not restore the bundled copy")
	}
}

func TestInstallWithoutManifestKeepsEditsOnly(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claude", "skills")
	edited := filepath.Join(dir, "diagram", "SKILL.md")
	untouched := filepath.Join(dir, "spore-bootstrap", "README.md")
	for path, body := range map[string]string{edited: "# hand-written\n", untouched: ""} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if body == "" {
			continue
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := install.Install(root, spore.BundledSkills, "bootstrap/skills", install.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Kept) != 1 || res.Kept[0] != edited {
		t.Errorf("Kept=%v, want only %s", res.Kept, edited)
	}
	if _, err := os.Stat(untouched); err != nil {
		t.Errorf("missing file not installed: %v", err)
	}
}

func TestInstallEmptyRootReturnsError(t *testing.T) {
	_, err := install.Install("", spore.BundledSkills, "bootstrap/skills", install.Options{})
	if err == nil {
		t.Fatal("Install(\"\") returned nil error; want non-nil")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestInstallFillsCodexSkillDir(t *testing.T) {
	root := t.TempDir()
	if _, err := install.Install(root, spore.BundledSkills, "bootstrap/skills", install.Options{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	for _, name := range []string{"spore-bootstrap", "diagram", "select-agent", "secrets-profile"} {
		claude, err := os.ReadFile(filepath.Join(root, ".claude", "skills", name, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		codex, err := os.ReadFile(filepath.Join(root, ".agents", "skills", name, "SKILL.md"))
		if err != nil {
			t.Fatalf("codex skill %s missing: %v", name, err)
		}
		if !bytes.Equal(claude, codex) {
			t.Errorf("%s: .agents copy differs from .claude copy", name)
		}
	}
	info, err := os.Stat(filepath.Join(root, ".agents", "skills", "diagram", "diagram"))
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("codex diagram script not executable: %v %v", info, err)
	}
}

func TestInstallDirWritesFlatTree(t *testing.T) {
	dest := t.TempDir()
	if _, err := install.InstallDir(dest, spore.BundledSkills, "bootstrap/skills"); err != nil {
		t.Fatalf("InstallDir: %v", err)
	}
	for _, name := range []string{"diagram", "select-agent", "spore-bootstrap", "secrets-profile"} {
		if _, err := os.Stat(filepath.Join(dest, name, "SKILL.md")); err != nil {
			t.Errorf("missing %s/SKILL.md: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, ".claude")); !os.IsNotExist(err) {
		t.Errorf("InstallDir created .claude under dest; want flat tree (err=%v)", err)
	}
	info, err := os.Stat(filepath.Join(dest, "diagram", "diagram"))
	if err != nil {
		t.Fatalf("stat diagram script: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("diagram script not executable: mode=%v", info.Mode().Perm())
	}
}
