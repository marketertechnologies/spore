// Package install drops the spore skill bodies into a target
// project's .claude/skills/ and .agents/skills/ directories so
// claude-code and Codex in that project can discover and run them. The skill source ships with the spore
// binary via embed.FS (see embed.go), so this works under `nix run`
// without a source-tree checkout.
package install

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Result records what a single Install call did.
type Result struct {
	// Written is the list of destination files that were created or
	// updated, in walk order.
	Written []string
	// Skipped is the list of destination files whose contents already
	// matched the embedded copy, so no write was needed.
	Skipped []string
	// Kept is the list of destination files left alone because their
	// content matches neither the embedded copy nor any bundled version
	// installed before: a local edit. Options.Force overwrites them.
	Kept []string
}

// Options tunes Install. Force overwrites locally edited skill files.
type Options struct {
	Force bool
}

// ManifestName is the file Install keeps in each skill dir: one
// `<sha256>  <path>` line per bundled file it installed, so the next
// run can tell an untouched old copy (safe to replace) from a local
// edit (kept).
const ManifestName = ".spore-install.sha256"

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
// no-op. A destination file with different content is replaced when it
// matches the bundled version recorded in the dir's ManifestName, or
// any version spore shipped before the manifest existed; otherwise it
// is a local edit and lands in Result.Kept unless opts.Force is set.
func Install(root string, src embed.FS, prefix string, opts Options) (Result, error) {
	if root == "" {
		return Result{}, errors.New("install: root is empty")
	}
	var res Result
	for _, dir := range SkillDirs {
		dest := filepath.Join(root, dir)
		prior, err := readManifest(dest)
		if err != nil {
			return res, err
		}
		w := &writer{res: &res, protect: !opts.Force, prior: prior, next: map[string]string{}}
		if err := w.installInto(dest, src, prefix); err != nil {
			return res, err
		}
		if err := writeManifest(dest, w.next); err != nil {
			return res, err
		}
	}
	return res, nil
}

// InstallDir copies every embedded skill under <prefix>/<name>/ into
// <dest>/<name>/, with the same mode and idempotency rules as Install.
// The spore-fleet NixOS module renders the host-wide skill tree with
// it at build time.
func InstallDir(dest string, src embed.FS, prefix string) (Result, error) {
	if dest == "" {
		return Result{}, errors.New("install: dest is empty")
	}
	var res Result
	err := (&writer{res: &res}).installInto(dest, src, prefix)
	return res, err
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

// writer installs one skill tree. With protect set it keeps files that
// match neither the new body, prior[rel] nor a legacy bundled version,
// and records each bundled file's hash in next for the manifest.
type writer struct {
	res     *Result
	protect bool
	prior   map[string]string
	next    map[string]string
}

func (w *writer) installInto(dest string, src embed.FS, prefix string) error {
	return fs.WalkDir(src, prefix, func(p string, d fs.DirEntry, err error) error {
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
		return w.writeSkillFile(filepath.ToSlash(rel), filepath.Join(dest, rel), body, mode)
	})
}

func (w *writer) writeSkillFile(rel, dest string, body []byte, mode os.FileMode) error {
	existing, err := os.ReadFile(dest)
	if err == nil && bytes.Equal(existing, body) {
		w.res.Skipped = append(w.res.Skipped, dest)
		if w.next != nil {
			w.next[rel] = sha256Hex(body)
		}
		if err := os.Chmod(dest, mode); err != nil {
			return fmt.Errorf("chmod %s: %w", dest, err)
		}
		return nil
	}
	if err == nil && w.protect && localEdit(rel, sha256Hex(existing), w.prior[rel]) {
		w.res.Kept = append(w.res.Kept, dest)
		if prev, ok := w.prior[rel]; ok {
			w.next[rel] = prev
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), err)
	}
	if err := os.WriteFile(dest, body, mode); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	if w.next != nil {
		w.next[rel] = sha256Hex(body)
	}
	w.res.Written = append(w.res.Written, dest)
	return nil
}

func localEdit(rel, have, prior string) bool {
	if have == prior {
		return false
	}
	for _, h := range legacyBundled[rel] {
		if have == h {
			return false
		}
	}
	return true
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func readManifest(dir string) (map[string]string, error) {
	out := map[string]string{}
	b, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if hash, rel, ok := strings.Cut(line, "  "); ok {
			out[rel] = hash
		}
	}
	return out, nil
}

func writeManifest(dir string, hashes map[string]string) error {
	if len(hashes) == 0 {
		return nil
	}
	rels := make([]string, 0, len(hashes))
	for rel := range hashes {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	var b strings.Builder
	for _, rel := range rels {
		fmt.Fprintf(&b, "%s  %s\n", hashes[rel], rel)
	}
	path := filepath.Join(dir, ManifestName)
	if old, err := os.ReadFile(path); err == nil && string(old) == b.String() {
		return nil
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}
