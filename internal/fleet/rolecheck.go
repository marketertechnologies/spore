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

// PhaseLabel is snap.Phase for display, suffixed ` (held: check)` while
// the driver holds the reviewer on a pending engineer check, so a
// review phase does not read as if the reviewer were running.
func (s Snapshot) PhaseLabel() string {
	if s.Check == task.CheckPending {
		return string(s.Phase) + " (held: check)"
	}
	return string(s.Phase)
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

// holdForCheck applies the engineer check gate to the mirror's pending
// events. With the gate on, an engineer round reaches GitHub only once
// the check recorded a result (pass or give-up) for the latest round at
// the worktree HEAD; until then pending is cut at its first engineer
// event, since every engineer event pushes and an earlier round's push
// would carry the latest, unchecked commits. Nothing after the cut is
// posted either. gated reports the gate is on; sha is the commit the
// reviewer read (HEAD once checked, else the newest checked commit of
// the latest round that has one), which the mirror pushes instead of a
// fresh HEAD. PhaseDone releases the hold ungated, since the engineer
// pane is gone and nothing would ever record a result.
func holdForCheck(projectRoot, slug string, snap Snapshot, pending []mirrorEvent) (out []mirrorEvent, sha string, gated bool, err error) {
	if len(pending) == 0 || snap.Phase == PhaseDone || snap.EngineerRound < 1 || task.LoadEngineerCheck(projectRoot).Cmd == "" {
		return pending, "", false, nil
	}
	head, err := task.HeadSHA(task.TaskWorktree(projectRoot, slug))
	if err != nil {
		head = ""
	}
	state, err := task.ReadCheckState(projectRoot, slug, snap.EngineerRound, head)
	if err != nil {
		return nil, "", true, err
	}
	if state == task.CheckPassed || state == task.CheckFailed {
		return pending, head, true, nil
	}
	for i, e := range pending {
		if e.kind == eventEngineer {
			pending = pending[:i]
			break
		}
	}
	for r := snap.EngineerRound; r >= 1 && sha == ""; r-- {
		if sha, err = task.LatestCheckedSHA(projectRoot, slug, r); err != nil {
			return nil, "", true, err
		}
	}
	return pending, sha, true, nil
}
