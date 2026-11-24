package evals

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/versality/spore/internal/fleet"
)

func gateFixture(t *testing.T) (fixture, string) {
	t.Helper()
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.store.Confirm(ctx, "demo.miss.A2.B1", ConfirmOptions{HeadSHA: f.head, Expect: "flags the defect"}); err != nil {
		t.Fatal(err)
	}
	rule := filepath.Join(t.TempDir(), "flag-defect.md")
	if err := os.WriteFile(rule, []byte("RULE-X: flag the defect.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f, rule
}

func gateRun(agent Agent) RunOptions {
	return RunOptions{Agent: agent, Judge: stubJudge{}, Runs: 1}
}

func TestPromotePlacesRuleOnGreen(t *testing.T) {
	f, rule := gateFixture(t)
	res, dst, err := f.store.Promote(context.Background(), GateOptions{Rule: rule, Motivating: []string{"demo.miss.A2.B1"}, Run: gateRun(&stubAgent{marker: "RULE-X", t: t})})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Body != "reviewer-code-review.md" {
		t.Fatalf("gate = %+v", res)
	}
	dir, _ := fleet.RoleRulesDir(f.root, res.Body)
	if dst != filepath.Join(dir, "flag-defect.md") {
		t.Errorf("placed at %s", dst)
	}
	c, err := LoadCase(filepath.Join(f.store.CasesDir(), "demo.miss.A2.B1"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Rule != "flag-defect.md" {
		t.Errorf("case rule = %q", c.Rule)
	}
}

func TestPromoteRefusesRuleThatBringsBackAnInvalidComment(t *testing.T) {
	f, rule := gateFixture(t)
	if _, err := f.store.Confirm(context.Background(), "demo.invalid.A1.eng2.p1", ConfirmOptions{HeadSHA: f.head, Expect: "no defect comment"}); err != nil {
		t.Fatal(err)
	}
	res, dst, err := f.store.Promote(context.Background(), GateOptions{Rule: rule, Motivating: []string{"demo.miss.A2.B1"}, Run: gateRun(&stubAgent{marker: "RULE-X", t: t})})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || dst != "" || !strings.Contains(strings.Join(res.Reasons, "\n"), "demo.invalid.A1.eng2.p1 fails with the rule") {
		t.Fatalf("gate = %+v, dst %q", res, dst)
	}
	dir, _ := fleet.RoleRulesDir(f.root, res.Body)
	if _, err := os.Stat(filepath.Join(dir, "flag-defect.md")); err == nil {
		t.Error("red gate placed the rule")
	}
}

func TestGateRedWhenCasePassesWithoutRule(t *testing.T) {
	f, rule := gateFixture(t)
	cases, _ := f.store.Cases()
	res, err := Gate(context.Background(), cases, GateOptions{Rule: rule, Motivating: []string{"demo.miss.A2.B1"}, Run: gateRun(&stubAgent{marker: "", t: t})})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || !strings.Contains(res.Reasons[0], "passes without the rule") {
		t.Fatalf("gate = %+v", res)
	}
	if _, err := Gate(context.Background(), cases, GateOptions{Rule: rule, Run: gateRun(&stubAgent{t: t})}); err == nil {
		t.Error("gate without a motivating case accepted")
	}
}

func TestKernelSummaryHasBareCountsOnly(t *testing.T) {
	f, rule := gateFixture(t)
	cases, err := f.store.HostCases()
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 {
		t.Fatalf("host cases = %d, want the one confirmed", len(cases))
	}
	res, err := Gate(context.Background(), cases, GateOptions{Rule: rule, Motivating: []string{"demo.miss.A2.B1"}, Run: gateRun(&stubAgent{marker: "RULE-X", t: t})})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	f.store.WriteKernelSummary(&b, res, cases)
	out := b.String()
	for _, leak := range []string{"demo", "acme", "flags the defect"} {
		if strings.Contains(out, leak) {
			t.Errorf("kernel summary leaks %q:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "project 1: 1 cases, 1 passed, 0 failed") || !strings.Contains(out, "gate: green") {
		t.Errorf("summary:\n%s", out)
	}
}
