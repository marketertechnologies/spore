package fleet

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Seams for tests: role tell must not touch a real tmux server.
var (
	tellHasSession = hasSession
	tellSend       = sendWakeVerified
)

// tellLockWait bounds how long a tell waits for a drive tick to
// release the slug's drive lock.
var tellLockWait = 5 * time.Second

// tellSuffix ends every delivered tell. The operator text leads so the
// submit check (first wakePendingFragLen alnum chars) matches it, and
// the fixed tail keeps a trailing ';' in that text away from tmux's
// command-separator parsing.
const tellSuffix = " (operator message via spore task tell)"

// TellRole delivers an operator message to a role-looped task's pane.
// Role panes have no inbox, so the message goes straight into the
// pane of the role that owns the next file write (activeRole), falling
// back to the engineer when the loop is terminal or that pane is not
// up. It holds the drive lock so a drive wake cannot interleave with
// the typed text. It errors when no pane takes the message, so a tell
// is never dropped silently. Returns the session name it reached.
func TellRole(projectRoot, slug, msg string) (string, error) {
	unlock, err := waitRoleDriveLock(projectRoot, slug)
	if err != nil {
		return "", lockRefusal("tell", slug, "a drive, reopen or force-approve holds the loop", err)
	}
	defer unlock()

	snap, err := DeriveSnapshot(projectRoot, slug)
	if err != nil {
		return "", fmt.Errorf("tell %s: %w", slug, err)
	}
	spec, _, _, ok := activeRole(projectRoot, slug, snap)
	if !ok {
		spec = EngineerSpec(projectRoot, slug)
	}
	session, err := spec.SessionName()
	if err != nil {
		return "", fmt.Errorf("tell %s: %w", slug, err)
	}
	if !tellHasSession(session) && spec.Role != "engineer" {
		spec = EngineerSpec(projectRoot, slug)
		if session, err = spec.SessionName(); err != nil {
			return "", fmt.Errorf("tell %s: %w", slug, err)
		}
	}
	if !tellHasSession(session) {
		return "", fmt.Errorf("tell %s: no live %s pane (%s); message not delivered", slug, spec.Role, session)
	}
	delivered, err := tellSend(session, flattenTell(msg)+tellSuffix)
	if err != nil {
		return "", fmt.Errorf("tell %s: %w; check the %s pane before retrying, the text may sit unsubmitted in its input", slug, err, spec.Role)
	}
	if !delivered {
		return "", fmt.Errorf("tell %s: %s pane %s shows no input prompt yet; message not delivered, retry shortly", slug, spec.Role, session)
	}
	return session, nil
}

// flattenTell turns control characters into spaces: a typed newline
// would submit the agent's prompt early and split the message.
func flattenTell(msg string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, msg))
}

func waitRoleDriveLock(projectRoot, slug string) (func(), error) {
	deadline := time.Now().Add(tellLockWait)
	for {
		unlock, err := lockRoleDrive(projectRoot, slug)
		if !errors.Is(err, errLockHeld) || !time.Now().Before(deadline) {
			return unlock, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
