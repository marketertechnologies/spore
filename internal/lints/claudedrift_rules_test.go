package lints

import (
	"strings"
	"testing"
)

const driftScoped = "---\npaths: [\"app/graphql/**\"]\n---\n# graphql body"

const driftScopedRender = "---\npaths:\n  - \"app/graphql/**\"\n---\n\n" +
	"<!-- generated from host.txt by spore compose; edit fragments, not this file. -->\n\n# graphql body\n"

func driftIssuePaths(t *testing.T, root string) []string {
	t.Helper()
	issues, err := ClaudeDrift{ConsumersDir: "rules/consumers", RulesDir: "rules"}.Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got []string
	for _, i := range issues {
		got = append(got, i.Path)
	}
	return got
}

func TestClaudeDrift_PathScopedRuleFiles(t *testing.T) {
	root := setupDriftRepo(t, map[string]string{
		"rules/area/graphql.md":         driftScoped,
		"rules/consumers/host.txt":      "# target: HOST.md\ncore/a\narea/graphql\n",
		"HOST.md":                       "# a body\n",
		".claude/rules/area/graphql.md": driftScopedRender,
		".claude/rules/hand.md":         "---\npaths: [\"x/**\"]\n---\n# hand-written\n",
	})
	if got := driftIssuePaths(t, root); len(got) != 0 {
		t.Fatalf("expected zero issues, got %v", got)
	}
}

func TestClaudeDrift_PathScopedMissingDriftStale(t *testing.T) {
	root := setupDriftRepo(t, map[string]string{
		"rules/area/graphql.md":         driftScoped,
		"rules/area/specs.md":           "---\npaths: [\"spec/**\"]\n---\n# specs body",
		"rules/consumers/host.txt":      "# target: HOST.md\ncore/a\narea/graphql\narea/specs\n",
		"HOST.md":                       "# a body\n",
		".claude/rules/area/graphql.md": strings.Replace(driftScopedRender, "graphql body", "edited by hand", 1),
		".claude/rules/old.md":          "<!-- generated from host.txt by spore compose; edit fragments, not this file. -->\n\n# old\n",
	})
	got := driftIssuePaths(t, root)
	want := []string{".claude/rules/area/graphql.md", ".claude/rules/area/specs.md", ".claude/rules/old.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("issues = %v, want %v", got, want)
	}
}

func TestClaudeDrift_ImportMirror(t *testing.T) {
	stub := "<!-- generated from host.txt by spore compose; edit fragments, not this file. -->\n\n@AGENTS.md\n"
	consumer := "# target: CLAUDE.md\n# target: AGENTS.md\n# mirror: import\ncore/a\n"
	root := setupDriftRepo(t, map[string]string{
		"rules/consumers/host.txt": consumer,
		"CLAUDE.md":                stub,
		"AGENTS.md":                "# a body\n",
	})
	if got := driftIssuePaths(t, root); len(got) != 0 {
		t.Fatalf("expected zero issues, got %v", got)
	}

	copied := setupDriftRepo(t, map[string]string{
		"rules/consumers/host.txt": consumer,
		"CLAUDE.md":                "# a body\n",
		"AGENTS.md":                "# a body\n",
	})
	if got := driftIssuePaths(t, copied); len(got) != 1 || got[0] != "CLAUDE.md" {
		t.Fatalf("copy-shaped CLAUDE.md under import mode: issues = %v, want [CLAUDE.md]", got)
	}
}

func TestClaudeDrift_TwoConsumersOnePath(t *testing.T) {
	root := setupDriftRepo(t, map[string]string{
		"rules/consumers/host.txt":  "# target: HOST.md\ncore/a\n",
		"rules/consumers/other.txt": "# target: HOST.md\ncore/a\n",
		"HOST.md":                   "# a body\n",
	})
	issues, err := ClaudeDrift{ConsumersDir: "rules/consumers", RulesDir: "rules"}.Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(issues) != 1 || !strings.Contains(issues[0].Message, `"host" and "other"`) {
		t.Fatalf("issues = %+v, want one collision issue", issues)
	}
}
