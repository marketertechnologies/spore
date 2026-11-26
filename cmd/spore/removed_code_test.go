package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The 0.55.0 cleanup deleted subcommands and packages nothing wired
// up. These guard against a merge quietly bringing them back.
func TestRemovedSubcommandsAreUnknown(t *testing.T) {
	t.Setenv("AGENT_BUDGET_STATE_DIR", t.TempDir())
	t.Setenv("SPORE_COORDINATOR_STATE_DIR", t.TempDir())
	cases := []struct {
		run func([]string) int
		sub string
		msg string
	}{
		{runCoordinator, "state-debt", "spore coordinator: unknown subcommand"},
		{runCoordinator, "verify-done", "spore coordinator: unknown subcommand"},
		{runCoordinator, "loop-guard", "spore coordinator: unknown subcommand"},
		{runCoordinator, "monitor", "spore coordinator: unknown subcommand"},
		{runBudget, "capture", "spore budget: unknown subcommand"},
		{runBudget, "stop-hook", "spore budget: unknown subcommand"},
	}
	for _, c := range cases {
		t.Run(c.sub, func(t *testing.T) {
			code, _, stderr := captureHook(t, "", func() int { return c.run([]string{c.sub}) })
			if code != 2 || !strings.Contains(stderr, c.msg) {
				t.Fatalf("code=%d stderr=%q", code, stderr)
			}
		})
	}
}

func TestRemovedPackagesStayGone(t *testing.T) {
	for _, p := range []string{
		"internal/coordinator/verify",
		"internal/coordinator/loopguard",
		"internal/coordinator/statedebt",
		"internal/fleet/rolewatcher.go",
	} {
		if _, err := os.Stat(filepath.Join("..", "..", p)); !os.IsNotExist(err) {
			t.Errorf("%s exists (err=%v); it was removed as unwired", p, err)
		}
	}
}
