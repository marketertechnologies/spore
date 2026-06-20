package main

import (
	"os"

	"github.com/versality/spore/internal/tokenstatus"
)

func runTokenStatus(args []string) int {
	return tokenstatus.Run(args, os.Stdin, os.Stdout, os.Stderr)
}
