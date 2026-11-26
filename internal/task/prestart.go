package task

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/versality/spore/internal/sporetoml"
)

// PreStartNoteVar is the shell variable the pre-start prelude sets to
// a one-line failure note when the hook exits non-zero. It stays unset
// on success, so `${var:+...}` expansions add nothing.
const PreStartNoteVar = "spore_pre_start_note"

// DefaultPreStartTimeoutSeconds bounds the hook when spore.toml sets
// no `[fleet] pre_start_timeout_seconds`, so a hung bring-up cannot
// hold the slug lock forever.
const DefaultPreStartTimeoutSeconds = 900

// PreStart is the `[fleet] pre_start` hook config. A zero
// TimeoutSeconds runs the hook unbounded.
type PreStart struct {
	Cmd            string
	TimeoutSeconds int
}

// LoadPreStart reads `[fleet] pre_start = "<shell command>"` (trimmed)
// and `[fleet] pre_start_timeout_seconds = N` from
// <projectRoot>/spore.toml. A missing file, section, or key leaves Cmd
// empty and spawn argv unchanged; a missing or bad timeout falls back
// to DefaultPreStartTimeoutSeconds.
func LoadPreStart(projectRoot string) PreStart {
	ps := PreStart{TimeoutSeconds: DefaultPreStartTimeoutSeconds}
	entries, err := sporetoml.ReadSection(projectRoot, "fleet")
	if err != nil {
		return ps
	}
	for _, e := range entries {
		switch e.Key {
		case "pre_start":
			ps.Cmd = strings.TrimSpace(e.Value)
		case "pre_start_timeout_seconds":
			if n, err := strconv.Atoi(e.Value); err == nil && n >= 0 {
				ps.TimeoutSeconds = n
			}
		}
	}
	return ps
}

// PreStartLockPath is the per-slug flock file serializing pre_start
// across every session of one task (worker, engineer, reviewers A/B).
// Workers use the same `.spore/<slug>/state/` tree as role panes so a
// slug that moves between the two still shares one lock.
func PreStartLockPath(projectRoot, slug string) string {
	return filepath.Join(RoleTaskDir(projectRoot, slug), "state", "pre-start.lock")
}

// PreStartMarkerPath exists while the session named by key ("worker",
// "engineer", "reviewer-A") is waiting on or running its hook. The
// pane's foreground command is the launch shell meanwhile, so role
// liveness checks it before calling the agent gone.
func PreStartMarkerPath(projectRoot, slug, key string) string {
	return filepath.Join(RoleTaskDir(projectRoot, slug), "state", "pre-start-"+key+".running")
}

// Prelude returns the shell snippet that runs the hook under the
// per-slug lock before the agent execs, or "" when Cmd is empty. The
// snippet ends with "; " so callers append their exec directly. A
// non-zero exit (timeout included) sets PreStartNoteVar; it never
// aborts the spawn.
func (ps PreStart) Prelude(projectRoot, slug, key string) string {
	if ps.Cmd == "" {
		return ""
	}
	lock := PreStartLockPath(projectRoot, slug)
	marker := shellQuote(PreStartMarkerPath(projectRoot, slug, key))
	bound := ""
	if ps.TimeoutSeconds > 0 {
		bound = "timeout " + strconv.Itoa(ps.TimeoutSeconds) + " "
	}
	return fmt.Sprintf(
		`mkdir -p %s && : > %s && flock %s %ssh -c %s; spore_pre_start_rc=$?; rm -f %s; `+
			`if [ "$spore_pre_start_rc" -ne 0 ]; then %s="$(printf %s "$spore_pre_start_rc" %s)"; fi; `,
		shellQuote(filepath.Dir(lock)), marker, shellQuote(lock), bound, shellQuote(ps.Cmd), marker,
		PreStartNoteVar,
		shellQuote("spore: pre-start hook failed (exit %s): %s. Services may be down."),
		shellQuote(ps.Cmd),
	)
}

// workerShellCommand wraps the worker agent command with the
// pre-start prelude. On hook failure the note becomes the agent's
// first prompt (workers otherwise start with none).
func workerShellCommand(prelude, agent string) string {
	if prelude == "" {
		return agent
	}
	return fmt.Sprintf(`%sexec %s ${%s:+"$%[3]s"}`, prelude, agent, PreStartNoteVar)
}

// dirtyNoteMaxPaths caps the paths a dirty-worktree note lists.
const dirtyNoteMaxPaths = 20

// dirtyWorktreeNote describes uncommitted changes in a worktree a
// worker is respawned onto, or "" when it is clean. The brief copy is
// left out: spore writes it there and it is often untracked.
func dirtyWorktreeNote(worktree, brief string) string {
	out, err := gitCmd(worktree, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		return ""
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		path := line[3:]
		if path == brief {
			continue
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return ""
	}
	shown := paths
	more := ""
	if len(paths) > dirtyNoteMaxPaths {
		shown = paths[:dirtyNoteMaxPaths]
		more = fmt.Sprintf(" (and %d more)", len(paths)-dirtyNoteMaxPaths)
	}
	return fmt.Sprintf("spore: this worktree has uncommitted changes from an earlier session (%d paths): %s%s. Inspect them (git status, git diff) before you go on; they may be unfinished work to keep.",
		len(paths), strings.Join(shown, ", "), more)
}

// appendStartNote returns shell that appends note to PreStartNoteVar,
// after any pre-start failure note, so both reach the agent's first
// prompt.
func appendStartNote(note string) string {
	return fmt.Sprintf("%[1]s=\"${%[1]s:+$%[1]s\n\n}\"%[2]s; ", PreStartNoteVar, shellQuote(note))
}
