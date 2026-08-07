# Go compiler-native indexing

## Public semantic APIs

[`go/packages`](https://pkg.go.dev/golang.org/x/tools/go/packages) is the right
workspace loader. It accounts for the Go command's package model and supports
an alternate build system through the Go packages driver protocol. Its load
mode can request compiled files, imports/dependencies, modules, syntax,
`go/types` packages, type information, sizes, test variants, embedding data,
and targets.

The package documentation also lists open interactions among `LoadMode` flags.
Weave should use a pinned `x/tools` version and fixture-test the exact load mode
rather than treating every combination as reliable.

[`go/types`](https://pkg.go.dev/go/types) supplies compiler semantics. In
particular, `types.Info.Defs` and `types.Info.Uses` map identifiers to resolved
objects, and selections/instances supply method and generic-instantiation
information. Types returned by separate `packages.Load` calls must not be mixed;
the `go/packages` documentation promises consistency within a load call, not
pointer identity across calls.

[`objectpath`](https://pkg.go.dev/golang.org/x/tools/go/types/objectpath) is
useful but deliberately narrower than a complete symbol identity system. It
encodes certain objects relative to a package so logically corresponding
`types.Object` values can be related across address spaces. Its guarantee is
focused on objects sufficient for a package API: package-level types, exported
package-level non-types, methods, parameters/results, and struct fields. It does
not name predeclared, import, local, or most unexported package-level objects.

Therefore:

- use package path/module coordinates plus a provider-composed descriptor for
  durable package symbols;
- evaluate `objectpath` for public-surface objects;
- use document-scoped identities for locals;
- never persist `types.Object` pointer identity.

## Existing SCIP Go indexer

[`scip-go`](https://github.com/scip-code/scip-go) is active Apache-2.0 prior
art. Its current implementation is based on `go/packages` and `go/types`, emits
SCIP, supports the Go packages driver protocol, indexes tests unless asked not
to, and provides snapshot fixtures. Its CLI can write the index to stdout.

This makes it immediately useful in three roles:

1. an executable batch provider for early SCIP ingestion;
2. a corpus of genuine semantic fixtures and edge cases;
3. a conformance oracle when Weave's native adapter is implemented.

It should not be assumed to be an importable library: its implementation is
mostly under Go `internal` directories. Prefer invoking the released executable
or learning from/reusing Apache-licensed algorithms with clear attribution and
focused tests.

One particularly relevant implementation detail is its implementation-search
fingerprinting. The
[`internal/implementations/fingerprint`](https://github.com/scip-code/scip-go/tree/main/internal/implementations/fingerprint)
package is synchronized from a gopls internal package and builds canonical
method-signature fingerprints for implementation filtering. This is evidence
that interface implementation matching has reusable specialized prior art; it
is not the same thing as Weave's package public-surface fingerprint.

## gopls as incremental-index prior art

gopls already confronted the scale problem Weave cares about. The official
[scalability account](https://go.dev/blog/gopls-scalability) describes moving
global queries to persistent per-package summaries: declaration types,
cross-reference indexes, and method sets. Query-time algorithms load those
package indexes instead of retaining a fully type-checked whole program.

The current
[gopls implementation design](https://go.dev/gopls/design/implementation)
describes:

- a metadata graph produced from `go/packages`;
- snapshots and overlays;
- dependency analysis and invalidation in the cache layer;
- persistent file-backed indexes for cross-references, method sets, and type
  references.

The older but still useful
[cache invalidation design](https://go.dev/gopls/design/design#cache-invalidation)
notes why file changes map to package invalidation: a file can move between
packages through package declarations or build constraints and can participate
in more than one test/package variant.

Adopt the architecture, not the internals. gopls packages are intentionally
`internal`, its cache is optimized for an interactive language server, and its
storage contract is not Weave's public interchange. Weave should independently
model:

```text
content/config change -> affected package variant -> re-index package
public surface changed -> propagate to reverse dependencies
public surface unchanged -> keep dependents
```

## Initial Go adapter scope

The smallest precise adapter should load real fixture modules and emit:

- module, package, and package-variant identity;
- compiled documents and build constraints selected by `go/packages`;
- definitions and typed references from `types.Info`;
- package imports/dependencies;
- functions, methods, receivers, fields, interfaces, and named types;
- diagnostics and an explicit ill-typed/partial status;
- a canonical semantic-input fingerprint;
- a public-surface fingerprint at package-variant granularity.

Call edges and implementation edges should be separate measured additions.
Direct syntax plus `types.Info` can resolve ordinary static calls, while dynamic
dispatch and whole-program call graphs have different precision/cost. Do not
label a syntactic callee as compiler-exact.

## Packaging and safety

`go/packages` commonly invokes the Go command or an explicit packages driver.
That is less isolated than parsing source. The adapter capability report must
say that it requires the Go toolchain and may invoke project/build discovery.
Network access and dependency download must remain disabled by policy/default
environment where Weave controls them; missing dependencies should produce a
structured partial/unavailable result rather than an implicit network restore.
