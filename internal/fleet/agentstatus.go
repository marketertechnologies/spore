package fleet

import (
	"path/filepath"

	"github.com/versality/spore/internal/task"
)

// AgentLine is one pane's resolved agent, model and effort for status
// output. Name is "coordinator", "worker", "engineer" or
// "reviewer-<instance>".
type AgentLine struct {
	Name     string
	Agent    string
	Settings task.AgentSettings
}

func (l AgentLine) String() string {
	return task.FormatAgentSettings(l.Agent, l.Settings)
}

// CoordinatorAgentLine resolves what the coordinator spawns with: the
// configured driver (claude when unset) and the model and effort
// EnsureCoordinator passes, env overrides included.
func CoordinatorAgentLine(projectRoot string) (AgentLine, error) {
	cfg, err := LoadCoordinatorConfig(projectRoot)
	if err != nil {
		return AgentLine{}, err
	}
	driver := coordinatorProvider(cfg)
	if driver == "" {
		driver = "claude"
	}
	return AgentLine{Name: CoordinatorSlug, Agent: driver, Settings: coordinatorSettings(cfg)}, nil
}

// TaskAgentLines resolves the panes a task spawns: engineer and both
// reviewers for a `loop: role` task, else the worker. Codex panes
// show the effort and model codex falls back to.
func TaskAgentLines(projectRoot, tasksDir, slug string) ([]AgentLine, error) {
	if tasksDir == "" {
		tasksDir = filepath.Join(projectRoot, "tasks")
	}
	meta, err := readRoleTaskMeta(tasksDir, slug)
	if err != nil {
		return nil, err
	}
	if meta.Loop == task.LoopRole {
		specs := []RoleSpawnSpec{
			EngineerSpec(projectRoot, slug),
			ReviewerSpec(projectRoot, slug, task.ReviewerA),
			ReviewerSpec(projectRoot, slug, task.ReviewerB),
		}
		var out []AgentLine
		for _, spec := range specs {
			spec.TasksDir = tasksDir
			agent, st, err := spec.ResolvedAgent()
			if err != nil {
				return nil, err
			}
			name := spec.Role
			if spec.Role == "reviewer" {
				name += "-" + string(spec.ReviewerInstance)
			}
			out = append(out, AgentLine{Name: name, Agent: agent, Settings: st})
		}
		return out, nil
	}
	st, err := task.WorkerAgentSettings(projectRoot, meta)
	if err != nil {
		return nil, err
	}
	agent := meta.Agent
	switch agent {
	case "", "claude-code":
		agent = "claude"
	case "codex":
		if st, err = task.CodexEffective(meta, st); err != nil {
			return nil, err
		}
	}
	return []AgentLine{{Name: task.AgentRoleWorker, Agent: agent, Settings: st}}, nil
}

// RoleBodyLine names where one role pane's body comes from.
type RoleBodyLine struct {
	Name, Source string
}

// TaskRoleBodyLines resolves the role body source of each pane a
// `loop: role` task spawns; nil for any other task.
func TaskRoleBodyLines(projectRoot, tasksDir, slug string) ([]RoleBodyLine, error) {
	if tasksDir == "" {
		tasksDir = filepath.Join(projectRoot, "tasks")
	}
	meta, err := readRoleTaskMeta(tasksDir, slug)
	if err != nil || meta.Loop != task.LoopRole {
		return nil, err
	}
	var out []RoleBodyLine
	for _, spec := range []RoleSpawnSpec{
		EngineerSpec(projectRoot, slug),
		ReviewerSpec(projectRoot, slug, task.ReviewerA),
		ReviewerSpec(projectRoot, slug, task.ReviewerB),
	} {
		spec.TasksDir = tasksDir
		src, err := spec.BodySource()
		if err != nil {
			return nil, err
		}
		out = append(out, RoleBodyLine{Name: roleKey(spec), Source: src})
	}
	return out, nil
}
