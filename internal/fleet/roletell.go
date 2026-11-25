package fleet

import (
	"fmt"
)

// Seams for tests: role tell must not touch a real tmux server.
var (
	tellHasSession = hasSession
	tellSend       = sendWakeVerified
)

// TellRole delivers an operator message to a role-looped task's pane.
// Role panes have no inbox, so the message goes straight into the
// pane of the role that owns the next file write (activeRole), or the
// engineer once the loop is terminal. It errors when the pane is gone
// or its input prompt is not up, so a tell is never dropped silently.
// Returns the session name the message reached.
func TellRole(projectRoot, slug, msg string) (string, error) {
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
	if !tellHasSession(session) {
		return "", fmt.Errorf("tell %s: no live %s pane (%s); message not delivered", slug, spec.Role, session)
	}
	delivered, err := tellSend(session, "Operator message (spore task tell): "+msg)
	if err != nil {
		return "", fmt.Errorf("tell %s: %w; message may not have been delivered", slug, err)
	}
	if !delivered {
		return "", fmt.Errorf("tell %s: %s pane %s shows no input prompt yet; message not delivered, retry shortly", slug, spec.Role, session)
	}
	return session, nil
}
