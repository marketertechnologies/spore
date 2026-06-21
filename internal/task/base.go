package task

import (
	"os"
	"path/filepath"

	"github.com/versality/spore/internal/sporetoml"
)

// DefaultBase is the integration branch a worker's wt/<slug> lands on
// when spore.toml does not configure one. Downstream consumers
// (marketer, crm-gateway) integrate on a local main, so this stays
// "main" and their behavior is unchanged.
const DefaultBase = "main"

// IntegrationBase returns the integration base branch from spore.toml's
// `[fleet] base` key, or DefaultBase when unset or unreadable. This is
// the branch ship/merge target and that UnmergedCommits diffs a worker
// branch against, so a repo that integrates on a non-main branch (e.g.
// a long-lived release branch with no local main) can point the whole
// lifecycle at it.
func IntegrationBase(projectRoot string) string {
	b, err := os.ReadFile(filepath.Join(projectRoot, "spore.toml"))
	if err != nil {
		return DefaultBase
	}
	base := DefaultBase
	_ = sporetoml.ScanSections(string(b), func(l sporetoml.Line) error {
		if l.Section != "fleet" {
			return nil
		}
		key, raw, ok := sporetoml.SplitKeyValue(l.Text)
		if ok && key == "base" {
			if v := sporetoml.StripQuotes(raw); v != "" {
				base = v
			}
		}
		return nil
	})
	return base
}

// resolveBaseRef resolves a configured integration base to a concrete
// git ref to diff against, trying in order: the base as a local branch,
// local main, local master, origin/<base>, then origin/main. ok is
// false when none resolve, so callers treat "no base" as "nothing
// unmerged" rather than running git against a missing ref - which exits
// 128 (the wedge this repo hit: it integrates on a feature branch with
// no local main, so the old `main`->`master` fallback ran
// `git rev-list master..wt/<slug>` against a ref that does not exist).
func resolveBaseRef(projectRoot, base string) (string, bool) {
	if base == "" {
		base = DefaultBase
	}
	for _, ref := range []string{
		"refs/heads/" + base,
		"refs/heads/main",
		"refs/heads/master",
		"refs/remotes/origin/" + base,
		"refs/remotes/origin/main",
	} {
		if gitCmd(projectRoot, "show-ref", "--verify", "--quiet", ref).Run() == nil {
			return ref, true
		}
	}
	return "", false
}
