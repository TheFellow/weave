# ADR 0005: Hardened SCIP and adapter ingestion

- Status: Accepted
- Date: 2026-08-06
- Extends: [ADR 0001](0001-semantic-interchange-and-adapters.md)
- Research: [SCIP and adapter protocol](../prior-art/scip-adapter-protocol/README.md)

## Context

ADR 0001 chose direct SCIP import and a one-shot NDJSON adapter boundary but
left exact resource, lifecycle, path, coordinate, and transaction behavior to a
fixture-backed spike. These boundaries consume repository-controlled files and
executable output, so permissive parsing would turn ordinary indexing failures
into stale or misleading graph facts.

## Decision

1. Weave pins the maintained `scip-code/scip` Go bindings. A direct import is
   explicitly requested; ordinary Go freshness never discovers or runs an
   external producer.
2. The initial SCIP reader bounds the complete protobuf before unmarshal. Every
   document path is canonical and repository-local. Filesystem source fallback
   additionally rejects symlink escapes.
3. Weave stores zero-based UTF-8 byte columns. SCIP UTF-16 and UTF-32 columns are
   converted against exact document text and invalid boundaries fail the run.
4. One SCIP document is one deterministic atomic Weave unit. Local symbol IDs
   are document-qualified. Global symbols preserve their original SCIP string
   inside a repository-qualified Weave identity.
5. Adapter v0 remains one-shot framed NDJSON. `describe` negotiates protocol and
   capabilities; `run.begin` initializes an `index` response. Every frame is
   correlated by request ID and validated in a strict lifecycle under per-frame,
   total-byte, frame, fact, diagnostic, and stderr limits.
6. The core launches an executable directly with argument arrays, an explicit
   repository working directory, bounded pipes, context cancellation, and a
   bounded wait. Stdout is protocol-only and stderr is diagnostics-only.
7. A SCIP import or adapter run is published only after the complete input has
   decoded and validated. Any malformed, partial, failed, cancelled, timed-out,
   or nonzero-exit run returns no replacement batches.
8. External adapter execution is an explicit CLI operation. It is not composed
   into the default Go freshness provider, because querying a Go repository
   must not unexpectedly execute repository-adjacent tools.
9. A direct SCIP import is a complete inventory for exactly one producer name,
   represented internally as `scip:<ToolInfo.name>`. Publication removes absent
   units only from that producer scope. `ToolInfo.version` remains recorded on
   every unit and participates in unit identity/fingerprints, but a version
   upgrade replaces the older inventory for the same producer instead of
   leaving stale side-by-side units. Other SCIP producers are never removed.

## Consequences

The first implementation has a conservative maximum `.scip` size and stages a
bounded normalized run in memory. That is simpler and safer than claiming
streaming while protobuf unmarshalling still materializes the top-level index.
Large-index streaming can be introduced without changing normalized storage.

Strict unknown-field rejection intentionally makes v0 experimental and catches
producer/consumer drift early. Compatibility can become additive after at least
two native adapters demonstrate which extension points are necessary.

SCIP navigation facts remain precise but intentionally less rich than a native
provider: ordinary occurrences produce references, not calls. Provider-specific
extensions may add richer edges only with explicit evidence semantics.
