# ADR 0006: Compiler-native .NET adapter

- Status: Proposed
- Date: 2026-08-06
- Extends: [ADR 0001](0001-semantic-interchange-and-adapters.md)
- Research: [compiler-native .NET adapters](../prior-art/dotnet-semantic-adapters/README.md)

## Context

SCIP ingestion provides portable navigation facts, but the project lifecycle,
explicit variants, fingerprints, F# semantics, and compiler-resolved call edges
needed by Weave require a native adapter. The adapter must remain optional: Go
queries cannot execute MSBuild, restore dependencies, or discover tools as a
side effect.

## Decision

1. `adapters/dotnet` is a separately built .NET tool implementing the existing
   one-shot `weave.adapter/v0` process contract. The Go core has no Roslyn/FCS
   dependency.
2. C# facts come from Roslyn `MSBuildWorkspace`, `Compilation`, `SemanticModel`,
   `ISymbol`, and `IOperation`. F# facts come from `FSharp.Compiler.Service`.
   Weave will not parse either language itself.
3. Microsoft.Build.Locator registers the installed SDK before project loading.
   Project evaluation requires the request's `build_tool` permission. Restore,
   networking, and generator execution remain denied unless explicitly allowed;
   the first adapter never initiates restore.
4. Each evaluated project/build variant is one complete atomic unit. C# units
   preserve distinct workspace project variants. F# units preserve ordered
   source files. Project references are explicit `depends-on` edges.
5. Provider identities use a versioned canonical composition, not Roslyn
   `SymbolKey` serialization or FCS object identity. Exact source spans are
   converted to zero-based UTF-8 byte columns and repository paths are confined
   before facts are emitted.
6. Definitions, references, static C# calls, inheritance, implementation,
   imports, and project dependencies are exact compiler evidence. Unsupported
   semantics are omitted with diagnostics rather than inferred.
7. Input fingerprints hash canonical project identity, options, ordered source
   content, references, and tool versions. Surface fingerprints initially hash
   sorted public/protected symbol signatures; they are a conservative adapter
   ABI summary, not a compiler binary-compatibility guarantee.
8. Output is sorted and framed incrementally under request limits. A failed or
   incomplete project fails the run so the Go core retains the previous atomic
   inventory.
9. Distribution may use a .NET tool package later. Discovery never restores or
   installs it. Explicit CLI execution remains the only indexing path.

## Initial validation boundary

The authoring host does not have `dotnet`, so the initial code cannot truthfully
be reported as locally compiled or executed. GitHub Actions builds and tests the
adapter on supported .NET runners. Go contract fixtures validate capability and
frame behavior without executing .NET. ADR status remains Proposed until CI has
built genuine C#/F# fixture projects and deterministic replay succeeds.

## Deferred deliberately

- F# call edges until a typed-expression visitor is fixture-backed.
- source generators and analyzer execution;
- persistent adapter processes and fine-grained delta refresh;
- a formal stable third-party adapter schema;
- automatic tool installation or restore;
- binary-compatible ABI calculation for every .NET language feature.

