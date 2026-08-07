# ADR 0001: Semantic interchange and adapter process boundary

- Status: Proposed
- Date: 2026-08-06
- Research: [semantic indexing prior art](../prior-art/semantic-indexing/README.md)

## Context

Weave needs precise facts from Go, C#, F#, and eventually other languages. The
Go core cannot embed every compiler runtime, and maintaining independent parsers
would weaken semantic correctness. Existing SCIP producers already emit useful
portable navigation facts, but SCIP does not carry Weave's complete lifecycle:
compilation-unit invalidation, evidence classes, build variants, semantic-input
and public-surface fingerprints, or partial refresh status.

The first process contract must be small enough to test and revise before it is
offered to third parties.

## Decision

### 1. SCIP is the first external semantic interchange

Weave will ingest SCIP protobuf indexes and preserve provider tool metadata and
original symbol strings. SCIP facts are normalized into Weave's internal model;
SCIP is not the database schema.

LSIF will not be a first-class ingestion or emission target. A later converter
may be added only for a concrete indexer with no SCIP/native path.

### 2. Native language providers are executable adapters

Adapters are separate processes using their native runtime. The Go core starts
them with argument arrays and explicit working directory/environment. It never
loads compiler plugins into its process.

The experimental v0 executable surface is:

```text
<adapter> describe --protocol weave.adapter/v0
<adapter> index    --protocol weave.adapter/v0
```

`describe` writes exactly one JSON object to stdout and exits. `index` reads
exactly one JSON request object from stdin followed by EOF, emits NDJSON frames
to stdout, and exits. Stderr is reserved for bounded human diagnostics and may
not contain protocol data.

One-shot execution is normative in v0. The core cancels by cancelling/terminating
the child process and boundedly draining it. A persistent length-delimited
protobuf mode may be added behind a capability after measurements justify it;
no correctness behavior may require persistence.

### 3. Capability document

The minimum `describe` response is:

```json
{
  "protocols": ["weave.adapter/v0"],
  "provider": {"name": "weave-go", "version": "0.0.1"},
  "languages": ["go"],
  "operations": ["index"],
  "refresh_modes": ["full"],
  "fact_encoding": "weave.facts/v0",
  "position_encodings": ["utf8-byte"],
  "requires": {"executables": ["go"], "may_run_build_tool": true}
}
```

Fields are additive. Consumers ignore unknown fields within a supported major
protocol and reject an unsupported protocol. The exact JSON schema remains
experimental until fixture implementations exist for both Go and F#/.NET.

### 4. Index request

The minimum request identifies:

- a request ID;
- canonical repository root;
- repository identity when known;
- build variant and explicit project/package selection;
- prior known unit IDs/fingerprints when available (a hint in v0);
- allowed operations (`network`, dependency restore, build-tool invocation);
- whitelisted environment/build values;
- time, record, byte, and frame limits.

The adapter must not restore dependencies, access the network, run generators,
or execute repository scripts unless the corresponding request permission is
explicit. An adapter may report that precise indexing is unavailable under the
given policy.

`full` is the only required refresh mode in v0. Future adapters may advertise
`changed-units`; the core must never infer delta support from the presence of
fingerprints.

### 5. Frame lifecycle and atomicity

Every stdout line is one JSON frame with `protocol`, `request_id`, `kind`, and a
kind-specific payload. Required order:

```text
run.begin
  unit.begin
    facts          (zero or more bounded batches)
  unit.end         (counts, dependencies, status, fingerprints)
  ...
run.end            (complete unit inventory and run status)
```

Facts use `weave.facts/v0` normalized records. Each record retains provider,
provider version, evidence class, source range/position encoding, and original
provider identity. Fact batches have a negotiated maximum encoded size and are
not semantic transaction boundaries.

The core stages one compilation unit. It atomically replaces that unit's facts
only after validating its terminal frame, counts, references, and digests. A
nonzero exit, unsupported version, malformed/oversized frame, duplicate
terminal frame, missing `run.end`, or count/digest mismatch fails the refresh.
Previously committed facts remain intact but are stale until freshness succeeds.

Structured diagnostic frames are part of the protocol. A unit can be `complete`,
`partial`, `unavailable`, or `failed`; only `complete` is eligible for an exact
freshness claim. `run.end` inventories units so removed units can be detected
without interpreting absent facts as deletion.

### 6. Fingerprints are provider-owned, domain-separated values

Each complete unit may report:

- document content digests;
- a full semantic-input fingerprint;
- a public-surface fingerprint;
- a normalized fact-inventory digest.

The tuple is `(algorithm, domain, value)`. SHA-256 is the initial required
algorithm. The adapter defines and versions canonicalization domains; the core
stores and compares opaque values only when domains match.

Public-surface equality can stop reverse-dependency invalidation. Missing,
failed, changed-domain, or unequal surface fingerprints conservatively
propagate invalidation. Ordered inputs stay ordered (notably F# source files);
sets are canonically sorted.

### 7. Provider plan

- **Existing SCIP:** direct importer.
- **Go:** initially validate ingestion with `scip-go`; native reference adapter
  uses `go/packages` and `go/types`, with `scip-go`/gopls as prior art and
  conformance sources.
- **C#/VB:** initially execute `scip-dotnet` with restore disabled and explicit
  recorded variant; add a Roslyn adapter when explicit variants, unit
  fingerprints, or incremental behavior require it.
- **F#:** focused .NET adapter using `FSharp.Compiler.Service`; never claim
  Roslyn supplies F# semantics.

## Adopted prior art

- SCIP protobuf, symbol grammar, occurrence/range model, and indexer snapshot
  testing.
- `go/packages`, `go/types`, and appropriate public `x/tools` packages.
- Roslyn Workspaces/SemanticModel for C#/VB.
- FCS project checks, symbol uses, project references, and incremental caches.
- gopls's per-compilation-unit persistent summary architecture.
- Bazel worker transport discipline and future one-shot/persistent evolution.
- Bazel-style complete input identity, Gradle-style ABI avoidance, and
  Salsa-style stop-propagation-when-output-is-unchanged.

## Original Weave work

- repository/snapshot/build-variant qualification of provider symbols;
- evidence normalization and the richer graph vocabulary;
- the adapter capability/request/frame contract;
- atomic unit replacement and freshness/error semantics;
- cross-provider fingerprint envelope and invalidation propagation;
- Git/worktree lifecycle, compact bstore schema, queries, and federation.

This distinction is intentional: Weave composes existing semantic truth and
implements the missing local lifecycle.

## Consequences

Positive:

- useful multi-language coverage can arrive before every native adapter;
- compiler/runtime dependencies remain explicit and replaceable;
- the Go core stays cross-platform and testable with recorded frame fixtures;
- incremental correctness can improve without changing query/storage APIs;
- no daemon, network, or LLM is required.

Costs and risks:

- two ingestion paths must normalize to one fact model;
- batch SCIP producers do not provide fine-grained refresh by themselves;
- NDJSON has size/performance limits and must be bounded;
- toolchain and project evaluation differences must be recorded as variants;
- FCS API churn and .NET/Go SDK packaging require pinned contract fixtures.

## Rejected alternatives

- **SCIP as the complete internal model:** missing lifecycle, evidence, and
  invalidation facts.
- **LSIF as primary interchange:** more opaque graph machinery and poorer
  partial-index ergonomics than SCIP.
- **LSP as adapter protocol:** too broad and query-oriented; encourages a
  required resident server.
- **In-process compiler plugins:** runtime, crash, dependency, and licensing
  coupling to the Go core.
- **Tree-sitter as exact semantics:** useful fallback, not compiler resolution.
- **Framed protobuf immediately:** premature compatibility surface before two
  real adapters prove the record vocabulary.
- **RIBLT for local adapter refresh:** Git/direct hashes already provide exact
  local inventory differences; no measured remote reconciliation problem yet.

## Validation before acceptance

Change status to Accepted only after spikes demonstrate:

1. streaming SCIP ingestion with Unicode range fixtures;
2. one Go adapter fixture and one F#/.NET fixture using the same frame validator;
3. atomic rejection of truncated, oversized, malformed, and partial streams;
4. stable unit/public-surface fingerprints across two clean identical runs;
5. an implementation-only edit that leaves the public surface unchanged;
6. a public edit that invalidates the expected dependent unit;
7. indexing with dependency restore/network denied by default.
