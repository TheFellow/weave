# Adapter protocols and semantic fingerprints

## Requirements

The process boundary must support native runtimes without turning Weave into an
IDE protocol or build server. It needs:

- capability/version discovery before indexing;
- one repository root and explicit build variant per request;
- bounded fact batches grouped into atomic compilation units;
- structured diagnostics and partial failure;
- provider/toolchain identity;
- dependency, semantic-input, and public-surface fingerprints;
- cancellation through normal process control initially;
- stdout reserved for protocol frames and stderr for bounded operator logs.

It does not initially need interactive completion, document mutation events,
server-to-client requests, multiplexing, or a resident process.

## Protocol prior art

### Language Server Protocol

[LSP](https://microsoft.github.io/language-server-protocol/specifications/lsp/3.18/specification/)
uses JSON-RPC between a client and language server. Its useful lessons are
explicit initialization/capabilities, request IDs, cancellation, structured
errors, and a disciplined stdout transport.

LSP itself is too broad for this boundary. It models a long-lived interactive
editor session and answers point queries; it does not define Weave's durable
compilation-unit fact stream or fingerprints. Running arbitrary language
servers and replaying “definition/references” queries would also be slower and
less reproducible than using compiler/indexer libraries or SCIP producers.

### Bazel persistent workers

Bazel's official
[persistent worker protocol](https://bazel.build/remote/creating) is closer to
the execution problem. A worker reads requests from stdin, writes only
responses to stdout, uses request IDs, can be one-shot or persistent, associates
input paths with digests, supports cancellation, and offers JSON or
length-delimited protobuf framing.

The key lesson is evolutionary: make a one-shot command correct first, then add
the same operation as a persistent worker when startup/caching measurements
justify it. Multiplexing and cancellation can be negotiated capabilities rather
than mandatory v0 complexity.

### SCIP and LSIF streaming

SCIP demonstrates a compact protobuf interchange and calls out streaming large
top-level indexes. LSIF demonstrates that NDJSON is inspectable and pipeline
friendly. Weave v0 should use bounded NDJSON frames for rapid contract and
fixture iteration, then move to length-delimited protobuf before promising a
third-party stable adapter API.

## Fingerprint prior art

Three distinct hashes must not be conflated:

1. **Content digest**: bytes of a document or immutable artifact.
2. **Semantic-input fingerprint**: everything that can affect checking one
   compilation unit—selected files, order, build options/tags, dependency
   surfaces, toolchain, adapter, generated inputs, and explicit environment.
3. **Public-surface fingerprint**: the provider's canonical representation of
   what dependent compilation units can observe.

Bazel's
[remote-cache model](https://bazel.build/remote/caching) hashes an action from
declared inputs, command line, and environment and stores outputs in a
content-addressed store. Adopt its insistence that all relevant inputs belong in
the cache key; Weave is not adopting Bazel's execution/cache service.

Gradle's
[incremental build documentation](https://docs.gradle.org/current/userguide/incremental_build.html)
and
[compile-avoidance guidance](https://docs.gradle.org/current/userguide/performance.html#compile_avoidance)
distinguish implementation changes from ABI changes: dependent work can be
kept when the observable surface is unchanged. Adopt that two-level decision,
but let each compiler-native adapter define its own observable surface. A
language-neutral core cannot correctly derive Go export data, .NET metadata,
and F# signatures from text with one algorithm.

Salsa's
[red-green algorithm](https://salsa-rs.github.io/salsa/reference/algorithm.html)
adds another useful rule: re-execute a computation whose inputs may have
changed, compare its result, and stop propagation when the result is unchanged.
Weave's analogous result is the public-surface fingerprint. Weave does not need
to embed Salsa to apply this conservative propagation rule.

gopls provides language-specific confirmation: its
[persistent-index design](https://go.dev/blog/gopls-scalability) stores
per-package cross-reference/method/type summaries and loads them for global
queries.

## Fingerprint contract

Adapters own canonical semantic meaning; the core owns storage, comparison, and
propagation. Each digest should be an opaque tuple:

```json
{
  "algorithm": "sha256",
  "domain": "weave.go.public-api/v1",
  "value": "...lowercase hex..."
}
```

Rules:

- include a domain/version so canonicalization changes invalidate safely;
- use deterministic, length-delimited or otherwise unambiguous encoding before
  hashing;
- sort sets and preserve order where the language makes order semantic;
- use repository-relative canonical paths where paths are semantic;
- include adapter/compiler versions in semantic-input identity;
- never use timestamps as semantic identity;
- never compare opaque public-surface digests across different domains;
- a missing/failed fingerprint forces conservative invalidation;
- collisions are correctness failures, so use a cryptographic digest (SHA-256
  is sufficient and ubiquitous) rather than a short in-memory hash.

RIBLT is not needed in this local adapter protocol. The core already knows the
requested repository and receives a bounded unit inventory; direct hashes and
Git differences are exact. Revisit RIBLT only for reconciling independently
maintained large remote inventories where exchanging all keys is measured to be
the bottleneck.

## Proposed experimental frame lifecycle

The full normative proposal is in ADR 0001. In outline:

```text
weave -> adapter describe -> one capability JSON document
weave -> adapter index    -> one request JSON document on stdin
adapter stdout            -> run.begin
                             unit.begin
                             facts (bounded batches)
                             unit.end (counts + fingerprints)
                             ...
                             run.end (complete unit inventory)
```

The core stages one unit and replaces its old facts only after a valid
`unit.end`. A nonzero exit, missing terminal frame, count/digest mismatch, or
oversized frame discards staged facts. Existing complete facts remain readable
but must not be reported as fresh.
