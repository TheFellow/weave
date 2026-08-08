# ADR 0019: Structured-content body discovery

- Status: Accepted
- Date: 2026-08-08
- Research: [structured-content discovery prior art](../prior-art/structured-content-discovery/README.md)

## Context

Structured-content indexing modeled Markdown sections precisely, but retrieval
posted only symbol display names. Agents asking about concepts stated in prose
could miss the correct section unless those concepts happened to appear in its
heading. IDE-scale discovery demonstrates that a compact inverted word
projection can coexist with richer semantic indexes without storing another
copy of source.

## Decision

Normalized graph schema 2 adds optional bounded `search_terms` to a symbol.
They are lowercase sorted unique identifier tokens, at most 128 bytes each and
2,048 per entity. Storage format 3 persists them with the symbol and emits the
same bstore token postings used for display-name lookup. Logical export and the
disposable cross-repository machine aggregate preserve them.

The current workspace provider extracts terms from a Markdown document's
prelude and metadata, each heading's direct body until the next heading, and
each fenced code block. It does not execute templates or store source text.
For every other Git-visible regular non-asset file, it indexes bounded UTF-8
body terms on the existing file entity without interpreting the language.
Files are capped at 2 MiB and the deterministic generic corpus at 512 MiB;
binary, oversized, and later corpus files remain topology-only. Subsequent
refreshes use the exact Git diff from the prior manifest commit and carry
unchanged complete file units forward without rereading them.
Explore ranking treats path/stable-name matches as durable scope, uses bounded
posting frequency and entity vocabulary length to prefer discriminating terms
and focused sections, keeps matching sections from the strongest document
coherent, and demotes explicit generated representations. Known `llms.txt` and
`llms-full.txt` aggregations are labeled generated evidence. A generic file
dossier locates the strongest matching current line before applying the normal
source context and byte bounds. Multi-focus explore results include complete
focus evidence but omit redundant source excerpts for every adjacent edge;
their typed relationships and locations remain, and direct `context` queries
retain the richer relationship excerpts. Search terms remain discovery hints
and never imply a graph relationship or stronger semantic edge.

## Consequences

Agents can find exact content entities from body concepts and immediately
receive current line-numbered sections. Index size and refresh work increase in
proportion to bounded unique section vocabulary. Existing format-2 indexes are
rebuildable derived data and are rejected with the normal remove-and-index
guidance; no migration is provided.

This is not arbitrary binary full-text search, a language-aware stemmer, phrase
search, a full BM25 implementation, or a second query engine. Query variants
cover only a few deterministic suffix forms. Ranking statistics remain bounded
to the existing posting result and entity vocabulary; they retain deterministic
results and the entity/evidence boundary.
