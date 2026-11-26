package secrets

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGH puts a gh on PATH that prints its args and stdin, so a test
// sees exactly what the broker ran.
func fakeGH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "gh"), `#!/bin/sh
echo "gh $* repo=$GH_REPO"
test -n "$GH_TOKEN" || { echo "no token" >&2; exit 4; }
if [ "$1 $2" = "pr create" ]; then echo https://github.com/acme/alpha/pull/9; fi
case "$*" in *body-file*|*" -F -"*) echo "body=$(cat)";; esac
`)
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestCLIGHThroughBroker covers the calls `spore gh pr` and `spore gh
// api` make behind the broker: draft create, ready, comment, and the
// refusals.
func TestCLIGHThroughBroker(t *testing.T) {
	c := testConfig(t)
	s := startServer(t, c)
	s.mint = func(context.Context, string) (string, error) { return "ghs_testtoken", nil }
	fakeGH(t)

	resp, err := GHRaw("alpha", "the body", []string{"pr", "create", "--draft", "--title", "t", "--body-file", "-"}, 10*time.Second)
	if err != nil || resp.Exit != 0 {
		t.Fatalf("pr create --draft: %+v %v", resp, err)
	}
	for _, want := range []string{"gh pr create --draft --title t --body-file -", "repo=github.com/acme/alpha", "/pull/9", "body=the body"} {
		if !strings.Contains(resp.Stdout, want) {
			t.Errorf("pr create stdout %q lacks %q", resp.Stdout, want)
		}
	}
	for _, args := range [][]string{
		{"pr", "ready", "9"},
		{"pr", "comment", "9", "--body-file", "-"},
		{"pr", "comment", "9", "-F", "-"},
		{"api", "repos/acme/alpha/pulls/9/comments"},
	} {
		resp, err := GHRaw("alpha", "note", args, 10*time.Second)
		if err != nil || resp.Exit != 0 || !strings.Contains(resp.Stdout, "gh "+strings.Join(args, " ")) {
			t.Errorf("%q: %+v %v", args, resp, err)
		}
	}
	for _, args := range [][]string{
		{"pr", "create", "--title", "t", "--body-file", "-"},
		{"pr", "comment", "9", "-F", filepath.Join(c.SecretsDir, "secrets.env")},
		{"pr", "merge", "9"},
		{"api", "repos/acme/beta/pulls/9"},
	} {
		if _, err := GHRaw("alpha", "", args, 10*time.Second); err == nil || !strings.Contains(err.Error(), "refused") {
			t.Errorf("%q: err = %v, want a refusal", args, err)
		}
	}
}

// TestCLIPushThroughBroker covers `spore gh push` behind the broker:
// the branch tip goes up (from a worktree too), a rewrite without a
// lease is refused, and a branch outside push_branch is refused.
func TestCLIPushThroughBroker(t *testing.T) {
	c := testConfig(t)
	p := c.Projects["alpha"]
	p.PushBranch = `^(wt|feature)/[A-Za-z0-9._-]+$`
	c.Projects["alpha"] = p
	s := startServer(t, c)
	tmp := t.TempDir()
	origin := filepath.Join(tmp, "origin.git")
	git(t, tmp, "init", "--quiet", "--bare", "-b", "main", origin)
	s.remote = func(string) string { return origin }
	s.mint = func(context.Context, string) (string, error) { return "ghs_testtoken", nil }

	work := filepath.Join(tmp, "work")
	git(t, tmp, "init", "--quiet", "-b", "main", work)
	git(t, work, "commit", "--quiet", "--allow-empty", "-m", "base")
	git(t, work, "remote", "add", "origin", origin)
	git(t, work, "push", "--quiet", "origin", "main")
	git(t, work, "fetch", "--quiet", "origin")
	git(t, work, "checkout", "--quiet", "-b", "feature/x")
	git(t, work, "commit", "--quiet", "--allow-empty", "-m", "one")
	wt := filepath.Join(tmp, "wt")
	git(t, work, "worktree", "add", "--quiet", "-b", "wt/side", wt, "feature/x")
	git(t, wt, "commit", "--quiet", "--allow-empty", "-m", "side")

	push := func(dir, branch, lease string) (string, error) {
		sha, leaseSHA, err := ResolvePush(dir, branch, lease)
		if err != nil {
			return "", err
		}
		return sha, Push("alpha", dir, branch, sha, leaseSHA, 30*time.Second)
	}
	sha, err := push(work, "feature/x", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, origin, "rev-parse", "refs/heads/feature/x"); got != sha {
		t.Fatalf("origin has %s, want %s", got, sha)
	}
	side, err := push(wt, "wt/side", "")
	if err != nil {
		t.Fatalf("push from a worktree: %v", err)
	}
	if got := git(t, origin, "rev-parse", "refs/heads/wt/side"); got != side {
		t.Fatalf("origin has %s, want %s", got, side)
	}

	git(t, work, "commit", "--quiet", "--amend", "--allow-empty", "-m", "rewritten")
	if _, err := push(work, "feature/x", ""); err == nil {
		t.Fatal("rewrite pushed without a lease")
	}
	rewritten, err := push(work, "feature/x", sha[:10])
	if err != nil {
		t.Fatalf("leased push: %v", err)
	}
	if got := git(t, origin, "rev-parse", "refs/heads/feature/x"); got != rewritten {
		t.Fatalf("origin has %s after the leased push, want %s", got, rewritten)
	}

	git(t, work, "checkout", "--quiet", "-b", "hotfix/y")
	if _, err := push(work, "hotfix/y", ""); err == nil || !strings.Contains(err.Error(), "push_branch") {
		t.Fatalf("branch outside push_branch: %v", err)
	}
	for _, bad := range []string{"+feature/x", "-f", "feature/x:main", "nope/missing"} {
		if _, _, err := ResolvePush(work, bad, ""); err == nil {
			t.Errorf("ResolvePush(%q) accepted", bad)
		}
	}
	if _, _, err := ResolvePush(work, "feature/x", "--force"); err == nil {
		t.Error("ResolvePush accepted an option as the lease")
	}
}
