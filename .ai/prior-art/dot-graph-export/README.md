# DOT graph export prior art

Researched 2026-08-07 before adding Weave's Graphviz output surface.

## Local `pkgdeps` precedent

The local tool at `/Users/ryan/src/github.com/TheFellow/x/pkgdeps` is the
immediate product precedent. Its focused graph is useful because it does not
render an indiscriminate repository dump:

- the selected package is gold;
- transitive importers and imports are colored differently;
- a node reachable in both directions is visually distinct;
- packages are clustered by ownership area;
- command and test roots have different shapes, with test edges dashed;
- graph flow is left to right and output is deterministically ordered; and
- DOT can be retained without requiring Graphviz, while Graphviz is an optional
  renderer for SVG, PNG, and PDF.

Weave should preserve those principles while replacing Go-package-specific
areas with provider clusters and typed semantic edges. A bounded focused
neighborhood is more useful than exporting an index containing hundreds of
thousands of occurrences and edges.

## Graphviz

Graphviz's [DOT language](https://graphviz.org/doc/info/lang.html) is the stable
text interchange. Its documented graph, node, edge, and cluster
[attributes](https://graphviz.org/doc/info/attrs.html) cover the layout and
presentation Weave needs without a rendering dependency. In particular:

- `rankdir=LR` makes dependency direction readable;
- `subgraph cluster_*` groups related nodes;
- `shape`, `style`, `fillcolor`, and `color` distinguish semantic roles;
- labels and tooltips are `escString` values whose backslash substitutions must
  be escaped deliberately; and
- `splines`, `nodesep`, and `ranksep` can reduce visual collisions.

The official [escString documentation](https://graphviz.org/docs/attr-types/escString/)
is important for untrusted source names: Graphviz interprets sequences such as
`\N`, `\E`, and `\L`. Weave therefore owns a small quoted-string encoder that
escapes backslashes, quotes, line breaks, and control characters instead of
concatenating source text into DOT.

## Other options considered

- A Go Graphviz object-model dependency would reduce a small amount of string
  emission code but would not remove the need to choose labels, clusters,
  styles, bounds, or stable ordering. The DOT grammar used here is intentionally
  tiny and covered by adversarial escaping tests.
- Shelling out to `dot` would conflate interchange generation with optional
  presentation software, fail on machines without Graphviz, and widen the
  execution boundary. Weave emits DOT only; users may pipe it into any Graphviz
  renderer.
- A complete database export is deterministic but not followable at real
  repository scale. The initial product is a bounded neighborhood around an
  explicitly resolved graph symbol, with direction and edge-kind controls.

## Decision carried into implementation

Add `weave graph TARGET`. It resolves `TARGET` with the same query semantics as
other commands, walks incoming, outgoing, or both directed neighborhoods under
explicit depth, node, and edge limits, and emits deterministic DOT. The focused
node and directional reachability use the `pkgdeps` palette. Materialized nodes
are clustered by provider; unresolved external endpoints remain visible in a
separate cluster. Edge labels retain kind and tooltips retain evidence/provider.
The command works against one repository or the catalog and can emit the same
bounded result as versioned JSON for agent use.
