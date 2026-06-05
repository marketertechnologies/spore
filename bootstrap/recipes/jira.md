# Recipe: Atlassian Jira REST API

Read access to a Jira Cloud tenant from a coordinator or worker pane.
Writes (commenting, transitions, assignment) are out of scope and
require a token with `write:*` scopes plus the equivalent API.

## Requirements

Operator-managed env vars, sourced via `spore-with-secrets`:

- `JIRA_BASE_URL` -- tenant URL, e.g. `https://example.atlassian.net`.
- `JIRA_EMAIL` -- Atlassian account email that owns the API token.
- `JIRA_API_TOKEN` -- token from
  `https://id.atlassian.com/manage-profile/security/api-tokens`.

Drop these in `~/.config/spore/secrets.env` (global, every coordinator
on the host sees them) or `~/.config/spore/<project>/secrets.env`
(per-project). Mode 0600 on the file, 0700 on the dir.

## Auth gotcha

The Atlassian token UI now attaches OAuth-style scopes to "classic"
API tokens. A scoped token rejects Basic auth against the tenant URL
with `HTTP 401 "Client must be authenticated to access this resource"`,
which is the same shape as a wrong-password failure and is easy to
misread as a propagation delay.

The correct combo for any scoped token:

- HTTP Basic with `--user "$JIRA_EMAIL:$JIRA_API_TOKEN"`.
- Base URL `https://api.atlassian.com/ex/jira/<cloudId>` instead of
  the tenant URL.

The tenant URL only works for the (now-deprecated) unscoped classic
tokens.

## Discover the cloudId

The tenant's cloudId is public and discoverable without auth:

```
curl -sS "${JIRA_BASE_URL%/}/_edge/tenant_info"
# {"cloudId":"..."}
```

Don't hardcode it. Caching it in `JIRA_CLOUD_ID=` inside `secrets.env`
is fine; rediscover after a tenant move.

## Worked examples

All examples assume `spore-with-secrets` is on PATH (it is, via the
nix derivation) and that `JIRA_*` vars resolve.

### Verify auth

```
spore-with-secrets bash -c '
CLOUD_ID=$(curl -sS "${JIRA_BASE_URL%/}/_edge/tenant_info" | jq -r .cloudId)
curl -sS -u "$JIRA_EMAIL:$JIRA_API_TOKEN" \
  -H "Accept: application/json" \
  "https://api.atlassian.com/ex/jira/${CLOUD_ID}/rest/api/3/myself"
'
```

A successful call returns the authenticated user's `accountId`,
`emailAddress`, and `displayName`. HTTP 401 means the token is wrong
or unrecognised; HTTP 403 means the scope set does not cover the
requested endpoint.

### List projects

```
spore-with-secrets bash -c '
CLOUD_ID=$(curl -sS "${JIRA_BASE_URL%/}/_edge/tenant_info" | jq -r .cloudId)
curl -sS -u "$JIRA_EMAIL:$JIRA_API_TOKEN" \
  -H "Accept: application/json" \
  "https://api.atlassian.com/ex/jira/${CLOUD_ID}/rest/api/3/project/search?maxResults=100"
'
```

### Search issues with JQL

```
spore-with-secrets bash -c '
CLOUD_ID=$(curl -sS "${JIRA_BASE_URL%/}/_edge/tenant_info" | jq -r .cloudId)
curl -sS -u "$JIRA_EMAIL:$JIRA_API_TOKEN" \
  -H "Accept: application/json" \
  -G "https://api.atlassian.com/ex/jira/${CLOUD_ID}/rest/api/3/search/jql" \
  --data-urlencode "jql=project = MT3 AND statusCategory != Done ORDER BY updated DESC" \
  --data-urlencode "fields=summary,status,assignee,updated" \
  --data-urlencode "maxResults=50"
'
```

The newer `/rest/api/3/search/jql` endpoint replaces the deprecated
`/rest/api/3/search`. Pagination is via `nextPageToken` (opaque
cursor), not `startAt`.

### Fetch one issue with rendered body

```
spore-with-secrets bash -c '
CLOUD_ID=$(curl -sS "${JIRA_BASE_URL%/}/_edge/tenant_info" | jq -r .cloudId)
curl -sS -u "$JIRA_EMAIL:$JIRA_API_TOKEN" \
  -H "Accept: application/json" \
  -G "https://api.atlassian.com/ex/jira/${CLOUD_ID}/rest/api/3/issue/MT3-9442" \
  --data-urlencode "expand=renderedFields,names"
'
```

`renderedFields.description` is HTML; `fields.description` is ADF
(Atlassian Document Format, JSON). For body comprehension prefer
the rendered HTML and strip tags.

## Scope reference

Minimum scopes for the calls above:

- `read:me` -- `/rest/api/3/myself`
- `read:jira-work` -- projects, issues, JQL search, comments
- `read:jira-user` -- user lookups
- `read:account` -- account-level fields on `myself`

Out of scope with the read-only set:

- `/rest/agile/1.0/*` (boards, sprints, backlogs). Needs
  `read:board-scope:jira-software` and `read:sprint:jira-software`.
  Note: most board contents are reachable via JQL on the project
  instead; only the board's column/sprint *structure* requires
  the agile endpoints.
- Any write. Comments, transitions, field edits, assignment all
  return `HTTP 401 "scope does not match"` without a `write:*`
  scope.

## Hygiene

- Never echo `$JIRA_API_TOKEN` to a pane or log. Use length-and-prefix
  shape checks (`${#v}`, `${v:0:4}`) for debugging.
- Token regeneration: Atlassian's docs note tokens may take a few
  minutes to become active after creation.
- The token can be revoked at any time from
  `id.atlassian.com/manage-profile/security/api-tokens`. Revocation
  is immediate.
