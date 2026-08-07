# Weave adapter protocol v0

Status: experimental. Compatibility is not promised before v1.

This is the language-neutral contract between the Weave core and an executable
semantic adapter. It deliberately follows the protoc plugin shape: the host
starts a subordinate process, supplies a known request on stdin, receives known
output on stdout, and does not care which language implemented the process.

The golden files in this directory are consumed by Weave's Go contract tests
and are suitable as fixtures for adapters written in other languages.

## Executable surface

```text
<adapter> describe --protocol weave.adapter/v0
<adapter> index    --protocol weave.adapter/v0
```

The host passes arguments literally without a shell. It sets the repository as
the working directory for `index` and supplies a small allowlisted environment.

`describe` writes exactly one UTF-8 JSON object followed by EOF. The object
advertises supported protocol versions, provider identity, languages,
operations, refresh modes, fact and position encodings, and runtime/build-tool
requirements. See [`describe.json`](describe.json).

`index` reads exactly one UTF-8 JSON request followed by EOF. It emits one JSON
object per line on stdout and exits after `run.end`. See
[`index-request.json`](index-request.json) and
[`index-response.ndjson`](index-response.ndjson).

Stdout is protocol-only. Stderr is bounded operator context and never contains
facts. A malformed request is a process/protocol failure. Repository semantic
failures belong in structured `diagnostic` frames followed by an unsuccessful
terminal state once v0 supports partial states; the current core accepts only a
complete run.

## Capability document

Required fields:

| Field | Meaning |
| --- | --- |
| `protocols` | Protocol versions the executable can speak. |
| `provider.name`, `provider.version` | Stable fact owner and implementation version. |
| `languages` | Lowercase language IDs understood by the adapter. |
| `operations` | Must include `index`. |
| `refresh_modes` | `full` is the v0 compatibility floor; `changed-units` is reserved. |
| `fact_encoding` | Must be `weave.facts/v0`. |
| `position_encodings` | Must include an encoding understood by the host; currently `utf8-byte`. |
| `requires` | External executables and whether project evaluation may invoke a build tool. |

All values are data, not shell fragments. Provider identity establishes fact
ownership; it must not trigger provider-specific behavior in the core.

## Index request

The request contains:

- `protocol` and a host-generated `request_id` echoed by every response frame;
- an absolute `repository_root` and optional stable `repository_identity`;
- an optional build `variant`, changed-path hints, and allowlisted environment;
- explicit `permissions` for network, restore, build tools, and generators;
- byte, frame, and fact limits selected by the host.

A false permission is a prohibition, not a hint. Changed paths are hints unless
the adapter advertised a compatible incremental refresh mode. A full-refresh
adapter must return its complete provider-owned unit inventory.

## Response lifecycle

Each line has `protocol`, `request_id`, `kind`, and `payload`. Required order:

```text
run.begin
  unit.begin
    facts       (zero or more bounded batches)
    diagnostic (zero or more, after run.begin)
  unit.end
  ...
run.end
```

- `run.begin` repeats the negotiated provider and fact encoding.
- `unit.begin` declares an atomically replaceable compilation unit. Its provider
  name and version match `run.begin`.
- `facts` contains any combination of `documents`, `symbols`, `occurrences`, and
  `edges` from the normalized graph model.
- `diagnostic` has severity `info`, `warning`, or `error`, a nonempty message,
  and an optional unit ID.
- `unit.end` has status `complete` and exact record counts for the unit.
- `run.end` has status `complete` and the exact, duplicate-free unit inventory.

IDs are nonempty UTF-8 strings and globally unique across all documents,
symbols, occurrences, and edges within a run. Repository paths use `/`. Ranges
are half-open and zero-based. `utf8-byte` columns count UTF-8 bytes. Every fact
identifies its unit and provider, and semantic symbols/occurrences/edges carry
an evidence class.

The current edge vocabulary and record shapes are defined by
`weave.facts/v0`; the golden response exercises a minimal valid document and
symbol. A symbol's `definition` is its canonical display anchor; providers may
emit several `definition` occurrences for languages with repeated bindings.
Queries should prefer those complete occurrences. Until a protobuf v1 schema is
published, the Go model and this fixture must change together.

The built-in workspace provider owns one path symbol for every Git-visible
file. An adapter can join compiler truth to that symbol by emitting an exact
`defines` edge from this provider-neutral endpoint to each declaration it
resolves in the file:

```text
"workspace-file:" + hex(sha256(
  "weave-workspace/v1\0file\0" + repository_identity + "\0" + repository_path
))
```

`repository_path` is the exact case-sensitive `/`-spelled Git path. The adapter
does not emit or own the path symbol; an absent workspace provider is a valid
open endpoint. The ID excludes checkout location, branch, content hash,
toolchain, and provider version. The Go provider implements this join first;
other adapters may adopt it without changing the wire format.

## Atomicity, bounds, and failure

The host stages and validates the complete stream before publishing anything.
It rejects unsupported versions or encodings, unknown fields or frame kinds,
wrong request IDs, lifecycle disorder, duplicate IDs, invalid facts, count or
inventory mismatches, resource-limit violations, missing terminals, timeouts,
cancellation, and nonzero exit. Previously committed facts remain untouched.

Adapters must stop when stdout closes or the host terminates the process. They
must not write Weave's database, contact another adapter, restore dependencies,
access the network, execute repository code, or run generators unless the
request explicitly permits the relevant action.

## Evolution

Additive v0 experimentation may occur before the first compatibility promise.
Stable v1 will be defined by a checked-in protobuf schema and retain this
one-shot mode. A persistent worker transport may later be negotiated for
expensive compiler startup, but it will not replace the one-shot contract.

Generated bindings and helper SDKs are optional. An adapter in C#, F#, Rust,
Java, Kotlin, TypeScript, Python, or another language is conformant if its bytes
and behavior satisfy the same contract.
