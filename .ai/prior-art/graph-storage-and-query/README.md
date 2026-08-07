# Graph storage and query prior art

Research date: 2026-08-06 (America/Los_Angeles)

This note records the primary-source research behind Weave's first durable
graph kernel. The scope is deliberately smaller than a general graph database:
atomic compilation-unit replacement, indexed directed adjacency, bounded
traversal, stable export, and symbol lookup.

## bstore and bbolt

[`bstore`](https://pkg.go.dev/github.com/mjl-/bstore) is a small Go-native
record store over bbolt. Its useful properties for Weave are serializable
transactions, automatic schema registration from Go structs, compound and
multikey indexes, explicit query limits, and query statistics that reveal
whether an index or table scan was selected. The first struct field is the
primary key. Compound index tags are declared on their first field, for example
`index Unit+Kind`, and index keys contain the indexed values plus the primary
key.

Consequences for the initial schema:

- one `DB.Write` transaction can remove and replace every record owned by a
  compilation unit atomically;
- edges need separate `From+Kind` and `To+Kind` indexes because bstore has no
  joins or graph primitive;
- stable string IDs avoid insertion-order-dependent foreign identities;
- compact token postings can use indexed `(Token, Symbol)` records and bstore's
  ordered string range scans for prefix lookup;
- query code must impose limits itself and must sort explicitly whenever the
  index does not prove the requested presentation order;
- explicit Weave schema metadata is still required: bstore can evolve many Go
  record shapes automatically, but it intentionally rejects some conversions
  and does not express application-level migration compatibility.

bstore v0.0.11 is the current stable release at this review. Its module remains
`github.com/mjl-/bstore`. The requested `codeberg.org/mjl/bstore` module path
does not resolve through the Go module/VCS tooling (the host requests
authentication and pkg.go.dev has no module at that path), which is a concrete
adoption blocker. We therefore pin the maintained, published upstream module
path rather than inventing a replace directive or vendored fork. Revisit if the
author publishes a Codeberg module.

The underlying [`bbolt`](https://github.com/etcd-io/bbolt) store allows one
writer and concurrent snapshot readers. A successful `Update` commits and an
error rolls back. It uses copy-on-write pages and does not shrink the file after
deletion; freed pages are reused. Physical space reclamation requires rewriting
to a second database with `bbolt.Compact`, closing both files, and atomically
replacing the old file. Long read transactions delay page reuse, and a database
file is exclusively locked by a process, so compaction is an explicit offline
operation rather than part of each refresh.

The bbolt `Tx.Check` consistency walk is useful for `verify`, but bstore does
not expose its underlying transaction. Weave's initial online verification
therefore checks its normalized invariants through bstore. Offline compaction
opens the file directly through bbolt and necessarily exercises its page
reader. A later maintenance API can add a dedicated offline `Tx.Check` pass if
field experience justifies the second open.

## Compact adjacency and traversal

CodeGraph's documented local design stores symbols and directed relationships
in SQLite and answers callers, callees, paths, and blast radius from indexed
edge rows. That is the relevant reusable idea, not its parser or FTS stack:
persist an edge once and index both endpoint directions. Weave applies the same
shape to bstore with explicit evidence and compilation-unit ownership.

An unweighted shortest path and reverse impact closure need no graph framework.
Breadth-first search is linear in visited vertices plus examined edges and
returns a shortest hop path. Determinism requires more than BFS itself: fetch
each frontier's neighbors in stable `(kind, target, edge-id)` order, mark a
vertex when enqueued, and retain the first predecessor. Every traversal takes
explicit maximum-depth, maximum-result, and maximum-examined-edge bounds.
Reaching a bound is reported as truncation rather than silently claiming a
complete closure.

We avoid importing a general graph package because persistence already owns the
adjacency operation and the bounded BFS implementation is small enough to
specify and test exhaustively. This also prevents an in-memory duplicate of the
whole graph.

## Deterministic result and JSON ordering

Go deliberately does not specify map iteration order. The standard
[`encoding/json`](https://pkg.go.dev/encoding/json) encoder sorts supported map
keys, but relying on maps would still obscure the domain ordering contract.
Weave exports versioned structs and pre-sorted slices. Records sort by stable
identity, locations by `(path, start, end, role)`, and edges by
`(from, kind, to, evidence, id)`. Human output consumes the same ordered result
objects as JSON, preventing separate order semantics.

JSON is a versioned envelope (`weave.query/v1` for query results and
`weave.export/v1` for full facts). Adding optional fields is compatible;
changing meanings or coordinate conventions requires a new schema identifier.
The encoder writes one trailing newline so byte-for-byte contract tests remain
portable.

## Lightweight symbol search

Full code search engines such as
[`zoekt`](https://github.com/sourcegraph/zoekt/blob/main/doc/design.md) use
positional trigrams and posting lists to support substring and regular-expression
search over large corpora. That machinery is excellent prior art but excessive
for the first symbol-name query.

Weave begins with Unicode-lowercased identifier tokens and prefixes:

1. retain an indexed normalized full display name for exact/prefix lookup;
2. split camel-case, underscore, punctuation, and letter/digit boundaries into
   lowercase tokens;
3. store unique `(token, symbol)` postings;
4. intersect or union bounded postings in memory and rank exact normalized name,
   then name prefix, then token prefix;
5. tie-break on stable symbol identity.

This is deterministic, language-neutral, small, and readily replaceable. A
trigram index should be introduced only after measured symbol corpora show that
substring/fuzzy lookup is necessary. Zoekt should remain an optional sibling
for source-text search rather than becoming a dependency of the graph kernel.

## Adopted

- bstore records and compound indexes over bbolt.
- One transaction per complete compilation-unit replacement.
- Stable string fact identities and explicit bidirectional edge indexes.
- Bounded, ordered BFS over indexed adjacency.
- Versioned struct-based JSON and one canonical sort order.
- Normalized name plus token postings; no FTS or embeddings.
- Explicit offline rewrite for physical compaction.

## Deferred

- A general graph query language or in-memory graph library.
- Fuzzy edit-distance and trigram symbol search.
- Physical integrity walks while a bstore handle is live.
- Automatic schema migrations across incompatible record shapes.
- Sharding or a concurrent multi-process writer protocol.
