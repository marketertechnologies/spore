package lints

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/versality/spore/internal/align"
	"github.com/versality/spore/internal/composer"
	"github.com/versality/spore/internal/lang"
)

// ClaudeDrift fails when a consumer's on-disk instruction files
// diverge from what the composer would render. The composer is the
// source of truth; rendered files are derived. To opt a consumer in,
// add one or more "# target: <repo-relative-path>" lines anywhere in
// <ConsumersDir>/<name>.txt (composer skips comment lines so the
// directive is inert during rendering). Consumers without a target
// directive are skipped.
//
// The check covers each target (a CLAUDE.md target is the import stub
// under "# mirror: import"), every path-scoped `.claude/rules/` file
// the consumer renders, and stale generated rule files that no
// opted-in consumer renders any more.
type ClaudeDrift struct {
	ConsumersDir string
	RulesDir     string
}

func (ClaudeDrift) Name() string { return "claude-drift" }

func (l ClaudeDrift) Run(root string) ([]Issue, error) {
	consumersDir := l.ConsumersDir
	if consumersDir == "" {
		consumersDir = "rules/consumers"
	}
	rulesDir := l.RulesDir
	if rulesDir == "" {
		rulesDir = "rules"
	}
	absConsumers := filepath.Join(root, consumersDir)
	absRules := filepath.Join(root, rulesDir)

	entries, err := os.ReadDir(absConsumers)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	alignActive, err := align.Active(root)
	if err != nil {
		return nil, fmt.Errorf("claude-drift: read align state: %w", err)
	}
	opts := composer.Options{Predicates: map[string]bool{
		"align": alignActive,
		"rails": lang.IsRails(root),
	}}

	var issues []Issue
	expected := map[string]string{}
	optedIn := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".txt")
		consumerPath := filepath.Join(absConsumers, e.Name())
		d, err := composer.ReadDirectives(consumerPath)
		if err != nil {
			return nil, err
		}
		if len(d.Targets) == 0 {
			continue
		}
		optedIn = true
		r, err := composer.Render(absRules, consumerPath, opts)
		if err != nil {
			return nil, fmt.Errorf("compose %s: %w", name, err)
		}
		files, err := r.Files()
		if err != nil {
			return nil, fmt.Errorf("compose %s: %w", name, err)
		}
		hint := fmt.Sprintf("rerun `spore compose --consumer %s --write`", name)
		paths := make([]string, 0, len(files))
		for p, content := range files {
			paths = append(paths, p)
			expected[p] = content
		}
		sort.Strings(paths)
		for _, p := range paths {
			on, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
			if err != nil {
				if os.IsNotExist(err) {
					issues = append(issues, Issue{
						Path:    p,
						Message: fmt.Sprintf("missing render target for consumer %q; %s", name, hint),
					})
					continue
				}
				return nil, err
			}
			if string(on) != files[p] {
				issues = append(issues, Issue{
					Path:    p,
					Message: fmt.Sprintf("drift vs composer (consumer %q); %s", name, hint),
				})
			}
		}
	}
	if !optedIn {
		return issues, nil
	}
	stale, err := composer.StaleRuleFiles(root, expected)
	if err != nil {
		return nil, err
	}
	for _, p := range stale {
		issues = append(issues, Issue{
			Path:    p,
			Message: "stale generated rule file that no consumer renders; rerun `spore compose --write` or delete it",
		})
	}
	return issues, nil
}
