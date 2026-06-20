package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	spore "github.com/versality/spore"
	"github.com/versality/spore/internal/recipes"
)

const recipesUsage = `spore recipes - browse the embedded recipe library

Usage:
  spore recipes ls
  spore recipes show <name>

Recipes are reusable how-to documents for talking to external systems
(Jira, Sentry, Notion, etc.) from a coordinator or worker pane. The
filename under bootstrap/recipes/ (sans .md) is the canonical name.

Subcommands:
  ls           List every recipe and its title.
  show <name>  Print the raw markdown body of one recipe to stdout.
`

func runRecipes(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, recipesUsage)
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(recipesUsage)
		return 0
	case "ls":
		return runRecipesLs(args[1:])
	case "show":
		return runRecipesShow(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "spore recipes: unknown subcommand %q\n\n%s", args[0], recipesUsage)
		return 2
	}
}

func runRecipesLs(args []string) int {
	fs := flag.NewFlagSet("recipes ls", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "spore recipes ls:", err)
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "spore recipes ls: unexpected positional args:", fs.Args())
		return 2
	}
	list, err := recipes.List(spore.BundledRecipes, "bootstrap/recipes")
	if err != nil {
		fmt.Fprintln(os.Stderr, "spore recipes ls:", err)
		return 1
	}
	if len(list) == 0 {
		fmt.Println("no recipes available")
		return 0
	}
	width := 0
	for _, r := range list {
		if n := len(r.Name); n > width {
			width = n
		}
	}
	for _, r := range list {
		if r.Title == "" {
			fmt.Printf("%-*s\n", width, r.Name)
			continue
		}
		fmt.Printf("%-*s  %s\n", width, r.Name, r.Title)
	}
	return 0
}

func runRecipesShow(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "spore recipes show: missing <name>")
		fmt.Fprint(os.Stderr, recipesUsage)
		return 2
	}
	if len(args) > 1 {
		fmt.Fprintln(os.Stderr, "spore recipes show: too many arguments")
		return 2
	}
	body, err := recipes.Get(spore.BundledRecipes, "bootstrap/recipes", args[0])
	if err != nil {
		if errors.Is(err, recipes.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "spore recipes: no recipe named %q (try `spore recipes ls`)\n", args[0])
			return 1
		}
		fmt.Fprintln(os.Stderr, "spore recipes show:", err)
		return 1
	}
	if _, err := os.Stdout.Write(body); err != nil {
		fmt.Fprintln(os.Stderr, "spore recipes show:", err)
		return 1
	}
	return 0
}
