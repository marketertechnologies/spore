package hooks

import (
	"fmt"

	"github.com/versality/spore/internal/sporetoml"
)

// kernelDeny is always emitted, even with default_deny = false: it is
// the inbox-messaging contract, not a guardrail. SendMessage ships as
// permissions.deny, not the top-level disallowedTools key: on
// claude-code 2.1.299 the latter is inert, while permissions.deny
// removes the tool even under --dangerously-skip-permissions.
var kernelDeny = []string{"SendMessage"}

// defaultDeny is the guardrail set. Rule syntax per
// https://code.claude.com/docs/en/permissions: a trailing " *" is a
// word-bounded prefix match that also matches the bare command, so
// --force-with-lease (used by the GitHub App mirror) stays allowed.
// "+*" has no space before the star: a plain glob that catches a
// +refspec, which force-updates that ref without any flag.
// Read rules also cover cat/head/sed on the same path, but not a
// subprocess that opens the file itself, which keeps
// spore-with-secrets working. Rules are matched per subcommand of a
// compound command and past leading env assignments. The Bash
// credentials rule catches readers the Read rule does not know
// (base64, grep, jq) by path substring, and the Edit rule (which
// also covers Write) stops an agent logging the host out; claude-code itself still
// reads the file, outside its own tools.
var defaultDeny = []string{
	"Read(~/.config/spore/**/secrets.env)",
	"Read(~/.config/spore/**/*.pem)",
	"Read(~/.claude/.credentials.json)",
	"Edit(~/.claude/.credentials.json)",
	"Bash(*.claude/.credentials.json*)",
	"Bash(git push --force *)",
	"Bash(git push -f *)",
	"Bash(git push * --force)",
	"Bash(git push * --force *)",
	"Bash(git push * -f)",
	"Bash(git push * -f *)",
	"Bash(git push +*)",
	"Bash(git push * +*)",
}

// execVectorDeny keeps the file tools (an Edit rule also covers Write
// and NotebookEdit) off files an unsandboxed process later runs: the
// Bash sandbox does not wrap them, and agents run with
// --dangerously-skip-permissions. "//" is the filesystem root, so
// "//**/" matches at any depth; a bare "**/" only matches under the
// cwd, and a leading "/" in a managed file has no documented anchor.
// On Linux the sandbox drops write globs, so the "//**/" rules never
// reach bwrap and cannot break git init in $TMPDIR. The "~/" rules do
// reach it, but every one lies outside the sandbox's write roots.
// ~/.claude/skills and the memory dirs stay writable on purpose; skill
// frontmatter hooks are a known residual (docs/sandbox.md).
var execVectorDeny = []string{
	"Edit(//**/.git)",
	"Edit(//**/.git/**)",
	"Edit(//**/*.git/hooks/**)",
	"Edit(//**/*.git/config)",
	"Edit(//**/.envrc)",
	"Edit(//**/.mcp.json)",
	"Edit(//**/.claude/settings*.json)",
	"Edit(//**/.claude/hooks/**)",
	"Edit(//**/.claude/agents/**)",
	"Edit(//**/.claude/commands/**)",
	"Edit(~/.claude.json)",
	"Edit(~/.claude/plugins/**)",
	"Edit(~/.bashrc)",
	"Edit(~/.bash_profile)",
	"Edit(~/.bash_login)",
	"Edit(~/.bash_logout)",
	"Edit(~/.profile)",
	"Edit(~/.zshrc)",
	"Edit(~/.zshenv)",
	"Edit(~/.zprofile)",
	"Edit(~/.zlogin)",
	"Edit(~/.tmux.conf)",
	"Edit(~/.config/tmux/**)",
	"Edit(~/.config/direnv/**)",
	"Edit(~/.gitconfig)",
	"Edit(~/.config/git/**)",
	"Edit(~/.config/systemd/user/**)",
	"Edit(~/.config/environment.d/**)",
	"Edit(~/.huskyrc)",
	"Edit(~/.config/husky/**)",
	"Edit(~/.config/fish/**)",
	"Edit(~/.npmrc)",
	"Edit(~/.yarnrc)",
	"Edit(~/.yarnrc.yml)",
	"Edit(~/.local/bin/**)",
	"Edit(~/.ssh/**)",
	"Edit(~/.config/spore/**)",
}

// DefaultDeny returns the kernel rules, the guardrail set and the
// exec-vector set, in the order Settings emits them.
func DefaultDeny() []string {
	return DefaultPermissions().DenyRules()
}

// Permissions is the [permissions] section of a project's spore.toml.
type Permissions struct {
	DefaultDeny bool
	Deny        []string
}

// DefaultPermissions is what a project without [permissions] gets.
func DefaultPermissions() Permissions {
	return Permissions{DefaultDeny: true}
}

// LoadPermissions reads [permissions] from <projectRoot>/spore.toml.
// A missing file or section yields DefaultPermissions.
func LoadPermissions(projectRoot string) (Permissions, error) {
	p := DefaultPermissions()
	entries, err := sporetoml.ReadSection(projectRoot, "permissions")
	if err != nil {
		return p, fmt.Errorf("permissions: %w", err)
	}
	seen := make(map[string]bool)
	for _, e := range entries {
		if seen[e.Key] {
			return p, fmt.Errorf("permissions: line %d: duplicate key %q in [permissions]", e.Line, e.Key)
		}
		seen[e.Key] = true
		switch e.Key {
		case "default_deny":
			v, err := e.Bool()
			if err != nil {
				return p, fmt.Errorf("permissions: %w", err)
			}
			p.DefaultDeny = v
		case "deny":
			v, err := e.Strings()
			if err != nil {
				return p, fmt.Errorf("permissions: %w", err)
			}
			p.Deny = v
		default:
			return p, fmt.Errorf("permissions: line %d: unknown key %q in [permissions]", e.Line, e.Key)
		}
	}
	return p, nil
}

// DenyRules merges the kernel rules, the default set (unless opted
// out) and the project rules, dropping empties and duplicates while
// keeping first-seen order.
func (p Permissions) DenyRules() []string {
	var out []string
	seen := make(map[string]bool)
	add := func(rules []string) {
		for _, r := range rules {
			if r == "" || seen[r] {
				continue
			}
			seen[r] = true
			out = append(out, r)
		}
	}
	add(kernelDeny)
	if p.DefaultDeny {
		add(defaultDeny)
		add(execVectorDeny)
	}
	add(p.Deny)
	return out
}
