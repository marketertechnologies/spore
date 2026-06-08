# Recipe: GitHub via `gh` CLI

Scoped GitHub access from a coordinator or worker pane: fetch and
pull, push non-default branches, create and manage pull requests,
and inspect GitHub Actions runs (including failing job logs).

Out of scope:

- Push to the default branch (`main` / `master`). Enforced by a
  server-side branch protection rule -- see "Default-branch
  protection" below. The recipe does not try to guard this
  client-side.
- Repo admin (settings, collaborators, webhooks), org-level
  actions (members, secrets, billing), releases, packages.

## Requirements

Operator-managed env var, sourced via `spore-with-secrets`:

- `GH_TOKEN` -- a GitHub fine-grained personal access token.

Placement is layered. `spore-with-secrets` sources
`~/.config/spore/secrets.env` first, then
`~/.config/spore/<project>/secrets.env` (per-project wins on
collisions). Typical setup:

- A default `GH_TOKEN` in the global file that reaches most
  repos a coordinator on this host needs to touch.
- A per-project override for projects that need a different
  identity or a different repo set (e.g. a token scoped to a
  separate org).

Mode 0600 on each file, 0700 on each containing dir. Never use
`GITHUB_TOKEN` -- see "Auth gotcha".

## Mint the token

Fine-grained PATs scope per-repo and per-permission, which is
what this recipe assumes. Classic PATs work but bundle too much
into `repo` + `workflow`; only fall back to a classic PAT if the
target org refuses fine-grained tokens.

1. Open `https://github.com/settings/personal-access-tokens` and
   pick "Generate new token".
2. **Resource owner**: pick the org (or your user account for
   personal repos). Org-owned tokens may require an org admin to
   approve the mint -- this is a one-time per-token cost.
3. **Repository access**: "Only select repositories", then pick
   the exact set the coordinator should reach. Avoid "All
   repositories" -- it defeats the per-repo scoping that
   motivates fine-grained PATs.
4. **Repository permissions** (minimum set for this recipe):
   - Contents: **Read and write** -- needed for `git fetch`,
     `git pull`, and `git push` on feature branches.
   - Pull requests: **Read and write** -- `gh pr create`,
     `gh pr edit`, `gh pr merge`.
   - Actions: **Read** -- `gh run list`, `gh run view --log*`.
   - Metadata: **Read** -- mandatory; auto-selected.
5. Set an expiry. 90 days is a reasonable default; the rotation
   reminder lands as a calendar event for the operator.
6. Copy the token (`github_pat_...`) and drop it into the chosen
   secrets file as `GH_TOKEN=github_pat_...`.

## Auth gotcha

Two env vars carry GitHub tokens, and they are NOT interchangeable:

- `GH_TOKEN` is `gh`-specific. `gh` consumes it first; git's
  credential helper does not. Use this one.
- `GITHUB_TOKEN` is the broad common-name silently consumed by
  git credential helpers, npm GitHub Packages, GitHub Actions
  runners, and a long tail of other tooling. Setting it in a
  coordinator's env leaks the token's reach into every tool that
  happens to look for it.

Always use `GH_TOKEN`. If a script you cannot edit insists on
`GITHUB_TOKEN`, set it inline for that single call
(`GITHUB_TOKEN=$GH_TOKEN ./script.sh`) instead of exporting it
globally.

## Wire `git` push through `gh`

`gh` ships a built-in git credential helper (`gh auth
git-credential`) that resolves the token at call time. Wire it
once per repo (or globally) so `git push` / `git fetch` over
HTTPS reach github.com under the token:

```
git config --global --replace-all credential.https://github.com.helper ""
git config --global --add         credential.https://github.com.helper \
  "!spore-with-secrets gh auth git-credential"
git config --global --replace-all credential.https://gist.github.com.helper ""
git config --global --add         credential.https://gist.github.com.helper \
  "!spore-with-secrets gh auth git-credential"
```

The empty first value clears any inherited helpers for the URL;
the second adds the wrapped invocation. Wrapping in
`spore-with-secrets` keeps `GH_TOKEN` off disk -- it resolves
fresh on every git op from the layered secrets files.

To wire a single repo only (does not touch `~/.gitconfig`), drop
`--global` and run the four lines inside the repo's working tree.
Per-repo wiring is the right choice on a shared host where the
operator's own shell should not pick up the helper.

Verify:

```
spore-with-secrets gh auth status
git ls-remote https://github.com/<org>/<repo>.git | head -3
```

`gh auth status` should report "Logged in to github.com account
<user> (GH_TOKEN)". `git ls-remote` should print refs without
prompting for credentials.

## Worked examples

All examples assume `spore-with-secrets` is on PATH (it is, via
the nix derivation) and `GH_TOKEN` resolves.

### Fetch and pull

```
git fetch origin
git pull --ff-only origin <branch>
```

These work without `spore-with-secrets` once the credential
helper above is wired -- git invokes the helper itself, and the
helper wraps the gh call.

### Push a feature branch

```
git push -u origin <feature-branch>
```

Same as above -- credential helper handles the auth. The
default-branch protection rule (below) is what stops a
mis-aimed push at `main`.

### Open a pull request

```
spore-with-secrets gh pr create \
  --base main \
  --head "$(git branch --show-current)" \
  --title "..." \
  --body  "..." \
  --draft
```

Drop `--draft` when the PR is ready for review. Use `--fill` to
auto-populate title and body from commits when the branch carries
one clean commit.

### List PRs / inspect one PR with checks

```
spore-with-secrets gh pr list --state open --limit 20
spore-with-secrets gh pr view <number-or-branch> \
  --json number,title,state,statusCheckRollup
```

`statusCheckRollup` is the compact view of CI state per check.
For a wall-of-text human view, drop `--json` and pipe through
`less`.

### Inspect Actions runs

```
spore-with-secrets gh run list --branch "$(git branch --show-current)" --limit 10
spore-with-secrets gh run view <run-id>
spore-with-secrets gh run view <run-id> --log-failed
```

`--log-failed` prints only the failing job's logs, which is what
you want 95% of the time. Use `--log` for the full archive when
the failure cause is upstream of the failing assertion.

For a live tail of an in-progress run:

```
spore-with-secrets gh run watch <run-id>
```

## Default-branch protection

`gh` and git both honor server-side branch protection. Configure
once per repo to prevent a token-holder from pushing to the
default branch:

1. `https://github.com/<org>/<repo>/settings/branches`.
2. Add a ruleset (or classic rule) on `main` (and `master` if
   present).
3. Enable "Restrict who can push to matching branches" and either
   leave the bypass list empty or restrict to a named admin
   account that is NOT the token's owner.
4. Optional: "Require a pull request before merging" with the
   review/approval bar your team prefers.

This is the only real enforcement. Client-side guards would only
catch honest mistakes; they cannot stop a confused coordinator
that types the wrong branch name.

## Hygiene

- Never echo `$GH_TOKEN` to a pane or log. Use length-and-prefix
  shape checks (`${#v}`, `${v:0:10}` -- a fine-grained PAT
  starts with `github_pat_`) for debugging.
- Rotate on expiry. The token has no refresh flow; mint a new
  one and overwrite the old value in the secrets file.
- Revoke at any time from
  `https://github.com/settings/personal-access-tokens`. Revocation
  is immediate.
- Org admins can also revoke org-scoped tokens from the org's
  "Personal access tokens" settings; expect this if a token's
  permission surface ever changes.
