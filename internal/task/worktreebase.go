package task

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/versality/spore/internal/sporetoml"
)

// WorktreeBase values for `[fleet] worktree_base`.
const (
	WorktreeBaseFresh = "fresh"
	WorktreeBaseHead  = "head"
)

// WorktreeIncludeFile names the project-root file listing gitignored
// paths to copy from the main checkout into each new task worktree.
const WorktreeIncludeFile = ".worktreeinclude"

// LoadWorktreeBase reads `[fleet] worktree_base` from
// <projectRoot>/spore.toml. Anything other than "head" (missing file,
// missing key, typo) yields "fresh".
func LoadWorktreeBase(projectRoot string) string {
	entries, err := sporetoml.ReadSection(projectRoot, "fleet")
	if err != nil {
		return WorktreeBaseFresh
	}
	for _, e := range entries {
		if e.Key == "worktree_base" && strings.TrimSpace(e.Value) == WorktreeBaseHead {
			return WorktreeBaseHead
		}
	}
	return WorktreeBaseFresh
}

// addTaskWorktree runs `git worktree add` for wt/<slug>, reusing an
// existing branch as is. A new branch starts from a freshly fetched
// origin/<default> unless spore.toml opts into "head" or the fetch is
// impossible, in which case it forks from the main checkout's HEAD.
// Matching .worktreeinclude files are copied in afterwards.
func addTaskWorktree(projectRoot, worktree, branch string) error {
	args := []string{"worktree", "add"}
	if branchExists(projectRoot, branch) {
		args = append(args, worktree, branch)
	} else {
		args = append(args, "--no-track", "-b", branch, worktree)
		if start := freshStartPoint(projectRoot); start != "" {
			args = append(args, start)
		}
	}
	if out, err := gitCmd(projectRoot, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := copyWorktreeIncludes(projectRoot, worktree); err != nil {
		fmt.Fprintf(os.Stderr, "spore: %s: copy %s: %v\n", branch, WorktreeIncludeFile, err)
	}
	return nil
}

// freshStartPoint returns "origin/<default>" after fetching it, or ""
// to fall back to HEAD. Never errors: a spawn must not fail on fetch.
func freshStartPoint(projectRoot string) string {
	if LoadWorktreeBase(projectRoot) == WorktreeBaseHead {
		return ""
	}
	if gitCmd(projectRoot, "remote", "get-url", "origin").Run() != nil {
		fmt.Fprintln(os.Stderr, "spore: worktree base: no origin remote, branching from HEAD")
		return ""
	}
	def := remoteDefaultBranch(projectRoot)
	if out, err := gitCmd(projectRoot, "fetch", "--quiet", "origin", def).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "spore: worktree base: git fetch origin %s failed, branching from HEAD: %s\n",
			def, firstLine(strings.TrimSpace(string(out)), err))
		return ""
	}
	ref := "refs/remotes/origin/" + def
	if gitCmd(projectRoot, "rev-parse", "--verify", "--quiet", ref).Run() != nil {
		fmt.Fprintf(os.Stderr, "spore: worktree base: %s missing after fetch, branching from HEAD\n", ref)
		return ""
	}
	return "origin/" + def
}

// remoteDefaultBranch resolves origin's default branch from
// refs/remotes/origin/HEAD, then the first of main / master known
// locally or as a remote-tracking ref, then "main".
func remoteDefaultBranch(projectRoot string) string {
	if out, err := gitCmd(projectRoot, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD").Output(); err == nil {
		if name := strings.TrimPrefix(strings.TrimSpace(string(out)), "refs/remotes/origin/"); name != "" {
			return name
		}
	}
	for _, name := range []string{"main", "master"} {
		for _, ref := range []string{"refs/remotes/origin/" + name, "refs/heads/" + name} {
			if gitCmd(projectRoot, "show-ref", "--verify", "--quiet", ref).Run() == nil {
				return name
			}
		}
	}
	return "main"
}

func firstLine(s string, err error) string {
	if s == "" {
		return err.Error()
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// includePattern is one .worktreeinclude line. Supported syntax: `#`
// comments, a leading `/` or an inner `/` anchoring to the project
// root, a trailing `/` for directories only, and filepath.Match globs
// per path segment, and `!` negation (last matching line wins). Not
// supported: `**` and escaped leading `#` / `!`.
type includePattern struct {
	segs     []string
	anchored bool
	dirOnly  bool
	negate   bool
}

func readWorktreeInclude(projectRoot string) ([]includePattern, error) {
	f, err := os.Open(filepath.Join(projectRoot, WorktreeIncludeFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var pats []includePattern
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var p includePattern
		if strings.HasPrefix(line, "!") {
			p.negate = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			p.dirOnly = true
			line = strings.TrimRight(line, "/")
		}
		if strings.HasPrefix(line, "/") {
			p.anchored = true
			line = strings.TrimLeft(line, "/")
		}
		if strings.Contains(line, "/") {
			p.anchored = true
		}
		if line == "" {
			continue
		}
		p.segs = strings.Split(line, "/")
		pats = append(pats, p)
	}
	return pats, sc.Err()
}

// matches reports whether rel (slash-separated) or one of its ancestor
// directories matches p, gitignore style.
func (p includePattern) matches(rel string, isDir bool) bool {
	segs := strings.Split(rel, "/")
	if !p.anchored {
		for i, s := range segs {
			last := i == len(segs)-1
			if p.dirOnly && last && !isDir {
				continue
			}
			if ok, _ := filepath.Match(p.segs[0], s); ok {
				return true
			}
		}
		return false
	}
	if len(segs) < len(p.segs) {
		return false
	}
	if p.dirOnly && len(segs) == len(p.segs) && !isDir {
		return false
	}
	for i, ps := range p.segs {
		if ok, _ := filepath.Match(ps, segs[i]); !ok {
			return false
		}
	}
	return true
}

// couldMatchBelow reports whether an anchored pattern may match a path
// under directory rel, so an ignored directory is worth walking.
func (p includePattern) couldMatchBelow(rel string) bool {
	if !p.anchored || p.negate {
		return false
	}
	segs := strings.Split(rel, "/")
	if len(segs) >= len(p.segs) {
		return false
	}
	for i, s := range segs {
		if ok, _ := filepath.Match(p.segs[i], s); !ok {
			return false
		}
	}
	return true
}

// copyWorktreeIncludes copies gitignored paths from the main checkout
// that match .worktreeinclude into worktree. Candidates come from
// `git ls-files --others --ignored --exclude-standard --directory`,
// which collapses wholly ignored directories; only anchored patterns
// descend into those, so an unanchored `.env` does not find
// `node_modules/x/.env`. A matched directory is copied whole, without
// re-checking negations inside it. Existing destination paths are left
// alone.
func copyWorktreeIncludes(projectRoot, worktree string) error {
	pats, err := readWorktreeInclude(projectRoot)
	if err != nil || len(pats) == 0 {
		return err
	}
	out, err := gitCmd(projectRoot, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory").Output()
	if err != nil {
		return fmt.Errorf("git ls-files: %w", err)
	}
	matchAny := func(rel string, isDir bool) bool {
		hit := false
		for _, p := range pats {
			if p.matches(rel, isDir) {
				hit = !p.negate
			}
		}
		return hit
	}
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, raw := range bytes.Split(out, []byte{0}) {
		entry := string(raw)
		if entry == "" {
			continue
		}
		isDir := strings.HasSuffix(entry, "/")
		rel := strings.TrimSuffix(entry, "/")
		if rel == ".worktrees" || strings.HasPrefix(rel, ".worktrees/") {
			continue
		}
		if matchAny(rel, isDir) {
			note(copyPath(filepath.Join(projectRoot, rel), filepath.Join(worktree, rel)))
			continue
		}
		if !isDir {
			continue
		}
		descend := false
		for _, p := range pats {
			if p.couldMatchBelow(rel) {
				descend = true
				break
			}
		}
		if !descend {
			continue
		}
		note(filepath.WalkDir(filepath.Join(projectRoot, rel), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			sub, err := filepath.Rel(projectRoot, path)
			if err != nil {
				return err
			}
			sub = filepath.ToSlash(sub)
			if sub == rel {
				return nil
			}
			if matchAny(sub, d.IsDir()) {
				note(copyPath(path, filepath.Join(worktree, sub)))
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				for _, p := range pats {
					if p.couldMatchBelow(sub) {
						return nil
					}
				}
				return filepath.SkipDir
			}
			return nil
		}))
	}
	return firstErr
}

// copyPath copies a file, symlink, or directory tree from src to dst,
// keeping modes and skipping anything already present at dst.
func copyPath(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		if _, err := os.Lstat(target); err == nil {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
