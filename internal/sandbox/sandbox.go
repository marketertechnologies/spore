// Package sandbox renders spore's Claude Code sandbox settings: the
// host-wide floor shipped in the managed drop-in, and the per-pane
// `--settings` JSON that turns the sandbox on for one project. See
// docs/sandbox.md for the behaviour each key was verified against.
package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/versality/spore/internal/sporetoml"
)

// EnvVar carries the pane settings JSON into each spore tmux session.
// Every claude launcher appends `--settings "$SPORE_SANDBOX_SETTINGS"`
// when it is set, so a respawned pane keeps the session's value.
const EnvVar = "SPORE_SANDBOX_SETTINGS"

// DefaultDomains is what sandboxed commands on every spore host may
// reach. A project adds more with [sandbox] allow_domains.
var DefaultDomains = []string{
	"api.anthropic.com",
	"github.com",
	"api.github.com",
	"codeload.github.com",
	"objects.githubusercontent.com",
	"raw.githubusercontent.com",
	"proxy.golang.org",
	"sum.golang.org",
	"cache.nixos.org",
	"channels.nixos.org",
	"registry.npmjs.org",
	"registry.yarnpkg.com",
	"rubygems.org",
	"index.rubygems.org",
}

// DefaultAllowWrite holds the tool caches agents write besides the
// task worktree and $TMPDIR, which Claude Code allows on its own.
// ~/.local/state/spore carries inboxes and task state that `spore task`
// commands write.
var DefaultAllowWrite = []string{
	"~/.cache",
	"~/go",
	"~/.local/state/spore",
}

// DefaultDenyRead is unreadable from every sandboxed command. Claude
// Code also folds Read(...) deny rules into this list.
var DefaultDenyRead = []string{
	"~/.config/spore",
	"~/.local/state/spore/*/github-token.json",
	"~/.ssh",
	"~/.claude/.credentials.json",
	"~/.npmrc",
}

// DefaultDenyWrite stops a sandboxed command repointing the GitHub App
// config that the unsandboxed `spore gh` later loads.
var DefaultDenyWrite = []string{
	"~/.local/state/spore/*/github.toml",
}

// DefaultExcluded runs outside the sandbox: `spore gh` mints the App
// token from the key under ~/.config/spore. The PreToolUse guard limits
// `spore gh exec` to git and gh.
var DefaultExcluded = []string{"spore gh *"}

// Managed is the sandbox block of the managed drop-in. It leaves
// `enabled` unset, so the sandbox is off for sessions spore did not
// launch and a project can opt out; every key here is a floor no lower
// settings tier can lift. Fields are in JSON key order.
type Managed struct {
	AllowUnsandboxedCommands bool       `json:"allowUnsandboxedCommands"`
	ExcludedCommands         []string   `json:"excludedCommands"`
	Filesystem               Filesystem `json:"filesystem"`
	Network                  Network    `json:"network"`
}

// Filesystem fields are in JSON key order.
type Filesystem struct {
	AllowWrite []string `json:"allowWrite,omitempty"`
	DenyRead   []string `json:"denyRead,omitempty"`
	DenyWrite  []string `json:"denyWrite,omitempty"`
}

// Network fields are in JSON key order. AllowAllUnixSockets is on
// because Linux cannot filter sockets by path and `nix develop` needs
// the nix daemon socket.
type Network struct {
	AllowAllUnixSockets bool     `json:"allowAllUnixSockets,omitempty"`
	AllowedDomains      []string `json:"allowedDomains,omitempty"`
	StrictAllowlist     bool     `json:"strictAllowlist,omitempty"`
}

// ManagedBlock returns the host-wide floor.
func ManagedBlock() *Managed {
	return &Managed{
		AllowUnsandboxedCommands: false,
		ExcludedCommands:         append([]string(nil), DefaultExcluded...),
		Filesystem: Filesystem{
			AllowWrite: append([]string(nil), DefaultAllowWrite...),
			DenyRead:   append([]string(nil), DefaultDenyRead...),
			DenyWrite:  append([]string(nil), DefaultDenyWrite...),
		},
		Network: Network{
			AllowAllUnixSockets: true,
			AllowedDomains:      append([]string(nil), DefaultDomains...),
			StrictAllowlist:     true,
		},
	}
}

// Config is the [sandbox] section of a project's spore.toml.
type Config struct {
	Enabled          bool
	AllowDomains     []string
	AllowWrite       []string
	ExcludedCommands []string
}

// Load reads [sandbox] from <projectRoot>/spore.toml. A missing file or
// section yields the sandbox on with no project extras.
func Load(projectRoot string) (Config, error) {
	c := Config{Enabled: true}
	entries, err := sporetoml.ReadSection(projectRoot, "sandbox")
	if err != nil {
		return c, fmt.Errorf("sandbox: %w", err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.Key] {
			return c, fmt.Errorf("sandbox: line %d: duplicate key %q in [sandbox]", e.Line, e.Key)
		}
		seen[e.Key] = true
		var err error
		switch e.Key {
		case "enabled":
			c.Enabled, err = e.Bool()
		case "allow_domains":
			c.AllowDomains, err = e.Strings()
		case "allow_write":
			c.AllowWrite, err = e.Strings()
		case "excluded_commands":
			c.ExcludedCommands, err = e.Strings()
		default:
			err = fmt.Errorf("line %d: unknown key %q in [sandbox]", e.Line, e.Key)
		}
		if err != nil {
			return c, fmt.Errorf("sandbox: %w", err)
		}
	}
	return c, nil
}

type paneTop struct {
	Sandbox paneSandbox `json:"sandbox"`
}

// paneSandbox fields are in JSON key order.
type paneSandbox struct {
	Enabled           bool        `json:"enabled"`
	ExcludedCommands  []string    `json:"excludedCommands,omitempty"`
	FailIfUnavailable bool        `json:"failIfUnavailable"`
	Filesystem        *Filesystem `json:"filesystem,omitempty"`
	Network           *Network    `json:"network,omitempty"`
}

// PaneSettings renders the `--settings` JSON for a pane of a project
// with config c, or "" when the project opted out. Flag settings sit
// above user and project settings, and strictAllowlist ignores
// project-tier domains, so the project extras ride here.
func PaneSettings(c Config) (string, error) {
	if !c.Enabled {
		return "", nil
	}
	s := paneSandbox{Enabled: true, FailIfUnavailable: true, ExcludedCommands: c.ExcludedCommands}
	if len(c.AllowWrite) > 0 {
		s.Filesystem = &Filesystem{AllowWrite: c.AllowWrite}
	}
	if len(c.AllowDomains) > 0 {
		s.Network = &Network{AllowedDomains: c.AllowDomains}
	}
	b, err := json.Marshal(paneTop{Sandbox: s})
	if err != nil {
		return "", fmt.Errorf("sandbox: %w", err)
	}
	return string(b), nil
}

// PaneEnv returns the `-e NAME=VALUE` tmux arguments that carry the
// project's pane settings, or nil when the sandbox is off. A broken
// [sandbox] section fails closed: the sandbox stays on with no extras,
// and the error is returned for the caller to report.
func PaneEnv(projectRoot string) ([]string, error) {
	c, loadErr := Load(projectRoot)
	if loadErr != nil {
		c = Config{Enabled: true}
	}
	js, err := PaneSettings(c)
	if err != nil {
		return nil, err
	}
	if js == "" {
		return nil, loadErr
	}
	return []string{"-e", EnvVar + "=" + js}, loadErr
}

// PlaceholderPaths are the names Claude Code 2.1.329 mounts over
// inside a sandboxed checkout when they do not exist, so a sandboxed
// command cannot create them. The placeholders are character devices:
// git lists them as untracked and `git add -A` fails on them.
var PlaceholderPaths = []string{
	".bash_profile", ".bashrc", ".profile", ".zprofile", ".zshrc",
	".gitconfig", ".gitmodules", ".ripgreprc", ".mcp.json", ".idea", ".vscode",
	".claude/agents", ".claude/commands", ".claude/hooks", ".claude/launch.json",
	".claude/loop.md", ".claude/output-styles", ".claude/routines",
	".claude/scheduled_tasks.json", ".claude/settings.json",
	".claude/settings.local.json", ".claude/skills", ".claude/workflows",
}

const (
	excludeBegin = "# BEGIN spore sandbox placeholders"
	excludeEnd   = "# END spore sandbox placeholders"
)

// PrepareCheckout adapts a git checkout to the sandbox: it adds the
// placeholder names to <git-common-dir>/info/exclude as root-anchored
// patterns, and creates an empty .gitmodules when missing, since libgit2
// (and so `nix develop` on a git flake) fails on the unreadable
// placeholder. Tracked files are unaffected by the excludes. Idempotent.
func PrepareCheckout(dir string) error {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return fmt.Errorf("sandbox: git common dir of %s: %w", dir, err)
	}
	exclude := filepath.Join(strings.TrimSpace(string(out)), "info", "exclude")
	if err := writeExcludeBlock(exclude); err != nil {
		return err
	}
	gm := filepath.Join(dir, ".gitmodules")
	if _, err := os.Lstat(gm); os.IsNotExist(err) {
		if err := os.WriteFile(gm, nil, 0o644); err != nil {
			return fmt.Errorf("sandbox: %w", err)
		}
	}
	return nil
}

func writeExcludeBlock(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("sandbox: %w", err)
	}
	var b strings.Builder
	b.WriteString(excludeBegin + "\n")
	for _, p := range PlaceholderPaths {
		b.WriteString("/" + p + "\n")
	}
	b.WriteString(excludeEnd + "\n")
	block := b.String()

	text := string(raw)
	if start := strings.Index(text, excludeBegin); start >= 0 {
		end := strings.Index(text[start:], excludeEnd)
		if end < 0 {
			return fmt.Errorf("sandbox: %s: %q without %q", path, excludeBegin, excludeEnd)
		}
		end += start + len(excludeEnd)
		if end < len(text) && text[end] == '\n' {
			end++
		}
		text = text[:start] + block + text[end:]
	} else {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += block
	}
	if text == string(raw) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	return os.WriteFile(path, []byte(text), 0o644)
}
