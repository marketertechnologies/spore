package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func gitAddAll(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestRunComposeWriteUsesRootForPredicates(t *testing.T) {
	rules := t.TempDir()
	writeTree(t, rules, map[string]string{
		"consumers/x.txt": "# target: CLAUDE.md\n# target: AGENTS.md\ncore/root\n?rails lang/rails\narea/gql\n",
		"core/root.md":    "# root\n",
		"lang/rails.md":   "rails rule\n",
		"area/gql.md":     "---\npaths: [\"app/graphql/**\"]\n---\ngql rule\n",
	})
	proj := t.TempDir()
	writeTree(t, proj, map[string]string{
		"Gemfile":                    "gem 'rails'\n",
		"config/application.rb":      "",
		".claude/rules/old/stale.md": "<!-- generated from x.txt -->\nold\n",
		".claude/rules/hand.md":      "hand-written\n",
	})

	if err := runCompose([]string{"--consumer", "x", "--rules", rules, "--write", "--root", proj}); err != nil {
		t.Fatalf("runCompose: %v", err)
	}
	claude, err := os.ReadFile(filepath.Join(proj, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(claude), "rails rule") {
		t.Errorf("CLAUDE.md lacks the rails fragment; predicates did not read --root:\n%s", claude)
	}
	if strings.Contains(string(claude), "gql rule") {
		t.Errorf("CLAUDE.md inlines the path-scoped fragment:\n%s", claude)
	}
	rule, err := os.ReadFile(filepath.Join(proj, ".claude/rules/area/gql.md"))
	if err != nil || !strings.Contains(string(rule), "gql rule") || !strings.HasPrefix(string(rule), "---\npaths:\n") {
		t.Errorf("rule file = %q, %v", rule, err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".claude/rules/old/stale.md")); !os.IsNotExist(err) {
		t.Errorf("stale generated rule file survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".claude/rules/hand.md")); err != nil {
		t.Errorf("hand-written rule file removed: %v", err)
	}
}

func TestRunLintWarnDoesNotFail(t *testing.T) {
	lines := func(n int) string { return strings.Repeat("x\n", n) }
	warn := t.TempDir()
	writeTree(t, warn, map[string]string{"CLAUDE.md": lines(250), "AGENTS.md": lines(250)})
	gitAddAll(t, warn)
	if code := runLint([]string{"--root", warn}); code != 0 {
		t.Errorf("250-line root: exit %d, want 0 (warning only)", code)
	}
	fail := t.TempDir()
	writeTree(t, fail, map[string]string{"CLAUDE.md": lines(401), "AGENTS.md": lines(401)})
	gitAddAll(t, fail)
	if code := runLint([]string{"--root", fail}); code != 1 {
		t.Errorf("401-line root: exit %d, want 1", code)
	}
}
