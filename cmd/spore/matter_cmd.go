package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/versality/spore/internal/matter"
	"github.com/versality/spore/internal/matter/linear"
)

const matterUsage = `spore matter - manage external work-item backends (Linear).

Usage:
  spore matter new <title> [--description <text>] [--description-stdin]

Subcommands:
  new <title>           Mint a Linear issue in the configured team and
                        delegate it to Rocky. delegateId is hardcoded
                        to the authenticated actor (viewer.id) so a
                        ticket created via this helper can never be
                        assigned or delegated elsewhere. assigneeId is
                        never set (humans only). The next reconcile
                        pass picks the ticket up.

Flags for 'new':
  --description <text>     Inline ticket body.
  --description-stdin      Read ticket body from stdin.
`

func runMatter(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, matterUsage)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(os.Stdout, matterUsage)
		return 0
	case "new":
		if err := runMatterNew(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "spore matter new:", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "spore matter: unknown subcommand %q\n\n%s", args[0], matterUsage)
		return 2
	}
}

func runMatterNew(args []string) error {
	fs := flag.NewFlagSet("matter new", flag.ContinueOnError)
	description := fs.String("description", "", "inline ticket body")
	descStdin := fs.Bool("description-stdin", false, "read body from stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("expected exactly one <title>, got %d", fs.NArg())
	}
	title := strings.TrimSpace(fs.Arg(0))
	if title == "" {
		return fmt.Errorf("title cannot be empty")
	}
	body := *description
	if *descStdin {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		body = strings.TrimSpace(string(raw))
	}

	root, err := os.Getwd()
	if err != nil {
		return err
	}
	cfgs, err := matter.LoadFromProject(root)
	if err != nil {
		return fmt.Errorf("load matter config: %w", err)
	}
	var linearCfg *matter.Config
	for i := range cfgs {
		if cfgs[i].Name == "linear" {
			linearCfg = &cfgs[i]
			break
		}
	}
	if linearCfg == nil {
		return fmt.Errorf("no [matter.linear] config in spore.toml or env")
	}
	src, err := linear.New(*linearCfg)
	if err != nil {
		return fmt.Errorf("init linear source: %w", err)
	}
	lin, ok := src.(*linear.Source)
	if !ok {
		return fmt.Errorf("matter.linear source has unexpected type %T", src)
	}
	_ = context.TODO()
	created, err := lin.CreateIssue(title, body)
	if err != nil {
		return err
	}
	fmt.Printf("%s\t%s\n", created.Identifier, created.URL)
	return nil
}
