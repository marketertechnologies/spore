package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
)

const coordinatorAgentsUsage = `spore coordinator agents - list the drivers coordinator swap accepts

Usage:
  spore coordinator agents

Prints one line per driver: name, then "current" (the running driver,
from $SPORE_COORDINATOR_PROVIDER, default claude), "available" (its
binary resolves on PATH), or "missing". The current line also carries
$SPORE_COORDINATOR_MODEL when set. Read by the select-agent skill.
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
	writeCoordinatorAgents(os.Stdout, os.Getenv("SPORE_COORDINATOR_PROVIDER"), os.Getenv("SPORE_COORDINATOR_MODEL"), exec.LookPath)
	return 0
}

func writeCoordinatorAgents(w io.Writer, current, model string, lookPath func(string) (string, error)) {
	if current == "" {
		current = "claude"
	}
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
