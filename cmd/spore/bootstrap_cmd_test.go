package main

import (
	"errors"
	"os/exec"
	"testing"
)

func TestRunBootstrapBlockedVersusDone(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	var err error
	captureHook(t, "", func() int { err = runBootstrap([]string{"--root", root}); return 0 })
	if !errors.Is(err, errBootstrapBlocked) {
		t.Fatalf("fresh repo: err = %v, want errBootstrapBlocked", err)
	}

	args := []string{"--root", root}
	for _, s := range []string{"repo-mapped", "info-gathered", "tests-pass", "creds-wired", "readme-followed", "validation-green", "pilot-aligned", "worker-fleet-ready"} {
		args = append(args, "--skip", s)
	}
	captureHook(t, "", func() int { err = runBootstrap(args); return 0 })
	if err != nil {
		t.Fatalf("every stage skipped: err = %v, want nil", err)
	}
}
