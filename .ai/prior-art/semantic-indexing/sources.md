# Reviewed sources and revisions

Research date: 2026-08-06 (America/Los_Angeles)

This file makes the point-in-time source review reproducible. Documentation
links elsewhere intentionally follow their maintained canonical URLs. Source
claims were checked against these repository heads; adopting code later
requires a separately pinned released dependency/tool version.

| Repository | Inspected revision | License | Relevant material |
| --- | --- | --- | --- |
| `scip-code/scip` | [`f7c0b17`](https://github.com/scip-code/scip/tree/f7c0b174aea88b51dbeef1b583844577efb989e0) | Apache-2.0 | `scip.proto`, streaming notes, symbol grammar, ranges, relationships |
| `microsoft/lsif-node` | [`757da87`](https://github.com/microsoft/lsif-node/tree/757da87b7351eab49cf950731cf1fc6420afb9a3) | MIT | NDJSON graph, result sets, monikers, package information |
| `scip-code/scip-go` | [`18c307a`](https://github.com/scip-code/scip-go/tree/18c307a4ccef5654c866addb42a5e1f579dc4684) | Apache-2.0 | `go/packages`/`go/types` indexer, fixtures, implementation fingerprints |
| `sourcegraph/scip-dotnet` | [`4788446`](https://github.com/sourcegraph/scip-dotnet/tree/47884461a79839fb74c99e6a0a7978cd7eb62476) | Apache-2.0 | Roslyn/MSBuild loading, restore behavior, target-framework selection, package targets |
| `dotnet/roslyn` | [`59d60a4`](https://github.com/dotnet/roslyn/tree/59d60a4243f04f97f0976ba449c26d0a0e1b2b64) | MIT | compiler and Workspace platform provenance |
| `dotnet/fsharp` | [`e1ad05c`](https://github.com/dotnet/fsharp/tree/e1ad05c7418fbdf3a7be95c9e09add5a02451119) | MIT | FCS provenance and compiler implementation |
| `golang/tools` | [`4b32d66`](https://github.com/golang/tools/tree/4b32d669ce28c3b3e274a377a955063223a90350) | BSD-3-Clause | `go/packages`, `objectpath`, gopls cache/index implementation |

## Canonical API and design documentation

- [SCIP protobuf schema](https://github.com/scip-code/scip/blob/main/scip.proto)
- [SCIP indexer guide](https://sourcegraph.com/docs/code-navigation/writing-an-indexer)
- [`go/packages`](https://pkg.go.dev/golang.org/x/tools/go/packages)
- [`go/types`](https://pkg.go.dev/go/types)
- [`objectpath`](https://pkg.go.dev/golang.org/x/tools/go/types/objectpath)
- [gopls implementation design](https://go.dev/gopls/design/implementation)
- [gopls scalability and persistent indexes](https://go.dev/blog/gopls-scalability)
- [Roslyn workspace model](https://learn.microsoft.com/en-us/dotnet/csharp/roslyn-sdk/work-with-workspace)
- [Roslyn semantic analysis](https://learn.microsoft.com/en-us/dotnet/csharp/roslyn-sdk/get-started/semantic-analysis)
- [FCS project analysis](https://fsharp.github.io/fsharp-compiler-docs/fcs/project.html)
- [FCS caches](https://fsharp.github.io/fsharp-compiler-docs/fcs/caches.html)
- [FCS project builds](https://fsharp.github.io/fsharp-compiler-docs/project-builds.html)
- [LSP 3.18](https://microsoft.github.io/language-server-protocol/specifications/lsp/3.18/specification/)
- [Bazel persistent workers](https://bazel.build/remote/creating)
- [Bazel remote cache](https://bazel.build/remote/caching)
- [Gradle incremental build](https://docs.gradle.org/current/userguide/incremental_build.html)
- [Salsa red-green algorithm](https://salsa-rs.github.io/salsa/reference/algorithm.html)

## Version notes at review time

- `scip-dotnet` source declared tool version `0.2.15` and targeted .NET 8, 9,
  and 10. The repository's README required at least .NET 8 for local install.
- `scip-go`, `go/packages`, FCS, Roslyn, and SCIP must be pinned at adoption;
  `latest` is inappropriate for reproducible CI or fingerprint domains.
- A repository receiving recent commits is not proof of API stability. FCS
  explicitly documents that its API may change, and `x/tools` remains a
  pre-v1 module despite its official Go project provenance.
