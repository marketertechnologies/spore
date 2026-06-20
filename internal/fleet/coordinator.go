package fleet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/versality/spore/internal/hooks/inject"
	"github.com/versality/spore/internal/task"
	"github.com/versality/spore/internal/tmuxsess"
)

// coordinatorSpawnSettleDelay is the wait between `tmux new-session -d`
// and the post-spawn `has-session` check. tmux returns success the
// moment its server has registered the session, before the inner shell
// has had a chance to `exec` the agent. A typo in the agent binary
// (e.g. `claude-code` when only `claude` is installed) causes the
// child to die within a few ms, after which the session is gone. The
// settle window catches that case so EnsureCoordinator surfaces a real
// error instead of lying with "spawned".
const coordinatorSpawnSettleDelay = 150 * time.Millisecond

// CoordinatorSlug is the reserved session slug for the singleton
// coordinator agent. Workers cannot use it; the fleet reconciler
// manages this session out-of-band from the per-task queue.
const CoordinatorSlug = "coordinator"

// CoordinatorRoleEnv overrides the role file path the reconciler
// hands to the coordinator session. Empty falls back to
// <projectRoot>/bootstrap/coordinator/role.md.
const CoordinatorRoleEnv = "SPORE_COORDINATOR_ROLE_FILE"

// CoordinatorAgentEnv selects the binary the coordinator session
// execs. Read before SPORE_AGENT_BINARY so operators can run a
// different agent (or a greet-and-shell wrapper) for the singleton
// coordinator without affecting per-task workers.
const CoordinatorAgentEnv = "SPORE_COORDINATOR_AGENT"

// CoordinatorSessionName returns the tmux session for the singleton
// coordinator. Resolves the project name via task.ProjectName so
// invocations from a worktree cwd still target the main repo session.
func CoordinatorSessionName(projectRoot string) string {
	return task.CoordinatorSession(projectRoot)
}

// CoordinatorRolePath returns the override path from
// SPORE_COORDINATOR_ROLE_FILE if set, else the [coordinator].brief
// entry in spore.toml (resolved against projectRoot when relative),
// else the in-tree default at <projectRoot>/bootstrap/coordinator/role.md.
func CoordinatorRolePath(projectRoot string) string {
	if p := os.Getenv(CoordinatorRoleEnv); p != "" {
		return p
	}
	if cfg, err := LoadCoordinatorConfig(projectRoot); err == nil && cfg.Brief != "" {
		if filepath.IsAbs(cfg.Brief) {
			return cfg.Brief
		}
		return filepath.Join(projectRoot, cfg.Brief)
	}
	return filepath.Join(projectRoot, "bootstrap", "coordinator", "role.md")
}

// EnsureCoordinator spawns the coordinator tmux session for projectRoot
// when it is not already alive. Idempotent: a live session is left
// alone. The session runs in projectRoot itself (no worktree) with
// SPORE_TASK_SLUG=coordinator and SPORE_COORDINATOR_ROLE=<path> in the
// session env. The session's command is a small shell snippet that
// passes the role file's contents as the agent's first positional
// arg when the file is readable and non-empty (so a default
// claude-code agent boots with the role as its first user message),
// and falls back to spawning the agent bare otherwise (so test agents
// like `sleep 30` and consumers without a role file installed are
// unaffected). Returns the session name and whether a spawn actually
// happened.
func EnsureCoordinator(projectRoot string) (string, bool, error) {
	session := CoordinatorSessionName(projectRoot)
	if tmuxsess.Has(session) {
		return session, false, nil
	}

	tomlCfg, _ := LoadCoordinatorConfig(projectRoot)
	if pat := tomlCfg.ExternalSessionPattern; pat != "" {
		if name, ok := externalCoordinatorSession(pat); ok {
			return name, false, nil
		}
	}
	agent := coordinatorAgent(tomlCfg)
	rolePath := CoordinatorRolePath(projectRoot)
	project, err := task.ProjectName(projectRoot)
	if err != nil {
		return "", false, err
	}
	inbox, err := task.CoordinatorInboxDirForProject(projectRoot)
	if err != nil {
		return "", false, err
	}
	coordinatorState, err := task.CoordinatorStateDir()
	if err != nil {
		return "", false, err
	}

	if _, _, err := inject.Inject(projectRoot, projectRoot, task.SessionKindCoordinator); err != nil {
		return "", false, fmt.Errorf("inject settings: %w", err)
	}
	if _, _, err := inject.InjectCodex(projectRoot, projectRoot, task.SessionKindCoordinator); err != nil {
		return "", false, fmt.Errorf("inject codex hooks: %w", err)
	}

	cmd := coordinatorShellCommand(agent, rolePath, coordinatorSupervise(tomlCfg))
	args := []string{
		"new-session", "-d",
		"-s", session,
		"-c", projectRoot,
		"-e", "SPORE_TASK_SLUG=" + CoordinatorSlug,
		"-e", "SPORE_COORDINATOR_ROLE=" + rolePath,
		"-e", "SPORE_PROJECT_ROOT=" + projectRoot,
		"-e", "WT_PROJECT=" + project,
		"-e", "SPORE_TASK_INBOX=" + inbox,
		"-e", "SPORE_COORDINATOR_STATE_DIR=" + coordinatorState,
		"-e", task.SessionKindEnv + "=" + task.SessionKindCoordinator,
	}
	if v := coordinatorProvider(tomlCfg); v != "" {
		args = append(args, "-e", "SPORE_COORDINATOR_PROVIDER="+v)
	}
	if v := coordinatorModel(tomlCfg); v != "" {
		args = append(args, "-e", "SPORE_COORDINATOR_MODEL="+v)
	}
	// Thread the operator's account tier into the session env so the
	// supervisor loop's inline derivation and the token-monitor Stop
	// hook agree on the wrap cap. SPORE_ACCOUNT_TIER seeds from
	// WT_ACCOUNT_TIER when the host only sets the latter.
	if v := os.Getenv("WT_ACCOUNT_TIER"); v != "" {
		args = append(args, "-e", "WT_ACCOUNT_TIER="+v)
		if os.Getenv("SPORE_ACCOUNT_TIER") == "" {
			args = append(args, "-e", "SPORE_ACCOUNT_TIER="+v)
		}
	}
	if v := os.Getenv("SPORE_ACCOUNT_TIER"); v != "" {
		args = append(args, "-e", "SPORE_ACCOUNT_TIER="+v)
	}
	// Wrap the shell snippet in `sh -c` so tmux execs the inner agent
	// through a real shell instead of the user's passwd shell. On a
	// deployed host that shell is spore-attach, which only handles
	// `coord` / `pilot` modes and exits on any other `-c` payload, so
	// the inner exec would die before the session settled. Worker
	// spawn does the same wrap for the same reason.
	args = append(args, "sh", "-c", cmd)
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		return "", false, fmt.Errorf("tmux new-session: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// tmux registers the session before the inner shell execs the
	// agent. Wait briefly, then confirm the session survived; an
	// agent binary that fails to exec tears the session down within
	// a few ms.
	time.Sleep(coordinatorSpawnSettleDelay)
	if !tmuxsess.Has(session) {
		return "", false, fmt.Errorf(
			"coordinator session %s died on spawn (agent=%q): the inner exec failed before the session could settle. Check that the agent binary is on PATH",
			session, agent,
		)
	}
	configureCoordinatorTmux(session)
	return session, true, nil
}

// configureCoordinatorTmux applies session-scoped tmux options to a
// freshly spawned coordinator session: a token-usage status line that
// refreshes on a timer, and detach-on-destroy off so that killing the
// driver pane (a supervisor-loop rotation or a manual kill) does not
// detach an attached operator client. All calls are best-effort: a tmux
// that rejects an option must not fail the spawn the caller already
// confirmed is alive.
func configureCoordinatorTmux(session string) {
	self, err := os.Executable()
	if err != nil || self == "" {
		self = "spore"
	}
	statusRight := fmt.Sprintf(
		`#(CLAUDE_PROJECT_DIR='#{pane_current_path}' %s statusline --label coordinator)`,
		self,
	)
	opts := [][]string{
		{"set-option", "-t", session, "detach-on-destroy", "off"},
		{"set-option", "-t", session, "status-interval", "5"},
		{"set-option", "-t", session, "status-right", statusRight},
	}
	for _, o := range opts {
		_ = exec.Command("tmux", o...).Run()
	}
}

// coordinatorAgent picks the binary the coordinator session execs.
// Precedence: SPORE_COORDINATOR_AGENT (lets operators run a different
// agent for the singleton coordinator) > SPORE_AGENT_BINARY (the same
// var workers honour) > spore.toml [coordinator].driver mapped to a
// known binary > "claude" (the kernel default).
func coordinatorAgent(cfg CoordinatorConfig) string {
	if a := os.Getenv(CoordinatorAgentEnv); a != "" {
		return a
	}
	if a := os.Getenv(task.AgentBinaryEnv); a != "" {
		return a
	}
	if a := driverToBinary(cfg.Driver); a != "" {
		return a
	}
	return "claude"
}

// coordinatorProvider returns the provider name for launcher scripts
// that dispatch on $SPORE_COORDINATOR_PROVIDER (e.g. the bundled
// spore-coordinator-launch.sh). Env wins over the spore.toml driver.
// Empty when nothing is configured: the session env stays clean.
func coordinatorProvider(cfg CoordinatorConfig) string {
	if v := os.Getenv("SPORE_COORDINATOR_PROVIDER"); v != "" {
		return v
	}
	return cfg.Driver
}

// coordinatorModel returns the model identifier injected into the
// session env. Env wins over the spore.toml model. Empty when neither
// source set it.
func coordinatorModel(cfg CoordinatorConfig) string {
	if v := os.Getenv("SPORE_COORDINATOR_MODEL"); v != "" {
		return v
	}
	return cfg.Model
}

// coordinatorSupervise reports whether the coordinator session runs its
// driver inside the respawn supervisor loop. Env
// SPORE_COORDINATOR_SUPERVISE (1/true/yes/on vs 0/false/no/off) wins
// over the spore.toml [coordinator].supervise flag. Default off: the
// kernel single-exec lifecycle keeps the spawn settle-check sharp and
// lets a clean driver exit close the session.
func coordinatorSupervise(cfg CoordinatorConfig) bool {
	if v := os.Getenv("SPORE_COORDINATOR_SUPERVISE"); v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return cfg.Supervise
}

// driverToBinary maps a friendly driver name to the binary to exec.
// "claude" -> "claude" (the Anthropic CLI binary name; the package is
// often called "claude-code" but its bin/ entry is "claude"), "codex"
// -> "codex". Unknown values pass through verbatim so a project can
// wire a launcher script by name. Empty input returns empty so the
// caller falls through to the next precedence level.
func driverToBinary(driver string) string {
	switch driver {
	case "":
		return ""
	case "claude":
		return "claude"
	default:
		return driver
	}
}

// coordinatorShellCommand builds the shell snippet tmux runs for the
// coordinator session. tmux invokes its operator shell to parse this
// string; the agent token is intentionally left unquoted so callers
// can pass space-bearing values (e.g. SPORE_AGENT_BINARY="sleep 30")
// the same way worker spawn does.
//
// When supervise is false the snippet execs the driver once and the
// session dies with it (the kernel default; keeps the post-spawn
// settle check able to detect a bad agent binary, and lets a clean
// driver exit tear the session down).
//
// When supervise is true the driver runs inside a respawn loop so a
// token-cap wrap exits only the driver, not the pane: the loop catches
// the exit and boots a fresh driver in place, re-reading the role file
// each iteration, so an attached operator client keeps its pane and
// full scrollback survives the rotation. SPORE_ACCOUNT_TIER is derived
// from WT_ACCOUNT_TIER inline on every iteration so a pane spawned
// before a tier change heals on the next wrap rather than carrying a
// stale (or unset) tier forever -- the loop's bash text is frozen at
// new-session time, so the derivation has to live inside the loop body.
func coordinatorShellCommand(agent, rolePath string, supervise bool) string {
	q := shellSingleQuote(rolePath)
	if !supervise {
		return fmt.Sprintf(
			`if [ -r %[1]s ] && [ -s %[1]s ]; then exec %[2]s "$(cat %[1]s)"; else exec %[2]s; fi`,
			q, agent,
		)
	}
	tier := `SPORE_ACCOUNT_TIER="${SPORE_ACCOUNT_TIER:-${WT_ACCOUNT_TIER:-}}"`
	return fmt.Sprintf(
		`while true; do if [ -r %[1]s ] && [ -s %[1]s ]; then %[3]s %[2]s "$(cat %[1]s)"; else %[3]s %[2]s; fi; sleep 1; done`,
		q, agent, tier,
	)
}

// shellSingleQuote returns s wrapped in single quotes, with embedded
// single quotes escaped, suitable for splicing into a shell-command
// string passed through tmux.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ReapCoordinator kills the coordinator tmux session for projectRoot.
// Idempotent: a missing session is not an error. Returns whether a
// kill was attempted.
func ReapCoordinator(projectRoot string) bool {
	session := CoordinatorSessionName(projectRoot)
	if !tmuxsess.Has(session) {
		return false
	}
	tmuxsess.Kill(session)
	return true
}

// CoordinatorAlive reports whether the coordinator tmux session for
// projectRoot is currently up. A matching external_session_pattern
// counts as alive so the wait poll does not spin against an
// operator-managed coordinator at a non-kernel session name.
func CoordinatorAlive(projectRoot string) bool {
	if tmuxsess.Has(CoordinatorSessionName(projectRoot)) {
		return true
	}
	if cfg, err := LoadCoordinatorConfig(projectRoot); err == nil && cfg.ExternalSessionPattern != "" {
		if _, ok := externalCoordinatorSession(cfg.ExternalSessionPattern); ok {
			return true
		}
	}
	return false
}

// externalCoordinatorSession returns the first tmux session name matching
// pattern (RE2). Used by EnsureCoordinator and CoordinatorAlive to defer
// to an operator-side coordinator running under a non-kernel session
// name. A malformed regex or an unreachable tmux server (no server
// running, exec failure) returns no match so the caller falls back to
// kernel-managed lifecycle instead of failing the reconcile pass.
func externalCoordinatorSession(pattern string) (string, bool) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", false
	}
	names, err := tmuxsess.List()
	if err != nil {
		return "", false
	}
	for _, name := range names {
		if re.MatchString(name) {
			return name, true
		}
	}
	return "", false
}
