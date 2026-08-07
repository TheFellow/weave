# .NET and F# compiler-native indexing

## Roslyn for C# and Visual Basic

[Roslyn](https://github.com/dotnet/roslyn) is the MIT-licensed compiler platform
for C# and Visual Basic. Its
[Workspace model](https://learn.microsoft.com/en-us/dotnet/csharp/roslyn-sdk/work-with-workspace)
represents solutions, projects, documents, parse/compilation options,
references, syntax trees, semantic models, and compilations. Solutions are
immutable snapshots; projects supply compilations with project dependencies and
options already represented.

The
[semantic API](https://learn.microsoft.com/en-us/dotnet/csharp/roslyn-sdk/get-started/semantic-analysis)
binds syntax to `ISymbol` values inside a `Compilation`. This is the correct
source for overload resolution, imported metadata, type/member identity, and
diagnostics. A syntactic C# parser cannot replace that compilation context.

For Weave, `MSBuildWorkspace` is the likely project-loading boundary, but its
behavior depends on the selected .NET SDK, MSBuild, target frameworks,
properties, generated sources, analyzers, and restored references. Those inputs
must be part of the build-variant and semantic-input fingerprints.

## Existing `scip-dotnet`

[`scip-dotnet`](https://github.com/sourcegraph/scip-dotnet) is active,
Apache-2.0, distributed as a .NET tool and container, and emits SCIP for C# and
Visual Basic. Current source inspection shows it uses `MSBuildWorkspace` and
Roslyn semantic models. Its snapshot tests are valuable fixtures and its
published executable is a pragmatic first batch provider.

Important constraints visible in the current source and
[README](https://github.com/sourcegraph/scip-dotnet/blob/main/readme.md):

- it supports C# and Visual Basic, not F#;
- it invokes `dotnet restore` by default, with a skip option;
- it loads `.csproj`/`.vbproj` or solutions through `MSBuildWorkspace`;
- when a project appears for several target frameworks, current code selects a
  framework matching the running .NET major version or falls back to the first;
- it emits a batch `index.scip`, not Weave compilation-unit lifecycle facts;
- its project currently targets several .NET runtimes and pins Roslyn/MSBuild
  NuGet dependencies.

Weave must not silently inherit restore or target-framework policy. Initial
integration should:

1. discover the executable and version;
2. run with restore disabled by default;
3. record the selected SDK, project, framework, configuration, and arguments;
4. surface workspace failures as partial/unavailable diagnostics;
5. ingest the emitted SCIP without claiming incremental refresh;
6. use small multi-target fixtures to decide whether a Roslyn adapter is needed
   for explicit variant control and incremental fingerprints.

## F# is a separate semantic provider

F# compiler semantics come from
[`FSharp.Compiler.Service`](https://github.com/dotnet/fsharp), not Roslyn. FCS
is part of the MIT-licensed F# compiler repository and contains the compiler
logic used by F# tooling.

The official
[project-analysis tutorial](https://fsharp.github.io/fsharp-compiler-docs/fcs/project.html)
demonstrates the required surface:

- `FSharpChecker.ParseAndCheckProject` for project-wide results;
- `FSharpCheckProjectResults.GetAllUsesOfAllSymbols` and
  `GetUsesOfSymbol` for resolved uses;
- background per-file results after project checking;
- `ParseAndCheckFileInProject` for updated file contents;
- `ProjectReferences` for incremental analysis of referenced F# projects.

The same documentation explicitly warns that the FCS API is subject to change
between package versions. Pin the NuGet package/.NET SDK and keep the adapter
behind Weave's process protocol so API churn does not enter the Go core.

FCS's
[cache design](https://fsharp.github.io/fsharp-compiler-docs/fcs/caches.html)
keeps project incremental builders and per-file parse/check results, and
recommends one shared `FSharpChecker` for IDE-style use. This makes a future
persistent adapter mode plausible, but does not justify requiring a daemon in
the first release.

The
[project-build design](https://fsharp.github.io/fsharp-compiler-docs/project-builds.html)
also warns that project checks are stateful, use on-disk assembly inputs, and do
not share typed-tree nodes between project compilations. Cross-project results
must be treated as serialized semantic inputs, not reusable in-process symbol
objects.

## Ordered invalidation in F#

F# project source order is semantic. Signature files must precede their
implementation files, as documented by
[Microsoft's F# signature-file reference](https://learn.microsoft.com/en-us/dotnet/fsharp/language-reference/signature-files).
More generally, later files can consume declarations from earlier files.

Consequences for Weave:

- the ordered `SourceFiles` list is part of the project/build-variant identity;
- changing/reordering an earlier file conservatively invalidates later files in
  that project unless FCS exposes a stronger reusable result;
- a project public-surface change invalidates dependent projects;
- FCS results and symbols are normalized before crossing the adapter boundary;
- type providers and required on-disk reference assemblies must be diagnosed as
  special build inputs, not hidden.

## F# symbol identity spike

SCIP contains symbol kinds for F# concepts, but there is no maintained F# SCIP
producer identified in this review. The initial adapter should not promise a
perfect stable identity before fixtures prove it.

Use compiler-resolved `FSharpSymbol` values within one check, then compose
provider symbols from assembly/project identity, logical enclosing entities,
compiled name, symbol kind, generic arity, and signature/disambiguation where
necessary. Locals remain document-scoped. Test at least:

- modules and namespaces;
- functions with curried and tupled forms;
- overloads and members;
- records, fields, discriminated unions, and union cases;
- active patterns, operators, and generic parameters;
- `.fsi`/`.fs` pairs;
- ordered multi-file projects;
- C# references consumed from F# and F# assemblies consumed from C#.

This composition is original adapter work and must be versioned as provider
identity, not asserted to be an existing FCS persistence guarantee.
