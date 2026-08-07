# ADR 0015: Source-rich explorer and guarded link authoring

- Status: Accepted
- Date: 2026-08-07
- Research: [source-rich local graph explorer prior art](../prior-art/source-rich-explorer/README.md)

## Context

The initial loopback explorer could navigate and animate a bounded DOT graph,
but a node or edge did not expose the source and provenance that justified it.
Authored contextual links were available only through the CLI. Adding direct
browser graph edits, an unrestricted file endpoint, or a second declaration
writer would split Weave's semantics and create avoidable local-service risk.

An interactive browser also remains open longer than a CLI invocation. Two
sessions can legitimately load the same link list and then race to update it;
the existing writer lock serializes writes but cannot identify an edit based on
stale human state by itself.

## Decision

The explorer remains a presentation adapter over application operations:

- graph snapshots use the existing bounded `graph` invocation and DOT writer;
- node and edge inspection uses the existing bounded `context` invocation and
  its current-source safety checks;
- link list/create/update/remove use the same `links` application invocations
  as the CLI; and
- a successful link mutation refreshes and requeries the graph, so the existing
  stable-ID d3-graphviz renderer animates the canonical result.

Project every collapsed visual edge back to all normalized source facts under
one deterministic SVG ID. Selecting a node or edge opens source range,
provider, evidence, document, repository, worktree, and bounded current source
where available. Relationship ranges are hydrated through the same
`contextquery` source loader and shared byte budget. There is no arbitrary path
or raw-file HTTP endpoint.

Add an order-independent SHA-256 revision for the validated canonical bridge
declaration. Missing configuration hashes the canonical empty v1 document.
Browser mutations must provide a valid revision. The application verifies it
inside `bridge.Edit`'s existing lock before changing the in-memory declaration;
a mismatch wraps the exported `ErrLinkRevision` sentinel and maps structurally
to HTTP 409. CLI operations without a revision remain backward compatible.
The browser pins that revision to an editor or removal confirmation when it is
opened. Reloading canonical state after a conflict must not silently rebase a
stale form; the user must cancel and explicitly reopen it against the new state.
The mutation response names its operation and one affected declaration. Only
the list response claims to represent canonical authored-link state, which the
browser reloads explicitly after each mutation or conflict.

The server retains its random token, exact loopback host, origin/fetch-site
checks, route allowlist, strict bounded JSON, no-store/CSP headers, and embedded
offline assets. Mutations use POST, PUT, and DELETE. Removal names one exact
link, carries the current revision, and requires an explicit native-dialog
confirmation in the browser.

Selection and navigation are separate. SVG nodes and edges are keyboard
selectable; an explicit **Refocus graph** button provides keyboard navigation.
Double-click is only a pointer shortcut. Source and declaration text enter the
DOM only as text.

## Consequences

Positive:

- humans can move from topology to exact current evidence without leaving the
  bounded explorer;
- non-code documents, headings, routes, assets, generated files, and compiler
  symbols use the same inspector;
- browser authoring cannot diverge from CLI/provider behavior;
- stale link editors fail without overwriting a newer canonical declaration;
  and
- authored graph changes animate without another graph or diff engine.

Costs and limits:

- edge inspection is bounded by the context relationship limit and fails
  honestly when the selected edge is no longer present;
- the revision protects authored-link concurrency, not unrelated source edits;
  exact endpoint identity prevents silent retargeting when source changes;
- browser layout still has practical size limits, so expensive tweening is
  disabled above the measured node threshold; and
- this is a focused semantic authoring surface, not a general graph editor.
