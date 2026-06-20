package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// BashInput is the shape claude-code passes as ToolInput for the Bash
// tool. Other tools use different shapes; the Bash leg of the decider
// only inspects this one.
type BashInput struct {
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
}

// pathInput is the shape claude-code passes as ToolInput for the
// file-touching tools. Read/Edit/Write use file_path, NotebookEdit
// uses notebook_path, Grep/Glob use path. Decode all three and take
// the first non-empty.
type pathInput struct {
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
	Path         string `json:"path"`
}

func (p pathInput) target() string {
	switch {
	case p.FilePath != "":
		return p.FilePath
	case p.NotebookPath != "":
		return p.NotebookPath
	default:
		return p.Path
	}
}

// ForbiddenPattern is one rule the decider checks against the Bash
// command line. Reason is surfaced verbatim in the deny response, so
// write it as a sentence the operator can act on.
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
			Re:     regexp.MustCompile(`\bgit[[:space:]]+push[[:space:]]+(--force|-f)\b`),
			Reason: "git push --force: confirm with the operator before force-pushing",
		},
	}
}

// sep / end model the characters that flank a command word in raw
// shell text (start-of-line or a shell separator before; whitespace,
// separator, or end after). endNoSpace excludes whitespace so a flag
// after the word (e.g. `set -e`) does not look like a bare dump.
const (
	sep        = "(^|[[:space:]`;&|(])"
	end        = "([[:space:]`;&|)]|$)"
	endNoSpace = "([`;&|)]|$)"
)

// envLeakPatterns blocks bash that would spill the process environment
// (which carries the matter API token and other credentials) into the
// agent transcript: bare env / printenv dumps, `set -x` / `bash -x`
// traces, the export / set state tables, compgen var listings, and
// /proc/<pid>/environ reads. These are not destructive like
// DefaultForbidden, so they ride in DefaultPreToolUseConfig rather
// than the universal forbidden set.
func envLeakPatterns() []ForbiddenPattern {
	const leak = "env-dump: this spills the process environment (including the matter API token) into the transcript. Reference a variable by name with redaction instead."
	return []ForbiddenPattern{
		// bare `env` / `printenv` dump: word followed by end or a pipe /
		// redirect, but not by a space+argument (so `env FOO=bar cmd`
		// and `printenv PATH` are left alone).
		{Re: regexp.MustCompile(sep + `(env|printenv)[ \t]*($|[|&;)` + "`" + `]|>)`), Reason: leak},
		// bare `export` / `set` (no operand -> dumps current state).
		{Re: regexp.MustCompile(sep + `(export|set)[[:space:]]*` + endNoSpace), Reason: leak},
		// `export -p` (dumps), `set -o` with no operand (options table).
		{Re: regexp.MustCompile(sep + `export[[:space:]]+-p` + end), Reason: leak},
		{Re: regexp.MustCompile(sep + `set[[:space:]]+-o[[:space:]]*` + endNoSpace), Reason: leak},
		// compgen -e (env vars), compgen -v (all shell vars).
		{Re: regexp.MustCompile(sep + `compgen[[:space:]]+-[ev]` + end), Reason: leak},
		// /proc/<pid>/environ read via any tool.
		{Re: regexp.MustCompile(`/proc/[^[:space:]` + "`" + `;|&]*environ`), Reason: leak},
		// `bash -x` / `set -x` trace flags echo expanded secrets.
		{Re: regexp.MustCompile(`(^|[[:space:]` + "`" + `;&|(])bash[[:space:]]+-[a-z]*x`), Reason: leak},
		{Re: regexp.MustCompile(sep + `set[[:space:]]+-[a-z]*x` + end), Reason: leak},
	}
}

// reGHPRCreate matches a `gh pr create` invocation as a command word
// (allowing a leading separator or env-prefix), so `something-gh pr
// create` does not trip it.
var reGHPRCreate = regexp.MustCompile("(^|[[:space:]`;&|(])gh[[:space:]]+pr[[:space:]]+create([[:space:]]|$)")

// reTaskFile matches a path under a tasks/ directory ending in .md,
// the shape matter projects from the work-item backend.
var reTaskFile = regexp.MustCompile(`(^|/)tasks/[^/]+\.md$`)

// reMemoryPath matches a claude-code auto-memory file: a
// .claude/projects/<sanitized-cwd>/memory/ tree. Works on a `~` or
// `$HOME` relative path too because it matches the substring.
var reMemoryPath = regexp.MustCompile(`/\.claude/projects/[^/]+/memory(/|$)`)

// defaultSecretPaths are path tails the decider refuses to read into
// inference: the runtime decrypt scratch, the local matter-token env
// file, and any age identity key. Tails (not absolute paths) so they
// match whether the agent wrote `~/.config/spore/secrets.env`, the
// $HOME-expanded form, or a Bash `cat` of either.
func defaultSecretPaths() []string {
	return []string{
		"/spore-secret-",            // XDG_RUNTIME_DIR decrypt scratch
		"/.config/spore/secrets.env", // local matter-token store
		"/age-key",                  // age identity (age-key.txt, ...)
	}
}

// PreToolUseConfig is the policy the decider enforces. The zero value
// enforces only the AskUserQuestion-in-worktree block; populate the
// fields (or use DefaultPreToolUseConfig) to turn on the rest.
type PreToolUseConfig struct {
	// Forbidden are bash command patterns that deny on first match.
	Forbidden []ForbiddenPattern
	// SecretPaths are path tails refused for any file or bash tool.
	SecretPaths []string
	// BlockMemoryWrites denies writes to claude-code auto-memory.
	BlockMemoryWrites bool
	// BlockManualPRCreate denies `gh pr create` from a worker worktree
	// (workers ship through the gated `spore task ship`).
	BlockManualPRCreate bool
	// BlockNewTaskFiles denies minting a NEW tasks/<slug>.md (the
	// backend projects them); editing an existing one stays allowed.
	BlockNewTaskFiles bool
	// Exists reports whether a (resolved) task path already exists. Nil
	// falls back to os.Stat. Injected for tests.
	Exists func(string) bool
}

// DefaultPreToolUseConfig is the policy the `spore hooks pretooluse`
// entry point enforces: the universal forbidden set plus the
// env-leak, secret-path, auto-memory, manual-PR, and task-mint guards
// distilled from the mcom block suite.
func DefaultPreToolUseConfig() PreToolUseConfig {
	return PreToolUseConfig{
		Forbidden:           append(DefaultForbidden(), envLeakPatterns()...),
		SecretPaths:         defaultSecretPaths(),
		BlockMemoryWrites:   true,
		BlockManualPRCreate: true,
		BlockNewTaskFiles:   true,
	}
}

// PreToolUse is the bash-and-AskUserQuestion subset of Decide, kept for
// callers that only carry a forbidden-pattern list.
func PreToolUse(req Request, forbidden []ForbiddenPattern) Response {
	return Decide(req, PreToolUseConfig{Forbidden: forbidden})
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

// Decide evaluates a PreToolUse request against cfg and returns the
// response claude-code receives. An empty (allow) Response means the
// tool call proceeds. Checks run in order; the first deny wins.
//
// AskUserQuestion is denied unconditionally inside a worker worktree
// (.worktrees/<slug>): no operator reads a worker turn, so the agent
// must act on a stated assumption instead. Bash commands are checked
// against the forbidden set, then for secret-path and `gh pr create`
// references. File-touching tools are checked for secret-path reads,
// auto-memory writes, and new-task-file minting.
func Decide(req Request, cfg PreToolUseConfig) Response {
	if req.ToolName == "AskUserQuestion" && isWorktreeCWD(req.CWD) {
		return deny("AskUserQuestion: workers run autonomously; no operator is reading. State your assumption in the task file's plan section and act on it. Flip the task to status=blocked only after exhausting alternatives.")
	}

	switch req.ToolName {
	case "Bash":
		return decideBash(req, cfg)
	case "Read", "Edit", "Write", "MultiEdit", "NotebookEdit", "Grep", "Glob":
		return decidePath(req, cfg)
	default:
		return Response{}
	}
}

func decideBash(req Request, cfg PreToolUseConfig) Response {
	var in BashInput
	if err := json.Unmarshal(req.ToolInput, &in); err != nil {
		return Response{}
	}
	cmd := strings.TrimSpace(in.Command)
	if cmd == "" {
		return Response{}
	}
	for _, p := range cfg.Forbidden {
		if p.Re.MatchString(cmd) {
			return deny(p.Reason)
		}
	}
	for _, s := range cfg.SecretPaths {
		if strings.Contains(cmd, s) {
			return deny("secret read: `" + s + "` holds decrypted credentials; reading it into a tool call leaks it into the transcript. Decrypt outside the agent loop.")
		}
	}
	if cfg.BlockManualPRCreate && isWorktreeCWD(req.CWD) && reGHPRCreate.MatchString(cmd) {
		return deny("gh pr create: workers ship through `spore task ship`, which gates on `spore wt-check` (lint+tests), pushes wt/<slug>, opens the PR, waits for CI, merges, and runs the close gates. Run that instead of opening a PR by hand.")
	}
	return Response{}
}

func decidePath(req Request, cfg PreToolUseConfig) Response {
	var in pathInput
	if err := json.Unmarshal(req.ToolInput, &in); err != nil {
		return Response{}
	}
	target := in.target()
	if target == "" {
		return Response{}
	}
	clean := filepath.ToSlash(filepath.Clean(target))

	for _, s := range cfg.SecretPaths {
		if strings.Contains(clean, s) {
			return deny("secret read: `" + s + "` holds decrypted credentials; reading it into a tool call leaks it into the transcript. Decrypt outside the agent loop.")
		}
	}

	if !isWriteTool(req.ToolName) {
		return Response{}
	}

	if cfg.BlockMemoryWrites && reMemoryPath.MatchString(clean) {
		return deny("auto-memory write: spore keeps standing context in tier CLAUDE.md / AGENTS.md and docs/<topic>.md, not claude-code auto-memory. Write the fact there instead.")
	}

	if cfg.BlockNewTaskFiles && reTaskFile.MatchString(clean) {
		exists := cfg.Exists
		if exists == nil {
			exists = func(p string) bool { _, err := os.Stat(p); return err == nil }
		}
		if !exists(resolveAgainstCWD(target, req.CWD)) {
			return deny("new task file: tasks/<slug>.md is projected from the work-item backend (matter). Do not hand-mint one; file the ticket upstream (or `spore task new`) and let projection write it. Editing an existing task file is fine.")
		}
	}

	return Response{}
}

func isWriteTool(name string) bool {
	switch name {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return true
	default:
		return false
	}
}

// resolveAgainstCWD makes path absolute for an os.Stat, joining a
// relative path onto the request cwd when one is present.
func resolveAgainstCWD(path, cwd string) string {
	if filepath.IsAbs(path) || cwd == "" {
		return path
	}
	return filepath.Join(cwd, path)
}

// isWorktreeCWD reports whether path sits inside a `.worktrees/`
// directory, the layout `wt` uses for worker checkouts. An empty cwd
// (older harnesses that did not forward it) returns false so the
// AskUserQuestion block stays opt-in by location.
func isWorktreeCWD(cwd string) bool {
	if cwd == "" {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(cwd))
	if strings.Contains(clean, "/.worktrees/") {
		return true
	}
	return strings.HasSuffix(clean, "/.worktrees") || clean == ".worktrees"
}
