# ADR 0012: Contextual relationship authoring

- Status: Accepted
- Date: 2026-08-07
- Research: [contextual linking prior art](../prior-art/contextual-linking/README.md)

## Context

Weave's compiler, SCIP, workspace, and bridge providers already emit the same
normalized directed edges. The initial checked-in bridge slice was intentionally
narrow: hand-edited exact symbol IDs and three relationship kinds. It did not
offer a safe authoring workflow, could not resolve catalog resources, and its
terminology implied that endpoints had to be code symbols. Agents therefore had
to export the database, copy opaque IDs, and edit JSON correctly before they
could connect a project document to its implementation or website.

A second annotation store would make authored context invisible to normal path,
impact, DOT, policy, and federation operations. Resolving fuzzy queries every
time the graph refreshes would make declarations change meaning as repositories
evolve.

## Decision

Treat authored context as another producer of ordinary `graph.Edge` facts.
Add one internal relationship builder that validates identity, provenance,
evidence, optional source location, and the closed edge vocabulary. Refactor the
built-in Go, SCIP, workspace/content, and bridge producers to construct edges
through it. Language-specific analysis still decides which endpoints exist and
which evidence it can claim.

Add `weave links add|update|remove|list` (`weave link` alias). Add and update:

1. refresh and query the selected local or bounded catalog indexes;
2. require each human query to resolve uniquely;
3. persist the resulting exact graph IDs in `.weave/bridges.json`;
4. serialize source edits with a Git-private lock and atomically publish a
   canonical declaration file; and
5. refresh the current repository so the authored relationship is immediately
   available as an ordinary edge.

Use `entity:<exact-graph-id>` for new declarations while retaining the legacy
`symbol:` reader. The entity may be a compiler symbol, package, file, document,
section, route, asset, URL, or any other normalized graph resource. Permit an
explicit CLI-only `id:<exact-id>` form for intentional open endpoints that are
not materialized yet. Never interpret it as a fuzzy query.

Allow every normalized edge kind. Authored facts use declared evidence, except
generation relationships, which use generated evidence. Notes remain in the
reviewable declaration and authoring response; compact graph edges do not copy
free-form prose.

## Consequences

Positive:

- agents and humans can author cross-language and cross-repository context
  without manipulating opaque JSON directly;
- documents and code use one relationship contract and every existing graph
  consumer sees the result;
- persisted exact IDs make refresh deterministic and reviewable;
- unique resolution fails closed instead of silently selecting a fuzzy match;
- open endpoints preserve forward references and immutable external identity;
  and
- built-in producers exercise the same builder contract that authored links
  depend on.

Costs and limitations:

- `.weave/bridges.json` retains its historical filename and schema spelling for
  compatibility even though its endpoint vocabulary is now heterogeneous;
- free-form notes are visible through `links list` and source review, not graph
  traversal output;
- an exact graph ID can become unmaterialized after a source rename, but the
  edge remains explicit and inspectable rather than being retargeted; and
- a small Git-private bbolt lock file remains after the first authoring command
  so concurrent processes can serialize read-modify-write operations without
  putting lock artifacts in the worktree.
