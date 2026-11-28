package lints

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AgentMirror keeps Codex-facing AGENTS.md files in lockstep with
// the CLAUDE.md files that already define agent instructions. A pair
// passes in one of two shapes: copy mode (the files are byte-equal)
// or import mode (CLAUDE.md pulls AGENTS.md in with an `@AGENTS.md`
// import line, so AGENTS.md is the single source).
type AgentMirror struct{}

func (AgentMirror) Name() string { return "agents-mirror" }

func (AgentMirror) Run(root string) ([]Issue, error) {
	files, err := listFiles(root, map[string]bool{"CLAUDE.md": true})
	if err != nil {
		return nil, err
	}
	var issues []Issue
	for _, claude := range files {
		if filepath.Base(claude) != "CLAUDE.md" {
			continue
		}
		agents := filepath.ToSlash(filepath.Join(filepath.Dir(claude), "AGENTS.md"))
		if filepath.Dir(claude) == "." {
			agents = "AGENTS.md"
		}
		claudeBytes, err := os.ReadFile(filepath.Join(root, claude))
		if err != nil {
			return nil, err
		}
		agentsBytes, err := os.ReadFile(filepath.Join(root, agents))
		if err != nil {
			if os.IsNotExist(err) {
				issues = append(issues, Issue{
					Path:    agents,
					Message: fmt.Sprintf("missing AGENTS.md mirror for %s", claude),
				})
				continue
			}
			return nil, err
		}
		if bytes.Equal(agentsBytes, claudeBytes) || importsAgents(claudeBytes) {
			continue
		}
		issues = append(issues, Issue{
			Path:    agents,
			Message: fmt.Sprintf("drift vs %s; copy the instruction mirror exactly, or make %s import it with an `@AGENTS.md` line", claude, claude),
		})
	}
	return issues, nil
}

// importsAgents reports whether a CLAUDE.md holds an `@AGENTS.md`
// import on a line of its own, outside fenced code blocks (Claude
// Code skips imports inside fences).
func importsAgents(b []byte) bool {
	inFence := false
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			continue
		}
		if !inFence && (t == "@AGENTS.md" || t == "@./AGENTS.md") {
			return true
		}
	}
	return false
}
