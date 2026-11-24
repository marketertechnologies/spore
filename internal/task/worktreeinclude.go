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
		line := trimTrailingSpaces(strings.TrimSuffix(sc.Text(), "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var p includePattern
		switch {
		case strings.HasPrefix(line, "!"):
			p.negate = true
			line = line[1:]
		case strings.HasPrefix(line, `\#`), strings.HasPrefix(line, `\!`):
			// Other escapes stay: path.Match reads `\*` as a literal `*`.
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

// trimTrailingSpaces drops trailing spaces unless escaped (`\ `), as
// gitignore does; tabs are kept.
func trimTrailingSpaces(line string) string {
	for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, `\ `) {
		line = line[:len(line)-1]
	}
	return line
}

// includeMatch applies the patterns to one entry git lists as
// ignored. rel is slash-separated and relative to the project root.
// The last matching pattern wins.
func includeMatch(pats []includePattern, rel string, isDir bool) bool {
	matched, _ := includeState(pats, rel, isDir)
	return matched
}

// includeState is includeMatch plus whether any pattern matched rel
// at all; an entry inside a copied directory no pattern names is
// copied with it.
func includeState(pats []includePattern, rel string, isDir bool) (matched, any bool) {
	for _, p := range pats {
		if p.dirOnly && !isDir {
			continue
		}
		subject := rel
		if !p.anchored {
			subject = path.Base(rel)
		}
		if ok, _ := path.Match(p.glob, subject); ok {
			matched, any = !p.negate, true
		}
	}
	return matched, any
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
	c.pats = pats
	// git lists entries sorted, so a copied directory precedes the
	// entries under it, which its walk already covered.
	var copiedDir string
	for _, entry := range strings.Split(string(out), "\x00") {
		if entry == "" {
			continue
		}
		isDir := strings.HasSuffix(entry, "/")
		rel := strings.TrimSuffix(entry, "/")
		if rel == ".worktrees" || strings.HasPrefix(rel, ".worktrees/") || rel == ".git" {
			continue
		}
		if copiedDir != "" && strings.HasPrefix(rel, copiedDir+"/") {
			continue
		}
		if !includeMatch(pats, rel, isDir) {
			continue
		}
		c.copyTree(filepath.FromSlash(rel))
		if isDir {
			copiedDir = rel
		}
	}
}

type includeCopier struct {
	src, dst         string
	srcReal, dstReal string
	warn             io.Writer
	// pats re-applies .worktreeinclude inside a copied directory, so
	// a later `!dir/secret` still excludes that file (last match wins).
	pats []includePattern
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
// following symlinks while walking. rel itself matched; each entry
// below it is included unless a pattern naming it says otherwise. As
// in gitignore, an excluded directory is skipped whole: nothing
// below it can be re-included.
func (c *includeCopier) copyTree(rel string) {
	err := filepath.WalkDir(filepath.Join(c.src, rel), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		r, err := filepath.Rel(c.src, p)
		if err != nil {
			return err
		}
		if r != rel {
			if m, any := includeState(c.pats, filepath.ToSlash(r), d.IsDir()); any && !m {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if !d.IsDir() {
			c.copyEntry(r, d.Type())
		}
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
	// A tracked symlinked directory on the way down could lead out of
	// the worktree. Check the deepest existing ancestor before MkdirAll
	// so not even an empty directory is created outside it.
	if !c.insideWorktree(filepath.Dir(dst)) {
		fmt.Fprintf(c.warn, "spore: %s: skip %s: parent directory leaves the worktree\n", WorktreeIncludeFile, rel)
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		fmt.Fprintf(c.warn, "spore: %s: %s: %v\n", WorktreeIncludeFile, rel, err)
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

// insideWorktree resolves the deepest existing ancestor of dir and
// reports whether it lies inside the worktree.
func (c *includeCopier) insideWorktree(dir string) bool {
	for {
		if _, err := os.Lstat(dir); err == nil {
			resolved, err := filepath.EvalSymlinks(dir)
			return err == nil && within(c.dstReal, resolved)
		}
		next := filepath.Dir(dir)
		if next == dir {
			return false
		}
		dir = next
	}
}

// copySymlink recreates a link only when following it from the
// worktree cannot reach the main checkout: absolute targets inside
// the main checkout and relative targets that climb out of it are
// skipped, as is a relative target that resolves (through EvalSymlinks
// in the main checkout) outside it, or that cannot be resolved and
// has a `..` after a named element (`link/../x` climbs from wherever
// `link` points).
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
		escapes := lexical == ".." || strings.HasPrefix(lexical, ".."+string(filepath.Separator))
		if !escapes {
			// Not filepath.Join: cleaning would fold `link/..` before
			// EvalSymlinks could follow link.
			raw := filepath.Join(c.src, filepath.Dir(rel)) + string(filepath.Separator) + target
			resolved, err := filepath.EvalSymlinks(raw)
			if err == nil {
				escapes = !within(c.srcReal, resolved)
			} else {
				escapes = innerDotDot(target)
			}
		}
		if escapes {
			fmt.Fprintf(c.warn, "spore: %s: skip symlink %s: relative target climbs out of the checkout\n", WorktreeIncludeFile, rel)
			return
		}
	}
	if err := os.Symlink(target, dst); err != nil {
		fmt.Fprintf(c.warn, "spore: %s: %s: %v\n", WorktreeIncludeFile, rel, err)
	}
}

// innerDotDot reports a `..` element after a named one.
func innerDotDot(target string) bool {
	named := false
	for _, el := range strings.Split(filepath.ToSlash(target), "/") {
		switch el {
		case "..":
			if named {
				return true
			}
		case "", ".":
		default:
			named = true
		}
	}
	return false
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
