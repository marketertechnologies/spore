package fleet

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/versality/spore/internal/hooks"
)

// NudgeMessage is typed into a pane whose last turn ended in an API
// error. Nothing else resumes such a pane: Claude Code does not retry
// the turn, and its Stop hook (the inbox watcher) never ran.
const NudgeMessage = "The API error that stopped your last turn is resolved. Continue the task you were working on; do not repeat work that already finished."

// NudgeResult lists sessions by outcome; Skipped entries carry the
// reason.
type NudgeResult struct {
	Nudged  []string
	Skipped []string
}

// Seams for tests.
var (
	nudgeFailedPanes = hooks.FailedPanes
	nudgePaneSession = tmuxPaneSession
	nudgeSessions    = listTmuxSessions
	nudgeCapture     = capturePane
	nudgeSend        = sendWakeVerified
	nudgeAuthStatus  = claudeAuthStatus
)

// ErrLoggedOut stops a nudge while the host login is still expired:
// every nudged pane would fail again on its next request.
var ErrLoggedOut = errors.New("claude is not logged in on this host: run /login in any Claude Code pane first, then nudge again")

// authStatus is the part of `claude auth status --json` the nudge
// reads. LoginExpiresAt is epoch milliseconds or an RFC 3339 string,
// null for an API key.
type authStatus struct {
	LoggedIn       bool            `json:"loggedIn"`
	LoginExpiresAt json.RawMessage `json:"loginExpiresAt"`
}

func (a authStatus) expired(now time.Time) bool {
	raw := strings.TrimSpace(string(a.LoginExpiresAt))
	if raw == "" || raw == "null" {
		return false
	}
	if ms, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return ms > 0 && time.UnixMilli(ms).Before(now)
	}
	var str string
	if json.Unmarshal(a.LoginExpiresAt, &str) == nil {
		if t, err := time.Parse(time.RFC3339, str); err == nil {
			return t.Before(now)
		}
	}
	return false
}

// claudeAuthStatus asks Claude Code itself; it prints no tokens. ok is
// false when the CLI is missing or its output does not parse (the exit
// code is ignored: logged out may exit non-zero with JSON), and the
// nudge then goes ahead. SPORE_* and WT_* are stripped so nothing
// spore-related starts from it.
func claudeAuthStatus() (authStatus, bool) {
	cmd := exec.Command("claude", "auth", "status", "--json")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SPORE_") && !strings.HasPrefix(kv, "WT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, _ := cmd.Output()
	var st authStatus
	if json.Unmarshal(out, &st) != nil {
		return st, false
	}
	return st, true
}

// Nudge resumes every pane stopped by an API error: first the panes
// the StopFailure hook recorded, then any other spore session whose
// screen shows the error (a pane started before the hook was bound).
// The operator sessions under spore/pilot/ are never typed into. It
// refuses (ErrLoggedOut) while `claude auth status` reports the host
// logged out.
func Nudge(dryRun bool) (NudgeResult, error) {
	var res NudgeResult
	if st, ok := nudgeAuthStatus(); ok && (!st.LoggedIn || st.expired(time.Now())) {
		return res, ErrLoggedOut
	}
	recs, err := nudgeFailedPanes()
	if err != nil {
		return res, err
	}
	seen := map[string]bool{}
	for _, r := range recs {
		session, ok := nudgePaneSession(r.PaneID)
		if !ok || (r.Session != "" && session != r.Session) {
			_ = r.Forget()
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s (pane %s gone)", r.Session, r.PaneID))
			continue
		}
		seen[session] = true
		if nudgeOne(&res, r.PaneID, session, dryRun) && !dryRun {
			_ = r.Forget()
		}
	}

	sessions, err := nudgeSessions()
	if err != nil {
		return res, err
	}
	for _, s := range sessions {
		if seen[s] || !isAgentSession(s) {
			continue
		}
		pane, err := nudgeCapture(s)
		if err != nil || !stoppedOnAPIError(pane) {
			continue
		}
		nudgeOne(&res, s, s, dryRun)
	}
	return res, nil
}

func nudgeOne(res *NudgeResult, target, session string, dryRun bool) bool {
	if dryRun {
		res.Nudged = append(res.Nudged, session)
		return true
	}
	ok, err := nudgeSend(target, NudgeMessage)
	switch {
	case err != nil:
		res.Skipped = append(res.Skipped, fmt.Sprintf("%s (%v)", session, err))
	case !ok:
		res.Skipped = append(res.Skipped, session+" (no input prompt; nudge again shortly)")
	default:
		res.Nudged = append(res.Nudged, session)
	}
	return err == nil && ok
}

func isAgentSession(name string) bool {
	if strings.HasPrefix(name, "spore/pilot/") {
		return false
	}
	return strings.HasPrefix(name, "spore/") || strings.HasPrefix(name, "spore-role/")
}

// stoppedOnAPIError reports whether the last lines above the input box
// show a turn that ended in an API error and is not being retried.
func stoppedOnAPIError(pane string) bool {
	lines := strings.Split(strings.TrimRight(transcriptPart(pane), "\n"), "\n")
	var tail []string
	for i := len(lines) - 1; i >= 0 && len(tail) < 8; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			tail = append(tail, l)
		}
	}
	text := strings.ToLower(strings.Join(tail, "\n"))
	if strings.Contains(text, "retrying in") || strings.Contains(text, "continuing automatically") {
		return false
	}
	return strings.Contains(text, "please run /login") || strings.Contains(text, "api error")
}

func tmuxPaneSession(paneID string) (string, bool) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", paneID, "#{session_name}").Output()
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(out))
	return s, s != ""
}

func listTmuxSessions() ([]string, error) {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return nil, nil
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		if l != "" {
			names = append(names, l)
		}
	}
	return names, nil
}
