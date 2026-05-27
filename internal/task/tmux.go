package task

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func tmuxSessionName(projectRoot, slug string) string {
	return tmuxSessionPrefix(projectRoot) + slug
}

// taskTmuxSession returns the tmux session name to target for slug
// when killing or probing the rower's session. The frontmatter
// `session:` field wins when set (the spawner registers the real
// session name there, e.g. "🐈 acme/my-task [opus]"); otherwise
// the kernel's computed "spore/<project>/<slug>" name is used. A
// task file that fails to read or parse falls back to the computed
// name so a corrupt brief never blocks cleanup.
func taskTmuxSession(tasksDir, projectRoot, slug string) string {
	if m, err := readTaskMeta(tasksDir, slug); err == nil && m.Session != "" {
		return m.Session
	}
	return tmuxSessionName(projectRoot, slug)
}

func tmuxSessionPrefix(projectRoot string) string {
	return fmt.Sprintf("spore/%s/", filepath.Base(projectRoot))
}

// IdleReapThreshold is how long a tmux session must sit without
// activity before pause/block reaps it. Sessions younger than this
// are kept alive: a mid-tool-call rower or a pane the operator just
// stopped typing into is not "abandoned". Override via
// SPORE_IDLE_REAP_SECS for tests / operator tuning.
const IdleReapThreshold = 5 * time.Minute

// matchingSlugSessions lists every tmux session whose name slot for
// this slug matches, regardless of formula drift between spawn and
// kill. Catches three shapes:
//
//   - spore-style "spore/<project>/<slug>"
//   - wt-style "<icon> <project>/<slug>" or "<icon> <project>/<slug> [tag]"
//   - any external spawner that recorded its own name in frontmatter
//     and embedded "<project>/<slug>" in it
//
// Returns nil when tmux isn't running or no session matches. Pure
// substring scan: the slug-end boundary is enforced by requiring the
// next char to be end-of-string or a space (so slug "foo" doesn't
// kill "foo-bar").
func matchingSlugSessions(tasksDir, projectRoot, slug string) []string {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return nil
	}
	project := filepath.Base(projectRoot)
	needle := project + "/" + slug
	recorded := ""
	if m, err := readTaskMeta(tasksDir, slug); err == nil {
		recorded = m.Session
	}
	seen := map[string]bool{}
	var matches []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		matches = append(matches, name)
	}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		i := strings.Index(line, needle)
		if i < 0 {
			continue
		}
		end := i + len(needle)
		if end < len(line) && line[end] != ' ' {
			continue
		}
		add(line)
	}
	if recorded != "" && hasSession(recorded) {
		add(recorded)
	}
	return matches
}

// killAllSlugSessions tears down every tmux session matching slug
// for the project. Errors are surfaced to stderr so a broken kill is
// visible; this is best-effort cleanup and the status flip stays the
// source of truth. Pass-through no-op when tmux isn't running or
// nothing matches.
func killAllSlugSessions(tasksDir, projectRoot, slug string) {
	for _, name := range matchingSlugSessions(tasksDir, projectRoot, slug) {
		if out, err := exec.Command("tmux", "kill-session", "-t", name).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "spore: tmux kill-session %s: %v: %s\n",
				name, err, strings.TrimSpace(string(out)))
		}
	}
}

// reapIdleSlugSessions kills only the matching sessions whose tmux
// activity is older than IdleReapThreshold (paused/blocked semantics:
// keep mid-run sessions alive). projectRoot is derived from tasksDir;
// any read or parse failure leaves the session alone.
func reapIdleSlugSessions(tasksDir, slug string) {
	projectRoot, err := projectRootFromTasksDir(tasksDir)
	if err != nil {
		return
	}
	threshold := IdleReapThreshold
	if v := os.Getenv("SPORE_IDLE_REAP_SECS"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			threshold = time.Duration(secs) * time.Second
		}
	}
	now := time.Now()
	for _, name := range matchingSlugSessions(tasksDir, projectRoot, slug) {
		idle, ok := sessionIdle(name, now)
		if !ok {
			continue
		}
		if idle < threshold {
			continue
		}
		_ = exec.Command("tmux", "kill-session", "-t", name).Run()
	}
}

// sessionIdle reports how long a tmux session has gone without pane
// activity. Returns (0, false) when the activity stamp can't be read
// (session gone, tmux quiet, garbage value); the false signals the
// caller to leave the session alone rather than treat unknown as
// stale.
func sessionIdle(name string, now time.Time) (time.Duration, bool) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", name, "#{session_activity}").Output()
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0, false
	}
	secs, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	last := time.Unix(secs, 0)
	if last.After(now) {
		return 0, true
	}
	return now.Sub(last), true
}

func hasSession(name string) bool {
	return exec.Command("tmux", "has-session", "-t", name).Run() == nil
}
