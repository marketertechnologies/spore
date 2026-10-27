package fleet

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/versality/spore/internal/sporetoml"
	"github.com/versality/spore/internal/task"
)

// CoordinatorConfig captures the [coordinator] section from a project's
// spore.toml, with driver and model possibly replaced by the host-side
// override a swap writes. Empty fields fall through to env-var overrides and the
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

// LoadCoordinatorConfig reads `[coordinator]` from <projectRoot>/spore.toml,
// then layers the host-side driver override (see DriverOverridePath) on
// top. A missing file returns a zero CoordinatorConfig with no error so
// callers can treat absent config as "use defaults". On an override
// error the spore.toml config is still returned alongside it.
func LoadCoordinatorConfig(projectRoot string) (CoordinatorConfig, error) {
	entries, err := sporetoml.ReadSection(projectRoot, "coordinator")
	if err != nil {
		return CoordinatorConfig{}, fmt.Errorf("coordinator: %w", err)
	}
	cfg, err := coordinatorFromEntries(entries)
	if err != nil {
		return CoordinatorConfig{}, err
	}
	path, err := DriverOverridePath(projectRoot)
	if err != nil {
		return cfg, nil
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("coordinator: read %s: %w", path, err)
	}
	ov, err := parseDriverOverride(string(b))
	if err != nil {
		return cfg, fmt.Errorf("coordinator: %s: %w", path, err)
	}
	return applyDriverOverride(cfg, ov), nil
}

// DriverOverridePath is the host-side file `spore coordinator swap`
// writes: <StateDirForProject>/coordinator/driver.toml. It lives outside
// the project tree so a swap never touches a consumer repo.
func DriverOverridePath(projectRoot string) (string, error) {
	dir, err := task.StateDirForProject(projectRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, CoordinatorSlug, "driver.toml"), nil
}

// parseDriverOverride reads [coordinator] from the override file. Only
// driver and model are allowed: brief and the session pattern stay
// project config.
func parseDriverOverride(content string) (CoordinatorConfig, error) {
	entries, err := sporetoml.ParseSection(content, "coordinator")
	if err != nil {
		return CoordinatorConfig{}, err
	}
	var ov CoordinatorConfig
	for _, e := range entries {
		switch e.Key {
		case "driver":
			ov.Driver = e.Value
		case "model":
			ov.Model = e.Value
		default:
			return CoordinatorConfig{}, fmt.Errorf("line %d: key %q not allowed in the driver override, only driver and model", e.Line, e.Key)
		}
	}
	return ov, nil
}

// applyDriverOverride replaces driver and model together: an override
// driver without a model means the agent's default, as after a swap,
// so a spore.toml model for the other provider must not leak through.
func applyDriverOverride(cfg, ov CoordinatorConfig) CoordinatorConfig {
	if ov.Driver != "" {
		cfg.Driver, cfg.Model = ov.Driver, ov.Model
	}
	return cfg
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

// driverPlan is a pending rewrite of the host-side driver override,
// holding the old bytes so a failed swap can undo it. cfg is the
// effective config once the override lands.
type driverPlan struct {
	path         string
	old, content string
	existed      bool
	cfg          CoordinatorConfig
}

// planCoordinatorDriver computes the driver override with driver and
// model set in [coordinator]. An empty model deletes the key. spore.toml
// is read for the effective config but never written; nothing is.
func planCoordinatorDriver(projectRoot, driver, model string) (driverPlan, error) {
	var p driverPlan
	entries, err := sporetoml.ReadSection(projectRoot, "coordinator")
	if err != nil {
		return p, fmt.Errorf("coordinator: %w", err)
	}
	base, err := coordinatorFromEntries(entries)
	if err != nil {
		return p, fmt.Errorf("coordinator: spore.toml: %w", err)
	}
	if p.path, err = DriverOverridePath(projectRoot); err != nil {
		return p, fmt.Errorf("coordinator: %w", err)
	}
	b, err := os.ReadFile(p.path)
	switch {
	case err == nil:
		p.existed = true
	case !os.IsNotExist(err):
		return p, fmt.Errorf("coordinator: read %s: %w", p.path, err)
	}
	p.old = string(b)
	p.content = sporetoml.SetKey(p.old, "coordinator", "driver", driver)
	if model == "" {
		p.content = sporetoml.DeleteKey(p.content, "coordinator", "model")
	} else {
		p.content = sporetoml.SetKey(p.content, "coordinator", "model", model)
	}
	ov, err := parseDriverOverride(p.content)
	if err != nil {
		return p, fmt.Errorf("coordinator: %s: %w", p.path, err)
	}
	p.cfg = applyDriverOverride(base, ov)
	return p, nil
}

func (p driverPlan) write() error {
	if err := writePrivateFile(p.path, p.content); err != nil {
		return fmt.Errorf("coordinator: write %s: %w", p.path, err)
	}
	return nil
}

func (p driverPlan) rollback() error {
	if !p.existed {
		return os.Remove(p.path)
	}
	return writePrivateFile(p.path, p.old)
}

// writePrivateFile replaces path via a temp file and rename, mode 0600
// in a 0700 directory.
func writePrivateFile(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
