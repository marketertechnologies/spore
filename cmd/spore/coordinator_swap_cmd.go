package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/versality/spore/internal/fleet"
)

const coordinatorSwapUsage = `spore coordinator swap - respawn the coordinator under another driver

Usage:
  spore coordinator swap --to claude|codex [--model M] [--project NAME]
                         [--session NAME] [--no-wrap] [--force]
                         [--wrap-timeout D]

Steps: ask the live coordinator (through its inbox) to flush state.md
and wait for the write; set [coordinator] driver/model in spore.toml;
set SPORE_COORDINATOR_PROVIDER/MODEL in the tmux session env; respawn
the session's first pane with the coordinator agent.

Without --model, a driver change clears the model and a same-driver
swap keeps it.

From an operator shell: run in the project tree, or pass --project.
From inside the coordinator pane: flush state.md yourself, then run
with --no-wrap; the pane (and this command) is replaced.

Flags:
  --to D            new driver: claude or codex (required)
  --model M         model for the new driver
  --project NAME    target spore/NAME/coordinator (default: cwd project)
  --session NAME    target tmux session (overrides --project)
  --no-wrap         skip the state.md flush request
  --force           swap even if state.md did not advance in time
  --wrap-timeout D  how long to wait for state.md (default 5m)
`

func runCoordinatorSwap(args []string) int {
	fs := flag.NewFlagSet("coordinator swap", flag.ContinueOnError)
	to := fs.String("to", "", "")
	model := fs.String("model", "", "")
	project := fs.String("project", "", "")
	session := fs.String("session", "", "")
	noWrap := fs.Bool("no-wrap", false, "")
	force := fs.Bool("force", false, "")
	wrapTimeout := fs.Duration("wrap-timeout", 5*time.Minute, "")
	help := fs.Bool("h", false, "")
	helpLong := fs.Bool("help", false, "")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(reorderFlagsFirst(fs, args)); err != nil {
		fmt.Fprintln(os.Stderr, "spore coordinator swap:", err)
		return 2
	}
	if *help || *helpLong {
		fmt.Print(coordinatorSwapUsage)
		return 0
	}
	if fs.NArg() != 0 || *to == "" {
		fmt.Fprint(os.Stderr, coordinatorSwapUsage)
		return 2
	}
	modelSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "model" {
			modelSet = true
		}
	})

	target := *session
	switch {
	case target != "":
	case *project != "":
		target = fmt.Sprintf("spore/%s/%s", *project, fleet.CoordinatorSlug)
	default:
		root, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "spore coordinator swap:", err)
			return 1
		}
		target = fleet.CoordinatorSessionName(root)
	}

	res, err := fleet.SwapCoordinator(fleet.SwapSpec{
		Session:     target,
		To:          *to,
		Model:       *model,
		ModelSet:    modelSet,
		Wrap:        !*noWrap,
		Force:       *force,
		WrapTimeout: *wrapTimeout,
		WrapSettle:  10 * time.Second,
		Log:         func(s string) { fmt.Fprintln(os.Stderr, s) },
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore coordinator swap:", err)
		if errors.Is(err, fleet.ErrWrapTimeout) {
			fmt.Fprintln(os.Stderr, "  spore.toml and the pane are untouched; rerun with --force or --no-wrap")
		}
		return 1
	}
	fmt.Printf("coordinator: swapped %s to %s (pane %s)\n", target, res.Config.Driver, res.Pane)
	return 0
}
