**Status**: phase 0 spike done 2026-10-16; phase 1 (`spore show`) landed 2026-10-16 (v0.12.0); phase 2 next; `/ide` deferred

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

### Decision (2026-10-15): second unrestricted key

- Operator adds a second laptop key to the spore user's
  `authorizedKeys` with no `command=` and no `no-port-forwarding`.
  Pilot keys keep their forced `spore-attach pilot <name>`.
- `spore-attach` falls through to bash for any `-c` command that is
  not an attach mode (PR #71, v0.11.0), so Remote-SSH can run its
  install and start commands.
- Rejected: a separate editor user (the `~/.claude/ide` lock dir is
  per-user, which breaks `/ide`), SSH as root (wrong user for `/ide`
  and file ownership).
- Open follow-up: the inline `sporeAttach` in
  `bootstrap/flake/configuration.nix` (used by `spore infect` hosts)
  lacks the fall-through.

Operator steps before phase 0: generate the laptop key, add it as a
plain line in the host's spore `authorizedKeys`, rebuild once the
host's spore input carries v0.11.0, add a `~/.ssh/config` Host entry.

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
GitLens-style extension; not settled in the spike, pick in phase 2.

The workspace also sets `files.watcherExclude` and `search.exclude`
for `.worktrees/**` on the main checkout folder. Each worktree is
added back as its own root instead. Spike measurement: marketer holds
about 212k files, about 152k of them under 7 worktrees, and crawling
them made the Explorer and the git view slow.

### 2. Jump to file:line: `spore show`

`spore show <path>[:<line>[:<col>]]` opens the location in the
operator's connected editor.

- Mechanism: VS Code Remote-SSH leaves a CLI IPC socket on the host
  (`VSCODE_IPC_HOOK_CLI`, under `/run/user/<uid>/vscode-ipc-*.sock`)
  and a remote `code` CLI at
  `~/.vscode-server/cli/servers/Stable-<hash>/server/bin/remote-cli/code`.
  `spore show` runs `code --reuse-window --goto <abs-path>:<line>`
  with `VSCODE_IPC_HOOK_CLI` set to a live socket (confirmed in the
  spike, see findings).
- Socket discovery must probe liveness (connect, expect no
  `ECONNREFUSED`). Dead sockets stay on disk after every window
  reload, so the newest mtime is not enough. Among live sockets, try
  the newest first.
- Relative paths resolve against the caller's cwd, so a worker in
  `.worktrees/<slug>` opens its own copy of the file.
- No editor connected: print one line to stderr and exit 0. A missing
  editor never fails an agent turn.
- Rule fragment (`rules/core/show-in-editor.md`): when a coordinator
  cites code in a decision or diagnosis, it also runs `spore show` on
  the primary location. One call per decision, not per mention.
- Backend seam: `internal/editor` with a VS Code backend first; a
  Neovim backend (option C) can implement the same interface later.
- Window choice (phase 1): newest live socket first, falling through
  to the next live one if the CLI call fails. No cheap way exists to
  tell which window a socket serves: `code --status` over a socket
  lists every window of the client app, not the socket's own, and the
  remote extension host's workspace storage carries no folder path.
  With one window per project and one connected window in practice,
  newest-live is right; per-path matching stays an open question.
- A missing target file also prints one line and exits 0, so a typo
  does not open an empty buffer.

### 3. Connected navigation: `/ide` on the coordinator only (deferred)

Out of the MVP by operator decision (2026-10-16): goals 1 and 2 do not
need the Claude Code extension, and `/ide` serves only goal 3
(selection and diagnostics flowing to the coordinator). Kept as an
optional later phase.


- Coordinator panes connect to the editor (`/ide`, or
  `CLAUDE_CODE_AUTO_CONNECT_IDE=1` from the coordinator launch shim)
  so the operator's selection flows to the coordinator and the
  coordinator can read diagnostics.
- Worker and role panes stay unconnected (`diffTool: terminal` /
  no auto-connect) so N workers in bypass mode do not open diff tabs
  in the operator's window. Revisit if the spike shows diffs are
  suppressed in bypass mode anyway.

## Phase 0 findings (2026-10-16)

Operator connected VS Code over Remote-SSH to `/home/spore/marketer`.

- Connect needed a client workaround until `spore-attach` handles
  no-command logins (PR #72): `RemoteCommand bash -l` and
  `RequestTTY no` in the ssh Host entry, plus
  `remote.SSH.enableRemoteCommand: true`.
- `code --goto` from a plain tmux shell on the host works: the
  operator saw `CLAUDE.md` open at line 20. Socket and CLI paths are
  as in section 2.
- Dead IPC sockets pile up: five socket files after a few reloads,
  one live.
- Opening a folder reloads the window and gives it a new socket.
  A window can also come back from a reload with no folder open.
- With the Claude Code extension installed on the remote side, its
  lock file `~/.claude/ide/<port>.lock` lists the workspace folders
  and `/ide` connects from a tmux pane. Claude Code says only one
  instance can connect to a VS Code window at a time, so
  coordinator-only is forced, not just preferred.
- Bug: VS Code resolves the user environment by running
  `$SHELL -i -l -c '<cmd>'`. `spore-attach` only falls through when
  `-c` is the first argument, so it exits 2 (`unknown mode: -i`) and
  the server log shows `Unable to resolve your shell environment`.
  This also breaks VS Code's built-in terminal (no command but a TTY
  gets the coord landing). Not checked: terminal link clicks, diff
  tabs for bypass-mode workers (deferred with `/ide`).
- Not an editor issue but found on the way: a stuck `ugrep` searching
  from `/` (left by a dead marketer worker's Grep call) held about 7
  of 8 cores for 2 days 17 hours and made everything slow.

1. `spore show` + `internal/editor` (VS Code backend) + rule fragment.
2. `spore code workspace` + reconciler regeneration.
3. Deferred: coordinator auto-connect in the launch shim; worker opt-out.
4. Optional: Neovim backend; NixOS module knob to install a pinned
   VS Code server extension set.

Each phase after 0 is one fleet task with its own PR and version bump
(phase 1 and 2 are features).

## Open questions

- `spore show` should pick the window that holds the path (one window
  per project, decided 2026-10-15). Phase 1 ships newest-live (see
  section 2). `code --status` was tried and ruled out: it is
  client-wide. Remaining candidate: a tiny companion extension that
  reports its workspace folders keyed by its `VSCODE_IPC_HOOK_CLI`.
- Consumer impact: phases 1 to 3 ship to downstream consumers; the
  rule fragment should be opt-in per consumer list until proven.
