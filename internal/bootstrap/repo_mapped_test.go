package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	spore "github.com/versality/spore"
	"github.com/versality/spore/internal/composer"
	"github.com/versality/spore/internal/install"
)

func TestDetectRepoMapped(t *testing.T) {
	cases := []struct {
		name     string
		files    map[string]string
		wantErr  string
		wantNote string
	}{
		{
			name:     "nix flake",
			files:    map[string]string{"flake.nix": "{}\n"},
			wantNote: "detected: nix",
		},
		{
			name:     "rust crate",
			files:    map[string]string{"Cargo.toml": "[package]\n"},
			wantNote: "detected: rust",
		},
		{
			name:     "go module",
			files:    map[string]string{"go.mod": "module x\n"},
			wantNote: "detected: go",
		},
		{
			name:     "node package",
			files:    map[string]string{"package.json": "{}\n"},
			wantNote: "detected: node",
		},
		{
			name:     "polyglot rust+nix",
			files:    map[string]string{"flake.nix": "{}\n", "Cargo.toml": "[package]\n"},
			wantNote: "detected: nix,rust",
		},
		{
			name:    "no markers",
			files:   map[string]string{"random.txt": "x\n"},
			wantErr: "no recognised project marker",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for p, c := range tc.files {
				writeFile(t, filepath.Join(root, p), []byte(c))
			}
			notes, err := detectRepoMapped(root)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err=%v; want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("detect: %v", err)
			}
			if !strings.HasPrefix(notes, tc.wantNote) {
				t.Errorf("notes=%q; want prefix %q", notes, tc.wantNote)
			}
		})
	}
}

func TestDetectRepoMappedDropsStarterInstructions(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), []byte("{}\n"))
	notes, err := detectRepoMapped(root)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !strings.Contains(notes, "wrote starter CLAUDE.md / AGENTS.md") {
		t.Errorf("notes=%q; want mention of starter instruction files", notes)
	}
	claude, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("starter CLAUDE.md missing: %v", err)
	}
	agents, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatalf("starter AGENTS.md missing: %v", err)
	}
	if string(agents) != string(claude) {
		t.Fatalf("starter AGENTS.md differs from CLAUDE.md")
	}
}

func TestDetectRepoMappedSeedsDefaultConsumerList(t *testing.T) {
	root := filepath.Join(t.TempDir(), "My App")
	writeFile(t, filepath.Join(root, "flake.nix"), []byte("{}\n"))
	notes, err := detectRepoMapped(root)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !strings.Contains(notes, "seeded rules/my-app/header.md / rules/consumers/my-app.txt") {
		t.Errorf("notes=%q; want seeded header and consumer list", notes)
	}
	list := filepath.Join(root, "rules", "consumers", "my-app.txt")
	missing, err := composer.MissingDefaults(list)
	if err != nil || len(missing) != 0 {
		t.Fatalf("seeded list missing defaults %v (err %v)", missing, err)
	}
	for _, id := range composer.DefaultCore {
		if _, err := os.Stat(filepath.Join(root, "rules", id+".md")); err != nil {
			t.Errorf("default fragment %s not copied into pool: %v", id, err)
		}
	}
	claude, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(claude), "# PR descriptions") || !strings.Contains(string(claude), "spore compose --consumer my-app") {
		t.Errorf("CLAUDE.md does not render the seeded list:\n%s", claude)
	}
	rendered, err := composer.Compose(filepath.Join(root, "rules"), list, composer.Options{})
	if err != nil || rendered != string(claude) {
		t.Errorf("CLAUDE.md drifts from compose output (err %v)", err)
	}
}

func TestDetectRepoMappedReusesExistingConsumerList(t *testing.T) {
	root := filepath.Join(t.TempDir(), "app")
	writeFile(t, filepath.Join(root, "flake.nix"), []byte("{}\n"))
	writeFile(t, filepath.Join(root, "rules", "app", "own.md"), []byte("# Own rule\n"))
	writeFile(t, filepath.Join(root, "rules", "consumers", "app.txt"), []byte("app/own\n"))
	notes, err := detectRepoMapped(root)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if strings.Contains(notes, "seeded") {
		t.Errorf("notes=%q; existing list must be reused, not reseeded", notes)
	}
	claude, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(claude), "# Own rule") || !strings.Contains(string(claude), "# PR descriptions") {
		t.Errorf("CLAUDE.md = %q; want own rule plus defaults", claude)
	}
}

func TestDetectRepoMappedInstallsSkills(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), []byte("{}\n"))
	notes, err := detectRepoMapped(root)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !strings.Contains(notes, "installed") || !strings.Contains(notes, "skill file") {
		t.Errorf("notes=%q; want mention of installed skill files", notes)
	}
	skill := filepath.Join(root, ".claude", "skills", "spore-bootstrap", "SKILL.md")
	if _, err := os.Stat(skill); err != nil {
		t.Fatalf("spore-bootstrap SKILL.md missing: %v", err)
	}
}

func TestDetectRepoMappedSkipsInstallWhenHostProvidesSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, dir := range install.HostScopeDirs {
		if _, err := install.InstallDir(filepath.Join(home, dir), spore.BundledSkills, "bootstrap/skills"); err != nil {
			t.Fatalf("seed host scope %s: %v", dir, err)
		}
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), []byte("{}\n"))
	notes, err := detectRepoMapped(root)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !strings.Contains(notes, "skills provided host-wide") {
		t.Errorf("notes=%q; want host-wide skip note", notes)
	}
	for _, dir := range install.SkillDirs {
		if _, err := os.Stat(filepath.Join(root, dir)); !os.IsNotExist(err) {
			t.Errorf("%s created in project despite host skills (err=%v)", dir, err)
		}
	}
}

func TestDetectRepoMappedInstallsWhenHostSkillsPartial(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := install.InstallDir(filepath.Join(home, ".claude", "skills"), spore.BundledSkills, "bootstrap/skills"); err != nil {
		t.Fatalf("seed claude scope: %v", err)
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), []byte("{}\n"))
	notes, err := detectRepoMapped(root)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !strings.Contains(notes, "installed") {
		t.Errorf("notes=%q; want project install when only one host scope is filled", notes)
	}
	if _, err := os.Stat(filepath.Join(root, ".agents", "skills", "select-agent", "SKILL.md")); err != nil {
		t.Fatalf("project select-agent missing: %v", err)
	}
}

func TestDetectRepoMappedSkillInstallIsIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), []byte("{}\n"))
	if _, err := detectRepoMapped(root); err != nil {
		t.Fatalf("detect #1: %v", err)
	}
	notes, err := detectRepoMapped(root)
	if err != nil {
		t.Fatalf("detect #2: %v", err)
	}
	if strings.Contains(notes, "installed") {
		t.Errorf("notes=%q; want no install mention on second run (idempotent)", notes)
	}
}

func TestDetectRepoMappedLeavesExistingClaude(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), []byte("{}\n"))
	original := []byte("# my CLAUDE.md\nhello\n")
	writeFile(t, filepath.Join(root, "CLAUDE.md"), original)
	if _, err := detectRepoMapped(root); err != nil {
		t.Fatalf("detect: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("starter clobbered existing CLAUDE.md")
	}
	agents, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatalf("AGENTS.md mirror missing: %v", err)
	}
	if string(agents) != string(original) {
		t.Errorf("AGENTS.md mirror did not match existing CLAUDE.md")
	}
}

func TestDetectRepoMappedLeavesExistingInstructionPair(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), []byte("{}\n"))
	claude := []byte("# existing\nclaude\n")
	agents := []byte("# existing\nagents\n")
	writeFile(t, filepath.Join(root, "CLAUDE.md"), claude)
	writeFile(t, filepath.Join(root, "AGENTS.md"), agents)
	notes, err := detectRepoMapped(root)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if strings.Contains(notes, "wrote starter") {
		t.Errorf("notes=%q; want no starter note", notes)
	}
	gotClaude, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	gotAgents, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if string(gotClaude) != string(claude) || string(gotAgents) != string(agents) {
		t.Errorf("existing instruction files were clobbered")
	}
}
