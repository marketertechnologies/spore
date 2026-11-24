package lints

import (
	"strings"
	"testing"
)

func TestAgentMirror_ImportMode(t *testing.T) {
	root := newTestRepo(t, map[string]string{
		"CLAUDE.md":     "<!-- generated -->\n\n@AGENTS.md\n",
		"AGENTS.md":     "# Root\nrules\n",
		"sub/CLAUDE.md": "@./AGENTS.md\n\n## Claude only\n",
		"sub/AGENTS.md": "# Sub\n",
	})
	issues, err := AgentMirror{}.Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("expected zero issues, got %v", issues)
	}
}

func TestAgentMirror_ImportInsideFenceIsDrift(t *testing.T) {
	root := newTestRepo(t, map[string]string{
		"CLAUDE.md": "```\n@AGENTS.md\n```\n",
		"AGENTS.md": "# Root\n",
	})
	issues, err := AgentMirror{}.Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(issues) != 1 || !strings.Contains(issues[0].Message, "drift") {
		t.Fatalf("expected one drift issue, got %v", issues)
	}
}

func TestAgentMirror_ImportWithoutAgents(t *testing.T) {
	root := newTestRepo(t, map[string]string{
		"CLAUDE.md": "@AGENTS.md\n",
	})
	issues, err := AgentMirror{}.Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(issues) != 1 || !strings.Contains(issues[0].Message, "missing") {
		t.Fatalf("expected one missing issue, got %v", issues)
	}
}
