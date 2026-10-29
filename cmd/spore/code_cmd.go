package main

import (
	"fmt"
	"io"
	"os"

	"github.com/versality/spore/internal/editor"
	"github.com/versality/spore/internal/task"
)

const codeUsage = `spore code - editor workspace helpers

Usage:
  spore code workspace [--print]

workspace  Write <repo>/.spore/spore.code-workspace: a VS Code
           multi-root workspace with the main checkout plus one
           folder per .worktrees/<slug>, labeled by slug. Open it over
           Remote-SSH to watch every worker's changes in Source
           Control. Rewritten only when the content changes. Once the
           file exists, the fleet reconciler and 'spore task done'
           keep it current.
--print    Print the JSON to stdout instead of writing the file.
`

func runCode(args []string) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore code:", err)
		return 1
	}
	return code(args, cwd, os.Stdout, os.Stderr)
}

func code(args []string, cwd string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, codeUsage)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, codeUsage)
		return 0
	case "workspace":
	default:
		fmt.Fprintf(stderr, "spore code: unknown subcommand %q\n\n%s", args[0], codeUsage)
		return 2
	}
	printOnly := false
	for _, a := range args[1:] {
		switch a {
		case "--print":
			printOnly = true
		case "-h", "--help":
			fmt.Fprint(stdout, codeUsage)
			return 0
		default:
			fmt.Fprintf(stderr, "spore code workspace: unknown flag %q\n", a)
			return 2
		}
	}
	root, err := task.MainRepoRoot(cwd)
	if err != nil {
		fmt.Fprintln(stderr, "spore code workspace:", err)
		return 1
	}
	if printOnly {
		data, err := editor.RenderWorkspace(root)
		if err != nil {
			fmt.Fprintln(stderr, "spore code workspace:", err)
			return 1
		}
		stdout.Write(data)
		return 0
	}
	path, _, err := editor.WriteWorkspace(root)
	if err != nil {
		fmt.Fprintln(stderr, "spore code workspace:", err)
		return 1
	}
	fmt.Fprintln(stdout, path)
	return 0
}
