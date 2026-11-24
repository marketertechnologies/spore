package fleet

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/versality/spore/internal/task"
	"github.com/versality/spore/internal/task/frontmatter"
)

// reopenState is the pending reopen the mirror announces on the PR:
// back to draft, plus one comment carrying the operator's note.
type reopenState struct {
	Cycle int    `json:"cycle"`
	Note  string `json:"note"`
}

// cyclesDir holds archived loop cycles: cycles/<n>/ keeps cycle n's
// responses/, reviews/, state/ and summary.md exactly as they were.
func cyclesDir(projectRoot, slug string) string {
	return filepath.Join(task.RoleTaskDir(projectRoot, slug), "cycles")
}

// ReopenRoleLoop starts a fresh engineer -> reviewer A -> reviewer B
// cycle on a role task whose loop finished (done or escalated),
// keeping its branch, worktree and PR. It archives the cycle's round
// files and state markers to cycles/<n>/ (the phase is derived from
// round files, so an empty tree restarts at engineer-initial), appends
// note to spec.md as the requirement for the new cycle, and resets the
// PR mirror so it re-drafts the PR and posts the new cycle. Returns
// the new cycle number (2 for the first reopen).
func ReopenRoleLoop(projectRoot, tasksDir, slug, note string) (int, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return 0, errors.New("reopen: a note is required; it tells the engineer what the new cycle must change")
	}
	if err := CheckReopenable(projectRoot, tasksDir, slug); err != nil {
		return 0, err
	}
	unlockDrive, err := lockRoleDrive(projectRoot, slug)
	if err != nil {
		return 0, lockRefusal("reopen", slug, "a drive tick holds the loop", err)
	}
	defer unlockDrive()
	// Every mirror runs inside a drive tick today, so drive.lock already
	// excludes it. This lock still guards a mirror from a binary without
	// drive.lock (a watcher not yet restarted after an upgrade).
	unlockGitHub, err := lockGitHubState(githubStatePath(projectRoot, slug))
	if err != nil {
		return 0, lockRefusal("reopen", slug, "github.json is locked by a running mirror", err)
	}
	defer unlockGitHub()
	// Re-check under the locks: a drive tick may have moved the phase.
	if err := CheckReopenable(projectRoot, tasksDir, slug); err != nil {
		return 0, err
	}

	_, _ = ReapRole(EngineerSpec(projectRoot, slug))
	_, _ = ReapRole(ReviewerSpec(projectRoot, slug, task.ReviewerA))
	_, _ = ReapRole(ReviewerSpec(projectRoot, slug, task.ReviewerB))

	cycle, err := nextCycle(projectRoot, slug)
	if err != nil {
		return 0, fmt.Errorf("reopen %s: %w", slug, err)
	}
	if err := archiveCycle(projectRoot, slug, cycle-1); err != nil {
		return 0, fmt.Errorf("reopen %s: archive cycle %d: %w", slug, cycle-1, err)
	}
	if _, err := task.EnsureRoleTaskDir(projectRoot, slug); err != nil {
		return 0, fmt.Errorf("reopen %s: %w", slug, err)
	}
	if err := appendReopenNote(projectRoot, slug, cycle, note); err != nil {
		return 0, fmt.Errorf("reopen %s: spec: %w", slug, err)
	}
	if err := resetGitHubState(projectRoot, slug, cycle, note); err != nil {
		return 0, fmt.Errorf("reopen %s: github state: %w", slug, err)
	}
	return cycle, nil
}

// CheckReopenable refuses a task ReopenRoleLoop would refuse: not a
// role-loop task, not active, or its loop not done or escalated.
func CheckReopenable(projectRoot, tasksDir, slug string) error {
	if err := task.RequireRoleLoop(tasksDir, slug); err != nil {
		return fmt.Errorf("reopen %s: %w", slug, err)
	}
	if err := requireActive(tasksDir, slug); err != nil {
		return err
	}
	snap, err := DeriveSnapshot(projectRoot, slug)
	if err != nil {
		return fmt.Errorf("reopen %s: snapshot: %w", slug, err)
	}
	if snap.Phase != PhaseDone && snap.Phase != PhaseEscalated {
		return fmt.Errorf("reopen %s: loop is in phase %s; only a done or escalated loop can be reopened", slug, snap.Phase)
	}
	return nil
}

func requireActive(tasksDir, slug string) error {
	path := filepath.Join(tasksDir, slug+".md")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	m, _, err := frontmatter.Parse(raw)
	if err != nil {
		return fmt.Errorf("reopen %s: parse %s: %w", slug, path, err)
	}
	if m.Status != "active" {
		return fmt.Errorf("reopen %s: task status is %q, want active", slug, m.Status)
	}
	return nil
}

// nextCycle is one past the highest archived cycle, and at least 2:
// the first run is cycle 1, archived on the first reopen.
func nextCycle(projectRoot, slug string) (int, error) {
	entries, err := os.ReadDir(cyclesDir(projectRoot, slug))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	last := 0
	for _, e := range entries {
		if n, err := strconv.Atoi(e.Name()); err == nil && e.IsDir() && n > last {
			last = n
		}
	}
	return last + 2, nil
}

// archiveCycle moves the finished cycle's loop files to cycles/<n>/.
// Pre-start locks and running markers stay: they guard the worktree,
// not the loop.
func archiveCycle(projectRoot, slug string, n int) error {
	root := task.RoleTaskDir(projectRoot, slug)
	dest := filepath.Join(cyclesDir(projectRoot, slug), strconv.Itoa(n))
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s already exists", dest)
	}
	if err := os.MkdirAll(filepath.Join(dest, "state"), 0o755); err != nil {
		return err
	}
	for _, name := range []string{"responses", "reviews", "summary.md"} {
		if err := moveIfExists(filepath.Join(root, name), filepath.Join(dest, name)); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "state"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		if keepOnReopen(e.Name()) {
			continue
		}
		if err := os.Rename(filepath.Join(root, "state", e.Name()), filepath.Join(dest, "state", e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func keepOnReopen(name string) bool {
	return strings.HasPrefix(name, "pre-start")
}

func moveIfExists(from, to string) error {
	if _, err := os.Stat(from); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return os.Rename(from, to)
}

// appendReopenNote adds the note to spec.md under a heading the role
// bodies know: the engineer builds on the existing branch and the
// requirements review checks the note like any spec item.
func appendReopenNote(projectRoot, slug string, cycle int, note string) error {
	spec, err := task.ReadSpec(projectRoot, slug)
	if err != nil {
		return err
	}
	body := strings.TrimRight(string(spec), "\n") +
		fmt.Sprintf("\n\n## Operator reopen (cycle %d)\n\n%s\n", cycle, note)
	return task.WriteSpec(projectRoot, slug, []byte(body))
}

// resetGitHubState keeps the PR identity and last push, drops what the
// mirror posted for the archived cycle (the new cycle reuses the same
// round file names) and queues the reopen announcement. No github.json
// means the PR flow never ran for this task; nothing to reset. The
// caller holds the github.json lock.
func resetGitHubState(projectRoot, slug string, cycle int, note string) error {
	old, err := readGitHubState(projectRoot, slug)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	m := &githubMirror{projectRoot: projectRoot, slug: slug, state: githubState{
		PRNumber:  old.PRNumber,
		PRURL:     old.PRURL,
		PushedSHA: old.PushedSHA,
		Reopen:    &reopenState{Cycle: cycle, Note: note},
	}}
	m.save()
	return nil
}

func reopenEventName(cycle int) string {
	return fmt.Sprintf("reopened-%d", cycle)
}

func reopenComment(r reopenState) string {
	return fmt.Sprintf("**Reopened by the operator (cycle %d)**\n\n%s\n\nThe role loop runs again on this branch: engineer, then both reviews. Round numbers and comment IDs restart at 1 for this cycle.",
		r.Cycle, prCommentNote(r.Note))
}
