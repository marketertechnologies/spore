package migrations

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runRailsPickup(t *testing.T, home string) string {
	t.Helper()
	script, err := filepath.Abs("../../bootstrap/migrations/002-rails-rule-pickup.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-eu", script)
	cmd.Dir = home
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("migration 002: %v\n%s", err, out)
	}
	return string(out)
}

func railsRepo(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "Gemfile"), "source 'https://rubygems.org'\n")
	writeFile(t, filepath.Join(root, "config/application.rb"), "module App; end\n")
}

// snapshot maps every regular file under root to its content.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		out[p] = readFile(t, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRailsRulePickup_onlySporeConsumers(t *testing.T) {
	home := t.TempDir()

	consumer := filepath.Join(home, "work/app")
	railsRepo(t, consumer)
	writeFile(t, filepath.Join(consumer, "spore.toml"), "[fleet]\nmax_workers = 2\n")
	consumerList := filepath.Join(consumer, "rules/consumers/app.txt")
	writeFile(t, consumerList, "# target: CLAUDE.md\ncore/header")
	handList := filepath.Join(consumer, "rules/consumers/notes.txt")
	writeFile(t, handList, "my own notes\n")

	lookalike := filepath.Join(home, "lookalike")
	railsRepo(t, lookalike)
	writeFile(t, filepath.Join(lookalike, "rules/consumers/notes.txt"), "# target: CLAUDE.md\nuser notes\n")

	nested := filepath.Join(home, "src/other")
	railsRepo(t, nested)
	writeFile(t, filepath.Join(nested, "rules/consumers/list.txt"), "# target: AGENTS.md\n")
	writeFile(t, filepath.Join(nested, "docs/spore.toml"), "")

	before := snapshot(t, home)

	out := runRailsPickup(t, home)

	if got := readFile(t, consumerList); got != "# target: CLAUDE.md\ncore/header\n?rails lang/rails-migrations\n" {
		t.Errorf("consumer list = %q", got)
	}
	frag := filepath.Join(consumer, "rules/lang/rails-migrations.md")
	if !strings.HasPrefix(readFile(t, frag), "# Rails database migrations\n") {
		t.Errorf("fragment not written:\n%s", out)
	}

	after := snapshot(t, home)
	for p, body := range before {
		if p == consumerList {
			continue
		}
		if after[p] != body {
			t.Errorf("%s changed: %q -> %q", p, body, after[p])
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok && p != frag {
			t.Errorf("%s created outside the spore consumer", p)
		}
	}

	out = runRailsPickup(t, home)
	if strings.TrimSpace(out) != "spore migrate 002: all spore consumer repos already carry the rails-migrations rule" {
		t.Errorf("second run not a no-op:\n%s", out)
	}
}

func TestRailsRulePickup_noSporeRepos(t *testing.T) {
	home := t.TempDir()
	railsRepo(t, filepath.Join(home, "proj"))
	list := filepath.Join(home, "proj/rules/consumers/x.txt")
	writeFile(t, list, "# target: CLAUDE.md\n")

	out := runRailsPickup(t, home)

	if !strings.Contains(out, "no spore consumer repos found") {
		t.Errorf("output:\n%s", out)
	}
	if got := readFile(t, list); got != "# target: CLAUDE.md\n" {
		t.Errorf("non-spore list changed: %q", got)
	}
}
