package fleet

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/versality/spore/internal/task"
)

// ForceApproveReview flips reviewer inst's final, escalated verdict to
// approve with the operator's reason in `operator_override`, which
// exempts it from the grade/round verdict rule (see task.Review.Forced).
// Every other field, comments included, is kept. Returns the round it
// overrode.
//
// Only an escalation whose fix pass has landed can be forced. Earlier
// verdicts were answered (or are being answered) by an engineer round
// the phase machine pairs with them; flipping one would shift that
// pairing onto the next reviewer's verdicts.
func ForceApproveReview(projectRoot, tasksDir, slug string, inst task.ReviewerInstance, reason string) (int, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return 0, fmt.Errorf("force-approve %s: a reason is required; it is posted with the override", slug)
	}
	if err := task.RequireRoleLoop(tasksDir, slug); err != nil {
		return 0, fmt.Errorf("force-approve %s: %w", slug, err)
	}
	unlock, err := lockRoleDrive(projectRoot, slug)
	if err != nil {
		return 0, lockRefusal("force-approve", slug, "a drive tick holds the loop", err)
	}
	defer unlock()
	snap, err := DeriveSnapshot(projectRoot, slug)
	if err != nil {
		return 0, fmt.Errorf("force-approve %s: snapshot: %w", slug, err)
	}
	if snap.Phase != PhaseEscalated || snap.CurrentReviewer != inst {
		return 0, fmt.Errorf("force-approve %s: loop is in phase %s (reviewer %s); only reviewer %s's escalated final verdict can be forced",
			slug, snap.Phase, snap.CurrentReviewer, inst)
	}
	if snap.EscalationFixPending {
		return 0, fmt.Errorf("force-approve %s: the engineer's fix pass for reviewer %s round %d has not landed; retry once engineer-round-%d.json is written",
			slug, inst, snap.ReviewerRound, snap.EngineerRound+1)
	}
	rounds, err := task.ListReviewRounds(projectRoot, slug, inst)
	if err != nil {
		return 0, fmt.Errorf("force-approve %s: %w", slug, err)
	}
	// The snapshot skipped invalid files; force the verdict it counted.
	round := 0
	for i := len(rounds) - 1; i >= 0 && round == 0; i-- {
		if _, err := task.ReadReview(projectRoot, slug, inst, rounds[i]); err == nil {
			round = rounds[i]
		}
	}
	if round == 0 {
		return 0, fmt.Errorf("force-approve %s: reviewer %s has no valid verdict", slug, inst)
	}

	path := task.ReviewPath(projectRoot, slug, inst, round)
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return 0, fmt.Errorf("force-approve %s: %s: %w", slug, path, err)
	}
	doc["verdict"], _ = json.Marshal(task.VerdictApprove)
	doc["operator_override"], _ = json.Marshal(reason)
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return 0, err
	}
	if _, err := task.ParseForcedReviewAt(projectRoot, slug, inst, round, out.Bytes()); err != nil {
		return 0, fmt.Errorf("force-approve %s: override does not validate; verdict left as it was: %w", slug, err)
	}
	// Recorded before the write so the file never sits on disk with an
	// unrecorded override; dropped again if the write fails, or the
	// still-live reviewer could write a self-approval that validates.
	if err := task.RecordOperatorOverride(projectRoot, slug, inst, round, out.Bytes()); err != nil {
		return 0, fmt.Errorf("force-approve %s: record override: %w", slug, err)
	}
	if err := writeFileAtomic(path, out.Bytes()); err != nil {
		if derr := task.DropOperatorOverride(projectRoot, slug, inst, round); derr != nil {
			return 0, fmt.Errorf("force-approve %s: %v; dropping the override record also failed: %w", slug, err, derr)
		}
		return 0, fmt.Errorf("force-approve %s: %w", slug, err)
	}
	// The loop is no longer escalated; a stale marker would keep the
	// task flagged (task.IsEscalated) through the whole next phase.
	if err := clearEscalationMarkers(projectRoot, slug, inst); err != nil {
		return round, fmt.Errorf("force-approve %s: verdict forced, but clearing the escalation marker failed: %w", slug, err)
	}
	return round, nil
}

// clearEscalationMarkers removes the `escalated-<inst>-*` files under
// the task's state dir, every reviewer's when inst is empty. Called on
// PhaseDone, and by force-approve for the forced reviewer. A missing
// state dir is fine.
func clearEscalationMarkers(projectRoot, slug string, inst task.ReviewerInstance) error {
	prefix := "escalated-"
	if inst != "" {
		prefix += string(inst) + "-"
	}
	dir := filepath.Join(task.RoleTaskDir(projectRoot, slug), "state")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// verdictHeading renders a verdict for the review summary, marking a
// forced one and carrying the operator's reason.
func verdictHeading(rev task.Review) string {
	if !rev.Forced() {
		return rev.Verdict
	}
	return fmt.Sprintf("%s (request changes, force-approved by the operator)\n\nOperator override: %s", rev.Verdict, strings.TrimSpace(rev.OperatorOverride))
}

// writeFileAtomic replaces path via a uniquely named sibling temp
// file, so a reader or a crash never sees a truncated file. The temp
// name never matches round-N.json, so a leftover is never read.
func writeFileAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}
