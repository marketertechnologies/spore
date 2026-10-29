package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	spore "github.com/versality/spore"
	"github.com/versality/spore/internal/install"
)

const installUsage = `spore install - drop spore skills into a target project

Usage:
  spore install [--root <path>]
  spore install --dest <dir>

Copies the bundled skill bodies (spore-bootstrap, diagram,
select-agent) into <root>/.claude/skills/ and <root>/.agents/skills/
so claude-code and Codex in that project can discover and run them.
Idempotent: re-runs only rewrite files whose contents drifted from
the embedded copy.

With --dest, copies the skills flat into <dir>/<name>/ instead. The
spore-fleet NixOS module uses this to render the host-wide skill tree
that home-manager links into ~/.claude/skills and ~/.agents/skills.

Flags:
  --root   Project root to install into. Defaults to the current
           directory.
  --dest   Directory to copy the skills into directly. Excludes --root.
`

func runInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	root := fs.String("root", "", "project root (default: cwd)")
	destDir := fs.String("dest", "", "copy skills flat into this directory")
	help := fs.Bool("h", false, "show help")
	helpLong := fs.Bool("help", false, "show help")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(reorderFlagsFirst(fs, args)); err != nil {
		fmt.Fprintln(os.Stderr, "spore install:", err)
		fmt.Fprint(os.Stderr, installUsage)
		return 2
	}
	if *help || *helpLong {
		fmt.Print(installUsage)
		return 0
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "spore install: unexpected positional args:", fs.Args())
		return 2
	}

	if *destDir != "" {
		if *root != "" {
			fmt.Fprintln(os.Stderr, "spore install: --dest and --root are mutually exclusive")
			return 2
		}
		res, err := install.InstallDir(*destDir, spore.BundledSkills, "bootstrap/skills")
		if err != nil {
			fmt.Fprintln(os.Stderr, "spore install:", err)
			return 1
		}
		fmt.Printf("installed %d file(s) under %s/\n", len(res.Written), *destDir)
		return 0
	}

	dest := *root
	if dest == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "spore install:", err)
			return 1
		}
		dest = cwd
	}

	res, err := install.Install(dest, spore.BundledSkills, "bootstrap/skills")
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore install:", err)
		return 1
	}
	for _, p := range res.Written {
		rel, _ := filepath.Rel(dest, p)
		fmt.Printf("wrote %s\n", rel)
	}
	if len(res.Written) == 0 {
		fmt.Println("install: already up to date")
	} else {
		fmt.Printf("installed %d file(s) under %s/{.claude,.agents}/skills/\n", len(res.Written), dest)
	}
	return 0
}
