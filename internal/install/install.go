// Package install drops the spore skill bodies into a target
// project's .claude/skills/ and .agents/skills/ directories so
// claude-code and Codex in that project can discover and run them. The skill source ships with the spore
// binary via embed.FS (see embed.go), so this works under `nix run`
// without a source-tree checkout.
package install

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Result records what a single Install call did.
type Result struct {
	// Written is the list of destination files that were created or
	// updated, in walk order.
	Written []string
	// Skipped is the list of destination files whose contents already
	// matched the embedded copy, so no write was needed.
	Skipped []string
}

// SkillDirs are the project-relative skill roots Install fills:
// claude-code reads .claude/skills, Codex reads .agents/skills in every
// dir from the git root down to cwd (codex-rs ext/skills host_roots.rs).
// A copy, not a symlink, because consumers commit these trees and a
// copy survives tools that do not preserve symlinks.
var SkillDirs = []string{".claude/skills", ".agents/skills"}

// Install copies every embedded skill under <prefix>/<name>/ into
// <root>/<dir>/<name>/ for each of SkillDirs. Files whose content begins with `#!`
// are written 0755; the rest are 0644. Files named `.gitkeep` are
// dropped (they exist only to keep the source tree tidy).
//
// Install is idempotent: re-running it with no source changes is a
// no-op. If a destination file exists with different content, Install
// overwrites it.
func Install(root string, src embed.FS, prefix string) (Result, error) {
	if root == "" {
		return Result{}, errors.New("install: root is empty")
	}
	dests := make([]string, len(SkillDirs))
	for i, dir := range SkillDirs {
		dests[i] = filepath.Join(root, dir)
	}
	return installInto(dests, src, prefix)
}

// InstallDir copies every embedded skill under <prefix>/<name>/ into
// <dest>/<name>/, with the same mode and idempotency rules as Install.
// The spore-fleet NixOS module renders the host-wide skill tree with
// it at build time.
func InstallDir(dest string, src embed.FS, prefix string) (Result, error) {
	if dest == "" {
		return Result{}, errors.New("install: dest is empty")
	}
	return installInto([]string{dest}, src, prefix)
}

// HostScopeDirs are the home-relative user skill scopes the spore-fleet
// NixOS module fills: Claude Code's personal scope and Codex's user
// scope (read from $HOME, not CODEX_HOME).
var HostScopeDirs = []string{".claude/skills", ".agents/skills"}

// HostProvides reports whether every embedded skill under <prefix>/
// already has a SKILL.md in each of HostScopeDirs under home. A
// project copy alongside them is redundant for Claude Code (personal
// beats project) and harmful for Codex (it keeps both and a plain
// $name mention then selects neither). A partial host set reports
// false so the caller falls back to a project install.
func HostProvides(home string, src embed.FS, prefix string) bool {
	if home == "" {
		return false
	}
	entries, err := fs.ReadDir(src, prefix)
	if err != nil {
		return false
	}
	found := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, dir := range HostScopeDirs {
			if _, err := os.Stat(filepath.Join(home, dir, e.Name(), "SKILL.md")); err != nil {
				return false
			}
		}
		found = true
	}
	return found
}

func installInto(dests []string, src embed.FS, prefix string) (Result, error) {
	var res Result
	err := fs.WalkDir(src, prefix, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(prefix, p)
		if err != nil {
			return err
		}
		if filepath.Base(rel) == ".gitkeep" {
			return nil
		}
		body, err := src.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		mode := os.FileMode(0o644)
		if bytes.HasPrefix(body, []byte("#!")) {
			mode = 0o755
		}
		for _, dest := range dests {
			if err := writeSkillFile(&res, filepath.Join(dest, rel), body, mode); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

func writeSkillFile(res *Result, dest string, body []byte, mode os.FileMode) error {
	if existing, err := os.ReadFile(dest); err == nil && bytes.Equal(existing, body) {
		res.Skipped = append(res.Skipped, dest)
		if err := os.Chmod(dest, mode); err != nil {
			return fmt.Errorf("chmod %s: %w", dest, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), err)
	}
	if err := os.WriteFile(dest, body, mode); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	res.Written = append(res.Written, dest)
	return nil
}
