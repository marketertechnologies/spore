package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/versality/spore/internal/sandbox"
)

const sandboxUsage = `spore sandbox - Claude Code sandbox settings for spore panes

Usage:
  spore sandbox settings [--project DIR]
  spore sandbox prepare [DIR]

Subcommands:
  settings  Print the --settings JSON spore passes to the project's panes
            (via $SPORE_SANDBOX_SETTINGS): sandbox.enabled plus the
            spore.toml [sandbox] extras. Prints nothing when the project
            set enabled = false. The host-wide floor is in the managed
            drop-in (spore hooks settings --managed).
  prepare   Add the sandbox placeholder names to the checkout's
            info/exclude and create an empty .gitmodules when missing
            (default DIR: the current directory). Spawns do this already.

See docs/sandbox.md.
`

func runSandbox(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, sandboxUsage)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(sandboxUsage)
		return 0
	case "settings":
		fs := flag.NewFlagSet("sandbox settings", flag.ContinueOnError)
		project := fs.String("project", ".", "project root holding spore.toml")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		c, err := sandbox.Load(*project)
		if err != nil {
			fmt.Fprintf(os.Stderr, "spore sandbox: %v\n", err)
			return 1
		}
		js, err := sandbox.PaneSettings(c)
		if err != nil {
			fmt.Fprintf(os.Stderr, "spore sandbox: %v\n", err)
			return 1
		}
		if js != "" {
			fmt.Println(js)
		}
		return 0
	case "prepare":
		dir := "."
		if len(args) > 2 {
			fmt.Fprint(os.Stderr, sandboxUsage)
			return 2
		}
		if len(args) == 2 {
			dir = args[1]
		}
		if err := sandbox.PrepareCheckout(dir); err != nil {
			fmt.Fprintf(os.Stderr, "spore sandbox: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "spore sandbox: unknown subcommand %q\n\n%s", args[0], sandboxUsage)
		return 2
	}
}
