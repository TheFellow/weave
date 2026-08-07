# SCIP ingestion and one-shot adapter protocol

## Scope

This review covers the two untrusted-input boundaries introduced by Weave's
first external semantic integrations: reading a producer-owned `.scip` file and
running a local executable adapter. The implementation should reuse SCIP's
maintained generated Go bindings and standard Go process/path primitives rather
than inventing either a semantic schema or a process supervisor.

## SCIP schema and maintained bindings

SCIP is an Apache-2.0 protobuf schema maintained by the
[`scip-code/scip`](https://github.com/scip-code/scip) project. Its repository
ships rich generated Go bindings at
[`github.com/scip-code/scip/bindings/go/scip`](https://pkg.go.dev/github.com/scip-code/scip/bindings/go/scip),
including range and symbol validation helpers. Weave should pin a tagged module
release and use `google.golang.org/protobuf/proto` to decode the wire format.
Vendoring or regenerating `scip.proto` would create unnecessary schema drift.

The authoritative
[`scip.proto`](https://github.com/scip-code/scip/blob/main/scip.proto) establishes
the semantics Weave needs to preserve:

- document paths are canonical, project-relative, `/`-separated paths;
- ranges are zero-based and half-open, encoded as three integers for one line
  or four integers for multiple lines;
- a document declares whether character offsets count UTF-8 bytes, UTF-16 code
  units, or UTF-32 code points;
- an occurrence carries a symbol plus bitwise definition/import/read/write/test
  roles;
- symbol information provides display name, kind, enclosing symbol, and
  relationships for reference/implementation/type-definition/definition
  equivalence;
- metadata records the producing tool and project-root relationship;
- local symbol identities are document-scoped, while global symbol strings use
  SCIP's package-and-descriptor grammar.

SCIP explicitly permits source text to be absent and warns that a complete
index can be large. Protobuf's own
[techniques guide](https://protobuf.dev/programming-guides/techniques/) likewise
recommends composing large data sets from bounded messages rather than treating
one huge protobuf as an ideal streaming container. The current SCIP top-level
wire format is nevertheless one `Index`; an initial direct importer can bound
the file before unmarshalling. True field-by-field streaming is deferred until
measurements justify a wire scanner or upstream streaming helper.

### Normalization choices

- Preserve every original SCIP symbol string as the stable semantic name.
- Qualify `local ...` symbols by repository identity and document path so equal
  producer-local IDs in two files cannot collide.
- Qualify workspace symbols by repository identity; retain already-global SCIP
  package coordinates in the stable name so later cross-repository resolution
  remains possible.
- Make one deterministic Weave unit per SCIP document. This bounds atomic
  replacement and prevents local symbols from escaping their document.
- Treat compiler-backed SCIP navigation facts as `Exact`, but do not invent
  `Calls` from an undifferentiated reference occurrence.
- Map definition occurrences to symbol definition locations; map ordinary
  occurrences to `References`; map relationship implementations to
  `Implements`. Read/write roles can additionally yield `Reads`/`Writes`.
- Convert columns to Weave's UTF-8-byte convention from document source bytes.
  UTF-16 conversion must count surrogate pairs and reject a column in the
  middle of a pair; UTF-32 conversion counts Unicode scalar values. If source
  is absent, non-UTF-8 positions cannot be represented exactly and are rejected
  rather than mislabeled.

## Paths and repository containment

Go's [`filepath.IsLocal`](https://pkg.go.dev/path/filepath#IsLocal) is the
right lexical first check after converting SCIP's slash path with
`filepath.Localize`: it excludes empty, absolute, parent-traversing, and Windows
reserved paths. Lexical containment alone does not resolve symlinks. An importer
that reads source from disk must also evaluate the repository root and the
existing target with `filepath.EvalSymlinks`, then verify the target remains
under the resolved root. Embedded `Document.Text` needs only lexical path
validation because no filesystem traversal occurs.

The `.scip` file itself is an explicit user-selected input; its documents are
data, not instructions. Import never runs producer commands or repository
scripts.

## One-shot process protocol prior art

JSON Lines requires UTF-8 and one complete JSON value per line
([jsonlines.org](https://jsonlines.org/)). This gives v0 a simple frame boundary,
but `bufio.Scanner`'s token behavior is too easy to configure accidentally and
does not itself impose a total-stream bound. A bounded reader that reads through
newline should enforce all of:

- maximum bytes per frame, including a final unterminated frame;
- maximum total stdout bytes and frame count;
- valid UTF-8, no blank frames, one JSON object, and no trailing JSON value;
- strict known fields for v0, so misspelled security- or lifecycle-sensitive
  fields fail closed;
- a fixed lifecycle and matching protocol/request ID on every frame.

The [Language Server Protocol initialization and cancellation
model](https://microsoft.github.io/language-server-protocol/specifications/lsp/3.18/specification/)
supports explicit capabilities and correlated requests, but its long-lived
bidirectional RPC surface is broader than Weave needs. Bazel's
[persistent worker protocol](https://bazel.build/remote/creating) is closer:
arguments are arrays, stdout is protocol-only, stderr is diagnostics, request
IDs correlate work, and one-shot execution can precede persistent workers.

Go's [`os/exec`](https://pkg.go.dev/os/exec) provides the necessary boundary:
`exec.CommandContext(name, args...)` avoids shell interpolation, cancellation
kills the child by default, `WaitDelay` bounds pipe shutdown after cancellation,
and separate stdout/stderr pipes keep human diagnostics out of frames. The core
should use an explicit working directory and a small inherited environment;
adapter discovery must return an executable path, never a shell fragment.

## Versioned v0 contract

Retain ADR 0001's one-shot two-command shape:

```text
adapter describe --protocol weave.adapter/v0
adapter index    --protocol weave.adapter/v0
```

`describe` returns one bounded JSON capability document. `index` receives one
bounded request line and emits:

```text
run.begin
  unit.begin
  facts (one or more batches)
  unit.end
run.end
```

Each frame contains `protocol`, `request_id`, `kind`, and one kind-specific
payload. `initialize` is represented by the mandatory `run.begin`, which echoes
the selected fact encoding, provider, and capabilities established by
`describe`. A structured `diagnostic` frame may appear only inside a run;
stderr remains bounded unstructured operator context.

The consumer stages the entire run in memory under explicit unit/fact/byte
limits, validates all IDs, ownership, counts, unit inventory, terminal state,
and normalized graph batches, and only then returns facts to storage. It never
publishes at `unit.end`. This is stricter than unit-local publication and makes a
truncated multi-unit run unambiguously atomic.

## Hostile and malformed output policy

Reject the whole run on unsupported protocol/fact encoding, request mismatch,
unknown frame kind, lifecycle disorder, duplicate unit or fact IDs, invalid
graph facts, missing or duplicate terminals, partial/failed status, count
mismatch, oversized frame/stream/stderr, malformed JSON, nonzero exit, timeout,
or cancellation. Preserve the prior database and freshness manifest.

Adapter diagnostics are data, not trusted terminal markup. They are returned as
bounded strings and written only to stderr by the CLI. A child failure includes
bounded stderr context but never reclassifies it as protocol output.

## Adopt, wrap, defer

Adopt:

- maintained SCIP generated bindings and symbol/range semantics;
- standard protobuf decoding, `os/exec`, context cancellation, and Go path
  containment primitives;
- LSP/Bazel lessons for negotiation, request correlation, stdout discipline,
  and evolution from one-shot to persistent operation.

Implement in Weave:

- repository/snapshot qualification, evidence mapping, UTF coordinate
  normalization, resource limits, lifecycle validation, and atomic publication;
- a deliberately small normalized fact stream shared by native adapters.

Defer:

- persistent/multiplexed adapters and in-protocol cancellation messages;
- field-streaming a single giant SCIP protobuf;
- automatic indexer discovery/invocation profiles (each producer has different
  restore/build/network behavior and requires an explicit policy contract);
- deriving call edges from SCIP references without producer-specific evidence.
