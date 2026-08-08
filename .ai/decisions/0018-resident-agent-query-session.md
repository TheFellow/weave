# ADR 0018: Resident agent query session

- Status: Accepted
- Date: 2026-08-08
- Extends: [ADR 0014](0014-optional-watch-mode-warming.md)
- Research: [resident query service prior art](../prior-art/resident-query-service/README.md)

## Context

Weave's one-shot CLI is correct and convenient, but every query starts a
process, performs exact freshness work, opens bstore, validates the schema, and
hydrates hot dictionaries. This is unlike an IDE, whose project/index services
remain alive while many navigation requests reuse them. bstore also permits
only one owning process to open the read/write database, so pretending that
many independent query processes can share it would be incorrect.

## Decision

`weave session` is an explicit foreground owner of one local worktree database.
It accepts newline-delimited `weave.query-session/v1` JSON requests on stdin and
writes one result or typed error frame per request to stdout. Requests have
bounded query, traversal, relationship, and source budgets and are restricted
to local read-oriented agent queries. Maintenance, mutation, catalog, adapter,
index, and full-export operations remain one-shot commands.

The first query runs the existing authoritative freshness path and opens the
database. Queries are serialized and reuse that handle. A background loop uses
the same exact Git observation at the watch-mode interval. When it detects a
changed observation, the next request closes the database, runs the ordinary
authoritative refresh, and opens the newly published generation before query
execution. The session never refreshes through a second provider path. Current
source excerpts retain their independent visibility, identity, hash, encoding,
range, and byte-budget checks.

EOF or cancellation closes the database. The client owns the child process;
there is no daemon registration, hidden hook, socket, PID file, or durable
session state. Other Weave processes against the same worktree are expected to
hit the existing bounded open timeout while a session owns the database. This
constraint is documented rather than hidden.

## Consequences

Warm requests avoid process startup, repeated bstore opens, schema preflights,
intern hydration, and synchronous Git observation. The contract is independent
of Go and of every producing language. Existing application query semantics,
graph evidence, source validation, and deterministic storage remain shared.

The first request remains cold. Detection is asynchronous at a bounded polling
interval, though source-rich output detects changed files independently. A
single session serializes work and excludes catalog and mutation commands. An
agent host must keep the subprocess alive to receive the latency benefit and
must close it before invoking maintenance.

## Deferred

- MCP and local-socket facades over the same application service;
- multi-client broker discovery and authentication;
- resident federated/catalog aggregate ownership; and
- native filesystem wakeup hints ahead of exact Git reconciliation.
