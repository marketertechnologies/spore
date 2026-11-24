package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/versality/spore/internal/fleet"
	"github.com/versality/spore/internal/task"
)

// stubAgent stands in for a reviewer: it flags the defect only when
// its prompt carries marker, and records what it was handed.
type stubAgent struct {
	marker string
	check  func(t *testing.T, req AgentRequest)
	t      *testing.T
	calls  atomic.Int32
}

func (a *stubAgent) Run(_ context.Context, req AgentRequest, log io.Writer) error {
	a.calls.Add(1)
	if a.check != nil {
		a.check(a.t, req)
	}
	env := map[string]string{}
	for _, kv := range req.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	rev := task.Review{Author: env["SPORE_REVIEWER_INSTANCE"], Verdict: task.VerdictApprove, Summary: "0 blockers", Comments: []string{}}
	if strings.Contains(req.Prompt, a.marker) {
		rev = task.Review{Author: rev.Author, Verdict: task.VerdictRequestChanges, Summary: "1 blocker", Comments: []string{"[blocker] a.txt:1: the defect"}}
	}
	rounds, _ := filepath.Glob(filepath.Join(env["SPORE_TASK_DIR"], "reviews", rev.Author, "round-*.json"))
	b, _ := json.Marshal(rev)
	fmt.Fprintln(log, "stub review")
	return os.WriteFile(filepath.Join(env["SPORE_TASK_DIR"], "reviews", rev.Author, fmt.Sprintf("round-%d.json", len(rounds)+1)), b, 0o644)
}

type stubJudge struct{}

func (stubJudge) Judge(_ context.Context, in JudgeInput) (JudgeResult, error) {
	hit := strings.Contains(string(in.Verdict), "the defect")
	if in.Kind == KindInvalid {
		hit = !hit
	}
	return JudgeResult{Pass: hit, Reason: "stub"}, nil
}

func confirmedMiss(t *testing.T) (fixture, Case) {
	t.Helper()
	f := newFixture(t)
	c, err := f.store.Confirm(context.Background(), "demo.miss.A2.B1", ConfirmOptions{HeadSHA: f.head, Expect: "flags the defect"})
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func TestRunReplaysFrozenCheckoutWithSlotConfig(t *testing.T) {
	f, c := confirmedMiss(t)
	agent := &stubAgent{marker: "RULE-X", t: t, check: func(t *testing.T, req AgentRequest) {
		if b, _ := os.ReadFile(filepath.Join(req.Dir, "a.txt")); string(b) != "two\n" {
			t.Errorf("checkout a.txt = %q, want head content", b)
		}
		if got := gitT(t, req.Dir, "rev-parse", "main"); got != f.base {
			t.Errorf("base branch main = %s, want %s", got, f.base)
		}
		if got := gitT(t, req.Dir, "for-each-ref", "--format=%(refname:short)", "refs/heads/"); got != "main\nwt/demo" {
			t.Errorf("scratch branches = %q, want only base and task branch", got)
		}
		if got := gitT(t, req.Dir, "remote"); got != "" {
			t.Errorf("scratch clone keeps remotes: %q", got)
		}
		env := strings.Join(req.Env, "\n")
		for _, want := range []string{"SPORE_REVIEWER_INSTANCE=A", "SPORE_REVIEWER_MODE=code-review", "SPORE_REVIEW_EFFORT=medium"} {
			if !strings.Contains(env, want) {
				t.Errorf("env lacks %s:\n%s", want, env)
			}
		}
		if req.Agent != "claude" || !strings.Contains(req.Prompt, "code-review mode") {
			t.Errorf("agent %q / prompt is not the code-review body", req.Agent)
		}
		taskDir := ""
		for _, kv := range req.Env {
			if v, ok := strings.CutPrefix(kv, "SPORE_TASK_DIR="); ok {
				taskDir = v
			}
		}
		for _, rel := range []string{"spec.md", "responses/engineer-round-2.json", "reviews/A/round-1.json"} {
			if _, err := os.Stat(filepath.Join(taskDir, rel)); err != nil {
				t.Errorf("task dir lacks %s", rel)
			}
		}
		if _, err := os.Stat(filepath.Join(taskDir, "github.json")); err == nil {
			t.Error("task dir has github.json; the replay would post to a PR")
		}
	}}
	out := t.TempDir()
	sum, err := Run(context.Background(), []Case{c}, RunOptions{Agent: agent, Judge: stubJudge{}, OutDir: out, Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if sum.OK() || agent.calls.Load() != DefaultRuns {
		t.Fatalf("without rule: ok=%t calls=%d, want a fail over %d runs", sum.OK(), agent.calls.Load(), DefaultRuns)
	}
	for _, rel := range []string{"summary.json", c.ID + "/run-1/verdict.json", c.ID + "/run-3/judge.json", c.ID + "/run-2/agent.log"} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Errorf("results lack %s", rel)
		}
	}
	b, _ := os.ReadFile(filepath.Join(out, c.ID, "run-1", "verdict.json"))
	if !strings.Contains(string(b), "approve") {
		t.Errorf("run-1 verdict = %s", b)
	}
}

func TestRunWithAndWithoutRule(t *testing.T) {
	_, c := confirmedMiss(t)
	rule := filepath.Join(t.TempDir(), "x.md")
	if err := os.WriteFile(rule, []byte("RULE-X: flag the defect.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agent := &stubAgent{marker: "RULE-X", t: t}
	sum, err := Run(context.Background(), []Case{c}, RunOptions{Agent: agent, Judge: stubJudge{}, WithRules: []string{rule}, Runs: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !sum.OK() {
		t.Fatalf("with rule: %+v", sum)
	}

	dir, err := fleet.RoleRulesDir(c.RepoRoot(), "reviewer-code-review.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := copyFile(rule, filepath.Join(dir, "x.md")); err != nil {
		t.Fatal(err)
	}
	sum, _ = Run(context.Background(), []Case{c}, RunOptions{Agent: agent, Judge: stubJudge{}, Runs: 1})
	if !sum.OK() {
		t.Errorf("placed rule not applied: %+v", sum)
	}
	sum, _ = Run(context.Background(), []Case{c}, RunOptions{Agent: agent, Judge: stubJudge{}, WithoutRules: []string{"x.md"}, Runs: 1})
	if sum.OK() {
		t.Errorf("--without-rule kept the placed rule: %+v", sum)
	}
}

type flakyAgent struct {
	stubAgent
	n atomic.Int32
}

func (a *flakyAgent) Run(ctx context.Context, req AgentRequest, log io.Writer) error {
	if a.n.Add(1) == 1 {
		return fmt.Errorf("agent died")
	}
	req.Prompt += a.marker
	return a.stubAgent.Run(ctx, req, log)
}

func TestRunPassesOnMajority(t *testing.T) {
	_, c := confirmedMiss(t)
	agent := &flakyAgent{stubAgent: stubAgent{marker: "RULE-X", t: t}}
	sum, err := Run(context.Background(), []Case{c}, RunOptions{Agent: agent, Judge: stubJudge{}, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := sum.Result(c.ID)
	if !r.Pass || r.Passed != 2 || !strings.Contains(r.Runs[0].Error, "no verdict") {
		t.Fatalf("result = %+v, want 2/3 pass with run 1 erroring", r)
	}
}

func TestRunFallsBackToBundleWhenBranchIsGone(t *testing.T) {
	f, c := confirmedMiss(t)
	gitT(t, f.root, "branch", "-D", "wt/demo")
	gitT(t, f.root, "reflog", "expire", "--expire=now", "--all")
	gitT(t, f.root, "gc", "-q", "--prune=now")
	if err := exec.Command("git", "-C", f.root, "cat-file", "-e", f.head).Run(); err == nil {
		t.Fatal("head still in the repo; the test proves nothing")
	}
	agent := &stubAgent{marker: "RULE-X", t: t, check: func(t *testing.T, req AgentRequest) {
		if b, _ := os.ReadFile(filepath.Join(req.Dir, "a.txt")); string(b) != "two\n" {
			t.Errorf("checkout a.txt = %q, want head content", b)
		}
	}}
	sum, err := Run(context.Background(), []Case{c}, RunOptions{Agent: agent, Judge: stubJudge{}, Runs: 1})
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := sum.Result(c.ID); r.Runs[0].Error != "" {
		t.Fatalf("replay from bundle: %s", r.Runs[0].Error)
	}
}

func TestHeadlessArgv(t *testing.T) {
	for agent, want := range map[string]string{
		"claude":               "claude -p --dangerously-skip-permissions -- P",
		"/bin/claude --x":      "/bin/claude --x -p --dangerously-skip-permissions -- P",
		"codex":                "codex exec --dangerously-bypass-approvals-and-sandbox -- P",
		"sh -c 'true'":         "sh -c 'true' P",
		"claude-code --flag 1": "claude-code --flag 1 -p --dangerously-skip-permissions -- P",
	} {
		got, err := headlessArgv(agent, "P")
		if err != nil || strings.Join(got, " ") != want {
			t.Errorf("headlessArgv(%q) = %q, %v; want %q", agent, got, err, want)
		}
	}
	if _, err := headlessArgv(" ", "P"); err == nil {
		t.Error("empty agent accepted")
	}
}

func TestParseJudge(t *testing.T) {
	r, err := parseJudge([]byte("sure:\n```json\n{\"pass\": true, \"reason\": \"flags it\"}\n```\n"))
	if err != nil || !r.Pass || r.Reason != "flags it" {
		t.Fatalf("parseJudge = %+v, %v", r, err)
	}
	if _, err := parseJudge([]byte("no idea")); err == nil {
		t.Error("judge output without JSON accepted")
	}
	if p := judgePrompt(JudgeInput{Kind: KindInvalid, Expect: "E", Verdict: []byte("{}")}); !strings.Contains(p, "at any tier") {
		t.Errorf("invalid prompt lacks the any-tier rule: %s", p)
	}
}

// TestLiveEvalRun replays a tiny synthetic miss through the real
// claude CLI and judge. Gated: it spends tokens.
//
//	SPORE_EVAL_LIVE=1 go test ./internal/evals/ -run TestLiveEvalRun -v
func TestLiveEvalRun(t *testing.T) {
	if os.Getenv("SPORE_EVAL_LIVE") == "" {
		t.Skip("SPORE_EVAL_LIVE not set; skipping live eval run")
	}
	_, c := confirmedMiss(t)
	sum, err := Run(context.Background(), []Case{c}, RunOptions{Agent: ExecAgent{}, Judge: ClaudeJudge{}, Runs: 1, Timeout: 15 * time.Minute, Progress: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := sum.Result(c.ID)
	if r.Runs[0].Error != "" {
		t.Fatalf("live run: %s", r.Runs[0].Error)
	}
	t.Logf("live run: pass=%t reason=%s", r.Pass, r.Runs[0].Reason)
}
