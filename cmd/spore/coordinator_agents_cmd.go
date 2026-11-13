package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
)

const coordinatorAgentsUsage = `spore coordinator agents - list the drivers coordinator swap accepts

Usage:
  spore coordinator agents

Prints one line per driver: name, then "current" (the running driver),
"available" (its binary resolves on PATH), or "missing". The current
driver is the basename of $SPORE_COORDINATOR_PROVIDER, else of
$SPORE_COORDINATOR_AGENT when that names a driver binary, else claude.
The current line also carries $SPORE_COORDINATOR_MODEL when set. Read
by the select-agent skill.
`

var swapDrivers = []string{"claude", "codex"}

func runCoordinatorAgents(args []string) int {
	if len(args) > 0 {
		if args[0] == "-h" || args[0] == "--help" {
			fmt.Print(coordinatorAgentsUsage)
			return 0
		}
		fmt.Fprint(os.Stderr, coordinatorAgentsUsage)
		return 2
	}
	current := currentCoordinatorDriver(os.Getenv("SPORE_COORDINATOR_PROVIDER"), os.Getenv("SPORE_COORDINATOR_AGENT"))
	writeCoordinatorAgents(os.Stdout, current, os.Getenv("SPORE_COORDINATOR_MODEL"), exec.LookPath)
	return 0
}

// currentCoordinatorDriver takes basenames because hosts may set the
// provider or agent to a binary path. An agent that is a launcher
// script (spore-coordinator-launch) names no driver and is skipped.
func currentCoordinatorDriver(provider, agent string) string {
	if provider != "" {
		return filepath.Base(provider)
	}
	if a := filepath.Base(agent); slices.Contains(swapDrivers, a) {
		return a
	}
	return "claude"
}

func writeCoordinatorAgents(w io.Writer, current, model string, lookPath func(string) (string, error)) {
	for _, d := range swapDrivers {
		switch {
		case d == current && model != "":
			fmt.Fprintf(w, "%s\tcurrent\t%s\n", d, model)
		case d == current:
			fmt.Fprintf(w, "%s\tcurrent\n", d)
		default:
			if _, err := lookPath(d); err == nil {
				fmt.Fprintf(w, "%s\tavailable\n", d)
			} else {
				fmt.Fprintf(w, "%s\tmissing\n", d)
			}
		}
	}
}
