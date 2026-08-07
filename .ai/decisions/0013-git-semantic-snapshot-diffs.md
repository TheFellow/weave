# ADR 0013: Git-aware semantic snapshot diffs

- Status: Accepted
- Date: 2026-08-07
- Research: [Git-aware semantic graph diffs](../prior-art/git-semantic-graph-diffs/README.md)

## Context

A Git file diff is not a graph diff. One source edit may leave normalized facts
unchanged, change a provider-owned API surface, remove relationships, or affect
reverse dependents and tests. Weave needs one machine-readable answer without
checking out over the user's files, poisoning the current worktree index, or
inventing cross-language compatibility claims.

## Decision

`weave diff graph`, `weave diff api`, `weave diff impact`, and
`weave diff tests` share `weave.snapshot-diff/v1`. `--base` is required;
`--head` selects a second immutable Git revision and defaults to the current
dirty worktree. Git name-status changes remain a separate collection from
normalized unit, document, symbol, occurrence, and edge changes.

Both revisions resolve to exact commit and tree object IDs. A historical side
is indexed through the configured provider/freshness pipeline in a temporary
detached linked worktree. Its per-worktree database and manifest remain derived
state and are removed with the worktree on success, provider failure, or
cancellation. Materialization supplies an empty `core.hooksPath`, preventing
repository post-checkout hooks from running. Configured Git checkout filters
may still execute during checkout; they remain part of the user's local Git
trust/configuration boundary and are not treated as portable source truth.

Snapshot identity has two layers:

- commit/tree plus freshness generation prove the exact observed provider and
  overlay state; a temporary worktree generation is intentionally ephemeral;
- SHA-256 over the canonical sorted normalized snapshot is stable across
  repeated materializations and identifies semantic equality.

For a live head, Weave first refreshes and exports the graph, then reads Git's
source changes, then reinspects commit/tree/dirty/count/generation. Mutation at
any point fails with a retry instruction rather than publishing a mixed diff.

API output compares only nonempty provider-owned public-surface fingerprints.
It reports added, removed, or changed surfaces and always labels compatibility
`unknown`; the core does not infer breaking changes from names or symbol kinds.

Impact seeds the existing bounded reverse traversal from current changed facts,
removed stable IDs, and Git paths that resolve in the head graph. Affected tests
are a projection of those same impact nodes. Explicit `tests` edges preserve
their evidence and edge ID, provider-classified test symbols preserve provider
evidence, and Go naming recognition is explicitly syntactic.

The graph delta also exposes stable-ID node/edge transition operations. The
secured explorer serves that same bounded contract through its application
boundary, allowing its keyed d3-graphviz renderer to animate add, remove, and
change operations without another diff engine.

## Consequences

- Ref comparison can be expensive because uncached historical facts are rebuilt
  deliberately; no committed database or permanent snapshot is required.
- Providers that require an explicitly trusted local build tool retain that
  existing trust boundary when historical facts are generated.
- Removed disconnected facts have no head adjacency. They remain visible in the
  graph delta and diagnostics explain when head impact cannot proceed.
- Every emitted collection and traversal is deterministic and bounded. Full
  snapshot export is currently required internally; streaming comparison can be
  introduced behind the unchanged contract if measurements justify it.

## Rejected alternatives

- Mutating the caller's checkout or branch.
- Comparing source paths and calling the result a semantic diff.
- Keeping temporary linked-worktree databases in the catalog.
- A second impact implementation specialized for diffs.
- Language-neutral breaking-change guesses over lexical graph facts.
