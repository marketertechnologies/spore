package fleet

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/versality/spore/internal/redact"
)

func (m *githubMirror) git(args ...string) (string, error) {
	return m.run1("git", "", args...)
}

// gh runs gh with its posted text redacted: stdin (JSON-aware for
// `--input -`) and the values of --title and --body. Other args are
// left alone, since they carry refs, logins and repo names that a
// secrets.env value could collide with.
func (m *githubMirror) gh(stdin string, args ...string) (string, error) {
	if m.redactor == nil {
		m.redactor = redact.New(m.projectRoot)
	}
	args = slices.Clone(args)
	jsonInput := false
	for i, a := range args {
		if i == 0 {
			continue
		}
		switch args[i-1] {
		case "--title", "--body":
			args[i] = m.redactor.String(a)
		case "--input":
			jsonInput = a == "-"
		}
	}
	if jsonInput {
		stdin = m.redactor.JSON(stdin)
	} else {
		stdin = m.redactor.String(stdin)
	}
	return m.run1("gh", stdin, args...)
}

// mirrorCallTimeout bounds every git/gh call and the token mint: the
// mirror runs inline in the fleet reconcile, so a hung call would stall
// every task's role loop.
var mirrorCallTimeout = 60 * time.Second

// cmdError is a failed git/gh call. key drops stderr, which often
// carries request ids, so a persistent failure notifies once.
type cmdError struct {
	key, stderr string
}

func (e *cmdError) Error() string { return e.key + ": " + e.stderr }

// run1 runs name in the worktree as the app and returns trimmed
// stdout; a failure carries stderr.
func (m *githubMirror) run1(name, stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mirrorCallTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	cmd.Dir = m.worktree
	cmd.Env = m.env
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("timed out after %s", mirrorCallTimeout)
		}
		return "", &cmdError{
			key:    fmt.Sprintf("%s %s: %v", name, strings.Join(args[:min(2, len(args))], " "), err),
			stderr: strings.TrimSpace(stderr.String()),
		}
	}
	return strings.TrimSpace(stdout.String()), nil
}
