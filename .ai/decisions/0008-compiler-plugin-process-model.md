# ADR 0008: Protoc-style compiler plugin process model

- Status: Accepted
- Date: 2026-08-06
- Research: [compiler plugin process prior art](../prior-art/compiler-plugin-processes/README.md)

## Context

Weave needs semantic coverage across languages whose authoritative compilers and
project systems run on different runtimes. Treating Go as the implementation
language for all parsing and resolution would duplicate compiler behavior,
reduce correctness, and make toolchain/platform support harder. Conversely,
embedding native runtimes or shared libraries would couple the core's address
space, release process, and failure modes to every ecosystem.

Protoc code generators demonstrate a smaller durable boundary: a compiler driver
starts a subordinate executable, writes a known request to stdin, reads a known
response from stdout, and remains independent of the plugin's implementation
language.

## Decision

1. Weave is a compiler-driver-style orchestration and graph core. Language
   semantics belong to compiler-native providers. With the exception of a
   bundled Go convenience that uses Go's own toolchain APIs, new language
   providers are separate executables by default.
2. The public extension boundary is a versioned, language-neutral process
   protocol. No adapter must import a Go package or load into the core process.
3. One-shot execution remains the normative compatibility floor: capability
   discovery, one bounded stdin request, framed protocol output on stdout,
   bounded diagnostics on stderr, a terminal response, and process exit.
4. The core owns discovery, negotiation, permissions, deadlines, cancellation,
   validation, normalization, atomic database publication, and freshness. An
   adapter owns compiler/build-system interaction, semantic units, facts,
   dependency and surface fingerprints, and toolchain diagnostics.
5. Capabilities, not executable names, control behavior. Provider names identify
   provenance and ownership only. A future plugin registry may configure paths,
   arguments, trust, and variants without teaching the core about each language.
6. The experimental NDJSON v0 contract is published with cross-language golden
   fixtures. Stable v1 will use a checked-in protobuf schema as the source of
   truth, with generated bindings and conformance harnesses offered as optional
   conveniences.
7. A persistent worker mode may be negotiated later. It may improve latency but
   cannot be required for correctness or parity with a one-shot adapter.
8. Host OS/architecture, target platform, compiler/toolchain version, project
   configuration, and adapter version are explicit semantic inputs. Adapters use
   native cross-compilation support when available and do not need to execute
   target binaries to index them.

## Consequences

Language adapters can be written and released in the ecosystem that best
supports their compiler APIs. Crashes and dependency conflicts remain outside
the Go core. CI can exercise the same recorded contract on macOS, Linux, and
Windows. Additional languages do not require a new parser or a new database path.

The tradeoff is process startup, packaging multiple runtimes, and protocol
evolution discipline. Fine-grained incremental operation requires adapters to
report meaningful compilation units and fingerprints. Installation and trust
policy must become first-class before arbitrary discovered adapters run
automatically.

## Rejected alternatives

- **Implement every parser/resolver in Go:** duplicates language semantics and
  cannot match native compiler behavior across build variants.
- **Go `plugin` or shared libraries:** platform-limited and couples ABI, crashes,
  dependencies, and licensing inside one process.
- **Make gRPC mandatory:** introduces listeners and generated runtime machinery
  before Weave needs bidirectional or long-lived RPC.
- **Use LSP/BSP as the fact protocol:** useful upstream sources for adapters, but
  their interactive query/build lifecycle does not express Weave's atomic fact
  inventory and freshness fingerprints directly.
- **Make SCIP the process lifecycle:** SCIP is valuable semantic interchange but
  does not define permissions, refresh negotiation, atomic unit inventory, or
  process supervision.
