package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/versality/spore/internal/fleet"
	"github.com/versality/spore/internal/task"
)

// Defaults for RunOptions.
const (
	DefaultRuns    = 3
	DefaultWorkers = 4
	DefaultTimeout = 30 * time.Minute
)

// AgentRequest is one headless reviewer run.
type AgentRequest struct {
	// Agent is the slot's agent command as the spawn resolves it.
	Agent  string
	Prompt string
	Dir    string
	Env    []string
}

// Agent runs a reviewer headless; it must leave the verdict file
// under the request's SPORE_TASK_DIR.
type Agent interface {
	Run(ctx context.Context, req AgentRequest, log io.Writer) error
}

// JudgeInput is what the judge sees: never the case's code or spec.
type JudgeInput struct {
	Kind    string
	Expect  string
	Verdict []byte
}

// JudgeResult is one judged run.
type JudgeResult struct {
	Pass   bool   `json:"pass"`
	Reason string `json:"reason"`
}

// Judge grades a verdict against a case's expect.
type Judge interface {
	Judge(ctx context.Context, in JudgeInput) (JudgeResult, error)
}

// RunOptions tunes Run. Agent and Judge are required; zero numbers
// take the defaults.
type RunOptions struct {
	Runs    int
	Workers int
	Timeout time.Duration
	// WithRules are rule files appended after the placed rules of
	// every case's body; WithoutRules are placed rule file names
	// dropped. Drafts are tested unplaced, so live panes never see an
	// unproven rule.
	WithRules    []string
	WithoutRules []string
	Agent        Agent
	Judge        Judge
	// OutDir receives per-run verdicts, judge output and the
	// summary. Empty keeps nothing.
	OutDir   string
	Progress io.Writer
}

// RunResult is one replay of one case.
type RunResult struct {
	Pass   bool   `json:"pass"`
	Reason string `json:"reason"`
	Error  string `json:"error,omitempty"`
}

// CaseResult aggregates a case's runs: it passes on a strict
// majority.
type CaseResult struct {
	ID      string      `json:"id"`
	Project string      `json:"project"`
	Passed  int         `json:"passed"`
	Pass    bool        `json:"pass"`
	Runs    []RunResult `json:"runs"`
}

func (c CaseResult) errored() bool {
	for _, r := range c.Runs {
		if r.Error != "" {
			return true
		}
	}
	return false
}

// Summary is a whole suite run.
type Summary struct {
	Cases []CaseResult `json:"cases"`
}

// OK reports whether every case passed.
func (s Summary) OK() bool {
	for _, c := range s.Cases {
		if !c.Pass {
			return false
		}
	}
	return true
}

// Passed counts the passing cases.
func (s Summary) Passed() int {
	n := 0
	for _, c := range s.Cases {
		if c.Pass {
			n++
		}
	}
	return n
}

// Result returns the case result for id.
func (s Summary) Result(id string) (CaseResult, bool) {
	for _, c := range s.Cases {
		if c.ID == id {
			return c, true
		}
	}
	return CaseResult{}, false
}

// Write prints one line per case and an overall line.
func (s Summary) Write(w io.Writer) {
	for _, c := range s.Cases {
		state := "pass"
		if !c.Pass {
			state = "FAIL"
		}
		fmt.Fprintf(w, "%-50s %d/%d  %s\n", c.ID, c.Passed, len(c.Runs), state)
	}
	overall := "pass"
	if !s.OK() {
		overall = "FAIL"
	}
	fmt.Fprintf(w, "overall: %d/%d cases pass: %s\n", s.Passed(), len(s.Cases), overall)
}

type prepared struct {
	c      Case
	plan   fleet.RolePlan
	prompt string
}

// Run replays every case Runs times on Workers parallel workers and
// judges each verdict.
func Run(ctx context.Context, cases []Case, opt RunOptions) (Summary, error) {
	if opt.Agent == nil || opt.Judge == nil {
		return Summary{}, errors.New("eval run: agent and judge are required")
	}
	if opt.Runs <= 0 {
		opt.Runs = DefaultRuns
	}
	if opt.Workers <= 0 {
		opt.Workers = DefaultWorkers
	}
	if opt.Timeout <= 0 {
		opt.Timeout = DefaultTimeout
	}
	extra, err := readRules(opt.WithRules)
	if err != nil {
		return Summary{}, err
	}
	preps := make([]prepared, len(cases))
	for i, c := range cases {
		if preps[i], err = prepare(c, extra, opt.WithoutRules); err != nil {
			return Summary{}, err
		}
	}

	type job struct{ ci, run int }
	jobs := make(chan job)
	results := make([][]RunResult, len(cases))
	for i := range results {
		results[i] = make([]RunResult, opt.Runs)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < opt.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				r := replay(ctx, preps[j.ci], j.run, opt)
				mu.Lock()
				results[j.ci][j.run] = r
				if opt.Progress != nil {
					fmt.Fprintf(opt.Progress, "%s run %d: pass=%t %s%s\n", preps[j.ci].c.ID, j.run+1, r.Pass, r.Reason, r.Error)
				}
				mu.Unlock()
			}
		}()
	}
	for ci := range cases {
		for r := 0; r < opt.Runs; r++ {
			jobs <- job{ci, r}
		}
	}
	close(jobs)
	wg.Wait()

	var sum Summary
	for i, c := range cases {
		cr := CaseResult{ID: c.ID, Project: c.Project, Runs: results[i]}
		for _, r := range results[i] {
			if r.Pass {
				cr.Passed++
			}
		}
		cr.Pass = cr.Passed*2 > len(cr.Runs)
		sum.Cases = append(sum.Cases, cr)
	}
	if opt.OutDir != "" {
		b, _ := json.MarshalIndent(sum, "", "  ")
		if err := os.WriteFile(filepath.Join(opt.OutDir, "summary.json"), append(b, '\n'), 0o644); err != nil {
			return sum, err
		}
	}
	return sum, ctx.Err()
}

func readRules(paths []string) ([]fleet.RoleRule, error) {
	var out []fleet.RoleRule
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("eval run: rule %s: %w", p, err)
		}
		out = append(out, fleet.RoleRule{Name: filepath.Base(p), Body: b})
	}
	return out, nil
}

// prepare settles a case's slot config and composed prompt once,
// the way a live spawn would for that slot today.
func prepare(c Case, extra []fleet.RoleRule, without []string) (prepared, error) {
	root := c.RepoRoot()
	plan, err := fleet.ReviewerSpec(root, c.SourceSlug, task.ReviewerInstance(c.Reviewer)).Plan()
	if err != nil {
		return prepared{}, fmt.Errorf("case %s: %w", c.ID, err)
	}
	path, err := fleet.ResolveRoleBody(root, plan.Body)
	if err != nil {
		return prepared{}, fmt.Errorf("case %s: %w", c.ID, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return prepared{}, err
	}
	placed, err := fleet.LoadRoleRules(root, plan.Body)
	if err != nil {
		return prepared{}, err
	}
	rules := slices.DeleteFunc(placed, func(r fleet.RoleRule) bool { return slices.Contains(without, r.Name) })
	rules = append(rules, extra...)
	return prepared{c: c, plan: plan, prompt: string(fleet.ComposeRoleBody(body, rules))}, nil
}

func (p prepared) env(repo, taskDir string) []string {
	env := []string{
		"SPORE_TASK_SLUG=" + p.c.SourceSlug,
		"SPORE_PROJECT_ROOT=" + repo,
		"WT_PROJECT=" + p.c.Project,
		"SPORE_TASK_DIR=" + taskDir,
		"SPORE_ROLE=reviewer",
		"SPORE_REVIEWER_INSTANCE=" + p.c.Reviewer,
		"SPORE_REVIEWER_MODE=" + p.plan.Slot.Mode,
	}
	if p.plan.Slot.Mode == fleet.ReviewerModeCodeReview {
		env = append(env, "SPORE_REVIEW_EFFORT="+p.plan.Slot.Effort)
	}
	return env
}

func replay(ctx context.Context, p prepared, run int, opt RunOptions) RunResult {
	var out string
	if opt.OutDir != "" {
		out = filepath.Join(opt.OutDir, p.c.ID, fmt.Sprintf("run-%d", run+1))
		_ = os.MkdirAll(out, 0o755)
	}
	r, verdict, log := replayOnce(ctx, p, opt)
	if out != "" {
		_ = os.WriteFile(filepath.Join(out, "agent.log"), log, 0o644)
		if verdict != nil {
			_ = os.WriteFile(filepath.Join(out, "verdict.json"), verdict, 0o644)
		}
		b, _ := json.MarshalIndent(r, "", "  ")
		_ = os.WriteFile(filepath.Join(out, "judge.json"), append(b, '\n'), 0o644)
	}
	return r
}

func replayOnce(ctx context.Context, p prepared, opt RunOptions) (RunResult, []byte, []byte) {
	fail := func(err error) RunResult { return RunResult{Error: err.Error()} }
	tmp, err := os.MkdirTemp("", "spore-eval-")
	if err != nil {
		return fail(err), nil, nil
	}
	defer os.RemoveAll(tmp)
	repo, taskDir := filepath.Join(tmp, "repo"), filepath.Join(tmp, "task")
	if err := checkout(ctx, p.c, repo); err != nil {
		return fail(err), nil, nil
	}
	if err := stageTaskDir(p.c, taskDir); err != nil {
		return fail(err), nil, nil
	}

	var log strings.Builder
	actx, cancel := context.WithTimeout(ctx, opt.Timeout)
	err = opt.Agent.Run(actx, AgentRequest{Agent: p.plan.Agent, Prompt: p.prompt, Dir: repo, Env: p.env(repo, taskDir)}, &log)
	cancel()
	verdict, verr := collectVerdict(p.c, taskDir)
	if verr != nil {
		if err != nil {
			verr = fmt.Errorf("%w (agent: %v)", verr, err)
		}
		return fail(verr), nil, []byte(log.String())
	}
	jctx, jcancel := context.WithTimeout(ctx, opt.Timeout)
	jr, err := opt.Judge.Judge(jctx, JudgeInput{Kind: p.c.Kind, Expect: p.c.Expect, Verdict: verdict})
	jcancel()
	if err != nil {
		return fail(fmt.Errorf("judge: %w", err)), verdict, []byte(log.String())
	}
	return RunResult{Pass: jr.Pass, Reason: jr.Reason}, verdict, []byte(log.String())
}

// stageTaskDir builds the scratch SPORE_TASK_DIR: spec, responses
// and the slot's earlier verdicts. No github.json, so the reviewer
// takes the no-PR path and posts nothing.
func stageTaskDir(c Case, dir string) error {
	if err := copyFile(filepath.Join(c.Dir, "spec.md"), filepath.Join(dir, "spec.md")); err != nil {
		return err
	}
	for _, sub := range []string{"responses", "reviews"} {
		if err := copyTree(filepath.Join(c.Dir, sub), filepath.Join(dir, sub)); err != nil {
			return err
		}
	}
	return os.MkdirAll(filepath.Join(dir, "reviews", c.Reviewer), 0o755)
}

// collectVerdict reads the replayed round's verdict; a reviewer that
// numbered its file differently still counts if it wrote exactly one
// new round.
func collectVerdict(c Case, taskDir string) ([]byte, error) {
	dir := filepath.Join(taskDir, "reviews", c.Reviewer)
	path := filepath.Join(dir, fmt.Sprintf("round-%d.json", c.Round))
	if _, err := os.Stat(path); err != nil {
		var fresh []string
		for r := c.Round + 1; r <= c.Round+fleet.MaxReviewerRounds; r++ {
			p := filepath.Join(dir, fmt.Sprintf("round-%d.json", r))
			if _, err := os.Stat(p); err == nil {
				fresh = append(fresh, p)
			}
		}
		if len(fresh) != 1 {
			return nil, fmt.Errorf("no verdict at reviews/%s/round-%d.json", c.Reviewer, c.Round)
		}
		path = fresh[0]
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if _, err := task.ParseReview(path, b, task.ReviewerInstance(c.Reviewer), false); err != nil {
		return nil, err
	}
	return b, nil
}
