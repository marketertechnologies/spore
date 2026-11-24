package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// ExecAgent runs the slot's agent CLI non-interactively: `claude -p`
// or `codex exec`, with the composed role body as the prompt.
type ExecAgent struct{}

// headlessArgv maps a slot's interactive argv (RolePlan.Argv) to its
// one-shot form, keeping every model, effort and config flag the live
// pane gets. Permissions are skipped as in a live pane: the replay
// runs in a throwaway clone.
func headlessArgv(argv []string, prompt string) ([]string, error) {
	if len(argv) == 0 || argv[0] == "" {
		return nil, fmt.Errorf("empty agent command")
	}
	switch filepath.Base(argv[0]) {
	case "claude", "claude-code":
		out := slices.Clone(argv)
		if !slices.Contains(out, "--dangerously-skip-permissions") {
			out = append(out, "--dangerously-skip-permissions")
		}
		return append(out, "-p", "--", prompt), nil
	case "codex":
		out := []string{argv[0], "exec"}
		for _, a := range argv[1:] {
			// TUI-only: `codex exec` rejects it.
			if a != "--no-alt-screen" {
				out = append(out, a)
			}
		}
		if !slices.Contains(out, "--dangerously-bypass-approvals-and-sandbox") {
			out = append(out, "--dangerously-bypass-approvals-and-sandbox")
		}
		return append(out, "--", prompt), nil
	default:
		return append(slices.Clone(argv), prompt), nil
	}
}

func (ExecAgent) Run(ctx context.Context, req AgentRequest, log io.Writer) error {
	argv, err := headlessArgv(req.Argv, req.Prompt)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = req.Dir
	cmd.Env = append(os.Environ(), req.Env...)
	cmd.Stdout, cmd.Stderr = log, log
	return cmd.Run()
}

// DefaultJudgeModel keeps the judge cheap; it only compares a short
// verdict against one or two sentences.
const DefaultJudgeModel = "haiku"

// ClaudeJudge asks `claude -p` for a pass/fail on one verdict.
type ClaudeJudge struct {
	Model string
}

func judgePrompt(in JudgeInput) string {
	rule := "PASS only if a comment tagged [blocker] (or untagged) points at the defect the expectation describes. " +
		"Wording may differ; the same defect in the same place counts. A non-blocker tier or a different defect is a FAIL."
	if in.Kind == KindInvalid {
		rule = "PASS only if no comment, at any tier, raises the rejected point the expectation describes. " +
			"Other comments do not matter."
	}
	return "You grade one code-review verdict against an expectation. " +
		`Reply with only a JSON object: {"pass": true or false, "reason": "<one line>"}.` + "\n\n" +
		"Case kind: " + in.Kind + "\nRule: " + rule + "\n\nExpectation:\n" + in.Expect +
		"\n\nVerdict JSON:\n" + string(in.Verdict) + "\n"
}

// parseJudge pulls the JSON object out of the model's reply.
func parseJudge(out []byte) (JudgeResult, error) {
	s := string(out)
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return JudgeResult{}, fmt.Errorf("no JSON in judge output: %q", strings.TrimSpace(s))
	}
	var r JudgeResult
	if err := json.Unmarshal([]byte(s[i:j+1]), &r); err != nil {
		return JudgeResult{}, fmt.Errorf("judge output: %w", err)
	}
	return r, nil
}

func (j ClaudeJudge) Judge(ctx context.Context, in JudgeInput) (JudgeResult, error) {
	model := j.Model
	if model == "" {
		model = DefaultJudgeModel
	}
	cmd := exec.CommandContext(ctx, "claude", "-p", "--model", model, "--", judgePrompt(in))
	// Outside any repo, so no project instructions reach the judge.
	cmd.Dir = os.TempDir()
	out, err := cmd.Output()
	if err != nil {
		return JudgeResult{}, fmt.Errorf("claude judge: %w", err)
	}
	return parseJudge(out)
}
