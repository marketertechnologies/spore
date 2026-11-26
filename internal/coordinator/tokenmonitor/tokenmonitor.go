// Package tokenmonitor wraps the budget short-window token context
// into a stop-hook shape for the coordinator. It reads the hook
// payload (session_id + transcript_path), sums input tokens from the
// latest assistant message's usage block, and fires reminders when
// thresholds are crossed. Three bands: soft (once per session, wrap
// at the next natural break), hard (every Stop, finish the in-flight
// unit then wrap; start nothing new), force (every Stop, wrap
// immediately regardless of what is in flight).
package tokenmonitor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/versality/spore/internal/events"
	"github.com/versality/spore/internal/transcript"
)

const (
	DefaultSoftCap = 250000
	DefaultHardCap = 290000

	// DefaultForceCap is the wrap ceiling on a 1M-context session; past it
	// the monitor demands an immediate wrap even mid-unit.
	DefaultForceCap = 300000

	// defaultRespawn is the pane command when Config.Respawn is empty:
	// the kernel's default coordinator agent.
	defaultRespawn = "exec claude --dangerously-skip-permissions"
)

type Config struct {
	SoftCap    int
	HardCap    int
	ForceCap   int
	StateDir   string
	LedgerFile string
	Inbox      string
	// Respawn is the shell command the respawned pane runs: the same
	// launch command the coordinator was spawned with.
	Respawn string
}

// RespawnCommand is the tmux command surfaced to the coordinator agent
// at each cap. respawn-pane -k re-execs the pane without tearing down
// the session, so an SSH-attached operator stays connected while the
// agent process is replaced. The session-name expression resolves to
// the current session, so an external_session_pattern coordinator
// (running under a non-spore session name) respawns itself in place.
func (c Config) RespawnCommand() string {
	return `tmux respawn-pane -k -t "$(tmux display-message -p '#S'):0" ` + shellSingleQuote(c.Respawn)
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type CheckResult struct {
	Ctx        int    `json:"ctx"`
	SoftCap    int    `json:"soft_cap"`
	HardCap    int    `json:"hard_cap"`
	ForceCap   int    `json:"force_cap"`
	Level      string `json:"level"`
	Message    string `json:"message,omitempty"`
	ShouldFire bool   `json:"should_fire"`
}

type HookPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
}

func (c Config) Defaults() Config {
	if c.SoftCap <= 0 {
		c.SoftCap = DefaultSoftCap
	}
	if c.HardCap <= 0 {
		c.HardCap = DefaultHardCap
	}
	if c.ForceCap <= 0 {
		c.ForceCap = DefaultForceCap
	}
	if c.StateDir == "" {
		c.StateDir = defaultStateDir()
	}
	if c.LedgerFile == "" {
		c.LedgerFile = filepath.Join(c.StateDir, "token-monitor.jsonl")
	}
	if c.Respawn == "" {
		c.Respawn = defaultRespawn
	}
	return c
}

// defaultStateDir resolves the coordinator state dir from the
// SPORE_COORDINATOR_STATE_DIR env var, falling back to
// <XDG_STATE_HOME or ~/.local/state>/spore/coordinator.
func defaultStateDir() string {
	if d := os.Getenv("SPORE_COORDINATOR_STATE_DIR"); d != "" {
		return d
	}
	base, _ := events.BaseDir()
	return filepath.Join(base, "coordinator")
}

// IsCoordinator returns true if the inbox path is under the state
// dir, matching the self-test logic from the bash version.
func (c Config) IsCoordinator() bool {
	if c.Inbox == "" {
		return false
	}
	stateRoot := strings.TrimRight(c.StateDir, "/")
	return c.Inbox == stateRoot || strings.HasPrefix(c.Inbox, stateRoot+"/")
}

// Check reads the transcript, sums context tokens, and determines
// whether a soft or hard cap has been crossed. The sessionID is used
// for the soft-marker (fire only once per session at soft cap).
func Check(cfg Config, payload HookPayload) CheckResult {
	cfg = cfg.Defaults()

	if !cfg.IsCoordinator() {
		return CheckResult{Level: "skip"}
	}

	tpath := payload.TranscriptPath
	if tpath == "" || !fileExists(tpath) {
		tpath = transcript.FindFallbackTranscript()
	}
	if tpath == "" {
		return CheckResult{Level: "skip"}
	}

	ctx := transcript.SumContextTokens(tpath)
	if ctx <= 0 {
		return CheckResult{Level: "ok", Ctx: ctx, SoftCap: cfg.SoftCap, HardCap: cfg.HardCap}
	}

	markerDir := filepath.Join(cfg.StateDir, "token-monitor")
	os.MkdirAll(markerDir, 0o700)
	sid := payload.SessionID
	if sid == "" {
		sid = "unknown"
	}
	softMarker := filepath.Join(markerDir, sid+".soft")

	result := CheckResult{
		Ctx:      ctx,
		SoftCap:  cfg.SoftCap,
		HardCap:  cfg.HardCap,
		ForceCap: cfg.ForceCap,
	}

	if ctx >= cfg.ForceCap {
		result.Level = "force"
		result.ShouldFire = true
		result.Message = fmt.Sprintf(
			"COORDINATOR TOKEN MONITOR (force): context %d tokens >= force cap %d.\n"+
				"Quality degrades from here. Wrap up IMMEDIATELY, regardless of\n"+
				"what is in flight:\n"+
				"  1. Flush state.md now; note where the in-flight unit stopped.\n"+
				"  2. Post a one-line summary to the operator if anything is still open.\n"+
				"  3. Run: %s\n"+
				"respawn-pane preserves the tmux session and any attached SSH client;\n"+
				"only the agent process is replaced. The reconciler is not involved.",
			ctx, cfg.ForceCap, cfg.RespawnCommand())
		appendLedger(cfg, sid, ctx, false, false, true)
		return result
	}

	if ctx >= cfg.HardCap {
		result.Level = "hard"
		result.ShouldFire = true
		result.Message = fmt.Sprintf(
			"COORDINATOR TOKEN MONITOR (finish-unit): context %d tokens >= finish-unit cap %d.\n"+
				"Do not start new investigations or new units of work.\n"+
				"Finish the unit currently in flight, then wrap:\n"+
				"  1. Flush state.md so the next coordinator boots from it.\n"+
				"  2. Post a one-line summary to the operator if anything is still open.\n"+
				"  3. Run: %s\n"+
				"respawn-pane preserves the tmux session and any attached SSH client;\n"+
				"only the agent process is replaced. The reconciler is not involved.\n"+
				"This reminder fires on every Stop past the cap. Force cap is %d;\n"+
				"crossing it demands an immediate wrap even mid-unit.",
			ctx, cfg.HardCap, cfg.RespawnCommand(), cfg.ForceCap)
		appendLedger(cfg, sid, ctx, false, true, false)
		return result
	}

	if ctx >= cfg.SoftCap && !fileExists(softMarker) {
		touch(softMarker)
		result.Level = "soft"
		result.ShouldFire = true
		result.Message = fmt.Sprintf(
			"COORDINATOR TOKEN MONITOR (soft): context %d tokens >= soft warn %d.\n"+
				"Wrap up at the next natural break: flush state.md, then run\n"+
				"  %s\n"+
				"Finish-unit cap is %d (past it, finish the in-flight unit and wrap;\n"+
				"a reminder fires on every Stop). Force cap is %d (immediate wrap).",
			ctx, cfg.SoftCap, cfg.RespawnCommand(), cfg.HardCap, cfg.ForceCap)
		appendLedger(cfg, sid, ctx, true, false, false)
		return result
	}

	result.Level = "ok"
	appendLedger(cfg, sid, ctx, false, false, false)
	return result
}

// appendLedger writes one row per check. force_cap / force_fired were
// added after the first ledger shape shipped; readers must tolerate
// rows without them (LedgerVerdict does: missing bools decode false).
func appendLedger(cfg Config, sessionID string, ctx int, softFired, hardFired, forceFired bool) {
	os.MkdirAll(filepath.Dir(cfg.LedgerFile), 0o700)
	f, err := os.OpenFile(cfg.LedgerFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().UTC().Format(time.RFC3339)
	fmt.Fprintf(f, `{"ts":"%s","session_id":"%s","ctx":%d,"soft_cap":%d,"hard_cap":%d,"force_cap":%d,"soft_fired":%s,"hard_fired":%s,"force_fired":%s}`+"\n",
		ts, sessionID, ctx, cfg.SoftCap, cfg.HardCap, cfg.ForceCap,
		strconv.FormatBool(softFired), strconv.FormatBool(hardFired),
		strconv.FormatBool(forceFired))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func touch(path string) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err == nil {
		f.Close()
	}
}
