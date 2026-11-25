package task

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/versality/spore/internal/callbudget"
	"github.com/versality/spore/internal/sporetoml"
)

// DefaultEngineerCheckTimeoutSeconds bounds the engineer check when
// spore.toml sets no `check_timeout_seconds`. It stays well under the
// Stop hook timeout in the shipped settings (EngineerCheckHookTimeout).
const DefaultEngineerCheckTimeoutSeconds = 900

// EngineerCheckHookTimeout is the Claude Code hook timeout, in seconds,
// the shipped settings give `spore hooks engineer-check`. A
// check_timeout_seconds at or above it gets killed by the harness
// before the hook can record a result.
const EngineerCheckHookTimeout = 3600

// MaxEngineerCheckTimeoutSeconds caps check_timeout_seconds a minute
// under EngineerCheckHookTimeout, leaving the hook time to record the
// attempt; a check killed with the hook records nothing and is rerun
// on every stop, so the give-up never triggers.
const MaxEngineerCheckTimeoutSeconds = EngineerCheckHookTimeout - 60

// MaxCheckAttempts is how many failing runs of the check on one
// engineer round and HEAD sha block the stop before the hook gives up,
// records check-failed, and lets the stop through.
const MaxCheckAttempts = 3

// EngineerCheck is the `[fleet.roles.engineer]` verification gate. An
// empty Cmd turns the gate off.
type EngineerCheck struct {
	Cmd            string
	TimeoutSeconds int
}

// LoadEngineerCheck reads `check` and `check_timeout_seconds` from
// `[fleet.roles.engineer]` in <projectRoot>/spore.toml. A missing file,
// section, or key leaves Cmd empty; a missing, bad, or non-positive
// timeout falls back to DefaultEngineerCheckTimeoutSeconds, and one
// past MaxEngineerCheckTimeoutSeconds is clamped to it.
func LoadEngineerCheck(projectRoot string) EngineerCheck {
	c := EngineerCheck{TimeoutSeconds: DefaultEngineerCheckTimeoutSeconds}
	entries, err := sporetoml.ReadSection(projectRoot, "fleet.roles.engineer")
	if err != nil {
		return c
	}
	for _, e := range entries {
		switch e.Key {
		case "check":
			c.Cmd = strings.TrimSpace(e.Value)
		case "check_timeout_seconds":
			if n, err := strconv.Atoi(e.Value); err == nil && n > 0 {
				c.TimeoutSeconds = min(n, MaxEngineerCheckTimeoutSeconds)
			}
		}
	}
	return c
}

// CheckState is the gate state of one engineer response at one HEAD.
type CheckState string

const (
	// CheckOff: no check configured, or no response to gate.
	CheckOff CheckState = ""
	// CheckPending: no pass or give-up recorded since the response.
	CheckPending CheckState = "pending"
	// CheckPassed: check-pass-<round>-<sha> is newer than the response.
	CheckPassed CheckState = "passed"
	// CheckFailed: the hook gave up after MaxCheckAttempts failures
	// (check-failed-<round>-<sha> newer than the response).
	CheckFailed CheckState = "failed"
)

// CheckPassPath is the marker the hook writes when the check passes on
// engineer round `round` at HEAD `sha`.
func CheckPassPath(projectRoot, slug string, round int, sha string) string {
	return checkMarker(projectRoot, slug, fmt.Sprintf("check-pass-%d-%s", round, sha))
}

// CheckFailedPath is the give-up marker for round and sha.
func CheckFailedPath(projectRoot, slug string, round int, sha string) string {
	return checkMarker(projectRoot, slug, fmt.Sprintf("check-failed-%d-%s", round, sha))
}

// CheckAttemptPath is the marker for the n-th failing run (from 1).
func CheckAttemptPath(projectRoot, slug string, round int, sha string, n int) string {
	return checkMarker(projectRoot, slug, fmt.Sprintf("check-attempt-%d-%s-%d", round, sha, n))
}

// CheckLockPath is the flock file serialising check runs on engineer
// round `round`, so the Stop hook and a manual run never overlap.
func CheckLockPath(projectRoot, slug string, round int) string {
	return checkMarker(projectRoot, slug, fmt.Sprintf("check-%d.lock", round))
}

func checkMarker(projectRoot, slug, name string) string {
	return filepath.Join(RoleTaskDir(projectRoot, slug), "state", name)
}

// TaskWorktree is the worktree the role panes run in and the check runs
// against.
func TaskWorktree(projectRoot, slug string) string {
	return filepath.Join(projectRoot, ".worktrees", slug)
}

// HeadSHA returns the full commit id HEAD points at in dir.
func HeadSHA(dir string) (string, error) {
	out, err := callbudget.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD in %s: %w", dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ReadCheckState reports the gate state of engineer round `round` at
// HEAD sha. Markers older than the response file do not count: a
// rewritten response (after a bounce, say) is checked again.
func ReadCheckState(projectRoot, slug string, round int, sha string) (CheckState, error) {
	resp, err := os.Stat(EngineerResponsePath(projectRoot, slug, round))
	if err != nil {
		return CheckOff, err
	}
	if sha == "" {
		return CheckPending, nil
	}
	for _, c := range []struct {
		path  string
		state CheckState
	}{
		{CheckPassPath(projectRoot, slug, round, sha), CheckPassed},
		{CheckFailedPath(projectRoot, slug, round, sha), CheckFailed},
	} {
		ok, err := notOlder(c.path, resp.ModTime())
		if err != nil {
			return CheckOff, err
		}
		if ok {
			return c.state, nil
		}
	}
	return CheckPending, nil
}

// CountCheckAttempts counts the failing runs recorded for round and
// sha since the response was written.
func CountCheckAttempts(projectRoot, slug string, round int, sha string) (int, error) {
	resp, err := os.Stat(EngineerResponsePath(projectRoot, slug, round))
	if err != nil {
		return 0, err
	}
	n := 0
	for i := 1; ; i++ {
		st, err := os.Stat(CheckAttemptPath(projectRoot, slug, round, sha, i))
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return 0, err
		}
		if !st.ModTime().Before(resp.ModTime()) {
			n++
		}
	}
	return n, nil
}

// LatestCheckAttemptSince returns the path of the highest attempt slot
// for round and sha written at or after both since and the response,
// or "" when none was.
func LatestCheckAttemptSince(projectRoot, slug string, round int, sha string, since time.Time) (string, error) {
	resp, err := os.Stat(EngineerResponsePath(projectRoot, slug, round))
	if err != nil {
		return "", err
	}
	if resp.ModTime().After(since) {
		since = resp.ModTime()
	}
	latest := ""
	for i := 1; ; i++ {
		p := CheckAttemptPath(projectRoot, slug, round, sha, i)
		st, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return latest, nil
		}
		if err != nil {
			return "", err
		}
		if !st.ModTime().Before(since) {
			latest = p
		}
	}
}

// WriteCheckAttempt records a failing run in the first free attempt
// slot for round and sha. The slot is claimed with O_EXCL, so a manual
// run racing the Stop hook cannot overwrite the other's attempt; stale
// attempts from before a rewritten response are kept for debugging.
func WriteCheckAttempt(projectRoot, slug string, round int, sha, body string) error {
	if err := os.MkdirAll(filepath.Join(RoleTaskDir(projectRoot, slug), "state"), 0o755); err != nil {
		return err
	}
	for i := 1; ; i++ {
		f, err := os.OpenFile(CheckAttemptPath(projectRoot, slug, round, sha, i), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		_, werr := f.WriteString(body)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		return werr
	}
}

// RevisionPending reports whether a request_changes verdict is newer
// than engineer round `round`: the engineer is mid-revision, and a
// stop then is not a handoff to gate.
func RevisionPending(projectRoot, slug string, round int) (bool, error) {
	resp, err := os.Stat(EngineerResponsePath(projectRoot, slug, round))
	if err != nil {
		return false, err
	}
	for _, inst := range []ReviewerInstance{ReviewerA, ReviewerB} {
		rounds, err := ListReviewRounds(projectRoot, slug, inst)
		if err != nil {
			return false, err
		}
		if len(rounds) == 0 {
			continue
		}
		last := rounds[len(rounds)-1]
		st, err := os.Stat(ReviewPath(projectRoot, slug, inst, last))
		if err != nil {
			return false, err
		}
		if !st.ModTime().After(resp.ModTime()) {
			continue
		}
		rev, err := ReadReview(projectRoot, slug, inst, last)
		if err != nil {
			return false, err
		}
		if rev.Verdict == VerdictRequestChanges {
			return true, nil
		}
	}
	return false, nil
}

func notOlder(path string, than time.Time) (bool, error) {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !st.ModTime().Before(than), nil
}
