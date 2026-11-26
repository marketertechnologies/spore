package task

import (
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
// Returns nil when tmux isn't running or no session matches. Pure
// substring scan: the slug-end boundary is enforced by requiring the
// next char to be end-of-string or a space (so slug "foo" doesn't
// kill "foo-bar").
func matchingSlugSessions(tasksDir, projectRoot, slug string, withRoles bool) []string {
	out, err := callbudget.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return nil
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
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
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
	return matches
}

// killAllSlugSessions tears down every tmux session matching slug
// for the project. Errors are surfaced to stderr so a broken kill is
// visible; this is best-effort cleanup and the status flip stays the
// source of truth. Pass-through no-op when tmux isn't running or
// nothing matches.
func killAllSlugSessions(tasksDir, projectRoot, slug string) {
	for _, name := range matchingSlugSessions(tasksDir, projectRoot, slug, true) {
		if out, err := callbudget.Command("tmux", "kill-session", "-t", name).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "spore: tmux kill-session %s: %v: %s\n",
				name, err, strings.TrimSpace(string(out)))
		}
	}
}

func hasSession(name string) bool {
	return callbudget.Command("tmux", "has-session", "-t", name).Run() == nil
}
