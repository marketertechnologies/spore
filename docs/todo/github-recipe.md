**Status**: draft -- awaiting operator review of scope + open questions

# `github` recipe: scoped `gh` CLI access from a spore pane

Jira: MT3-9444 -- "Further evolve M360 spore to give it access to
GitHub".

## Goal

Let any coordinator or worker pane perform the routine GitHub
operations a spore workflow needs, with the minimum privilege set
the platform can express, and with a recipe that documents the
auth path, the gotchas, and worked examples (same shape as
`bootstrap/recipes/jira.md`).

In-scope operations (operator's spec):

1. Fetch and pull from remotes.
2. Push non-default branches to remotes.
3. Create and manage pull requests.
4. Debug GitHub Actions (list runs, inspect failing job logs).

Out of scope:

- Pushing to the default branch (`main` / `master`). Protected by a
  server-side branch protection rule, not by token scope -- the
  recipe will document the rule the operator should configure but
  will not try to enforce client-side.
- Repo admin (settings, collaborators, webhooks).
- Org-level actions (member management, secret rotation).
- Releases and packages.

## Deliverables

1. `bootstrap/recipes/github.md` -- the recipe body. Picked up
   automatically by the existing `//go:embed all:bootstrap/recipes`
   directive in `embed.go`; no Go code changes needed.
2. A short addition to the existing memory entry
   `project_gh_token_env_var.md` (or a new entry) covering the
   fine-grained-PAT permission set the recipe assumes. The current
   entry only states *where* to put `GH_TOKEN`; the new recipe
   adds *what scopes* it should carry.
3. No CLI changes, no flake changes. `gh` is already in
   `bootstrap/flake/configuration.nix` via the system package set
   (verified: `which gh` resolves on this host).

No migration is needed: existing per-project vaults that already
have a working `GH_TOKEN` keep working; vaults that don't get a
one-time operator-side token mint per the recipe.

## Open design questions

**Q1. Token type: classic PAT or fine-grained PAT?**

Recommendation: **fine-grained PAT**, default. Two reasons:

- Per-repo scoping. A classic PAT with `repo` scope grants access
  to every repo the minting account can see, including private
  ones unrelated to the spore-managed project. Fine-grained lets
  the operator pick the exact repo set.
- Per-permission scoping. The recipe needs Contents (read/write),
  Pull requests (read/write), Actions (read), Metadata (read). A
  classic PAT bundles all of these into the monolithic `repo` +
  `workflow` scopes.

Tradeoff: fine-grained PATs require org admin approval on
org-owned repos (one-time, per token mint). For the
`marketertechnologies` org and the newbuilds org the operator
already controls admin, so this is a one-time cost per token.

Alternative if Q1 lands the other way: document both, recommend
fine-grained, fall back to classic with `repo` + `workflow`.

**Q2. git push auth path.**

Two options:

- (a) `gh auth setup-git` -- writes a git credential helper that
  shells out to `gh auth token`. Clean, but persists state into
  the user's git config (`~/.gitconfig` or per-repo). Survives
  reboots.
- (b) Token-in-URL via a remote rewrite or per-call
  `https://x-access-token:$GH_TOKEN@github.com/...`. Stateless,
  but every git operation has to be wrapped in
  `spore-with-secrets` and the URL has to be rebuilt each call.

Recommendation: **(a)**, with the helper invocation itself wrapped
in `spore-with-secrets` so the token only resolves at call time.
The credential helper will be `!spore-with-secrets gh auth git-credential`
which keeps the token off disk entirely.

**Q3. Default-branch push protection: client-side check or
server-side branch protection rule?**

Recommendation: **server-side rule**, recipe documents it.
Client-side guards in the recipe (a wrapper that refuses `git push`
when the target ref matches the default branch) would only catch
honest mistakes -- they can't stop a determined-or-confused
coordinator. A repo-level branch protection rule with "Restrict
who can push to matching branches" set to exclude the token's
owner is the only real enforcement.

The recipe will include the exact GitHub UI path to set this and
note that the operator is responsible.

**Q4. Recipe scope: one combined `github` recipe, or split into
`github-gh` and `github-actions`?**

Recommendation: **one combined `github` recipe** for now. The
Actions debugging surface is small (three `gh run` commands) and
shares the same auth setup. Split later if the file grows past
~200 lines.

## Recipe outline (for the writeup)

Following the `jira.md` shape:

1. **Header** -- one paragraph stating scope and the explicit
   "no default-branch push, no admin" exclusions.
2. **Requirements** -- `GH_TOKEN` in per-project vault; pointer
   to the existing memory entry on env-var choice; the
   fine-grained PAT permission set.
3. **Mint the token** -- click path on GitHub, the four
   permissions, repo selection, expiry recommendation.
4. **Auth gotcha** -- `GH_TOKEN` vs `GITHUB_TOKEN` precedence
   inside `gh` and the leak surface that motivates picking the
   former (cross-link to the memory entry).
5. **Wire git push** -- the `git config --global credential.helper`
   one-liner that shells out to `spore-with-secrets gh auth
   git-credential`; verification via
   `git ls-remote https://github.com/<org>/<repo>.git`.
6. **Worked examples**:
   - Fetch + pull (`gh repo sync` for the common case;
     `git fetch && git pull --ff-only` for explicit control).
   - Push a feature branch.
   - Open a PR (`gh pr create`, with `--draft` and `--base`
     conventions).
   - List PRs / view one PR with checks (`gh pr list`,
     `gh pr view --json statusCheckRollup`).
   - List recent workflow runs on a branch
     (`gh run list --branch <b> --limit 10`).
   - View failing job logs (`gh run view <id> --log-failed`).
7. **Default-branch protection** -- the server-side rule the
   operator should configure, with the exact UI path.
8. **Hygiene** -- never echo `$GH_TOKEN`; rotation cadence;
   revocation URL.

## Validation plan

- `just check` green on the branch (the recipe is a markdown
  file; the gates that matter are `lints/em-dash`,
  `lints/comment-noise` on any Go change (none expected), and
  `internal/recipes` already has a test that walks every
  `bootstrap/recipes/*.md` and checks the H1 -- adding a new
  file will exercise that path).
- `spore recipes ls` shows the new entry.
- `spore recipes show github` prints the body.
- End-to-end smoke: run each worked example against a throwaway
  branch on this very repo (`MT3-9444/github-recipe`) and confirm
  the four scoped capabilities work. Default-branch push is
  *not* exercised; the recipe states that the protection rule is
  the enforcement.

## Operator owes (post-merge)

- Mint a fine-grained PAT per the recipe; drop into
  `~/.config/spore/<project>/secrets.env` for each project that
  needs `gh` access.
- Configure the default-branch protection rule on each repo the
  token can reach.
