# Contextual linking prior art

This note records the design inputs for Weave's authored relationship SDK and
CLI. The goal is not to import another graph vocabulary. It is to preserve the
small useful contracts those systems have already established.

## RDF: resources and directed statements

[RDF 1.1 Concepts](https://www.w3.org/TR/rdf-concepts/) models a statement as
subject, predicate, and object. Its important lesson for Weave is that either
endpoint can denote any resource: code, a document, an abstract concept, a URL,
or something else. Endpoint identity is independent of whether a particular
graph currently has a materialized description for that resource.

Weave already has the equivalent normalized shape in `graph.Edge` (`From`,
`Kind`, `To`). Contextual links should use that shape directly rather than
introducing a document-only relation table. Unlike RDF, Weave retains a small
closed predicate vocabulary so traversal, policy, and rendering stay
predictable.

## SCIP: producers emit one normalized fact contract

[SCIP](https://github.com/sourcegraph/scip) separates language-specific indexers
from a language-neutral code-intelligence schema and provides bindings and
utilities for producers. Weave follows the same producer/core boundary for
compiled languages.

The corresponding lesson for authored links is that built-in providers and the
CLI should construct the same normalized relationship fact. A CLI-only sidecar
that queries cannot traverse would fragment the model. A small builder should
own relationship validation and provenance defaults, while provider-specific
code continues to own endpoint discovery and evidence quality.

## LSP document links: origin context is optional

The [Language Server Protocol](https://microsoft.github.io/language-server-protocol/specifications/specification-current)
models document links separately from code definitions and permits a source
range and tooltip while the target remains a URI. This supports two useful
concessions:

- a relationship may have source context, but source context is not required;
- the target need not be a compiler symbol or even be locally materialized.

Weave already represents optional source context as `DocumentID` plus `Range`.
Authored links have no trustworthy source range by default, so they should not
invent one. A human note belongs in the declaration and authoring output; the
normalized graph edge remains the compact query primitive.

## Git notes: annotations and source history are different concerns

[`git notes`](https://git-scm.com/docs/git-notes) can attach arbitrary content
to Git objects without changing the annotated object, but notes live on a
separate ref and require explicit propagation. That is useful prior art for
local annotations but a poor default source of project truth.

Weave declarations remain ordinary reviewable worktree files. The generated
database remains under Git's private worktree storage. This keeps authored
intent in normal branch history while derived edges remain disposable.

## Adopted contract

- One relationship is an identified directed triple plus evidence and
  provenance.
- Endpoints are exact graph entity IDs, not necessarily code symbols.
- Human queries are resolved at authoring time; exact IDs are persisted.
- `id:<value>` permits an intentional open endpoint that is not materialized in
  the selected indexes yet.
- Built-in producers and authored-link ingestion use the same relationship
  builder and `graph.Edge` validation.
- The CLI exposes add, update, remove, and list operations and supports bounded
  catalog resolution for cross-repository links.
- Authored declarations use declared evidence, except `generates`, which uses
  generated evidence. They cannot claim compiler-exact evidence.
- All existing edge kinds are available; relationship semantics do not change
  merely because an endpoint is a document, section, route, asset, or URL.
