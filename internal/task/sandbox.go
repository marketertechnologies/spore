package task

import (
	"fmt"
	"os"

	"github.com/versality/spore/internal/sandbox"
)

// SandboxSessionEnv returns the tmux `-e` arguments that turn the
// Claude Code sandbox on for a pane of projectRoot running in dir, and
// prepares dir's checkout for it. Problems are warnings: a spawn never
// fails over them, and a broken [sandbox] section keeps the sandbox on.
func SandboxSessionEnv(projectRoot, dir string) []string {
	env, err := sandbox.PaneEnv(projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spore: %s: %v (sandbox stays on without project extras)\n", projectRoot, err)
	}
	if len(env) == 0 {
		return nil
	}
	if err := sandbox.PrepareCheckout(dir); err != nil {
		fmt.Fprintf(os.Stderr, "spore: %v\n", err)
	}
	return env
}
