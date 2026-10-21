package task

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/versality/spore/internal/sporetoml"
)

// PreStartNoteVar is the shell variable the pre-start prelude sets to
// a one-line failure note when the hook exits non-zero. It stays unset
// on success, so `${var:+...}` expansions add nothing.
const PreStartNoteVar = "spore_pre_start_note"

// LoadPreStart reads `[fleet] pre_start = "<shell command>"` from
// <projectRoot>/spore.toml, trimmed. Returns "" when the file, the
// section, or the key is missing; spawn argv is then unchanged.
func LoadPreStart(projectRoot string) string {
	entries, err := sporetoml.ReadSection(projectRoot, "fleet")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.Key == "pre_start" {
			return strings.TrimSpace(e.Value)
		}
	}
	return ""
}

// PreStartLockPath is the per-slug flock file serializing pre_start
// across every session of one task (worker, engineer, reviewers A/B).
// Workers use the same `.spore/<slug>/state/` tree as role panes so a
// slug that moves between the two still shares one lock.
func PreStartLockPath(projectRoot, slug string) string {
	return filepath.Join(RoleTaskDir(projectRoot, slug), "state", "pre-start.lock")
}

// PreStartPrelude returns the shell snippet that runs cmd under the
// per-slug lock before the agent execs, or "" when cmd is empty. The
// snippet ends with "; " so callers append their exec directly. A
// non-zero exit sets PreStartNoteVar; it never aborts the spawn.
func PreStartPrelude(projectRoot, slug, cmd string) string {
	if cmd == "" {
		return ""
	}
	lock := PreStartLockPath(projectRoot, slug)
	return fmt.Sprintf(
		`mkdir -p %s && flock %s sh -c %s; spore_pre_start_rc=$?; `+
			`if [ "$spore_pre_start_rc" -ne 0 ]; then %s="$(printf %s "$spore_pre_start_rc" %s)"; fi; `,
		shellQuote(filepath.Dir(lock)), shellQuote(lock), shellQuote(cmd),
		PreStartNoteVar,
		shellQuote("spore: pre-start hook failed (exit %s): %s. Services may be down."),
		shellQuote(cmd),
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
