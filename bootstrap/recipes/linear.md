# Recipe: Linear GraphQL API

Read and write access to a Linear workspace from a coordinator or
worker pane via a personal API key. In-scope operations: verify
auth, list teams and workflow states, filter issues, fetch one
issue with comments, create issues, comment on issues, transition
state, and reassign.

Out of scope:

- Admin surface: workspace settings, integrations, billing, member
  provisioning, SCIM. A personal API key inherits the minting
  user's permissions, so an admin's key technically reaches these,
  but the recipe does not encode admin flows -- run those from
  the Linear UI.
- OAuth2 applications. Linear supports OAuth2 (with `Bearer`
  tokens and actor-mode acting as a user), useful when a server
  needs to act on behalf of many users. For a single-operator
  coordinator pane the personal API key is the smaller surface
  and this recipe stays there.
- Webhooks, real-time subscriptions, the SDK. The recipe uses
  plain `curl` against the GraphQL endpoint so it works in any
  pane without per-language tooling.

## Requirements

Operator-managed env var, sourced via `spore-with-secrets`:

- `LINEAR_API_KEY` -- a personal API key (`lin_api_...`) minted
  from `https://linear.app/settings/account/security`.

Linear's own SDK (`@linear/sdk`) consumes `LINEAR_API_KEY` by
default. The recipe matches that convention so any later script
that picks up the SDK reads from the same env var.

Placement is layered. `spore-with-secrets` sources
`~/.config/spore/secrets.env` first, then
`~/.config/spore/<project>/secrets.env` (per-project wins on
collisions). Typical setup:

- A default `LINEAR_API_KEY` in the global file when a single
  workspace serves every coordinator on this host.
- A per-project override when a project targets a different
  workspace (each key is bound to exactly one).

Mode 0600 on each file, 0700 on each containing dir.

No `LINEAR_WORKSPACE` env var. The workspace is whatever the key
resolves to; the verify-auth call below reflects it back via
`organization.urlKey`.

## Mint the key

1. Open `https://linear.app/settings/account/security` while
   signed in as the account whose permissions the coordinator
   should inherit.
2. "Personal API keys" -> "New API key". Pick a label that
   names the host plus purpose (`spore-<host>-coordinator`).
3. Copy the token (`lin_api_...`) and drop it into the chosen
   secrets file as `LINEAR_API_KEY=lin_api_...`.

There is no scope picker. The key carries the full permission
set of the minting account, including any workspace it is a
member of and any private team it can see. Treat the key as
equivalent to that account's password and rotate on the same
cadence.

## Auth gotcha

Two token shapes reach the Linear API and the header they go in
is NOT the same:

- Personal API keys (`lin_api_...`) go in the `Authorization`
  header **without** a `Bearer` prefix:
  `Authorization: lin_api_xxx`.
- OAuth2 access tokens use `Authorization: Bearer ...` in the
  conventional shape.

The two are easy to swap. A personal key with the `Bearer`
prefix returns `HTTP 400 {"errors":[{"message":"Authentication
required"}]}`, which reads like a malformed-request error but is
really an auth misroute. Drop the prefix and the same key works.

## Endpoint

There is one endpoint: `https://api.linear.app/graphql`. All
operations -- reads and writes -- are POSTs with a JSON body of
shape `{"query": "...", "variables": {...}}`. Use
`Content-Type: application/json`. The recipe encodes the body via
`jq -n` so quoting in the GraphQL string does not collide with
the shell.

## Worked examples

All examples assume `spore-with-secrets` is on PATH (it is, via
the nix derivation) and `LINEAR_API_KEY` resolves. A small
helper makes the rest readable:

```
linear_query() {
  local q="$1"; shift
  jq -n --arg q "$q" "$@" '{query: $q} + ($ENV.VARS // {} | fromjson? // {})'
}
```

The examples below inline `jq -n` directly to keep each block
self-contained.

### Verify auth

```
spore-with-secrets bash -c '
curl -sS -X POST "https://api.linear.app/graphql" \
  -H "Authorization: $LINEAR_API_KEY" \
  -H "Content-Type: application/json" \
  --data "$(jq -n "{query: \"query { viewer { id name email } organization { id name urlKey } }\"}")"
'
```

A successful call returns the authenticated user's `id`, `name`,
`email`, and the workspace's `urlKey` (the workspace slug in
`https://linear.app/<urlKey>/...`). HTTP 400 with
`"Authentication required"` means the key is wrong, expired, or
was sent with a stray `Bearer` prefix.

### List teams

```
spore-with-secrets bash -c '
curl -sS -X POST "https://api.linear.app/graphql" \
  -H "Authorization: $LINEAR_API_KEY" \
  -H "Content-Type: application/json" \
  --data "$(jq -n "{query: \"query { teams(first: 100) { nodes { id key name } } }\"}")"
'
```

`key` is the short prefix that appears in issue identifiers
(e.g. `ENG-123` lives in the team whose `key` is `ENG`). The
`id` is the UUID used in mutations.

### Filter issues

```
spore-with-secrets bash -c '
QUERY='\''
  query($key: String!) {
    issues(
      filter: {
        team: { key: { eq: $key } }
        state: { type: { nin: ["completed", "canceled"] } }
      }
      first: 50
      orderBy: updatedAt
    ) {
      nodes {
        identifier
        title
        state { name type }
        assignee { name }
        updatedAt
      }
    }
  }
'\''
curl -sS -X POST "https://api.linear.app/graphql" \
  -H "Authorization: $LINEAR_API_KEY" \
  -H "Content-Type: application/json" \
  --data "$(jq -n --arg q "$QUERY" --arg key "ENG" \
    "{query: \$q, variables: {key: \$key}}")"
'
```

Linear's `WorkflowStateType` enum has five values: `triage`,
`backlog`, `unstarted`, `started`, `completed`, `canceled`.
Filtering on `type` is portable across teams; filtering on
`state.name` is not (teams can rename states).

Pagination is cursor-based. The response includes
`issues.pageInfo.endCursor` and `hasNextPage`; pass `after:
$endCursor` on the next call. Use `first: 250` (the max) to
minimise round-trips for bulk reads.

### Fetch one issue with body and comments

```
spore-with-secrets bash -c '
QUERY='\''
  query($id: String!) {
    issue(id: $id) {
      identifier
      title
      description
      state { name }
      assignee { name }
      url
      comments(first: 50) {
        nodes { id body user { name } createdAt }
      }
    }
  }
'\''
curl -sS -X POST "https://api.linear.app/graphql" \
  -H "Authorization: $LINEAR_API_KEY" \
  -H "Content-Type: application/json" \
  --data "$(jq -n --arg q "$QUERY" --arg id "ENG-123" \
    "{query: \$q, variables: {id: \$id}}")"
'
```

The `issue(id:)` field accepts either the UUID or the
human-readable identifier (`ENG-123`); the API resolves both.
`description` is markdown; the Linear UI renders it. There is
no separate "rendered HTML" view as with Jira's ADF.

### List workflow states for a team

You need state IDs to transition issues. They are stable per
team and worth caching per coordinator session.

```
spore-with-secrets bash -c '
QUERY='\''
  query($key: String!) {
    team(id: $key) {
      states(first: 50) { nodes { id name type position } }
    }
  }
'\''
curl -sS -X POST "https://api.linear.app/graphql" \
  -H "Authorization: $LINEAR_API_KEY" \
  -H "Content-Type: application/json" \
  --data "$(jq -n --arg q "$QUERY" --arg key "ENG" \
    "{query: \$q, variables: {key: \$key}}")"
'
```

`team(id:)` also accepts either the UUID or the team `key`.
`position` is the column order in the board view; sort by it
when rendering states to the operator.

### Create an issue

```
spore-with-secrets bash -c '
MUTATION='\''
  mutation($input: IssueCreateInput!) {
    issueCreate(input: $input) {
      success
      issue { identifier url }
    }
  }
'\''
curl -sS -X POST "https://api.linear.app/graphql" \
  -H "Authorization: $LINEAR_API_KEY" \
  -H "Content-Type: application/json" \
  --data "$(jq -n --arg q "$MUTATION" \
    --arg teamId "ENG" \
    --arg title "Investigate flaky test in auth suite" \
    --arg desc  "Repro: just test ./auth/... -count=10" \
    "{query: \$q, variables: {input: {teamId: \$teamId, title: \$title, description: \$desc}}}")"
'
```

`teamId` accepts either the UUID or the team `key`. `success`
is `true` on accept; the created issue's `identifier` is the
human-readable ID to surface to the operator.

### Add a comment

```
spore-with-secrets bash -c '
MUTATION='\''
  mutation($input: CommentCreateInput!) {
    commentCreate(input: $input) {
      success
      comment { id url }
    }
  }
'\''
curl -sS -X POST "https://api.linear.app/graphql" \
  -H "Authorization: $LINEAR_API_KEY" \
  -H "Content-Type: application/json" \
  --data "$(jq -n --arg q "$MUTATION" \
    --arg issueId "ENG-123" \
    --arg body "Reproduced locally; bisecting now." \
    "{query: \$q, variables: {input: {issueId: \$issueId, body: \$body}}}")"
'
```

`issueId` accepts either the UUID or the human identifier.
`body` is markdown.

### Transition state

```
spore-with-secrets bash -c '
MUTATION='\''
  mutation($id: String!, $input: IssueUpdateInput!) {
    issueUpdate(id: $id, input: $input) {
      success
      issue { identifier state { name } }
    }
  }
'\''
curl -sS -X POST "https://api.linear.app/graphql" \
  -H "Authorization: $LINEAR_API_KEY" \
  -H "Content-Type: application/json" \
  --data "$(jq -n --arg q "$MUTATION" \
    --arg id "ENG-123" \
    --arg stateId "<workflow-state-uuid>" \
    "{query: \$q, variables: {id: \$id, input: {stateId: \$stateId}}}")"
'
```

`stateId` is a UUID; resolve it via the "List workflow states"
query above. The same mutation handles other field edits:
`assigneeId`, `priority` (0-4), `labelIds` (replaces), and so
on -- all under the single `IssueUpdateInput` type.

## Error shape

GraphQL errors come back with HTTP 200 and an `errors` array on
the JSON body. A read of `.errors[0].extensions.code` classifies
the failure:

- `AUTHENTICATION_ERROR` -- key missing, wrong, or sent with
  `Bearer`. Same shape as a 401 elsewhere.
- `FORBIDDEN` -- key is valid but the minting account does not
  have access to the requested resource (private team, archived
  project).
- `INVALID_INPUT` -- malformed mutation input; the message
  names the offending field.
- `RATE_LIMITED` -- request budget exhausted. Linear's
  documented limit is 1500 complexity points per hour per token;
  most reads are 1-10 points, mutations 10-30.

A non-200 HTTP status is rare and usually means the request
never reached Linear (network, DNS, proxy).

## Hygiene

- Never echo `$LINEAR_API_KEY` to a pane or log. Use
  length-and-prefix shape checks (`${#v}`, `${v:0:8}` -- a
  personal key starts with `lin_api_`) for debugging.
- Personal keys do not expire. Rotate on operator schedule, not
  on a timeout. Mint a new one and overwrite the old value in
  the secrets file; old key keeps working until revoked from the
  settings page.
- Revocation is immediate from
  `https://linear.app/settings/account/security`.
- A personal key carries the minting account's full permissions.
  Use a service-account-style Linear user (named after the
  coordinator, no individual ownership) when the workspace
  permits one; that way revoking the human's access does not
  also revoke the coordinator's.
