# ADR 0004: Native Go semantic provider

## Status

Accepted.

## Context

Weave needs compiler-accurate Go facts, deterministic identities, and package
incrementality without a daemon or private compiler implementation. It must not
claim dynamic relationships it cannot prove.

## Decision

The Go reference provider loads the active repository build with
`go/packages`, obtains definitions, uses, selections, and type relationships
from the resulting single `go/types` universe, and emits one atomic unit per
loaded repository package variant.

Stable semantic identities are repository-qualified package/object paths.
External objects use package-qualified external identities. Local-only objects
may include source anchors, but exported fingerprints never do.

The provider emits exact static calls resolved by `types.Info`. Interface calls
target the declared interface method. It does not run pointer analysis or
invent concrete dynamic targets.

Package input fingerprints combine local content/build context with direct
dependency surface fingerprints. Units are reused only when input, surface,
and inventory digests match; changes consequently propagate through the
reverse dependency graph. Package loading can do extra work, but persisted
replacement is incremental and never knowingly stale.

The active Go environment is a named variant. Weave passes `-mod=readonly`,
indexes ordinary test variants, and treats load/type errors as a failed refresh
rather than publishing partial facts.

## Consequences

- Go syntax and name resolution are not reimplemented.
- Workspace, module, test, and build-constraint behavior agrees with the Go
  command selected by the user.
- Exact definitions, references, imports, methods, implementations, and direct
  calls are available without SSA.
- Uninstantiated generic declarations and uses are indexed, but implementation
  edges for uninstantiated generic named types are omitted because `go/types`
  does not specify that predicate.
- Public API changes conservatively replace dependent units; implementation
  changes normally replace only their unit.
- Initial provider refresh still runs one package metadata/type load after a
  Git-visible change. Persistent parse/type caches are a future optimization.
- Missing dependencies may require a populated module cache; no restore or
  source mutation is initiated explicitly by Weave.
