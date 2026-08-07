# ADR 0017: Source-only schema and declarative build provider

- Status: Accepted
- Date: 2026-08-07
- Research: [schema/build provider prior art](../prior-art/schema-build-providers/README.md)

## Context

Compiler and SCIP providers explain executable source, while workspace facts
explain files and documents. Important declared knowledge still falls between
them: schemas, API operations, migration resources, infrastructure addresses,
project identities, dependencies, and explicit generation mappings. These
relationships should reach every existing query/export/DOT/diff/explorer path
without another graph model or an unsafe build/evaluation step.

The formats are heterogeneous. Reimplementing their grammars in Go would repeat
the same mistake Weave avoids for programming languages. Some upstream parsers
also include loaders or evaluators that can read the network, filesystem, state,
or toolchain unless Weave constrains their input boundary.

## Decision

1. Add one built-in `weave-schema-build` freshness provider composed internally
   of six category parsers: Protobuf, OpenAPI 3, GraphQL, PostgreSQL migration
   DDL, Terraform HCL, and a documented declarative build-manifest subset.
2. Feed parsers only bounded Git-visible regular-file bytes. Do not execute or
   invoke a build, restore, generator, template, package manager, database,
   Terraform runtime/provider, repository hook, or network loader.
3. Reuse maintained upstream parsers: Buf protocompile, kin-openapi plus YAML
   source nodes, gqlparser, Bytebase Omni's PostgreSQL parser, HashiCorp HCL,
   x/mod, go-toml, and Go's JSON/XML parsers. Do not add a Weave grammar.
4. Publish normal `graph.UnitFacts` and construct every edge with the shared
   `relationship.Builder`. Automatic discovery never mutates authored bridge
   declarations. Existing storage, queries, catalog, aggregate, context, DOT,
   explorer, export, diff, watch, and CI consumers require no provider-specific
   path.
5. Use one atomic unit per category. Its normalized corpus fingerprint is the
   parser cache key. An unchanged category reuses its previous unit without
   invoking a parser; a changed category is completely relinked. A malformed or
   unsupported cross-file schema category publishes no partial semantic facts
   and removes only that category's prior unit with a bounded diagnostic. Build
   manifests are independently parseable: a malformed manifest is diagnosed
   and omitted while valid projects are relinked, and an entirely invalid build
   category is omitted. This conservative invalidation favors correctness over
   an early fine-grained parser cache without letting a test fixture erase an
   unrelated project graph.
6. Stable identities are domain-separated by provider schema, repository
   identity, semantic domain, and declared stable name; relative manifest/file
   paths disambiguate file-scoped identities without including checkout paths.
   Local links resolve only when an exact declaration and contained
   Git-visible target prove identity. Everything else is a stable open endpoint.
7. Use `Declared` for source declarations and their declared relationships,
   `Generated` only for explicit MSBuild `AutoGen` plus `DependentUpon`
   source/output mappings, `Inferred` for per-directory migration filename
   order, and `Syntactic` for executable GraphQL navigation. Never use `Exact`.
8. Retain source ranges, provider/version, and definitions/references. Compile
   Protobuf with at most two workers and use category fingerprint reuse so
   automatic refresh and CI do not repeatedly tax parser/tool resources.
9. Bound the Git inventory, each source, the aggregate source corpus, emitted
   facts, and diagnostics. Honor cancellation and ignore symlink or other
   non-regular inputs rather than following them outside the worktree.

## Build-manifest boundary

The first set is `go.mod`, `Cargo.toml`, `package.json`, Maven `pom.xml`, and
MSBuild C#/F#/VB project files. It models declared projects, explicit
dependencies, Cargo lib/bin targets, npm scripts, MSBuild targets, exact local
project paths, and the one proven MSBuild generation mapping. Imported SDK
targets and registry dependencies remain open endpoints.

Gradle and CMake are executable DSLs and are not parsed heuristically. Adding
them requires a maintained source-only representation or an explicitly
permissioned external adapter. The provider does not expand architecture rules
or CI policy; those remain optional graph consumers as previously decided.

## Consequences

Schema, infrastructure, migration, and project facts automatically participate
in the same graph as compiler/content/authored facts and rebuild from source.
Malformed OpenAPI cannot remove valid Protobuf facts, a malformed independent
project fixture cannot remove valid build facts, and a remote `$ref` cannot
cause HTTP or filesystem access. Category-wide relinking can be broader than a
single changed file, but publication is simple, cached when unchanged, and
cannot retain stale cross-file answers.

The SQL slice is explicitly PostgreSQL. OpenAPI is version 3 only. Terraform
JSON syntax and runtime semantics are absent. Build support is a useful
declarative subset, not a universal build engine. These limits are diagnostics
and documentation, not guessed relationships.
