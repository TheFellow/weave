# Weave query session protocol v1

Status: local read contract. Additive response fields may appear; incompatible
request or semantic changes require another protocol name.

This is the language-neutral persistent query contract between an agent host
and the Weave core. The client starts `weave session` in the repository it wants
to query, writes one UTF-8 JSON request per line to stdin, reads one UTF-8 JSON
response per line from stdout, and closes stdin when finished. Stdout is
protocol-only. The client owns the foreground child process and its lifetime.

The process opens no database until its first valid query. It then exclusively
owns that worktree's bstore database and serializes requests. Other Weave
processes cannot open the same database until the session exits. Background Git
observation detects a changed source state; the next request closes the handle,
uses the ordinary authoritative refresh pipeline, and reopens the published
generation before querying.

## Request

Every request requires:

| Field | Meaning |
| --- | --- |
| `protocol` | Exactly `weave.query-session/v1`. |
| `id` | A nonempty client request ID of at most 128 bytes. |
| `command` | One supported local read command below. |
| `arguments` | The command's exact positional query strings. |

Optional bounds mirror the CLI: `limit`, `max_depth`, `max_edges`, `kinds`,
`direction`, `context_lines`, `max_source_bytes`, `relationship_limit`,
`impact_files`, `impact_packages`, and `diff_revision`. Zero or omission selects
the documented CLI-shaped default. Unknown fields, unknown edge kinds, invalid
bounds, catalog scope, and unsupported commands are rejected without ending the
session.

Supported commands are:

```text
symbols             context              explore
definition          references           callers
callees             dependencies         path
impact              graph                workspace find
workspace outline   workspace links      workspace backlinks
```

All but `path` take one argument. `path` takes two. `impact` takes either one
symbol argument or one or more of its file/package/diff fields. Maintenance,
indexing, mutation, catalog, adapter, full export, verification, and GC are
intentionally not resident agent queries.

See [`requests.ndjson`](requests.ndjson) for valid requests implemented without
any Go dependency.

## Response

Every response repeats `protocol` and the request `id`. A successful frame has
`result`, the normal `weave.query/v1` application envelope. A failed frame has:

```json
{"protocol":"weave.query-session/v1","id":"q1","error":{"code":"invalid_request","message":"..."}}
```

`invalid_request` covers framing, protocol, command, arity, and bound errors.
`query_failed` means a valid request could not be answered by freshness,
storage, resolution, source validation, or query execution. Per-request errors
do not terminate the stream. Error messages are valid UTF-8 and bounded to 8
KiB. A request line is bounded to 1 MiB; exceeding the framing limit terminates
the process because the next record boundary cannot be recovered safely.

Responses are bounded by the request/default result ceilings. `context` and
`explore` include current source excerpts and provenance; graph identities and
facts remain independent of the language provider that produced them.

## Lifecycle and freshness

The first request is deliberately cold. Warm requests reuse storage pages and
the in-memory intern dictionary without repeating process startup, schema
preflight, database open, or synchronous Git observation. Exact observations
run at the foreground watch interval. There can therefore be a short bounded
window before a graph-only request notices a source change. Source-rich queries
re-read and hash-check the file on every response and will report it changed
rather than pair stale ranges with new text.

EOF is graceful shutdown. Interrupt or termination cancellation also closes the
database. Clients must stop the session before invoking one-shot maintenance or
mutation commands for the same worktree.
