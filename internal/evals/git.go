package evals

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/versality/spore/internal/task"
	"github.com/versality/spore/internal/task/frontmatter"
)

// bundleHead is the ref a case bundle carries its head_sha under.
const bundleHead = "refs/heads/spore-eval-head"

func git(ctx context.Context, dir string, args ...string) (string, error) {
	return gitTrusting(ctx, dir, nil, args...)
}

// gitTrusting runs git in dir, trusting dir plus extra (a clone
// source) past the dubious-ownership guard, like task.gitCmd.
func gitTrusting(ctx context.Context, dir string, extra []string, args ...string) (string, error) {
	var full []string
	for _, p := range append([]string{dir}, extra...) {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		full = append(full, "-c", "safe.directory="+p)
	}
	full = append(append(full, "-C", dir), args...)
	out, err := exec.CommandContext(ctx, "git", full...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// reviewedSHA recovers the commit slot reviewed at round: the drive
// loop's stamp when present, else the last commit on the task branch
// at or before the verdict file's mtime. Runs that predate the stamp
// and whose branch is gone need an explicit sha.
func reviewedSHA(ctx context.Context, projectRoot, slug string, slot task.ReviewerInstance, round int) (string, error) {
	if b, err := os.ReadFile(task.ReviewedSHAPath(projectRoot, slug, slot, round)); err == nil {
		if sha := strings.TrimSpace(string(b)); sha != "" {
			return sha, nil
		}
	}
	fi, err := os.Stat(task.ReviewPath(projectRoot, slug, slot, round))
	if err != nil {
		return "", err
	}
	// RFC 3339: git's approxidate misreads a bare epoch.
	before := fi.ModTime().UTC().Format(time.RFC3339)
	for _, ref := range []string{"refs/heads/wt/" + slug, "refs/remotes/origin/wt/" + slug} {
		if _, err := git(ctx, projectRoot, "rev-parse", "--verify", "--quiet", ref); err != nil {
			continue
		}
		sha, err := git(ctx, projectRoot, "rev-list", "-1", "--before="+before, ref)
		if err == nil && sha != "" {
			return sha, nil
		}
	}
	return "", fmt.Errorf("%s: cannot recover the commit reviewer %s reviewed at round %d (branch wt/%s gone); pass --head-sha", slug, slot, round, slug)
}

// baseBranch picks the review base the way the loop does: the task's
// `base:` frontmatter, else origin/HEAD, else main or master.
func baseBranch(ctx context.Context, projectRoot, slug string) (string, error) {
	if raw, err := os.ReadFile(filepath.Join(projectRoot, "tasks", slug+".md")); err == nil {
		if m, _, err := frontmatter.Parse(raw); err == nil && m.Extra["base"] != "" {
			return m.Extra["base"], nil
		}
	}
	if head, err := git(ctx, projectRoot, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(head, "origin/"), nil
	}
	for _, name := range []string{"main", "master"} {
		if _, err := git(ctx, projectRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+name); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("no base branch: set `base:` in tasks/%s.md or pass --base", slug)
}

func resolveRef(ctx context.Context, projectRoot, name string) (string, error) {
	for _, ref := range []string{name, "origin/" + name} {
		if sha, err := git(ctx, projectRoot, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil {
			return sha, nil
		}
	}
	return "", fmt.Errorf("cannot resolve %s", name)
}

// writeBundle stores head's objects past base in <dir>/head.bundle so
// the case survives the task branch being deleted. The ref is set in
// a shared scratch clone: the project repo is never written.
func writeBundle(ctx context.Context, projectRoot, base, head, dir string) error {
	tmp, err := os.MkdirTemp("", "spore-eval-bundle-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	clone := filepath.Join(tmp, "repo")
	if _, err := gitTrusting(ctx, tmp, []string{projectRoot}, "clone", "--quiet", "--shared", "--bare", projectRoot, clone); err != nil {
		return err
	}
	if _, err := git(ctx, clone, "update-ref", bundleHead, head); err != nil {
		return err
	}
	rng := []string{bundleHead}
	if base != head {
		rng = append(rng, "^"+base)
	}
	_, err = git(ctx, clone, append([]string{"bundle", "create", filepath.Join(dir, "head.bundle")}, rng...)...)
	return err
}

// checkout makes a scratch repo at dir with head checked out on
// branch, and baseBranch pointing at base. It is a shared clone with
// no remote, so the reviewer cannot diff against a live branch that
// moved on since the case was frozen.
func checkout(ctx context.Context, c Case, dir string) error {
	repo := c.RepoRoot()
	if _, err := gitTrusting(ctx, filepath.Dir(dir), []string{repo}, "clone", "--quiet", "--shared", "--no-checkout", repo, dir); err != nil {
		return err
	}
	if _, err := git(ctx, dir, "cat-file", "-e", c.HeadSHA+"^{commit}"); err != nil {
		bundle := filepath.Join(c.Dir, "head.bundle")
		if _, serr := os.Stat(bundle); serr != nil {
			return fmt.Errorf("case %s: head %s not in %s and no head.bundle", c.ID, c.HeadSHA, repo)
		}
		if _, err := git(ctx, dir, "fetch", "--quiet", bundle, bundleHead); err != nil {
			return fmt.Errorf("case %s: %w", c.ID, err)
		}
	}
	if _, err := git(ctx, dir, "remote", "remove", "origin"); err != nil {
		return err
	}
	branch := "wt/" + c.SourceSlug
	if c.SourceSlug == "" {
		branch = "wt/eval"
	}
	if _, err := git(ctx, dir, "checkout", "--quiet", "-B", branch, c.HeadSHA); err != nil {
		return err
	}
	// The clone's default branch sits at the source repo's live tip;
	// drop every branch but the checked-out one so no stale base is
	// left for the reviewer to diff against.
	others, err := git(ctx, dir, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return err
	}
	for _, b := range strings.Fields(others) {
		if b != branch {
			if _, err := git(ctx, dir, "branch", "-D", b); err != nil {
				return err
			}
		}
	}
	if c.BaseBranch != "" && c.BaseBranch != branch {
		if _, err := git(ctx, dir, "branch", "--force", c.BaseBranch, c.BaseSHA); err != nil {
			return err
		}
	}
	return nil
}
