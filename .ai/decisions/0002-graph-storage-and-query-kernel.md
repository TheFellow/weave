# ADR 0002: Initial graph storage and query kernel

- Status: Accepted
- Date: 2026-08-06
- Research: [graph storage and query prior art](../prior-art/graph-storage-and-query/README.md)

## Context

Weave needs a compact disposable store that can replace one adapter compilation
unit atomically and answer predictable agent-facing graph queries. It must not
require a daemon, server, LLM, cgo, or a full in-memory graph. Results must be
bounded and deterministic even when adapter fact order changes.

## Decision

### Store and schema

Use `github.com/mjl-/bstore` v0.0.11 over bbolt. The intended Codeberg module is
not published/resolvable, so the extant upstream module path is the supported
option. Persist explicit records for metadata, compilation units, documents,
symbols, occurrences, edges, and symbol-token postings.

Every record has a stable string primary key supplied or deterministically
derived at normalization time. Every unit-owned record has an indexed `UnitID`.
Edges have compound indexes beginning with `From` and with `To`, each followed
by `Kind`, so forward and reverse adjacency do not scan the edge table. Token
postings index both token and symbol.

The database contains a singleton metadata record with Weave's integer schema
version. Opening a newer or older unsupported version fails explicitly and
instructs callers to rebuild; bstore's automatic compatible record evolution is
not treated as an application migration system.

### Atomic unit replacement

The storage API accepts one complete, prevalidated unit fact set. In one bstore
write transaction it removes the unit's old token postings, edges, occurrences,
symbols, and documents in dependency-safe order, inserts the replacement facts,
and finally updates the unit record. Any validation, uniqueness, cancellation,
or storage error rolls back the transaction and preserves the prior complete
unit.

Provider facts may point to symbols outside the current unit or database, so
edge endpoints are stable strings rather than enforced bstore foreign keys.
`verify` distinguishes unresolved external targets from malformed local facts.

### Query strategy

Exact and prefix name queries use indexed normalized names; token lookup uses
indexed token postings. Results are ranked and then sorted deterministically.
Definitions come from symbols and references from occurrences. Callers and
callees are filtered `Calls` adjacency queries.

Path is ordered bounded breadth-first search. Impact is the same kernel over
reverse adjacency, defaulting to dependency-bearing edge kinds. Traversals mark
on enqueue, preserve first predecessor, and return explicit truncation metadata
when depth, result, or examined-edge limits are reached.

All store queries require positive bounded limits. Unbounded export is a
separate diagnostic operation with deterministic stable-ID sorting.

### Output

Application result structs are the single source for human and JSON output.
JSON is emitted in versioned envelopes and sorted slices with a trailing
newline. Empty successful search/traversal results are silent in text mode but
produce a complete empty JSON envelope when `--json` is requested.

### Verification, corruption, and compaction

Open and query errors that identify invalid/checksum/storage state are wrapped
in a stable corruption category with rebuild guidance. `verify` scans normalized
invariants and reports every deterministic issue. It is silent on success in
text mode.

Deletion reuses bbolt pages but does not shrink the file. `gc` performs an
offline `bbolt.Compact` rewrite into a sibling temporary file and atomically
replaces the database only after successful close/sync. It cannot run while a
live store holds the exclusive file lock.

## Consequences

Positive:

- pure-Go, single-file, transactional persistence;
- cheap adjacency in both directions without a graph server;
- adapter reordering does not change exports or query presentation;
- compilation-unit failure cannot expose half-updated facts;
- the CLI can serve humans and agents from the same typed results.

Costs:

- repeated stable strings consume more index space than custom packed IDs;
- bstore has no joins, so application code assembles occurrences and symbols;
- one process owns the file and one transaction writes at a time;
- incompatible schema evolution initially requires deterministic rebuild;
- compaction is an explicit offline maintenance operation.

## Rejected alternatives

- **Raw bbolt buckets:** smaller potential encoding, but Weave would own schema,
  record codecs, constraints, and query mechanics immediately.
- **SQLite/FTS5:** capable prior art, but adds cgo or a large pure-Go SQLite
  dependency before Weave needs SQL or full-text search.
- **Graph database/server:** contradicts local disposable operation and adds an
  operational dependency.
- **Adjacency arrays embedded on symbols:** replacement rewrites unrelated
  symbol records and makes edge evidence/ownership awkward.
- **Autoincrement-only fact identity:** makes cross-unit links and reproducible
  exports depend on insertion history.
- **General graph library:** duplicates the persisted graph in memory for small
  BFS operations and adds no required semantics.
- **Committed binary database:** derived branch/worktree state would produce
  opaque diffs and conflicts.
