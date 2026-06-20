package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	spore "github.com/versality/spore"
	"github.com/versality/spore/internal/migrations"
)

const migrateUsage = `spore migrate - apply pending host-state migrations

Usage:
  spore migrate [--auto] [--dry-run]

Walks the embedded migration tree under bootstrap/migrations/, runs
every script whose name is not already recorded in the ledger at
$XDG_STATE_HOME/spore/migrations.applied, and appends successful runs
to that ledger. Idempotent: re-running with no pending migrations is a
clean no-op.

Flags:
  --auto      Quiet on no-op. Designed for the bundled flake's
              activation hook so a rebuild that requires no migration
              activity leaves no output on the journal.
  --dry-run   List pending migrations without executing them.

See bootstrap/migrations/README.md for the authoring contract.
`

func runMigrate(args []string) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	auto := fs.Bool("auto", false, "quiet on no-op (for activation hooks)")
	dryRun := fs.Bool("dry-run", false, "list pending migrations without running them")
	help := fs.Bool("h", false, "show help")
	helpLong := fs.Bool("help", false, "show help")
	fs.SetOutput(io.Discard)
	if err := fs.Parse(reorderFlagsFirst(fs, args)); err != nil {
		fmt.Fprintln(os.Stderr, "spore migrate:", err)
		fmt.Fprint(os.Stderr, migrateUsage)
		return 2
	}
	if *help || *helpLong {
		fmt.Print(migrateUsage)
		return 0
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "spore migrate: unexpected positional args:", fs.Args())
		return 2
	}

	opts := migrations.Options{
		DryRun:  *dryRun,
		Version: spore.BuildVersion(),
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}
	res, err := migrations.Apply(spore.BundledMigrations, "bootstrap/migrations", opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore migrate:", err)
		return 1
	}

	if *dryRun {
		if len(res.Pending) == 0 {
			fmt.Println("spore migrate: no migrations pending")
			return 0
		}
		fmt.Printf("spore migrate: %d pending\n", len(res.Pending))
		for _, n := range res.Pending {
			fmt.Println("  " + n)
		}
		return 0
	}

	if len(res.Applied) == 0 {
		if !*auto {
			fmt.Println("spore migrate: no migrations pending")
		}
		return 0
	}
	fmt.Printf("spore migrate: applied %d migration(s)\n", len(res.Applied))
	for _, n := range res.Applied {
		fmt.Println("  " + n)
	}
	return 0
}
