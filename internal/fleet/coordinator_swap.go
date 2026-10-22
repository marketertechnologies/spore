package fleet

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/versality/spore/internal/task"
)

// Seams for the swap. Tests swap them to record the tmux sequence and
// drive state.md without a live server or agent.
var (
	swapTmux = func(args ...string) ([]byte, error) {
		out, err := exec.Command("tmux", args...).CombinedOutput()
		if err != nil {
			return out, fmt.Errorf("tmux %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return out, nil
	}
	swapSleep = time.Sleep
	swapNow   = time.Now
)

// ErrWrapTimeout is returned when state.md did not advance before the
// wrap deadline and the caller did not force the swap.
var ErrWrapTimeout = errors.New("coordinator swap: state.md not flushed before the wrap timeout")

// SwapSpec describes one coordinator swap. Session is the tmux session
// that owns the coordinator pane; everything else is read from its
// env so a swap from an operator shell targets the same paths the
// pane was spawned with.
type SwapSpec struct {
	Session string
	// To is the new driver: "claude" or "codex".
	To string
	// Model is the new model when ModelSet; see SwapModel.
	Model    string
	ModelSet bool
	// Wrap asks the live coordinator to flush state.md first.
	Wrap bool
	// Force proceeds after a wrap timeout instead of aborting.
	Force       bool
	WrapTimeout time.Duration
	// WrapSettle is how long state.md must stay unchanged after its
	// first write before the pane turns over, so a multi-edit wrap
	// finishes.
	WrapSettle time.Duration
	Poll       time.Duration
	// Log receives progress lines; nil discards them.
	Log func(string)
}

// SwapResult reports what the swap did.
type SwapResult struct {
	ProjectRoot string
	Pane        string
	Wrapped     bool
	Config      CoordinatorConfig
}

// SwapModel resolves the model to write: an explicit model wins; on a
// driver change an omitted model clears it (ids do not carry across
// providers); on the same driver it keeps the current one.
func SwapModel(cur CoordinatorConfig, to, model string, modelSet bool) string {
	if modelSet {
		return model
	}
	if cur.Driver != to {
		return ""
	}
	return cur.Model
}

// runningConfig is the driver and model the pane was launched with:
// the session env first (it beats spore.toml at spawn), then spore.toml,
// then the kernel default driver.
func runningConfig(root string, env map[string]string) CoordinatorConfig {
	cfg, _ := LoadCoordinatorConfig(root)
	if v := env["SPORE_COORDINATOR_PROVIDER"]; v != "" {
		cfg.Driver = v
	}
	if v, ok := env["SPORE_COORDINATOR_MODEL"]; ok {
		cfg.Model = v
	}
	if cfg.Driver == "" {
		cfg.Driver = "claude"
	}
	return cfg
}

// SwapCoordinator flushes, flips spore.toml and respawns the
// coordinator pane of spec.Session under the new driver. Called from
// inside that pane it does not return on success: respawn-pane kills
// the caller with the pane.
func SwapCoordinator(spec SwapSpec) (SwapResult, error) {
	logf := func(format string, a ...any) {
		if spec.Log != nil {
			spec.Log(fmt.Sprintf(format, a...))
		}
	}
	if spec.To != "claude" && spec.To != "codex" {
		return SwapResult{}, fmt.Errorf("coordinator swap: --to must be claude or codex, got %q", spec.To)
	}
	env, err := sessionEnv(spec.Session)
	if err != nil {
		return SwapResult{}, fmt.Errorf("coordinator swap: session %s: %w", spec.Session, err)
	}
	root := env["SPORE_PROJECT_ROOT"]
	if root == "" {
		return SwapResult{}, fmt.Errorf("coordinator swap: session %s has no SPORE_PROJECT_ROOT in its env", spec.Session)
	}
	pane, err := coordinatorPane(spec.Session)
	if err != nil {
		return SwapResult{}, err
	}
	res := SwapResult{ProjectRoot: root, Pane: pane}

	if spec.Wrap {
		if insideSession(spec.Session) {
			return res, fmt.Errorf("coordinator swap: running inside %s, which cannot answer its own wrap request; flush state.md, then rerun with --no-wrap", spec.Session)
		}
		inbox := env["SPORE_TASK_INBOX"]
		if inbox == "" {
			if inbox, err = task.CoordinatorInboxDirForProject(root); err != nil {
				return res, err
			}
		}
		stateFile := filepath.Join(root, "state.md")
		logf("coordinator swap: asking %s to flush %s", spec.Session, stateFile)
		ok, err := requestWrap(inbox, stateFile, spec)
		if err != nil {
			return res, err
		}
		switch {
		case ok:
			res.Wrapped = true
		case spec.Force:
			logf("coordinator swap: state.md did not advance within %s; --force, swapping anyway", spec.WrapTimeout)
		default:
			return res, ErrWrapTimeout
		}
	}

	model := SwapModel(runningConfig(root, env), spec.To, spec.Model, spec.ModelSet)
	cfg, err := SetCoordinatorDriver(root, spec.To, model)
	if err != nil {
		return res, err
	}
	res.Config = cfg
	logf("coordinator swap: %s driver=%s model=%s", filepath.Join(root, "spore.toml"), cfg.Driver, displayModel(cfg.Model))

	rolePath := env["SPORE_COORDINATOR_ROLE"]
	if rolePath == "" {
		rolePath = CoordinatorRolePath(root)
	}
	logf("coordinator swap: respawning %s (%s)", spec.Session, pane)
	for _, args := range swapCommands(spec.Session, pane, root, cfg, coordinatorAgent(cfg), rolePath) {
		if _, err := swapTmux(args...); err != nil {
			return res, fmt.Errorf("coordinator swap: %w", err)
		}
	}
	return res, nil
}

// swapCommands is the tmux sequence that turns the pane over. The
// session env is authoritative: respawn-pane builds the new process
// env from the global env plus the session env, so provider and model
// go there, and a cleared model is marked -r so a value inherited by
// the tmux server (e.g. from /etc/spore/coordinator.env) cannot leak
// back in.
func swapCommands(session, pane, root string, cfg CoordinatorConfig, agent, rolePath string) [][]string {
	model := []string{"set-environment", "-t", session, "-r", "SPORE_COORDINATOR_MODEL"}
	if cfg.Model != "" {
		model = []string{"set-environment", "-t", session, "SPORE_COORDINATOR_MODEL", cfg.Model}
	}
	return [][]string{
		{"set-environment", "-t", session, "SPORE_COORDINATOR_PROVIDER", cfg.Driver},
		model,
		{"respawn-pane", "-k", "-t", pane, "-c", root, coordinatorShellCommand(agent, rolePath)},
	}
}

// requestWrap drops a wrap-up envelope into the coordinator inbox and
// waits for stateFile's mtime to advance and then settle. A Claude
// coordinator picks it up through its inbox hook, a Codex one through
// the watch-inbox sidecar. Returns false when state.md never advanced
// before the deadline; a write that is still settling counts.
func requestWrap(inbox, stateFile string, spec SwapSpec) (bool, error) {
	before := fileMtime(stateFile)
	msg := fmt.Sprintf("spore coordinator swap: this pane is about to be respawned as a %s coordinator. "+
		"Wrap up now: flush everything the next coordinator needs into %s (in-flight work, open decisions, next steps), "+
		"then end your turn. Do not start new work; the pane turns over once state.md is written.",
		spec.To, stateFile)
	if err := task.TellDir(inbox, CoordinatorSlug, msg); err != nil {
		return false, fmt.Errorf("coordinator swap: write wrap request to %s: %w", inbox, err)
	}
	poll := spec.Poll
	if poll <= 0 {
		poll = 2 * time.Second
	}
	deadline := swapNow().Add(spec.WrapTimeout)
	var seen time.Time
	var stableSince time.Time
	for {
		m := fileMtime(stateFile)
		if m.After(before) {
			if !m.Equal(seen) {
				seen, stableSince = m, swapNow()
			}
			if swapNow().Sub(stableSince) >= spec.WrapSettle {
				return true, nil
			}
		}
		if !swapNow().Before(deadline) {
			return !seen.IsZero(), nil
		}
		swapSleep(poll)
	}
}

func fileMtime(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// sessionEnv reads the session's own environment table. Removed
// entries ("-VAR") are skipped.
func sessionEnv(session string) (map[string]string, error) {
	out, err := swapTmux("show-environment", "-t", session)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(k, "-") {
			env[k] = v
		}
	}
	return env, nil
}

// coordinatorPane returns the id of the session's first pane: the one
// new-session started the agent in. Ids survive window renumbering and
// base-index settings that a "<session>:0" target would not.
func coordinatorPane(session string) (string, error) {
	out, err := swapTmux("list-panes", "-s", "-t", session, "-F", "#{pane_id}")
	if err != nil {
		return "", fmt.Errorf("coordinator swap: %w", err)
	}
	pane, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if pane == "" {
		return "", fmt.Errorf("coordinator swap: session %s has no panes", session)
	}
	return pane, nil
}

func insideSession(session string) bool {
	pane := os.Getenv("TMUX_PANE")
	if os.Getenv("TMUX") == "" || pane == "" {
		return false
	}
	out, err := swapTmux("display-message", "-p", "-t", pane, "#{session_name}")
	return err == nil && strings.TrimSpace(string(out)) == session
}

func displayModel(m string) string {
	if m == "" {
		return "(cli default)"
	}
	return m
}
