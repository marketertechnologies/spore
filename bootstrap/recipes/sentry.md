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
3. **Scopes** (minimum set for this recipe):
   - `org:read` -- list projects, releases, environments.
   - `project:read` -- per-project filters, project details.
   - `event:read` -- issue details, events, stack traces,
     breadcrumbs.
   - `member:read` -- assignee names on issues.
   - `team:read` -- team names on issues.
4. Copy the token (starts with `sntrys_` for new tokens; legacy
   tokens are 64-char hex) and drop it into the chosen secrets
   file as `SENTRY_AUTH_TOKEN=...`.

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
- A **personal auth token** (`sntrys_...`) is what this recipe
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

### Verify auth

```
spore-with-secrets bash -c '
curl -sS -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" \
  "${SENTRY_BASE_URL%/}/api/0/organizations/${SENTRY_ORG}/" \
  | jq "{slug, name, status: .status.id}"
'
```

A successful call returns the org's slug, name, and status. HTTP
401 means the token is wrong, revoked, or sent under the wrong
auth scheme (see "Auth gotcha"). HTTP 403 means the token's scope
set does not cover the requested endpoint.

### List projects

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
  | jq ".[] | {shortId, title, level, count, lastSeen, permalink}"
'
```

`shortId` (e.g. `MARKETER-AB1`) is the human-readable handle the
UI uses; pass it back to the operator instead of the numeric `id`.

### Fetch one issue's metadata

```
spore-with-secrets bash -c '
ISSUE_ID="$1"
curl -sS -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" \
  "${SENTRY_BASE_URL%/}/api/0/issues/${ISSUE_ID}/" \
  | jq "{shortId, title, status, level, culprit, firstSeen, lastSeen, count, userCount, assignedTo, tags: [.tags[] | {key, value: .topValues[0].name}]}"
' _ <issue-id-or-short-id>
```

Both numeric `id` and `shortId` work as the path segment.

### Fetch the latest event with full stack trace

```
spore-with-secrets bash -c '
ISSUE_ID="$1"
curl -sS -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" \
  "${SENTRY_BASE_URL%/}/api/0/issues/${ISSUE_ID}/events/latest/" \
  | jq "{eventID, dateCreated, message, exception: .entries[] | select(.type==\"exception\") | .data.values[0] | {type, value, frames: [.stacktrace.frames[] | {filename, function, lineNo, context: .contextLine}]}}"
' _ <issue-id-or-short-id>
```

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

Minimum scopes for the calls above:

- `org:read` -- `/organizations/<org>/`, `/organizations/<org>/projects/`,
  `/organizations/<org>/releases/`.
- `project:read` -- per-project filters in issue search; project
  details endpoints.
- `event:read` -- `/issues/<id>/`, `/issues/<id>/events/...`,
  `/organizations/<org>/issues/`.
- `member:read`, `team:read` -- assignee and team names embedded
  in issue payloads. Without these, those fields come back null
  instead of failing the call.

Out of scope with the read-only set:

- Any write. Commenting, resolving, ignoring, assigning, deleting
  return `HTTP 403` without `event:write` (and `project:write`
  for assignment).
- Project / team / org admin endpoints. Membership changes,
  integration installs, alert rule edits all need `*:admin` and
  are intentionally not part of this recipe.

## Hygiene

- Never echo `$SENTRY_AUTH_TOKEN` to a pane or log. Use
  length-and-prefix shape checks (`${#v}`, `${v:0:7}` -- a
  current-format token starts with `sntrys_`) for debugging.
- Rotate on whatever cadence your team prefers. Personal auth
  tokens have no built-in expiry; the audit log entry on the
  Sentry side is the only trail.
- Revoke at any time from
  `${SENTRY_BASE_URL}/settings/account/api/auth-tokens/`.
  Revocation is immediate.
