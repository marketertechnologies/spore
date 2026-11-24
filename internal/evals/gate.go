package evals

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/versality/spore/internal/fleet"
	"github.com/versality/spore/internal/task"
)

// GateOptions drives one ship-gate check of a draft rule.
type GateOptions struct {
	Rule string
	// Body is the role body file the rule targets. Empty takes the
	// body of the motivating cases.
	Body string
	// Motivating are the cases the rule was written for: each must
	// fail without it and pass with it.
	Motivating []string
	Run        RunOptions
}

// GateResult reports both runs and the decision.
type GateResult struct {
	Body    string
	Without Summary
	With    Summary
	OK      bool
	Reasons []string
}

func caseBody(c Case) (string, error) {
	plan, err := fleet.ReviewerSpec(c.RepoRoot(), c.SourceSlug, task.ReviewerInstance(c.Reviewer)).Plan()
	if err != nil {
		return "", fmt.Errorf("case %s: %w", c.ID, err)
	}
	return plan.Body, nil
}

// Gate runs the ship gate over cases: the motivating cases fail
// without the rule and pass with it, and every case whose reviewer
// runs the same body passes with it. Both kinds gate, so a rule that
// fixes a miss cannot bring back an invalid comment, or the reverse.
func Gate(ctx context.Context, cases []Case, opt GateOptions) (GateResult, error) {
	if len(opt.Motivating) == 0 {
		return GateResult{}, fmt.Errorf("gate: name the motivating case (--case)")
	}
	byID := map[string]Case{}
	bodies := map[string]string{}
	for _, c := range cases {
		b, err := caseBody(c)
		if err != nil {
			return GateResult{}, err
		}
		byID[c.ID], bodies[c.ID] = c, b
	}
	body := opt.Body
	for _, id := range opt.Motivating {
		if _, ok := byID[id]; !ok {
			return GateResult{}, fmt.Errorf("gate: no case %q", id)
		}
		if body == "" {
			body = bodies[id]
		}
		if bodies[id] != body {
			return GateResult{}, fmt.Errorf("gate: case %s runs %s, not %s", id, bodies[id], body)
		}
	}
	var gated, motivating []Case
	for _, c := range cases {
		if bodies[c.ID] != body {
			continue
		}
		gated = append(gated, c)
		if slices.Contains(opt.Motivating, c.ID) {
			motivating = append(motivating, c)
		}
	}

	// A placed copy of the same file would make "without" a lie.
	name := filepath.Base(opt.Rule)
	without := opt.Run
	without.WithoutRules = append(slices.Clone(without.WithoutRules), name)
	without.WithRules = nil
	with := without
	with.WithRules = []string{opt.Rule}
	if opt.Run.OutDir != "" {
		without.OutDir = filepath.Join(opt.Run.OutDir, "without-rule")
		with.OutDir = filepath.Join(opt.Run.OutDir, "with-rule")
		for _, d := range []string{without.OutDir, with.OutDir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return GateResult{}, err
			}
		}
	}
	res := GateResult{Body: body}
	var err error
	if res.Without, err = Run(ctx, motivating, without); err != nil {
		return res, err
	}
	if res.With, err = Run(ctx, gated, with); err != nil {
		return res, err
	}
	for _, c := range motivating {
		r, _ := res.Without.Result(c.ID)
		switch {
		case r.Pass:
			res.Reasons = append(res.Reasons, fmt.Sprintf("%s passes without the rule: the rule is not what fixes it", c.ID))
		case r.errored():
			// An errored run is no evidence the case fails without the
			// rule; counting it as a fail would ship an unproven rule.
			res.Reasons = append(res.Reasons, fmt.Sprintf("%s: a without-rule run errored, so the baseline is inconclusive", c.ID))
		}
	}
	for _, r := range res.With.Cases {
		if !r.Pass {
			res.Reasons = append(res.Reasons, fmt.Sprintf("%s fails with the rule", r.ID))
		}
	}
	res.OK = len(res.Reasons) == 0
	return res, nil
}

// Promote gates a draft project rule over this project's cases and,
// on green, places it under <state>/roles/rules/<body>/ and records it
// on the motivating cases.
func (s Store) Promote(ctx context.Context, opt GateOptions) (GateResult, string, error) {
	cases, err := s.Cases()
	if err != nil {
		return GateResult{}, "", err
	}
	res, err := Gate(ctx, cases, opt)
	if err != nil || !res.OK {
		return res, "", err
	}
	dir, err := fleet.RoleRulesDir(s.ProjectRoot, res.Body)
	if err != nil {
		return res, "", err
	}
	dst := filepath.Join(dir, filepath.Base(opt.Rule))
	if err := copyFile(opt.Rule, dst); err != nil {
		return res, "", err
	}
	for _, c := range cases {
		if !slices.Contains(opt.Motivating, c.ID) {
			continue
		}
		c.Rule = filepath.Base(dst)
		if err := os.WriteFile(filepath.Join(c.Dir, "case.toml"), c.Marshal(), 0o644); err != nil {
			return res, dst, err
		}
	}
	return res, dst, nil
}

// HostCases returns the cases of every project under the host state
// base, plus this repo's kernel cases. A kernel rule reaches every
// project, so its gate runs them all.
func (s Store) HostCases() ([]Case, error) {
	kernel, err := LoadCases(s.KernelCasesDir())
	if err != nil {
		return nil, err
	}
	projects, err := os.ReadDir(filepath.Dir(s.State))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	out := kernel
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		cs, err := LoadCases(filepath.Join(filepath.Dir(s.State), p.Name(), "evals", "cases"))
		if err != nil {
			return nil, err
		}
		out = append(out, cs...)
	}
	return out, nil
}

// WriteKernelSummary prints the gate as bare counts per project for a
// public PR: the kernel's own cases by name, host projects only as
// "project N", and no case ids or expect text.
func (s Store) WriteKernelSummary(w io.Writer, res GateResult, cases []Case) {
	kernelDir := s.KernelCasesDir()
	label := map[string]string{}
	var projects []string
	for _, c := range cases {
		if filepath.Dir(c.Dir) == kernelDir {
			label[c.ID] = "kernel"
			continue
		}
		if !slices.Contains(projects, c.Project) {
			projects = append(projects, c.Project)
		}
	}
	sort.Strings(projects)
	for _, c := range cases {
		if label[c.ID] == "" {
			label[c.ID] = fmt.Sprintf("project %d", slices.Index(projects, c.Project)+1)
		}
	}
	type count struct{ cases, passed int }
	counts := map[string]*count{}
	var order []string
	for _, r := range res.With.Cases {
		l := label[r.ID]
		if counts[l] == nil {
			counts[l] = &count{}
			order = append(order, l)
		}
		counts[l].cases++
		if r.Pass {
			counts[l].passed++
		}
	}
	sort.Strings(order)
	fmt.Fprintf(w, "spore eval gate (body %s, with rule):\n", res.Body)
	for _, l := range order {
		c := counts[l]
		fmt.Fprintf(w, "  %s: %d cases, %d passed, %d failed\n", l, c.cases, c.passed, c.cases-c.passed)
	}
	failWithout := 0
	for _, r := range res.Without.Cases {
		if !r.Pass {
			failWithout++
		}
	}
	fmt.Fprintf(w, "  motivating: %d of %d fail without the rule\n", failWithout, len(res.Without.Cases))
	verdict := "green"
	if !res.OK {
		verdict = "red"
	}
	fmt.Fprintf(w, "  gate: %s\n", verdict)
}
