package fleet

import (
	"fmt"

	"github.com/versality/spore/internal/secrets"
)

// push sends sha (HEAD when empty) to origin as the app. A branch
// whose last pushed commit is no longer an ancestor of sha was
// rewritten and goes up with --force-with-lease pinned to that commit.
func (m *githubMirror) push(sha string) error {
	if sha == "" {
		var err error
		if sha, err = m.git("rev-parse", "HEAD"); err != nil {
			return err
		}
	}
	if sha == m.state.PushedSHA {
		return nil
	}
	branch, err := m.branch()
	if err != nil {
		return err
	}
	ref := "refs/heads/" + branch
	lease := ""
	if prev := m.state.PushedSHA; prev != "" {
		if _, err := m.git("merge-base", "--is-ancestor", prev, sha); err != nil {
			lease = prev
		}
	}
	if m.brokered {
		err = secrets.Push(m.project, m.worktree, branch, sha, lease, mirrorCallTimeout)
	} else {
		args := []string{"push", "--quiet", "origin", sha + ":" + ref}
		if lease != "" {
			args = append(args, "--force-with-lease="+ref+":"+lease)
		}
		_, err = m.git(args...)
	}
	if err != nil {
		return err
	}
	m.state.PushedSHA = sha
	m.save()
	return nil
}

// pushForward is push without moving origin back to an ancestor of the
// last pushed commit.
func (m *githubMirror) pushForward(sha string) error {
	if prev := m.state.PushedSHA; prev != "" {
		if _, err := m.git("merge-base", "--is-ancestor", sha, prev); err == nil {
			return nil
		}
	}
	return m.push(sha)
}

func (m *githubMirror) branch() (string, error) {
	b, err := m.git("symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("worktree HEAD is not on a branch: %w", err)
	}
	return b, nil
}
