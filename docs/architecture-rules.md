# Architecture rules

Weave reads `.weave/architecture.json` by default. The checked-in file contains
declarations only; the graph database remains derived local state.

```json
{
  "schema": "weave.architecture/v1",
  "layers": [
    { "id": "api", "paths": ["internal/api/**"] },
    { "id": "contracts", "units": ["example.com/service/contracts"] },
    { "id": "storage", "symbols": ["example.com/service/storage.*"] }
  ],
  "rules": [
    {
      "id": "api-no-storage",
      "action": "forbid",
      "from": "api",
      "to": "storage",
      "kinds": ["imports", "calls"],
      "message": "API code must use application contracts"
    },
    {
      "id": "api-call-allowlist",
      "action": "allow",
      "from": "api",
      "to": "contracts",
      "kinds": ["calls"]
    }
  ]
}
```

A layer is the union of its `paths`, semantic `units`, and stable `symbols`.
Patterns use Go's portable `path.Match` syntax: `*`, `?`, and character classes
do not cross `/`. Weave additionally supports a final `/**` as a prefix match.
Patterns are repository-relative, use `/`, and may not escape the repository.

A `forbid` rule reports an edge when both layers and an edge kind match. All
applicable `allow` rules for the same source and kind form an OR allowlist: the
target must match at least one target layer. Facts without compiler/provider
evidence are still visible; each violation reports its provider, evidence
class, document, and exact source range when present.

```sh
weave architecture check
weave arch check --format json
weave arch check --format sarif > weave.sarif
```

No configuration means no policy and succeeds silently in text mode. Invalid
configuration is an error. Violations produce deterministic output and exit
code 3. JSON uses `weave.architecture-result/v1`; SARIF uses version 2.1.0.

Initial bounds are 1 MiB of configuration, 256 layers, 256 selectors per
layer, 4,096 rules, and 32 edge kinds per rule.
