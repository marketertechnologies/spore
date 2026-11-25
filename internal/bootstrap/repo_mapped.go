package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	spore "github.com/versality/spore"
	"github.com/versality/spore/internal/composer"
	"github.com/versality/spore/internal/install"
)

// repoMarkers maps a marker file (project root relative) to a short
// language / build-system label. Order is irrelevant; the detector
// reports every marker it finds, sorted, so notes are deterministic.
var repoMarkers = map[string]string{
	"flake.nix":      "nix",
	"Cargo.toml":     "rust",
	"go.mod":         "go",
	"package.json":   "node",
	"pyproject.toml": "python",
	"setup.py":       "python",
	"Gemfile":        "ruby",
	"deps.edn":       "clojure",
	"project.clj":    "clojure",
	"pom.xml":        "java",
	"build.gradle":   "gradle",
	"Makefile":       "make",
	"justfile":       "just",
}

// starterHeader is the project-local header fragment spore seeds for
// a project with no instruction files. The rendered CLAUDE.md and
// AGENTS.md are the same bytes so Claude Code and Codex start from the
// same contract.
const starterHeader = `# Agent Instructions

This project uses spore for agent governance. Edit the fragments under
` + "`rules/`" + ` and the list in ` + "`rules/consumers/%[1]s.txt`" + `, then re-render with
` + "`spore compose --consumer %[1]s > CLAUDE.md`" + ` and copy CLAUDE.md to AGENTS.md.

## Validation

Run ` + "`spore lint`" + ` for the portable lint set and ` + "`spore bootstrap status`" + ` to
see which onboarding stages are still pending.
`

func detectRepoMapped(root string) (string, error) {
	if root == "" {
		return "", errors.New("repo-mapped: empty root")
	}
	var hits []string
	seenLabel := map[string]bool{}
	for marker, label := range repoMarkers {
		if _, err := os.Stat(filepath.Join(root, marker)); err == nil {
			if !seenLabel[label] {
				hits = append(hits, label)
				seenLabel[label] = true
			}
		}
	}
	if len(hits) == 0 {
		return "", errors.New("no recognised project marker (flake.nix / Cargo.toml / go.mod / package.json / pyproject.toml / Gemfile / deps.edn / pom.xml / Makefile / justfile)")
	}
	sort.Strings(hits)

	wrote, seeded, err := ensureInstructionFiles(root)
	if err != nil {
		return "", err
	}

	notes := "detected: " + strings.Join(hits, ",")
	if len(wrote) > 0 {
		notes += "; wrote starter " + strings.Join(wrote, " / ")
	}
	if len(seeded) > 0 {
		notes += "; seeded " + strings.Join(seeded, " / ")
	}
	home, _ := os.UserHomeDir()
	if install.HostProvides(home, spore.BundledSkills, "bootstrap/skills") {
		return notes + "; skills provided host-wide", nil
	}
	skills, err := install.Install(root, spore.BundledSkills, "bootstrap/skills")
	if err != nil {
		return "", fmt.Errorf("install skills: %w", err)
	}
	if len(skills.Written) > 0 {
		notes += fmt.Sprintf("; installed %d skill file(s)", len(skills.Written))
	}
	return notes, nil
}

func ensureInstructionFiles(root string) (wrote, seeded []string, err error) {
	claudePath := filepath.Join(root, "CLAUDE.md")
	agentsPath := filepath.Join(root, "AGENTS.md")
	claude, claudeErr := os.ReadFile(claudePath)
	agents, agentsErr := os.ReadFile(agentsPath)

	if claudeErr != nil && !os.IsNotExist(claudeErr) {
		return nil, nil, claudeErr
	}
	if agentsErr != nil && !os.IsNotExist(agentsErr) {
		return nil, nil, agentsErr
	}

	switch {
	case os.IsNotExist(claudeErr) && os.IsNotExist(agentsErr):
		var rendered string
		seeded, rendered, err = seedConsumer(root)
		if err != nil {
			return nil, nil, err
		}
		if err := os.WriteFile(claudePath, []byte(rendered), 0o644); err != nil {
			return nil, nil, fmt.Errorf("write starter CLAUDE.md: %w", err)
		}
		if err := os.WriteFile(agentsPath, []byte(rendered), 0o644); err != nil {
			return nil, nil, fmt.Errorf("write starter AGENTS.md: %w", err)
		}
		wrote = append(wrote, "CLAUDE.md", "AGENTS.md")
	case os.IsNotExist(claudeErr):
		if err := os.WriteFile(claudePath, agents, 0o644); err != nil {
			return nil, nil, fmt.Errorf("write starter CLAUDE.md: %w", err)
		}
		wrote = append(wrote, "CLAUDE.md")
	case os.IsNotExist(agentsErr):
		if err := os.WriteFile(agentsPath, claude, 0o644); err != nil {
			return nil, nil, fmt.Errorf("write starter AGENTS.md: %w", err)
		}
		wrote = append(wrote, "AGENTS.md")
	}
	return wrote, seeded, nil
}

// seedConsumer gives a project without instruction files a consumer
// list named after its directory: a local header fragment plus
// composer.DefaultCore, with target directives so the claude-drift
// lint tracks the render from day one. An existing list is reused.
// Returns the repo-relative paths it created and the rendered text.
func seedConsumer(root string) ([]string, string, error) {
	name := consumerName(root)
	rulesDir := filepath.Join(root, "rules")
	listRel := filepath.Join("rules", "consumers", name+".txt")
	listPath := filepath.Join(root, listRel)
	var seeded []string
	if _, err := os.Stat(listPath); os.IsNotExist(err) {
		headerRel := filepath.Join("rules", name, "header.md")
		headerPath := filepath.Join(root, headerRel)
		if _, err := os.Stat(headerPath); os.IsNotExist(err) {
			if err := writeSeed(headerPath, fmt.Sprintf(starterHeader, name)); err != nil {
				return nil, "", err
			}
			seeded = append(seeded, headerRel)
		}
		list := fmt.Sprintf("# %s's root instruction files, rendered by `spore compose`.\n# target: CLAUDE.md\n# target: AGENTS.md\n%s/header\n", name, name)
		if err := writeSeed(listPath, list); err != nil {
			return nil, "", err
		}
		seeded = append(seeded, listRel)
	} else if err != nil {
		return nil, "", err
	}
	if _, err := composer.AddDefaults(rulesDir, listPath, spore.CorePool()); err != nil {
		return nil, "", err
	}
	rendered, err := composer.Compose(rulesDir, listPath, composer.Options{})
	if err != nil {
		return nil, "", err
	}
	return seeded, rendered, nil
}

func writeSeed(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

var consumerNameRE = regexp.MustCompile(`[^a-z0-9_-]+`)

// consumerName derives a consumer list name from the project directory.
func consumerName(root string) string {
	name := strings.Trim(consumerNameRE.ReplaceAllString(strings.ToLower(filepath.Base(root)), "-"), "-")
	if name == "" {
		return "project"
	}
	return name
}
