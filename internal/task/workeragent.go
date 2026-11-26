package task

import (
	"os"

	"github.com/versality/spore/codexpolicy"
	"github.com/versality/spore/internal/task/frontmatter"
)

// workerAgentCommand returns the agent command for a worker. The
// SPORE_AGENT_BINARY override applies to claude workers only: on hosts
// it is a claude launcher, so honouring it for codex would run claude.
// Model and effort flags go on a claude-shaped command; a launcher
// reads them from SPORE_AGENT_MODEL / SPORE_AGENT_EFFORT instead (see
// workerSettingsEnv).
func workerAgentCommand(m frontmatter.Meta, s AgentSettings) (string, error) {
	switch m.Agent {
	case "", "claude", "claude-code":
		agent := defaultAgentBinary
		if override := os.Getenv(AgentBinaryEnv); override != "" {
			agent = override
		}
		if flags := ClaudeFlags(s); len(flags) > 0 && IsClaudeCommand(agent) {
			agent += " " + shellJoin(flags)
		}
		return agent, nil
	case "codex":
		return CodexAgentCommand(m, s)
	default:
		return m.Agent, nil
	}
}

// isClaudeWorker reports whether the task runs on the claude CLI.
func isClaudeWorker(m frontmatter.Meta) bool {
	switch m.Agent {
	case "", "claude", "claude-code":
		return true
	}
	return false
}

// Session env a claude worker launcher reads for model and effort.
const (
	AgentModelEnv  = "SPORE_AGENT_MODEL"
	AgentEffortEnv = "SPORE_AGENT_EFFORT"
)

// workerSettingsEnv returns the tmux -e pairs carrying a claude
// worker's model and effort, for launchers such as spore-worker-brief.
func workerSettingsEnv(m frontmatter.Meta, s AgentSettings) []string {
	if !isClaudeWorker(m) {
		return nil
	}
	var out []string
	if s.Model != "" {
		out = append(out, "-e", AgentModelEnv+"="+s.Model)
	}
	if s.Effort != "" {
		out = append(out, "-e", AgentEffortEnv+"="+NormalizeAgentEffort(s.Effort))
	}
	return out
}

// WorkerAgentSettings returns the worker's settings from
// [agents.worker] with the task frontmatter on top.
func WorkerAgentSettings(projectRoot string, m frontmatter.Meta) (AgentSettings, error) {
	cfg, err := LoadAgentsConfig(projectRoot)
	if err != nil {
		return AgentSettings{}, err
	}
	return ResolveAgentSettings(cfg, AgentRoleWorker, "", m, true), nil
}

// CodexAgentCommand returns the interactive codex command line for a
// task, without a prompt, from the pane's resolved settings (see
// CodexEffective). Role panes share it.
func CodexAgentCommand(m frontmatter.Meta, s AgentSettings) (string, error) {
	eff, err := CodexEffective(m, s)
	if err != nil {
		return "", err
	}
	args := codexpolicy.InteractiveArgs(eff.Model, eff.Effort)
	if bin := os.Getenv(CodexBinEnv); bin != "" {
		args[0] = bin
	}
	return shellJoin(args), nil
}

// CodexEffective fills what codex actually runs with: an unset effort
// falls back to the task's complexity policy, an unset model to
// SPORE_CODEX_MODEL.
func CodexEffective(m frontmatter.Meta, s AgentSettings) (AgentSettings, error) {
	effort, err := codexpolicy.EffortForTask(s.Effort, m.Extra["complexity"])
	if err != nil {
		return AgentSettings{}, err
	}
	model := s.Model
	if model == "" {
		model = os.Getenv(CodexModelEnv)
	}
	return AgentSettings{Model: model, Effort: effort}, nil
}
