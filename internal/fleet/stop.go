package fleet

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/versality/spore/internal/callbudget"
	"github.com/versality/spore/internal/task"
)

// StopResult lists the sessions `spore fleet stop` killed, and the
// ones it tried to kill but could not ("<name>: <error>"). Self is
// the caller's own session when it is a target (a coordinator asked
// to stop the fleet): Stop leaves it for the caller to kill last with
// KillSession, after it has reported.
type StopResult struct {
	Killed []string
	Failed []string
	Self   string
}

// Seams for tests.
var (
	stopListSessions = task.ListSessions
	stopSessionEnv   = tmuxSessionEnv
	stopKill         = KillSession
	stopSelf         = currentSession
)

// KillSession kills the tmux session named exactly name.
func KillSession(name string) error {
	out, err := callbudget.Command("tmux", "kill-session", "-t", "="+name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// currentSession is the tmux session this process runs in, or "".
func currentSession() string {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" || os.Getenv("TMUX") == "" {
		return ""
	}
	out, err := callbudget.Command("tmux", "display-message", "-p", "-t", pane, "#{session_name}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Stop is the emergency stop: it removes the kill-switch flag (as
// Disable) and then kills every agent session spore spawned for
// project, or for every project on the host when project is empty.
// Sessions are matched by ManagedSession, so the operator's own
// spore/pilot/* shells and anything spore did not spawn survive. The
// caller's own session is not killed but returned as Self. The
// sessions are killed even when the flag cannot be removed; the
// returned error then says so. A second sweep catches a session a
// pass already past its flag check spawned meanwhile.
func Stop(project string) (StopResult, error) {
	var res StopResult
	disableErr := Disable()
	self := stopSelf()
	seen := map[string]bool{}
	for range 2 {
		names, err := stopListSessions()
		if err != nil {
			return res, errors.Join(disableErr, err)
		}
		for _, name := range names {
			if seen[name] || !stopTarget(name, project) {
				continue
			}
			seen[name] = true
			if name == self {
				res.Self = name
				continue
			}
			if err := stopKill(name); err != nil {
				res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", name, err))
				continue
			}
			res.Killed = append(res.Killed, name)
		}
	}
	if disableErr != nil {
		disableErr = fmt.Errorf("kill-switch flag not removed (the reconciler may respawn): %w", disableErr)
	}
	return res, disableErr
}

// stopTarget is ManagedSession plus the session's own environment:
// every spore spawn sets SPORE_TASK_SLUG and WT_PROJECT with
// `new-session -e`, so a hand-made session that only borrows the name
// shape is left alone.
func stopTarget(name, project string) bool {
	proj, slug, ok := ManagedSession(name, project)
	if !ok {
		return false
	}
	env, err := stopSessionEnv(name)
	if err != nil {
		return false
	}
	return env["SPORE_TASK_SLUG"] == slug && env["WT_PROJECT"] == proj
}

// sessionPart is the character set of a project name, task slug or
// reviewer instance in a session name spore spawns.
var sessionPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ManagedSession reports whether name has the exact shape of an agent
// session spore spawns, and for which project and slug:
//
//   - spore/<project>/coordinator (slug "coordinator")
//   - spore/<project>/<slug>, a worker
//   - spore-role/<project>/<slug>/engineer
//   - spore-role/<project>/<slug>/reviewer-<instance>
//
// spore/pilot/* is the operator's own shells and never matches. A
// non-empty project limits the match to that project.
func ManagedSession(name, project string) (proj, slug string, ok bool) {
	parts := strings.Split(name, "/")
	switch {
	case len(parts) == 3 && parts[0] == "spore":
		if parts[1] == "pilot" {
			return "", "", false
		}
	case len(parts) == 4 && parts[0] == "spore-role":
		role := parts[3]
		instance, isReviewer := strings.CutPrefix(role, "reviewer-")
		if role != "engineer" && (!isReviewer || !sessionPart.MatchString(instance)) {
			return "", "", false
		}
	default:
		return "", "", false
	}
	proj, slug = parts[1], parts[2]
	if !sessionPart.MatchString(proj) || !sessionPart.MatchString(slug) {
		return "", "", false
	}
	if project != "" && proj != project {
		return "", "", false
	}
	return proj, slug, true
}

func tmuxSessionEnv(name string) (map[string]string, error) {
	out, err := callbudget.Command("tmux", "show-environment", "-t", "="+name).Output()
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			env[k] = v
		}
	}
	return env, nil
}

// RolePane is one live spore-role/<project>/<slug>/<role> session.
type RolePane struct {
	Slug string
	Role string
}

// RolePanes lists projectRoot's live role panes, sorted by session
// name.
func RolePanes(projectRoot string) ([]RolePane, error) {
	names, err := task.ListSessions()
	if err != nil {
		return nil, err
	}
	project := projectName(projectRoot)
	var out []RolePane
	for _, name := range names {
		if _, _, ok := ManagedSession(name, project); !ok || !strings.HasPrefix(name, "spore-role/") {
			continue
		}
		parts := strings.Split(name, "/")
		out = append(out, RolePane{Slug: parts[2], Role: parts[3]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Slug != out[j].Slug {
			return out[i].Slug < out[j].Slug
		}
		return out[i].Role < out[j].Role
	})
	return out, nil
}
