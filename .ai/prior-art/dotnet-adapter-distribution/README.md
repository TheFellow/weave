# .NET adapter distribution: prior art

Research date: 2026-08-06

## Sources and lessons adopted

- [.NET tool packaging](https://learn.microsoft.com/dotnet/core/tools/global-tools-how-to-create)
  defines `PackAsTool`, `ToolCommandName`, and NuGet installation. The adapter
  already follows this model, so a `.nupkg` is the smallest portable companion
  artifact for developers with the pinned SDK/runtime.
- [`dotnet publish`](https://learn.microsoft.com/dotnet/core/tools/dotnet-publish)
  supports runtime identifiers, framework-dependent or self-contained output,
  and single-file bundles. Release archives should use framework-dependent
  single-file executables for supported RIDs: much smaller than embedding six
  runtimes, while preserving a clear .NET 10 prerequisite.
- [NuGet package versioning](https://learn.microsoft.com/nuget/concepts/package-versioning)
  uses SemVer. The adapter package and core release must share the same release
  version because the native handshake is the compatibility authority.

## Decision

The release workflow builds a `Weave.Adapter.DotNet.<version>.nupkg` companion
tool plus framework-dependent, single-file archives for `linux-x64`,
`linux-arm64`, `osx-x64`, `osx-arm64`, `win-x64`, and `win-arm64`. The archive
contains `weave-dotnet` and a small README; users need the .NET 10 runtime.

The build script is locally dry-runnable, derives no unpublished version by
itself, and verifies the adapter's `describe` handshake after publishing the
host RID. The tag workflow passes the same SemVer as GoReleaser. No NuGet push
is performed; the package is a GitHub release artifact until a separate
publication decision is made.
