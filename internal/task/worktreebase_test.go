package task

import (
	"bytes"
	"context"
	"errors"
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
	oldTimeout, oldDelay := fetchTimeout, fetchWaitDelay
	fetchTimeout, fetchWaitDelay = 300*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { fetchTimeout, fetchWaitDelay = oldTimeout, oldDelay })
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
	cmd := fetchCmd(context.Background(), r.root, "feat")
	if !strings.HasSuffix(strings.Join(cmd.Args, " "), "origin +refs/heads/feat:refs/remotes/origin/feat") {
		t.Errorf("args = %q, want an explicit refspec", cmd.Args)
	}
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid {
		t.Error("fetch runs in its own process group")
	}
	if cmd.WaitDelay == 0 {
		t.Error("no WaitDelay")
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
			if cmd := fetchCmd(context.Background(), r.root, "feat"); cmd.Env != nil {
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
	oldTimeout, oldDelay := fetchTimeout, fetchWaitDelay
	fetchTimeout, fetchWaitDelay = 800*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { fetchTimeout, fetchWaitDelay = oldTimeout, oldDelay })
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
