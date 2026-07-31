# Recipe: Metabase SQL API

Read-only SQL access to a database that sits behind a Metabase
instance, from a coordinator or worker pane. The primary use case is
running ad-hoc analytics / verification queries against a production
read replica the agent has no direct psql credential for. Everything
else Metabase does (cards, dashboards, collections, users) is out of
scope -- this recipe is "POST SQL, get rows back".

## Requirements

Operator-managed env vars, sourced via `spore-with-secrets`:

- `METABASE_URL` -- instance base URL, e.g.
  `https://metabase.example.com`. Tolerate a trailing slash in
  scripts (`${METABASE_URL%/}`).
- `METABASE_API_KEY` -- a Metabase API key (see Provisioning).

Placement is the standard layered scheme: per-project
`~/.config/spore/<project>/secrets.env` is canonical for these --
the key is scoped to one org's data -- with an optional global
fallback under `~/.config/spore/secrets.env`.

## Provisioning (operator does this once)

Admin settings -> Authentication -> API Keys -> create a key
**bound to a group**. The group's data permissions define everything
the key can do, so scope the group to:

- native-query (SQL) access on the ONE database the agent should
  reach -- typically the read replica;
- no access to every other database;
- no curate/admin permissions.

Drop `METABASE_URL` + `METABASE_API_KEY` into the per-project
secrets.env. Nothing else is needed; API keys don't expire by
default.

## Auth

Every request carries the key in a header (no session/login dance):

    -H "x-api-key: ${METABASE_API_KEY}"

## Core operation: run a native SQL query

    POST ${METABASE_URL}/api/dataset
    Content-Type: application/json

    {"database": <db-id>, "type": "native", "native": {"query": "<SQL>"}}

Response shape (the fields that matter):

- `.status` -- `"completed"` on success; anything else is a failure.
- `.data.cols[].name` -- column names.
- `.data.rows` -- array of row arrays.
- `.error` -- human-readable message when the query failed (SQL
  error, permission denied, timeout).

Reference runner (jq builds the JSON so the SQL needs no escaping;
`--rawfile` takes the query from a file). `<DB_ID>` is a literal
placeholder: fill in the approved database id, which comes from the
operator (visible in the Metabase UI URL,
`/browse/databases/<id>-<slug>`) -- query-only keys cannot discover
it via the API (see gotchas).

    #!/usr/bin/env bash
    # Usage: mb-run.sh <sql-file> <out-json>
    set -euo pipefail
    sql_file="$1"; out="$2"
    : "${METABASE_URL:?}" "${METABASE_API_KEY:?}"
    jq -n --rawfile q "$sql_file" \
      '{database: <DB_ID>, type: "native", native: {query: $q}}' \
      | curl -sS --fail-with-body -X POST "${METABASE_URL%/}/api/dataset" \
          -H "x-api-key: ${METABASE_API_KEY}" \
          -H 'Content-Type: application/json' \
          -d @- > "$out"
    jq -r '.status as $s | if $s == "completed" then
      ( [.data.cols[].name] | @tsv ), ( .data.rows[] | @tsv )
    else
      "QUERY FAILED: \(.error // .status)"
    end' "$out"

Wrap invocations with
`spore-with-secrets bash -c '.../mb-run.sh q.sql out.json'`.

## Limits and gotchas

- **Ad-hoc row cap: 2,000 rows** on `/api/dataset`. Results are
  silently truncated at the cap -- check `.row_count` / design
  queries with aggregates + LIMIT. For bigger pulls use the export
  endpoint: `POST /api/dataset/csv` with a form-encoded body
  (`--data-urlencode "query=<the same JSON>"`), which raises the
  cap to ~1M rows. Verify the exact export shape on the instance's
  Metabase version before relying on it.
- **`GET /api/database` returns an empty list** for a key whose
  group only has query permissions (no curate). This is NORMAL, not
  a broken key -- it means the database **id cannot be discovered
  via the API**; get it from the operator (visible in the Metabase
  UI URL: `/browse/databases/<id>-<slug>`). Sanity-check the id
  with `SELECT 1`: a permitted db completes, a non-permitted one
  returns 403.
- One statement per request; no transactions, no temp tables. The
  key can only do what its group can -- a properly scoped key makes
  writes impossible at the permission layer, but treat the surface
  as read-only regardless.
- Timeouts on long queries surface as a non-`completed` status with
  `.error`; there is no server-side query kill exposed to the key,
  so keep queries replica-friendly (the replica serves other
  consumers).

## Policy (spore convention, not Metabase)

Per-project rules may constrain use beyond what the key allows. The
recommended per-project convention:

- read-only, ONE approved database id, never target another id even
  if the key happens to permit it;
- ALWAYS show the operator the SQL and get approval before every
  `POST /api/dataset` (blanket approval for a named, pre-composed
  run counts; follow-up drill-downs need a fresh ask unless they
  were part of the approved plan);
- never echo the key; results are production data -- quote only
  what the task needs.

Record the approved database id and any project-specific rules in
the consuming project's state.md / memory, not in this recipe.
