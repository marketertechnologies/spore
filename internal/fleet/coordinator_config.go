package fleet

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/versality/spore/internal/sporetoml"
)

// CoordinatorConfig captures the [coordinator] section from a project's
// spore.toml. Empty fields fall through to env-var overrides and the
// kernel defaults; nothing in this struct overrides an explicit env.
type CoordinatorConfig struct {
	// Driver is the agent provider ("claude", "codex", or a binary
	// path). When set without an explicit SPORE_COORDINATOR_AGENT it
	// supplies the binary the coordinator session execs and seeds
	// SPORE_COORDINATOR_PROVIDER for launcher scripts that dispatch
	// on provider name.
	Driver string

	// Model is the model identifier passed through to the agent
	// (claude --model <m>, codex -m <m>). Surfaced as
	// SPORE_COORDINATOR_MODEL in the session env.
	Model string

	// Brief is the role-file path. Mirrors SPORE_COORDINATOR_ROLE_FILE;
	// relative paths resolve against projectRoot.
	Brief string

	// ExternalSessionPattern is an RE2 regex matched against tmux
	// session names. When set and a live session matches, EnsureCoordinator
	// treats the coordinator role as externally provided and skips the
	// kernel spawn. Use this when an operator-side process owns the
	// coordinator under a non-spore session name (for example a helm-*
	// session running outside the kernel's spore/<project>/coordinator
	// slot). Empty disables the check and the kernel spawns its own.
	ExternalSessionPattern string
}

// LoadCoordinatorConfig reads `[coordinator]` from <projectRoot>/spore.toml.
// A missing file returns a zero CoordinatorConfig with no error so callers
// can treat absent config as "use defaults".
func LoadCoordinatorConfig(projectRoot string) (CoordinatorConfig, error) {
	entries, err := sporetoml.ReadSection(projectRoot, "coordinator")
	if err != nil {
		return CoordinatorConfig{}, fmt.Errorf("coordinator: %w", err)
	}
	return coordinatorFromEntries(entries)
}

// parseCoordinatorTOML reads only the [coordinator] section. Unknown
// keys are an error so misconfigured spore.toml surfaces loudly.
func parseCoordinatorTOML(content string) (CoordinatorConfig, error) {
	entries, err := sporetoml.ParseSection(content, "coordinator")
	if err != nil {
		return CoordinatorConfig{}, err
	}
	return coordinatorFromEntries(entries)
}

func coordinatorFromEntries(entries []sporetoml.Entry) (CoordinatorConfig, error) {
	var cfg CoordinatorConfig
	for _, e := range entries {
		switch e.Key {
		case "driver":
			cfg.Driver = e.Value
		case "model":
			cfg.Model = e.Value
		case "brief":
			cfg.Brief = e.Value
		case "external_session_pattern":
			cfg.ExternalSessionPattern = e.Value
		default:
			return CoordinatorConfig{}, fmt.Errorf("line %d: unknown key %q in [coordinator]", e.Line, e.Key)
		}
	}
	return cfg, nil
}

// SetCoordinatorDriver writes driver and model into [coordinator] of
// <projectRoot>/spore.toml, keeping comments and other keys. An empty
// model deletes the key. Returns the config as re-read from disk.
func SetCoordinatorDriver(projectRoot, driver, model string) (CoordinatorConfig, error) {
	path := filepath.Join(projectRoot, "spore.toml")
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return CoordinatorConfig{}, fmt.Errorf("coordinator: read %s: %w", path, err)
	}
	content := sporetoml.SetKey(string(b), "coordinator", "driver", driver)
	if model == "" {
		content = sporetoml.DeleteKey(content, "coordinator", "model")
	} else {
		content = sporetoml.SetKey(content, "coordinator", "model", model)
	}
	if _, err := parseCoordinatorTOML(content); err != nil {
		return CoordinatorConfig{}, fmt.Errorf("coordinator: %s: %w", path, err)
	}
	if err := sporetoml.WriteFile(projectRoot, content); err != nil {
		return CoordinatorConfig{}, fmt.Errorf("coordinator: write %s: %w", path, err)
	}
	return LoadCoordinatorConfig(projectRoot)
}
