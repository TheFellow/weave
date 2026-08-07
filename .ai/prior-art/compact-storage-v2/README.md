# Compact storage v2 prior art — 2026-08-07

This review preceded the storage-v2 implementation. Links are pinned to the
version or commit inspected. The decision boundary was deliberately narrow:
retain bstore/bbolt, preserve Weave's normalized public graph, and improve the
disposable per-worktree representation through measured changes rather than a
second database engine or a private binary format.

## bstore and bbolt

### bstore v0.0.11

- Source: [`mjl-/bstore` v0.0.11 at
  `8d2fe118`](https://github.com/mjl-/bstore/tree/8d2fe1182522fe74bc929207aae1f1f68cb46cd2)
- Format: [record/type/index encoding](https://github.com/mjl-/bstore/blob/8d2fe1182522fe74bc929207aae1f1f68cb46cd2/format.md)
- License: MIT.

bstore packs non-zero record fields, varint-encodes integers, gives integer
primary keys an auto sequence, provides compound/unique indexes and atomic
transactions, and records historical Go type descriptions. Those are all a
good fit. Its important constraint is that every index key repeats all indexed
fields and the primary key in full. A string primary key therefore amplifies
every secondary index. bstore also has no joins or projection API: retrieving a
record decodes the complete record, and application-managed interning requires
explicit lookups and lifecycle accounting.

**Adopt:** auto numeric primary keys, typed compound indexes, transactions,
query-plan statistics, and bstore's existing packed encoding.

**Adapt:** keep an explicit Weave storage marker independent of the normalized
graph schema; use ref-counted dictionary rows for repeated provider, version,
language, kind, and role strings; map stable symbol IDs to numeric entity IDs;
and keep verbose range/provider/evidence fields in separately retrievable
detail records. A small in-memory dictionary contains only categorical intern
values, not graph/source data.

**Reject:** relying on bstore's automatic struct evolution for this revision.
Changing string primary keys to integers is unsupported, and per-worktree data
is cheaper and safer to rebuild. Production opens inspect the frozen metadata
record read-only before registering v2 types, so v1 cannot be partially
converted.

### bbolt v1.4.3

- Source: [`etcd-io/bbolt` v1.4.3 at
  `b92c7f6e`](https://github.com/etcd-io/bbolt/tree/b92c7f6eaf7b2e9b1d40e6b9e7d0b545d7b71ee7)
- Storage notes: [README](https://github.com/etcd-io/bbolt/blob/b92c7f6eaf7b2e9b1d40e6b9e7d0b545d7b71ee7/README.md)
- License: MIT.

bbolt supplies the single-file ACID B+tree, one-writer/many-reader model,
read-only inspection, and `Compact`. Deleted pages remain on the freelist and
do not shrink the file; physical size after churn therefore requires a rewrite.
Files are not portable across CPU endianness, which reinforces treating them as
local derived state rather than release artifacts.

**Adopt:** read-only schema preflight, existing locking/cancellation behavior,
and atomic close-then-compact replacement.

**Adapt:** logical reference GC happens transactionally during unit replacement;
physical free-page reclamation remains the explicit `weave gc` rewrite.

## Stable external names and compact internal IDs

### SCIP

- Source: [`sourcegraph/scip` at
  `f7c0b174`](https://github.com/sourcegraph/scip/tree/f7c0b174aea88b51dbeef1b583844577efb989e0)
- Design rationale: [SCIP announcement](https://sourcegraph.com/blog/announcing-scip)
- License: Apache-2.0.

SCIP deliberately uses human-readable stable symbol strings at its interchange
boundary; the project reports that opaque global IDs complicated debugging and
incremental index construction. That argues against replacing Weave's public
IDs with database row numbers.

**Adapt:** stable IDs remain the graph/JSON/DOT/query identity. Numeric IDs are
private join keys only. An entity dictionary also gives unresolved external
endpoints a numeric key without inventing a materialized symbol.

### MillenniumDB

- Paper: [MillenniumDB: An Open-Source Graph Database
  System](https://doi.org/10.1162/dint_a_00229)
- License/source decision: reviewed as design prior art, not adopted as a
  dependency.

MillenniumDB represents graph objects and strings with IDs and builds B+tree
index permutations for source/target/type traversal. Its core lesson applies to
Weave even though its property-graph engine does not: fixed-width identities in
adjacency indexes avoid repeating long external names.

**Adapt:** numeric `From`/`To` fields lead both directional bstore indexes. The
stable endpoint and edge strings remain explicit ordering keys so bounded
results preserve the existing lexical truncation contract.

### LadybugDB v0.17.0

- Source: [`LadybugDB/ladybug` v0.17.0 at
  `1478b7e9`](https://github.com/LadybugDB/ladybug/tree/1478b7e98aa6e1265560937b5437f3ed8e0b0703)
- License: MIT.

Ladybug uses columnar storage and forward/reverse CSR adjacency structures. It
is compelling for large analytical graphs but would add a C++ engine, another
file lifecycle, and a second query model.

**Adapt:** explicit indexes in both edge directions and separation of hot
adjacency fields from cold properties.

**Defer:** CSR/columnar snapshots. Weave needs bounded atomic per-unit mutation
more than analytical scan throughput, and the measured bstore revision reduces
the representative file by 34% without another engine.

### petgraph and CodeGraph

- [`petgraph` at
  `ed714652`](https://github.com/petgraph/petgraph/tree/ed714652ab4576104e506c096b6ed9f5128613a7)
  offers compact/stable numeric in-memory graph indices.
- [`colbymchenry/codegraph` at
  `222f82b9`](https://github.com/colbymchenry/codegraph/tree/222f82b9a57780e4eaf28e46bbbad9cbda2d6666)
  is a local graph/query product using SQLite/FTS and a Rust kernel.

**Adapt:** keep numeric identities implementation-private; retain explicit
before/after size, lookup, traversal, export, replacement, open, and compaction
measurements; and optimize bounded result materialization before cold hydration.

**Defer:** an in-memory whole-graph mirror, SQLite/FTS, or a Rust storage kernel.
They would violate the one-store scope and are unnecessary for the measured
size goal. Search remains bstore prefix/token indexes.

## Lifecycle decision

Storage v2 is a deterministic rebuild, not an in-place migration. A v1 or
unknown marker produces `ErrSchema` with exact remove-and-`weave index`
guidance, before bstore registers v2 types. The read-only rejection is covered
by a byte-preservation test. Fresh v2 writes sort units and every fact family by
stable ID; normalized exports and hot scans are stable even though bbolt page
allocation and auto sequences are intentionally not a cross-platform artifact
contract.

Intern and entity rows carry reference counts. Unit deletion gathers all hot
and detail records, deletes them atomically, and releases shared references;
zero-count rows are deleted in the same transaction. `verify` independently
recomputes the counts and cold/hot correspondence. There is no orphan-tolerant
background GC.

Authored contextual links do not need a separate stable database:
`.weave/bridges.json` is already the canonical durable declaration, and its
provider reconstructs ordinary graph facts after a complete index deletion.
Only its edit lock is derived state.

## Possible upstream bstore follow-ups

No upstream change is required and this increment does not modify the bstore
fork. Two measured ideas may be useful upstream:

1. A query projection API could decode selected fields without forcing callers
   to split one logical record solely to keep verbose fields off hot reads.
2. Documenting or offering an application-level dictionary pattern with
   reference constraints could make string interning less error-prone.

Neither should be proposed without an isolated bstore benchmark and maintainer
interest. Weave's query losses around cold hydration may reflect its schema
trade-off rather than a general library defect.
