# Executable adapter distribution prior art

Research date: 2026-08-07.

## Decision

Weave adapters are executable compiler plugins, not Go shared objects. The host
must accept either an explicit executable path or a trusted registry entry and
invoke it with literal arguments. A conventional `weave-adapter-NAME` filename
is useful for packages and humans, but Weave must not silently scan `PATH` for
untrusted adapters.

This keeps the useful part of the `protoc` model: a plugin is an independently
compiled program, stdin/stdout are the ABI, implementations may use their
ecosystem's best tooling, and Windows uses an ordinary `.exe`. Protobuf's
official plugin documentation supports both conventional `protoc-gen-NAME`
lookup and an explicit `--plugin` path; Weave deliberately chooses the explicit
path/registry side of that design for its trust boundary.

Primary source:

- [Protocol Buffers compiler plugin API](https://protobuf.dev/reference/cpp/api-docs/google.protobuf.compiler.plugin/)

## Release shape

GoReleaser supports multiple Go build definitions, OS/architecture matrices,
and archives filtered by build ID. Therefore all pure-Go protocol bridges can
ship together as a platform adapter bundle without introducing a new build
system. The core remains a separate small archive. Windows bundles are ZIP;
macOS and Linux bundles are tarballs.

Primary sources:

- [GoReleaser Go builds](https://goreleaser.com/customization/builds/builders/go/)
- [GoReleaser archives](https://goreleaser.com/customization/package/archives/)

Each artifact must contain only host-platform executables and documentation.
It must not imply that an ecosystem runtime or semantic producer is embedded
when it is not. `describe` remains safe and declarative even if an upstream
producer is absent; `weave adapters doctor` reports the missing requirement.

## Current gap matrix

| Component | Implementation | Current tagged artifact | Desired artifact |
| --- | --- | --- | --- |
| `weave` | pure Go | macOS/Linux/Windows amd64/arm64 | unchanged core archive |
| `weave-cpp` | pure-Go bridge to `scip-clang` | none | platform adapter bundle; producer remains explicit |
| `weave-typescript` | pure-Go bridge to `scip-typescript` | none | platform adapter bundle; Node producer remains explicit |
| `weave-jvm` | pure-Go bridge to `scip-java` | none | platform adapter bundle; JVM producer remains explicit |
| `weave-dotnet` | .NET 10 | self-contained RID archives | retain independently versioned companion archives |
| `weave-rust` | Rust bridge to `rust-analyzer` | none | native companion archives after a reproducible target matrix exists |
| `weave-python` | Python package | wheel | retain as an explicit-runtime adapter until a native implementation or honest standalone packager is justified |

The release workflow currently builds only the core in GoReleaser. Adding the
three pure-Go bridges is a mechanical and low-risk first increment. Rust and
Python must not be labeled standalone binaries until CI tests the exact shipped
artifacts on each target.

## Manifest and discovery

The existing trusted registry is the installation manifest: it stores literal
argv, conservative source inputs, permissions, and a timeout. Distribution
archives should include an example fragment, not mutate user configuration.
The handshake supplies provider identity, protocol versions, semantic
languages/formats, operations, refresh modes, and external executable/runtime
requirements. Artifact filenames and provider identity are not security
authority; selecting a registry entry is.

## Verification floor

Before release, CI should:

1. compile every pure-Go bridge for the same macOS/Linux/Windows amd64/arm64
   matrix as the core;
2. run protocol conformance tests natively on the three hosted operating
   systems without requiring the semantic producer for declarative `describe`;
3. run at least one real-producer smoke test where upstream packaging permits;
4. build a GoReleaser snapshot and inspect `artifacts.json` for the expected
   binary set; and
5. publish one checksum file covering core, adapter bundles, and non-Go
   companion artifacts.

GoReleaser documents `artifacts.json` as the machine-readable inventory of
release outputs:

- [GoReleaser artifacts](https://goreleaser.com/customization/general/artifacts/)

