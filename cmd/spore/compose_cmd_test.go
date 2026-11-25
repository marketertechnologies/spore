package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/versality/spore/internal/composer"
	"github.com/versality/spore/internal/lints"
)

func TestComposeAddDefaultsRewritesTargets(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("rules/app/own.md", "# Own\n")
	write("rules/consumers/app.txt", "# target: CLAUDE.md\n# target: AGENTS.md\napp/own\n")
	write("CLAUDE.md", "# Own\n")
	write("AGENTS.md", "# Own\n")

	if err := runCompose([]string{"--consumer", "app", "--add-defaults"}); err != nil {
		t.Fatalf("runCompose: %v", err)
	}
	if missing, _ := composer.MissingDefaults(filepath.Join(root, "rules/consumers/app.txt")); len(missing) != 0 {
		t.Fatalf("still missing %v", missing)
	}
	for _, f := range []string{"CLAUDE.md", "AGENTS.md"} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil || !strings.Contains(string(b), "# PR descriptions") {
			t.Fatalf("%s not re-rendered: %q, %v", f, b, err)
		}
	}
	issues, err := lints.ClaudeDrift{}.Run(root)
	if err != nil || len(issues) != 0 {
		t.Fatalf("claude-drift after --add-defaults = %v, %v; want clean", issues, err)
	}
}
