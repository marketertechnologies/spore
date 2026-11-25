package hooks

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
)

// BashInput is the shape claude-code passes as ToolInput for the Bash
// tool. Other tools use different shapes; PreToolUse only inspects
// Bash.
type BashInput struct {
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
}

// ForbiddenPattern is one rule the PreToolUse decider checks against
// the Bash command line. Reason is surfaced verbatim in the deny
// response, so write it as a sentence the operator can act on.
type ForbiddenPattern struct {
	Re     *regexp.Regexp
	Reason string
}

// DefaultForbidden is the starter set of bash patterns spore blocks
// out of the box. Downstream projects override with their own set
// (e.g. nixos-rebuild for a NixOS host repo, terraform apply for an
// infra repo). Keep the kernel set small and obviously universal.
func DefaultForbidden() []ForbiddenPattern {
	return []ForbiddenPattern{
		{
			Re:     regexp.MustCompile(`(?m)^[[:space:]]*sudo([[:space:]]|$)`),
			Reason: "sudo: ask the operator instead of escalating from a hook context",
		},
		{
			Re:     regexp.MustCompile(`\brm[[:space:]]+(-[a-zA-Z]*r[a-zA-Z]*f|-[a-zA-Z]*f[a-zA-Z]*r)[[:space:]]+/(\s|$)`),
			Reason: "rm -rf /: refusing root-tree wipe",
		},
		{
			// Matches inside quoted text on purpose, so `bash -c "git push -f"`
			// is caught; a command that only mentions a force push is denied too.
			Re:     regexp.MustCompile(`\bgit(?:[[:space:]]+(?:-[cC][[:space:]]+(?:"[^"]*"|'[^']*'|[^[:space:]]+)|-[^[:space:]]+))*[[:space:]]+push\b[^|;&\n]*[[:space:]](?:--force|-[a-zA-Z]*f[a-zA-Z]*|["']?\\?\+[^[:space:];|&)]+)(?:[[:space:];|&)]|$)`),
			Reason: "git push --force (or a +refspec): confirm with the operator before force-pushing",
		},
		{
			// A configured push refspec (`git -c remote.origin.push=+...`)
			// force-pushes with no + on the push command line itself.
			Re:     regexp.MustCompile(`\bgit[[:space:]][^|;&\n]*-c[[:space:]]*["']?remote\.[^[:space:]=]+\.push=["']?\\?\+[^|;&\n]*[[:space:]]push\b`),
			Reason: "git push with a +refspec in remote.<name>.push: confirm with the operator before force-pushing",
		},
	}
}

// PreToolUse evaluates a PreToolUse request and returns the response
// claude-code should receive. Non-Bash tools are always allowed (this
// scaffold only knows about shell). Bash commands are checked against
// the forbidden set; the first match denies with that pattern's
// reason.
func PreToolUse(req Request, forbidden []ForbiddenPattern) Response {
	if req.ToolName != "Bash" {
		return Response{}
	}
	var in BashInput
	if err := json.Unmarshal(req.ToolInput, &in); err != nil {
		return Response{}
	}
	cmd := strings.TrimSpace(in.Command)
	if cmd == "" {
		return Response{}
	}
	for _, p := range forbidden {
		if p.Re.MatchString(cmd) {
			return deny(p.Reason)
		}
	}
	if reason := ghOutsideSandbox(cmd); reason != "" {
		return deny(reason)
	}
	return Response{}
}

func deny(reason string) Response {
	return Response{
		HookSpecificOutput: &HookSpecificOutput{
			HookEventName:            "PreToolUse",
			PermissionDecision:       Deny,
			PermissionDecisionReason: reason,
		},
	}
}

var sporeGH = regexp.MustCompile(`\bspore[[:space:]]+gh\b([^;|&\n]*)`)

// ghOutsideSandbox vets `spore gh`, which the sandbox excludes so it
// can read the GitHub App key: an agent may run `spore gh exec` around
// git or gh, and read-only `spore gh config`. Anything else behind it
// would run unsandboxed (exec of another program), print the token, or
// repoint the key (config --set). Returns the deny reason, or "".
func ghOutsideSandbox(cmd string) string {
	for _, m := range sporeGH.FindAllStringSubmatch(cmd, -1) {
		args := strings.Fields(m[1])
		sub := ""
		if len(args) > 0 {
			sub, args = args[0], args[1:]
		}
		switch sub {
		case "exec":
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			prog := ""
			if len(args) > 0 {
				prog = filepath.Base(strings.Trim(args[0], `"'`))
			}
			if prog != "git" && prog != "gh" {
				return "spore gh exec runs outside the Claude Code sandbox with the GitHub App token: wrap only git or gh (spore gh exec -- git push ..., spore gh exec -- gh api ...)"
			}
		case "config":
			if strings.Contains(m[1], "--set") {
				return "spore gh config --set changes the GitHub App setup: ask the operator to run it"
			}
		case "", "help", "-h", "--help":
		default:
			return "spore gh " + sub + " runs outside the Claude Code sandbox: use spore gh exec -- gh api ... instead of handling the token"
		}
	}
	return ""
}
