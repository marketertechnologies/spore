package sporetoml

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// anyKey marks a section whose keys are user-chosen names (matter
// options, worker ratios and rules, align criteria); its owner
// validates them.
const anyKey = "*"

// schema lists every spore.toml section the kernel reads and its keys.
// A loader that gains a key must add it here, or `spore config check`
// flags it. Prefix entries ending in "." match one dotted level, as in
// [matter.linear].
var schema = map[string][]string{
	"coordinator":          {"driver", "model", "brief", "external_session_pattern"},
	"fleet":                {"max_workers", "account_tier", "pre_start", "pre_start_timeout_seconds", "worktree_base", "role_stall_minutes"},
	"fleet.workers":        {"default"},
	"fleet.workers.ratio":  {anyKey},
	"fleet.workers.rules":  {anyKey},
	"fleet.reviewers.A":    {"mode", "agent", "effort"},
	"fleet.reviewers.B":    {"mode", "agent", "effort"},
	"fleet.roles.engineer": {"check", "check_timeout_seconds"},
	"lint":                 {"root_line_limit", "root_char_limit", "subdir_line_limit"},
	"align":                {anyKey},
	"editor":               {"auto_connect_ide"},
	"permissions":          {"default_deny", "deny"},
	"matter.":              {anyKey},
	"agents.coordinator":   {"model", "effort"},
	"agents.worker":        {"model", "effort"},
	"agents.engineer":      {"model", "effort"},
	"agents.reviewer":      {"model", "effort"},
	"agents.reviewer.A":    {"model", "effort"},
	"agents.reviewer.B":    {"model", "effort"},
}

// Problem is one unknown section or key in a spore.toml.
type Problem struct {
	Line    int
	Section string
	Key     string
}

func (p Problem) String() string {
	if p.Key == "" {
		return fmt.Sprintf("line %d: unknown section [%s]", p.Line, p.Section)
	}
	if p.Section == "" {
		return fmt.Sprintf("line %d: unknown top-level key %q", p.Line, p.Key)
	}
	return fmt.Sprintf("line %d: unknown key %q in [%s] (known: %s)", p.Line, p.Key, p.Section, strings.Join(schema[p.Section], ", "))
}

// Check returns the unknown sections and keys in content, in file
// order. Keys under an unknown section are not reported again.
// Malformed lines are left to the section loaders.
func Check(content string) []Problem {
	var out []Problem
	section, known := "", true
	scanner := bufio.NewScanner(strings.NewReader(content))
	for lineNum := 1; scanner.Scan(); lineNum++ {
		line := strings.TrimSpace(StripComment(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.Trim(line, "[]"))
			_, known = sectionKeys(section)
			if !known {
				out = append(out, Problem{Line: lineNum, Section: section})
			}
			continue
		}
		if !known {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if !keyKnown(section, key) {
			out = append(out, Problem{Line: lineNum, Section: section, Key: key})
		}
	}
	return out
}

// CheckFile runs Check over <projectRoot>/spore.toml. A missing file
// has no problems.
func CheckFile(projectRoot string) ([]Problem, error) {
	path := filepath.Join(projectRoot, "spore.toml")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return Check(string(b)), nil
}

// WarnUnknown prints one `warning:` line per CheckFile problem to w,
// each prefixed with who. Read errors are left to the loaders.
func WarnUnknown(w io.Writer, who, projectRoot string) {
	problems, err := CheckFile(projectRoot)
	if err != nil {
		return
	}
	for _, p := range problems {
		fmt.Fprintf(w, "%s: warning: spore.toml %s\n", who, p)
	}
}

func sectionKeys(section string) ([]string, bool) {
	if keys, ok := schema[section]; ok {
		return keys, true
	}
	if i := strings.LastIndexByte(section, '.'); i > 0 {
		if keys, ok := schema[section[:i+1]]; ok {
			return keys, true
		}
	}
	return nil, section == ""
}

func keyKnown(section, key string) bool {
	keys, _ := sectionKeys(section)
	for _, k := range keys {
		if k == anyKey || k == key {
			return true
		}
	}
	return false
}
