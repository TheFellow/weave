# Vision compliance audit

Audit date: 2026-08-07
Audited implementation: `main` through the compiler-plugin contract, Python,
Rust, C/C++, TypeScript/JavaScript, and JVM adapters, explicit
arbitrary-adapter registration, the Universal Ctags fallback, companion adapter
packaging, repository-scale hardening, the workspace/content increment,
contextual link authoring, focused DOT graph export, and the animated local
graph explorer with source-rich selection and guarded contextual-link
authoring, plus Git-aware semantic graph/API/impact/test diffs and optional
foreground watch-mode warming, and the source-only schema/build provider for
Protobuf, OpenAPI 3, GraphQL, PostgreSQL migrations, Terraform, and the
documented Go/Rust/npm/Maven/MSBuild manifest set.
Method: read `.ai/vision.md`, ADRs, prior-art notes, implementation and tests;
ran unit/race/static checks, bounded fuzz campaigns, microbenchmarks, .NET
fixtures, release/action validation, and a built-executable smoke test in a real
temporary Git repository.

Status meanings:

- **Implemented**: user-visible behavior exists and has direct test or smoke
  evidence.
- **Partial**: a useful slice exists, but the vision's stated breadth or
  lifecycle is not complete.
- **Gap**: no behavior satisfying the promise exists. A command name or design
  note alone is not implementation evidence.

This is a traceability report, not a declaration that the complete vision or
the useful-first-release definition has been achieved.

## Approved completion pass

The nine-item completion pass in `.ai/vision.md` is complete at its documented
scope. This does not erase the broader partials and deferred work recorded
below.

| # | Increment | Status | Durable evidence |
| ---: | --- | --- | --- |
| 1 | Release readiness | **Complete** | `a59e40a`; reproducible cross-platform archives, checksums, SPDX SBOMs, release inspection, draft publication/provenance workflow, installation/recovery guide, release-readiness CI. Actual public prerelease publication remains an explicit product milestone. |
| 2 | Bounded source-rich context | **Complete** | `1f1cfdc`; `weave.context/v1`, shared local/federated query path, current bounded excerpts, evidence, provenance, one-hop relationships, CLI/explorer tests. |
| 3 | Machine-wide aggregate | **Complete** | `279c55b`, `13d78ba`; immutable generation-keyed bstore aggregate under platform state, authoritative per-worktree revalidation/fallback, corruption/concurrency tests. |
| 4 | Compact storage v2 | **Complete** | `ca8aee9`, `808ff3a`; interned numeric hot paths, split detail records, rebuild-only version boundary, retained v1 fixture, deterministic logical exports, recorded benchmark. |
| 5 | Semantic snapshot diffs | **Complete** | `4ad4d9e`, `910f70d`, `d24070b`; Git-aware graph/API/impact/test diffs, stable snapshot identities, explorer transitions, real-browser and repository fixture tests. |
| 6 | Optional watch warmer | **Complete** | `f7f45a2`; foreground bounded polling/coalescing/backoff/cancellation over the same authoritative freshness path, human/NDJSON tests. |
| 7 | Source-rich human explorer | **Complete** | `a1b150a`; inspect/search/expand/pin/hide/snapshot comparison and revision-guarded contextual-link authoring over the shared graph/context APIs. |
| 8 | Managed adapter ecosystem | **Complete** | `4aff063`, `0ecf38f`, `ea6280b`; install/update/remove/list/doctor/conformance, integrity and capability pins, explicit claim routing, independent packages and cross-platform contract/producer CI. `8e409b6`, `0fa395c`, and `d6bb7b3` add bounded native caches, stale-run cancellation, and worktree-clean reporting. |
| 9 | Schema and build providers | **Complete** | `db82a7e`; maintained-parser Protobuf/OpenAPI 3/GraphQL/PostgreSQL/Terraform/build-manifest facts, bounded source-only trust boundary, category reuse, shared relationship contract, behavioral and real-CLI tests. |

## Product and architecture promises

| Vision promise | Status | Implemented evidence | Remaining gap |
| --- | --- | --- | --- |
| Deterministic, local operation without an LLM or required service | **Implemented** | `internal/goindex`, `internal/schemabuild`, `internal/storage`, `internal/query`; deterministic export/provider tests | None for implemented providers. |
| Compiler-native semantic truth with visible evidence/provider | **Partial** | Go uses `go/packages`/`go/types`; C# uses Roslyn/MSBuild; F# uses FCS; Python uses CPython `ast`/`symtable`; Rust uses `rust-analyzer scip`; C/C++/CUDA use `scip-clang`; TypeScript/JavaScript use `scip-typescript`; Java/Kotlin use `scip-java`; Universal Ctags adds an explicitly `syntactic` definition-only fallback with per-file routing around precise providers | Visual Basic, Scala, and many ecosystems still lack exact providers; F#/Python type-enriched call coverage is absent. |
| Preserve prior art before major subsystems | **Implemented** | `.ai/prior-art/*` and ADRs 0001–0019, including compiler-plugin, content/body discovery, contextual-linking, CodeGraph, Git semantic-diff, watch-mode warming, generic-mapper, release/security/performance, source-rich animated Graphviz, resident query service, managed-adapter ecosystem, and schema/build parser research | Research must continue for each future subsystem. |
| Local, disposable, non-committed detailed indexes | **Implemented** | `repository.Discover`, Git-resolved storage, README recovery procedure, rebuild tests | Shared immutable snapshot storage is not implemented. |
| Fresh before every observed answer | **Partial** | Local and selected federated worktrees call `Freshness.Ensure`; Go, schema/build, workspace/content, authored-link, .NET, Python, Rust, C/C++, root TypeScript/JavaScript projects, and registered adapters compose freshness-owned inventories; failed catalog members are excluded | Explicit SCIP files and explicit one-shot adapter imports, including JVM unless explicitly registered with broad permissions and conservative inputs, remain unmanaged snapshots and can become stale. |
| Incremental work proportional to change | **Partial** | Git overlay comparison; Go input/surface/inventory fingerprints; schema/build category fingerprints bypass unchanged parsers and republish only changed categories; only changed unit batches are atomically replaced | A changed schema category conservatively relinks its complete cross-file corpus; Go still loads/analyzes the package universe to calculate fingerprints; no content-addressed fact reuse across branches; no measured RIBLT use. |
| Stable human, JSON, and DOT CLI | **Partial** | urfave/cli v3 tree; injected streams; deterministic text; `weave.query/v1`; bounded focused DOT with adversarial escaping and stable semantic SVG IDs; secured local animated explorer over the same DOT query; command, HTTP, and real-browser WASM tests | Exit codes do not yet distinguish unavailable capability, stale/corrupt storage, and internal failure; JSON/DOT/explorer compatibility policy is not published beyond schema labels. |
| Cross-platform core and isolated native adapters | **Partial** | Pure-Go core is built, tested, and executed on macOS/Linux/Windows; release matrix covers both major architectures; .NET/Python/Rust contract matrices run on all three OSes; Go wrappers for C++, TypeScript, JVM, and Universal Ctags are tested on all three; real producer E2E/smoke jobs; JVM and Ctags wrapper tests do not require their semantic producers | scip-clang has no upstream Windows or macOS x86-64 binary; Universal Ctags itself is deliberately not bundled; producer runtime availability follows each ecosystem; no tagged release has exercised publication. |
| Bounded evidence rather than guesses | **Implemented** | `graph.Evidence`, validation, traversal/result bounds, protocol/import limits, provider-preserving JSON/export | Some external endpoints are intentionally unmaterialized and reported by `verify` as warnings. |
| Non-compiling workspace knowledge is first-class | **Implemented** initial slice | `internal/workspaceindex`; Git inventory, Goldmark/GFM, YAML, inert HTML, routes/topics/series, fences, generated provenance, exact cross-repo path IDs; real website and modular-monolith smoke evidence | Renderer profiles, raw-reference attributes, content-specific diagnostics, config/data-file semantics, and generated-family collapse remain future work. |

## System capability traceability

| Capability | Status | Evidence | Notes/gaps |
| --- | --- | --- | --- |
| Repository/worktree identity | **Implemented** | `internal/repository`; temporary Git tests for remotes, branches, renames, linked worktrees | Paths remain locations, canonical remote/root commit identity is used. |
| Snapshot and dirty overlay identity | **Implemented observation contract** | freshness manifest records commit/tree/branch/overlay/provider; semantic diff identities add exact generation plus stable canonical normalized-snapshot digest and live-overlay revalidation | No persistent immutable snapshot database or multiple build variants active at once; historical facts rebuild in disposable worktrees. |
| Fact vocabulary and validation | **Implemented** | `internal/graph/model.go`, model property/table tests | Project and external-symbol entities are represented through units/stable IDs rather than first-class persisted records. |
| bstore storage, bidirectional adjacency, atomic unit replacement | **Implemented** | `internal/storage`; rollback, replacement, query, export and integrity tests; prevalidated bounded unit transactions for large refreshes | One database per worktree rather than shared snapshot databases. A crash between large-refresh chunks is replayed because the manifest is not published, but the database may be temporarily mixed until replay. |
| Resident agent query access | **Implemented local foreground slice** | `weave session`; `weave.query-session/v1` bounded NDJSON; one serialized hot bstore handle; background exact Git observation; close-refresh-reopen lifecycle; protocol, ownership, refresh, CLI, and race tests | One session owns one worktree database. No daemon discovery, socket multiplexing, MCP facade, or catalog-resident service yet. Other processes cannot open the same bstore file concurrently. |
| Schema versioning and safe migration | **Implemented rebuild contract** | Independent storage format v3 marker; read-only preflight rejects a retained v1 fixture without modifying its bytes; actionable remove-and-`weave index` guidance; stable entity identities remain unchanged; deterministic logical export tests | Derived per-worktree databases deliberately rebuild instead of converting. bbolt page layout/auto sequences are not a cross-platform byte contract. |
| Corruption/recovery | **Implemented** | bbolt/bstore corruption classification, derived-index rebuild guidance, README delete/reindex procedure | No `weave doctor --repair`; recovery is deliberate discard/rebuild. |
| Compaction/GC | **Implemented** | `storage.Compact`, `weave gc`, closed-database command test; v2 intern/entity counts are released transactionally and recomputed by verify; unit deletion covers hot, detail, posting, and reference rows | Snapshot/content garbage collection is inapplicable until those stores exist. |
| Resource limits | **Implemented** | graph string limits; adapter byte/frame/fact/diagnostic/stderr/depth/time limits; SCIP byte/depth/document/fact/string/source limits; query and federation bounds | OS-level child memory limits are not implemented. |
| Git-native freshness and locking | **Implemented** for automatic providers | `internal/freshness` composite ownership; built-in and registered adapter input fingerprints; project activation; fail-closed missing adapters; manifest atomic publication; exact ref resolution and hook-disabled temporary-worktree semantic comparison with success/failure/cancellation cleanup; optional foreground polling over exact observation tokens and the same `Ensure` path | Stale-lock PID recovery is timeout/manual; explicit one-shot imports remain unmanaged; user-configured checkout filters remain a local Git trust boundary. |
| Git/source/graph/API/impact/test diffs | **Implemented** bounded local slice | `weave.snapshot-diff/v1`; separate Git name-status and normalized before/after facts; stable snapshot digests; provider-owned API surface changes with compatibility `unknown`; shared reverse traversal; evidence-explained tests; explorer transition projection/API | Historical refs rebuild rather than reuse immutable snapshot databases; removed disconnected facts have no head adjacency; full snapshots are materialized internally before bounded output. |
| Native Go provider | **Implemented** core semantic slice | genuine compiled fixture covers declarations/references/imports/dependencies/implementations/calls/build constraints/fingerprints; test source is loaded; deterministic rebuild test | Explicit `tests` edges and whole-program dynamic call-graph precision are absent. |
| Go toolchain/network safety | **Implemented** | `GOPROXY=off`, `-mod=readonly`, preserved user toolchain selection, regression tests; Go 1.26.5 build baseline and actionable too-new-target rejection | The selected compatible target toolchain must already be installed/cached; Weave must be built at least as new as target export data. |
| SCIP ingest | **Implemented** | bounded protobuf importer, path/symlink containment, selective replacement, strict position encodings, explicit legacy-producer overrides; Rust, C++, TypeScript, and JVM producer E2E | Explicit `.scip` file imports remain snapshots rather than freshness-owned producers. |
| Versioned external adapter protocol | **Implemented v0 ecosystem floor** | strict one-shot `weave.adapter/v0`, independently implemented by .NET, Python, Rust, and thin C++/TypeScript/JVM/Ctags wrappers; normalized input/evidence/fallback/invalidation claims; provider fact ownership, open endpoints, negotiation, permissions, cancellation, fuzzing; language-neutral black-box conformance command/corpus with a non-Go fixture adapter | The wire contract remains experimental v0 rather than a stable v1 promise; bounded read-only enrichment anchors and persistent workers are deferred. Protobuf is not required. |
| Adapter discovery, lifecycle, routing, and doctor | **Implemented local-managed slice** | platform user-state install/update/remove/list/doctor; local regular-file artifacts; locked atomic bounded manifest with replacement recovery; SHA-256 artifact/capability pins; registry-over-environment-over-managed same-name precedence; precise conflict diagnostics and per-file broad-fallback routing; literal argv; no repository/PATH scan; established explicit companion environment selections; richer no-index doctor for integrity, compatibility, claim drift, external requirements, permissions, and current-worktree activation | No remote package manager, signature policy, sandbox, or automatic download. |
| C# precision | **Implemented** with automatic full refresh | Roslyn/MSBuild mixed-solution fixture; discovered adapter inventory/fingerprint lifecycle; unrelated-file reuse test | Any changed .NET semantic input causes the advertised full refresh. Visual Basic absent. |
| F# precision | **Partial** | .NET 10-hosted FCS typed definitions/references, MSBuild-evaluated dependency ordering, repository-only documents, ordered-file/project fingerprint, mixed solution tests, genuine `fkyeah` index/query | Calls and a formal binary-compatible API fingerprint are deferred; referenced outputs must be built before safe design-time indexing. |
| Python precision | **Implemented lexical slice** | Python 3.9+ subprocess uses compiler `symtable`; regular Git-visible UTF-8 modules, repeated definitions, pattern and PEP 695 bindings, lexical references, declared imports/dependencies, syntactic calls, topology/conservative-surface fingerprints, fsmonitor/symlink containment, installed-wheel E2E | `.pyi`, namespace/configured roots, attributes, inheritance, typing and runtime dispatch are deliberately absent; provider advertises full refresh. |
| Rust precision | **Implemented SCIP slice** | Rust-native adapter supervises `rust-analyzer scip`; toolchain-sensitive identity; exact definitions/references/implementations; permission/offline/generator policy; fake-process and real-RA CI | Upstream SCIP does not distinguish calls; build variants and changed-unit refresh are absent. |
| C/C++ precision | **Implemented SCIP slice** | `weave-cpp` supervises pinned `scip-clang` from a compilation database; exact SCIP facts; genuine fixture; real Linux indexer CI; explicit old-producer UTF-8 compatibility | One compilation database/full refresh only; ignored/generated databases are explicit; upstream binary coverage is incomplete. |
| TypeScript/JavaScript precision | **Implemented SCIP slice** | `weave-typescript` supervises pinned `scip-typescript`; compiler-derived definitions/references/implementations; explicit UTF-16 compatibility for its legacy unspecified ranges; root-project automatic freshness; genuine TS/JS fixtures and real producer CI | Automatic mode requires a root `tsconfig.json`/`jsconfig.json`; packages must already be available; monorepo project selection is explicit; upstream does not emit call relationships. |
| Java/Kotlin precision | **Implemented explicit SCIP slice** | Java-free `weave-jvm` negotiation supervises pinned `scip-java`; exact Java/Kotlin definitions/references/implementations; genuine mixed fixture and real producer CI; producer metadata must match the declared version | Real indexing needs JDK 17+ or a container shim and deliberately requires build, restore, network, and generator grants; Kotlin support is less mature upstream; automatic freshness requires explicit trusted registration. |
| Broad syntax fallback | **Implemented routable definition slice** | `weave-ctags` supervises a pinned Universal Ctags process; fingerprints parser inventories; private bounded Git-visible snapshots; document units plus syntactic definitions across Lua/Proto/SQL/CMake/Sh fixtures; explicit fallback claim loses per path to in-process Go and every precise external adapter; routed `input_paths` constrain its complete polyglot inventory and host validation rejects escaped documents | No references, calls, inheritance, or bundled producer. Those relationships remain absent rather than guessed. |
| Structured and arbitrary-text body discovery | **Implemented bounded lexical slice** | graph v2 symbol search terms; storage v3 round-trip/inverted postings; Markdown document/section/code-block extraction; arbitrary-extension regular UTF-8 file fallback; Git-diff unchanged-unit carry-forward; aggregate propagation; body-concept dossier tests; generated-name diversification | Terms are syntactic discovery hints, not semantic evidence. Each entity is bounded to 2,048 unique terms; binary/assets, files over 2 MiB, and content after the 512 MiB corpus ceiling remain topology-only. |
| Workspace/content provider | **Implemented** initial slice | One unit per Git-visible path plus bounded repository inventory; Markdown/GFM, front matter, raw HTML, headings, fences, links/embeds, assets, routes, series/topics, generated-from, persisted topology-only malformed/oversized diagnostics, incremental fingerprints, symlink/identity-race containment, real corpus smoke | Profile selection, first-class unresolved-reference diagnostics, and renderer-complete semantics are deliberately absent; full link-surface changes conservatively re-resolve all structured documents. |
| Schema/build provider | **Implemented** bounded source-only slice | `internal/schemabuild`; Buf linked descriptors; kin-openapi model plus YAML source nodes and contained local/open `$ref` handling; gqlparser SDL/query validation; Bytebase Omni PostgreSQL AST; HashiCorp HCL traversals; x/mod, go-toml, JSON, and XML manifests; shared relationship builder; source-range/provider/evidence facts; six-category fixture, deterministic rebuild, category-atomic malformed schemas/incremental deletion, per-manifest malformed build degradation, cancellation, symlink/size containment, remote/root-escape, unsupported SQL, and real CLI context/DOT tests | OpenAPI 2, non-identifiable component fragments, non-PostgreSQL dialects, `.tf.json`, Gradle/CMake executable DSLs, full JSON Schema/directive semantics, and build evaluation are deliberately absent. A changed category relinks completely. |
| Cross-language/contextual relationships | **Implemented** authored plus declared-schema/build slice | shared relationship builder used by Go, SCIP, workspace, schema/build, and authored-link providers; exact declared local imports/refs/project paths and explicit generation mappings publish rebuildable provider facts without mutating bridges; `links add/update/remove/list`; locked strict atomic `.weave/bridges.json`; unique local/catalog resolution; heterogeneous `entity:` and intentional open `id:` endpoints; every edge kind; revision-guarded explorer create/update/remove; refresh/export/query/federation/policy tests | Notes remain declaration metadata rather than graph facts; generated client-to-schema mappings without an explicit source/output declaration remain absent. |
| Global catalog and bounded federation | **Implemented** local machine slice | catalog add/remove/list/status/sync; refresh-before-open, stale-member exclusion, exact-ID bounded federation, provenance and partial-failure tests; immutable generation-keyed bstore aggregate accelerates symbol/graph queries with post-scan source revalidation, collision-equivalent variants, atomic publication, corruption/missing/concurrency recovery, and authoritative fallback; contextual authoring resolves unique queries across members then persists exact IDs | Runtime joins deliberately remain exact-ID; the hot aggregate omits documents/occurrences/source and therefore does not accelerate context/workspace queries; there is no hosted/shared catalog. |
| Source-rich context query | **Implemented** bounded one-hop slice | `context` uniquely resolves code or heterogeneous workspace entities; composes definition/reference and relationship-range evidence, current line-numbered excerpts, incoming/outgoing typed edges, adjacent entities, document/worktree provenance, freshness, and granular shared-budget truncation in `weave.context/v1`; CLI and explorer consume the same local/catalog unsafe-source contract | Natural-language multi-entry ranking, full symbol bodies, path tracing, adaptive token allocation, and secret-aware syntax policy remain deferred. |
| Architecture policy | **Implemented** initial slice | versioned checked-in config, allow/forbid edge rules, evidence paths, text/JSON/SARIF, exit 3 | Higher-level “must have test”, instantiation-count, and approved-boundary rule families are absent. |
| CI cache/artifact/verify/policy | **Implemented** | `ci key/index/check`, `.github/workflows/weave.yml`, SARIF integrity notifications, deterministic export artifact | Workflow does not compare normalized exports across platforms or run diff impact. |
| Cross-platform release packaging | **Implemented** configuration, not yet published | Commit-reproducible `.goreleaser.yaml`; exact archive/content/target validator; SHA-256 coverage; per-archive SPDX SBOMs; draft-first tagged workflow with keyless GitHub provenance; byte-identical double-snapshot evidence; C++/TypeScript/JVM/Ctags Go-wrapper archives; .NET tool package/six RID archives; pure Python wheel | No tag/release has exercised GitHub publication or draft promotion; Universal Ctags and other semantic producers remain separate; no Apple/Windows platform signing, companion-specific SBOMs, Homebrew/WinGet/Scoop/PyPI package, or NuGet.org push. |
| Repository/adapter trust boundary | **Partial** | Literal argv, explicit build/restore/network/generator permissions, no shell interpolation, path containment, strict/bounded input; historical worktree creation disables repository hooks | User-configured Git checkout filters can execute during historical materialization; the Go compiler may include an ignored `.go` file that participates in a package; explicit ignored-source exclusion and OS-level child memory limits are absent. |
| Security fuzzing | **Implemented** initial boundary set | adapter-frame and SCIP-import fuzz targets; bounded 5-second campaigns passed | Long-running scheduled/OSS-Fuzz integration and database-file fuzzing remain future work. |
| Performance baselines | **Partial** | retained v1/v2 fixture and storage benchmark in `.ai/benchmarks/2026-08-07-compact-storage-v2.md`; representative v2 file is 34.3% smaller and exact bounded prefix search allocates 22% fewer objects at similar latency; machine-aggregate and four named repository attempts retained; .NET 10 `fkyeah` follow-up succeeds | Existing large v1 indexes were measured but not destructively rebuilt; full-evidence adjacency/export retain documented cold-record costs; Cedar exceeds the .NET bound and dirty one-file samples remain unrecorded. |

## CLI command audit

| Command surface | Status | Behavior |
| --- | --- | --- |
| `init`, `index`, `status` | **Implemented** | Git-resolved lifecycle; automatic workspace, schema/build, native Go, authored-link, and installed-adapter refresh; explicit SCIP/native adapter ingest. |
| `watch` | **Implemented** optional warmer | Initial non-forced refresh; bounded exact Git polling; burst coalescing; same-observation retry backoff; linked-worktree isolation; graceful cancellation; deterministic human and NDJSON events. Queries remain authoritative and no daemon/native watcher is required. |
| `symbols`, `definition`, `references`, `callers`, `callees` | **Implemented** | Bounded local/federated graph queries; local reads refresh first; definition returns every binding occurrence with singular-symbol fallback. |
| `context` | **Implemented** initial slice | Unique local/catalog entity resolution; independently bounded occurrences/incoming/outgoing/source; current Git-visible regular UTF-8 excerpts with hash/race/root checks; deterministic human and `weave.context/v1` JSON output | One focus and one hop only; no natural-language task search, summaries, MCP, or LLM. |
| `dependencies` | **Implemented** | Resolves one symbol/package and emits direct `depends-on`/`imports` edges; command/fixture test. |
| `path`, `impact` | **Implemented** initial slice | Bounded symbol paths; symbol/file/package/Git-diff multi-root reverse impact; evidence-based affected-test projection; executable dirty-Go fixture | Test selection is limited to explicit `tests` edges and recognizable Go test declarations, not build targets. Deleted files absent from the current graph may be reported unmatched. |
| `diff graph/api/impact/tests` | **Implemented** bounded local slice | Exact ref or dirty-worktree comparison; Git rename/delete/add inventory; normalized fact before/after deltas; honest provider-surface API changes; shared reverse impact; evidence-backed tests; stable text and `weave.snapshot-diff/v1` JSON; keyed explorer transitions | Catalog-wide and persistent cached historical snapshots are deferred; provider-specific ABI/source compatibility classifiers are absent. |
| `graph` | **Implemented** human and machine slice | Bounded incoming/outgoing neighborhood; kind/provider/evidence filters; provider clusters; directional/evidence styling; escaped deterministic DOT to stdout/file; versioned JSON; `--interactive` tokenized loopback explorer with embedded Graphviz WASM, animated stable node/collapsed-edge IDs, source/provenance inspector, keyboard selection and explicit refocus, history/pan/zoom, reduced-motion/large-view fallbacks, guarded link editing, and headless selection/navigation coverage | Provider clusters are provenance-oriented; no architecture-area grouping, general derived-graph editing, or static PNG/PDF renderer. |
| `links add/update/remove/list` | **Implemented** | Unique query-to-exact-ID authoring; all normalized predicates; notes; open endpoints; Git-private writer lock; atomic canonical source; deterministic empty/file revision and typed stale-write conflict for long-lived explorer sessions; immediate freshness; local and genuine two-repository catalog tests | No bulk transaction/import command. |
| `workspace find/outline/links/backlinks` | **Implemented** initial slice | Bounded local/federated lookup; strict ambiguity; recursive containment; section-link aggregation; command/application/provider fixtures | No `workspace check`, route collision, stale-generation, unused-asset, or canonical-representation command yet. |
| `architecture check` | **Implemented** | Text/JSON/SARIF and policy exit status. |
| `repos add/remove/list/status/sync` | **Implemented** | Explicit platform-data catalog with worktree identity and locking. |
| `adapters install/update/remove/list/doctor/conformance` | **Implemented** local lifecycle | Explicit local executable management with atomic locked metadata and digest pins; metadata-only list; no-index/build/restore/network doctor; claims/requirements/permissions/integrity reporting; language-neutral black-box fixture conformance with meaningful failure exit. |
| `export`, `verify`, `gc` | **Implemented** | Deterministic export, severity-aware logical verification, physical compaction. |
| `version` | **Implemented** | Release-injected or Go-embedded version/commit/date/dirty/runtime/platform metadata; text and versioned JSON. |
| Silent production stubs | **None found** | The command-level `noop` was removed; every command in the real `Local` application implements behavior. `application.Noop` remains only as an injected-service/test fallback and for lifecycle calls deliberately constructed without a freshness manager. |

## Useful first release criteria

| # | Criterion | Status | Evidence and exact limitation |
| ---: | --- | --- | --- |
| 1 | Install one executable | **Partial** | `go install` works and tagged archives are configured; no tagged release exists yet. |
| 2 | Enter Go, C#, or F# repository | **Partial** | Go is built in. Same-version .NET 10 `weave-dotnet` companion archives/tool package are configured; C#/F# is automatic once installed. Publication is untested and Cedar exceeds the bound. |
| 3 | Query without daemon | **Implemented** | Queries are one-shot local commands; the optional human explorer owns a temporary loopback server only for the foreground command's lifetime. |
| 4 | Current definitions/references/dependency paths/impact with source evidence | **Implemented for Go and C#; partial overall** | Go and installed C# facts refresh before local/federated reads; `context` now returns bounded current excerpts and provenance for any document-backed provider; F# lacks calls; explicit SCIP imports remain unmanaged. |
| 5 | Change a file and refresh without full rebuild | **Partial** | Go publishes only changed fingerprint units and dirty-file smoke passed, although package loading remains broad. .NET skips unrelated edits but performs a complete adapter refresh for semantic changes because that is its advertised capability. |
| 6 | Switch branches/worktrees safely | **Implemented for correctness** | Git/worktree regression tests and per-worktree storage; reuse across snapshots is limited. |
| 7 | Delete/reconstruct all data | **Implemented** | Derived-state location and rebuild documented/tested. |
| 8 | Agent consumes CLI without another model | **Implemented** | Stable bounded JSON CLI; no embedded LLM. MCP is optional and absent. |
| 9 | Same index/policy checks in CI | **Implemented** | CI commands and GitHub workflow. |
| 10 | Provider known for every material edge | **Implemented** | Required graph field, validation, export and provider-specific tests. |

**Conclusion:** this is a credible Go-first alpha and a strong implementation
foundation, but it is not yet the complete useful first release as written.
The blocking differences are an actually published/tagged install artifact,
bounded large-solution performance, finer-grained
.NET refresh, and unmanaged explicit SCIP import currency. The automatic
installed-adapter and catalog lifecycle is usable, but is not equivalent to
shipping one executable for all three languages.

## Deferred work in priority order

1. Run and fix the first intentional prerelease publication of the configured
   artifacts; archive validation, three-OS core CI, SPDX SBOMs, and keyless
   provenance are ready. Add platform signing and package-manager channels only
   when audience and credentials justify them.
2. Run the Python type-enrichment comparison corpus, then add `.pyi` and
   configured package-root support without promoting dynamic inference to exact.
3. Define an explicit producer/freshness lifecycle for imported standalone SCIP
   snapshots; automatic native/registered adapters now have one.
4. Extend source-only schemas/builds only where a maintained parser adds
   material evidence: additional SQL dialects, OpenAPI 2 conversion,
   `.tf.json`, or a safe declarative Gradle/CMake representation. Do not
   evaluate executable build DSLs or turn automatic facts into authored
   bridges.
5. Extend test evidence beyond Go declarations/explicit `tests` edges and model
   build targets only through compiler/build-system providers, never heuristics.
6. Resolve the measured Cedar adapter limit, add dirty-file samples, and safely
   remeasure rebuilt v2 indexes on the four named repositories; the synthetic
   rich fixture is 34.3% smaller, while old real v1 files were left untouched.
7. Promote a stable adapter v1 only after third parties exercise the published
   v0 conformance corpus; do not require protobuf without a measured need.
   Storage v2 intentionally rebuilds disposable indexes rather than migrating.
8. Exclude ignored/untrusted source explicitly where compiler project systems
   would otherwise include it, and add OS-level adapter memory/process limits.
