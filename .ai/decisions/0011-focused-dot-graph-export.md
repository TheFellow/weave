# ADR 0011: Focused Graphviz DOT export

- Status: Accepted
- Date: 2026-08-07
- Research: [DOT graph export prior art](../prior-art/dot-graph-export/README.md)

## Context

Weave already stores typed bidirectional graph adjacency and exposes bounded
path, impact, dependency, and workspace queries. JSON is appropriate for agents
but unnecessarily difficult for a person reviewing dependency direction,
documentation relationships, or a mixed-language neighborhood. The local
`pkgdeps` tool demonstrates a useful presentation: focus one node, distinguish
importers from imports, group related nodes, retain directed edges, and let
Graphviz perform optional rendering.

Exporting the complete Weave database would not produce an understandable
diagram. Real repositories contain hundreds of thousands of occurrences and
edges. DOT generation also must not introduce a required Graphviz executable or
allow symbol text to become DOT syntax or `escString` substitutions.

## Decision

Add `weave graph TARGET` as a freshness-aware database query. Resolve `TARGET`
with the normal deterministic symbol resolver, then walk incoming, outgoing, or
both neighborhoods. The node, examined-edge, and depth bounds are explicit CLI
inputs with conservative defaults and hard maxima. Repeated `--kind` filters use
the existing graph edge vocabulary. Local and catalog scopes share the same
operation and federated provenance behavior.

Keep incoming and outgoing traversal states independent and interleave their
queues. This preserves the distinction between upstream and downstream reach
while sharing one global resource bound without always exhausting it in the
first direction. Return the same nodes, edges, truncation marker, symbols, and
catalog sources as `weave.query/v1` when `--json` is requested.

Emit DOT directly from a small presentation package:

- use generated sequential DOT node names and retain exact semantic IDs in
  tooltips;
- use compact display/stable labels and shapes based on symbol kind;
- color the focus, incoming, outgoing, and bidirectionally reachable nodes as
  `pkgdeps` does;
- cluster materialized nodes by provider and isolate unresolved endpoints;
- label edges by relationship kind and retain evidence/provider in tooltips;
- omit occurrence-level `defines` and `references` from the default view unless
  explicitly selected, and collapse equivalent rendered edges with a count;
- use evidence to select solid, dashed, or dotted edge styling;
- sort clusters, nodes, and edges deterministically; and
- quote every dynamic string, including source backslashes, quotes, newlines,
  and control characters.

DOT is written to stdout unless `--output` is supplied. Weave never invokes
Graphviz. A user can pipe the result to `dot`, another renderer, or an
interactive viewer. Optional tests ask an installed `dot` executable to parse a
hostile-label fixture, but Graphviz is not a build or runtime dependency.

## Consequences

Positive:

- one graph query works for code, content, bridges, and catalog relationships;
- diagrams remain followable because every graph is focused and bounded;
- DOT source is reproducible and useful in CI artifacts or documentation;
- agents can request JSON while humans use the corresponding picture; and
- renderer choice and installation remain outside Weave.

Costs and limitations:

- provider clusters are provenance-oriented rather than architecture-owned
  areas; future checked-in grouping rules may offer better domain clusters;
- generated DOT node names are stable only for the selected result, while
  semantic IDs remain the durable identity in tooltips/JSON;
- very dense graphs can still be visually busy, so edge-kind filters remain an
  important part of the product; and
- DOT styling is a compatibility surface that should change deliberately even
  though the pre-1.0 CLI has not promised strict byte stability.
