# Semantic indexing prior art

Research date: 2026-08-06

## Question

What is the smallest dependable foundation for a cross-language semantic index
that uses compiler-native truth, interoperates with existing indexers, and can
later refresh compilation units incrementally?

## Recommendation

Use two deliberately separate boundaries:

1. **SCIP ingestion** for existing batch indexers and portable code-navigation
   facts.
2. **A narrow Weave adapter process protocol** for native providers that must
   also report compilation-unit identity, dependencies, fingerprints,
   diagnostics, and evidence.

Do not invent a universal parser and do not make language servers the durable
index format. For the first implementation:

- ingest SCIP protobuf directly;
- use `scip-go` and `scip-dotnet` as executable baselines and conformance
  oracles;
- build the native Go adapter on `go/packages` and `go/types` when Weave needs
  incremental-unit facts that SCIP does not carry;
- build F# support as a focused .NET adapter over
  `FSharp.Compiler.Service` (FCS), because Roslyn does not provide F# semantic
  analysis;
- keep the experimental adapter protocol one-shot and newline-delimited JSON;
  add a framed protobuf/persistent mode only after measurements show process
  startup or JSON dominates indexing time.

SCIP should be retained verbatim as provider identity where possible, but a
SCIP symbol string should not automatically become Weave's database primary
key. Weave must qualify workspace/local identities with repository, document,
build-variant, and provider domains as appropriate.

## Findings by area

| Area | Adopt | Do not adopt unchanged | Weave work still required |
| --- | --- | --- | --- |
| Semantic interchange | SCIP protobuf, symbol grammar, occurrences, position encodings, relationships | LSIF's opaque vertex/edge graph | ingestion validation, evidence normalization, internal identity domains |
| Go | `go/packages`, `go/types`, selected `x/tools`; `scip-go` fixtures and behavior; gopls's per-package persistent-index pattern | gopls internal packages; whole-program pointer identity | compilation units, calls policy, public API and semantic-input fingerprints |
| C#/VB | Roslyn Workspaces; initially execute/import `scip-dotnet` | automatic restore; implicit selection of a target framework; F# through Roslyn | explicit variants, offline policy, incremental adapter if batch SCIP is insufficient |
| F# | FCS project checks, symbol uses, project references, caches | Roslyn; file-independent invalidation | focused adapter, stable symbol composition, ordered-file invalidation |
| Process protocol | stdin/stdout separation, request IDs and bounded frames inspired by LSP/Bazel workers | full LSP surface or a daemon requirement | small versioned request/fact/diagnostic lifecycle |
| Incrementality | content-addressed inputs, compilation-unit boundaries, ABI/public-surface comparison, conservative propagation | a language-neutral definition of “public API” | provider-owned canonical fingerprints and core-owned dependency propagation |

Detailed notes:

- [SCIP, LSIF, and identity](scip-lsif-and-identity.md)
- [Go compiler-native indexing](go-native-indexing.md)
- [.NET and F# indexing](dotnet-and-fsharp-indexing.md)
- [Adapter protocols and semantic fingerprints](adapter-protocol-and-fingerprints.md)
- [Reviewed sources and revisions](sources.md)

The resulting decision is recorded in
[ADR 0001](../../decisions/0001-semantic-interchange-and-adapters.md).

## License and packaging inventory

The repository activity column is only a point-in-time health signal observed
on the research date, not a stability guarantee.

| Component | License | Observed state | Packaging/maintenance risk |
| --- | --- | --- | --- |
| [SCIP](https://github.com/scip-code/scip) | [Apache-2.0](https://github.com/scip-code/scip/blob/main/LICENSE) | active, not archived | schema/bindings must be pinned; large protobuf indexes need streaming-aware ingestion |
| [LSIF Node](https://github.com/microsoft/lsif-node) | [MIT](https://github.com/microsoft/lsif-node/blob/main/LICENSE) | not archived; historical implementation | substantially more graph machinery than Weave needs; retain only as compatibility research |
| [`golang.org/x/tools`](https://github.com/golang/tools) | [BSD-3-Clause](https://github.com/golang/tools/blob/master/LICENSE) | active, pre-v1 module releases | public APIs are usable; gopls implementation packages are `internal` and cannot be imported |
| [`scip-go`](https://github.com/scip-code/scip-go) | [Apache-2.0](https://github.com/scip-code/scip-go/blob/main/LICENSE) | active, not archived | CLI-oriented, batch SCIP output; useful executable and oracle rather than a stable library API |
| [Roslyn](https://github.com/dotnet/roslyn) | [MIT](https://github.com/dotnet/roslyn/blob/main/License.txt) | active, not archived | .NET SDK/MSBuild selection and project evaluation are material runtime dependencies |
| [`scip-dotnet`](https://github.com/sourcegraph/scip-dotnet) | [Apache-2.0](https://github.com/sourcegraph/scip-dotnet/blob/main/LICENSE) | active, not archived | executable restores by default; C#/VB only; multi-target behavior needs explicit validation |
| [F# compiler and FCS](https://github.com/dotnet/fsharp) | [MIT](https://github.com/dotnet/fsharp/blob/main/License.txt) | active, not archived | FCS explicitly warns that its API can change; pin SDK/package and contract-test fixtures |

## Research method

Claims here prefer specifications, compiler/library documentation, source
repositories, and current source inspection. Product comparison posts were not
used as the basis for API or licensing decisions. Exact versions must be
recorded in the implementation dependency lock files when adopted. The exact
revisions inspected for this review are in [sources.md](sources.md).
