package fleet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/versality/spore/internal/task"
)

// DriveRoleLoop advances the role-loop state machine for slug by one
// tick. It is idempotent and side-effecting:
//
//   - On first call, copies tasks/<slug>.md (frontmatter stripped) to
//     .spore/<slug>/spec.md.
//   - Reads the derived snapshot.
//   - Spawns the role pane the snapshot says owns the next file write
//     (engineer for initial / revise phases, reviewer A or B for
//     review phases). Spawn calls are idempotent.
//   - Reaps reviewer A when the loop transitions into B (A's pane is
//     no longer needed once it has approved).
//   - When the snapshot is engineer-revise-*, sends a tmux send-keys
//     wake to the engineer pane pointing at the latest verdict file.
//     A delivery marker guards against re-sending the same verdict.
//   - When the snapshot is PhaseDone, writes a synthesized summary at
//     .spore/<slug>/summary.md (idempotent: skip if file already
//     exists).
//   - When the snapshot is PhaseEscalated, leaves panes alive.
//
// Returns the snapshot observed after side effects.
func DriveRoleLoop(projectRoot, tasksDir, slug string) (Snapshot, error) {
	if err := task.CacheSpecFromTaskFile(projectRoot, tasksDir, slug); err != nil {
		return Snapshot{}, fmt.Errorf("drive %s: cache spec: %w", slug, err)
	}
	// Own the worktree precondition explicitly. The role panes cwd
	// into <projectRoot>/.worktrees/<slug>/; without this call the
	// loop would rely on the homogeneous fleet's task.Ensure to add
	// the worktree, which is gated off for role-looped slugs.
	if _, err := task.EnsureWorktree(tasksDir, slug); err != nil {
		return Snapshot{}, fmt.Errorf("drive %s: ensure worktree: %w", slug, err)
	}
	snap, err := DeriveSnapshot(projectRoot, slug)
	if err != nil {
		return Snapshot{}, fmt.Errorf("drive %s: snapshot: %w", slug, err)
	}

	switch snap.Phase {
	case PhaseEngineerInitial, PhaseEngineerReviseA, PhaseEngineerReviseB:
		if _, _, err := SpawnRole(EngineerSpec(projectRoot, slug)); err != nil {
			return snap, fmt.Errorf("drive %s: spawn engineer: %w", slug, err)
		}
		if snap.Phase != PhaseEngineerInitial {
			if err := wakeEngineer(projectRoot, slug, snap); err != nil {
				return snap, fmt.Errorf("drive %s: wake engineer: %w", slug, err)
			}
		}
	case PhaseReviewA:
		if _, _, err := SpawnRole(EngineerSpec(projectRoot, slug)); err != nil {
			return snap, fmt.Errorf("drive %s: spawn engineer: %w", slug, err)
		}
		if _, _, err := SpawnRole(ReviewerSpec(projectRoot, slug, task.ReviewerA)); err != nil {
			return snap, fmt.Errorf("drive %s: spawn reviewer A: %w", slug, err)
		}
	case PhaseReviewB:
		// A approved; tear down A's pane and bring B up. Engineer
		// stays alive across the handover.
		if _, err := ReapRole(ReviewerSpec(projectRoot, slug, task.ReviewerA)); err != nil {
			return snap, fmt.Errorf("drive %s: reap reviewer A: %w", slug, err)
		}
		if _, _, err := SpawnRole(EngineerSpec(projectRoot, slug)); err != nil {
			return snap, fmt.Errorf("drive %s: spawn engineer: %w", slug, err)
		}
		if _, _, err := SpawnRole(ReviewerSpec(projectRoot, slug, task.ReviewerB)); err != nil {
			return snap, fmt.Errorf("drive %s: spawn reviewer B: %w", slug, err)
		}
	case PhaseDone:
		if err := writeSummary(projectRoot, slug); err != nil {
			return snap, fmt.Errorf("drive %s: write summary: %w", slug, err)
		}
		// Operator wants the panes torn down once the branch is
		// ready; leaving them alive only re-spends tokens.
		_, _ = ReapRole(EngineerSpec(projectRoot, slug))
		_, _ = ReapRole(ReviewerSpec(projectRoot, slug, task.ReviewerA))
		_, _ = ReapRole(ReviewerSpec(projectRoot, slug, task.ReviewerB))
	case PhaseEscalated:
		// Leave panes alive per spec: the operator inspects.
	}

	return snap, nil
}

// wakeEngineer sends a tmux send-keys nudge to the engineer pane with
// the path of the latest reviewer verdict, so the agent picks the
// revision round up without waiting on a manual prompt. The marker
// file under <roletaskdir>/state/ keeps the wake idempotent across
// drive passes; its filename embeds the engineer session's
// `#{session_created}` stamp so a respawn (engineer pane crashed,
// then re-minted by SpawnRole on the next tick) clears the gate and
// the new pane gets nudged in turn.
func wakeEngineer(projectRoot, slug string, snap Snapshot) error {
	if snap.CurrentReviewer == "" || snap.ReviewerRound < 1 {
		return nil
	}
	verdictPath := task.ReviewPath(projectRoot, slug, snap.CurrentReviewer, snap.ReviewerRound)

	session, err := EngineerSpec(projectRoot, slug).SessionName()
	if err != nil {
		return err
	}
	if !hasSession(session) {
		// Engineer pane is not up; the spawn pass above will have
		// failed if the agent crashed. Skip the wake and let the
		// next tick try.
		return nil
	}
	created, err := sessionCreated(session)
	if err != nil {
		// `display-message` failure means we cannot tell if this is a
		// fresh pane or the one we already nudged. Skip rather than
		// risk spamming send-keys; the next tick retries.
		return nil
	}

	markerDir := filepath.Join(task.RoleTaskDir(projectRoot, slug), "state")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		return err
	}
	marker := filepath.Join(markerDir, fmt.Sprintf("woken-%s-%d-%s", snap.CurrentReviewer, snap.ReviewerRound, created))
	if _, err := os.Stat(marker); err == nil {
		return nil
	}

	msg := fmt.Sprintf("Reviewer %s requested changes at round %d. Read %s and start the revision.",
		snap.CurrentReviewer, snap.ReviewerRound, verdictPath)
	if err := exec.Command("tmux", "send-keys", "-t", session, msg, "Enter").Run(); err != nil {
		return fmt.Errorf("tmux send-keys: %w", err)
	}
	return os.WriteFile(marker, []byte(verdictPath+"\n"), 0o644)
}

// sessionCreated returns the `#{session_created}` stamp tmux assigns
// the named session at spawn (a unix timestamp). Used by the engineer
// wake marker to detect a respawn: a fresh pane gets a different
// stamp, so the marker name changes and the next drive tick re-fires
// the send-keys nudge.
func sessionCreated(name string) (string, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", name, "#{session_created}").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// writeSummary synthesises .spore/<slug>/summary.md from the on-disk
// review threads. Idempotent: a present summary.md is left alone.
func writeSummary(projectRoot, slug string) error {
	path := filepath.Join(task.RoleTaskDir(projectRoot, slug), "summary.md")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	body, err := buildSummary(projectRoot, slug)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

func buildSummary(projectRoot, slug string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: review summary\n\n", slug)
	fmt.Fprintf(&b, "Generated %s\n\n", time.Now().UTC().Format(time.RFC3339))

	for _, instance := range []task.ReviewerInstance{task.ReviewerA, task.ReviewerB} {
		rounds, err := task.ListReviewRounds(projectRoot, slug, instance)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "## reviewer %s\n\n", instance)
		if len(rounds) == 0 {
			b.WriteString("No rounds recorded.\n\n")
			continue
		}
		for _, r := range rounds {
			rev, err := task.ReadReview(projectRoot, slug, instance, r)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "### round %d -> %s\n\n", r, rev.Verdict)
			if rev.Summary != "" {
				fmt.Fprintf(&b, "%s\n\n", rev.Summary)
			}
			if len(rev.Comments) > 0 {
				for _, c := range rev.Comments {
					fmt.Fprintf(&b, "- %s\n", c)
				}
				b.WriteString("\n")
			}
		}
	}

	engRounds, err := task.ListEngineerRounds(projectRoot, slug)
	if err != nil {
		return "", err
	}
	if len(engRounds) > 0 {
		b.WriteString("## engineer responses\n\n")
		for _, r := range engRounds {
			resp, err := task.ReadEngineerResponse(projectRoot, slug, r)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "### round %d\n\n", r)
			if len(resp.Addressed) > 0 {
				b.WriteString("Addressed:\n")
				for _, c := range resp.Addressed {
					fmt.Fprintf(&b, "- %s\n", c)
				}
				b.WriteString("\n")
			}
			if len(resp.Pushback) > 0 {
				b.WriteString("Pushback:\n")
				for _, c := range resp.Pushback {
					fmt.Fprintf(&b, "- %s\n", c)
				}
				b.WriteString("\n")
			}
			if resp.Notes != "" {
				fmt.Fprintf(&b, "Notes: %s\n\n", resp.Notes)
			}
		}
	}

	return b.String(), nil
}
