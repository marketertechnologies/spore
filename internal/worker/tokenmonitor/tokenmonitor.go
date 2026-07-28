// Package tokenmonitor is the worker-side claude-code Stop-hook helper
// that watches a worker's context budget. It reads the hook payload on
// stdin (session_id + transcript_path), parses the latest assistant
// message's usage block from the transcript, and on threshold crossing
// fires a wrap-up reminder so the worker can flush progress to its
// tasks/<slug>.md and let the fleet reconciler resume it.
//
// Thresholds are tier-keyed and form three bands. Soft (wrap cap minus
// 30k: 150k max / 90k sub) fires once per session: wrap at the next
// natural break. Wrap (180k max / 120k sub) fires on every Stop:
// finish the unit in flight, start nothing new, then flush and
// self-kill. Force (195k max / 140k sub, under the 150k API hard block
// on sub) fires on every Stop: wrap immediately regardless of what is
// in flight. Tier defaults to non-max so a session with an unknown
// tier wraps at the safer caps.
//
// The once-per-session soft marker lives next to the inbox, at
// <state>/<slug>/token-monitor/<session_id>.soft; the worker monitor
// has no state dir of its own.
//
// The worker monitor skips any session whose inbox is under the
// coordinator state dir (those are owned by the coordinator monitor)
// and any session with no inbox set.
package tokenmonitor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/versality/spore/internal/transcript"
)

const (
	DefaultWrapMax  = 180000
	DefaultWrapSub  = 120000
	DefaultForceMax = 195000
	// DefaultForceSub stays under the 150k API hard block on sub-max
	// accounts.
	DefaultForceSub = 140000
	// softGap is subtracted from the wrap cap to place the one-time
	// early warning (150k on max, 90k on sub).
	softGap = 30000

	// workerKillCommand is the self-kill surfaced in every firing
	// message; the fleet reconciler respawns the worker on its next
	// pass.
	workerKillCommand = `tmux kill-session -t "$(tmux display-message -p '#S')"`
)

type Config struct {
	WrapMax             int
	WrapSub             int
	WrapOverride        int
	ForceMax            int
	ForceSub            int
	ForceOverride       int
	Tier                string
	Inbox               string
	CoordinatorStateDir string
}

type CheckResult struct {
	Ctx        int    `json:"ctx"`
	SoftCap    int    `json:"soft_cap"`
	WrapCap    int    `json:"wrap_cap"`
	ForceCap   int    `json:"force_cap"`
	Tier       string `json:"tier"`
	Slug       string `json:"slug,omitempty"`
	Level      string `json:"level"`
	Message    string `json:"message,omitempty"`
	ShouldFire bool   `json:"should_fire"`
}

type HookPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
}

func (c Config) Defaults() Config {
	if c.WrapMax <= 0 {
		c.WrapMax = DefaultWrapMax
	}
	if c.WrapSub <= 0 {
		c.WrapSub = DefaultWrapSub
	}
	if c.ForceMax <= 0 {
		c.ForceMax = DefaultForceMax
	}
	if c.ForceSub <= 0 {
		c.ForceSub = DefaultForceSub
	}
	if c.CoordinatorStateDir == "" {
		c.CoordinatorStateDir = defaultCoordinatorStateDir()
	}
	return c
}

func defaultCoordinatorStateDir() string {
	if d := os.Getenv("SPORE_COORDINATOR_STATE_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "spore", "coordinator")
}

// IsCoordinator returns true if the inbox is under the coordinator
// state dir; the coordinator token monitor handles those.
func (c Config) IsCoordinator() bool {
	if c.Inbox == "" {
		return false
	}
	stateRoot := strings.TrimRight(c.CoordinatorStateDir, "/")
	if stateRoot == "" {
		return false
	}
	return c.Inbox == stateRoot || strings.HasPrefix(c.Inbox, stateRoot+"/")
}

// WrapCap returns the effective wrap threshold given the configured
// override / tier. WrapOverride > 0 wins for any tier (test/debug);
// otherwise tier=="max" picks WrapMax; everything else picks WrapSub.
func (c Config) WrapCap() int {
	if c.WrapOverride > 0 {
		return c.WrapOverride
	}
	if c.Tier == "max" {
		return c.WrapMax
	}
	return c.WrapSub
}

// ForceCap returns the effective force threshold, same override / tier
// precedence as WrapCap.
func (c Config) ForceCap() int {
	if c.ForceOverride > 0 {
		return c.ForceOverride
	}
	if c.Tier == "max" {
		return c.ForceMax
	}
	return c.ForceSub
}

// SoftCap returns the one-time early-warning threshold, softGap under
// the wrap cap. A non-positive result (tiny WrapOverride) disables the
// soft band.
func (c Config) SoftCap() int {
	return c.WrapCap() - softGap
}

// Slug returns the worker slug parsed from the inbox layout
// <state>/<slug>/inbox. Returns "" when the layout doesn't match.
func (c Config) Slug() string {
	if c.Inbox == "" {
		return ""
	}
	parent := filepath.Base(filepath.Dir(c.Inbox))
	if parent == "." || parent == "/" || parent == "" {
		return ""
	}
	return parent
}

// Check reads the transcript, sums context tokens, and decides which
// band the worker is in. Wrap and force fire on every Stop past their
// caps; soft fires once per session via a marker file next to the
// inbox. Message is the reminder for the worker to flush progress to
// tasks/<slug>.md and self-kill so the reconciler can respawn it.
func Check(cfg Config, payload HookPayload) CheckResult {
	cfg = cfg.Defaults()

	if cfg.Inbox == "" || cfg.IsCoordinator() {
		return CheckResult{Level: "skip"}
	}

	slug := cfg.Slug()
	if slug == "" {
		return CheckResult{Level: "skip"}
	}

	tpath := payload.TranscriptPath
	if tpath == "" || !fileExists(tpath) {
		tpath = transcript.FindFallbackTranscript()
	}
	if tpath == "" {
		return CheckResult{Level: "skip", Slug: slug}
	}

	wrap := cfg.WrapCap()
	force := cfg.ForceCap()
	soft := cfg.SoftCap()
	ctx := transcript.SumContextTokens(tpath)
	result := CheckResult{
		Ctx:      ctx,
		SoftCap:  soft,
		WrapCap:  wrap,
		ForceCap: force,
		Tier:     cfg.Tier,
		Slug:     slug,
	}
	if ctx <= 0 {
		result.Level = "ok"
		return result
	}

	if ctx >= force {
		result.Level = "force"
		result.ShouldFire = true
		result.Message = fmt.Sprintf(
			"WORKER TOKEN MONITOR (force): context %d tokens >= force cap %d on tier=%s.\n"+
				"Wrap up IMMEDIATELY, regardless of what is in flight:\n"+
				"  1. Flush progress to tasks/%s.md now; note where the in-flight unit stopped.\n"+
				"  2. If you have a blocker for the coordinator, send it before exiting.\n"+
				"  3. Run: %s\n"+
				"The fleet reconciler resumes a fresh worker with the same worktree on the next pass.",
			ctx, force, normTier(cfg.Tier), slug, workerKillCommand)
		return result
	}

	if ctx >= wrap {
		result.Level = "wrap"
		result.ShouldFire = true
		var reason string
		if cfg.Tier == "max" {
			reason = "Quality degrades past 200k on max."
		} else {
			reason = "Sub-max account; the 150k hard block is close."
		}
		result.Message = fmt.Sprintf(
			"WORKER TOKEN MONITOR (finish-unit): context %d tokens >= finish-unit cap %d on tier=%s.\n"+
				"%s Do not start new investigations or new units of work.\n"+
				"Finish the unit currently in flight, then wrap:\n"+
				"  1. Flush in-flight progress to tasks/%s.md so the next worker boots from it.\n"+
				"  2. If you have a blocker for the coordinator, send it before exiting.\n"+
				"  3. Run: %s\n"+
				"The fleet reconciler resumes a fresh worker with the same worktree on the next pass.\n"+
				"This reminder fires on every Stop past the cap. Force cap is %d;\n"+
				"crossing it demands an immediate wrap even mid-unit.",
			ctx, wrap, normTier(cfg.Tier), reason, slug, workerKillCommand, force)
		return result
	}

	if soft > 0 && ctx >= soft {
		softMarker := softMarkerPath(cfg.Inbox, payload.SessionID)
		if !fileExists(softMarker) {
			touch(softMarker)
			result.Level = "soft"
			result.ShouldFire = true
			result.Message = fmt.Sprintf(
				"WORKER TOKEN MONITOR (soft): context %d tokens >= soft warn %d on tier=%s.\n"+
					"Wrap up at the next natural break: flush progress to tasks/%s.md, then run\n"+
					"  %s\n"+
					"Finish-unit cap %d and force cap %d are approaching; past the\n"+
					"finish-unit cap a reminder fires on every Stop.",
				ctx, soft, normTier(cfg.Tier), slug, workerKillCommand, wrap, force)
			return result
		}
	}

	result.Level = "ok"
	return result
}

// softMarkerPath places the once-per-session marker under the slug's
// state dir next to the inbox: <state>/<slug>/token-monitor/<sid>.soft.
func softMarkerPath(inbox, sessionID string) string {
	if sessionID == "" {
		sessionID = "unknown"
	}
	markerDir := filepath.Join(filepath.Dir(inbox), "token-monitor")
	os.MkdirAll(markerDir, 0o700)
	return filepath.Join(markerDir, sessionID+".soft")
}

func touch(path string) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err == nil {
		f.Close()
	}
}

func normTier(t string) string {
	if t == "" {
		return "unknown"
	}
	return t
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
