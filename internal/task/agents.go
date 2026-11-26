package task

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/versality/spore/codexpolicy"
	"github.com/versality/spore/internal/sporetoml"
	"github.com/versality/spore/internal/task/frontmatter"
)

// Agent roles keyed in spore.toml as [agents.<role>]. A reviewer
// slot may also be keyed [agents.reviewer.<A|B>], overlaying
// [agents.reviewer] key by key.
const (
	AgentRoleCoordinator = "coordinator"
	AgentRoleWorker      = "worker"
	AgentRoleEngineer    = "engineer"
	AgentRoleReviewer    = "reviewer"
)

// AgentsSectionPrefix is the spore.toml table prefix for per-role
// agent settings.
const AgentsSectionPrefix = "agents."

// AgentSettingKeys are the keys an [agents.<role>] table accepts.
var AgentSettingKeys = []string{"model", "effort"}

// AgentSections lists every [agents.*] table the kernel reads, for the
// spore.toml schema check.
var AgentSections = []string{
	"agents." + AgentRoleCoordinator,
	"agents." + AgentRoleWorker,
	"agents." + AgentRoleEngineer,
	"agents." + AgentRoleReviewer,
	"agents." + AgentRoleReviewer + "." + string(ReviewerA),
	"agents." + AgentRoleReviewer + "." + string(ReviewerB),
}

// agentEfforts is the union of the claude (--effort) and codex
// (model_reasoning_effort) vocabularies plus the very-high aliases.
// Each CLI's flag builder rejects what that CLI cannot take.
var agentEfforts = []string{"low", "medium", "high", "xhigh", "max", "very-high", "very_high"}

// AgentSettings is the model and effort a spawn passes to the agent
// CLI. An empty field means the CLI's own default.
type AgentSettings struct {
	Model  string
	Effort string
}

// overlay returns s with every non-empty field of o on top.
func (s AgentSettings) overlay(o AgentSettings) AgentSettings {
	if o.Model != "" {
		s.Model = o.Model
	}
	if o.Effort != "" {
		s.Effort = o.Effort
	}
	return s
}

// AgentsConfig maps a section suffix ("worker", "reviewer.A") to its
// settings.
type AgentsConfig map[string]AgentSettings

// For returns the settings for role; instance, when set, overlays the
// role's base table (only reviewers have instances).
func (c AgentsConfig) For(role, instance string) AgentSettings {
	s := c[role]
	if instance != "" {
		s = s.overlay(c[role+"."+instance])
	}
	return s
}

// LoadAgentsConfig reads every [agents.*] table from
// <projectRoot>/spore.toml. Unknown tables and keys are warned about
// on stderr once per process and skipped; an invalid effort is an
// error. A missing file is an empty config.
func LoadAgentsConfig(projectRoot string) (AgentsConfig, error) {
	path := filepath.Join(projectRoot, "spore.toml")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return AgentsConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agents: read %s: %w", path, err)
	}
	cfg, warnings, err := ParseAgentsConfig(string(b))
	if err != nil {
		return nil, fmt.Errorf("agents: %s: %w", path, err)
	}
	for _, w := range warnings {
		warnAgentsOnce(fmt.Sprintf("spore: warning: %s: %s\n", path, w))
	}
	return cfg, nil
}

// ParseAgentsConfig parses the [agents.*] tables in content and
// returns the warnings for unknown tables and keys.
func ParseAgentsConfig(content string) (AgentsConfig, []string, error) {
	cfg := AgentsConfig{}
	var warnings []string
	for _, section := range agentsSectionsIn(content) {
		if !slices.Contains(AgentSections, section) {
			warnings = append(warnings, fmt.Sprintf("unknown section [%s] (known: %s)", section, strings.Join(AgentSections, ", ")))
			continue
		}
		entries, err := sporetoml.ParseSection(content, section)
		if err != nil {
			return nil, nil, err
		}
		s := cfg[strings.TrimPrefix(section, AgentsSectionPrefix)]
		for _, e := range entries {
			switch e.Key {
			case "model":
				s.Model = e.Value
			case "effort":
				if !slices.Contains(agentEfforts, e.Value) {
					return nil, nil, fmt.Errorf("line %d: [%s] effort %q: want one of low, medium, high, xhigh, max, very-high", e.Line, section, e.Value)
				}
				s.Effort = e.Value
			default:
				warnings = append(warnings, fmt.Sprintf("line %d: unknown key %q in [%s] (known: %s)", e.Line, e.Key, section, strings.Join(AgentSettingKeys, ", ")))
			}
		}
		cfg[strings.TrimPrefix(section, AgentsSectionPrefix)] = s
	}
	return cfg, warnings, nil
}

// agentsSectionsIn returns the distinct [agents] and [agents.*] table
// names in content, in file order.
func agentsSectionsIn(content string) []string {
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(sporetoml.StripComment(scanner.Text()))
		if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
			continue
		}
		name := strings.TrimSpace(line[1 : len(line)-1])
		if (name == "agents" || strings.HasPrefix(name, AgentsSectionPrefix)) && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

var (
	agentsWarnings io.Writer = os.Stderr
	warnedAgents   sync.Map
)

func warnAgentsOnce(msg string) {
	if _, dup := warnedAgents.LoadOrStore(msg, true); !dup {
		fmt.Fprint(agentsWarnings, msg)
	}
}

// TaskAgentSettings returns the task frontmatter's `model:` and
// `effort:` override.
func TaskAgentSettings(m frontmatter.Meta) AgentSettings {
	return AgentSettings{Model: m.Extra["model"], Effort: m.Extra["effort"]}
}

// ResolveAgentSettings returns the settings for one pane: the
// [agents.<role>] table (instance overlaid), then the task
// frontmatter on top when withTask is set. Callers pass withTask
// false for a pane whose agent differs from the task's, so a model
// named for one CLI never reaches the other.
func ResolveAgentSettings(cfg AgentsConfig, role, instance string, m frontmatter.Meta, withTask bool) AgentSettings {
	s := cfg.For(role, instance)
	if withTask {
		s = s.overlay(TaskAgentSettings(m))
	}
	return s
}

// ClaudeFlags returns the claude CLI flags for s. Effort aliases
// very-high to xhigh; claude takes low, medium, high, xhigh and max.
func ClaudeFlags(s AgentSettings) []string {
	var out []string
	if s.Model != "" {
		out = append(out, "--model", s.Model)
	}
	if s.Effort != "" {
		out = append(out, "--effort", NormalizeAgentEffort(s.Effort))
	}
	return out
}

// NormalizeAgentEffort folds the very-high aliases to xhigh, the name
// both claude and codex use.
func NormalizeAgentEffort(e string) string {
	if e == "very-high" || e == "very_high" {
		return "xhigh"
	}
	return e
}

// CodexFlags returns the codex CLI flags for s: `-m` and the
// model_reasoning_effort config. max has no codex equivalent and is
// an error.
func CodexFlags(s AgentSettings) ([]string, error) {
	var out []string
	if s.Model != "" {
		out = append(out, "-m", s.Model)
	}
	if s.Effort != "" {
		e, err := codexpolicy.NormalizeEffort(s.Effort, "")
		if err != nil {
			return nil, err
		}
		out = append(out, "-c", codexpolicy.ReasoningConfig(e))
	}
	return out, nil
}

// IsClaudeCommand reports whether the command's argv0 is the claude
// CLI (claude or claude-code), ignoring trailing args.
func IsClaudeCommand(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	base := filepath.Base(fields[0])
	return base == "claude" || base == "claude-code"
}

// FormatAgentSettings renders the resolved agent, model and effort
// for status output; an empty field prints as "default".
func FormatAgentSettings(agent string, s AgentSettings) string {
	orDefault := func(v string) string {
		if v == "" {
			return "default"
		}
		return v
	}
	return fmt.Sprintf("%s model=%s effort=%s", agent, orDefault(s.Model), orDefault(s.Effort))
}
