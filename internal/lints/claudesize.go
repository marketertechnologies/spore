package lints

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ClaudeSize flags top-level sections in CLAUDE.md files that exceed a
// line threshold. Sections are split by readClaudeSections: `# `
// headings, or `## ` under a lone `# ` title. Blank lines count.
//
// Opt out by placing `<!-- lint: size-ok -->` inside the section body.
type ClaudeSize struct {
	Limit int
}

func (ClaudeSize) Name() string { return "claude-size" }

func (l ClaudeSize) Run(root string) ([]Issue, error) {
	limit := l.Limit
	if limit <= 0 {
		limit = 40
	}
	files, err := listFiles(root, map[string]bool{"CLAUDE.md": true})
	if err != nil {
		return nil, err
	}
	var issues []Issue
	for _, rel := range files {
		if !strings.HasSuffix(rel, "CLAUDE.md") {
			continue
		}
		path := filepath.Join(root, rel)
		found, err := scanClaudeSize(path, rel, limit)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		issues = append(issues, found...)
	}
	return issues, nil
}

const claudeSizeMarker = "<!-- lint: size-ok -->"

func scanClaudeSize(path, rel string, limit int) ([]Issue, error) {
	sections, err := readClaudeSections(path)
	if err != nil {
		return nil, err
	}
	var issues []Issue
	for _, sec := range sections {
		if len(sec.body) <= limit || strings.Contains(strings.Join(sec.body, "\n"), claudeSizeMarker) {
			continue
		}
		issues = append(issues, Issue{
			Path:    rel,
			Line:    sec.start,
			Message: fmt.Sprintf("section %q has %d lines (limit %d)", sec.name, len(sec.body), limit),
		})
	}
	return issues, nil
}
