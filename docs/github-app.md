# GitHub App draft-PR flow

With `pr_flow = "draft"`, the role loop runs on a visible draft PR. A
GitHub App pushes the task branch, opens the PR, and posts every
round; the operator gives the only approval and merges. Design and
locked decisions: `docs/todo/github-app-pr-flow.md`. For ad-hoc use
from a coordinator (detect, `spore gh pr|api|push`, App vs personal token),
see `spore recipes show github`. On a host running the secrets broker the App key lives with the broker and the mirror pushes through it; see `docs/secrets.md`.

## What happens

```
engineer round 1 done  -> push branch (app), open DRAFT PR, assign operator
review verdict         -> COMMENT review "<label> (round N): <verdict>", items numbered R1.1, C2.3, ...; `path:line` items inline on the diff
engineer round 2+      -> push, PR comment "Engineer (round N)": outcome table by ID, then notes
PhaseDone              -> gh pr edit (engineer `pr` title/body, if any); gh pr ready; coordinator: "<slug>: done, PR #N ready for operator approval: <url>"
PhaseEscalated         -> one PR comment naming the cap and reviewer; PR stays draft
```

`<label>` is the slot's display name from its mode (`Requirements
review`, `Code review`; see "Comment IDs" in `docs/role-loop.md`).

`DriveRoleLoop` does all of it (`internal/fleet/rolegithub.go`). Agents
still only write their `.spore/<slug>/` files; GitHub is a mirror,
never read back for phase. The app never submits `APPROVE` or
`REQUEST_CHANGES`: it is the PR author, and the operator stays the
only real gate.

The PR opens with the task title and the brief's first paragraph.
At done, the driver replaces both with the `pr` object of the latest
engineer response that carries one (`{"title", "body"}`, see
`bootstrap/roles/engineer.md`): `gh pr edit --title ... --body-file -`
with the engineer's body, a line with each reviewer's final verdict,
and the spore footer. Without any `pr` the PR keeps its opening title
and body. The applied response and a hash of its `pr` are recorded as
`pr_description` in `github.json`, so a re-tick never edits twice. An
edit failure is logged and told to the coordinator once, is not
retried, and never holds back `gh pr ready`.

Mirror state lives in `.spore/<slug>/github.json` (PR number and url,
last pushed sha, posted file names, ready flag, last error). Each tick
posts only what is missing, in loop order. A GitHub failure never
blocks the loop: it logs to stderr, retries next tick, and tells the
coordinator once per distinct error (`<slug>: github mirror failed,
retrying each tick: ...`); a git/gh error counts as the same error
when only its stderr differs. If the loop reaches done while the
mirror is failing, the coordinator also gets the plain `<slug>: done`
once. Every git/gh call and the token mint is capped at 60 seconds,
with git and gh prompts disabled, so a hung connection delays a tick
by about a minute at most. Deleting `github.json` re-posts every
comment; an open PR for the branch is adopted, not duplicated.

Every gh call runs with `GH_REPO` set from `origin`'s URL, so gh and
`git push` always target the same repo; without it gh prefers an
`upstream` remote in a fork-style clone.

A verdict is posted with `gh api repos/<owner>/<repo>/pulls/<n>/reviews`
(`event: COMMENT`, `commit_id` = the last pushed sha). Each graded
comment whose `where` is `path:line` becomes an inline comment
(`side: RIGHT`) carrying its ID, grade, text, trigger and suggestion;
the rest stay in the review body. GitHub rejects the whole review
with HTTP 422 when an anchor is outside the diff and posts nothing,
so the mirror retries: it reads `pulls/<n>/files` and keeps only the
anchors on a line the patches show, folding the others into the body.
When the patches cannot be read, no anchor is left, or that retry is
rejected too, the review goes out with every comment in the body. A
verdict is recorded as posted only after GitHub accepts it.

The PR body is the first prose paragraph of `.spore/<slug>/spec.md`
(the task brief, skipping headings and the `**Status**` line) plus a
fixed footer; a paragraph naming an absolute path (`/...` or `~/...`)
is replaced by the task title. Everyone with repo access reads it, so
open the brief with a plain summary: no host paths, no
coordinator-only notes. The rest of the brief stays off GitHub.

Everything the mirror posts (PR title and body, review bodies, inline comments, engineer comments, the final `pr` title and body) passes through one redaction step (`internal/redact`) on its way to gh. It replaces, with `[redacted]`: every value of 8 or more characters in the layered secrets files (`${XDG_CONFIG_HOME:-~/.config}/spore/secrets.env`, then `.../spore/<project>/secrets.env`), marked `[redacted:NAME]` with the variable name, never the value; GitHub token shapes (`ghp_`, `ghs_`, `gho_`, `ghu_`, `ghr_`, `github_pat_`) and Anthropic keys (`sk-ant-`); PEM private key blocks; and absolute paths under `$HOME`, `/home/<user>` or `/Users/<user>`. Shorter secrets.env values are left alone, since they would shred ordinary prose. Commit contents are not scanned. The same step covers `spore gh pr|api` on every host (locally, or broker-side behind the secrets broker). Redacted are stdin (JSON-aware after `--input -` or `--input=-`), the values of `--title` / `--body` (and `-t` / `-b` under `gh pr` and `gh issue`), `--comment` / `-c` under `close` and `reopen`, `--subject` under `gh pr merge`, and the values of `gh api` fields (`-f`, `-F`, `--field`, `--raw-field`), in every spelling: `--x v`, `--x=v`, `-x v`, `-xv`, `-x=v`, and a short group such as `-iF k=v`. Field keys are redacted too (`-f '<secret>=v'`, `-f 'body[<secret>]=v'`). Refs, logins, repo names and other flags are left alone. A field whose key names a ref or enum (`head`, `base`, `ref`, `sha`, `commit_id`, `owner`, `repo`, `name`, `number`, `path`, `assignees`, `reviewers`, `team_reviewers`, `labels`, `state`, `event`, `side`, `start_side`) gets only the token and PEM patterns: a secrets.env value colliding with one would break the call, but a token shape is never a real ref.

Each redaction appends a `redaction` event to the project ledger (`fields`: `class` is `secrets_env`, `token`, `pem` or `home_path`; `where` is the flag, `field <key>`, `field <key> key` (the secret sat in the key) or `stdin`, the key itself redacted; `count`; `via` is `mirror` or `spore-gh`; `cmd` is the gh verb or API path), never the matched value, and `spore report` sums them per class (docs/report.md). Behind the broker the broker returns its hits and the caller records them, since the broker cannot write the ledger.

The PR base is the task's `base:` frontmatter (a stack parent branch),
else the remote default branch (`git symbolic-ref
refs/remotes/origin/HEAD`; run `git remote set-head origin --auto` if
it is missing).

A branch whose last pushed commit is no longer an ancestor of HEAD
(rebased, amended) goes up with `--force-with-lease` pinned to that
commit; otherwise pushes are fast-forward only.

## One-time setup

Org-owner path: create the app once per org. Teammates joining an
existing app skip to [Teammate onboarding](#teammate-onboarding).

1. Org owner: Settings -> Developer settings -> GitHub Apps -> New.
   Permissions: Contents read/write, Pull requests read/write,
   Metadata read. No webhook. One app per org, shared by every
   teammate.
2. Install it on the target repos. Note the app id (app settings
   page) and the installation id (the number at the end of the
   installation's settings URL).
3. Generate a private key per teammate (see
   [Teammate onboarding](#teammate-onboarding)). Store it outside the
   repo, mode 0600, e.g. `~/.config/spore/<project>/github-app.pem`.
4. Point the project's `origin` at https (an ssh remote is refused:
   it would push as your own key):
   `git remote set-url origin https://github.com/<owner>/<repo>.git`.
5. Configure, then verify a token mints:

   ```
   spore gh config --set app_id=123456 --set installation_id=7890123 \
     --set private_key=~/.config/spore/<project>/github-app.pem \
     --set operator_login=<github login> \
     --set "commit_name=<your name>" --set commit_email=<email linked to that login> \
     --set pr_flow=draft
   spore gh exec -- gh api installation/repositories --jq .total_count
   ```

## Teammate onboarding

Per-teammate path: the org already has its app (today
`marketer-spore`). You share that app and bring your own private key.
Below, `<pem>` is `~/.config/spore/<project>/github-app.pem`.

1. Generate a key: app settings -> "Private keys" -> "Generate a
   private key". The browser downloads a `.pem`. This is a private
   key, NOT a client secret; client secrets are for OAuth user login,
   which spore does not use.
2. On the spore host, create the secrets dir (it may not exist):
   `install -d -m 0700 ~/.config/spore/<project>`.
3. Get the `.pem` onto the host. Never paste the key into an agent
   chat or run it through the `!` prefix.
   - Paste (works over plain SSH, recommended). Copy the key on your
     laptop: `pbcopy < key.pem` (Mac), `xclip -selection clipboard <
     key.pem` or `wl-copy < key.pem` (Linux). In a fresh tmux window on
     the host run `umask 077; cat > <pem>`, paste the full PEM (BEGIN
     through END line), press Enter, then Ctrl-D, and close the window.
     Clear the clipboard after.
   - scp from the laptop:
     `scp <file>.pem <user>@<host>:~/.config/spore/<project>/github-app.pem`.
     Replace every placeholder fully: zsh reads a leftover `<` or `>`
     as a redirection. If your ssh config auto-attaches tmux
     (`RemoteCommand` / `RequestTTY`), non-interactive ssh fails with
     "open terminal failed: not a terminal"; use the paste route.
4. Verify without printing the key:
   `chmod 600 <pem>; stat -c %a <pem>; openssl pkey -in <pem> -noout && echo OK`.
   Expect `600` and `OK`.
5. Find the ids.
   - App id: the app settings page shows an "App ID" (plain integer,
     e.g. `4071579`; this is `app_id`) and a "Client ID" (starts with
     `Iv`). spore does not use the Client ID; ignore it.
   - Installation id: an installation is the app added to an org or
     account, with its own separate id. Org Settings -> GitHub Apps ->
     Configure on the app opens
     `github.com/organizations/<org>/settings/installations/<id>`;
     the trailing number is `installation_id`. On that page, confirm
     the app is installed on the target repos.
6. Continue with [One-time setup](#one-time-setup) steps 4-5: https
   `origin`, `spore gh config --set ...` with your own
   `private_key`, `operator_login`, `commit_name`, `commit_email`, then
   `spore gh exec -- gh api installation/repositories --jq .total_count`.

## Config

`$XDG_STATE_HOME/spore/<project>/github.toml` (0600), host-side, never
in the consumer repo:

| key | meaning |
|---|---|
| `pr_flow` | `draft` turns the flow on; absent or `off` keeps the files-only loop exactly as before |
| `app_id`, `installation_id` | integers from step 2 |
| `private_key` | path to the PEM; a leading `~/` expands |
| `operator_login` | your GitHub login: PR assignee and the only approver |
| `commit_name`, `commit_email` | engineer commit identity (see below) |
| `api_url` | optional REST root for the token mint; default `https://api.github.com`. Untested against GitHub Enterprise Server: `GH_ENTERPRISE_TOKEN` is set for gh, but no GHES run has verified the flow |

`spore gh config` prints the file; `--set key=` deletes a key.

## Commit identity

With the flow on, engineer panes start with `GIT_AUTHOR_NAME`,
`GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_NAME`, `GIT_COMMITTER_EMAIL` from
`commit_name` / `commit_email`, overriding the repo's git config for
that pane only. The app is the pusher and PR author; you are the
commit author, so GitHub lets you approve. Panes spawned before the
flow was turned on keep their old identity until respawned.

## `spore gh`

- `spore gh push <branch> [--force-with-lease=<sha>]`,
  `spore gh pr <verb> [args...]` and `spore gh api <path> [args...]`
  are what agents use: push the local branch from the cwd repo
  (worktrees included) and run `gh pr` / `gh api` as the app. On a
  host with the secrets broker they go through it and its allowlist
  (`docs/secrets.md`); elsewhere they run like `spore gh exec`
  around `git push -u origin <branch>` or `gh`. Push never
  force-pushes without a lease. `--body-file <path>` (or `--input
  <path>` for `api`) is read by spore itself, from the cwd repo or
  `$TMPDIR` only, and fed to gh on stdin. Run each call as one
  command: a redirect, pipe or `cd ... &&` chain keeps it in the
  sandbox, where it cannot read the App key without the broker.
- `spore gh token` prints an installation token, for humans only:
  run from an agent pane, the token lands in the transcript. Agents
  use `spore gh pr|api|push`. Tokens are cached in
  `$XDG_STATE_HOME/spore/<project>/github-token.json` (0600) until 5
  minutes before expiry; changing `app_id` or `installation_id`
  invalidates the cache.
- `spore gh exec -- <cmd...>` runs a command as the app: `GH_TOKEN`
  and `GH_ENTERPRISE_TOKEN` are set, and a git credential helper
  (username `x-access-token`, password from `$GH_TOKEN`) is injected
  through `GIT_CONFIG_COUNT` / `GIT_CONFIG_KEY_n` /
  `GIT_CONFIG_VALUE_n`, after an empty `credential.helper` that
  clears your own helpers. Both `gh` and `git push` over https then
  act as the app.

## Limits (v1)

- Human PR comments are not read back into a running loop. The
  operator pulls them in on demand with `spore task reopen <slug>
  --from-pr`, which reads the feedback left since the loop last
  marked the PR ready or was reopened (see "Reopening a finished
  loop" in `docs/role-loop.md`).
- Signed commits: if a repo requires them, agent commits fail that
  rule. Not solved.
- A task already mid-loop when the flow is turned on gets its
  existing rounds posted in order on the next tick.
