// Package spore exists solely to host go:embed assets that ship with
// the spore CLI. The kernel implementation lives under cmd/ and
// internal/; this top-level package is just an asset container.
package spore

import (
	"embed"
	"strings"
)

// Version returns the release version embedded from the repository's
// VERSION file.
//
//go:embed VERSION
var versionFile string

var buildCommit = "unknown"

func Version() string {
	return strings.TrimSpace(versionFile)
}

func BuildCommit() string {
	return strings.TrimSpace(buildCommit)
}

func BuildVersion() string {
	commit := BuildCommit()
	if commit == "" || commit == "unknown" {
		return Version() + " (commit unknown)"
	}
	return Version() + " (" + commit + ")"
}

// BundledFlake is the minimal NixOS flake `spore infect` stages into a
// temp directory and runs nixos-anywhere against when the operator
// does not pass --flake. See bootstrap/flake/README.md for shape and
// limits.
//
//go:embed all:bootstrap/flake
var BundledFlake embed.FS

// BundledSkills is the skill tree `spore install` drops into a target
// project's .claude/skills/ directory so the agent can discover the
// spore-bootstrap and diagram skills without a source-tree checkout.
//
//go:embed all:bootstrap/skills
var BundledSkills embed.FS

// BundledHandover is the attach shell, agent wrappers, hooks, and
// systemd user units installed by `spore infect --repo`.
//
//go:embed all:bootstrap/handover
var BundledHandover embed.FS

// BundledScripts is the harness shell-script tree `spore install`
// drops into a target project's harness/ directory. These are the
// generic-core wrappers (hooks-render, auto-commit-tasks, quiet-run,
// report-main-worktree-dirty) lifted out of nix-config so consumers
// pick them up via the spore binary instead of vendoring per-project.
//
//go:embed all:bootstrap/scripts
var BundledScripts embed.FS

// BundledMigrations is the embedded tree of idempotent host-state
// migration scripts `spore migrate` runs against a deployed host. Each
// NNN-slug.sh under bootstrap/migrations/ runs once per host, tracked
// in a ledger; see bootstrap/migrations/README.md for the contract.
//
//go:embed all:bootstrap/migrations
var BundledMigrations embed.FS

// BundledRecipes is the embedded recipe library `spore recipes ls` and
// `spore recipes show <name>` read from. Each markdown file under
// bootstrap/recipes/ is a reusable how-to for talking to an external
// system (Jira, GitHub, Sentry) from a coordinator or worker pane.
//
//go:embed all:bootstrap/recipes
var BundledRecipes embed.FS

// BundledCoordinatorRole is the default role file the fleet reconciler
// uses to boot the singleton coordinator agent. Consumers can override
// by writing their own bootstrap/coordinator/role.md before bootstrap.
//
//go:embed bootstrap/coordinator/role.md
var BundledCoordinatorRole []byte
