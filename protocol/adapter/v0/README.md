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
| `claims.inputs` | Lowercase extensions, exact base filenames, and project markers used for deterministic activation/routing. |
| `claims.evidence` | Evidence classes this adapter may emit. |
| `claims.fallback` | Optional broad syntactic fallback; precise active claims always win. |

All values are data, not shell fragments. Provider identity establishes fact
ownership; it must not trigger provider-specific behavior in the core.

Claims are normalized, sorted, and hashed with the complete capability document
when an executable is installed. Automatic execution verifies that pin before
indexing. Two precise providers may not claim the same extension, filename, or
concrete input path. A fallback receives inputs only when no precise provider
owns them. The host supplies the resulting concrete `input_paths` allowlist.
Fallback adapters must emit a complete inventory only for those paths, and the
host rejects fallback documents outside the routed set.

## Language-neutral conformance

[`conformance/`](conformance/) contains the machine-readable case inventory, a
genuine tiny repository, malformed inputs, and a Python fixture adapter that
imports no Go code. Any executable can be tested as a black box:

```text
weave adapters conformance ADAPTER --fixture /path/to/genuine/project --json
```

The runner negotiates `describe`, requires wrong-protocol and malformed-request
failure exits, indexes the fixture twice and compares normalized facts, keeps
stderr outside protocol data, and proves the host's byte bounds. Adapters whose
fixture needs build/restore/network/generator behavior require the matching
explicit conformance flags; no permission is inferred from a provider name.

## Index request

The request contains:

- `protocol` and a host-generated `request_id` echoed by every response frame;
- an absolute `repository_root` and optional stable `repository_identity`;
- an optional build `variant`, changed-path hints, routed `input_paths`, and an
  allowlisted environment;
- explicit `permissions` for network, restore, build tools, and generators;
- byte, frame, and fact limits selected by the host.

A false permission is a prohibition, not a hint. Changed paths are hints unless
the adapter advertised a compatible incremental refresh mode. A full-refresh
adapter must return its complete provider-owned unit inventory. `input_paths`
are the concrete result of claim routing; precise compiler providers may read
related project metadata, while a fallback's emitted source documents must stay
inside this set.

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
Queries should prefer those complete occurrences. Until the first stable wire
specification is published, this document, the fixtures, and the Go model must
change together. A future stable encoding is not required to be protobuf.

A symbol may include up to 2,048 sorted unique `search_terms`. Each value is one
lowercase normalized identifier token of at most 128 bytes. Terms make the
exact entity discoverable by additional provider-owned lexical concepts—for
example, prose in a Markdown section. They are not source text, semantic facts,
or evidence and cannot imply an edge. Providers should omit them unless they
can deterministically derive them from that entity's authoritative input.

## Additive enrichment and composition

Each successful run replaces only the complete inventory owned by the run's
provider. It cannot replace, relabel, or delete another provider's facts. The
host rejects documents, symbols, occurrences, and edges whose provider identity
does not match their enclosing unit and run.

Providers enrich the shared graph by returning additional provider-owned facts.
An edge may connect two local symbols, an existing stable entity ID learned from
a documented namespace, or an open endpoint that another provider or a manually
authored contextual link may later satisfy. Owning an edge never implies owning
its endpoints. Precise SCIP/compiler facts, structured-content facts, broad
syntactic outlines, generated-schema bridges, and declared human relationships
therefore coexist without a provider calling another provider or writing the
database directly.

The core unions these observations and preserves their provider and evidence.
Reconciliation may add an explicitly evidenced relationship between uniquely
matching observations; it must not silently merge them or upgrade a heuristic
to compiler truth. Future additive request fields may supply bounded read-only
anchors from the existing graph for conservative matching, but those anchors
remain host-owned context and can never enter the adapter's replaceable
inventory.

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
Stable v1 will be defined by a checked-in language-neutral wire specification,
compatibility rules, fixtures, and a reusable executable conformance suite. It
will retain this one-shot mode. A persistent worker transport may later be
negotiated for expensive compiler startup, but it will not replace the one-shot
contract.

Generated bindings and helper SDKs are optional. An adapter in C#, F#, Rust,
Java, Kotlin, TypeScript, Python, or another language is conformant if its bytes
and behavior satisfy the same contract.
