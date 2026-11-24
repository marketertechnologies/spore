package fleet

import (
	"fmt"
	"os"
	"time"

	"github.com/versality/spore/internal/task"
)

// checkNote is the reviewer wake's sentence on the engineer check, or
// "" when the gate is off.
func checkNote(projectRoot, slug string, snap Snapshot) string {
	cmd := task.LoadEngineerCheck(projectRoot).Cmd
	switch snap.Check {
	case task.CheckPassed:
		return fmt.Sprintf(" The project check (`%s`) passed on HEAD %s (%s): do not re-run the full suite, run only targeted tests.",
			cmd, shortSHA(snap.CheckSHA), task.CheckPassPath(projectRoot, slug, snap.EngineerRound, snap.CheckSHA))
	case task.CheckFailed:
		return fmt.Sprintf(" The project check (`%s`) still failed on HEAD %s after %d attempts and the engineer's stop was let through; the output tail is in %s. Treat the failure as a finding.",
			cmd, shortSHA(snap.CheckSHA), task.MaxCheckAttempts, task.CheckFailedPath(projectRoot, slug, snap.EngineerRound, snap.CheckSHA))
	}
	return ""
}

// checkHoldGrace pads the check timeout before the driver decides a
// pending check is not merely still running in the engineer's Stop
// hook.
const checkHoldGrace = time.Minute

// wakeEngineerForCheck nudges the engineer, once per round and HEAD,
// when its latest response has waited longer than the check timeout
// with no recorded result: the Stop hook never ran (a codex pane, a
// loop already in review when the gate was configured) or HEAD moved
// after it ran. Before that the hook may still be running the check.
func wakeEngineerForCheck(projectRoot, slug string, snap Snapshot) error {
	cfg := task.LoadEngineerCheck(projectRoot)
	resp := task.EngineerResponsePath(projectRoot, slug, snap.EngineerRound)
	st, err := os.Stat(resp)
	if err != nil {
		return err
	}
	if nowFunc().Sub(st.ModTime()) < time.Duration(cfg.TimeoutSeconds)*time.Second+checkHoldGrace {
		return nil
	}
	msg := fmt.Sprintf("Reviewers are held: no passing run of the project check (`%s`) is recorded for %s at HEAD %s. "+
		"Run `spore hooks engineer-check` from the worktree (it runs the check and records the result). It can take up to %ds: run it with a tool timeout at least that long, or in the background and wait for it, since a killed run records nothing. Fix any failure and commit, then end your turn.",
		cfg.Cmd, resp, shortSHA(snap.CheckSHA), cfg.TimeoutSeconds)
	stem := fmt.Sprintf("check-held-%d-%s", snap.EngineerRound, shortSHA(snap.CheckSHA))
	_, err = wakeRole(EngineerSpec(projectRoot, slug), stem, msg)
	return err
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// readCheckGate fills snap.Check in review phases when the engineer
// check is configured. A worktree whose HEAD cannot be read reads as
// pending: the hook cannot have recorded a result for it either.
func readCheckGate(projectRoot, slug string, snap *Snapshot) error {
	if snap.Phase != PhaseReviewA && snap.Phase != PhaseReviewB {
		return nil
	}
	if task.LoadEngineerCheck(projectRoot).Cmd == "" {
		return nil
	}
	sha, err := task.HeadSHA(task.TaskWorktree(projectRoot, slug))
	if err != nil {
		sha = ""
	}
	snap.CheckSHA = sha
	snap.Check, err = task.ReadCheckState(projectRoot, slug, snap.EngineerRound, sha)
	return err
}
