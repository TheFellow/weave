# Machine-wide aggregate read cache prior art

Research was performed before implementation on 2026-08-07. Links and source
revisions are pinned so later changes in upstream projects do not silently
rewrite the basis for this design.

## Git commit-graph: derived truth with an explicit generation

- Source: Git v2.51.0, commit
  [`c44beea485f0f2feaf460e2ac87fdd5608d63cf0`](https://github.com/git/git/tree/c44beea485f0f2feaf460e2ac87fdd5608d63cf0/Documentation/technical)
- Documentation:
  [`commit-graph.adoc`](https://github.com/git/git/blob/c44beea485f0f2feaf460e2ac87fdd5608d63cf0/Documentation/technical/commit-graph.adoc)
  and
  [`commit-graph-format.adoc`](https://github.com/git/git/blob/c44beea485f0f2feaf460e2ac87fdd5608d63cf0/Documentation/technical/commit-graph-format.adoc)
- Relevant behavior: the commit graph is a supplemental representation, has a
  versioned/checksummed format, is selected by source object identities, and is
  written to a temporary file under a lock before publication.

**Adopt:** treat the aggregate strictly as a supplemental representation; key
it by an explicit digest of authoritative source generations; write a complete
temporary database under a bounded process lock and publish only after close
and validation.

**Adapt:** Weave uses immutable generation-named bstore databases rather than a
chain. The current graph is small enough that a complete rebuild is simpler and
safer than a layered format. Old generations are disposable and can be removed
after the new generation is visible.

**Defer:** split aggregate chains and Bloom filters. They add invalidation and
compaction complexity before repository-scale measurements justify them.

## Zoekt: repository shards and compact search projections

- Source: Sourcegraph Zoekt commit
  [`c4d8f3537f7a67423835d8cf6b0e1d13e68e0b4c`](https://github.com/sourcegraph/zoekt/tree/c4d8f3537f7a67423835d8cf6b0e1d13e68e0b4c)
- Documentation:
  [`doc/design.md`](https://github.com/sourcegraph/zoekt/blob/c4d8f3537f7a67423835d8cf6b0e1d13e68e0b4c/doc/design.md)
  and
  [`shards/shards.go`](https://github.com/sourcegraph/zoekt/blob/c4d8f3537f7a67423835d8cf6b0e1d13e68e0b4c/shards/shards.go)
- Relevant behavior: search reads immutable repository shards, reloads changed
  shards, and keeps indexing lifecycle separate from query serving.

**Adopt:** materialize only the projection needed for global symbol search and
edge traversal. Keep repository/worktree provenance beside each projected fact
and make selection/fan-out explicit.

**Adapt:** Weave has no daemon and no source crawler. A foreground query first
refreshes each selected worktree through its normal authority, then validates
or rebuilds the disposable projection.

**Reject:** an independently updated shard service. It would weaken Weave's
query-time freshness contract and add a mandatory background component.

## Bazel remote cache: input identity before reuse

- Source: Bazel 8.3.1 tag
  [`61aa5a57c5511508a0fd5af8576e541108c0f07d`](https://github.com/bazelbuild/bazel/tree/61aa5a57c5511508a0fd5af8576e541108c0f07d)
- Documentation:
  [`site/en/remote/caching.md`](https://github.com/bazelbuild/bazel/blob/61aa5a57c5511508a0fd5af8576e541108c0f07d/site/en/remote/caching.md)
- Relevant behavior: an action result is reusable only through the digest of
  its declared inputs; absent cache entries cause authoritative local work.

**Adopt:** derive a canonical source-set digest from catalog key, repository and
worktree identity, local database path, and the freshness manifest generation.
A miss, stale generation, schema mismatch, or corrupt cache triggers rebuild or
authoritative federation, never a stale answer.

**Reject:** storing source bodies or compiler outputs in a CAS. Per-worktree
indexes already own those facts; the machine cache needs only hot query facts.

## bstore/bbolt: bounded single-writer embedded storage

- Dependency: `github.com/mjl-/bstore` v0.0.11, already pinned in `go.mod`.
- API documentation:
  [`pkg.go.dev/github.com/mjl-/bstore@v0.0.11`](https://pkg.go.dev/github.com/mjl-/bstore@v0.0.11)
- Relevant behavior: bstore is concurrency-safe within a handle, bbolt permits
  one writer, and `Options.Timeout` bounds cross-process open/lock contention.

**Adopt:** a separate tiny bstore lock database serializes cache builders.
Generation databases use indexed symbol, token, and bidirectional edge records.

**Adapt:** provenance is normalized into source and source-link records rather
than copying repository strings onto every fact. The aggregate deliberately
omits units, documents, occurrences, and source text.

## Resulting contract

The machine aggregate is an immutable, generation-named bstore database under
the same platform state root as `catalog.db`. It contains only symbols, symbol
tokens, edges, and fact-to-worktree provenance. Before every accelerated query,
all selected worktrees pass their existing authoritative freshness check. The
cache filename and metadata must then match the exact sorted source-generation
set. Any uncertainty falls back to the already-open authoritative federation;
cache failure is a performance diagnostic, not a correctness failure.

This deliberately postpones incremental aggregate reconciliation. Measurements
must first show that rebuilding the hot projection is a material cost and that
RIBLT or layered shards beat direct generation comparison and replacement.
