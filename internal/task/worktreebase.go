package task

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
// <projectRoot>/spore.toml. Missing file or key yields "fresh"; an
// unreadable file or an unknown value also yields "fresh" but warns on
// stderr so a mistyped opt-out does not go unnoticed.
func LoadWorktreeBase(projectRoot string) string {
	entries, err := sporetoml.ReadSection(projectRoot, "fleet")
	if err != nil {
		fmt.Fprintf(os.Stderr, "spore: worktree base: %v, using %q\n", err, WorktreeBaseFresh)
		return WorktreeBaseFresh
	}
	for _, e := range entries {
		if e.Key != "worktree_base" {
			continue
		}
		switch v := strings.TrimSpace(e.Value); v {
		case WorktreeBaseHead:
			return WorktreeBaseHead
		case WorktreeBaseFresh:
		default:
			fmt.Fprintf(os.Stderr, "spore: worktree base: unknown [fleet] worktree_base %q (want %q or %q), using %q\n",
				v, WorktreeBaseFresh, WorktreeBaseHead, WorktreeBaseFresh)
		}
	}
	return WorktreeBaseFresh
}

// addTaskWorktree runs `git worktree add` for branch, reusing an
// existing branch as is. A new branch starts from startPoint. Matching
// .worktreeinclude files are copied in afterwards. base is the task's
// `base:` frontmatter, "" for the project default.
func addTaskWorktree(projectRoot, worktree, branch, base string) error {
	args := []string{"worktree", "add"}
	if branchExists(projectRoot, branch) {
		args = append(args, worktree, branch)
	} else {
		args = append(args, "--no-track", "-b", branch, worktree)
		if start := startPoint(projectRoot, base); start != "" {
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

// fetchTimeout bounds the base fetch; a var so tests can shorten it.
var fetchTimeout = 30 * time.Second

// fetchMemoTTL lets spawns in one reconcile pass share a fetch result
// instead of racing on the remote-tracking ref lock or each waiting out
// their own timeout against a dead remote.
const fetchMemoTTL = 30 * time.Second

// fetchRetryDelay spaces the single retry after a ref-lock failure.
var fetchRetryDelay = 500 * time.Millisecond

type fetchMemo struct {
	at  time.Time
	err error
}

// fetchProject serializes fetches within one repo (they share
// FETCH_HEAD, packed-refs and gc locks) while letting different
// projects fetch independently.
type fetchProject struct {
	mu    sync.Mutex
	memos map[string]fetchMemo
}

var (
	fetchProjectsMu sync.Mutex
	fetchProjects   = map[string]*fetchProject{}
)

func lockFetchProject(projectRoot string) *fetchProject {
	fetchProjectsMu.Lock()
	p := fetchProjects[projectRoot]
	if p == nil {
		p = &fetchProject{memos: map[string]fetchMemo{}}
		fetchProjects[projectRoot] = p
	}
	fetchProjectsMu.Unlock()
	p.mu.Lock()
	return p
}

// startPoint picks the fully qualified ref a new task branch forks
// from. It returns refs/remotes/origin/<b> after fetching it, where <b>
// is base or origin's default branch. A local <b> strictly ahead of
// origin (unpushed drift, operator or review-fix commits) wins and
// refs/heads/<b> is returned. When local and origin have diverged, the
// default branch keeps local (a later ff-only merge targets it) while
// any other base follows origin (a stacked parent may have been rebased
// and force-pushed). With the "head" opt-out, no origin remote, or a
// failed fetch it returns refs/heads/<base> when base exists locally,
// else "" (HEAD). Never errors: a spawn must not fail on the fetch.
func startPoint(projectRoot, base string) string {
	fallback := ""
	if base != "" && branchExists(projectRoot, base) {
		fallback = "refs/heads/" + base
	}
	fallbackName := "HEAD"
	if fallback != "" {
		fallbackName = base
	}
	if LoadWorktreeBase(projectRoot) == WorktreeBaseHead {
		return fallback
	}
	if gitCmd(projectRoot, "remote", "get-url", "origin").Run() != nil {
		fmt.Fprintf(os.Stderr, "spore: worktree base: no origin remote, branching from %s\n", fallbackName)
		return fallback
	}
	defaultBranch := remoteDefaultBranch(projectRoot)
	def := base
	if def == "" {
		def = defaultBranch
	}
	if cached, err := fetchBase(projectRoot, def); err != nil {
		if !cached {
			fmt.Fprintf(os.Stderr, "spore: worktree base: git fetch origin %s failed, branching from %s: %v\n", def, fallbackName, err)
		}
		return fallback
	}
	local, remote := "refs/heads/"+def, "refs/remotes/origin/"+def
	if !branchExists(projectRoot, def) {
		return remote
	}
	if isAncestor(projectRoot, remote, local) {
		return local
	}
	if def == defaultBranch && !isAncestor(projectRoot, local, remote) {
		return local
	}
	return remote
}

func isAncestor(projectRoot, a, b string) bool {
	return gitCmd(projectRoot, "merge-base", "--is-ancestor", a, b).Run() == nil
}

// fetchBase updates refs/remotes/origin/<def> with an explicit refspec,
// so a narrow remote.origin.fetch cannot leave a stale ref behind. The
// result is reused for fetchMemoTTL; cached reports a reused failure so
// the caller warns once per window. A ref-lock failure (a concurrent
// manual fetch) is retried once and never memoized, since it is local
// and transient.
func fetchBase(projectRoot, def string) (cached bool, err error) {
	p := lockFetchProject(projectRoot)
	defer p.mu.Unlock()
	if m, ok := p.memos[def]; ok && time.Since(m.at) < fetchMemoTTL {
		return true, m.err
	}
	err = runFetch(projectRoot, def)
	if isLockError(err) {
		time.Sleep(fetchRetryDelay)
		err = runFetch(projectRoot, def)
	}
	if isLockError(err) {
		delete(p.memos, def)
	} else {
		p.memos[def] = fetchMemo{at: time.Now(), err: err}
	}
	return false, err
}

func isLockError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "cannot lock ref") || strings.Contains(msg, ".lock")
}

// runFetch runs the fetch under a deadline. It stays in spore's process
// group so Ctrl-C and an operator ssh's /dev/tty prompt behave as in a
// plain shell; on timeout git is killed and WaitDelay closes its pipes,
// and the injected batch-mode ssh carries its own connect and keepalive
// timeouts so it cannot outlive the fetch for long. --no-write-fetch-head
// and --no-auto-gc keep it off locks a concurrent git process may hold.
func runFetch(projectRoot, def string) error {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	refspec := "+refs/heads/" + def + ":refs/remotes/origin/" + def
	cmd := gitCmd(projectRoot, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-auto-gc", "origin", refspec)
	fetch := exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	fetch.WaitDelay = time.Second
	fetch.Env = fetchEnv(projectRoot)
	out, err := fetch.CombinedOutput()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("timed out after %s", fetchTimeout)
	}
	return errors.New(firstLine(strings.TrimSpace(string(out)), err))
}

// batchSSH is the ssh command injected when the operator has none:
// no prompts, and bounded connect and dead-peer detection.
const batchSSH = "ssh -o BatchMode=yes -o ConnectTimeout=15 -o ServerAliveInterval=5 -o ServerAliveCountMax=3"

// fetchEnv disables credential prompts and puts ssh in batch mode,
// unless the operator routes ssh through GIT_SSH_COMMAND, GIT_SSH or
// core.sshCommand, which an injected GIT_SSH_COMMAND would override.
func fetchEnv(projectRoot string) []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if os.Getenv("GIT_SSH_COMMAND") == "" && os.Getenv("GIT_SSH") == "" &&
		gitCmd(projectRoot, "config", "--get", "core.sshCommand").Run() != nil {
		env = append(env, "GIT_SSH_COMMAND="+batchSSH)
	}
	return env
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
	// Inside a collapsed ignored directory only anchored patterns
	// select; unanchored ones still negate.
	matchAny := func(rel string, isDir, anchoredOnly bool) bool {
		hit := false
		for _, p := range pats {
			if anchoredOnly && !p.anchored && !p.negate {
				continue
			}
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
		if matchAny(rel, isDir, false) {
			note(copyPath(projectRoot, worktree, rel))
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
			if matchAny(sub, d.IsDir(), true) {
				note(copyPath(projectRoot, worktree, sub))
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

// copyPath copies the file, symlink, or directory tree at rel from
// projectRoot to worktree, keeping modes and skipping anything already
// present. An absolute symlink into projectRoot is retargeted at the
// same path under worktree, so a worker writing through it cannot
// mutate the main checkout.
func copyPath(projectRoot, worktree, rel string) error {
	src := filepath.Join(projectRoot, rel)
	dst := filepath.Join(worktree, rel)
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		sub, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, sub)
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
			return os.Symlink(retargetLink(projectRoot, worktree, path, link), target)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

// retargetLink rewrites a symlink copied from srcPath. An absolute
// target inside the main checkout (matched on both the given and the
// symlink-resolved root) moves to the same path under worktree. A
// relative target that escapes the project root is made absolute,
// since the worktree sits deeper and the relative climb would land
// elsewhere. Everything else is kept verbatim.
func retargetLink(projectRoot, worktree, srcPath, link string) string {
	roots := []string{}
	if abs, err := filepath.Abs(projectRoot); err == nil {
		roots = append(roots, abs)
		if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
			roots = append(roots, real)
		}
	}
	under := func(target string) (string, bool) {
		for _, r := range roots {
			rel, err := filepath.Rel(r, target)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
				return rel, true
			}
		}
		return "", false
	}
	if !filepath.IsAbs(link) {
		dir := filepath.Dir(srcPath)
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			dir = real
		}
		target := filepath.Clean(filepath.Join(dir, link))
		if abs, err := filepath.Abs(target); err == nil {
			if _, ok := under(abs); !ok {
				return abs
			}
		}
		return link
	}
	rel, ok := under(filepath.Clean(link))
	if !ok {
		return link
	}
	wt, err := filepath.Abs(worktree)
	if err != nil {
		return link
	}
	return filepath.Join(wt, rel)
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

// taskBase returns the task's `base:` frontmatter (its stack parent),
// "" when unset or unreadable.
func taskBase(tasksDir, slug string) string {
	m, err := readTaskMeta(tasksDir, slug)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(m.Extra["base"])
}
