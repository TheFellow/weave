# CodeGraph and an animated local explorer

This note audits two pieces of prior art for features that Weave can adapt without
giving up its stronger semantic and local-first foundations:

- [CodeGraph](https://github.com/colbymchenry/codegraph/tree/969ea1ec371dc62d056cbeb3920fa79036128842), commit `969ea1ec371dc62d056cbeb3920fa79036128842`, package version 1.5.0, MIT.
- [d3-graphviz](https://github.com/magjac/d3-graphviz/tree/355158dc789ff8556549018a0c0f7a567ac0bfc3), commit `355158dc789ff8556549018a0c0f7a567ac0bfc3`, package version 5.6.0, BSD-3-Clause.
- [Graphviz Visual Editor](https://github.com/magjac/graphviz-visual-editor), MIT.

The audit is pinned because both products will evolve. Statements below describe
those commits, not every past or future release.

## Conclusion

CodeGraph has several product ideas worth adapting: a compact query language,
source-rich exploration, explicit index-health reporting, affected-test UX,
framework-aware enrichment, and self-contained platform releases. Its broad
tree-sitter and heuristic model is useful as a fallback, but it must not displace
Weave's SCIP/LSP/compiler precision, explicit evidence levels, stable identities,
open contextual links, per-worktree bstore databases, or query-time Git freshness.

The right synthesis is:

1. Exact and declared facts from compiler-grade providers remain authoritative.
2. Framework and generic mappers add provider-owned facts with explicit rule,
   version, evidence, diagnostics, and source ranges.
3. Every query still verifies Git/worktree freshness. A watcher may reduce
   latency, but never becomes the correctness boundary.
4. A human-only local explorer consumes the same bounded deterministic DOT and
   source-query surfaces as the CLI. It does not introduce a second graph model.

## Architectural comparison

| Concern | CodeGraph at the audited commit | Weave direction |
| --- | --- | --- |
| Extraction | Native Rust tree-sitter kernel for 20 languages with WASM fallback | SCIP/LSP/compiler providers first; syntax mapper as a broad fallback |
| Storage | Per-project SQLite/FTS5 in `.codegraph/`, WAL | Retain bstore, per-worktree state, catalog, and deterministic rebuilds |
| Identity | Hash of path, kind, name, and line | Retain stable semantic/provider identities; line movement must not reidentify a symbol |
| Resolution | Exact, import, framework, fuzzy, instance, file-path, and function-reference rules | Preserve evidence taxonomy and ambiguity; no heuristic may masquerade as exact |
| Endpoints | Edges require materialized source and target nodes | Retain open endpoints and contextual cross-repository links |
| Freshness | Active watcher, catch-up sync, optional Git hooks, stale warnings | Query-time Git freshness is authoritative; watcher is optional acceleration |
| Multi-repo | Nested repositories can be scanned into one project | Retain repository/worktree boundaries plus catalog and explicit cross-repo links |
| Query UX | CLI, one default MCP explore tool, Markdown and JSON | CLI schema is canonical; optional protocol wrappers remain thin |
| Network | Local graph, but default-on telemetry and update checking | No telemetry or network activity by default |

## CodeGraph findings

### CLI and query ergonomics

CodeGraph exposes a coherent progression from lifecycle to graph use:
`init`, `index`, `sync`, `status`, `query`, `explore`, `node`, `files`,
`callers`, `callees`, `impact`, and `affected`. The
[CLI reference](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/site/src/content/docs/reference/cli.md)
documents stable text and JSON modes, stdin support for affected paths, filters,
depth limits, and quiet behavior suitable for scripts.

Search accepts free text plus `kind:`, `lang:`/`language:`, `path:`, and `name:`
fields. Its implementation uses FTS5, name segmentation, bounded fuzzy fallback,
and generated-source down-ranking. Weave should adapt the field-qualified UX,
extended with its own graph concepts: `repo:`, `provider:`, and `evidence:`.
The parser and result schema should be deterministic and shared by text, JSON,
and any later MCP wrapper.

CodeGraph deliberately publishes only
[`codegraph_explore`](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/mcp/tools.ts)
as its default MCP tool, after observing that agents frequently chose narrower
tools poorly. The lesson is to provide one good high-level exploration operation,
not to make MCP the architecture. Weave's CLI should stay canonical; an MCP or
other adapter can translate to the same request and response types.

### Source-rich exploration

CodeGraph's strongest user-facing feature is `explore`. It combines natural-name
and symbol/file search, graph expansion, ranking, source retrieval, relationship
paths, synthesized edges, blast radius, and tests. The output is bounded according
to repository size, limits file count and per-file source, and points to omitted
files instead of silently truncating them. It also suppresses duplicate source in
a session and restores enough context when deduplication would otherwise make a
response useless. See the
[explore implementation](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/mcp/tools.ts),
[deduplication logic](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/mcp/explore-dedup.ts),
and [diagnostics](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/mcp/explore-diagnostics.ts).

Before returning indexed source slices, it compares current file content to the
indexed hash. On drift it warns and omits ranges that may now be wrong. Weave can
do better because its normal query path already updates stale units: an eventual
`weave explore` should first use that freshness contract, then return current,
line-numbered source with graph paths, evidence, provider/rule provenance,
diagnostics, omissions, and stable JSON.

The implementation is also a warning. The central tools file exceeds six thousand
lines and contains many tuned ranking, token-budget, and agent-coaching heuristics.
Weave should build a small deterministic exploration plan over existing query
primitives, keep ranking stages independently tested, and avoid instructions such
as telling a consuming agent which filesystem tools it may use.

### Framework resolution and synthesizers

CodeGraph separates syntactic extraction from a broad framework registry and
post-extraction synthesizers. The
[framework registry](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/resolution/frameworks/index.ts)
detects and resolves conventions for web frameworks across JavaScript/TypeScript,
Python, Ruby, Java, Go, Rust, .NET, Swift, infrastructure configuration, and more.
Its [resolution types](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/resolution/types.ts)
record confidence and mechanisms such as exact, import, qualified, framework,
fuzzy, instance-method, file-path, and function-reference resolution.

Postprocessors synthesize relationships that a local AST does not directly state:
callback registration, event handlers, routes, C/C++ function pointers and
overrides, Go interface dispatch and gRPC, cross-language mobile bridges,
MyBatis XML/Java mappings, Redux and framework stores, Celery tasks, UI render
edges, and others. Representative implementations are the
[callback synthesizer](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/resolution/callback-synthesizer.ts),
[C function-pointer synthesizer](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/resolution/c-fnptr-synthesizer.ts),
and [Go framework synthesizer](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/resolution/goframe-synthesizer.ts).
Metadata includes the synthesizer, registration site, field or event, and the
indirect mechanism, and exploration renders that provenance.

This is the correct extensibility shape but not an evidence model to copy
verbatim. Some framework results are assigned confidence `1.0` even though they
remain convention-derived. In Weave:

- Provider identity, provider version, rule identity, rule version, source ranges,
  and diagnostics must be first-class.
- `exact`, `declared`, `generated`, `inferred`, `syntactic`, and `ambiguous`
  evidence must retain their meanings. A numeric score is additional ranking
  information, never a replacement.
- Enrichment is deterministic, scoped to provider-owned output, and invalidated
  when its input facts, configuration, or provider/rule version changes.
- Enrichment may add facts or explain ambiguity but cannot overwrite or downgrade
  compiler-grade facts.
- Generic mapping should be useful for unsupported languages and formats, but its
  output must remain visibly syntactic or inferred.

Start with a few frameworks represented by real repositories and fixtures rather
than copying a large registry. Route declarations, manifest/configuration links,
and common callback registration are likely high-value first slices.

### Auto-sync and index health

CodeGraph's [watcher](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/sync/watcher.ts)
uses recursive native watching on macOS and Windows and per-directory inotify on
Linux, with an explicit directory cap. Small edits use a short quiet period;
larger batches debounce longer. Event storms and directory removal fall back to a
scan diff. Lock and transient failures retry with bounded exponential backoff, and
degradation is surfaced. WSL-mounted paths disable watching unless explicitly
forced. Optional, marker-delimited
[Git hooks](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/sync/git-hooks.ts)
run background synchronization after checkout, merge, and commit.

The implementation is thoughtful, but the product claim that the graph is never
stale is too strong. Watching depends on a running process, can be disabled or
degraded, and CodeGraph itself contains content-drift and
[worktree mismatch checks](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/sync/worktree.ts).
Weave should retain query-driven Git freshness as the correctness path. A watcher
can eagerly enqueue changed units and improve interactive latency, but every query
must still verify its snapshot. Git hooks may be opt-in accelerators, never
required mutations of a user's repository.

The status surface is worth adapting. CodeGraph reports complete, partial, failed,
or indexing states; extraction version; pending references; stale builds; watcher
and lock health; and worktree mismatch. Weave should expose the analogous facts per
worktree and provider, including incomplete units and diagnostics. Machine-readable
health lets CI and agents distinguish an empty result from an incomplete index.

### Affected tests

[`affected`](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/site/src/content/docs/guides/affected-tests.md)
accepts changed paths directly or over stdin and walks a file-level projection of
resolved cross-file semantic edges. It identifies tests with path/name patterns or
custom globs and emits both human and JSON results. This is good CI ergonomics.

Weave should layer the same interface over its impact traversal and Git diff
support, but an affected-test answer must include explainable paths and their
evidence. Compiler-indexed test declarations and explicit `tests` edges are strong;
filename inference must be labeled. An incomplete graph must not produce a
confident false-empty answer. JSON should distinguish `complete`, `partial`, and
`unknown`, while CI policy chooses whether partial results fail, warn, or run a
broader fallback suite.

Tests should cover positive and negative fixtures, rename/delete cases, provider
failure, ambiguous dispatch, branch/worktree changes, explicit and inferred tests,
depth and filter bounds, stdin, stable JSON, and safe behavior when the index is
partial.

### Packaging and supply chain

CodeGraph's
[release workflow](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/.github/workflows/release.yml)
builds native Rust kernels and self-contained bundles for macOS, Linux, and Windows
on x64 and arm64. It tests native/reference parity, publishes per-platform npm
packages with npm provenance, writes SHA-256 checksums, and creates GitHub artifact
attestations. Those parity and provenance checks are excellent models for Weave's
core and extension binaries.

There are important gaps at this commit:

- [`install.sh`](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/install.sh)
  downloads and installs an archive without verifying the published checksums or
  attestation.
- [`build-bundle.sh`](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/scripts/build-bundle.sh)
  downloads a pinned Node runtime without verifying an upstream digest.
- The release workflow pushes generated changes directly to the default branch
  with an administrative token and relies on mutable major-version action tags.
- At the audited commit, the repository has release and site-deployment workflows
  but no normal pull-request/push test workflow, despite a large test suite.
- Telemetry and update checking are opt-out rather than opt-in; see
  [`TELEMETRY.md`](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/TELEMETRY.md).

Weave should keep its small Go core and separate platform adapter artifacts, then
add SBOMs, GitHub artifact attestations, checksum/signature verification in any
installer, packaged-binary smoke tests, and cross-platform protocol conformance.
Reproducible metadata and `-trimpath` remain preferable. Core operation must not
phone home.

## Animated human explorer with d3-graphviz

Magjac's Graphviz Visual Editor validates the interaction model: pan and zoom,
click selection, layout-engine controls, SVG export, and animated transition as
DOT changes. Weave should adapt that immediacy, not its general-purpose editing
surface. The graph remains generated from current semantic facts; a click
changes focus or filters rather than silently editing source or derived state.

d3-graphviz renders DOT through `@hpcc-js/wasm` Graphviz, converts the SVG to D3
data, and animates the data join between layouts. It supports fading entering and
exiting nodes and edges, growing entering edges, path and shape tweening, pan/zoom,
and worker-based layout. The
[README](https://github.com/magjac/d3-graphviz/blob/355158dc789ff8556549018a0c0f7a567ac0bfc3/README.md)
and [transition implementation](https://github.com/magjac/d3-graphviz/blob/355158dc789ff8556549018a0c0f7a567ac0bfc3/src/transition.js)
show that each new DOT snapshot is laid out and joined against the current SVG.
A named transition is recommended when zoom behavior is also installed.

Object constancy is the key integration requirement. The default
[`title` key mode](https://github.com/magjac/d3-graphviz/blob/355158dc789ff8556549018a0c0f7a567ac0bfc3/src/keyMode.js)
uses Graphviz SVG titles, typically a DOT node ID or `a -> b`. Its `id` mode uses
Graphviz IDs, which are only stable when the producer supplies explicit `id`
attributes. Weave's current sorted transient IDs such as `n0` and `n1` will shift
as nodes enter or leave a bounded graph. Before adding animation, the DOT writer
must emit stable, escaped node and edge IDs derived from semantic fact identities.
Collapsed or parallel edges need stable identities too. The CLI's deterministic
DOT output remains the canonical snapshot.

A suitable local interaction is:

1. The Go process serves only a random loopback port, with an unguessable session
   token and a restrictive content security policy.
2. It embeds pinned JavaScript, WASM, licenses, and static assets; it performs no
   network requests.
3. The UI requests a bounded DOT snapshot and source facts using the same query
   request types as the CLI.
4. Changing focus, depth, kinds, providers, or evidence requests another snapshot
   and animates the stable-ID transition.
5. Clicking a node requests current source and provenance; source is escaped text,
   never injected HTML.

Path and shape tweening become expensive on large graphs, including dense or
random models such as Erdős–Rényi graphs. Use a web worker, disable or simplify
tweening above measured thresholds, honor `prefers-reduced-motion`, and terminate
the worker when the explorer closes. 3Blue1Brown's Manim can later
consume the same deterministic snapshots for curated offline animation, but it
must not be coupled to the core or interactive UI.

Explorer tests should include stable IDs across snapshot insertion/removal,
enter/exit transitions, parallel edges, zoom plus named transitions, reduced
motion, large-graph fallback, malicious labels, CSP/no-network behavior, loopback
binding, token and path-traversal rejection, worker cleanup, and headless visual
screenshots.

## Prioritized adaptation matrix

| Priority | Increment | Adapt | Preserve / reject | Acceptance signal |
| --- | --- | --- | --- | --- |
| P0 | Source-rich `weave explore` and `weave show` | Bounded current source, relationship paths, evidence/provenance, diagnostics, omissions, text and JSON | Do not copy the monolithic MCP implementation or agent-coaching prose | Deterministic golden JSON/text; stale edits refresh before ranges are returned; strict budgets |
| P0 | Enrichment/synthesizer contract | Provider/rule/version-owned post-index facts and diagnostics | Exact facts are immutable; evidence remains categorical; deterministic invalidation | Protocol conformance plus positive/negative fixtures and version/input invalidation tests |
| P0 | Initial semantic mappers | Routes, manifests/config links, and callback registration selected from real repositories | No broad framework catalog until fixtures demonstrate value | Each mapper reports wiring ranges, rule provenance, ambiguity, and no false-positive fixture |
| P0 | `weave affected` | Changed paths/stdin, filters/depth, explanatory paths, JSON and CI-friendly exits | No confident false-empty on partial graphs; filename tests are inferred | Complete/partial/unknown cases and safe configured fallback behavior |
| P0 | Release trust | Per-platform adapter binaries, checksums, SBOMs, attestations, packaged smoke and protocol tests | No unverified curl installer, admin-token branch mutation, or network dependency | Fresh-machine verification of digest/attestation and extension handshake on each target |
| P1 | Qualified query language | `kind:`, `lang:`, `path:`, `name:`, `repo:`, `provider:`, `evidence:` and generated ranking | Keep bstore; do not adopt SQLite merely for FTS | Parser/property tests, deterministic ordering, bounded fuzzy fallback, benchmarks |
| P1 | Provider/index health | Per-worktree/provider versions, pending/partial/failed units, diagnostics and freshness state | Empty results must not hide incomplete indexing | Stable JSON status and CI assertions for degraded providers |
| P1 | Optional auto-sync | Native watcher feeding existing incremental units and an explicit retry/degradation state | Query-time Git verification remains authoritative; no required hooks or daemon | Race/event-storm/rename/delete/worktree tests; queries correct with watcher disabled |
| P1 | Generic syntax mapper | Tree-sitter-backed symbols, headings, imports/references, and structure for unsupported formats | Never replace SCIP/LSP/compiler providers; mark facts syntactic/inferred | Broad fixture corpus, graceful unsupported grammar behavior, deterministic portable output |
| P1 | Local animated explorer | Embedded d3-graphviz UI over bounded DOT/source endpoints and stable IDs | Human-only view; no second graph or remote assets | Headless interaction/security tests and smooth bounded snapshot transitions |
| P2 | Session source deduplication | Optional fingerprints/pointers for long-lived protocol clients | Do not complicate ordinary one-shot CLI output | Multi-request protocol tests demonstrate material context savings |
| P2 | Thin MCP wrapper | One high-level explore tool plus stable low-level schemas when evidence warrants it | CLI and Go contracts remain canonical | MCP/CLI responses share fixtures and freshness semantics |

## Recommended implementation sequence

1. Specify the enrichment fact/provenance contract and protocol conformance tests.
   This is the foundation shared by compiler providers, syntax mappers, and
   framework synthesizers.
2. Implement one route or manifest mapper end to end, including invalidation,
   evidence, diagnostics, and query rendering. Use it to correct the contract
   before broadening support.
3. Add qualified query filters and provider/index health, because exploration and
   affected-test safety depend on them.
4. Build source-rich exploration as a deterministic composition of current graph,
   freshness, and source APIs.
5. Add affected-test UX with complete/partial/unknown semantics and explicit CI
   policy.
6. Add the generic syntax mapper as a separately versioned fallback extension.
7. Add the optional watcher only after the query-driven behavior has benchmarks
   and correctness tests; demonstrate latency improvement rather than assuming it.
8. Add stable DOT IDs, then the embedded d3-graphviz explorer and its security and
   browser test harness.
9. Harden extension packaging with checksums, SBOMs, attestations, and artifact
   smoke/conformance tests.

## Anti-patterns to avoid

- Treating a syntax tree, fuzzy name match, or framework convention as compiler
  truth.
- Collapsing categorical evidence into an attractive but unauditable confidence
  number.
- Identifying facts by source line, making ordinary edits destroy continuity.
- Requiring both endpoints to exist before preserving a useful reference.
- Combining unrelated repositories and worktrees into one opaque database.
- Making a watcher, daemon, or Git hook the only way an index becomes correct.
- Replacing bstore without representative end-to-end benchmarks.
- Allowing exploration ranking and output budgeting to grow into one untestable
  function.
- Default telemetry, update checking, CDN assets, or any other surprise network
  behavior in a local graph tool.
- Publishing checksums or attestations that the documented install path never
  verifies.
- Animating transient DOT IDs and calling the resulting visual churn semantics.

## Primary-source index

- CodeGraph: [README](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/README.md),
  [how it works](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/site/src/content/docs/core-concepts/how-it-works.md),
  [resolution](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/site/src/content/docs/core-concepts/resolution.md),
  [knowledge graph](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/site/src/content/docs/core-concepts/knowledge-graph.md),
  [configuration](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/site/src/content/docs/getting-started/configuration.md),
  [CLI](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/site/src/content/docs/reference/cli.md),
  [MCP server](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/site/src/content/docs/reference/mcp-server.md),
  [API](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/site/src/content/docs/reference/api.md),
  [routes guide](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/site/src/content/docs/guides/framework-routes.md),
  [affected tests](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/site/src/content/docs/guides/affected-tests.md),
  [database schema](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/db/schema.sql),
  [graph queries](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/db/queries.ts),
  [extraction](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/extraction/index.ts),
  [resolution](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/resolution/index.ts),
  [watch policy](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/sync/watch-policy.ts),
  and [release workflow](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/.github/workflows/release.yml).
- d3-graphviz: [README](https://github.com/magjac/d3-graphviz/blob/355158dc789ff8556549018a0c0f7a567ac0bfc3/README.md),
  [renderer](https://github.com/magjac/d3-graphviz/blob/355158dc789ff8556549018a0c0f7a567ac0bfc3/src/graphviz.js),
  [transitions](https://github.com/magjac/d3-graphviz/blob/355158dc789ff8556549018a0c0f7a567ac0bfc3/src/transition.js),
  [key modes](https://github.com/magjac/d3-graphviz/blob/355158dc789ff8556549018a0c0f7a567ac0bfc3/src/keyMode.js),
  [package metadata](https://github.com/magjac/d3-graphviz/blob/355158dc789ff8556549018a0c0f7a567ac0bfc3/package.json),
  and [test workflow](https://github.com/magjac/d3-graphviz/blob/355158dc789ff8556549018a0c0f7a567ac0bfc3/.github/workflows/node.js.yml).
