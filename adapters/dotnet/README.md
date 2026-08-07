# weave-dotnet

`weave-dotnet` is the optional compiler-native C#/F# adapter. It implements the
one-shot `weave.adapter/v0` process contract and is never run by ordinary Weave
queries.

The repository `global.json` stays on the latest installed .NET 9 feature band
so Buildalyzer and its child MSBuild process select the same supported SDK.

The implementation uses Roslyn/MSBuildWorkspace for C# and
FSharp.Compiler.Service with Buildalyzer's design-time build results for F#.
It does not parse either source language itself.

## Build and test

```console
dotnet restore adapters/dotnet/Weave.Adapter.sln
dotnet build adapters/dotnet/Weave.Adapter.sln --no-restore
dotnet build adapters/dotnet/tests/fixtures/Mixed/Mixed.sln --configuration Debug
dotnet test adapters/dotnet/Weave.Adapter.sln --no-build
```

During development, invoke it explicitly:

```console
weave index \
  --adapter ./adapters/dotnet/src/Weave.Adapter/bin/Debug/net9.0/Weave.Adapter \
  --allow-build-tool
```

Restore is intentionally not performed by the adapter. Restore the target
repository yourself first when its assets are unavailable. `--allow-build-tool`
is required because MSBuild project evaluation may execute imported targets;
do not grant it to an untrusted repository. Source generators and analyzers are
removed from C# projects unless `--allow-generators` is explicit.

## Current fact coverage

- C#: definitions/references, statically resolved invocations and constructors,
  imports, inheritance/implementation, project dependencies, diagnostics.
- F#: FCS-resolved definitions/references and project dependencies with ordered
  source files in the input fingerprint.
- Both: repository-confined paths, UTF-8 byte coordinates, deterministic IDs,
  complete project units, input/surface/inventory fingerprints.

The initial F# slice does not emit call edges. Generated documents and a formal
binary-compatible ABI fingerprint are also deferred; see ADR 0006. Unsupported
facts are omitted rather than inferred.
