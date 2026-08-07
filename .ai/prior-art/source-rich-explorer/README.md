# Source-rich local graph explorer prior art

Research date: 2026-08-07.

This increment joins three existing Weave capabilities: bounded DOT graph
navigation, safe current-source context, and canonical contextual-relationship
authoring. It must not create a second graph, source reader, or declaration
writer.

## Animated Graphviz interaction

Magjac's [Graphviz Visual Editor at commit
`4823bcd`](https://github.com/magjac/graphviz-visual-editor/tree/4823bcd2ee1e48b8b6aa051e5750acf3f50c9619)
demonstrates the useful interaction: select nodes and edges, pan and zoom,
change the graph, and animate the existing layout into its new shape. Its
general DOT editor, URL export, and browser-local graph persistence are not a
fit for Weave because semantic facts and authored links have canonical local
sources outside the browser.

[`d3-graphviz` at commit
`355158d`](https://github.com/magjac/d3-graphviz/tree/355158dc789ff8556549018a0c0f7a567ac0bfc3)
is the smaller reusable mechanism. It lays out DOT through the Graphviz WASM
bundle, joins the resulting SVG through D3, and supports keyed enter/exit,
edge growth, path and shape tweening, pan, zoom, and worker-based layout. Its
documented `id` key mode requires the graph author to supply stable IDs;
Graphviz-generated IDs are not predictable. Named transitions are important
when zoom is active, and path/shape tweening should be disabled for large
graphs.

Graphviz's [output documentation](https://graphviz.org/docs/outputs/) confirms
that an explicit DOT `id` attribute becomes the SVG object ID and that
multiedges need distinct external IDs. [xdot](https://graphviz.org/docs/outputs/canon/)
and [Graphviz JSON](https://graphviz.org/docs/outputs/json/) can carry complete
layout/drawing operations, but adopting either would duplicate the working
browser-side DOT layout contract. Weave therefore retains deterministic DOT as
the presentation input and adds stable mappings for both nodes and collapsed
visual edges.

Adopted:

- pinned embedded D3, d3-graphviz, and Graphviz WASM already shipped by Weave;
- stable semantic node IDs and stable collapsed-edge IDs;
- named transitions, reduced-motion support, a large-view no-tween fallback,
  worker layout, pan/zoom, and explicit worker teardown when supported; and
- selection separated from navigation so evidence can be inspected before a
  deliberate refocus.

Rejected:

- a general DOT editor or direct editing of derived graph facts;
- a system Graphviz dependency or server-side xdot/JSON layout;
- CDN/runtime downloads, browser-local canonical graph state, and a required
  Node/npm runtime.

## Source-rich navigation

CodeGraph's current [`codegraph_explore` documentation at commit
`222f82b`](https://github.com/colbymchenry/codegraph/blob/222f82b9a57780e4eaf28e46bbbad9cbda2d6666/site/src/content/docs/reference/mcp-server.md)
returns line-numbered source grouped by file together with relationship paths
and impact context. Its [knowledge-graph guide](https://github.com/colbymchenry/codegraph/blob/222f82b9a57780e4eaf28e46bbbad9cbda2d6666/site/src/content/docs/core-concepts/knowledge-graph.md)
also surfaces provenance for synthesized edges. The valuable human-facing
lesson is that a graph node without its source and evidence forces a second
discovery loop.

Weave already has the stricter primitive: `weave.context/v1` uniquely resolves
an exact entity, checks query-time freshness, reports categorical evidence and
repository/worktree provenance, and reads only bounded current Git-visible
regular UTF-8 source whose hash still matches the indexed document. The
explorer should call that application operation. Relationship source ranges
use the same loader and total byte budget; they never become a browser-specific
file endpoint.

## Local mutation security and concurrency

MDN's [local network access guidance](https://developer.mozilla.org/en-US/docs/Web/Security/Defenses/Local_network_access)
describes the cross-origin and DNS-rebinding risk around loopback/private
services. A loopback bind alone is not an authorization protocol. Weave keeps
its random 256-bit path token, exact `Host` allowlist, same-origin and fetch-site
checks, restrictive CSP, no-store responses, exact route allowlist, strict JSON,
bounded bodies, and no remote assets.

Mutations use HTTP POST/PUT/DELETE and accept exact structured requests. They
do not write graph facts. They construct the same `links add|update|remove`
application invocations as the CLI, which in turn use `bridge.Edit`'s
Git-private lock and atomic canonical `.weave/bridges.json` publication.

Long-lived browser state adds one risk the one-shot CLI normally avoids: two
sessions can edit from the same old list. The established solution is
optimistic concurrency. A deterministic digest of the canonical declaration
is returned with every list/mutation response. Browser writes must present
that revision; the application verifies it while holding `bridge.Edit`'s lock.
A mismatch is a typed conflict and changes nothing. Missing configuration has
one deterministic empty revision. CLI callers that omit a revision retain the
existing serialized behavior.

Deletes require an exact link ID, a current revision, and an explicit browser
confirmation. There is no delete-by-endpoint or fuzzy destructive match.

## Accessibility

The W3C [ARIA keyboard-interface guidance](https://www.w3.org/WAI/ARIA/apg/practices/keyboard-interface/)
requires custom graphical controls to implement their own keyboard behavior
and visible focus. The [modal dialog pattern](https://www.w3.org/WAI/ARIA/apg/patterns/dialog-modal/)
requires focus to enter a dialog, remain operable by keyboard, close with
Escape, and return to a sensible control. The [Graphics ARIA
specification](https://www.w3.org/TR/graphics-aria-1.0/) reinforces
device-independent access to interactive SVG.

Adopted:

- generated SVG nodes and edges receive button roles, accessible names,
  visible focus, and Enter/Space selection;
- selection opens ordinary semantic HTML source/provenance content in a live
  inspector, rather than requiring visual tooltip interpretation;
- navigation has an explicit keyboard-operable **Refocus graph** action;
- destructive confirmation uses the native modal `dialog`; and
- source is assigned through `textContent`, never interpreted as HTML.

