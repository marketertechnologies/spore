package lints

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/versality/spore/internal/composer"
)

// ConsumerDefaults flags a root consumer list (one targeting CLAUDE.md
// or AGENTS.md) that does not name every composer.DefaultCore
// fragment, so a new default reaches existing projects on their next
// lint. Subdir lists are skipped: the defaults are project-wide.
// Warn-only: the fix is the project's own commit, made via
// `spore compose --add-defaults`.
type ConsumerDefaults struct {
	ConsumersDir string
}

func (ConsumerDefaults) Name() string { return "consumer-defaults" }

func (ConsumerDefaults) Advisory() bool { return true }

func (l ConsumerDefaults) Run(root string) ([]Issue, error) {
	dir := l.ConsumersDir
	if dir == "" {
		dir = "rules/consumers"
	}
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var issues []Issue
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		path := filepath.Join(root, dir, e.Name())
		targets, err := composer.Targets(path)
		if err != nil {
			return nil, err
		}
		if !targetsRoot(targets) {
			continue
		}
		missing, err := composer.MissingDefaults(path)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(e.Name(), ".txt")
		for _, id := range missing {
			issues = append(issues, Issue{
				Path:    filepath.ToSlash(filepath.Join(dir, e.Name())),
				Message: fmt.Sprintf("missing default core fragment %q; run `spore compose --consumer %s --add-defaults` (rewrites the list's targets)", id, name),
			})
		}
	}
	return issues, nil
}

func targetsRoot(targets []string) bool {
	for _, t := range targets {
		if t == "CLAUDE.md" || t == "AGENTS.md" {
			return true
		}
	}
	return false
}
