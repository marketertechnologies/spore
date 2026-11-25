package lints

import (
	"github.com/versality/spore/internal/sandbox"
)

// SandboxOff warns when spore.toml turns the Claude Code sandbox off
// (`[sandbox] enabled = false`) and fails on a [sandbox] section spore
// cannot parse, since a spawn then falls back to the bare sandbox.
type SandboxOff struct{}

func (SandboxOff) Name() string { return "sandbox-off" }

func (SandboxOff) Run(root string) ([]Issue, error) {
	c, err := sandbox.Load(root)
	if err != nil {
		return []Issue{{Path: "spore.toml", Message: err.Error()}}, nil
	}
	if c.Enabled {
		return nil, nil
	}
	return []Issue{{
		Path:    "spore.toml",
		Warn:    true,
		Message: "[sandbox] enabled = false: agent Bash commands run unsandboxed, with read access to secrets and open network (docs/sandbox.md)",
	}}, nil
}
