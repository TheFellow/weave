# Compiler-native .NET semantic adapters

Research date: 2026-08-06. This review favors compiler APIs and maintained
open-source tools over language parsing implemented by Weave.

## Roslyn is the C# semantic authority

Roslyn's [compiler and workspace overview][roslyn-overview] defines the useful
layers cleanly: an immutable `Solution` contains projects and documents, a
`Compilation` owns the symbol table, and each syntax tree has a `SemanticModel`
that resolves declarations, references, types, diagnostics, and data flow.
Weave should therefore load configured projects with `MSBuildWorkspace`, obtain
the actual `Compilation`, and derive facts from `ISymbol` and `IOperation`.
Syntax-only matching would lose overload resolution, inferred generic types,
extension methods, and metadata/project-reference identity.

`SymbolKey` is useful for resolution within a compatible compilation snapshot,
but is intentionally an internal Roslyn persistence format and is not a public
cross-tool stable identifier. Weave identities should instead be versioned,
deterministic compositions of assembly/project identity plus Roslyn's fully
qualified symbol display (including parameter types and arity). Locals need a
document-qualified identity and declaration span. The original compiler symbol
identity remains an adapter concern, not a database key promise.

Roslyn's operation tree is the narrowest compiler-resolved source for static
call edges. `IInvocationOperation.TargetMethod`, object creation constructors,
and member-reference operations retain binding results. Declarations and
ordinary references can be collected from semantic-model symbol information;
base types and implemented interfaces come directly from named-type symbols.

## Loading is evaluation, not parsing

`MSBuildWorkspace` evaluates SDK projects through the selected installed SDK
and MSBuild. [Microsoft.Build.Locator][locator] is the official mechanism for
registering that toolset before any Microsoft.Build types are loaded. It keeps
the adapter aligned with the user's SDK and avoids shipping a partial MSBuild.
[Buildalyzer 9.0][buildalyzer] is useful prior art for extracting design-time
build results and, unlike Roslyn Workspaces, captures the F# compiler arguments
needed by FCS. The maintained fork targets .NET 6/8, supports SLNX, and depends
on MSBuild 17.14.28 and Roslyn C#/VB 4.0 or newer. It still executes MSBuild and
adds another project-model abstraction, so C# uses Locator plus
`MSBuildWorkspace` directly while only the focused F# loader uses Buildalyzer.
Because its permissive Roslyn floors can otherwise select an incompatible
Visual Basic/Common pair beside Workspaces 4.14, the adapter explicitly aligns
that compiler package family rather than suppressing NuGet constraint warnings.
Buildalyzer 9's MSBuild 17.14.28 dependency publishes `net9.0` and `net472`
assets, so the adapter targets .NET 9; consuming its framework fallback from a
.NET 8 application would produce NU1701 and is not treated as compatible.

Project evaluation may run imported targets and expose generated documents.
Consequently it is a build-tool operation even when no assembly is emitted.
The adapter must refuse indexing unless `permissions.build_tool` is true. It
must never perform restore itself; unresolved references become diagnostics.
Source generators are excluded unless `permissions.run_generators` is true.
This distinction cannot fully sandbox malicious project targets; callers must
treat indexing an untrusted repository as code execution.

Target framework, configuration, platform, language version, define constants,
nullable mode, SDK/toolset, ordered references, analyzers/generators, and project
properties affect semantic truth. `MSBuildWorkspace` commonly yields separate
project instances for target-framework variants. Weave must preserve each loaded
project as a separate unit and include its project name/assembly/options in the
variant and fingerprints rather than choosing an arbitrary framework.

Generated documents should be represented only when Roslyn exposes them in the
compilation. They require generated evidence and stable synthetic paths; Weave
must not pretend they are ordinary repository files. Initial implementation can
diagnose and omit them while generators are denied.

## F# requires FSharp.Compiler.Service

F# semantics do not come from Roslyn. The official [FCS project analysis
tutorial][fcs-project] uses one shared `FSharpChecker`,
`ParseAndCheckProject`, `AssemblySignature`, and
`GetAllUsesOfAllSymbols`. Those APIs supply project diagnostics, definitions,
and compiler-resolved uses, including local symbols. The FCS documentation
explicitly warns that the API can change, so the NuGet version belongs behind
the adapter protocol and is pinned. The initial adapter pins current stable FCS
43.12.204 so it understands compiler flags emitted by maintained SDKs, including
nullness checking, while the protocol contains future FCS API churn.

`FSharpProjectOptions` carries ordered `SourceFiles`, compiler options, and
`ReferencedProjects`. Source order is semantic in F#: an edit to an earlier file
can affect every later file. A project therefore remains the atomic unit and the
ordered source inventory participates in its input fingerprint. FCS can analyze
F# project references from source when options are populated, but type-provider
components may still require an on-disk assembly; the adapter should report
that limitation rather than synthesize meaning.

FCS does not promise a durable serialized key for `FSharpSymbol`. Versioned
Weave identities should compose assembly/project identity, declaration kind,
logical enclosing entity, compiled/display name, generic arity/signature, and a
document/range discriminator for locals. `GetAllUsesOfAllSymbols` is sufficient
for exact definitions and references. Call edges require typed-expression
walking or another compiler-resolved surface and are deliberately deferred
until fixture-backed implementation proves their stability.

## Existing producers and packaging

[`scip-dotnet`][scip-dotnet] is Apache-2.0, Roslyn-backed prior art for C# and
Visual Basic navigation facts. It demonstrates large-solution traversal and
SCIP symbol normalization, but it does not cover F#, defaults to restoring, and
does not expose Weave's unit lifecycle/fingerprints. It remains a useful fallback
and conformance corpus, not the native adapter implementation.

LSIF producers validate the general document/range/symbol graph shape, while
SCIP's more compact protocol and existing importer make another LSIF ingestion
path unnecessary.

[.NET tools][dotnet-tools] are standard NuGet-packaged console applications.
A local manifest makes versions repository-specific, but restoring tools is a
networked mutation and must never happen during discovery or ordinary queries.
Weave should discover an explicit executable or `weave-dotnet` on `PATH`, run
`describe` only for an explicit doctor operation, and leave installation to the
operator. The adapter itself is also runnable as `dotnet Weave.Adapter.dll` in
development/CI.

## Practical first slice

The first reviewable adapter should provide:

1. strict `weave.adapter/v0` framing and path confinement;
2. C# project loading, definitions, references, statically resolved calls,
   inheritance/implementation, project dependencies, diagnostics, and hashes;
3. F# project discovery plus compiler-backed definitions/references through
   FCS, preserving source order;
4. complete per-project unit inventories and deterministic ordering;
5. fixture solutions and CI on Linux, macOS, and Windows.

The implementation must label omissions honestly. In particular, F# call
edges, source-generator execution, perfect ABI fingerprints, and per-target
framework selection remain follow-on work unless tests demonstrate them.

[roslyn-overview]: https://github.com/dotnet/roslyn/blob/main/docs/wiki/Roslyn-Overview.md
[locator]: https://github.com/microsoft/MSBuildLocator
[buildalyzer]: https://github.com/phmonte/Buildalyzer
[fcs-project]: https://fsharp.github.io/fsharp-compiler-docs/fcs/project.html
[fcs-options]: https://fsharp.github.io/fsharp-compiler-docs/reference/fsharp-compiler-codeanalysis-fsharpprojectoptions.html
[fcs-caches]: https://fsharp.github.io/fsharp-compiler-docs/fcs/caches.html
[scip-dotnet]: https://github.com/sourcegraph/scip-dotnet
[dotnet-tools]: https://learn.microsoft.com/dotnet/core/tools/global-tools
