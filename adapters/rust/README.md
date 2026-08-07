# weave-rust

`weave-rust` is the compiler-native Rust adapter for Weave. It is a separate
Rust process implementing `weave.adapter/v0`; the Go core does not parse Rust or
link rust-analyzer libraries.

The adapter delegates semantic analysis to `rust-analyzer scip`, decodes the
official SCIP protobuf, and emits bounded Weave facts. This provides resolved
definitions, references, symbol kinds, and explicit implementation/reference
relationships. The current rust-analyzer SCIP export does not distinguish calls
from ordinary references, so the adapter deliberately does not invent call
edges.

## Install

Rust 1.81 or newer, Cargo, rustc, and a compatible rust-analyzer executable are
required. With rustup:

```console
rustup component add rust-analyzer
cargo install --locked --path ./adapters/rust
weave-rust describe --protocol weave.adapter/v0
```

When `weave-rust` is on `PATH`, or `WEAVE_RUST_ADAPTER` points to it, ordinary
queries automatically refresh repositories containing a Git-visible
`Cargo.toml` or `rust-project.json`. Automatic mode grants build-tool evaluation
but keeps network, restore, build scripts, and procedural macros disabled.
Weave conservatively fingerprints every Git-visible file because Rust include
macros can consume inputs with arbitrary extensions.

For source development:

```console
cargo build --manifest-path adapters/rust/Cargo.toml
export WEAVE_RUST_ANALYZER="$(command -v rust-analyzer)"
weave index \
  --adapter "$PWD/adapters/rust/target/debug/weave-rust" \
  --allow-build-tool
```

`WEAVE_RUST_ANALYZER` selects an explicit rust-analyzer executable; otherwise
the adapter uses `rust-analyzer` from `PATH`. Weave passes this explicit setting
through its bounded child environment in both automatic and one-shot modes.

The target repository and dependencies must already be available locally by
default. The adapter disables rustup automatic installation, so a toolchain
selected by `rust-toolchain.toml` must also already be installed. Network access and dependency restoration require both
`--allow-network` and `--allow-restore`. Build scripts and procedural macros are
disabled unless the trusted repository is indexed with `--allow-generators`:

```console
weave index --adapter weave-rust \
  --allow-build-tool \
  --allow-generators
```

## Trust boundary

`--allow-build-tool` is mandatory because rust-analyzer evaluates the Cargo
workspace. This is not safe for an untrusted checkout. Even with generators
disabled, Cargo configuration, toolchain files, compiler wrappers, and selected
executables can affect or execute during project evaluation. rust-analyzer
itself documents this trusted-project assumption.

The adapter runs the producer without a shell, forces Cargo offline unless
network and restore are both allowed, uses a private temporary SCIP file,
validates repository-relative paths and symlink containment, and bounds the
index, sources, facts, frames, and protocol bytes. A failed producer or invalid
index publishes nothing.

## Evidence and freshness

- rust-analyzer-resolved symbols and definition/reference occurrences are
  `Exact` facts about the selected static crate graph.
- SCIP implementation relationships become `Exact` `implements` edges.
- An ordinary SCIP occurrence remains a reference, even when its source syntax
  resembles a call.
- Symbols omitted because build scripts or proc macros were disabled are not
  replaced by heuristic syntax matches.

Provider identity includes the adapter source, rust-analyzer, Cargo, rustc host
and commit, and active sysroot. Probes run from the repository directory so a
project-pinned rustup toolchain participates in freshness. One SCIP document is
one atomic Weave unit.

## Test

```console
cargo fmt --manifest-path adapters/rust/Cargo.toml -- --check
cargo test --locked --manifest-path adapters/rust/Cargo.toml --all-targets
cargo clippy --locked --manifest-path adapters/rust/Cargo.toml \
  --all-targets -- -D warnings
```

The test corpus covers resolved symbol facts and relationships, local symbol
scope, source coordinates, process negotiation, explicit permission refusal,
offline/generator policy, and host-selected frame limits. CI additionally runs
the real rust-analyzer producer against `tests/fixtures/sample` and validates
the complete stream through the Go host.

See the [prior-art decision](../../.ai/prior-art/rust-semantic-indexing/README.md)
for alternatives, security constraints, and the upgrade path.
