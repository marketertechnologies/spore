package main

import (
	"fmt"
	"io"
	"os"

	"github.com/versality/spore/internal/editor"
)

const showUsage = `spore show - open a file location in the connected editor

Usage:
  spore show <path>[:<line>[:<col>]]

Relative paths resolve against the current directory, so a worker in
.worktrees/<slug> opens its own copy. Backend: VS Code over Remote-SSH
(remote code CLI plus a live CLI IPC socket).

No editor, no live socket, no CLI, or a missing file: one line to
stderr, exit 0. A missing editor never fails an agent turn.
`

func runShow(args []string) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore show:", err)
		return 0
	}
	return show(args, cwd, editor.NewVSCode(), os.Stdout, os.Stderr)
}

func show(args []string, cwd string, b editor.Backend, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprint(stdout, showUsage)
		return 0
	}
	if len(args) != 1 {
		fmt.Fprint(stderr, showUsage)
		return 2
	}
	loc, err := editor.ParseLocation(args[0], cwd)
	if err != nil {
		fmt.Fprintln(stderr, "spore show:", err)
		return 2
	}
	if _, err := os.Stat(loc.Path); err != nil {
		fmt.Fprintf(stderr, "spore show: %s: no such file, not opened\n", loc.Path)
		return 0
	}
	if err := b.Open(loc); err != nil {
		fmt.Fprintf(stderr, "spore show: %s not opened: %v\n", loc, err)
		return 0
	}
	fmt.Fprintf(stdout, "spore show: opened %s in %s\n", loc, b.Name())
	return 0
}
