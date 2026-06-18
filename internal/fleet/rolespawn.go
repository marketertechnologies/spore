package fleet

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/versality/spore/internal/task"
)

// RoleSpawnSpec describes a single role pane the coordinator wants
// alive for a task. The coordinator builds one per role / instance
// (engineer + reviewer A + reviewer B) and feeds them to SpawnRole.
type RoleSpawnSpec struct {
	// ProjectRoot is the main repo root. Required.
	ProjectRoot string
	// Slug is the task slug. Required.
	Slug string
	// Role is the role name ("engineer" or "reviewer"). Required.
	Role string
	// ReviewerInstance is the per-instance label. Required when Role
	// is "reviewer". Ignored otherwise.
	ReviewerInstance task.ReviewerInstance
	// RoleBodyPath is the absolute path to the role markdown that
	// the agent should consume as its first user message.
	// Defaults to <ProjectRoot>/bootstrap/roles/<Role>.md.
	RoleBodyPath string
	// Cwd is the working directory for the pane. Defaults to the
	// task worktree at <ProjectRoot>/.worktrees/<Slug>/ for both
	// roles: the engineer commits there and the reviewer needs to
	// `git diff` the branch from there. A consumer with a non-
	// standard worktree layout must set Cwd explicitly.
	Cwd string
	// Agent is the binary the pane execs. Defaults to "claude".
	Agent string
}

// SessionName returns the tmux session name the spec spawns. For
// engineers: "spore-role/<project>/<slug>/engineer". For reviewers:
// "spore-role/<project>/<slug>/reviewer-<instance>". The "spore-role/"
// prefix keeps role panes off the existing fleet reconciler's spawn-
// and-reap path, which targets the "spore/" prefix.
func (s RoleSpawnSpec) SessionName() (string, error) {
	project, err := task.ProjectName(s.ProjectRoot)
	if err != nil {
		return "", err
	}
	switch s.Role {
	case "engineer":
		return fmt.Sprintf("spore-role/%s/%s/engineer", project, s.Slug), nil
	case "reviewer":
		if err := validateInstance(s.ReviewerInstance); err != nil {
			return "", err
		}
		return fmt.Sprintf("spore-role/%s/%s/reviewer-%s", project, s.Slug, s.ReviewerInstance), nil
	default:
		return "", fmt.Errorf("rolespawn: unknown role %q (want engineer or reviewer)", s.Role)
	}
}

// TmuxArgs returns the argv that `tmux <args>` would receive to spawn
// the session. Exposed so tests can assert on the env wiring without
// invoking tmux.
func (s RoleSpawnSpec) TmuxArgs() ([]string, error) {
	if s.ProjectRoot == "" {
		return nil, fmt.Errorf("rolespawn: empty project root")
	}
	if s.Slug == "" {
		return nil, fmt.Errorf("rolespawn: empty slug")
	}
	session, err := s.SessionName()
	if err != nil {
		return nil, err
	}

	rolePath := s.RoleBodyPath
	if rolePath == "" {
		rolePath = filepath.Join(s.ProjectRoot, "bootstrap", "roles", s.Role+".md")
	}
	cwd := s.Cwd
	if cwd == "" {
		cwd = filepath.Join(s.ProjectRoot, ".worktrees", s.Slug)
	}
	agent := s.Agent
	if agent == "" {
		agent = "claude"
	}

	taskDir := task.RoleTaskDir(s.ProjectRoot, s.Slug)
	project, err := task.ProjectName(s.ProjectRoot)
	if err != nil {
		return nil, err
	}

	args := []string{
		"new-session", "-d",
		"-s", session,
		"-c", cwd,
		"-e", "SPORE_TASK_SLUG=" + s.Slug,
		"-e", "SPORE_PROJECT_ROOT=" + s.ProjectRoot,
		"-e", "WT_PROJECT=" + project,
		"-e", "SPORE_TASK_DIR=" + taskDir,
		"-e", "SPORE_ROLE=" + s.Role,
	}
	if s.Role == "reviewer" {
		args = append(args, "-e", "SPORE_REVIEWER_INSTANCE="+string(s.ReviewerInstance))
	}
	args = append(args, coordinatorShellCommand(agent, rolePath))
	return args, nil
}

// SpawnRole creates the tmux session for spec, idempotently. A live
// session at the computed name is left alone. Returns the session
// name plus whether a spawn actually happened.
func SpawnRole(spec RoleSpawnSpec) (string, bool, error) {
	name, err := spec.SessionName()
	if err != nil {
		return "", false, err
	}
	if hasSession(name) {
		return name, false, nil
	}
	args, err := spec.TmuxArgs()
	if err != nil {
		return "", false, err
	}
	if _, err := task.EnsureRoleTaskDir(spec.ProjectRoot, spec.Slug); err != nil {
		return "", false, err
	}
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		return "", false, fmt.Errorf("tmux new-session: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return name, true, nil
}

// ReapRole kills the tmux session for spec. Idempotent.
func ReapRole(spec RoleSpawnSpec) (bool, error) {
	name, err := spec.SessionName()
	if err != nil {
		return false, err
	}
	if !hasSession(name) {
		return false, nil
	}
	_ = exec.Command("tmux", "kill-session", "-t", name).Run()
	return true, nil
}

// EngineerSpec builds the engineer pane spec for a task.
func EngineerSpec(projectRoot, slug string) RoleSpawnSpec {
	return RoleSpawnSpec{ProjectRoot: projectRoot, Slug: slug, Role: "engineer"}
}

// ReviewerSpec builds a reviewer pane spec for the given instance.
func ReviewerSpec(projectRoot, slug string, instance task.ReviewerInstance) RoleSpawnSpec {
	return RoleSpawnSpec{
		ProjectRoot:      projectRoot,
		Slug:             slug,
		Role:             "reviewer",
		ReviewerInstance: instance,
	}
}

func validateInstance(i task.ReviewerInstance) error {
	if i != task.ReviewerA && i != task.ReviewerB {
		return fmt.Errorf("rolespawn: reviewer instance %q must be %q or %q", i, task.ReviewerA, task.ReviewerB)
	}
	return nil
}
