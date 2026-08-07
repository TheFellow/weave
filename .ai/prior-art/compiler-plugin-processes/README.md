# Compiler plugin process architecture prior art

Research date: 2026-08-06

## Question

How should Weave accept precise semantic facts from an open-ended set of
language-native implementations without embedding every compiler runtime in its
Go core?

## Primary sources

- Protobuf's canonical
  [`plugin.proto`](https://github.com/protocolbuffers/protobuf/blob/main/src/google/protobuf/compiler/plugin.proto)
  defines the durable minimal model: a plugin is an executable that reads one
  `CodeGeneratorRequest` from stdin and writes one `CodeGeneratorResponse` to
  stdout. The request includes an ordered transitive descriptor inventory and
  compiler version. The response reports supported features, structured errors,
  and generated files. Executables are discovered by a naming convention or an
  explicit path.
- The official protoc
  [plugin API](https://protobuf.dev/reference/cpp/api-docs/google.protobuf.compiler.plugin/)
  keeps language bindings optional. C++ authors can use `PluginMain`, but the
  serialized request/response contract is the actual interoperability boundary.
- Bazel's
  [persistent worker protocol](https://bazel.build/remote/creating)
  uses stdin/stdout, request IDs, protocol-only stdout, diagnostics on stderr,
  and either protobuf or JSON. One-shot tools can evolve to persistent workers
  after startup cost warrants the extra lifecycle and cancellation machinery.
- The
  [Build Server Protocol](https://build-server-protocol.github.io/docs/specification)
  demonstrates explicit client/server capabilities, client-managed process
  lifetime, build targets, compiler options, and language-specific extension
  data. It is useful for acquiring build topology, but its long-lived JSON-RPC
  surface is broader than Weave's indexing transaction.
- HashiCorp's
  [`go-plugin`](https://github.com/hashicorp/go-plugin)
  demonstrates production subprocess isolation, protocol negotiation, crash
  containment, checksums, and cross-language gRPC plugins. Its listener,
  handshake, bidirectional RPC, and reattachment machinery solve a larger
  problem than a deterministic one-shot semantic extraction run.

## Lessons for Weave

1. The wire messages, not a host-language interface, are the extension point.
   Generated Go, C#, JVM, Rust, or TypeScript bindings are conveniences only.
2. Keep a one-request/one-complete-response execution mode permanently. It is
   easy to implement, test, terminate, sandbox, and reproduce in CI.
3. Reserve stdout exclusively for protocol data and stderr for bounded operator
   diagnostics. A nonzero exit or incomplete terminal message rejects the run.
4. Negotiate protocol, fact encoding, operations, refresh modes, position
   encoding, toolchain requirements, and optional features before indexing.
5. The core owns storage and atomic publication. Plugins return facts and
   fingerprints; they never mutate Weave's database.
6. Persistence is a capability and performance optimization, not a correctness
   dependency. Add it only when compiler startup measurements justify it.
7. Build topology and target configuration are semantic inputs. BSP or native
   build APIs may be used inside an adapter without making the Weave core a
   build server.
8. Cross-platform support comes from small native-runtime adapters plus a
   platform-neutral contract and conformance suite, not from porting compilers
   to Go.

## Adopt, adapt, defer

Adopt from protoc:

- explicit executable or conventional discovery;
- a language-neutral request/response source of truth;
- compiler/tool version context and feature negotiation;
- one-shot process isolation as the lowest common denominator.

Adapt from Bazel workers:

- request IDs, bounded framing, protocol-only stdout, diagnostics on stderr,
  cancellation, and a future negotiated persistent mode.

Reuse where appropriate:

- SCIP as semantic interchange;
- compiler and build-system APIs inside their native adapter runtimes;
- BSP implementations for build-target discovery when they are already present.

Defer:

- `go-plugin`/gRPC-style listeners and callback RPC;
- long-lived adapter processes until benchmarks demonstrate material benefit;
- dynamic capability registration;
- an SDK that makes generated bindings normative;
- automatic execution of arbitrary PATH entries without explicit trust policy.

## Resulting constraint

Weave's Go executable is a compiler driver and graph engine, not a universal
parser. Language support is an open-ended family of replaceable executables.
The current NDJSON v0 contract is the experimental form of that boundary; a
stable v1 should be defined in protobuf and retain an equally simple one-shot
mode.
