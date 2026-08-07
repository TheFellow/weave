# Rust semantic indexing prior art

Research date: 2026-08-07

## Decision

Use the Rust ecosystem's own maintained semantic pipeline:

```text
rust-analyzer scip
        │
        ▼
official SCIP protobuf (`scip` Rust crate)
        │
        ▼
weave-rust — weave.adapter/v0
```

`weave-rust` is a narrow producer supervisor and normalization boundary. It
does not parse Rust, load rust-analyzer internals, infer calls from token text,
or pretend that a syntax tree is a compiler-resolved graph. It invokes
[`rust-analyzer scip`][ra-scip-source], decodes the Apache-2.0 [`scip` crate][scip-crate],
and translates definitions, references, symbol kinds, and explicit SCIP
relationships into the same bounded facts used by Weave's direct SCIP importer.

This is now the practical compiler-native choice. The actively released
[`scip-code/scip-rust` v0.0.6][scip-rust-release] is intentionally just a thin
wrapper around `rust-analyzer scip`; its script performs tool preflights and
then delegates directly to the rust-analyzer command. Weave should reuse that
authority, not fork it or insert another Rust grammar between rust-analyzer and
the graph.

The adapter remains a distinct Rust executable because it has responsibilities
the SCIP producer does not: `weave.adapter/v0` negotiation, request permission
enforcement, repository containment, coordinate conversion, stable IDs,
evidence mapping, resource bounds, deterministic fingerprints, and atomic
unit lifecycle frames.

## Options evaluated

| Option | What it provides | Practical boundary | Decision |
| --- | --- | --- | --- |
| `rust-analyzer scip` through `scip-rust` precedent | Cargo/crate graph loading, cfg-aware name resolution, macro-aware IDE semantics, stable source navigation, and a batch SCIP index. The current implementation emits UTF-8 positions, definitions, references, symbol kinds, local/global identities, and producer metadata. | rust-analyzer is a trusted project tool: it invokes Cargo and, by default, build scripts and procedural macros. SCIP generation and a few symbol-identity cases are still explicitly documented as imperfect in its source. | **Adopt.** Best maintained batch semantic output with no private API coupling. |
| Embed rust-analyzer crates (`ra_ap_*`) | The same analysis engine in-process and theoretically finer-grained control. | rust-analyzer's internal crate graph is large and release-cadenced as an implementation, not a small stable embedding API. The installation guide points to programmatic crates, but that still makes their internal surface Weave's compatibility burden. | Defer. A process plus stable interchange is the intended architecture. |
| External `rustc_driver` / `rustc_private` | Compiler HIR/type-checking data at maximum fidelity. | The [rustc developer guide][rustc-private] requires unstable `rustc_private`, nightly-matched compiler internals, `rustc-dev`, and LLVM tools. It would tie the adapter to toolchain internals and make cross-platform packaging substantially harder. | Reject for the baseline. Useful only for a future specialized adapter with a pinned compiler. |
| `ra_ap_syntax` or tree-sitter-rust | Fast, error-tolerant declarations and syntax ranges without building a project. | Neither supplies the resolved crate graph, cfg selection, macro expansion, trait identity, or reference targets required for semantic evidence. rust-analyzer itself describes its syntax crate as deliberately independent of semantic context. | Do not use as a silent fallback. A future explicitly syntactic provider could be separate and labeled `Syntactic`. |
| LSP requests to rust-analyzer | Definitions, references, implementations, workspace symbols, and other interactive operations. | LSP is request-oriented, not a deterministic complete inventory. Issuing definition/reference requests for every token would be slower and harder to make atomic than rust-analyzer's native static-index export. | Retain for interactive tooling, not indexing. |
| Parse Cargo manifests and Rust with `syn` | Stable libraries and convenient source ASTs. | `syn` parses Rust token syntax for procedural macros; it is not Cargo/rustc name resolution. `cargo metadata` supplies a workspace/package model but no source binding graph. | Useful supporting tools, not semantic authority. |

## Why SCIP is the correct interchange

SCIP is a language-neutral protobuf designed for code navigation. Its official
repository ships maintained Rust bindings and defines canonical project-relative
paths, compact half-open ranges, per-document position encodings, definition
roles, symbol information, and implementation/reference relationships. This
matches Weave's subordinate-process model while keeping the language engine
replaceable.

The current rust-analyzer exporter is unusually inspectable:

- it loads a Cargo workspace and computes rust-analyzer's `StaticIndex`;
- it records its exact rust-analyzer version in SCIP metadata;
- it emits project-relative Rust documents using UTF-8 code-unit offsets;
- it emits a definition bit only when a token exactly matches its resolved
  definition range;
- it retains document-local SCIP symbols and package-qualified global symbols;
- it currently emits empty `relationships` and does not distinguish a call
  occurrence from another reference.

That last point determines the first evidence boundary: resolved SCIP
definition/reference occurrences are `Exact` navigation facts, but there is no
honest basis for a `Calls` edge in the present export. Weave must not inspect
nearby parentheses and upgrade a reference to compiler truth. If rust-analyzer
later exports explicit call/relationship semantics, the adapter can map those
facts without changing its protocol.

SCIP's top-level `Index` can be large. The v0 adapter bounds the generated file
before protobuf decoding, bounds each source file and aggregate source bytes,
then emits byte-aware NDJSON batches. A single fact that cannot fit the host's
frame limit fails the refresh; facts are never truncated.

## Trust and permissions

rust-analyzer's own [security page][ra-security] says it assumes project code is
trusted. Its non-exhaustive list includes default execution of build scripts and
procedural macros, `.cargo/config` executable overrides, and project-selected
toolchains. The SCIP command currently requests build-script output and enables
a proc-macro server while loading the workspace.

Therefore `weave-rust`:

- refuses indexing unless `permissions.build_tool` is true;
- writes an explicit rust-analyzer configuration with Cargo build scripts and
  procedural macros disabled unless `permissions.run_generators` is true;
- sets Cargo offline mode unless both network and restore are permitted;
- disables rustup's automatic installation for probes and producer execution,
  so a project-selected but absent toolchain fails instead of being downloaded;
- passes arguments without a shell and keeps rust-analyzer stdout away from the
  protocol stream;
- treats stderr as operator diagnostics bounded by the Weave host;
- validates every SCIP path before reading source and rejects symlink escapes;
- uses a private temporary output/config directory removed after the run.

Disabling build scripts and proc macros reduces repository code execution but
does not make an untrusted checkout safe. Cargo metadata, `.cargo/config`,
`rust-toolchain.toml`, compiler wrappers, and the selected toolchain remain part
of build-tool authority. The explicit permission and documentation must say so.

The rust-analyzer [configuration contract][ra-config] confirms that build
scripts and proc macros default to enabled and documents the initialization
JSON used to disable them. Cargo's [metadata contract][cargo-metadata] provides
a stable machine-readable workspace surface, but rust-analyzer remains the
component responsible for translating that workspace into a semantic crate
graph.

## Identity, freshness, and working directory

The same source can mean something different under another Cargo or Rust
toolchain. `cfg` values, target layout, standard-library symbols, macro
expansion, and crate graph resolution depend on more than the rust-analyzer
binary. The adapter's provider version therefore hashes:

- adapter package version and source fingerprint;
- `rust-analyzer --version`;
- `cargo --version`;
- `rustc --version --verbose`, including host and compiler commit;
- `rustc --print sysroot`.

All probes run with the repository as their working directory. This matters for
rustup proxies: a checked-in `rust-toolchain.toml` can select a different Cargo,
rustc, sysroot, and rust-analyzer component. During a normal host invocation,
`describe` and `index` both run from that repository. A human who runs
`weave-rust describe` from elsewhere may correctly see a different provider
version than the one negotiated for a repository.

Stable fact IDs follow the direct SCIP importer's algorithms where possible:
global SCIP symbols are repository/provider qualified, `local ...` symbols add
their document path, one document is one atomic unit, and ranges contribute to
occurrence identity. Adapter and producer/toolchain versions invalidate units
without becoming public symbol names.

## Normalization and honest evidence

| SCIP input | Weave fact |
| --- | --- |
| `Document` | One Rust document and one atomically replaceable unit. |
| `SymbolInformation` | `Exact` symbol with the original SCIP string as `stable_name`; the Go host supplies its canonical normalized search name. |
| Definition or forward-definition occurrence | `Exact` definition occurrence and canonical display anchor. |
| Other symbol occurrence | `Exact` reference occurrence to the resolved SCIP identity. |
| `is_implementation` relationship | `Exact` `implements` edge. |
| Reference/definition/type-definition relationship | `Exact` `references` edge. |
| An ordinary reference used syntactically as a call | Reference only; no invented `calls` edge. |
| Symbols unavailable because cfg/macro/build-script support was disabled | Omit; do not fabricate a syntax match. Re-run with explicit generator permission when the repository is trusted. |

rust-analyzer currently warns about duplicate global symbols in several known
cases. Weave fails a run with duplicate fact IDs rather than accepting an
ambiguous graph under `Exact` evidence. A future adapter revision can retain
bounded ambiguous candidates if the normalized model gains an explicit
producer-ambiguity representation.

## Test and distribution strategy

The adapter is an ordinary Cargo binary pinned by `Cargo.lock`. Unit and
integration tests cover UTF-8/16/32-to-byte range conversion, unsafe paths,
toolchain-sensitive provider identity, exact evidence mapping, absence of
invented call edges, permission refusal, generator/offline configuration,
byte-aware frame splitting, and the complete child-process lifecycle.

Deterministic contract tests compile a tiny fake rust-analyzer executable and
feed a genuine SCIP protobuf through the real adapter binary. Dedicated CI also
runs a real `rust-analyzer scip` against a small Cargo fixture and asks the Go
host to validate and publish the resulting stream on Linux. Rust compilation
and contract tests run on Linux, macOS, and Windows.

A release can ship platform binaries for `weave-rust`, but rust-analyzer, Cargo,
rustc, and matching standard-library sources remain explicit runtime
dependencies. This is preferable to quietly embedding one toolchain snapshot
inside the adapter. Container/Nix packaging can later follow `scip-rust` for
hermetic deployments.

## Primary OSS sources

- [`scip-code/scip-rust` repository][scip-rust] and [v0.0.6 release][scip-rust-release]
- [rust-analyzer SCIP exporter source][ra-scip-source]
- [rust-analyzer architecture][ra-architecture]
- [rust-analyzer configuration][ra-config]
- [rust-analyzer security model][ra-security]
- [rust-analyzer installation/programmatic crate note][ra-install]
- [SCIP protocol and maintained bindings][scip]
- [official `scip` Rust crate 0.9.0][scip-crate]
- [external `rustc_driver` and `rustc_private` requirements][rustc-private]
- [Cargo metadata contract][cargo-metadata]
- [rustup automatic-install environment contract][rustup-environment]

[scip-rust]: https://github.com/scip-code/scip-rust
[scip-rust-release]: https://github.com/scip-code/scip-rust/releases/tag/v0.0.6
[ra-scip-source]: https://github.com/rust-lang/rust-analyzer/blob/master/crates/rust-analyzer/src/cli/scip.rs
[ra-architecture]: https://rust-analyzer.github.io/book/contributing/architecture.html
[ra-config]: https://rust-analyzer.github.io/book/configuration.html
[ra-security]: https://rust-analyzer.github.io/book/security.html
[ra-install]: https://rust-analyzer.github.io/book/installation.html
[scip]: https://github.com/scip-code/scip
[scip-crate]: https://docs.rs/scip/0.9.0/scip/
[rustc-private]: https://rustc-dev-guide.rust-lang.org/rustc-driver/external-rustc-drivers.html
[cargo-metadata]: https://doc.rust-lang.org/cargo/commands/cargo-metadata.html
[rustup-environment]: https://rust-lang.github.io/rustup/environment-variables.html
