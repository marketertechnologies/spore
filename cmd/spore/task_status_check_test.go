package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/versality/spore/internal/fleet"
	"github.com/versality/spore/internal/task"
)

func captureRoleSnapshot(t *testing.T, snap fleet.Snapshot) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	printRoleSnapshot(snap)
	os.Stdout = orig
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestPrintRoleSnapshotCheckLine(t *testing.T) {
	snap := fleet.Snapshot{
		Slug: "demo", Phase: fleet.PhaseReviewA, EngineerRound: 2,
		CurrentReviewer: task.ReviewerA, ReviewerRound: 1,
		Check: task.CheckPending, CheckSHA: "0123456789abcdef0123",
	}
	out := captureRoleSnapshot(t, snap)
	if want := "check:    pending (engineer round 2, HEAD 0123456789ab)\n"; !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
	snap.Check = task.CheckOff
	if out := captureRoleSnapshot(t, snap); strings.Contains(out, "check:") {
		t.Errorf("gate off still prints a check line:\n%s", out)
	}
}
