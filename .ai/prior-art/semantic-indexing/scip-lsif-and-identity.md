# SCIP, LSIF, and symbol identity

## SCIP's useful contract

[SCIP](https://github.com/scip-code/scip) is a language-neutral Apache-2.0
protobuf format for code-navigation indexes. Its top-level `Index` contains
metadata, documents, and optional external symbols. A document supplies a
canonical project-relative path, language, occurrences, symbol information,
optional source text, and an explicit source-position encoding. The schema
allows compiler-precise and heuristic producers; precision is a producer
property, not something the file format proves.

The primary source is the documented
[`scip.proto`](https://github.com/scip-code/scip/blob/main/scip.proto). The
[indexer guide](https://sourcegraph.com/docs/code-navigation/writing-an-indexer)
also recommends compiler or language-server frontends, deterministic output,
and snapshot tests over real occurrences.

Facts directly worth adopting:

- canonical `/`-separated relative document paths;
- explicit UTF-8/UTF-16/UTF-32 position encodings;
- half-open source ranges;
- symbol occurrences with roles;
- symbol kind, display name, documentation, signature, and enclosing symbol;
- implementation/reference/type-definition relationships;
- tool name, version, and arguments;
- external symbols for separately indexed packages.

SCIP specifically says source text is optional and consumers should normally
read it from the project root. That aligns with Weave's goal of storing ranges
and bounded evidence instead of duplicating repositories.

SCIP's `Index` documentation warns that a complete message may have a large
memory footprint and requires metadata to appear first so a consumer can read
field values incrementally. Weave's importer must therefore avoid assuming a
whole index comfortably fits in memory. The schema's newer typed occurrence
ranges should be preferred, with the deprecated packed integer range accepted
for compatibility.

## Symbol grammar and identity limits

A non-local SCIP symbol has this conceptual shape:

```text
scheme + package(manager, name, version) + fully-qualified descriptors
```

A local symbol has the distinct `local <id>` form. The schema requires local
symbols to be used only for entities inaccessible outside one document. A
descriptor path is intended to be unique across its package; method
disambiguators distinguish overloads where required.

This is strong prior art for a provider-supplied semantic identity, but it is
not by itself a universal Weave primary key:

- local IDs are document-scoped;
- indexers can use placeholder package coordinates;
- workspace source may not yet have a published package version;
- the same source can be indexed under different build variants;
- provider versions may correct or change symbol composition;
- a symbol occurrence can be valid only for a particular snapshot/overlay.

Weave should preserve the original SCIP symbol string and parsed components.
It should resolve them inside an explicit identity domain:

| SCIP form | Minimum Weave qualification |
| --- | --- |
| `local <id>` | repository + snapshot/overlay + build variant + document + provider + local ID |
| package symbol with real coordinates | provider scheme + manager + package + version + descriptors; repository association retained separately |
| package symbol with placeholder/empty coordinates | repository + build variant + provider + descriptors |

This wrapper is original Weave lifecycle work. It does not redefine the SCIP
grammar. Cross-repository unification should occur only when package coordinates
or another exact declaration establish equivalence, never merely because two
display names match.

SCIP's `Relationship` currently represents reference, implementation, type
definition, and definition behavior. It does not directly model Weave's richer
edge vocabulary (calls, imports, tests, generates, reads/writes), compilation
units, snapshots, evidence classes, or invalidation fingerprints. Those belong
in Weave's normalized model.

## Why not LSIF as the primary format

[LSIF](https://github.com/microsoft/lsif-node) was designed to dump language
server knowledge so navigation queries can be answered without the live
server. Its implementation emits newline-separated JSON and models a graph of
vertices, edges, result sets, monikers, and package information.

SCIP is the smaller fit for Weave. SCIP replaced opaque graph IDs with
human-readable symbol strings and organizes occurrences by document. The SCIP
announcement explains that LSIF's global opaque IDs and ordering constraints
make partial/incremental index construction difficult; that account is
consistent with the LSIF implementation's ID-linked result-set graph. See
[“SCIP—a better code indexing format than LSIF”](https://sourcegraph.com/blog/announcing-scip)
and the [LSIF Node README](https://github.com/microsoft/lsif-node/blob/main/README.md).

Decision:

- implement SCIP ingestion first;
- do not emit LSIF or use LSIF as the internal graph;
- consider a separate LSIF-to-SCIP compatibility path only if a real producer
  lacks SCIP output;
- borrow NDJSON's debuggability for Weave's experimental adapter protocol, not
  LSIF's semantic graph shape.

## Validation requirements derived from SCIP

The importer should eventually test:

- project-root and relative-path canonicalization on all platforms;
- every declared position encoding, including astral Unicode characters;
- local symbol scope isolation;
- duplicate definitions and ambiguous relationships;
- unknown protobuf fields and old packed ranges;
- bounded strings, documents, occurrences, and diagnostics;
- deterministic normalization independent of protobuf/document order;
- indexes whose documents are streamed rather than held as one object.
