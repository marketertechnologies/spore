package task

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// WorktreeIncludeFile names the gitignore-style list, at the main
// checkout's root, of ignored files to copy into each new worktree.
const WorktreeIncludeFile = ".worktreeinclude"

type includePattern struct {
	glob     string
	negate   bool
	dirOnly  bool
	anchored bool
}

// parseWorktreeInclude reads the supported gitignore subset; see
// docs/worker-dispatch.md for the rules.
func parseWorktreeInclude(r io.Reader) []includePattern {
	var pats []includePattern
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var p includePattern
		if strings.HasPrefix(line, "!") {
			p.negate = true
			line = line[1:]
		} else if strings.HasPrefix(line, `\`) {
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			p.dirOnly = true
			line = strings.TrimRight(line, "/")
		}
		if strings.Contains(line, "/") {
			p.anchored = true
			line = strings.TrimPrefix(line, "/")
		}
		if line == "" {
			continue
		}
		p.glob = line
		pats = append(pats, p)
	}
	return pats
}

// includeMatch applies the patterns to one entry git lists as
// ignored. rel is slash-separated and relative to the project root.
// The last matching pattern wins.
func includeMatch(pats []includePattern, rel string, isDir bool) bool {
	matched := false
	for _, p := range pats {
		if p.dirOnly && !isDir {
			continue
		}
		subject := rel
		if !p.anchored {
			subject = path.Base(rel)
		}
		if ok, _ := path.Match(p.glob, subject); ok {
			matched = !p.negate
		}
	}
	return matched
}

// copyWorktreeInclude copies the ignored entries of projectRoot that
// match .worktreeinclude into worktree. Problems warn; none fail the
// spawn. Existing files are never overwritten.
func copyWorktreeInclude(projectRoot, worktree string, warn io.Writer) {
	raw, err := os.ReadFile(filepath.Join(projectRoot, WorktreeIncludeFile))
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(warn, "spore: read %s: %v\n", WorktreeIncludeFile, err)
		}
		return
	}
	pats := parseWorktreeInclude(bytes.NewReader(raw))
	if len(pats) == 0 {
		return
	}
	out, err := gitCmd(projectRoot, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory").Output()
	if err != nil {
		fmt.Fprintf(warn, "spore: %s: list ignored files: %v\n", WorktreeIncludeFile, err)
		return
	}
	c, err := newIncludeCopier(projectRoot, worktree, warn)
	if err != nil {
		fmt.Fprintf(warn, "spore: %s: %v\n", WorktreeIncludeFile, err)
		return
	}
	for _, entry := range strings.Split(string(out), "\x00") {
		if entry == "" {
			continue
		}
		isDir := strings.HasSuffix(entry, "/")
		rel := strings.TrimSuffix(entry, "/")
		if rel == ".worktrees" || strings.HasPrefix(rel, ".worktrees/") || rel == ".git" {
			continue
		}
		if !includeMatch(pats, rel, isDir) {
			continue
		}
		c.copyTree(filepath.FromSlash(rel))
	}
}

type includeCopier struct {
	src, dst         string
	srcReal, dstReal string
	warn             io.Writer
}

func newIncludeCopier(projectRoot, worktree string, warn io.Writer) (*includeCopier, error) {
	srcReal, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		return nil, err
	}
	dstReal, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return nil, err
	}
	return &includeCopier{src: filepath.Clean(projectRoot), dst: filepath.Clean(worktree), srcReal: srcReal, dstReal: dstReal, warn: warn}, nil
}

// copyTree copies rel (a file, symlink or directory) without
// following symlinks while walking.
func (c *includeCopier) copyTree(rel string) {
	err := filepath.WalkDir(filepath.Join(c.src, rel), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		r, err := filepath.Rel(c.src, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		c.copyEntry(r, d.Type())
		return nil
	})
	if err != nil {
		fmt.Fprintf(c.warn, "spore: %s: copy %s: %v\n", WorktreeIncludeFile, rel, err)
	}
}

func (c *includeCopier) copyEntry(rel string, typ fs.FileMode) {
	dst := filepath.Join(c.dst, rel)
	if _, err := os.Lstat(dst); err == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		fmt.Fprintf(c.warn, "spore: %s: %s: %v\n", WorktreeIncludeFile, rel, err)
		return
	}
	// A tracked symlinked directory on the way down could lead out of
	// the worktree; only write where the parent really lies inside it.
	parent, err := filepath.EvalSymlinks(filepath.Dir(dst))
	if err != nil || !within(c.dstReal, parent) {
		fmt.Fprintf(c.warn, "spore: %s: skip %s: parent directory leaves the worktree\n", WorktreeIncludeFile, rel)
		return
	}
	src := filepath.Join(c.src, rel)
	switch {
	case typ&fs.ModeSymlink != 0:
		c.copySymlink(rel, src, dst)
	case typ.IsRegular():
		if err := copyRegular(src, dst); err != nil {
			fmt.Fprintf(c.warn, "spore: %s: copy %s: %v\n", WorktreeIncludeFile, rel, err)
		}
	default:
		fmt.Fprintf(c.warn, "spore: %s: skip %s: not a regular file or symlink\n", WorktreeIncludeFile, rel)
	}
}

// copySymlink recreates a link only when following it from the
// worktree cannot reach the main checkout: absolute targets inside
// the main checkout and relative targets that climb out of it are
// skipped.
func (c *includeCopier) copySymlink(rel, src, dst string) {
	target, err := os.Readlink(src)
	if err != nil {
		fmt.Fprintf(c.warn, "spore: %s: %s: %v\n", WorktreeIncludeFile, rel, err)
		return
	}
	if filepath.IsAbs(target) {
		resolved, err := filepath.EvalSymlinks(target)
		if err != nil {
			resolved = filepath.Clean(target)
		}
		if within(c.src, target) || within(c.srcReal, target) || within(c.src, resolved) || within(c.srcReal, resolved) {
			fmt.Fprintf(c.warn, "spore: %s: skip symlink %s: points into the main checkout\n", WorktreeIncludeFile, rel)
			return
		}
	} else {
		lexical := filepath.Join(filepath.Dir(rel), target)
		if lexical == ".." || strings.HasPrefix(lexical, ".."+string(filepath.Separator)) {
			fmt.Fprintf(c.warn, "spore: %s: skip symlink %s: relative target climbs out of the checkout\n", WorktreeIncludeFile, rel)
			return
		}
	}
	if err := os.Symlink(target, dst); err != nil {
		fmt.Fprintf(c.warn, "spore: %s: %s: %v\n", WorktreeIncludeFile, rel, err)
	}
}

func copyRegular(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// within reports whether p is root or lies under it.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, filepath.Clean(p))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
