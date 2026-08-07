# Bounded source-rich context query prior art

Research date: 2026-08-07.

This increment is intentionally a query composition over Weave's existing
normalized graph. It does not add a second index, an LLM, embeddings, MCP, or
heuristic relationships.

## Pinned primary sources

- [CodeGraph at `969ea1ec`](https://github.com/colbymchenry/codegraph/tree/969ea1ec371dc62d056cbeb3920fa79036128842)
  composes search, a shallow graph expansion, related files, and source blocks.
  Its [default context budget](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/context/index.ts#L135-L174)
  is deliberately small, and its
  [pipeline](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/context/index.ts#L202-L264)
  treats context as a composition rather than a new persistence model.
- CodeGraph's newer explorer path explicitly serves
  [current, line-numbered on-disk source under a hard response ceiling](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/mcp/tools.ts#L4079-L4105).
  Its source-serving chokepoint performs both
  [lexical and symlink-aware root containment](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/utils.ts#L68-L125).
- [SCIP at `f7c0b174`](https://github.com/scip-code/scip/tree/f7c0b174aea88b51dbeef1b583844577efb989e0)
  remains the source of compiler/LSP-grade symbol identities, occurrences, and
  ranges consumed by Weave adapters. The context response must preserve those
  coordinates and evidence classes rather than reinterpret source.
- Git's [`ls-files`](https://git-scm.com/docs/git-ls-files) is the authoritative
  worktree inventory primitive already used by Weave. Source serving should
  require an exact cached-or-untracked, non-ignored path instead of trusting an
  arbitrary path stored in a graph record.

## Adopt

- One bounded call should return the focus, binding/reference evidence, direct
  incoming and outgoing relationships, materialized adjacent entities, and
  current line-numbered source around evidence locations.
- Read source from disk at query time. The graph locates evidence; it is not a
  source-content cache.
- Make all budgets explicit and report truncation by section. Keep ordering
  deterministic so agents can safely consume the JSON.
- Treat root containment and Git visibility as source-serving security
  boundaries. Require a regular file and validate the opened identity again to
  close path replacement races.
- Compare current bytes with an indexed content hash when one exists. If they
  differ, report drift and withhold a potentially misaligned excerpt.

## Adapt

- CodeGraph's natural-language multi-entry search is useful prior art, but this
  first Weave command resolves exactly one heterogeneous graph entity. A fuzzy
  query with multiple results fails with exact IDs in the diagnostic instead of
  guessing which entity the caller meant.
- CodeGraph returns whole symbol bodies and later uses adaptive multi-file
  allocation. Weave initially returns small line windows around exact graph
  ranges, under both per-file and total source-byte bounds. This works for code,
  Markdown sections, routes, and other document-backed entities without
  pretending all entities have an AST body.
- Weave keeps compiler/content/manual provider and evidence provenance visible
  on every fact. Catalog context uses the existing bounded federation store and
  associates source reads with the repository that produced the evidence.
- Weave's normal query-driven freshness check remains authoritative. There is
  no watcher or daemon prerequisite.

## Defer

- Natural-language task ranking, multiple entry points, full-body exploration,
  path tracing, source deduplication across a large dossier, and adaptive token
  allocation. These need measured retrieval-quality work, not guesses.
- Secret classification or redaction based on file syntax. This command only
  serves evidence from Git-visible files already indexed by an enabled provider;
  a future policy may explicitly suppress configuration-value excerpts.
- MCP, an embedded model, embeddings, summaries, generated explanations, or a
  separate source database.
- Editing source or graph facts from the context response.

## Acceptance boundary

`weave context TARGET` must be one-hop and independently bounded for
occurrences, incoming edges, outgoing edges, source file bytes, source lines,
and total returned source bytes. Missing, external, generated, changed,
non-UTF-8, non-regular, ignored, and out-of-root documents are data conditions
reported in the response, not reasons to expose unsafe bytes or fabricate
source.
