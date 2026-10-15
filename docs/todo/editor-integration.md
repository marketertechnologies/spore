**Status**: design; decisions taken 2026-10-15; phase 0 blocked on SSH access model; nothing implemented

# Editor integration: see and navigate agent work in an IDE

## Problem

The operator watches coordinators and workers through tmux panes over
SSH. Everything about the code arrives as terminal text:

1. Worker changes are only visible as `git diff` output pasted into a
   pane, or after a PR exists. There is no live view of what a worker
   is changing before the branch is pushed.
2. Coordinators cite code as `path:line` while explaining a task
   decision. The operator has to find that spot by hand.
3. There is no way to browse the code alongside the agents, or to
   point an agent at a selection.

Goal: a connected editor where agent activity and the code itself sit
side by side, and where an agent citing a line can move the editor
there.

## Facts established (2026-10-15)

Host:

- Operator connects over SSH from a laptop; tmux clients attach to
  `spore/<proj>/coordinator` sessions.
- No VS Code server has ever connected (`~/.vscode-server` absent), no
  code-server, no Neovim. `programs.nix-ld.enable = true` is already
  set, which VS Code Remote-SSH's server needs on NixOS.

Claude Code IDE integration (docs at code.claude.com/docs/en/vs-code,
/jetbrains, /settings-reference):

- The VS Code extension starts a loopback WebSocket server per window
  and writes `~/.claude/ide/<port>.lock` (0600, dir 0700). A CLI
  running in an external terminal (tmux) connects with `/ide`.
  `autoConnectIde` defaults to false; `CLAUDE_CODE_AUTO_CONNECT_IDE`
  overrides per session.
- When connected: edits open in the IDE's native diff view, the
  editor selection and active file are sent with each prompt, and the
  agent can read IDE diagnostics (`mcp__ide__getDiagnostics`).
- `diffTool: "terminal"` in `~/.claude.json` keeps diffs in the
  terminal.
- JetBrains has an official plugin with the same `/ide` flow. Neovim
  has a community implementation of the protocol
  (`coder/claudecode.nvim`).

Not documented, must be tested:

- `/ide` over VS Code Remote-SSH. Community reports show failures;
  our layout (extension host and tmux CLI on the same machine, shared
  loopback and lock dir) is the favorable case.
- How the CLI picks among several lock files, and how session cwd
  under `.worktrees/<slug>` maps to a workspace folder.
- Whether the diff view opens for edits made under
  `--dangerously-skip-permissions` (all workers run this way).
- Opening a file at a line from the CLI. The integration does not
  offer it; problem 2 needs a spore-side mechanism.

## Operator decisions (2026-10-15)

- Editor: desktop VS Code over Remote-SSH (option A).
- Diffs: watch only. No approval step in the editor; panes stay in
  bypass mode.
- Windows: one VS Code window per project.
- Rollout: pilot the editor rule on the marketer consumer first, not
  spore and not crm-gateway.

## Blocker: SSH access model (found 2026-10-15)

Operator keys in `/etc/ssh/authorized_keys.d/spore` carry
`command="/usr/local/bin/spore-attach pilot <name>",no-port-forwarding`,
and the spore user's login shell is `spore-attach`. Remote-SSH needs
to run its own install/start commands and to forward a port to its
server. Today:

- The forced command replaces whatever VS Code asks to run with a
  tmux attach.
- `no-port-forwarding` blocks the tunnel to the VS Code server.
- Even without the forced command, `spore-attach -c "<cmd>"` reshapes
  argv and exits `unknown mode` for anything that is not an attach.

Phase 0 cannot start until a VS Code access path exists. Choosing it
is a privileged host change (operator, `/etc/nixos`).

## Options compared

| | Live diffs before push | Jump to file:line | Cost to operator |
|---|---|---|---|
| A. Desktop VS Code + Remote-SSH | SCM view, live | VS Code terminal links, or spore drives the `code` CLI | Low |
| B. code-server in a browser | Same as A | Same as A | Low; needs tunnel + auth; Claude extension availability on Open VSX unverified |
| C. Neovim in a tmux pane | diffview-style plugins | Trivial: `nvim --server <sock> --remote` | High unless already a vim user |

Recommendation: A. Most familiar editor, stays on the operator's
machine, host prerequisite already met. Keep C's driving model in
mind: whatever spore builds for A should sit behind one command so a
Neovim backend can be added later.

## Design (option A)

### 1. Live diffs: one workspace for all active worktrees

`spore code workspace` writes `<repo>/.spore/spore.code-workspace`, a
multi-root workspace with the main checkout plus one folder per active
`.worktrees/<slug>`, labeled by slug. The fleet reconciler regenerates
it when tasks start or finish. The operator opens that file over
Remote-SSH; the Source Control view then lists each worker's
uncommitted changes live.

Committed-but-unpushed work: each folder's branch compared with the
base (`ovidiu-spore-deviation`). Built-in "compare with branch" or a
GitLens-style extension; pick during the spike.

### 2. Jump to file:line: `spore show`

`spore show <path>[:<line>[:<col>]]` opens the location in the
operator's connected editor.

- Mechanism: VS Code Remote-SSH leaves a CLI IPC socket on the host
  (`VSCODE_IPC_HOOK_CLI`, under `/run/user/<uid>/vscode-ipc-*.sock`)
  and a remote `code` CLI under `~/.vscode-server`. `spore show`
  finds the newest live socket and runs `code --reuse-window --goto
  <abs-path>:<line>` against it. Exact discovery to be confirmed in
  the spike.
- Relative paths resolve against the caller's cwd, so a worker in
  `.worktrees/<slug>` opens its own copy of the file.
- No editor connected: print one line to stderr and exit 0. A missing
  editor never fails an agent turn.
- Rule fragment (`rules/core/show-in-editor.md`): when a coordinator
  cites code in a decision or diagnosis, it also runs `spore show` on
  the primary location. One call per decision, not per mention.
- Backend seam: `internal/editor` with a VS Code backend first; a
  Neovim backend (option C) can implement the same interface later.

### 3. Connected navigation: `/ide` on the coordinator only

- Coordinator panes connect to the editor (`/ide`, or
  `CLAUDE_CODE_AUTO_CONNECT_IDE=1` from the coordinator launch shim)
  so the operator's selection flows to the coordinator and the
  coordinator can read diagnostics.
- Worker and role panes stay unconnected (`diffTool: terminal` /
  no auto-connect) so N workers in bypass mode do not open diff tabs
  in the operator's window. Revisit if the spike shows diffs are
  suppressed in bypass mode anyway.

## Phases

0. Spike (operator-bound: needs the laptop). Operator connects VS Code
   over Remote-SSH and opens `/home/spore/spore`. Coordinator then
   verifies, on the host: lock file appears in `~/.claude/ide/`;
   `/ide` connects from a tmux pane; IPC socket location and a manual
   `code --goto` from a tmux pane; diff-view behavior for a bypass-mode
   worker; terminal link clicks inside VS Code's terminal running
   `tmux attach`. Record findings in this doc.
1. `spore show` + `internal/editor` (VS Code backend) + rule fragment.
2. `spore code workspace` + reconciler regeneration.
3. Coordinator auto-connect in the launch shim; worker opt-out.
4. Optional: Neovim backend; NixOS module knob to install a pinned
   VS Code server extension set.

Each phase after 0 is one fleet task with its own PR and version bump
(phase 1 and 2 are features).

## Open questions

- Does the operator want diffs only, or also an approval step in the
  editor for coordinator edits (Manual mode)? Today all panes run in
  bypass mode.
- Multiple projects (marketer, crm-gateway) share the host: one VS
  Code window per project, or one workspace spanning projects?
  `spore show` must pick the window that holds the path.
- Consumer impact: phases 1 to 3 ship to downstream consumers; the
  rule fragment should be opt-in per consumer list until proven.
