package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/versality/spore/internal/callbudget"
	"github.com/versality/spore/internal/events"
	"github.com/versality/spore/internal/task"
)

// Seams for superviseCoordinator; tests swap the probe to avoid
// depending on /proc timing.
var (
	coordinatorAgentGone = coordinatorAgentGoneTmux
	coordinatorGrace     = spawnGrace
)

// superviseCoordinator is the reconcile pass's coordinator step. It
// spawns a missing coordinator, and kills and respawns one whose agent
// exited while the session lives on (a launcher that falls back to a
// greet shell). Fast deaths are capped like workers: past
// MaxFastDeaths the pass stops spawning and tells the coordinator
// inbox once. Failures come back as a warning; they never abort the
// pass, so workers and role loops still run.
func superviseCoordinator(projectRoot string) string {
	session := CoordinatorSessionName(projectRoot)
	guard := coordinatorGuard(projectRoot)
	defer saveGuard(guard)
	now := nowFunc()
	respawnReason := ""
	if guard.Spawned != 0 {
		respawnReason = "session gone"
	}

	if hasSession(session) {
		reason := coordinatorAgentGone(session, now)
		if reason == "" {
			guard.alive(now)
			return ""
		}
		guard.dead()
		if !guard.mayRespawn() {
			return coordinatorCapped(projectRoot, guard, reason)
		}
		respawnReason = reason
		_ = callbudget.Command("tmux", "kill-session", "-t", session).Run()
		fmt.Fprintf(os.Stderr, "fleet: coordinator %s: %s; respawning\n", session, reason)
	} else {
		if tomlCfg, _ := LoadCoordinatorConfig(projectRoot); tomlCfg.ExternalSessionPattern != "" {
			if _, ok := externalCoordinatorSession(tomlCfg.ExternalSessionPattern); ok {
				return ""
			}
		}
		guard.dead()
		if !guard.mayRespawn() {
			return coordinatorCapped(projectRoot, guard, "session gone")
		}
	}

	_, spawned, err := EnsureCoordinator(projectRoot)
	if spawned || err != nil {
		guard.spawned(now)
	}
	if spawned || err != nil {
		ev := events.Event{Kind: events.CoordinatorSpawn}
		if respawnReason != "" {
			ev.Kind, ev.Detail = events.CoordinatorRespawn, respawnReason
		}
		if err != nil {
			ev.Fields = map[string]string{"error": err.Error()}
		}
		task.RecordEvent(projectRoot, ev)
	}
	if err != nil {
		guard.dead()
		if !guard.mayRespawn() {
			return coordinatorCapped(projectRoot, guard, err.Error())
		}
		return fmt.Sprintf("coordinator: %v", err)
	}
	return ""
}

func coordinatorCapped(projectRoot string, guard *spawnGuard, reason string) string {
	msg := fmt.Sprintf("coordinator: agent died within %s of spawn %d times in a row (last: %s); respawn stopped. Start it by hand (spore coordinator start) or run a pass with the fleet disabled to re-arm.",
		FastDeathWindow, guard.Deaths, reason)
	if guard.capOnce() {
		notifyFleet(projectRoot, msg)
		task.RecordEvent(projectRoot, events.Event{Kind: events.CoordinatorCrash,
			Detail: fmt.Sprintf("%d fast deaths in a row (last: %s); respawn stopped", guard.Deaths, reason)})
	}
	return msg
}

func coordinatorGuardPath(projectRoot string) string {
	dir, err := fleetStateDir(projectRoot)
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "coordinator.json")
}

func coordinatorGuard(projectRoot string) *spawnGuard {
	p := coordinatorGuardPath(projectRoot)
	if p == "" {
		return &spawnGuard{}
	}
	return loadGuard(p)
}

// coordinatorAgentGoneTmux reports why the coordinator's agent is no
// longer running in a live session, or "" when it looks alive or the
// state is unknown. It reads the session's first pane: a dead pane, or
// an idle shell (no child process) past the grace window, means the
// agent exited. pane_current_command alone cannot tell: a launcher
// script that runs the agent as a child shows as a shell the whole
// time.
func coordinatorAgentGoneTmux(session string, now time.Time) string {
	out, err := callbudget.Command("tmux", "list-panes", "-s", "-t", session, "-F",
		"#{pane_id}\t#{pane_dead}\t#{pane_pid}\t#{session_created}").Output()
	if err != nil {
		return ""
	}
	var first []string
	firstID := -1
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			continue
		}
		id, err := strconv.Atoi(strings.TrimPrefix(f[0], "%"))
		if err != nil {
			continue
		}
		if firstID == -1 || id < firstID {
			firstID, first = id, f
		}
	}
	if first == nil {
		return ""
	}
	if first[1] == "1" {
		return "pane dead"
	}
	if secs, err := strconv.ParseInt(first[3], 10, 64); err != nil || now.Sub(time.Unix(secs, 0)) < coordinatorGrace {
		return ""
	}
	pid, err := strconv.Atoi(first[2])
	if err != nil {
		return ""
	}
	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(comm))
	if !isShellCommand(name) {
		return ""
	}
	if has, ok := procHasChildren(pid); !ok || has {
		return ""
	}
	return "agent exited to an idle " + name
}

// procHasChildren scans /proc for a process whose parent is pid. ok is
// false when /proc is unreadable (non-Linux), so the caller leaves the
// session alone.
func procHasChildren(pid int) (has, ok bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, false
	}
	want := strconv.Itoa(pid)
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		// The comm field may hold spaces and parens; ppid is the second
		// field after its closing paren.
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) >= 2 && f[1] == want {
			return true, true
		}
	}
	return false, true
}
