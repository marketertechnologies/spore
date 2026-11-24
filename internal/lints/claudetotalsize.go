package lints

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/versality/spore/internal/sporetoml"
)

// ClaudeTotalSize flags whole-file CLAUDE.md / AGENTS.md that exceed
// total-size caps. The root file (CLAUDE.md or AGENTS.md at the repo
// root) is capped on lines and chars; whichever cap is hit first
// triggers the issue. Subdirectory mirrors get a smaller line cap and
// no char cap; their job is single-area pointer notes, not narrative.
//
// Limits resolve from the struct fields, then spore.toml `[lint]`
// (root_line_limit, root_char_limit, subdir_line_limit), then the
// defaults. A limit set in either place is strict. The default root
// line limit is 200, following Claude Code's sizing guidance, with a
// grace band: a root file between 200 and 400 lines (the old default)
// is a warning, above 400 an error. Pin `root_line_limit` to opt out
// of the band in either direction.
//
// Opt out by placing `<!-- lint: totalsize-ok -->` anywhere in the file.
type ClaudeTotalSize struct {
	RootLineLimit   int
	RootCharLimit   int
	SubdirLineLimit int
}

const (
	defaultRootLineLimit   = 200
	graceRootLineLimit     = 400
	defaultRootCharLimit   = 40000
	defaultSubdirLineLimit = 150
	claudeTotalSizeMarker  = "<!-- lint: totalsize-ok -->"
)

func (ClaudeTotalSize) Name() string { return "claude-totalsize" }

type totalSizeLimits struct {
	rootLines   int
	rootGrace   int
	rootChars   int
	subdirLines int
}

func (l ClaudeTotalSize) limits(root string) (totalSizeLimits, error) {
	lim := totalSizeLimits{
		rootLines:   defaultRootLineLimit,
		rootGrace:   graceRootLineLimit,
		rootChars:   defaultRootCharLimit,
		subdirLines: defaultSubdirLineLimit,
	}
	entries, err := sporetoml.ReadSection(root, "lint")
	if err != nil {
		return lim, fmt.Errorf("claude-totalsize: %w", err)
	}
	for _, e := range entries {
		var dst *int
		switch e.Key {
		case "root_line_limit":
			dst = &lim.rootLines
			lim.rootGrace = 0
		case "root_char_limit":
			dst = &lim.rootChars
		case "subdir_line_limit":
			dst = &lim.subdirLines
		default:
			continue
		}
		n, err := strconv.Atoi(e.Value)
		if err != nil || n <= 0 {
			return lim, fmt.Errorf("claude-totalsize: spore.toml line %d: %s: want integer > 0, got %q", e.Line, e.Key, e.Value)
		}
		*dst = n
	}
	if l.RootLineLimit > 0 {
		lim.rootLines = l.RootLineLimit
		lim.rootGrace = 0
	}
	if l.RootCharLimit > 0 {
		lim.rootChars = l.RootCharLimit
	}
	if l.SubdirLineLimit > 0 {
		lim.subdirLines = l.SubdirLineLimit
	}
	return lim, nil
}

func (l ClaudeTotalSize) Run(root string) ([]Issue, error) {
	lim, err := l.limits(root)
	if err != nil {
		return nil, err
	}

	files, err := listFiles(root, map[string]bool{
		"CLAUDE.md": true,
		"AGENTS.md": true,
	})
	if err != nil {
		return nil, err
	}

	var issues []Issue
	for _, rel := range files {
		path := filepath.Join(root, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if bytes.Contains(data, []byte(claudeTotalSizeMarker)) {
			continue
		}
		lines := countTotalLines(data)
		chars := len(data)

		if rel == "CLAUDE.md" || rel == "AGENTS.md" {
			if chars > lim.rootChars || lines > max(lim.rootLines, lim.rootGrace) {
				issues = append(issues, Issue{
					Path:    rel,
					Message: fmt.Sprintf("file is %d lines / %d chars (limit: %d lines, %d chars)", lines, chars, max(lim.rootLines, lim.rootGrace), lim.rootChars),
				})
			} else if lines > lim.rootLines {
				issues = append(issues, Issue{
					Path:    rel,
					Warn:    true,
					Message: fmt.Sprintf("file is %d lines, over the %d-line target (error above %d); move area rules to path-scoped fragments, or pin [lint] root_line_limit in spore.toml", lines, lim.rootLines, lim.rootGrace),
				})
			}
			continue
		}
		if lines > lim.subdirLines {
			issues = append(issues, Issue{
				Path:    rel,
				Message: fmt.Sprintf("file is %d lines (limit: %d lines)", lines, lim.subdirLines),
			})
		}
	}
	return issues, nil
}

func countTotalLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := bytes.Count(data, []byte("\n"))
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}
