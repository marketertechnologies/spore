// Package tokenstatus implements `spore token-status`, the reader twin of
// the context-tee Stop hook (internal/hooks/contexttee). Two modes:
//
//	--statusline  Reads claude-code's statusLine payload on stdin
//	              ({cwd, session_id, transcript_path, ...}), parses the
//	              transcript live for the latest assistant usage row, and
//	              prints "ctx 87k / 190k (43%) max". Falls back to the tee
//	              file when the transcript can't be parsed, then to a
//	              zero-ctx render derived from cwd/inbox role detection so a
//	              fresh session before its first assistant turn still shows
//	              "ctx 0 / 190k (0%) coordinator" instead of "ctx ?". "ctx ?"
//	              only fires when cwd and inbox are both missing.
//
//	--fleet       Aggregates the coordinator tee plus every worker tee into
//	              one line for tmux status-right. Sorted by pct desc, capped
//	              at FleetMaxEntries with "+N more" when truncated. Tees
//	              older than FleetStaleAfter are skipped.
//
// The tee shape, cap precedence, and role split mirror
// internal/hooks/contexttee so the writer and reader never disagree.
package tokenstatus

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/versality/spore/internal/budget"
	"github.com/versality/spore/internal/coordinator"
	"github.com/versality/spore/internal/hooks/contexttee"
	"github.com/versality/spore/internal/transcript"
)

const (
	FleetStaleAfter = 30 * time.Minute
	FleetMaxEntries = 5
)

// activeTierFn resolves the live account tier when SPORE_ACCOUNT_TIER is
// unset (interactive operator sessions don't carry it; only the worker /
// coordinator spawn paths export it). Indirected so tests can stub the
// credential read.
var activeTierFn = func() string {
	t, err := budget.ActiveTierString()
	if err != nil {
		return ""
	}
	return t
}

// tee is the on-disk schema written by the context-tee hook.
type tee = contexttee.TokenJSON

type statuslinePayload struct {
	Cwd            string `json:"cwd"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
}

// Run dispatches between --statusline and --fleet.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	mode := ""
	colorEnv := os.Getenv("SPORE_TOKEN_STATUS_COLOR")
	color := colorEnv != "0" && colorEnv != "false" && os.Getenv("NO_COLOR") == ""
	for _, a := range args {
		switch a {
		case "--statusline":
			mode = "statusline"
		case "--fleet":
			mode = "fleet"
		case "--no-color":
			color = false
		case "--color":
			color = true
		case "-h", "--help":
			fmt.Fprint(stdout, usage)
			return 0
		default:
			fmt.Fprintf(stderr, "spore token-status: unknown arg %q\n%s", a, usage)
			return 2
		}
	}
	switch mode {
	case "statusline":
		return runStatusline(stdin, stdout, color)
	case "fleet":
		return runFleet(stdout, color, time.Now)
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
}

const usage = `spore token-status - render claude-code context counts

Usage:
  spore token-status --statusline   # claude-code statusLine command
  spore token-status --fleet        # tmux status-right one-liner

Reads tee files written by the context-tee Stop hook:
  <coordinator-state-dir>/token.json          (coordinator)
  $SPORE_WORKER_TOKEN_DIR/<slug>.json         (each worker)
`

func runStatusline(stdin io.Reader, stdout io.Writer, color bool) int {
	body, _ := io.ReadAll(stdin)
	var p statuslinePayload
	_ = json.Unmarshal(body, &p)

	if t, ok := liveStatusline(p); ok {
		fmt.Fprintln(stdout, formatStatusline(t, color))
		return 0
	}
	if t, ok := lookupTee(p.SessionID, p.Cwd); ok {
		fmt.Fprintln(stdout, formatStatusline(t, color))
		return 0
	}
	if t, ok := zeroStatusline(p); ok {
		fmt.Fprintln(stdout, formatStatusline(t, color))
		return 0
	}
	fmt.Fprintln(stdout, "ctx ?")
	return 0
}

// liveStatusline parses the claude-code transcript pointed at by the
// statusLine payload and synthesizes a tee row from the latest assistant
// usage. Returns ok=false when the transcript is missing/unreadable or has
// no usage yet, so the caller can fall back to the Stop-hook tee.
func liveStatusline(p statuslinePayload) (tee, bool) {
	if p.TranscriptPath == "" || !fileExists(p.TranscriptPath) {
		return tee{}, false
	}
	ctx := transcript.SumContextTokens(p.TranscriptPath)
	if ctx <= 0 {
		return tee{}, false
	}
	return synthTee(p, ctx), true
}

// zeroStatusline renders a ctx=0 frame from cwd/inbox role detection when
// neither the live transcript nor a cached tee yields a count. Covers the
// fresh-session window: claude-code calls statusLine before the first
// assistant turn writes a usage row. role + caps + tier are knowable from
// env+cwd alone; only the live ctx number (genuinely 0 here) needs the
// transcript.
func zeroStatusline(p statuslinePayload) (tee, bool) {
	if p.Cwd == "" && os.Getenv("SPORE_TASK_INBOX") == "" {
		return tee{}, false
	}
	return synthTee(p, 0), true
}

func synthTee(p statuslinePayload, ctx int) tee {
	role, slug := detectRole(p.Cwd)
	tier := resolveTier()
	soft, hard := caps(role, tier)
	pct := 0
	if hard > 0 {
		pct = (ctx * 100) / hard
	}
	return tee{
		Slug:      slug,
		SessionID: p.SessionID,
		Role:      role,
		Tier:      tier,
		Ctx:       ctx,
		CapSoft:   soft,
		CapHard:   hard,
		Pct:       pct,
	}
}

// resolveTier returns the spawn-exported SPORE_ACCOUNT_TIER when present,
// else the live tier, else "unknown".
func resolveTier() string {
	if t := strings.TrimSpace(os.Getenv("SPORE_ACCOUNT_TIER")); t != "" {
		return t
	}
	if t := activeTierFn(); t != "" && t != "unknown" {
		return t
	}
	return "unknown"
}

// detectRole mirrors contexttee.resolve: an inbox under the coordinator
// state dir is a coordinator; any other inbox names a worker by its
// parent dir. With no inbox, a cwd inside a .worktrees/<slug> checkout is
// a worker; everything else is the coordinator.
func detectRole(cwd string) (role, slug string) {
	coordDir := strings.TrimRight(coordinator.StateDir(), "/")
	if inbox := os.Getenv("SPORE_TASK_INBOX"); inbox != "" {
		if coordDir != "" && (inbox == coordDir || strings.HasPrefix(inbox, coordDir+"/")) {
			return "coordinator", "coordinator"
		}
		parent := filepath.Base(filepath.Dir(inbox))
		if parent != "" && parent != "." && parent != "/" {
			return "worker", parent
		}
	}
	if s := worktreeSlug(cwd); s != "" {
		return "worker", s
	}
	return "coordinator", "coordinator"
}

// worktreeSlug returns the segment after ".worktrees" in path, or "".
func worktreeSlug(path string) string {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	for i, p := range parts {
		if p == ".worktrees" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func caps(role, tier string) (soft, hard int) {
	if role == "coordinator" {
		return envInt("SPORE_COORDINATOR_TOKEN_SOFT", contexttee.DefaultCoordSoftCap),
			envInt("SPORE_COORDINATOR_TOKEN_HARD", contexttee.DefaultCoordHardCap)
	}
	if v := strings.TrimSpace(os.Getenv("SPORE_WORKER_TOKEN_WRAP")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n, n
		}
	}
	var cap int
	if tier == "max" {
		cap = envInt("SPORE_WORKER_TOKEN_WRAP_MAX", contexttee.DefaultWorkerWrapMax)
	} else {
		cap = envInt("SPORE_WORKER_TOKEN_WRAP_SUB", contexttee.DefaultWorkerWrapSub)
	}
	return cap, cap
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func formatStatusline(t tee, color bool) string {
	cap := t.CapHard
	if cap == 0 {
		cap = t.CapSoft
	}
	pct := t.Pct
	if pct == 0 && cap > 0 {
		pct = (t.Ctx * 100) / cap
	}
	tier := t.Tier
	if tier == "" {
		tier = "unknown"
	}
	out := fmt.Sprintf("ctx %s / %s (%d%%) %s",
		formatTokens(t.Ctx), formatTokens(cap), pct, tier)
	if color {
		out = colorize(out, pct)
	}
	return out
}

func runFleet(stdout io.Writer, color bool, now func() time.Time) int {
	tees := collectTees(now())
	if len(tees) == 0 {
		fmt.Fprintln(stdout, "")
		return 0
	}
	sort.Slice(tees, func(i, j int) bool {
		if tees[i].Pct != tees[j].Pct {
			return tees[i].Pct > tees[j].Pct
		}
		return tees[i].Slug < tees[j].Slug
	})
	more := 0
	if len(tees) > FleetMaxEntries {
		more = len(tees) - FleetMaxEntries
		tees = tees[:FleetMaxEntries]
	}
	parts := make([]string, 0, len(tees))
	for _, t := range tees {
		parts = append(parts, formatFleetEntry(t, color))
	}
	if more > 0 {
		parts = append(parts, fmt.Sprintf("+%d more", more))
	}
	fmt.Fprintln(stdout, strings.Join(parts, " | "))
	return 0
}

func formatFleetEntry(t tee, color bool) string {
	cap := t.CapHard
	if cap == 0 {
		cap = t.CapSoft
	}
	bang := ""
	if t.Pct >= 90 {
		bang = "!"
	}
	entry := fmt.Sprintf("%s %s/%s%s",
		t.Slug, formatTokens(t.Ctx), formatTokens(cap), bang)
	if color {
		entry = colorize(entry, t.Pct)
	}
	return entry
}

// formatTokens renders a token count in compact "Nk" form when >= 1000;
// truncates fractional thousands so 87543 -> "87k".
func formatTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

const (
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiReset  = "\x1b[0m"
)

func colorize(s string, pct int) string {
	switch {
	case pct >= 90:
		return ansiRed + s + ansiReset
	case pct >= 70:
		return ansiYellow + s + ansiReset
	default:
		return s
	}
}

// lookupTee finds the tee whose session_id matches sid. Falls back to a
// cwd-based slug match when sid is empty (fresh session, statusLine fires
// before the first Stop has written a tee).
func lookupTee(sid, cwd string) (tee, bool) {
	for _, p := range candidateTeePaths() {
		t, err := readTee(p)
		if err != nil {
			continue
		}
		if sid != "" && t.SessionID == sid {
			return t, true
		}
	}
	if cwd != "" {
		path := filepath.Join(workerTokenDir(), filepath.Base(cwd)+".json")
		if t, err := readTee(path); err == nil {
			return t, true
		}
	}
	return tee{}, false
}

func candidateTeePaths() []string {
	var paths []string
	if p := coordinatorTeePath(); p != "" {
		paths = append(paths, p)
	}
	entries, err := os.ReadDir(workerTokenDir())
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			paths = append(paths, filepath.Join(workerTokenDir(), e.Name()))
		}
	}
	return paths
}

func collectTees(now time.Time) []tee {
	var out []tee
	for _, p := range candidateTeePaths() {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > FleetStaleAfter {
			continue
		}
		t, err := readTee(p)
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	return out
}

func readTee(path string) (tee, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return tee{}, err
	}
	var t tee
	if err := json.Unmarshal(b, &t); err != nil {
		return tee{}, err
	}
	return t, nil
}

func coordinatorTeePath() string {
	dir := coordinator.StateDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "token.json")
}

func workerTokenDir() string {
	if d := os.Getenv("SPORE_WORKER_TOKEN_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "spore", "worker-token")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
