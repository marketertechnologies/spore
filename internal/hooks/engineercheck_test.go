package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/versality/spore/internal/task"
)

// checkFixture is a git repo with a task worktree dir and a check that
// appends to <root>/runs on every invocation before running body.
func checkFixture(t *testing.T, body string) (EngineerCheckInput, string) {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.MkdirAll(task.TaskWorktree(root, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	runs := filepath.Join(root, "runs")
	check := fmt.Sprintf("echo x >> %s; %s", runs, body)
	toml := fmt.Sprintf("[fleet.roles.engineer]\ncheck = '%s'\ncheck_timeout_seconds = 30\n", check)
	if err := os.WriteFile(filepath.Join(root, "spore.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	return EngineerCheckInput{Role: "engineer", ProjectRoot: root, Slug: "demo"}, runs
}

func writeResponse(t *testing.T, root string, round int) {
	t.Helper()
	if err := task.WriteEngineerResponse(root, "demo", round, task.EngineerResponse{Notes: "n"}); err != nil {
		t.Fatal(err)
	}
}

func checkRuns(t *testing.T, runs string) int {
	t.Helper()
	b, err := os.ReadFile(runs)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "x\n")
}

func mustCheck(t *testing.T, in EngineerCheckInput) EngineerCheckResult {
	t.Helper()
	res, err := EngineerCheck(in)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func headOf(t *testing.T, root string) string {
	t.Helper()
	sha, err := task.HeadSHA(root)
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

func TestEngineerCheckSkipsOutsideEngineerPane(t *testing.T) {
	in, runs := checkFixture(t, "exit 1")
	writeResponse(t, in.ProjectRoot, 1)
	for _, role := range []string{"", "reviewer"} {
		in.Role = role
		if res := mustCheck(t, in); res.Code != 0 {
			t.Errorf("role %q: code %d, want 0", role, res.Code)
		}
	}
	if n := checkRuns(t, runs); n != 0 {
		t.Errorf("check ran %d times outside an engineer pane", n)
	}
}

func TestEngineerCheckSkipsWithoutCheckOrResponse(t *testing.T) {
	in, runs := checkFixture(t, "exit 1")
	if res := mustCheck(t, in); res.Code != 0 {
		t.Errorf("no response: code %d, want 0", res.Code)
	}
	writeResponse(t, in.ProjectRoot, 1)
	if err := os.WriteFile(filepath.Join(in.ProjectRoot, "spore.toml"), []byte("[fleet]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := mustCheck(t, in); res.Code != 0 {
		t.Errorf("no check configured: code %d, want 0", res.Code)
	}
	if n := checkRuns(t, runs); n != 0 {
		t.Errorf("check ran %d times, want 0", n)
	}
}

func TestEngineerCheckPassRecordsMarkerOnce(t *testing.T) {
	in, runs := checkFixture(t, "true")
	writeResponse(t, in.ProjectRoot, 1)
	for i := 0; i < 2; i++ {
		if res := mustCheck(t, in); res.Code != 0 || res.Stderr != "" {
			t.Fatalf("call %d: %+v, want silent pass", i, res)
		}
	}
	if n := checkRuns(t, runs); n != 1 {
		t.Errorf("check ran %d times, want 1 (pass marker skips the rerun)", n)
	}
	sha := headOf(t, in.ProjectRoot)
	if _, err := os.Stat(task.CheckPassPath(in.ProjectRoot, "demo", 1, sha)); err != nil {
		t.Fatalf("pass marker: %v", err)
	}

	// A newer response re-arms the gate.
	writeResponse(t, in.ProjectRoot, 2)
	mustCheck(t, in)
	if n := checkRuns(t, runs); n != 2 {
		t.Errorf("check ran %d times after a new response, want 2", n)
	}
	if st, err := task.ReadCheckState(in.ProjectRoot, "demo", 2, sha); err != nil || st != task.CheckPassed {
		t.Errorf("round 2 state = %q, %v; want passed", st, err)
	}
}

func TestEngineerCheckFailBlocksWithTail(t *testing.T) {
	in, _ := checkFixture(t, "seq 1 50; exit 3")
	writeResponse(t, in.ProjectRoot, 1)
	res := mustCheck(t, in)
	if res.Code != 2 {
		t.Fatalf("code %d, want 2", res.Code)
	}
	for _, want := range []string{"seq 1 50; exit 3", "exited 3", "attempt 1/3", "\n50\n", "\n11\n"} {
		if !strings.Contains(res.Stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, res.Stderr)
		}
	}
	if strings.Contains(res.Stderr, "\n10\n") {
		t.Errorf("stderr carries more than the last 40 lines:\n%s", res.Stderr)
	}
}

func TestEngineerCheckGivesUpAfterThreeFailures(t *testing.T) {
	in, runs := checkFixture(t, "exit 1")
	writeResponse(t, in.ProjectRoot, 1)
	for i := 1; i < task.MaxCheckAttempts; i++ {
		if res := mustCheck(t, in); res.Code != 2 {
			t.Fatalf("attempt %d: code %d, want 2", i, res.Code)
		}
	}
	res := mustCheck(t, in)
	if res.Code != 0 || !strings.Contains(res.Stderr, "giving up") {
		t.Fatalf("attempt %d: %+v, want the stop let through", task.MaxCheckAttempts, res)
	}
	sha := headOf(t, in.ProjectRoot)
	if st, err := task.ReadCheckState(in.ProjectRoot, "demo", 1, sha); err != nil || st != task.CheckFailed {
		t.Errorf("state = %q, %v; want failed", st, err)
	}
	if res := mustCheck(t, in); res.Code != 0 {
		t.Errorf("after give-up: code %d, want 0", res.Code)
	}
	if n := checkRuns(t, runs); n != task.MaxCheckAttempts {
		t.Errorf("check ran %d times, want %d", n, task.MaxCheckAttempts)
	}

	// Rewriting the response starts a fresh attempt budget.
	marks, _ := filepath.Glob(filepath.Join(task.RoleTaskDir(in.ProjectRoot, "demo"), "state", "check-*"))
	earlier := time.Now().Add(-time.Minute)
	for _, m := range marks {
		if err := os.Chtimes(m, earlier, earlier); err != nil {
			t.Fatal(err)
		}
	}
	writeResponse(t, in.ProjectRoot, 1)
	if res := mustCheck(t, in); res.Code != 2 || !strings.Contains(res.Stderr, "attempt 1/3") {
		t.Errorf("after rewrite: %+v, want a fresh first attempt", res)
	}
}

func TestEngineerCheckTimeout(t *testing.T) {
	in, _ := checkFixture(t, "sleep 30")
	if err := os.WriteFile(filepath.Join(in.ProjectRoot, "spore.toml"),
		[]byte("[fleet.roles.engineer]\ncheck = 'sleep 30'\ncheck_timeout_seconds = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeResponse(t, in.ProjectRoot, 1)
	start := time.Now()
	res := mustCheck(t, in)
	if res.Code != 2 || !strings.Contains(res.Stderr, "exited 124") || !strings.Contains(res.Stderr, "timed out after 1s") {
		t.Errorf("timeout: %+v", res)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("timeout took %s", took)
	}
}

func TestEngineerCheckRunsInTaskWorktree(t *testing.T) {
	in, _ := checkFixture(t, "pwd -P > ../../cwd")
	writeResponse(t, in.ProjectRoot, 1)
	if res := mustCheck(t, in); res.Code != 0 {
		t.Fatalf("code %d: %s", res.Code, res.Stderr)
	}
	got, err := os.ReadFile(filepath.Join(in.ProjectRoot, "cwd"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(task.TaskWorktree(in.ProjectRoot, "demo"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != want {
		t.Errorf("check ran in %q, want %q", strings.TrimSpace(string(got)), want)
	}
}
