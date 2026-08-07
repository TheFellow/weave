# ADR 0014: Optional polling watch-mode warmer

- Status: Accepted
- Date: 2026-08-07
- Research: [watch-mode warming prior art](../prior-art/watch-mode-warming/README.md)

## Context

Query-driven freshness is correct but can put provider cold-start latency on an
interactive query. An optional foreground process may warm the same worktree
index after source, branch, index, or build-input changes. It must not become a
second freshness authority, graph store, provider pipeline, or required daemon.

Cross-platform native notification APIs expose different recursive, rename,
overflow, network-filesystem, and resource-limit behavior. Even a mature wrapper
still requires exact reconciliation. Weave already has an exact Git worktree
observation, content hashing, linked-worktree storage, a repository writer lock,
and atomic freshness publication.

## Decision

`weave watch` is a foreground polling warmer over the existing freshness
manager. By default it performs one non-forced initial `Freshness.Ensure`, emits
one ready event, and then polls at a configurable bounded interval. Disabling
the initial refresh performs a read-only observation first and defers warming to
the first poll.

Each poll obtains a repository observation token covering repository/worktree,
provider, commit/tree/branch/detached state, and the exact hashed Git overlay.
The polling observation compares that source state with the published manifest
but deliberately does not reopen the graph database merely to reverify its
generation; the initial `Ensure` and every query retain that authority. A
current observation does no work unless an authoritative `Ensure` remains
pending after failure. A stale observation or pending failed refresh calls only
`Freshness.Ensure(false)`; a failed observation is retried without guessing at
source state. A new token bypasses an older token's retry delay but still runs
the pending authoritative check. One event loop means refreshes never overlap; Go's
ticker coalesces ticks while a refresh is running, so bursts and atomic saves are
reconciled as one resulting Git state rather than replayed as filesystem events.

Failure does not publish a current manifest. It emits a bounded error record and
continues when safe. A new observation is eligible immediately; an unchanged
failure retries with exponential backoff capped at 30 seconds. The loop owns and
stops its ticker, treats context cancellation as graceful termination, and
passes that context through repository inspection and provider refresh.

The status returned by a successful `Ensure` is the sole truth for that
completion event. A follow-up observation token is attached only when repository
identity, worktree, commit/tree, overlay shape, and exact freshness generation
match that returned status. An edit between publication and observation therefore
produces an honest successful completion with no token; the newly observed state
is refreshed on the next poll. A failed follow-up observation likewise cannot
erase the successful completion: the completion is emitted first without a
token and the observation error is a separate bounded event. This also preserves
exactly one `ready` lifecycle event.

Machine output is newline-delimited `weave.watch-event/v1` JSON with monotonic
sequence numbers and `ready`, `refreshed`, or `error` event types. Human success
events go to stdout; diagnostics and recoverable errors go to stderr. No output
is produced for unchanged polls. Error text is normalized to valid UTF-8 and
capped at 8 KiB per record; freshness diagnostics retain their existing
validated count and size bounds.

The process entry point derives its context from interrupt and termination
signals. Queries continue to call `Freshness.Ensure` and may independently warm
or verify the index through the same worktree writer lock.

## Consequences

Positive:

- no new durable state, indexing algorithm, daemon, hook, native dependency, or
  platform-specific watcher code;
- atomic-save bursts, missed events, branch changes, ignored files, and linked
  worktrees inherit the already-tested Git freshness semantics;
- one loop and one worktree lock prevent refresh storms; and
- cancellation and output have directly testable contracts.

Costs:

- every interval executes an exact Git observation and hashes currently changed
  regular files; very large untracked worktrees may need a longer interval;
- polling latency is bounded by the selected interval rather than immediate OS
  notification; and
- the process observes only the provider input universe already represented by
  Git freshness. Explicit unmanaged SCIP imports remain unmanaged.

Native event hints may later wake the same reconciliation loop if benchmarks
justify the dependency and directory-management complexity. They may not replace
periodic or query-time exact reconciliation.

## Rejected alternatives

- **Make native filesystem events authoritative:** event loss, recursive-watch
  maintenance, editor renames, Git metadata, and unsupported filesystems still
  require reconciliation.
- **Add a persistent daemon:** contradicts the optional local-first lifecycle.
- **Build a watch-specific indexer:** risks semantic divergence and partial
  publication outside the freshness coordinator.
- **Retry every poll forever without backoff:** a broken provider would create
  noisy logs and repeated expensive work with no new evidence.
