package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/versality/spore/internal/task"
)

// checkTailLines is how much of a failing check's output goes back to
// the model on stderr.
const checkTailLines = 40

// EngineerCheckInput is the role pane context the engineer-check hook
// acts on, read from the env rolespawn sets.
type EngineerCheckInput struct {
	Role        string
	ProjectRoot string
	Slug        string
}

// EngineerCheckInputFromEnv reads SPORE_ROLE, SPORE_PROJECT_ROOT and
// SPORE_TASK_SLUG.
func EngineerCheckInputFromEnv() EngineerCheckInput {
	return EngineerCheckInput{
		Role:        os.Getenv("SPORE_ROLE"),
		ProjectRoot: os.Getenv("SPORE_PROJECT_ROOT"),
		Slug:        os.Getenv("SPORE_TASK_SLUG"),
	}
}

// EngineerCheckResult is the hook's exit code (0 lets the stop
// through, 2 blocks it and feeds Stderr to the model) plus its stderr.
type EngineerCheckResult struct {
	Code   int
	Stderr string
}

// EngineerCheck is the Stop hook gating an engineer round on the
// project's `[fleet.roles.engineer] check`. It acts only in an engineer
// role pane with a check configured, and only when the latest engineer
// response has no pass or give-up marker for the worktree's HEAD
// newer than it. It then runs the check in the task worktree: a pass
// records check-pass-<round>-<sha>; a failure records an attempt and
// blocks the stop, until the MaxCheckAttempts-th failure on the same
// round and sha records check-failed-<round>-<sha> and lets the stop
// through. Errors reading state let the stop through: the driver keeps
// reviewers held while no marker exists, so failing open here cannot
// skip the gate.
func EngineerCheck(in EngineerCheckInput) (EngineerCheckResult, error) {
	if in.Role != "engineer" || in.ProjectRoot == "" || in.Slug == "" {
		return EngineerCheckResult{}, nil
	}
	cfg := task.LoadEngineerCheck(in.ProjectRoot)
	if cfg.Cmd == "" {
		return EngineerCheckResult{}, nil
	}
	rounds, err := task.ListEngineerRounds(in.ProjectRoot, in.Slug)
	if err != nil || len(rounds) == 0 {
		return EngineerCheckResult{}, err
	}
	round := rounds[len(rounds)-1]
	wt := task.TaskWorktree(in.ProjectRoot, in.Slug)
	sha, err := task.HeadSHA(wt)
	if err != nil {
		return EngineerCheckResult{}, err
	}
	state, err := task.ReadCheckState(in.ProjectRoot, in.Slug, round, sha)
	if err != nil || state != task.CheckPending {
		return EngineerCheckResult{}, err
	}

	start := time.Now()
	code, out := runCheck(wt, cfg)
	took := time.Since(start).Round(time.Second)
	tail := lastLines(out, checkTailLines)
	record := fmt.Sprintf("command: %s\nexit: %d\nduration: %s\nhead: %s\n", cfg.Cmd, code, took, sha)
	if code == 0 {
		return EngineerCheckResult{}, writeMarker(task.CheckPassPath(in.ProjectRoot, in.Slug, round, sha), record)
	}
	record += "--- last output ---\n" + tail
	if err := writeMarker(task.NextCheckAttemptPath(in.ProjectRoot, in.Slug, round, sha), record); err != nil {
		return EngineerCheckResult{}, err
	}
	n, err := task.CountCheckAttempts(in.ProjectRoot, in.Slug, round, sha)
	if err != nil {
		return EngineerCheckResult{}, err
	}
	short := sha
	if len(short) > 12 {
		short = short[:12]
	}
	if n >= task.MaxCheckAttempts {
		if err := writeMarker(task.CheckFailedPath(in.ProjectRoot, in.Slug, round, sha), record); err != nil {
			return EngineerCheckResult{}, err
		}
		return EngineerCheckResult{Stderr: fmt.Sprintf(
			"spore engineer-check: `%s` failed %d times on %s for engineer round %d; giving up, reviewers are told the check failed\n",
			cfg.Cmd, n, short, round)}, nil
	}
	return EngineerCheckResult{Code: 2, Stderr: fmt.Sprintf(
		"spore engineer-check: `%s` exited %d on HEAD %s (engineer round %d, attempt %d/%d). "+
			"Reviewers stay held until it passes. Fix the failure, commit, and end your turn again.\n"+
			"--- last %d lines ---\n%s",
		cfg.Cmd, code, short, round, n, task.MaxCheckAttempts, checkTailLines, tail)}, nil
}

// runCheck runs cfg.Cmd under sh in dir, bounded by cfg.TimeoutSeconds,
// and returns its exit code (124 on timeout, like timeout(1)) and
// combined output. The command runs in its own process group so a
// timeout kills its children too.
func runCheck(dir string, cfg task.EngineerCheck) (int, string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", cfg.Cmd)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return 124, buf.String() + fmt.Sprintf("\nspore: check timed out after %ds\n", cfg.TimeoutSeconds)
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, buf.String()
	case errors.As(err, &exitErr) && exitErr.ExitCode() > 0:
		return exitErr.ExitCode(), buf.String()
	default:
		return 1, buf.String() + fmt.Sprintf("\nspore: run check: %v\n", err)
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}

func writeMarker(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
