package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/versality/spore/internal/sporetoml"
)

const configUsage = `spore config - project configuration

Usage:
  spore config check [--root <path>]

check   Report unknown sections and keys in <root>/spore.toml, one per
        line, and exit 1 when there are any. The loaders ignore them,
        so a typo silently drops a setting; spore fleet reconcile,
        spore lint and spore coordinator print the same findings as
        warnings.
`

func runConfig(args []string) int {
	if len(args) < 1 || args[0] != "check" {
		if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
			fmt.Print(configUsage)
			return 0
		}
		fmt.Fprint(os.Stderr, configUsage)
		return 2
	}
	fs := flag.NewFlagSet("config check", flag.ContinueOnError)
	root := fs.String("root", ".", "project root")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		fmt.Fprint(os.Stderr, configUsage)
		return 2
	}
	problems, err := sporetoml.CheckFile(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore config check:", err)
		return 1
	}
	for _, p := range problems {
		fmt.Printf("spore.toml %s\n", p)
	}
	if len(problems) > 0 {
		return 1
	}
	return 0
}
