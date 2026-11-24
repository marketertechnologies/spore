package task

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/versality/spore/internal/sporetoml"
)

// `[fleet] worktree_base` values. Fresh forks a new wt/<slug> branch
// from the freshly fetched base branch; head keeps the pre-0.34.2
// behaviour of forking from the main checkout's HEAD.
const (
	WorktreeBaseFresh = "fresh"
	WorktreeBaseHead  = "head"
)

// Package vars so tests can shorten them.
var (
	fetchTimeout       = 30 * time.Second
	fetchTimeoutMemory = 2 * time.Minute
	fetchAuthTimeout   = 10 * time.Second
)

// FetchAuth returns base extended so a git fetch from originURL
// authenticates, or ErrNoFetchAuth when the project has nothing to
// authenticate with for that URL's host. It lives behind a hook because the provider
// (internal/ghapp) imports this package.
type FetchAuth func(ctx context.Context, projectRoot, originURL string, base []string) ([]string, error)

// ErrNoFetchAuth tells fetchBase to fetch plainly without a warning.
var ErrNoFetchAuth = errors.New("no fetch credentials configured")

var fetchAuth FetchAuth

// SetFetchAuth installs the base-fetch credential provider. Call it
// once at startup, before any worktree is created.
func SetFetchAuth(f FetchAuth) { fetchAuth = f }

// runFetchCmd is runFetch; tests swap it to inspect the command.
var runFetchCmd = runFetch

// fetchTimedOut remembers, per git common dir, until when origin is
// treated as unreachable after a fetch hit its deadline. Only a
// deadline is remembered: a fast failure costs nothing to retry, and
// it may be transient (ref-lock contention), so it must not push
// later spawns to the fallback.
var (
	fetchMemoMu   sync.Mutex
	fetchTimedOut = map[string]time.Time{}
)

// LoadWorktreeBase reads `[fleet] worktree_base` from spore.toml. A
// parse error or an unknown value warns and yields the fresh default.
func LoadWorktreeBase(projectRoot string, warn io.Writer) string {
	entries, err := sporetoml.ReadSection(projectRoot, "fleet")
	if err != nil {
		fmt.Fprintf(warn, "spore: %v; [fleet] worktree_base falls back to %q\n", err, WorktreeBaseFresh)
		return WorktreeBaseFresh
	}
	mode := WorktreeBaseFresh
	for _, e := range entries {
		if e.Key != "worktree_base" {
			continue
		}
		switch v := strings.TrimSpace(e.Value); v {
		case WorktreeBaseFresh, WorktreeBaseHead:
			mode = v
		default:
			fmt.Fprintf(warn, "spore: spore.toml line %d: unknown [fleet] worktree_base %q (want %q or %q); using %q\n",
				e.Line, e.Value, WorktreeBaseFresh, WorktreeBaseHead, WorktreeBaseFresh)
			mode = WorktreeBaseFresh
		}
	}
	return mode
}

// forkPoint returns the fully qualified start point for a new
// wt/<slug> branch, or "" to fork from HEAD. base is the task's
// `base:` frontmatter ("" for the default branch).
func forkPoint(projectRoot, base string, warn io.Writer) string {
	if LoadWorktreeBase(projectRoot, warn) == WorktreeBaseHead {
		return ""
	}
	b := normalizeBase(base)
	if b == "" {
		b = defaultBaseBranch(projectRoot)
	}
	if b == "" {
		fmt.Fprintf(warn, "spore: no base branch (origin/HEAD, main, master all missing); forking from HEAD\n")
		return ""
	}
	if !validBranchName(projectRoot, b) {
		fmt.Fprintf(warn, "spore: base %q is not a valid branch name; forking from HEAD\n", b)
		return ""
	}
	fetchBase(projectRoot, b, warn)
	return pickStart(projectRoot, b, warn)
}

// normalizeBase turns the common ways of writing a branch in
// `base:` (origin/main, refs/heads/main, refs/remotes/origin/main)
// into the bare branch name the fork and merge logic compare.
func normalizeBase(base string) string {
	b := strings.TrimSpace(base)
	for _, prefix := range []string{"refs/heads/", "refs/remotes/origin/", "origin/"} {
		if rest, ok := strings.CutPrefix(b, prefix); ok {
			return rest
		}
	}
	return b
}

// defaultBaseBranch is origin/HEAD's branch, else main, else master
// (each as a local or remote-tracking branch), else "".
func defaultBaseBranch(projectRoot string) string {
	out, err := gitCmd(projectRoot, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD").Output()
	if err == nil {
		ref := strings.TrimSpace(string(out))
		if name := strings.TrimPrefix(ref, "refs/remotes/origin/"); name != ref && name != "" {
			return name
		}
	}
	for _, name := range []string{"main", "master"} {
		if refExists(projectRoot, "refs/heads/"+name) || refExists(projectRoot, "refs/remotes/origin/"+name) {
			return name
		}
	}
	return ""
}

func validBranchName(projectRoot, b string) bool {
	if strings.HasPrefix(b, "-") {
		return false
	}
	return gitCmd(projectRoot, "check-ref-format", "refs/heads/"+b).Run() == nil
}

func refExists(projectRoot, ref string) bool {
	return gitCmd(projectRoot, "show-ref", "--verify", "--quiet", ref).Run() == nil
}

func isAncestor(projectRoot, ancestor, descendant string) bool {
	return gitCmd(projectRoot, "merge-base", "--is-ancestor", ancestor, descendant).Run() == nil
}

// pickStart chooses between the local and remote-tracking refs of b.
// Local wins when it is equal to or strictly ahead of the remote, and
// on divergence: forking from local never drops unpushed commits,
// while the remote's extra commits stay one merge away.
func pickStart(projectRoot, b string, warn io.Writer) string {
	local := "refs/heads/" + b
	remote := "refs/remotes/origin/" + b
	hasLocal := refExists(projectRoot, local)
	hasRemote := refExists(projectRoot, remote)
	switch {
	case hasLocal && hasRemote:
		if isAncestor(projectRoot, remote, local) {
			return local
		}
		if isAncestor(projectRoot, local, remote) {
			return remote
		}
		fmt.Fprintf(warn, "spore: %s and %s have diverged; forking from local %s to keep its unpushed commits\n", b, "origin/"+b, b)
		return local
	case hasRemote:
		return remote
	case hasLocal:
		return local
	}
	fmt.Fprintf(warn, "spore: base branch %q exists neither locally nor on origin; forking from HEAD\n", b)
	return ""
}

// fetchBase updates refs/remotes/origin/<b> from origin under a
// deadline. Every failure warns once and returns; pickStart then
// falls back to the refs already present.
func fetchBase(projectRoot, b string, warn io.Writer) {
	urlOut, err := gitCmd(projectRoot, "config", "--get", "remote.origin.url").Output()
	originURL := strings.TrimSpace(string(urlOut))
	if err != nil || originURL == "" {
		fmt.Fprintf(warn, "spore: no origin remote; forking from last-known %s\n", b)
		return
	}
	key := gitCommonDir(projectRoot)
	if until, ok := fetchMemo(key); ok {
		fmt.Fprintf(warn, "spore: origin fetch timed out recently (retry after %s); forking from last-known %s\n", until.Format(time.TimeOnly), b)
		return
	}
	env, authErr := fetchAuthEnv(projectRoot, originURL)
	authNote := ""
	if authErr != nil {
		authNote = fmt.Sprintf(" (GitHub App token unavailable: %v; fetched without it)", authErr)
	}

	lockCtx, cancelLock := context.WithTimeout(context.Background(), fetchTimeout)
	unlock, err := lockFetch(lockCtx, key)
	cancelLock()
	if err != nil {
		fmt.Fprintf(warn, "spore: fetch origin %s: %v%s; forking from last-known %s\n", b, err, authNote, b)
		return
	}
	defer unlock()

	// The fetch gets its own deadline: time spent waiting on another
	// process's lock must not turn a reachable origin into a
	// remembered timeout.
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	out, err := runFetchCmd(fetchCmd(ctx, projectRoot, b, env))
	if env != nil && fetchOutcome(err, ctx.Err()) == fetchFailed {
		// The app may not cover this repo, and the token env cleared
		// the operator's own credential helper: retry plainly, under
		// its own deadline so a slow token failure cannot turn a
		// reachable origin into a remembered timeout.
		authFail := lastLine(out)
		authNote = fmt.Sprintf(" (with the GitHub App token it failed too: %s)", authFail)
		retryCtx, cancelRetry := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancelRetry()
		ctx = retryCtx
		out, err = runFetchCmd(fetchCmd(ctx, projectRoot, b, nil))
		if err == nil {
			fmt.Fprintf(warn, "spore: fetch origin %s failed with the GitHub App token (%s); a plain fetch worked\n", b, authFail)
			return
		}
	}
	switch fetchOutcome(err, ctx.Err()) {
	case fetchOK:
		if authErr != nil {
			fmt.Fprintf(warn, "spore: fetch origin %s: GitHub App token unavailable: %v; fetched without it\n", b, authErr)
		}
	case fetchDeadline:
		rememberFetchTimeout(key)
		fmt.Fprintf(warn, "spore: fetch origin %s timed out after %s%s; forking from last-known %s\n", b, fetchTimeout, authNote, b)
	default:
		fmt.Fprintf(warn, "spore: fetch origin %s failed: %v: %s%s; forking from last-known %s\n", b, err, lastLine(out), authNote, b)
	}
}

// fetchAuthEnv returns the authenticated fetch env, or nil for a
// plain fetch: no provider, a non-https origin (the token cannot
// help an ssh fetch), or no credentials configured. originURL is
// read raw from config, as `spore gh exec` does. A provider error
// (a failed mint) is returned for the caller's single warning.
func fetchAuthEnv(projectRoot, originURL string) ([]string, error) {
	if fetchAuth == nil || !strings.HasPrefix(originURL, "https://") {
		return nil, nil
	}
	base := plainFetchEnv(projectRoot)
	if base == nil {
		base = os.Environ()
	}
	ctx, cancel := context.WithTimeout(context.Background(), fetchAuthTimeout)
	defer cancel()
	env, err := fetchAuth(ctx, projectRoot, originURL, base)
	switch {
	case errors.Is(err, ErrNoFetchAuth):
		return nil, nil
	case err != nil:
		return nil, err
	}
	return env, nil
}

// fetchCmd fetches exactly the ref pickStart reads, whatever the
// configured refspec. git stays in spore's process group so Ctrl-C
// reaches it and an operator-configured ssh can still prompt. env
// nil means plainFetchEnv.
func fetchCmd(ctx context.Context, projectRoot, b string, env []string) *exec.Cmd {
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		abs = projectRoot
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "safe.directory="+abs, "-C", projectRoot,
		"fetch", "--quiet", "--no-tags", "origin", "+refs/heads/"+b+":refs/remotes/origin/"+b)
	if env == nil {
		env = plainFetchEnv(projectRoot)
	}
	cmd.Env = env
	return cmd
}

// plainFetchEnv makes the fetch non-interactive unless the operator
// set their own ssh command; nil inherits spore's environment.
func plainFetchEnv(projectRoot string) []string {
	if sshConfigured(projectRoot) {
		return nil
	}
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
}

// runFetch sends the command's output to a temp file, not a pipe: an
// ssh grandchild (a ControlPersist master, or one outliving a killed
// git) inherits the descriptor, and with a pipe Wait would block on
// it after git itself has exited.
func runFetch(cmd *exec.Cmd) ([]byte, error) {
	f, err := os.CreateTemp("", "spore-fetch-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	cmd.Stdout, cmd.Stderr = f, f
	runErr := cmd.Run()
	out, _ := os.ReadFile(f.Name())
	return out, runErr
}

type fetchResult int

const (
	fetchOK fetchResult = iota
	fetchDeadline
	fetchFailed
)

// fetchOutcome checks the command's error first: a fetch that exits 0
// just as the deadline passes still succeeded.
func fetchOutcome(cmdErr, ctxErr error) fetchResult {
	switch {
	case cmdErr == nil:
		return fetchOK
	case errors.Is(ctxErr, context.DeadlineExceeded):
		return fetchDeadline
	}
	return fetchFailed
}

// sshConfigured reports whether the operator set their own ssh
// command, in which case spawn leaves prompting alone.
func sshConfigured(projectRoot string) bool {
	if os.Getenv("GIT_SSH_COMMAND") != "" || os.Getenv("GIT_SSH") != "" {
		return true
	}
	out, err := gitCmd(projectRoot, "config", "--get", "core.sshCommand").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func gitCommonDir(projectRoot string) string {
	out, err := gitCmd(projectRoot, "rev-parse", "--git-common-dir").Output()
	dir := strings.TrimSpace(string(out))
	if err != nil || dir == "" {
		return filepath.Join(projectRoot, ".git")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(projectRoot, dir)
	}
	return filepath.Clean(dir)
}

func fetchMemo(key string) (time.Time, bool) {
	fetchMemoMu.Lock()
	defer fetchMemoMu.Unlock()
	until, ok := fetchTimedOut[key]
	if ok && time.Now().Before(until) {
		return until, true
	}
	delete(fetchTimedOut, key)
	return time.Time{}, false
}

func rememberFetchTimeout(key string) {
	fetchMemoMu.Lock()
	defer fetchMemoMu.Unlock()
	fetchTimedOut[key] = time.Now().Add(fetchTimeoutMemory)
}

// lockFetch takes an exclusive flock on <common-dir>/spore-fetch.lock,
// polling so the wait honours ctx. Two fetches into one repository
// race on FETCH_HEAD, ref locks and auto-gc; the flock serializes
// them across processes as well as goroutines.
func lockFetch(ctx context.Context, commonDir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(commonDir, "spore-fetch.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("waiting for another fetch: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
