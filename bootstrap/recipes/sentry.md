# Recipe: Sentry REST API

Read access to a Sentry tenant from a coordinator or worker pane:
find an issue, pull its metadata, fetch a representative event with
the full stack trace, and list releases. Writes (commenting,
resolving, assigning) are out of scope and require a token with
`event:write` / `project:write` scopes.

Out of scope entirely:

- DSN minting and event ingestion. DSNs are a separate, write-only,
  public ingestion path; not relevant for reading.
- Project / team / org admin (membership, alerts, integrations).
- Performance, replays, profiling. The bug-debug use case is
  errors-only; expand later if you need those surfaces.

## Requirements

Operator-managed env vars, sourced via `spore-with-secrets`:

- `SENTRY_AUTH_TOKEN` -- a personal auth token (read scope set,
  below).
- `SENTRY_BASE_URL` -- tenant URL, no trailing slash. On
  `sentry.io` this is `https://sentry.io`; on a self-hosted
  tenant, the host you point a browser at (e.g.
  `https://sentry.production.example.com`).
- `SENTRY_ORG` -- the org slug. Visible in any logged-in URL at
  `/organizations/<slug>/...`.

Placement is layered. `spore-with-secrets` sources
`~/.config/spore/secrets.env` first, then
`~/.config/spore/<project>/secrets.env` (per-project wins on
collisions). Typical setup:

- A default `SENTRY_AUTH_TOKEN` (and `SENTRY_BASE_URL` +
  `SENTRY_ORG`) in the global file so every coordinator on the
  host can read the tenant.
- A per-project override only if a project needs a tighter scope
  (e.g. a token restricted to a single project rather than the
  whole org).

Mode 0600 on each file, 0700 on each containing dir.

## Mint the token

Personal auth tokens are tied to a Sentry user and minted from
that user's account settings. For a long-lived machine identity
you can use an Internal Integration instead -- same API surface,
org-owned, requires org admin -- but personal tokens are easier
to rotate and revoke and are the default this recipe assumes.

1. Open `${SENTRY_BASE_URL}/settings/account/api/auth-tokens/`
   and pick "Create New Token".
2. Give the token a name that names the host and purpose
   (`spore-coordinator-<hostname>-readonly`); this is what shows
   up in the audit log.
3. **Scopes** -- minimum for the bug-debug loop (search issues,
   fetch event with stack trace, list releases):
   - `event:read` -- issue details, events, stack traces,
     breadcrumbs, `/organizations/<org>/issues/` search.
   - `project:read` -- per-project filters in search; releases.
   - `team:read` -- team names on issues.

   Optional, useful but not strictly required:
   - `org:read` -- enables `/organizations/<org>/projects/`
     enumeration and the org-level verify-auth call. Without it,
     the bug-debug loop still works (search infers the org from
     the URL path), but `spore-with-secrets ... projects/` will
     return HTTP 403.
   - `member:read` -- fills `assignedTo.email` on issues. Without
     it, that field comes back null but other fields still work.
4. Copy the token (current-format tokens start with `sntry`; the
   exact prefix is `sntrys_` for some flows and `sntryu_` for
   others -- both are personal auth tokens. Legacy tokens are
   64-char hex.) Drop into the chosen secrets file as
   `SENTRY_AUTH_TOKEN=...`.

Sentry personal auth tokens do not expire by default. There is no
rotation reminder; rotate manually on the cadence your team
prefers and revoke the old one from the same UI.

## Auth gotcha

Two kinds of Sentry credentials look superficially similar and are
NOT interchangeable:

- A **DSN** (`https://<public>@<tenant>/<project-id>`) is a
  *write-only*, *public* event ingestion URL. It cannot read
  issues, list releases, or hit `/api/0/...` at all. If you find
  yourself with a DSN, that is the wrong credential for this
  recipe.
- A **personal auth token** (`sntry...`) is what this recipe
  uses. It carries the read scopes minted above and authenticates
  against `/api/0/...` over HTTPS Bearer.

Use Bearer, not Basic. The header is
`Authorization: Bearer $SENTRY_AUTH_TOKEN`. Basic auth gets a
plain 401 with no scope context, which is easy to misread as a
wrong-token error when the real issue is the wrong auth scheme.

## Worked examples

All examples assume `spore-with-secrets` is on PATH (it is, via
the nix derivation) and `SENTRY_AUTH_TOKEN`, `SENTRY_BASE_URL`,
`SENTRY_ORG` all resolve.

### Verify auth (and inspect token scopes)

```
spore-with-secrets bash -c '
curl -sS -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" \
  "${SENTRY_BASE_URL%/}/api/0/" \
  | jq "{version, scopes: .auth.scopes, user: .user.email}"
'
```

The `/api/0/` root endpoint requires no scope and is the only
call that reflects the token's own scope list back -- prefer it
for verification over endpoints that 403 on missing scopes. HTTP
401 means the token is wrong, revoked, or sent under the wrong
auth scheme (see "Auth gotcha").

### List projects

Requires `org:read`. If your token omits that scope (see "Scopes"
above), skip this -- the bug-debug loop below does not need a
project list.

```
spore-with-secrets bash -c '
curl -sS -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" \
  "${SENTRY_BASE_URL%/}/api/0/organizations/${SENTRY_ORG}/projects/" \
  | jq ".[] | {slug, name, platform}"
'
```

The project slug is what you pass as `<project>` in the per-project
endpoints below.

### Find an issue by query

Sentry's issue search uses the same DSL as the UI's search box.
Useful predicates: `is:unresolved`, `project:<slug>`,
`environment:production`, `release:<version>`, `level:error`,
free-text against the issue title.

```
spore-with-secrets bash -c '
curl -sS -G -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" \
  "${SENTRY_BASE_URL%/}/api/0/organizations/${SENTRY_ORG}/issues/" \
  --data-urlencode "query=is:unresolved project:<project-slug>" \
  --data-urlencode "limit=25" \
  | jq ".[] | {id, shortId, title, level, count, lastSeen, permalink}"
'
```

Project both `id` (numeric) and `shortId` (e.g. `MARKETER-AB1`).
Pass `shortId` back to the operator -- it is the human-readable
handle the UI uses -- but keep the numeric `id` for the
`/issues/<id>/` calls below.

To look up an issue by `shortId` alone, append `shortIdLookup=1`
to the search:

```
spore-with-secrets bash -c '
curl -sS -G -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" \
  "${SENTRY_BASE_URL%/}/api/0/organizations/${SENTRY_ORG}/issues/" \
  --data-urlencode "query=$1" \
  --data-urlencode "shortIdLookup=1" \
  --data-urlencode "limit=1" \
  | jq ".[0] | {id, shortId, title}"
' _ MARKETER-AB1
```

### Fetch one issue's metadata

```
spore-with-secrets bash -c '
ISSUE_ID="$1"
curl -sS -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" \
  "${SENTRY_BASE_URL%/}/api/0/issues/${ISSUE_ID}/" \
  | jq "{shortId, title, status, level, culprit, firstSeen, lastSeen, count, userCount, assignedTo, tags: [.tags[] | {key, value: .topValues[0].name}]}"
' _ <numeric-issue-id>
```

**Gotcha:** the path segment must be the numeric `id`, not the
`shortId`. Passing a `shortId` returns HTTP 200 with every field
set to `null` -- a silent failure that is easy to misread as a
real "no such issue". Resolve `shortId` -> numeric `id` via the
`shortIdLookup=1` search above, then call this endpoint.

### Fetch the latest event with full stack trace

```
spore-with-secrets bash -c '
ISSUE_ID="$1"
curl -sS -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" \
  "${SENTRY_BASE_URL%/}/api/0/issues/${ISSUE_ID}/events/latest/" \
  | jq "{eventID, dateCreated, message, exception: .entries[] | select(.type==\"exception\") | .data.values[0] | {type, value, frames: [.stacktrace.frames[] | {filename, function, lineNo, context: .contextLine}]}}"
' _ <numeric-issue-id>
```

Same gotcha as `/issues/<id>/`: pass the numeric `id`, not the
`shortId`.

`events/latest/` returns the most recent event for the issue.
`events/oldest/` and `events/<event-id>/` also work. The
`entries` array carries breadcrumbs, request, user, and other
context types alongside the exception; project them out as
needed.

### List recent releases

```
spore-with-secrets bash -c '
curl -sS -G -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" \
  "${SENTRY_BASE_URL%/}/api/0/organizations/${SENTRY_ORG}/releases/" \
  --data-urlencode "per_page=20" \
  | jq ".[] | {version, dateCreated, dateReleased, projects: [.projects[].slug]}"
'
```

### Issues introduced in a release

```
spore-with-secrets bash -c '
VERSION="$1"
curl -sS -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" \
  "${SENTRY_BASE_URL%/}/api/0/organizations/${SENTRY_ORG}/releases/${VERSION}/" \
  | jq "{version, newGroups, firstEvent, lastEvent, commitCount, deployCount}"
' _ <release-version>
```

For the issue list itself, use the issue search with
`firstRelease:<version>`:

```
spore-with-secrets bash -c '
VERSION="$1"
curl -sS -G -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" \
  "${SENTRY_BASE_URL%/}/api/0/organizations/${SENTRY_ORG}/issues/" \
  --data-urlencode "query=firstRelease:${VERSION}" \
  | jq ".[] | {shortId, title, level, count}"
' _ <release-version>
```

## Scope reference

Minimum scopes for the bug-debug loop:

- `event:read` -- `/issues/<id>/`, `/issues/<id>/events/...`,
  `/organizations/<org>/issues/` search.
- `project:read` -- per-project filters in issue search;
  `/organizations/<org>/releases/` (verified empirically).
- `team:read` -- team names embedded in issue payloads.

Optional, only needed for endpoints outside the core loop:

- `org:read` -- `/organizations/<org>/` direct (the verify-auth
  alternative) and `/organizations/<org>/projects/` enumeration.
- `member:read` -- fills assignee fields. Without it those come
  back null but the call still succeeds.

Out of scope with the read-only set:

- Any write. Commenting, resolving, ignoring, assigning, deleting
  return `HTTP 403` without `event:write` (and `project:write`
  for assignment).
- Project / team / org admin endpoints. Membership changes,
  integration installs, alert rule edits all need `*:admin` and
  are intentionally not part of this recipe.

## Hygiene

- Never echo `$SENTRY_AUTH_TOKEN` to a pane or log. Use
  length-and-prefix shape checks (`${#v}`, `${v:0:5}` -- any
  current-format token starts with `sntry`) for debugging.
- Rotate on whatever cadence your team prefers. Personal auth
  tokens have no built-in expiry; the audit log entry on the
  Sentry side is the only trail.
- Revoke at any time from
  `${SENTRY_BASE_URL}/settings/account/api/auth-tokens/`.
  Revocation is immediate.
