package task

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// baseRepo is a project checkout with an origin bare repo and a second
// clone ("pusher") that moves origin behind the project's back.
type baseRepo struct {
	t      *testing.T
	root   string
	origin string
	pusher string
}

func newBaseRepo(t *testing.T) *baseRepo {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitRepo(t, root)
	origin := filepath.Join(dir, "origin.git")
	runGit(t, dir, "init", "--bare", "-q", "-b", "main", origin)
	runGit(t, root, "remote", "add", "origin", origin)
	runGit(t, root, "push", "-q", "origin", "main")
	runGit(t, root, "remote", "set-head", "origin", "main")
	pusher := filepath.Join(dir, "pusher")
	runGit(t, dir, "clone", "-q", origin, pusher)
	runGit(t, pusher, "config", "user.email", "test@example.com")
	runGit(t, pusher, "config", "user.name", "Test")
	return &baseRepo{t: t, root: root, origin: origin, pusher: pusher}
}

// push commits on branch in the pusher clone and pushes it to origin.
func (r *baseRepo) push(branch, msg string) string {
	r.t.Helper()
	runGit(r.t, r.pusher, "fetch", "-q", "origin")
	if gitCmd(r.pusher, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+branch).Run() == nil {
		runGit(r.t, r.pusher, "checkout", "-q", "-B", branch, "origin/"+branch)
	} else {
		runGit(r.t, r.pusher, "checkout", "-q", "-B", branch)
	}
	runGit(r.t, r.pusher, "commit", "-q", "--allow-empty", "-m", msg)
	runGit(r.t, r.pusher, "push", "-q", "origin", branch)
	return gitOutput(r.t, r.pusher, "rev-parse", "HEAD")
}

// commit commits on the project's current branch.
func (r *baseRepo) commit(msg string) string {
	r.t.Helper()
	runGit(r.t, r.root, "commit", "-q", "--allow-empty", "-m", msg)
	return gitOutput(r.t, r.root, "rev-parse", "HEAD")
}

// onFeature moves the project's HEAD to a side branch with its own
// commit, so a fork from HEAD is distinguishable from any base.
func (r *baseRepo) onFeature() string {
	r.t.Helper()
	runGit(r.t, r.root, "checkout", "-q", "-b", "feature")
	return r.commit("feature")
}

func (r *baseRepo) rev(ref string) string {
	r.t.Helper()
	return gitOutput(r.t, r.root, "rev-parse", ref)
}

// add runs addWorktree for slug and returns the new branch's commit
// and the warnings.
func (r *baseRepo) add(slug, base string) (string, string) {
	r.t.Helper()
	var warn bytes.Buffer
	if err := addWorktree(filepath.Join(r.root, "tasks"), r.root, slug, base, &warn); err != nil {
		r.t.Fatalf("addWorktree: %v\n%s", err, warn.String())
	}
	return r.rev("refs/heads/wt/" + slug), warn.String()
}

func TestAddWorktreeForksFromFetchedBase(t *testing.T) {
	r := newBaseRepo(t)
	r.onFeature()
	want := r.push("main", "remote ahead")
	got, warn := r.add("x", "")
	if got != want {
		t.Errorf("fork = %s, want origin/main %s (warn %q)", got, want, warn)
	}
	if warn != "" {
		t.Errorf("unexpected warning: %q", warn)
	}
	if out, err := gitCmd(r.root, "config", "--get", "branch.wt/x.remote").Output(); err == nil {
		t.Errorf("wt/x tracks %q, want --no-track", strings.TrimSpace(string(out)))
	}
}

func TestAddWorktreeStackedBase(t *testing.T) {
	r := newBaseRepo(t)
	want := r.push("feat", "stacked parent")
	got, warn := r.add("x", "feat")
	if got != want {
		t.Errorf("fork = %s, want origin/feat %s (warn %q)", got, want, warn)
	}
}

func TestAddWorktreeNonMainOriginHEAD(t *testing.T) {
	r := newBaseRepo(t)
	r.push("develop", "develop 1")
	runGit(t, r.root, "fetch", "-q", "origin")
	runGit(t, r.root, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/develop")
	want := r.push("develop", "develop 2")
	got, warn := r.add("x", "")
	if got != want {
		t.Errorf("fork = %s, want origin/develop %s (warn %q)", got, want, warn)
	}
}

func TestAddWorktreeLocalStrictlyAhead(t *testing.T) {
	for _, base := range []string{"", "main"} {
		t.Run("base="+base, func(t *testing.T) {
			r := newBaseRepo(t)
			want := r.commit("unpushed")
			r.onFeature()
			got, warn := r.add("x", base)
			if got != want {
				t.Errorf("fork = %s, want local main %s (warn %q)", got, want, warn)
			}
		})
	}
}

func TestAddWorktreeDivergedForksFromLocal(t *testing.T) {
	r := newBaseRepo(t)
	r.push("main", "remote side")
	want := r.commit("local side")
	got, warn := r.add("x", "")
	if got != want {
		t.Errorf("fork = %s, want local main %s", got, want)
	}
	if !strings.Contains(warn, "diverged") {
		t.Errorf("warn = %q, want a divergence warning", warn)
	}
}

func TestAddWorktreeOfflineUsesLastKnownBase(t *testing.T) {
	r := newBaseRepo(t)
	lastKnown := r.push("feat", "feat 1")
	runGit(t, r.root, "fetch", "-q", "origin")
	r.push("feat", "feat 2")
	r.onFeature()
	runGit(t, r.root, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))

	got, warn := r.add("x", "feat")
	if got != lastKnown {
		t.Errorf("fork = %s, want last-known origin/feat %s", got, lastKnown)
	}
	if strings.Count(warn, "\n") != 1 || !strings.Contains(warn, "fetch origin feat failed") {
		t.Errorf("warn = %q, want one fetch-failed line", warn)
	}

	mainSHA := r.rev("refs/heads/main")
	got, _ = r.add("y", "")
	if got != mainSHA {
		t.Errorf("default base offline: fork = %s, want main %s, not HEAD", got, mainSHA)
	}
}

func TestAddWorktreeNoRemote(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitRepo(t, root)
	r := &baseRepo{t: t, root: root}
	want := r.rev("refs/heads/main")
	r.onFeature()
	got, warn := r.add("x", "")
	if got != want {
		t.Errorf("fork = %s, want local main %s", got, want)
	}
	if strings.Count(warn, "\n") != 1 || !strings.Contains(warn, "no origin remote") {
		t.Errorf("warn = %q, want one no-remote line", warn)
	}
}

func TestAddWorktreeOptOutForksFromHEAD(t *testing.T) {
	r := newBaseRepo(t)
	writeSporeToml(t, r.root, "[fleet]\nworktree_base = \"head\"\n")
	head := r.onFeature()
	stale := r.rev("refs/remotes/origin/main")
	r.push("main", "remote ahead")
	got, warn := r.add("x", "")
	if got != head {
		t.Errorf("fork = %s, want HEAD %s", got, head)
	}
	if warn != "" {
		t.Errorf("unexpected warning: %q", warn)
	}
	if now := r.rev("refs/remotes/origin/main"); now != stale {
		t.Errorf("opt-out fetched origin/main (%s -> %s)", stale, now)
	}
}

func TestLoadWorktreeBaseRejectsBadConfig(t *testing.T) {
	cases := map[string]string{
		"misspelled":  "[fleet]\nworktree_base = \"haed\"\n",
		"empty":       "[fleet]\nworktree_base = \"\"\n",
		"parse error": "[fleet]\nworktree_base\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			r := newBaseRepo(t)
			writeSporeToml(t, r.root, body)
			r.onFeature()
			want := r.push("main", "remote ahead")
			got, warn := r.add("x", "")
			if got != want {
				t.Errorf("fork = %s, want fresh origin/main %s", got, want)
			}
			if !strings.Contains(warn, "worktree_base") {
				t.Errorf("warn = %q, want a worktree_base warning", warn)
			}
		})
	}
}

func TestAddWorktreeReusesExistingBranch(t *testing.T) {
	r := newBaseRepo(t)
	head := r.onFeature()
	runGit(t, r.root, "branch", "wt/x", head)
	stale := r.rev("refs/remotes/origin/main")
	r.push("main", "remote ahead")
	got, warn := r.add("x", "")
	if got != head {
		t.Errorf("wt/x = %s, want unchanged %s", got, head)
	}
	if warn != "" {
		t.Errorf("unexpected warning: %q", warn)
	}
	if now := r.rev("refs/remotes/origin/main"); now != stale {
		t.Errorf("reuse fetched origin/main (%s -> %s)", stale, now)
	}
}

func TestAddWorktreeUsesQualifiedRefs(t *testing.T) {
	r := newBaseRepo(t)
	head := r.onFeature()
	runGit(t, r.root, "tag", "main", head)
	runGit(t, r.root, "tag", "origin/main", head)
	want := r.push("main", "remote ahead")
	got, warn := r.add("x", "")
	if got != want {
		t.Errorf("fork = %s, want origin/main %s, not the tag (warn %q)", got, want, warn)
	}
}

func TestAddWorktreeFetchesDespiteNarrowRefspec(t *testing.T) {
	r := newBaseRepo(t)
	runGit(t, r.root, "config", "remote.origin.fetch", "+refs/heads/other:refs/remotes/origin/other")
	want := r.push("main", "remote ahead")
	got, warn := r.add("x", "")
	if got != want {
		t.Errorf("fork = %s, want origin/main %s (warn %q)", got, want, warn)
	}
}

func shortFetchTimeout(t *testing.T) {
	t.Helper()
	old := fetchTimeout
	fetchTimeout = 300 * time.Millisecond
	t.Cleanup(func() { fetchTimeout = old })
}

func TestFetchTimeoutIsRememberedPerRepo(t *testing.T) {
	shortFetchTimeout(t)
	r := newBaseRepo(t)
	hang := filepath.Join(t.TempDir(), "hang-ssh")
	if err := os.WriteFile(hang, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", hang)
	runGit(t, r.root, "remote", "set-url", "origin", "ssh://example.invalid/repo.git")
	want := r.rev("refs/heads/main")
	r.onFeature()

	start := time.Now()
	got, warn := r.add("a", "")
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("first spawn took %s, want about the fetch timeout", d)
	}
	if got != want || !strings.Contains(warn, "timed out after") {
		t.Errorf("first spawn: fork %s (want %s), warn %q", got, want, warn)
	}

	start = time.Now()
	got, warn = r.add("b", "")
	if d := time.Since(start); d > fetchTimeout {
		t.Errorf("second spawn took %s, want no second stall", d)
	}
	if got != want || !strings.Contains(warn, "timed out recently") {
		t.Errorf("second spawn: fork %s (want %s), warn %q", got, want, warn)
	}
}

func TestFastFetchFailureIsNotRemembered(t *testing.T) {
	r := newBaseRepo(t)
	runGit(t, r.root, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	if _, warn := r.add("a", ""); !strings.Contains(warn, "failed") {
		t.Fatalf("warn = %q, want a fetch failure", warn)
	}
	runGit(t, r.root, "remote", "set-url", "origin", r.origin)
	want := r.push("main", "remote ahead")
	if got, warn := r.add("b", ""); got != want {
		t.Errorf("fork after recovery = %s, want origin/main %s (warn %q)", got, want, warn)
	}
}

func TestFetchSerializedByLock(t *testing.T) {
	shortFetchTimeout(t)
	r := newBaseRepo(t)
	f, err := os.OpenFile(filepath.Join(gitCommonDir(r.root), "spore-fetch.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		t.Fatal(err)
	}
	stale := r.rev("refs/remotes/origin/main")
	r.push("main", "remote ahead")
	got, warn := r.add("a", "")
	if got != stale || !strings.Contains(warn, "waiting for another fetch") {
		t.Errorf("fork %s (want last-known %s), warn %q", got, stale, warn)
	}
	if _, ok := fetchMemo(gitCommonDir(r.root)); ok {
		t.Error("a lock wait was remembered as an unreachable origin")
	}
}

func TestFetchOutcomeChecksCommandFirst(t *testing.T) {
	if got := fetchOutcome(nil, context.DeadlineExceeded); got != fetchOK {
		t.Errorf("success at the deadline = %v, want fetchOK", got)
	}
	if got := fetchOutcome(errors.New("killed"), context.DeadlineExceeded); got != fetchDeadline {
		t.Errorf("killed at the deadline = %v, want fetchDeadline", got)
	}
	if got := fetchOutcome(errors.New("exit 128"), nil); got != fetchFailed {
		t.Errorf("fast failure = %v, want fetchFailed", got)
	}
}

func TestFetchCmd(t *testing.T) {
	r := newBaseRepo(t)
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_SSH", "")
	cmd := fetchCmd(context.Background(), r.root, "feat", nil)
	if !strings.HasSuffix(strings.Join(cmd.Args, " "), "origin +refs/heads/feat:refs/remotes/origin/feat") {
		t.Errorf("args = %q, want an explicit refspec", cmd.Args)
	}
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid {
		t.Error("fetch runs in its own process group")
	}
	env := strings.Join(cmd.Env, "\n")
	if !strings.Contains(env, "GIT_TERMINAL_PROMPT=0") || !strings.Contains(env, "BatchMode=yes") {
		t.Errorf("unconfigured ssh: env lacks the non-interactive settings")
	}

	configured := map[string]func(){
		"GIT_SSH_COMMAND": func() { t.Setenv("GIT_SSH_COMMAND", "ssh -i key") },
		"GIT_SSH":         func() { t.Setenv("GIT_SSH", "/usr/bin/ssh") },
		"core.sshCommand": func() { runGit(t, r.root, "config", "core.sshCommand", "ssh -i key") },
	}
	for name, set := range configured {
		t.Run(name, func(t *testing.T) {
			t.Setenv("GIT_SSH_COMMAND", "")
			t.Setenv("GIT_SSH", "")
			_ = exec.Command("git", "-C", r.root, "config", "--unset", "core.sshCommand").Run()
			set()
			if cmd := fetchCmd(context.Background(), r.root, "feat", nil); cmd.Env != nil {
				t.Errorf("operator ssh overridden: env set")
			}
		})
	}
}

// TestEnsureWorktreeReadsBaseAndIncludes drives the entry point the
// role loop and worker spawn share, so the frontmatter `base:` and
// the .worktreeinclude copy stay wired to worktree creation.
func TestEnsureWorktreeReadsBaseAndIncludes(t *testing.T) {
	r := newBaseRepo(t)
	want := r.push("feat", "stacked parent")
	r.push("main", "default ahead")
	writeFile(t, filepath.Join(r.root, ".gitignore"), ".env\n")
	writeFile(t, filepath.Join(r.root, ".env"), "SECRET=1\n")
	writeFile(t, filepath.Join(r.root, WorktreeIncludeFile), ".env\n")
	tasksDir := filepath.Join(r.root, "tasks")
	writeFile(t, filepath.Join(tasksDir, "x.md"), "---\nstatus: active\nslug: x\nbase: feat\n---\nbody\n")

	worktree, err := EnsureWorktree(tasksDir, "x")
	if err != nil {
		t.Fatalf("EnsureWorktree: %v", err)
	}
	if got := r.rev("refs/heads/wt/x"); got != want {
		t.Errorf("wt/x = %s, want origin/feat %s from the base: frontmatter", got, want)
	}
	if b, err := os.ReadFile(filepath.Join(worktree, ".env")); err != nil || string(b) != "SECRET=1\n" {
		t.Errorf(".env in worktree = %q (%v), want the .worktreeinclude copy", b, err)
	}
}

// TestLockWaitDoesNotEatFetchDeadline: a spawn that waited on another
// fetch's lock still gives its own fetch the full deadline, so a slow
// but answering origin is not remembered as a timeout.
func TestLockWaitDoesNotEatFetchDeadline(t *testing.T) {
	old := fetchTimeout
	fetchTimeout = 800 * time.Millisecond
	t.Cleanup(func() { fetchTimeout = old })
	r := newBaseRepo(t)
	slow := filepath.Join(t.TempDir(), "slow-ssh")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 0.1\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", slow)
	runGit(t, r.root, "remote", "set-url", "origin", "ssh://example.invalid/repo.git")

	f, err := os.OpenFile(filepath.Join(gitCommonDir(r.root), "spore-fetch.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(700 * time.Millisecond)
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}()

	_, warn := r.add("a", "")
	if !strings.Contains(warn, "failed") {
		t.Errorf("warn = %q, want the slow fetch's own failure, not a timeout", warn)
	}
	if _, ok := fetchMemo(gitCommonDir(r.root)); ok {
		t.Error("lock wait plus a fast-enough fetch was remembered as a timeout")
	}
}

// TestFetchDoesNotWaitOnSSHGrandchild: an ssh helper that leaves a
// background process holding stderr must not delay the spawn past
// git's own exit.
func TestFetchDoesNotWaitOnSSHGrandchild(t *testing.T) {
	old := fetchTimeout
	fetchTimeout = 5 * time.Second
	t.Cleanup(func() { fetchTimeout = old })
	r := newBaseRepo(t)
	lingering := filepath.Join(t.TempDir(), "lingering-ssh")
	if err := os.WriteFile(lingering, []byte("#!/bin/sh\nsleep 3 >/dev/null &\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", lingering)
	runGit(t, r.root, "remote", "set-url", "origin", "ssh://example.invalid/repo.git")
	start := time.Now()
	_, warn := r.add("a", "")
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("spawn took %s, want it to return when git exits", d)
	}
	if !strings.Contains(warn, "failed") {
		t.Errorf("warn = %q, want a fetch failure", warn)
	}
}

const fakeFetchToken = "ghs_faketoken123"

// withFetchAuth installs a fake provider that applies the token the
// way ghapp.Env does (GH_TOKEN in the env) and records its calls.
func withFetchAuth(t *testing.T, err error) *int {
	t.Helper()
	calls := 0
	SetFetchAuth(func(ctx context.Context, projectRoot, originURL string, base []string) ([]string, error) {
		calls++
		if originURL != "https://github.invalid/o/r.git" {
			t.Errorf("provider got origin %q, want the raw configured URL", originURL)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("provider called without a deadline")
		}
		if err != nil {
			return nil, err
		}
		return append(append([]string{}, base...), "GH_TOKEN="+fakeFetchToken), nil
	})
	t.Cleanup(func() { SetFetchAuth(nil) })
	return &calls
}

// captureFetch records the env and argv of every fetch spore runs.
func captureFetch(t *testing.T) *[]*exec.Cmd {
	t.Helper()
	var cmds []*exec.Cmd
	runFetchCmd = func(cmd *exec.Cmd) ([]byte, error) {
		cmds = append(cmds, cmd)
		return runFetch(cmd)
	}
	t.Cleanup(func() { runFetchCmd = runFetch })
	return &cmds
}

// setOrigin points origin at url and rewrites url to target, so an
// https or ssh origin fetches from a local repo with no network.
func (r *baseRepo) setOrigin(url, target string) {
	r.t.Helper()
	runGit(r.t, r.root, "remote", "set-url", "origin", url)
	runGit(r.t, r.root, "config", "url."+target+".insteadOf", url)
}

func envHas(cmd *exec.Cmd, kv string) bool {
	for _, e := range cmd.Env {
		if e == kv {
			return true
		}
	}
	return false
}

func TestBaseFetchUsesAppTokenForHTTPSOrigin(t *testing.T) {
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_SSH", "")
	r := newBaseRepo(t)
	r.setOrigin("https://github.invalid/o/r.git", r.origin)
	want := r.push("main", "ahead on origin")
	calls := withFetchAuth(t, nil)
	cmds := captureFetch(t)

	got, warn := r.add("tok", "")
	if got != want {
		t.Errorf("forked from %s, want the fetched origin/main %s", got, want)
	}
	if warn != "" {
		t.Errorf("warnings = %q, want none", warn)
	}
	if *calls != 1 || len(*cmds) != 1 {
		t.Fatalf("provider calls = %d, fetches = %d, want 1 and 1", *calls, len(*cmds))
	}
	cmd := (*cmds)[0]
	if !envHas(cmd, "GH_TOKEN="+fakeFetchToken) {
		t.Error("fetch env lacks the app token")
	}
	if !envHas(cmd, "GIT_TERMINAL_PROMPT=0") {
		t.Error("authenticated fetch lost the non-interactive settings")
	}
	if strings.Contains(strings.Join(cmd.Args, " "), fakeFetchToken) {
		t.Errorf("token on the command line: %q", cmd.Args)
	}
	for _, dir := range []string{r.root, filepath.Join(r.root, ".worktrees", "tok")} {
		if cfg := gitOutput(t, dir, "config", "--list", "--show-origin"); strings.Contains(cfg, fakeFetchToken) {
			t.Errorf("token written to git config in %s", dir)
		}
	}
	if url := gitOutput(t, r.root, "config", "--get", "remote.origin.url"); url != "https://github.invalid/o/r.git" {
		t.Errorf("origin url changed to %q", url)
	}
}

func TestBaseFetchFallsBackToPlainFetch(t *testing.T) {
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_SSH", "")
	cases := []struct {
		name      string
		url       string
		authErr   error
		badOrigin bool
		calls     int
		warnLike  string
	}{
		{name: "no config", url: "https://github.invalid/o/r.git", authErr: ErrNoFetchAuth, calls: 1},
		{name: "mint error", url: "https://github.invalid/o/r.git", authErr: errors.New("mint: 401 Bad credentials"), calls: 1, warnLike: "token unavailable: mint: 401"},
		{name: "ssh origin", url: "ssh://git@github.invalid/o/r.git", calls: 0},
		{name: "mint error and failed fetch", url: "https://github.invalid/o/r.git", authErr: errors.New("mint: 401 Bad credentials"), badOrigin: true, calls: 1, warnLike: "failed"},
		{name: "no config and failed fetch", url: "https://github.invalid/o/r.git", authErr: ErrNoFetchAuth, badOrigin: true, calls: 1, warnLike: "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newBaseRepo(t)
			target := r.origin
			if tc.badOrigin {
				target = filepath.Join(t.TempDir(), "missing.git")
			}
			r.setOrigin(tc.url, target)
			pushed := r.push("main", "ahead on origin")
			calls := withFetchAuth(t, tc.authErr)
			cmds := captureFetch(t)

			got, warn := r.add("plain", "")
			if *calls != tc.calls {
				t.Errorf("provider calls = %d, want %d", *calls, tc.calls)
			}
			if len(*cmds) != 1 {
				t.Fatalf("fetches = %d, want 1", len(*cmds))
			}
			cmd := (*cmds)[0]
			if envHas(cmd, "GH_TOKEN="+fakeFetchToken) {
				t.Error("plain fallback carries the token")
			}
			if !envHas(cmd, "GIT_TERMINAL_PROMPT=0") {
				t.Error("plain fetch is not non-interactive")
			}
			if !tc.badOrigin && got != pushed {
				t.Errorf("forked from %s, want the plainly fetched %s", got, pushed)
			}
			if n := strings.Count(warn, "\n"); tc.warnLike == "" && n != 0 || tc.warnLike != "" && n != 1 {
				t.Errorf("warnings = %q, want %s", warn, map[bool]string{true: "none", false: "exactly one"}[tc.warnLike == ""])
			}
			if !strings.Contains(warn, tc.warnLike) {
				t.Errorf("warning %q does not mention %q", warn, tc.warnLike)
			}
			if tc.badOrigin && tc.authErr != ErrNoFetchAuth && !strings.Contains(warn, "token unavailable") {
				t.Errorf("failed-fetch warning drops the mint error: %q", warn)
			}
		})
	}
}

func TestEnsureWorktreeExistingSkipsMeta(t *testing.T) {
	r := newBaseRepo(t)
	tasksDir := filepath.Join(r.root, "tasks")
	writeFile(t, filepath.Join(tasksDir, "x.md"), "---\nstatus: active\nslug: x\n")
	if _, err := readTaskMeta(tasksDir, "x"); err == nil {
		t.Fatal("fixture: frontmatter parses; want a half-written one")
	}
	want := filepath.Join(r.root, ".worktrees", "x")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := EnsureWorktree(tasksDir, "x")
	if err != nil || got != want {
		t.Fatalf("EnsureWorktree = %q, %v; want the existing %q", got, err, want)
	}
}

// TestAddWorktreeCopiesIncludesBeforeBrief: a later Ensure skips an
// existing worktree, so a failed brief copy must not lose the includes.
func TestAddWorktreeCopiesIncludesBeforeBrief(t *testing.T) {
	r := newBaseRepo(t)
	writeFile(t, filepath.Join(r.root, ".gitignore"), ".env\n")
	writeFile(t, filepath.Join(r.root, ".env"), "SECRET=1\n")
	writeFile(t, filepath.Join(r.root, WorktreeIncludeFile), ".env\n")
	tasksDir := filepath.Join(r.root, "tasks")
	if err := os.MkdirAll(filepath.Join(tasksDir, "x.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := addWorktree(tasksDir, r.root, "x", "", io.Discard); err == nil {
		t.Fatal("addWorktree: want the brief copy to fail")
	}
	if !exists(filepath.Join(r.root, ".worktrees", "x", ".env")) {
		t.Error(".worktreeinclude files not copied when the brief copy failed")
	}
}

// TestBaseFetchRetriesPlainAfterAuthFailure: an app installation that
// does not cover origin must not cost the operator's own credentials.
func TestBaseFetchRetriesPlainAfterAuthFailure(t *testing.T) {
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_SSH", "")
	for _, plainWorks := range []bool{true, false} {
		r := newBaseRepo(t)
		target := r.origin
		if !plainWorks {
			target = filepath.Join(t.TempDir(), "missing.git")
		}
		r.setOrigin("https://github.invalid/o/r.git", target)
		want := r.push("main", "ahead on origin")
		// The token env refuses the local transport, standing in for an
		// app that cannot read this repo.
		SetFetchAuth(func(ctx context.Context, projectRoot, originURL string, base []string) ([]string, error) {
			return append(append([]string{}, base...), "GH_TOKEN="+fakeFetchToken,
				"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=protocol.file.allow", "GIT_CONFIG_VALUE_0=never"), nil
		})
		t.Cleanup(func() { SetFetchAuth(nil) })
		cmds := captureFetch(t)

		got, warn := r.add("retry", "")
		if len(*cmds) != 2 || !envHas((*cmds)[0], "GH_TOKEN="+fakeFetchToken) || envHas((*cmds)[1], "GH_TOKEN="+fakeFetchToken) {
			t.Fatalf("plainWorks=%v: want an authenticated fetch then a plain one, got %d fetches", plainWorks, len(*cmds))
		}
		if strings.Count(warn, "\n") != 1 || !strings.Contains(warn, "GitHub App token") {
			t.Errorf("plainWorks=%v: warnings = %q, want one naming the token failure", plainWorks, warn)
		}
		if plainWorks && got != want {
			t.Errorf("forked from %s, want the plainly fetched %s", got, want)
		}
	}
}

func TestBaseNormalized(t *testing.T) {
	for in, want := range map[string]string{
		" feat ":                   "feat",
		"origin/feat":              "feat",
		"refs/heads/feat":          "feat",
		"refs/remotes/origin/feat": "feat",
		"feat/origin/x":            "feat/origin/x",
		"":                         "",
	} {
		if got := normalizeBase(in); got != want {
			t.Errorf("normalizeBase(%q) = %q, want %q", in, got, want)
		}
	}
	r := newBaseRepo(t)
	want := r.push("feat", "stacked parent")
	if got, warn := r.add("norm", "origin/feat"); got != want || warn != "" {
		t.Errorf("base: origin/feat forked from %s (warn %q), want origin/feat %s", got, warn, want)
	}
}

// TestPlainRetryGetsItsOwnDeadline: a token fetch that fails just
// before the deadline must not leave the retry to time out and mark
// a reachable origin as down.
func TestPlainRetryGetsItsOwnDeadline(t *testing.T) {
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_SSH", "")
	old := fetchTimeout
	fetchTimeout = 2 * time.Second
	t.Cleanup(func() { fetchTimeout = old })
	r := newBaseRepo(t)
	r.setOrigin("https://github.invalid/o/r.git", r.origin)
	want := r.push("main", "ahead on origin")
	withFetchAuth(t, nil)
	runFetchCmd = func(cmd *exec.Cmd) ([]byte, error) {
		if envHas(cmd, "GH_TOKEN="+fakeFetchToken) {
			time.Sleep(fetchTimeout - 300*time.Millisecond)
			return []byte("fatal: Authentication failed\n"), errors.New("exit status 128")
		}
		// Starts past the first attempt's deadline: only a fresh
		// deadline lets it run.
		time.Sleep(400 * time.Millisecond)
		return runFetch(cmd)
	}
	t.Cleanup(func() { runFetchCmd = runFetch })

	got, warn := r.add("slow", "")
	if got != want {
		t.Errorf("forked from %s, want the plainly fetched %s (warn %q)", got, want, warn)
	}
	if _, ok := fetchMemo(gitCommonDir(r.root)); ok {
		t.Error("a reachable origin was remembered as timed out")
	}
}
