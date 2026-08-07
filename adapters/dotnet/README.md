# weave-dotnet

`weave-dotnet` is the optional compiler-native C#/F# adapter. It implements the
one-shot `weave.adapter/v0` process contract. When the executable is added with
`weave adapters install` or selected explicitly by `WEAVE_DOTNET_ADAPTER`,
ordinary Weave queries run it automatically
after relevant C#/F#/project inputs change.

The repository `global.json` stays on the latest installed .NET 10 feature band
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

## Install a release companion

Every tagged Weave release includes a same-version `Weave.Adapter.DotNet`
NuGet tool package and framework-dependent archives for macOS, Linux, and
Windows on x64 and arm64. All require the .NET 10 runtime. Install a downloaded
package into an explicit local tool directory:

```console
dotnet tool install Weave.Adapter.DotNet --tool-path .weave-tools \
  --add-source /path/to/downloads --version VERSION
export WEAVE_DOTNET_ADAPTER="$PWD/.weave-tools/weave-dotnet"
weave adapters doctor
```

Alternatively unpack the archive matching the host RID and install its
single-file `weave-dotnet` (`weave-dotnet.exe` on Windows) with
`weave adapters install PATH --allow-build-tool`. The adapter and core release
versions are built together; `weave adapters doctor` performs the protocol
compatibility handshake. No package is published to NuGet.org by the release
workflow.

Maintainers can dry-run one or all release RIDs without a tag:

```console
WEAVE_DOTNET_RIDS=osx-arm64 WEAVE_DOTNET_VERIFY_RID=osx-arm64 \
  scripts/package-dotnet-adapter.sh 0.0.0-local /tmp/weave-dotnet-dist
```

During development, expose the built adapter to Weave:

```console
export WEAVE_DOTNET_ADAPTER="$PWD/adapters/dotnet/src/Weave.Adapter/bin/Debug/net10.0/Weave.Adapter"
weave symbols MyType
```

Restore and ordinary compilation are intentionally not performed by the adapter.
Restore the target repository first; F# project graphs also need their referenced
outputs built once before indexing. `--allow-build-tool`
is automatically granted in discovery mode because MSBuild project evaluation
may execute imported targets; do not expose the adapter while querying an
untrusted repository. Network, restore, and generators remain denied. Source
generators and analyzers are removed from C# projects unless an explicit adapter
run grants `--allow-generators`.

Weave fingerprints tracked and non-ignored C#/F# source, solution, project,
props, targets, SDK-selection, NuGet configuration, and lock inputs. The adapter
advertises only `full` refresh, so any changed semantic fingerprint replaces its
complete provider-owned inventory; unrelated edits reuse the persisted inventory
without starting the adapter. Go and .NET inventories have separate owners and
are published in the same storage transaction.

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

The current adapter targets .NET 10 so its Buildalyzer/MSBuild host can evaluate
.NET 10 SDK tasks. Large solutions can still exceed the bounded four-minute full
refresh; that condition fails without publishing a partial inventory. See the
recorded real-repository baseline under `.ai/benchmarks`.
