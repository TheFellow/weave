# Go semantic indexing prior art

## Scope

This research covers the native Go provider: package loading, compiler-resolved
identities and occurrences, direct calls, interface satisfaction, variants,
fingerprints, and conservative incremental invalidation.

## Findings

### Let the Go command define the build

[`go/packages`](https://pkg.go.dev/golang.org/x/tools/go/packages) is the
supported high-level loader for tools that need syntax and types while obeying
modules, workspaces, build constraints, generated file selection, tests, and
the current `GOOS`/`GOARCH`. Its `NeedName`, `NeedFiles`,
`NeedCompiledGoFiles`, `NeedImports`, `NeedTypes`, `NeedSyntax`,
`NeedTypesInfo`, `NeedModule`, and `NeedForTest` fields supply the facts Weave
needs. A single `Load` result is important: the `go/types` documentation warns
that type identity predicates require one consistent universe of package
objects.

The loader invokes the build-system query tool. Weave therefore sets
`GOWORK=off` only when explicitly configured (not by default), preserves the
user's selected build environment, and uses `-mod=readonly` so indexing cannot
rewrite `go.mod` or `go.sum`. This is offline-conscious, not an offline
guarantee: a missing dependency may still prompt the Go command to consult its
configured proxy. Failed loads are returned as diagnostics/errors and never
published as a complete index.

Tests are loaded because they are useful impact targets. Synthetic test-main
packages are excluded, while ordinary, in-package test variants and external
test packages are retained as distinct compilation units.

### Compiler objects are the semantic truth

[`go/types.Info`](https://pkg.go.dev/go/types) maps identifier definitions and
uses to `types.Object` values. It also records selector resolution and
instantiations. Weave derives identities from semantic ownership rather than
source offsets:

```text
go:<repository-identity>:<package-path>:<object-path>
```

Package-scope objects use their declared name. Methods include the origin
receiver type and method name. Fields include their owning named type and
field name. Parameters, results, local variables, labels, and anonymous
functions include their owning function plus a source anchor; those are less
stable under edits and are deliberately not part of the public fingerprint.
External objects use `go:external:<package-path>:<object-path>` so references
and calls remain queryable before another repository is federated.

The Go specification defines exported identifiers and method sets; `go/types`
implements those rules. `types.NewMethodSet`, `types.Implements`, and
`types.Satisfies` avoid reimplementing embedding, pointer receiver, aliases,
and generic constraint rules. The initial provider evaluates satisfaction
between named concrete types and named interfaces in the loaded repository.
This is quadratic in declared types and intentionally bounded to a package
load; a serialized method-set index like gopls uses is a later optimization.

### Calls: exact static targets first

[`go/ssa`](https://pkg.go.dev/golang.org/x/tools/go/ssa) exposes
`CallCommon.StaticCallee`, but building SSA and a whole-program call graph adds
cost and introduces policy choices for dynamic dispatch. For the first native
provider, AST call expressions plus `types.Info` resolve package functions,
methods, method expressions, built-ins, and interface method selections. A
call edge is emitted only when the invoked expression resolves to a
`*types.Func`; interface invocation therefore targets the exact interface
method symbol, not guessed concrete implementations. Calls through function
values and closures are deferred. This is compiler-resolved, bounded, and
honest about what is absent.

### Public surfaces are canonical compiler renderings

The package public-surface fingerprint is a sorted stream of exported
package-scope declarations plus exported methods and exported fields reachable
from exported named types. Types and signatures use `types.TypeString` with a
qualifier that emits package paths, so import aliases and formatting do not
affect the digest. Constants include exact values. This follows the broad
approach of Go export data and API tools without adopting an unstable internal
encoding.

The full input fingerprint covers the selected package ID/variant, toolchain,
build environment, module/workspace manifests, compiled file paths and content,
and direct dependency surface fingerprints. It is deliberately conservative.

### Incrementality follows compilation units and reverse dependencies

[gopls' implementation](https://go.googlesource.com/tools/+/master/gopls/doc/design/implementation.md)
separates local package inputs from dependency-derived inputs and revalidates
reverse dependencies when dependency keys change. Its current package-handle
design describes "precise pruning": local edits invalidate the package, while
dependent package keys are recomputed bottom-up and may be reused when relevant
dependency state is unchanged.

Weave's durable first implementation uses the same correctness boundary with
less machinery:

1. Load the selected repository package graph once.
2. Compute each unit's local input and public surface.
3. Compute its full input from local input plus direct dependency surfaces.
4. Reuse a previous unit only when all persisted fingerprints match.
5. Replace changed units and remove disappeared variants atomically.

This naturally limits implementation-only changes to their package while a
public API change changes dependent input fingerprints and propagates through
the reverse dependency closure. A build manifest, workspace, toolchain, build
variant, or package-load topology change conservatively re-evaluates all
selected units. Git's exact dirty inventory remains the cheap gate that decides
whether the provider runs at all. RIBLT is not useful here: one local loader
already has exact package and fingerprint inventories.

## Adopted and deferred

Adopted:

- `golang.org/x/tools/go/packages` and `go/types` as semantic authorities.
- Compilation-package variants as atomic units.
- Exact typed definitions, references, selections, imports, static calls, and
  named-type/interface satisfaction.
- Sorted SHA-256 fingerprints over canonical records.
- Conservative reverse-dependency invalidation through dependency surfaces.

Deferred with explicit boundaries:

- Whole-program pointer analysis and guessed dynamic call targets.
- Implementation edges for uninstantiated generic named types; `go/types`
  explicitly leaves `Implements` behavior unspecified for those types.
- Serialized gopls internal indexes; they are excellent prior art but internal
  APIs and unnecessary for the first durable implementation.
- Alternate build variants beyond the active environment and normal tests.
- Cross-repository implementation matching.
- RIBLT until an independently maintained inventory exists and benchmarks show
  a win over exact sets.

## Authoritative and OSS sources

- [Go packages API](https://pkg.go.dev/golang.org/x/tools/go/packages)
- [Go types API](https://pkg.go.dev/go/types)
- [Go SSA API](https://pkg.go.dev/golang.org/x/tools/go/ssa)
- [Go language specification: declarations and scope](https://go.dev/ref/spec#Declarations_and_scope)
- [Go language specification: method sets](https://go.dev/ref/spec#Method_sets)
- [Go build constraint documentation](https://pkg.go.dev/cmd/go#hdr-Build_constraints)
- [Go module reference](https://go.dev/ref/mod)
- [Go workspace tutorial](https://go.dev/doc/tutorial/workspaces)
- [gopls architecture and caches](https://go.googlesource.com/tools/+/master/gopls/doc/design/implementation.md)
- [gopls package-handle precise pruning](https://go.googlesource.com/tools/+/master/gopls/internal/cache/check.go)
- [SCIP Go indexer](https://github.com/sourcegraph/scip-go)
