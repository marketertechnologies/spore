package composer

import (
	"fmt"
	"strings"
)

const frontmatterFence = "---"

// parseFrontmatter splits an optional leading `---` block off a
// fragment and returns its `paths` globs plus the remaining body. It
// reads the small YAML subset Claude Code's rule frontmatter uses: a
// flow list (`paths: ["a", "b"]`), a block list (`paths:` then
// `  - "a"` lines), or a comma-separated string (`paths: a, b`). Any
// other key is an error so a typo does not silently inline a fragment
// meant to be path-scoped.
func parseFrontmatter(src string) ([]string, string, error) {
	lines := strings.Split(src, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != frontmatterFence {
		return nil, src, nil
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == frontmatterFence {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, "", fmt.Errorf("frontmatter: no closing %q", frontmatterFence)
	}
	body := strings.Join(lines[end+1:], "\n")

	var globs []string
	seenPaths := false
	inBlock := false
	for i := 1; i < end; i++ {
		raw := lines[i]
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if inBlock && strings.HasPrefix(line, "- ") {
			globs = append(globs, unquoteYAML(strings.TrimSpace(line[2:])))
			continue
		}
		inBlock = false
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			return nil, "", fmt.Errorf("frontmatter line %d: malformed %q", i+1, line)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if key != "paths" {
			return nil, "", fmt.Errorf("frontmatter line %d: unsupported key %q (only paths)", i+1, key)
		}
		if seenPaths {
			return nil, "", fmt.Errorf("frontmatter line %d: duplicate paths key", i+1)
		}
		seenPaths = true
		switch {
		case val == "":
			inBlock = true
		case strings.HasPrefix(val, "["):
			if !strings.HasSuffix(val, "]") {
				return nil, "", fmt.Errorf("frontmatter line %d: unterminated flow list %q", i+1, val)
			}
			for _, item := range splitGlobList(val[1 : len(val)-1]) {
				globs = append(globs, unquoteYAML(item))
			}
		default:
			for _, item := range splitGlobList(unquoteYAML(val)) {
				globs = append(globs, unquoteYAML(item))
			}
		}
	}
	if !seenPaths {
		return nil, body, nil
	}
	var clean []string
	for _, g := range globs {
		if g != "" {
			clean = append(clean, g)
		}
	}
	if len(clean) == 0 {
		return nil, "", fmt.Errorf("frontmatter: paths is empty")
	}
	return clean, body, nil
}

// splitGlobList splits on commas that sit outside quotes and outside
// brace groups, so `"src/*.{ts,tsx}", lib/**` yields two items.
func splitGlobList(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	depth := 0
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '{':
			depth++
		case r == '}' && depth > 0:
			depth--
		case r == ',' && depth == 0:
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	if t := strings.TrimSpace(cur.String()); t != "" {
		out = append(out, t)
	}
	return out
}

func unquoteYAML(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		r := strings.NewReplacer(`\"`, `"`, `\\`, `\`)
		return r.Replace(s[1 : len(s)-1])
	}
	return s
}

// quoteYAML picks single quotes when the glob holds a backslash: Claude
// Code's docs escape a literal `[` as `'\['`, and `\[` is not a valid
// escape inside a YAML double-quoted string.
func quoteYAML(s string) string {
	if strings.Contains(s, `\`) {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func renderFrontmatter(globs []string) string {
	var b strings.Builder
	b.WriteString(frontmatterFence + "\npaths:\n")
	for _, g := range globs {
		b.WriteString("  - " + quoteYAML(g) + "\n")
	}
	b.WriteString(frontmatterFence + "\n")
	return b.String()
}
