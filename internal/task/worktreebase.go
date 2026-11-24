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
	fetchWaitDelay     = 5 * time.Second
	fetchTimeoutMemory = 2 * time.Minute
)

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
	b := strings.TrimSpace(base)
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
	if gitCmd(projectRoot, "remote", "get-url", "origin").Run() != nil {
		fmt.Fprintf(warn, "spore: no origin remote; forking from local %s\n", b)
		return
	}
	key := gitCommonDir(projectRoot)
	if until, ok := fetchMemo(key); ok {
		fmt.Fprintf(warn, "spore: origin fetch timed out recently (retry after %s); forking from last-known %s\n", until.Format(time.TimeOnly), b)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	unlock, err := lockFetch(ctx, key)
	if err != nil {
		fmt.Fprintf(warn, "spore: fetch origin %s: %v; forking from last-known %s\n", b, err, b)
		return
	}
	defer unlock()

	out, err := fetchCmd(ctx, projectRoot, b).CombinedOutput()
	switch fetchOutcome(err, ctx.Err()) {
	case fetchOK:
	case fetchDeadline:
		rememberFetchTimeout(key)
		fmt.Fprintf(warn, "spore: fetch origin %s timed out after %s; forking from last-known %s\n", b, fetchTimeout, b)
	default:
		fmt.Fprintf(warn, "spore: fetch origin %s failed: %v: %s; forking from last-known %s\n", b, err, lastLine(out), b)
	}
}

// fetchCmd fetches exactly the ref pickStart reads, whatever the
// configured refspec. git stays in spore's process group so Ctrl-C
// reaches it and an operator-configured ssh can still prompt.
func fetchCmd(ctx context.Context, projectRoot, b string) *exec.Cmd {
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		abs = projectRoot
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "safe.directory="+abs, "-C", projectRoot,
		"fetch", "--quiet", "--no-tags", "origin", "+refs/heads/"+b+":refs/remotes/origin/"+b)
	if !sshConfigured(projectRoot) {
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	// An ssh grandchild that outlives the killed git would otherwise
	// hold the output pipe open past the deadline.
	cmd.WaitDelay = fetchWaitDelay
	return cmd
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
