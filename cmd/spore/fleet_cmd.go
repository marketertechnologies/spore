package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/versality/spore/internal/budget"
	"github.com/versality/spore/internal/callbudget"
	"github.com/versality/spore/internal/fleet"
	"github.com/versality/spore/internal/task"

	// Linear is the first matter adapter; the side-effect import
	// fires its init() so [matter.linear] in spore.toml (or
	// SPORE_MATTER_LINEAR__* env) wires through fleet.Reconcile's
	// matter prelude pass.
	_ "github.com/versality/spore/internal/matter/linear"
)

const fleetUsage = `spore fleet - reconcile worker tmux sessions against the task queue

Usage:
  spore fleet reconcile [--max-workers N]
  spore fleet replenish-hook
  spore fleet enable
  spore fleet disable
  spore fleet status
  spore fleet worker-agent [claude|codex | --clear]

Subcommands:
  reconcile       Run a single reconcile pass: when [matter.linear] is
                  set in spore.toml, poll Linear for ready/done updates
                  first; then list status=active tasks; for each one
                  without a live tmux session, ensure the worktree and
                  spawn a session; for each spore-prefix tmux session
                  whose task is no longer active, kill it. Idempotent;
                  exits 0 when there is nothing to do. Short-circuits when
                  the kill-switch flag is missing.
  replenish-hook  Stop-hook variant of reconcile: reads context from env
                  ($SPORE_TASK_INBOX, $SPORE_PROJECT_ROOT, $WT_PROJECT,
                  $WT_FLEET_FLOOR), no-ops in non-coordinator sessions and
                  when the cwd repo is not $SPORE_PROJECT_ROOT (else a repo
                  named $WT_PROJECT), skips when the budget advice is
                  tighten/ration, and never propagates errors.
  enable          Create the kill-switch flag (the reconciler resumes
                  spawning on the next pass).
  disable         Remove the kill-switch flag (the reconciler stops
                  spawning; running sessions are left alone).
  status          Print the kill-switch state plus the list of slugs whose
                  session is currently alive.
  worker-agent    Print the effective [fleet.workers] default agent and its
                  source. With claude|codex, write it to the host-side
                  overlay (<state dir>/workers.toml, which beats
                  spore.toml; the project tree is never written). --clear
                  drops the overlay key. Project: $SPORE_PROJECT_ROOT,
                  else the cwd repo (a worktree resolves to its main repo).

Flags (reconcile):
  --max-workers N   Override concurrency cap (worker sessions plus live
                    role loops, one slot each). Beats spore.toml.

Time budgets (Go durations): each git/tmux/matter call gets
$SPORE_FLEET_CALL_TIMEOUT (default 2m); the whole pass gets
$SPORE_FLEET_PASS_TIMEOUT (default 10m), after which in-flight calls
are killed and reconcile exits non-zero.
`

func runFleet(args []string) error {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, fleetUsage)
		return fmt.Errorf("subcommand required")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "-h", "--help", "help":
		fmt.Print(fleetUsage)
		return nil
	case "reconcile":
		return runFleetReconcile(rest)
	case "replenish-hook":
		return runFleetReplenishHook(rest)
	case "enable":
		return runFleetEnable(rest)
	case "disable":
		return runFleetDisable(rest)
	case "status":
		return runFleetStatus(rest)
	case "worker-agent":
		return runFleetWorkerAgent(rest)
	default:
		return fmt.Errorf("unknown subcommand %q\n\n%s", sub, fleetUsage)
	}
}

func runFleetReconcile(args []string) error {
	fs := flag.NewFlagSet("fleet reconcile", flag.ContinueOnError)
	maxWorkers := fs.Int("max-workers", 0, "concurrency cap (0 = use spore.toml or default)")
	help := fs.Bool("h", false, "show help")
	helpLong := fs.Bool("help", false, "show help")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(reorderFlagsFirst(fs, args)); err != nil {
		return err
	}
	if *help || *helpLong {
		fmt.Print(fleetUsage)
		return nil
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional args: %v", fs.Args())
	}

	root, err := os.Getwd()
	if err != nil {
		return err
	}
	resolved, err := resolveMaxWorkers(*maxWorkers, root)
	if err != nil {
		return err
	}

	res, err := reconcileBudgeted(fleet.Config{
		TasksDir:    "tasks",
		ProjectRoot: root,
		MaxWorkers:  resolved,
	})
	if err != nil {
		return err
	}
	if res.Locked {
		return nil
	}
	if res.Disabled {
		flagPath, _ := fleet.FlagPath()
		fmt.Printf("fleet: disabled (flag missing at %s)\n", flagPath)
		return nil
	}
	fmt.Printf("fleet: active=%d spawned=%d kept=%d reaped=%d skipped=%d\n",
		len(res.Active), len(res.Spawned), len(res.Kept), len(res.Reaped), len(res.Skipped))
	if len(res.Spawned) > 0 {
		fmt.Printf("  spawned: %s\n", strings.Join(res.Spawned, ", "))
	}
	if len(res.Reaped) > 0 {
		fmt.Printf("  reaped: %s\n", strings.Join(res.Reaped, ", "))
	}
	if len(res.Skipped) > 0 {
		fmt.Printf("  skipped: %s (max-workers=%d)\n", strings.Join(res.Skipped, ", "), resolved)
	}
	if len(res.Crashed) > 0 {
		fmt.Printf("  crash-looped (respawn stopped): %s\n", strings.Join(res.Crashed, ", "))
	}
	for _, m := range res.Matter {
		if m.Err != nil {
			fmt.Fprintf(os.Stderr, "  matter %s: %v\n", m.Name, m.Err)
			continue
		}
		fmt.Printf("  matter %s: created=%d updated=%d\n", m.Name, m.Created, m.Updated)
	}
	return nil
}

func runFleetEnable(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: spore fleet enable")
	}
	if err := fleet.Enable(); err != nil {
		return err
	}
	p, _ := fleet.FlagPath()
	fmt.Printf("fleet: enabled (%s)\n", p)
	return nil
}

func runFleetDisable(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: spore fleet disable")
	}
	if err := fleet.Disable(); err != nil {
		return err
	}
	p, _ := fleet.FlagPath()
	fmt.Printf("fleet: disabled (%s removed)\n", p)
	return nil
}

func runFleetStatus(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: spore fleet status")
	}
	on, err := fleet.Enabled()
	if err != nil {
		return err
	}
	if on {
		fmt.Println("fleet: enabled")
	} else {
		fmt.Println("fleet: disabled")
	}

	root, err := os.Getwd()
	if err != nil {
		return err
	}
	slugs, err := task.SpawnedSlugs(root)
	if err != nil {
		return err
	}
	if len(slugs) == 0 {
		fmt.Println("sessions: none")
		return nil
	}
	fmt.Println("sessions:")
	for _, s := range slugs {
		fmt.Printf("  %s\n", s)
	}
	return nil
}

func resolveMaxWorkers(flagVal int, projectRoot string) (int, error) {
	if flagVal > 0 {
		return flagVal, nil
	}
	if env := os.Getenv("SPORE_FLEET_MAX_WORKERS"); env != "" {
		n, err := strconv.Atoi(env)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("SPORE_FLEET_MAX_WORKERS=%q: want positive integer", env)
		}
		return n, nil
	}
	if env := os.Getenv("WT_FLEET_FLOOR"); env != "" {
		n, err := strconv.Atoi(env)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("WT_FLEET_FLOOR=%q: want positive integer", env)
		}
		return n, nil
	}
	return fleet.LoadMaxWorkers(projectRoot)
}

// replenishReconcile is a test seam: the real Reconcile drives tmux.
var replenishReconcile = fleet.Reconcile

// passAbort is a test seam: the real one kills the abandoned pass's
// calls and is permanent for the process.
var passAbort = callbudget.Abort

// PassTimeoutEnv overrides defaultPassTimeout with a Go duration.
const PassTimeoutEnv = "SPORE_FLEET_PASS_TIMEOUT"

// defaultPassTimeout bounds a whole reconcile pass. Each git/tmux call
// has its own, shorter budget (callbudget); this one catches a pass
// that is slow through many calls. Keep it under the NixOS module's
// TimeoutStartSec so the pass reports before systemd kills it.
const defaultPassTimeout = 10 * time.Minute

func passTimeout() time.Duration {
	if v := os.Getenv(PassTimeoutEnv); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		fmt.Fprintf(os.Stderr, "spore: %s=%q: want a positive Go duration; using %s\n", PassTimeoutEnv, v, defaultPassTimeout)
	}
	return defaultPassTimeout
}

// reconcileBudgeted runs one pass under passTimeout. Past the budget it
// kills the pass's in-flight calls and returns an error; the caller
// exits, so the next timer tick starts clean.
func reconcileBudgeted(cfg fleet.Config) (fleet.Result, error) {
	type outcome struct {
		res fleet.Result
		err error
	}
	budget := passTimeout()
	done := make(chan outcome, 1)
	go func() {
		res, err := replenishReconcile(cfg)
		done <- outcome{res, err}
	}()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case o := <-done:
		return o.res, o.err
	case <-timer.C:
	}
	passAbort()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	return fleet.Result{}, fmt.Errorf("reconcile pass exceeded its %s budget (%s); in-flight calls killed", budget, PassTimeoutEnv)
}

// runFleetReplenishHook is the Stop-hook entry point for `spore fleet
// replenish-hook`. It reads context entirely from the environment so a
// settings.json hook entry can be a static string (no slug
// interpolation). Behaviour mirrors the bash cmd_fleet_replenish_hook
// shim:
//
//   - swallow stdin (claude-code feeds the hook payload there)
//   - no-op when the firing session is not the coordinator (per
//     $SPORE_TASK_INBOX vs $SKYHELM_STATE_DIR / $SPORE_COORDINATOR_STATE_DIR)
//   - skip the spawn pass when budget advice is "tighten" / "ration"
//   - never exit non-zero: a failing reconcile must not block the Stop
//     hook
//   - keep stdout empty and report on stderr: Codex parses Stop-hook
//     stdout as JSON
//
// $WT_PROJECT and $WT_FLEET_FLOOR are read by the helpers above
// (resolveMaxWorkers honours $WT_FLEET_FLOOR; project root is the
// firing session's cwd, which is the per-project coordinator's tree).
func runFleetReplenishHook(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: spore fleet replenish-hook (no args; reads env)")
	}
	_, _ = io.Copy(io.Discard, os.Stdin)

	if !isCoordinatorSession() {
		return nil
	}

	if a := budget.Advice(); a == "tighten" || a == "ration" {
		fmt.Fprintf(os.Stderr, "replenish-hook: skipping reconcile (budget advice=%s)\n", a)
		return nil
	}

	root, ok := replenishProjectRoot()
	if !ok {
		return nil
	}
	resolved, err := resolveMaxWorkers(0, root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "replenish-hook:", err)
		return nil
	}

	res, err := reconcileBudgeted(fleet.Config{
		TasksDir:    filepath.Join(root, "tasks"),
		ProjectRoot: root,
		MaxWorkers:  resolved,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "replenish-hook:", err)
		return nil
	}
	if res.Disabled || res.Locked {
		return nil
	}
	fmt.Fprintf(os.Stderr, "replenish-hook: active=%d spawned=%d kept=%d reaped=%d skipped=%d\n",
		len(res.Active), len(res.Spawned), len(res.Kept), len(res.Reaped), len(res.Skipped))
	return nil
}

// replenishProjectRoot returns the main repo root of the cwd when it
// is the session's own project: $SPORE_PROJECT_ROOT's main repo, else
// a repo named $WT_PROJECT. Any agent started from a coordinator pane
// (`claude -p` in a scratch repo) inherits the coordinator env, so
// without this check its Stop hook would reconcile, and spawn a
// coordinator for, whatever directory it ran in.
func replenishProjectRoot() (string, bool) {
	wd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	root, err := task.MainRepoRoot(wd)
	if err != nil {
		return "", false
	}
	if want := os.Getenv("SPORE_PROJECT_ROOT"); want != "" {
		wantRoot, err := task.MainRepoRoot(want)
		if err != nil {
			return "", false
		}
		return root, samePath(root, wantRoot)
	}
	if want := os.Getenv("WT_PROJECT"); want != "" {
		return root, filepath.Base(root) == want
	}
	return "", false
}

func samePath(a, b string) bool {
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// isCoordinatorSession reports whether $SPORE_TASK_INBOX points under the
// coordinator state dir (matching the bash self_id == "coordinator" test).
// Honours both SKYHELM_STATE_DIR (legacy) and SPORE_COORDINATOR_STATE_DIR
// (kernel-neutral) so the hook works during the rename transition.
func isCoordinatorSession() bool {
	inbox := os.Getenv("SPORE_TASK_INBOX")
	if inbox == "" {
		return false
	}
	for _, key := range []string{"SKYHELM_STATE_DIR", "SPORE_COORDINATOR_STATE_DIR"} {
		root := strings.TrimRight(os.Getenv(key), "/")
		if root == "" {
			continue
		}
		if inbox == root || strings.HasPrefix(inbox, root+"/") {
			return true
		}
	}
	return false
}

func runFleetWorkerAgent(args []string) error {
	fs := flag.NewFlagSet("fleet worker-agent", flag.ContinueOnError)
	clearKey := fs.Bool("clear", false, "")
	help := fs.Bool("h", false, "")
	helpLong := fs.Bool("help", false, "")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(reorderFlagsFirst(fs, args)); err != nil {
		return err
	}
	if *help || *helpLong {
		fmt.Print(fleetUsage)
		return nil
	}
	if fs.NArg() > 1 || (*clearKey && fs.NArg() > 0) {
		return fmt.Errorf("usage: spore fleet worker-agent [claude|codex | --clear]")
	}
	root, err := task.MainRepoRoot(os.Getenv("SPORE_PROJECT_ROOT"))
	if err != nil {
		return err
	}
	switch {
	case *clearKey:
		path, err := fleet.SetWorkerDefault(root, "")
		if err != nil {
			return err
		}
		fmt.Printf("cleared worker default in %s\n", path)
	case fs.NArg() == 1:
		agent := fs.Arg(0)
		if agent != "claude" && agent != "codex" {
			return fmt.Errorf("worker agent %q: want claude or codex", agent)
		}
		path, err := fleet.SetWorkerDefault(root, agent)
		if err != nil {
			return err
		}
		fmt.Printf("worker default %s written to %s\n", agent, path)
		return nil
	}
	agent, source, err := fleet.EffectiveWorkerDefault(root)
	if err != nil {
		return err
	}
	if source == fleet.WorkerDefaultFromOverlay {
		path, _ := fleet.WorkersOverlayPath(root)
		source += " " + path
	}
	fmt.Printf("%s (%s)\n", agent, source)
	return nil
}
