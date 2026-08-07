# CI

Weave treats indexes as disposable build state. CI restores `.git/weave` by a
content-derived key, refreshes it if necessary, verifies normalized facts,
checks architecture policy, and optionally uploads deterministic exports and
SARIF. Generated databases are never committed.

```sh
key=$(weave ci key)
weave ci index
weave ci check
weave ci check --format sarif > weave.sarif
weave export --json > weave-export.json
```

`ci key` includes repository identity, committed tree, dirty-overlay hashes,
graph schema, semantic provider/version, architecture configuration digest, and
OS/architecture. Its JSON form uses `weave.ci/v1`. `ci index` is silent on
success and reuses a current restored index. `ci check` composes graph
verification and architecture checking; policy or integrity failures exit 3.
Unmaterialized external or builtin occurrence targets remain visible as
warnings but are not structural failures. Orphaned records and invalid
unit/document ownership remain fatal. Text, JSON, and SARIF retain these
diagnostics; SARIF uses warning/error levels to preserve the distinction.

See [the GitHub Actions workflow](../.github/workflows/weave.yml) for a complete
example. Pin third-party actions to reviewed commit SHAs when adopting the
example in a protected production repository.
