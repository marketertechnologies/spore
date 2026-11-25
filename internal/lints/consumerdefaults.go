package lints

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/versality/spore/internal/composer"
)

// ConsumerDefaults flags a consumer list that does not name every
// composer.DefaultCore fragment, so a new default reaches existing
// projects on their next lint. Warn-only: the fix is the project's own
// commit, made via `spore compose --add-defaults`.
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
		missing, err := composer.MissingDefaults(filepath.Join(root, dir, e.Name()))
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(e.Name(), ".txt")
		for _, id := range missing {
			issues = append(issues, Issue{
				Path:    filepath.ToSlash(filepath.Join(dir, e.Name())),
				Message: fmt.Sprintf("missing default core fragment %q; run `spore compose --consumer %s --add-defaults`", id, name),
			})
		}
	}
	return issues, nil
}
