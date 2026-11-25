package fleet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/versality/spore/internal/redact"
	"github.com/versality/spore/internal/secrets"
)

func (m *githubMirror) git(args ...string) (string, error) {
	return m.run1("git", "", args...)
}

// gh runs gh with its posted text redacted (redact.GHCall). Behind the
// secrets broker the call runs there instead, redacted broker-side,
// since the agent user cannot read the secrets to redact against.
func (m *githubMirror) gh(stdin string, args ...string) (string, error) {
	if m.brokered {
		return m.brokerGH(stdin, args)
	}
	if m.redactor == nil {
		m.redactor = redact.New(m.projectRoot)
	}
	stdin, args = m.redactor.GHCall(stdin, args)
	return m.run1("gh", stdin, args...)
}

func (m *githubMirror) brokerGH(stdin string, args []string) (string, error) {
	out, err := secrets.GH(m.project, stdin, args, mirrorCallTimeout)
	var ee *secrets.ExitError
	if errors.As(err, &ee) {
		return "", &cmdError{
			key:    fmt.Sprintf("gh %s: %v", strings.Join(args[:min(2, len(args))], " "), ee),
			stderr: ee.Stderr,
		}
	}
	return out, err
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
