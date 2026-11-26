package lints

import (
	"bufio"
	"os"
	"strings"
)

// mdSection is one top-level section of a CLAUDE.md: its heading text,
// 1-indexed heading line, and the body lines up to the next section.
type mdSection struct {
	name  string
	start int
	body  []string
}

// readClaudeSections splits path into top-level sections. Sections
// start at `# ` headings outside fenced code. A file with a single
// `# ` heading treats it as the document title and splits on `## `
// instead, so a subdir file titled `# area` is not one giant section;
// the lines between the title and the first `## ` form a section named
// after the title. Lines before the first heading belong to no section.
func readClaudeSections(path string) ([]mdSection, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	heading := make([]int, len(lines))
	h1 := 0
	fenced := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			fenced = !fenced
			continue
		}
		switch {
		case fenced:
		case strings.HasPrefix(line, "# "):
			heading[i] = 1
			h1++
		case strings.HasPrefix(line, "## "):
			heading[i] = 2
		}
	}
	level := 1
	if h1 == 1 {
		level = 2
	}

	var out []mdSection
	for i, line := range lines {
		if heading[i] == 1 || heading[i] == level {
			name := strings.TrimSpace(strings.TrimLeft(line, "#"))
			out = append(out, mdSection{name: name, start: i + 1})
			continue
		}
		if len(out) > 0 {
			out[len(out)-1].body = append(out[len(out)-1].body, line)
		}
	}
	return out, nil
}
