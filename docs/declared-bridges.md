# Declared semantic bridges

Compilers cannot prove every relationship across repositories, languages,
schemas, generated clients, and documentation. Put those exact relationships
in `.weave/bridges.json`:

```json
{
  "schema": "weave.bridges/v1",
  "links": [
    {
      "id": "openapi-generates-go-client",
      "from": "symbol:scip openapi example.com/contracts 1 BillingAPI#",
      "to": "symbol:scip go example.com/client 1 BillingClient#",
      "kind": "generates"
    },
    {
      "id": "service-depends-on-contract",
      "from": "symbol:scip go example.com/service 1 BillingService#",
      "to": "symbol:scip openapi example.com/contracts 1 BillingAPI#",
      "kind": "depends-on"
    }
  ]
}
```

The endpoint grammar is `symbol:<exact-symbol-id>`. The suffix must be the
complete ID emitted by `weave export --json`; it is never treated as a display
name, search term, pattern, or repository-relative shorthand. This makes the
same edge join a target indexed in another catalog repository without guessing.

Version one accepts `depends-on`, `documents`, and `generates`. Generates edges
carry `generated` evidence; the others carry `declared` evidence. IDs must be
unique within the file. Unknown fields, kinds, schemas, malformed endpoints,
oversized files, and duplicate IDs stop refresh rather than being ignored.

Bridge facts refresh automatically with the Git worktree and live in
Git-local derived storage with compiler facts. Consequently `dependencies`,
`path`, `impact`, `export`, `verify`, catalog-scoped traversal, and architecture
rules all see the same ordinary graph edge. Architecture symbol layers can
match either endpoint even when the target definition is only present in a
different catalog member:

```json
{
  "id": "contracts",
  "symbols": ["scip openapi example.com/contracts 1 *"]
}
```

Do not declare a bridge merely because two names look similar. A bridge is a
reviewed source fact: build metadata, generator configuration, a schema binding,
or another explicit ownership decision should justify it.

