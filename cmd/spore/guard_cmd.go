package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/versality/spore/internal/headlessguard"
)

const guardUsage = `spore guard - headless-spawn guard for agent binaries

Usage:
  spore guard <binary> [args...]

Refuses to exec <binary> when the spawn would be headless (stdin is not
a TTY and $TMUX is unset) so an agent session always has an attachable
tmux pane. Otherwise execs <binary> with the remaining args, replacing
this process so signals propagate cleanly. Seeds SPORE_ACCOUNT_TIER from
WT_ACCOUNT_TIER when unset.

Wire it as the agent binary to make every spawn pass through the guard:
  SPORE_COORDINATOR_AGENT="spore guard claude"
  SPORE_AGENT_BINARY="spore guard claude"
`

func runGuard(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, guardUsage)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(guardUsage)
		return 0
	}

	bin := args[0]
	rest := args[1:]

	headlessguard.BackstopAccountTier()

	if !headlessguard.CurrentEnv().Allowed() {
		fmt.Fprint(os.Stderr, headlessguard.RefusalMessage(bin))
		return 1
	}

	path, err := exec.LookPath(bin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spore guard: %v\n", err)
		return 127
	}
	argv := append([]string{path}, rest...)
	if err := syscall.Exec(path, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "spore guard: exec %s: %v\n", bin, err)
		return 126
	}
	return 0
}
