package fleet

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/versality/spore/internal/task"
)

// RoleRule is one project rule: a markdown fragment appended to a
// role body at spawn time.
type RoleRule struct {
	Name string
	Body []byte
}

// RoleRulesDir returns <state>/roles/rules/<body>/, body being the
// role body file name without .md. Host-side: spore never writes a
// consumer repo.
func RoleRulesDir(projectRoot, name string) (string, error) {
	state, err := task.StateDirForProject(projectRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "roles", "rules", strings.TrimSuffix(name, ".md")), nil
}

// LoadRoleRules returns the placed rules for role body name, sorted
// by file name. A missing dir holds no rules.
func LoadRoleRules(projectRoot, name string) ([]RoleRule, error) {
	dir, err := RoleRulesDir(projectRoot, name)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rolespawn: role rules %s: %w", dir, err)
	}
	var out []RoleRule
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("rolespawn: role rule %s: %w", e.Name(), err)
		}
		out = append(out, RoleRule{Name: e.Name(), Body: b})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ComposeRoleBody appends rules to body under a "Project rules"
// heading. No rules returns body unchanged.
func ComposeRoleBody(body []byte, rules []RoleRule) []byte {
	if len(rules) == 0 {
		return body
	}
	var b bytes.Buffer
	b.Write(bytes.TrimRight(body, "\n"))
	b.WriteString("\n\n## Project rules\n")
	for _, r := range rules {
		b.WriteString("\n")
		b.Write(bytes.TrimSpace(r.Body))
		b.WriteString("\n")
	}
	return b.Bytes()
}

// ResolveRoleBodyWithRules is ResolveRoleBody plus the project's
// placed rules for name. With rules, the composed body is
// materialized under <state>/roles/composed/<sha>/<name>, so live
// panes and `spore eval run` read the same text.
func ResolveRoleBodyWithRules(projectRoot, name string) (string, error) {
	path, err := ResolveRoleBody(projectRoot, name)
	if err != nil {
		return "", err
	}
	rules, err := LoadRoleRules(projectRoot, name)
	if err != nil || len(rules) == 0 {
		return path, err
	}
	base, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("rolespawn: read role body %s: %w", path, err)
	}
	return MaterializeRoleBody(projectRoot, name, ComposeRoleBody(base, rules))
}

// MaterializeRoleBody writes body under
// <state>/roles/composed/<sha>/<name> and returns the path. The
// content hash keeps concurrent spawns with different rule sets from
// rewriting each other's file.
func MaterializeRoleBody(projectRoot, name string, body []byte) (string, error) {
	state, err := task.StateDirForProject(projectRoot)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	path := filepath.Join(state, "roles", "composed", hex.EncodeToString(sum[:6]), name)
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, body) {
		return path, nil
	}
	if err := writePrivateFile(path, string(body)); err != nil {
		return "", fmt.Errorf("rolespawn: materialize role body %s: %w", name, err)
	}
	return path, nil
}
