**Status**: spec draft; recipe body not yet written. Open questions
below need operator answers before the recipe lands.

# `sentry` recipe: scoped Sentry API access from a spore pane

Jira: MT3-9443 -- "Further evolve M360 spore to give it access to
Sentry and Notion". This spec covers the Sentry half; Notion is a
separate cycle.

## Goal

Let any coordinator or worker pane pull the data needed to debug a
production bug surfaced by Sentry, with the minimum privilege set
the platform can express, and with a recipe that documents the auth
path, the gotchas, and worked examples (same shape as
`bootstrap/recipes/jira.md` and `bootstrap/recipes/github.md`).

In-scope operations (v1):

1. Find an issue by title, message, or short-id (`PROJECT-123`).
2. Pull an issue's metadata: status, assignee, first/last seen,
   event count, tags, level, culprit.
3. Pull a representative event from an issue: stack trace,
   breadcrumbs, request context, user context, runtime tags.
4. Filter issues by project, environment, release, time window,
   query string (Sentry's search DSL).
5. List recent releases and the issues each release introduced.

Out of scope for v1:

- Issue writes: resolve, ignore, assign, comment. Possible v2;
  read-only first matches the `jira` recipe's posture and keeps
  the token's blast radius small.
- Project / team / org admin (membership, integrations, alerts).
- DSN minting and event ingestion (write-only, unrelated path).
- Performance / replay / profiling product surfaces. The bug-debug
  use case is errors-only; expand later if the operator needs it.

## Deliverables

1. `bootstrap/recipes/sentry.md` -- the recipe body. Picked up
   automatically by the existing `//go:embed all:bootstrap/recipes`
   directive in `embed.go`; no Go code changes needed.
2. A small memory entry on Sentry token env-var conventions
   (`project_sentry_token_env_var.md`) mirroring
   `project_gh_token_env_var.md`: which env var, which scopes,
   which vault (global vs per-project).
3. No CLI changes, no flake changes. `curl` + `jq` are already on
   PATH everywhere this recipe will run.

No migration needed: existing vaults that already carry a Sentry
token keep working; vaults that don't get a one-time operator-side
mint per the recipe.

## Open design questions

**Q1. Scope: read-only (v1 above) or include writes (resolve,
comment, assign)?**

Recommendation: **read-only** for v1. Sentry's auth-token scope
list separates `event:read` / `project:read` / `org:read` (read
surface) from `event:write` / `project:write` / `org:write` (write
surface). Minting a read-only token caps the blast radius if the
secrets file is ever compromised, and the common debug flow
("what's failing in prod right now?") never needs writes. Add a
write-scoped recipe later if the operator wants the coordinator to
close issues when a fix lands.

**Q2. Token type: personal auth token or internal-integration
token?**

Two options Sentry supports:

- (a) Personal auth token, minted at
  `https://sentry.io/settings/account/api/auth-tokens/`. Tied to a
  Sentry user; revoked if that user leaves the org. Per-token
  scope list. Easy to mint, easy to rotate.
- (b) Internal integration token, minted at
  `https://sentry.io/settings/<org>/developer-settings/`. Tied to
  the org, not a user; survives user offboarding. Broader scope
  granularity, but requires org-admin to create.

Recommendation: **(a) personal auth token**, default. Matches the
github recipe's PAT posture; coordinator-owned identities are
shorter-lived than org-integration tokens, which we don't want to
manage centrally yet. Document (b) as a fallback for the
machine-account case (no human user behind the token).

**Q3. Sentry deployment: cloud (`sentry.io`) or self-hosted?**

Need to know the base URL. The recipe will template
`SENTRY_BASE_URL` either way, but the auth-token URL and the
org-settings URL differ. If `sentry.io`, recipe links resolve to
the SaaS UI; if self-hosted, the recipe says "your tenant's
equivalent". **Asking the operator.**

**Q4. Org and project slugs.**

Sentry's API is org-scoped (`/api/0/organizations/<org-slug>/...`)
with project filters layered on top. The recipe needs to name the
org slug in worked examples (e.g. `marketertechnologies` or
`m360`?) so the operator can copy-paste. **Asking the operator.**

**Q5. Token placement: global vault, per-project vault, or both?**

Recommendation: **layered, same as `GH_TOKEN`**. Default
`SENTRY_AUTH_TOKEN` in the global secrets file so every
coordinator on the host can pull issues; per-project override
where a project needs a different identity (e.g. tighter scope on
a sensitive repo). `spore-with-secrets` handles the layering
automatically; per-project wins on key collisions.

**Q6. Recipe scope: one combined `sentry` recipe, or split into
`sentry-issues` and `sentry-releases`?**

Recommendation: **one combined `sentry` recipe** for now. The
releases surface is small (one or two `curl` calls) and shares
the same auth setup. Split later if the file grows past ~200
lines.

## Recipe outline (for the writeup)

Following the `jira.md` / `github.md` shape:

1. **Header** -- one paragraph stating read-only scope and the
   explicit "no writes, no admin, no DSN minting" exclusions.
2. **Requirements** -- `SENTRY_AUTH_TOKEN`, `SENTRY_BASE_URL` (if
   self-hosted; default `https://sentry.io` on cloud),
   `SENTRY_ORG` in the vault. Pointer to the memory entry on
   env-var choice.
3. **Mint the token** -- click path on Sentry, the read-only
   scope set (`event:read`, `project:read`, `org:read`,
   `member:read`, `team:read`), expiry recommendation.
4. **Auth gotcha** -- DSN vs auth token confusion (DSN is
   write-only, public, ingestion-only -- not what you want here).
   Token prefix shapes (`sntrys_...` for new tokens vs legacy
   hex). Bearer header, not Basic.
5. **Worked examples**:
   - Find an issue by short-id or query
     (`/api/0/organizations/<org>/issues/?query=...`).
   - Fetch one issue's metadata
     (`/api/0/issues/<id>/`).
   - Fetch a representative event with full stack trace
     (`/api/0/issues/<id>/events/latest/`).
   - List recent releases for a project
     (`/api/0/projects/<org>/<project>/releases/`).
   - Issues introduced in a release
     (`/api/0/organizations/<org>/releases/<version>/resolved/`
     and `/.../new-issues/`).
6. **Hygiene** -- never echo `$SENTRY_AUTH_TOKEN`; rotation
   cadence; revocation URL.

## Validation plan

- `just check` green on the branch (markdown-only change; the
  `internal/recipes` test that walks `bootstrap/recipes/*.md` and
  checks the H1 will exercise the new file).
- `spore recipes ls` shows the new entry.
- `spore recipes show sentry` prints the body.
- End-to-end smoke: run each worked example from this pane
  against the operator's Sentry tenant once the token lands.
  Quote one real issue's short-id back to the operator as proof.

## Operator owes (post-merge)

- Mint a read-only personal auth token per the recipe; drop into
  `~/.config/spore/secrets.env` as `SENTRY_AUTH_TOKEN=...` plus
  `SENTRY_BASE_URL=...` (only if self-hosted) and
  `SENTRY_ORG=<slug>`.
- Decide whether any project needs a per-project override (e.g.
  scoped to a single project rather than the whole org).
