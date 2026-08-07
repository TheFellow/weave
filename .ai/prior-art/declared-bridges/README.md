# Declared semantic bridges: prior art

Research date: 2026-08-06

Weave needs exact relationships that compilers cannot discover across schema,
generated-code, language, and repository boundaries. This is deliberately a
declaration mechanism, not another inference engine.

## Sources and lessons adopted

- [Bazel labels](https://bazel.build/concepts/labels) give targets canonical,
  repository-aware identities and keep dependency declarations exact. Weave
  adopts the same core property--an endpoint denotes exactly one semantic
  entity--but does not copy Bazel's package-relative abbreviation rules.
- [Nx project configuration](https://nx.dev/docs/reference/project-configuration)
  uses `implicitDependencies` for relationships static analysis cannot derive.
  Weave similarly keeps manually supplied relationships visibly declared and
  separate from compiler evidence.
- [Nx's project graph API](https://nx.dev/docs/reference/devkit/ProjectGraph)
  treats dependencies and external nodes as ordinary graph data. Weave likewise
  normalizes bridges into its existing edge vocabulary instead of creating a
  parallel query engine.
- [Buf generation configuration](https://buf.build/docs/generate/) checks in
  versioned inputs/plugins/output declarations, including inputs from Git and
  remote modules. This supports treating schema-to-generated-source links as
  reproducible configuration and labeling generated edges distinctly.

## Decision

Use strict, bounded JSON at `.weave/bridges.json`, with schema
`weave.bridges/v1`. Each endpoint uses the stable grammar `symbol:<symbol-id>`;
the suffix is an exact Weave/SCIP/native semantic symbol ID, not a search term.
There are no relative names, wildcards, inference, or fallback resolution.

Only `depends-on`, `documents`, and `generates` are accepted initially.
`generates` is normalized with `generated` evidence; the other kinds use
`declared` evidence. Bridge facts enter the same storage, export, traversal,
federation, verification, and architecture-policy paths as compiler edges.

JSON is already in the Go standard library, allows strict unknown-field
rejection, and avoids adding or maintaining a custom parser.

## Rejected for now

- Parsing BUILD/Starlark, project.json, MSBuild, or Buf configuration directly:
  useful future adapters, but each has ecosystem-specific semantics and would
  turn an exact bridge layer into another build-system implementation.
- Glob endpoints: concise but ambiguous under rename and incompatible with
  exact cross-repository joining.
- Automatically matching names across languages: violates the evidence model;
  equal spelling is not semantic identity.

