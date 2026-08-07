# Authored contextual relationships

Compilers and document parsers cannot prove every useful relationship across
repositories, languages, schemas, generated clients, implementation files, and
explanatory content. `weave links` authors those reviewed relationships:

```sh
weave links add guide-documents-handler \
  --from 'docs/guide.md#request-flow' \
  --to 'HandleRequest' \
  --kind documents \
  --note 'The guide explains this request entry point.'

weave links add website-documents-project \
  --from '/projects/go-modular-monolith/' \
  --to 'README.md' \
  --kind documents \
  --scope catalog \
  --repo github.com/TheFellow/TheFellow.github.io \
  --repo github.com/TheFellow/go-modular-monolith

weave links update guide-documents-handler --to 'id:git-commit:0123456789abcdef'
weave links list
weave links remove guide-documents-handler
```

`link` is an alias for `links`; `rm` is an alias for `remove`. Add and update
accept the normal catalog flags, so either endpoint can resolve in another
registered worktree. A query must resolve uniquely. If it does not, inspect it
with `weave symbols QUERY` or `weave workspace find QUERY`, then pass the exact
graph ID.

Resolution happens once while authoring. Weave persists exact endpoints in
`.weave/bridges.json`, refreshes them into the ordinary graph, and returns the
stored declaration. The link therefore remains deterministic when a display
name changes:

```json
{
  "schema": "weave.bridges/v1",
  "links": [
    {
      "id": "guide-documents-handler",
      "from": "entity:workspace-section:...",
      "to": "entity:scip go example.com/service 1 HandleRequest().",
      "kind": "documents",
      "note": "The guide explains this request entry point."
    }
  ]
}
```

`entity:<exact-graph-id>` is the canonical endpoint grammar. The older
`symbol:<exact-symbol-id>` spelling remains readable. Endpoints are graph
entities, not necessarily compiler symbols: files, Markdown documents,
headings, routes, assets, URLs, packages, and code declarations all use the
same contract. `id:<exact-id>` on the CLI intentionally creates an open
endpoint without requiring it to exist in the selected indexes; this is useful
for immutable commits, external resources, or producer facts that will arrive
later. It is not a fuzzy lookup escape hatch.

Every normalized edge kind accepted by `weave graph --kind` is authorable.
Authored relationships carry `declared` evidence, except `generates`, which
carries `generated` evidence. The CLI cannot claim compiler-exact evidence.
Notes remain review context in the declaration and `links` output; they are not
duplicated into every compact graph edge.

Bridge facts refresh automatically with the Git worktree and live in
Git-private derived storage with compiler and content facts. Consequently
`graph`, `path`, `impact`, `export`, `verify`, catalog traversal, and
architecture rules see the same ordinary edge. The built-in Go, SCIP,
workspace, and authored-link providers construct edges through the same
normalized relationship builder, so the CLI is an authoring surface over the
core fact contract rather than a separate annotation database.

The file is strict and bounded. IDs must be unique, notes are limited to 8 KiB,
the file is limited to 1 MiB and 4,096 links, and unknown fields, kinds,
schemas, malformed endpoints, symlinked destination files, and duplicates are
errors. Concurrent commands serialize through a Git-private lock; writes are
canonical and atomic. The `weave graph --interactive` inspector delegates its
create/update/remove controls to these same application operations. Because a
browser session may remain open while another process edits the file, every
browser mutation carries a deterministic revision of the canonical declaration
and verifies it while holding that writer lock. A stale session receives a
conflict and changes nothing; removing a link additionally requires an exact ID
and explicit confirmation. CLI commands remain one-shot and do not expose the
revision flag. Do not add a relationship merely because two names look similar:
the declaration is reviewed source truth and should have a defensible reason.
