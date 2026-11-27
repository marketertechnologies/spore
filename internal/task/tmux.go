package task

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/versality/spore/internal/callbudget"
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

// matchingSlugSessions lists every tmux session whose name slot for
// this slug matches, regardless of formula drift between spawn and
// kill. Catches three shapes:
//
//   - spore-style "spore/<project>/<slug>"
//   - wt-style "<icon> <project>/<slug>" or "<icon> <project>/<slug> [tag]"
//   - any external spawner that recorded its own name in frontmatter
//     and embedded "<project>/<slug>" in it
//   - role-loop panes "spore-role/<project>/<slug>/<role>", only with
//     withRoles (done/merge teardown)
//
// Returns nil when tmux isn't running or no session matches, and an
// error when the listing fails, so a wedged server is not read as
// nothing to kill. Pure
// substring scan: the slug-end boundary is enforced by requiring the
// next char to be end-of-string or a space (so slug "foo" doesn't
// kill "foo-bar").
func matchingSlugSessions(tasksDir, projectRoot, slug string, withRoles bool) ([]string, error) {
	sessions, err := ListSessions()
	if err != nil {
		return nil, err
	}
	project := filepath.Base(projectRoot)
	needle := project + "/" + slug
	rolePrefix := "spore-role/" + needle + "/"
	if name, err := ProjectName(projectRoot); err == nil {
		rolePrefix = "spore-role/" + name + "/" + slug + "/"
	}
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
	for _, line := range sessions {
		if strings.HasPrefix(line, rolePrefix) {
			if withRoles {
				add(line)
			}
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
	return matches, nil
}

// killAllSlugSessions tears down every tmux session matching slug
// for the project. Kill errors are surfaced to stderr so a broken
// kill is visible; a failed listing is returned, since nothing was
// killed. No-op when tmux isn't running or nothing matches.
func killAllSlugSessions(tasksDir, projectRoot, slug string) error {
	names, err := matchingSlugSessions(tasksDir, projectRoot, slug, true)
	if err != nil {
		return fmt.Errorf("%s sessions left running: %w", slug, err)
	}
	for _, name := range names {
		if out, err := callbudget.Command("tmux", "kill-session", "-t", name).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "spore: tmux kill-session %s: %v: %s\n",
				name, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func hasSession(name string) bool {
	return callbudget.Command("tmux", "has-session", "-t", name).Run() == nil
}

// TmuxNoServer reports whether a tmux client's stderr says no server
// is listening: none was started, or its socket is gone or stale. A
// socket the caller cannot reach (EACCES from a sandbox, a foreign
// uid) is not "no server": the sessions may all be running.
func TmuxNoServer(stderr string) bool {
	if strings.Contains(stderr, "no server running") {
		return true
	}
	if !strings.Contains(stderr, "error connecting to") {
		return false
	}
	return strings.Contains(stderr, "(No such file or directory)") || strings.Contains(stderr, "(Connection refused)")
}

// ListSessions returns every tmux session name. No server (or a stale
// socket) is an empty list; any other failure, a call killed by its
// budget included, is an error.
func ListSessions() ([]string, error) {
	cmd := callbudget.Command("tmux", "list-sessions", "-F", "#{session_name}")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if TmuxNoServer(msg) {
			return nil, nil
		}
		if msg != "" {
			return nil, fmt.Errorf("tmux list-sessions: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("tmux list-sessions: %w", err)
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}
