package task

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/versality/spore/internal/task/frontmatter"
)

// StartOptions tunes Start. Agent, when set, overwrites the task's
// agent: frontmatter before the session spawns.
type StartOptions struct {
	Agent string
}

// ValidateAgent rejects agent names spore cannot launch as a worker.
func ValidateAgent(agent string) error {
	switch agent {
	case "claude", "codex":
		return nil
	}
	return fmt.Errorf("agent %q: want claude or codex", agent)
}

// Start flips status to active and (when starting from draft) creates
// the worktree and wt/<slug> branch under <projectRoot>/.worktrees/.
// In every case it spawns a detached tmux session named
// "spore/<project>/<slug>" running ${SPORE_AGENT_BINARY:-claude-code}
// in the worktree, with SPORE_TASK_SLUG=<slug> in the session env.
// Returns the tmux session name on success.
func Start(tasksDir, slug string) (string, error) {
	return StartWithOptions(tasksDir, slug, StartOptions{})
}

func StartWithOptions(tasksDir, slug string, opts StartOptions) (string, error) {
	m, err := activate(tasksDir, slug, opts)
	if err != nil {
		return "", err
	}

	projectRoot, err := projectRootFromTasksDir(tasksDir)
	if err != nil {
		return "", err
	}
	session := taskTmuxSession(tasksDir, projectRoot, slug)
	// Pause leaves the session alive for the operator; Start
	// replaces it so a resume gets a fresh agent and new-session
	// does not collide on the name.
	_ = exec.Command("tmux", "kill-session", "-t", session).Run()
	out, err := ensureSession(tasksDir, slug)
	if err != nil {
		return "", err
	}
	notifyMatterSpawn(projectRoot, slug, m, os.Stderr)
	return out, nil
}

// activate validates the task for Start and writes status: active
// (plus opts.Agent) to its file.
func activate(tasksDir, slug string, opts StartOptions) (frontmatter.Meta, error) {
	if opts.Agent != "" {
		if err := ValidateAgent(opts.Agent); err != nil {
			return frontmatter.Meta{}, err
		}
	}
	path := filepath.Join(tasksDir, slug+".md")
	raw, err := os.ReadFile(path)
	if err != nil {
		return frontmatter.Meta{}, err
	}
	m, body, err := frontmatter.Parse(raw)
	if err != nil {
		return frontmatter.Meta{}, fmt.Errorf("parse %s: %w", path, err)
	}
	// A role-looped task must never get the homogeneous worker session
	// Start spawns: its panes are owned by DriveRoleLoop and share the
	// same wt/<slug> branch.
	if m.Loop == LoopRole {
		return frontmatter.Meta{}, fmt.Errorf(
			"task %s: has loop: role; edit status to active and let the reconciler drive it, or run `spore task role-drive %s`",
			slug, slug,
		)
	}
	switch m.Status {
	case "draft", "paused", "blocked":
	case "active":
		return frontmatter.Meta{}, fmt.Errorf("task %s: already active", slug)
	case "done":
		return frontmatter.Meta{}, fmt.Errorf("task %s: already done", slug)
	default:
		return frontmatter.Meta{}, fmt.Errorf("task %s: unexpected status %q", slug, m.Status)
	}
	m.Status = "active"
	if opts.Agent != "" {
		m.Agent = opts.Agent
	}
	if err := os.WriteFile(path, frontmatter.Write(m, body), 0o644); err != nil {
		return frontmatter.Meta{}, err
	}
	return m, nil
}
