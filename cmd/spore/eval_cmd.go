package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/versality/spore/internal/evals"
	"github.com/versality/spore/internal/task"
)

const evalUsage = `spore eval - reviewer regression suite

Usage:
  spore eval candidates [--project PATH]
  spore eval flag <slug> <reviewer> <round> <comment-ref> [--why "..."]
  spore eval confirm <candidate> [--expect "..."] [--head-sha SHA] [--base BRANCH] [--kernel]
  spore eval dismiss <candidate>
  spore eval run [--case ID]... [--runs N] [--workers N] [--with-rule FILE]... [--without-rule NAME]...
  spore eval promote FILE --case ID [--case ID]... [--body NAME] [--kernel]

Subcommands:
  candidates  List detected reviewer misses (a slot approved, a later
              slot requested changes) and invalid comments (engineer
              pushback, operator flags) not yet confirmed or dismissed.
  flag        Mark a reviewer comment a non-issue. <comment-ref> is the
              1-based comment index or text matching one comment.
  confirm     Write the case dir. --expect defaults to a draft built from
              the evidence; --head-sha overrides the recovered reviewed
              commit. --kernel writes under the repo's evals/cases/ (spore's
              own runs only: kernel cases are public).
  dismiss     Record a no, so the candidate stops showing.
  run         Replay and judge cases; exits 1 unless every case passes.
              --with-rule appends a draft rule (unplaced) to every case's
              body; --without-rule drops a placed rule by file name.
  promote     Run the ship gate for a draft rule: the --case cases fail
              without it and pass with it, every case on the same body
              passes with it. Green places it under <state>/roles/rules/.
              --kernel gates kernel plus every host project's cases and
              prints bare counts for the PR instead of placing anything.

Flags (all):
  --project PATH  Project repo. Default: $SPORE_PROJECT_ROOT, else the cwd
                  repo (a worktree resolves to its main repo).
Flags (run, promote):
  --runs N        Replays per case; a case passes on a strict majority (3).
  --workers N     Parallel replays (4).
  --timeout D     Per-replay agent timeout (30m).
  --judge-model M Judge model for claude -p (haiku).

Cases, flags, dismissals and run results live under
$XDG_STATE_HOME/spore/<project>/evals/. Each run spends real agent
tokens: run it before a rule change ships, not on every push.
`

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

type evalFlags struct {
	fs                *flag.FlagSet
	project           *string
	runs, workers     *int
	timeout           *time.Duration
	judgeModel        *string
	cases, with, wout multiFlag
}

func newEvalFlags(name string, runner bool) *evalFlags {
	f := &evalFlags{fs: flag.NewFlagSet("eval "+name, flag.ContinueOnError)}
	f.fs.SetOutput(io.Discard)
	f.project = f.fs.String("project", "", "project repo")
	if runner {
		f.runs = f.fs.Int("runs", evals.DefaultRuns, "replays per case")
		f.workers = f.fs.Int("workers", evals.DefaultWorkers, "parallel replays")
		f.timeout = f.fs.Duration("timeout", evals.DefaultTimeout, "per-replay agent timeout")
		f.judgeModel = f.fs.String("judge-model", evals.DefaultJudgeModel, "judge model")
		f.fs.Var(&f.cases, "case", "case id (repeatable)")
	}
	return f
}

func (f *evalFlags) parse(args []string) error {
	return f.fs.Parse(reorderFlagsFirst(f.fs, args))
}

func (f *evalFlags) store() (evals.Store, error) {
	p := *f.project
	if p == "" {
		p = os.Getenv("SPORE_PROJECT_ROOT")
	}
	root, err := task.MainRepoRoot(p)
	if err != nil {
		return evals.Store{}, err
	}
	return evals.OpenStore(root)
}

func (f *evalFlags) runOptions(out string) evals.RunOptions {
	return evals.RunOptions{
		Runs: *f.runs, Workers: *f.workers, Timeout: *f.timeout,
		WithRules: f.with, WithoutRules: f.wout,
		Agent: evals.ExecAgent{}, Judge: evals.ClaudeJudge{Model: *f.judgeModel},
		OutDir: out, Progress: os.Stderr,
	}
}

func runEval(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, evalUsage)
		return 2
	}
	var err error
	code := 0
	switch sub, rest := args[0], args[1:]; sub {
	case "-h", "--help", "help":
		fmt.Print(evalUsage)
		return 0
	case "candidates":
		err = evalCandidates(rest)
	case "flag":
		err = evalFlag(rest)
	case "confirm":
		err = evalConfirm(rest)
	case "dismiss":
		err = evalDismiss(rest)
	case "run":
		code, err = evalRun(rest)
	case "promote":
		code, err = evalPromote(rest)
	default:
		fmt.Fprintf(os.Stderr, "spore eval: unknown subcommand %q\n\n%s", sub, evalUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore eval:", err)
		return 1
	}
	return code
}

func evalCandidates(args []string) error {
	f := newEvalFlags("candidates", false)
	if err := f.parse(args); err != nil {
		return err
	}
	st, err := f.store()
	if err != nil {
		return err
	}
	cs, err := st.Candidates()
	if err != nil {
		return err
	}
	for _, c := range cs {
		ev := strings.Join(c.Evidence, " / ")
		if len(ev) > 100 {
			ev = ev[:97] + "..."
		}
		fmt.Printf("%s\t%s\t%s\t%s\n", c.ID, c.Kind, c.Source, ev)
	}
	if len(cs) == 0 {
		fmt.Fprintln(os.Stderr, "no open candidates")
	}
	return nil
}

func evalFlag(args []string) error {
	f := newEvalFlags("flag", false)
	why := f.fs.String("why", "", "why the comment is a non-issue")
	if err := f.parse(args); err != nil {
		return err
	}
	if f.fs.NArg() != 4 {
		return fmt.Errorf("flag: want <slug> <reviewer> <round> <comment-ref>")
	}
	round, err := strconv.Atoi(f.fs.Arg(2))
	if err != nil {
		return fmt.Errorf("flag: round %q: %w", f.fs.Arg(2), err)
	}
	st, err := f.store()
	if err != nil {
		return err
	}
	id, err := st.AddFlag(f.fs.Arg(0), task.ReviewerInstance(f.fs.Arg(1)), round, f.fs.Arg(3), *why)
	if err != nil {
		return err
	}
	fmt.Println(id)
	return nil
}

func evalConfirm(args []string) error {
	f := newEvalFlags("confirm", false)
	var opt evals.ConfirmOptions
	f.fs.StringVar(&opt.Expect, "expect", "", "judge-facing expectation")
	f.fs.StringVar(&opt.HeadSHA, "head-sha", "", "commit the reviewer reviewed")
	f.fs.StringVar(&opt.BaseBranch, "base", "", "review base branch")
	f.fs.StringVar(&opt.ConfirmedBy, "by", os.Getenv("USER"), "who confirmed")
	f.fs.BoolVar(&opt.Kernel, "kernel", false, "write a kernel case into the repo")
	if err := f.parse(args); err != nil {
		return err
	}
	if f.fs.NArg() != 1 {
		return fmt.Errorf("confirm: want one <candidate>")
	}
	st, err := f.store()
	if err != nil {
		return err
	}
	c, err := st.Confirm(context.Background(), f.fs.Arg(0), opt)
	if err != nil {
		return err
	}
	fmt.Printf("%s\nexpect: %s\n", c.Dir, c.Expect)
	return nil
}

func evalDismiss(args []string) error {
	f := newEvalFlags("dismiss", false)
	if err := f.parse(args); err != nil {
		return err
	}
	if f.fs.NArg() != 1 {
		return fmt.Errorf("dismiss: want one <candidate>")
	}
	st, err := f.store()
	if err != nil {
		return err
	}
	return st.Dismiss(f.fs.Arg(0))
}

func selectCases(all []evals.Case, ids []string) ([]evals.Case, error) {
	if len(ids) == 0 {
		return all, nil
	}
	var out []evals.Case
	for _, id := range ids {
		found := false
		for _, c := range all {
			if c.ID == id {
				out, found = append(out, c), true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("no case %q", id)
		}
	}
	return out, nil
}

func newRunDir(st evals.Store) (string, error) {
	dir := filepath.Join(st.RunsDir(), time.Now().UTC().Format("20060102T150405Z"))
	return dir, os.MkdirAll(dir, 0o755)
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func evalRun(args []string) (int, error) {
	f := newEvalFlags("run", true)
	f.fs.Var(&f.with, "with-rule", "draft rule file to append (repeatable)")
	f.fs.Var(&f.wout, "without-rule", "placed rule file name to drop (repeatable)")
	if err := f.parse(args); err != nil {
		return 0, err
	}
	st, err := f.store()
	if err != nil {
		return 0, err
	}
	all, err := st.Cases()
	if err != nil {
		return 0, err
	}
	cases, err := selectCases(all, f.cases)
	if err != nil {
		return 0, err
	}
	if len(cases) == 0 {
		return 0, fmt.Errorf("run: no cases (confirm one with `spore eval confirm`)")
	}
	out, err := newRunDir(st)
	if err != nil {
		return 0, err
	}
	ctx, cancel := signalContext()
	defer cancel()
	sum, err := evals.Run(ctx, cases, f.runOptions(out))
	sum.Write(os.Stdout)
	fmt.Println("results:", out)
	if err != nil {
		return 0, err
	}
	if !sum.OK() {
		return 1, nil
	}
	return 0, nil
}

func evalPromote(args []string) (int, error) {
	f := newEvalFlags("promote", true)
	body := f.fs.String("body", "", "role body file the rule targets (default: the --case body)")
	kernel := f.fs.Bool("kernel", false, "gate a kernel rule over every project; print the PR summary")
	if err := f.parse(args); err != nil {
		return 0, err
	}
	if f.fs.NArg() != 1 {
		return 0, fmt.Errorf("promote: want one rule FILE")
	}
	st, err := f.store()
	if err != nil {
		return 0, err
	}
	out, err := newRunDir(st)
	if err != nil {
		return 0, err
	}
	ctx, cancel := signalContext()
	defer cancel()
	opt := evals.GateOptions{Rule: f.fs.Arg(0), Body: *body, Motivating: f.cases, Run: f.runOptions(out)}
	var res evals.GateResult
	if *kernel {
		cases, err := st.HostCases()
		if err != nil {
			return 0, err
		}
		if res, err = evals.Gate(ctx, cases, opt); err != nil {
			return 0, err
		}
		st.WriteKernelSummary(os.Stdout, res, cases)
	} else {
		var dst string
		if res, dst, err = st.Promote(ctx, opt); err != nil {
			return 0, err
		}
		res.With.Write(os.Stdout)
		if dst != "" {
			fmt.Println("placed:", dst)
		}
	}
	for _, r := range res.Reasons {
		fmt.Println("gate:", r)
	}
	fmt.Println("results:", out)
	if !res.OK {
		return 1, nil
	}
	return 0, nil
}
