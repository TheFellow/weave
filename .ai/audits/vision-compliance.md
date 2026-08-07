# Vision compliance audit

Audit date: 2026-08-07
Audited implementation: `main` through the compiler-plugin contract, Python,
Rust, C/C++, TypeScript/JavaScript, and JVM adapters, explicit
arbitrary-adapter registration, the Universal Ctags fallback, companion adapter
packaging, repository-scale hardening, the workspace/content increment,
contextual link authoring, focused DOT graph export, and the animated local
graph explorer.
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

## Product and architecture promises

| Vision promise | Status | Implemented evidence | Remaining gap |
| --- | --- | --- | --- |
| Deterministic, local operation without an LLM or required service | **Implemented** | `internal/goindex`, `internal/storage`, `internal/query`; deterministic export/provider tests | None for implemented providers. |
| Compiler-native semantic truth with visible evidence/provider | **Partial** | Go uses `go/packages`/`go/types`; C# uses Roslyn/MSBuild; F# uses FCS; Python uses CPython `ast`/`symtable`; Rust uses `rust-analyzer scip`; C/C++/CUDA use `scip-clang`; TypeScript/JavaScript use `scip-typescript`; Java/Kotlin use `scip-java`; Universal Ctags adds an explicitly `syntactic` definition-only fallback | Visual Basic, Scala, and many ecosystems still lack exact providers; F#/Python type-enriched call coverage is absent; automatic exact-provider/fallback overlap routing is deferred. |
| Preserve prior art before major subsystems | **Implemented** | `.ai/prior-art/*` and ADRs 0001–0012, including compiler-plugin, content, contextual-linking, CodeGraph, generic-mapper, release/security/performance, and animated Graphviz research | Research must continue for each future subsystem. |
| Local, disposable, non-committed detailed indexes | **Implemented** | `repository.Discover`, Git-resolved storage, README recovery procedure, rebuild tests | Shared immutable snapshot storage is not implemented. |
| Fresh before every observed answer | **Partial** | Local and selected federated worktrees call `Freshness.Ensure`; Go, .NET, Python, Rust, C/C++, root TypeScript/JavaScript projects, and registered adapters compose freshness-owned inventories; failed catalog members are excluded | Explicit SCIP files and explicit one-shot adapter imports, including JVM unless explicitly registered with broad permissions and conservative inputs, remain unmanaged snapshots and can become stale. |
| Incremental work proportional to change | **Partial** | Git overlay comparison; Go input/surface/inventory fingerprints; only changed unit batches are atomically replaced | Go still loads/analyzes the package universe to calculate fingerprints; no content-addressed fact reuse across branches; no measured RIBLT use. |
| Stable human, JSON, and DOT CLI | **Partial** | urfave/cli v3 tree; injected streams; deterministic text; `weave.query/v1`; bounded focused DOT with adversarial escaping and stable semantic SVG IDs; secured local animated explorer over the same DOT query; command, HTTP, and real-browser WASM tests | Exit codes do not yet distinguish unavailable capability, stale/corrupt storage, and internal failure; JSON/DOT/explorer compatibility policy is not published beyond schema labels. |
| Cross-platform core and isolated native adapters | **Partial** | Pure-Go core is built, tested, and executed on macOS/Linux/Windows; release matrix covers both major architectures; .NET/Python/Rust contract matrices run on all three OSes; Go wrappers for C++, TypeScript, JVM, and Universal Ctags are tested on all three; real producer E2E/smoke jobs; JVM and Ctags wrapper tests do not require their semantic producers | scip-clang has no upstream Windows or macOS x86-64 binary; Universal Ctags itself is deliberately not bundled; producer runtime availability follows each ecosystem; no tagged release has exercised publication. |
| Bounded evidence rather than guesses | **Implemented** | `graph.Evidence`, validation, traversal/result bounds, protocol/import limits, provider-preserving JSON/export | Some external endpoints are intentionally unmaterialized and reported by `verify` as warnings. |
| Non-compiling workspace knowledge is first-class | **Implemented** initial slice | `internal/workspaceindex`; Git inventory, Goldmark/GFM, YAML, inert HTML, routes/topics/series, fences, generated provenance, exact cross-repo path IDs; real website and modular-monolith smoke evidence | Renderer profiles, raw-reference attributes, content-specific diagnostics, config/data-file semantics, and generated-family collapse remain future work. |

## System capability traceability

| Capability | Status | Evidence | Notes/gaps |
| --- | --- | --- | --- |
| Repository/worktree identity | **Implemented** | `internal/repository`; temporary Git tests for remotes, branches, renames, linked worktrees | Paths remain locations, canonical remote/root commit identity is used. |
| Snapshot and dirty overlay identity | **Partial** | freshness manifest records commit/tree/branch/overlay digest/provider | No immutable snapshot database or multiple build variants active at once. |
| Fact vocabulary and validation | **Implemented** | `internal/graph/model.go`, model property/table tests | Project and external-symbol entities are represented through units/stable IDs rather than first-class persisted records. |
| bstore storage, bidirectional adjacency, atomic unit replacement | **Implemented** | `internal/storage`; rollback, replacement, query, export and integrity tests; prevalidated bounded unit transactions for large refreshes | One database per worktree rather than shared snapshot databases. A crash between large-refresh chunks is replayed because the manifest is not published, but the database may be temporarily mixed until replay. |
| Schema versioning and safe migration | **Partial** | Schema v1 marker and explicit `ErrSchema` rebuild guidance; unsupported-schema regression test | There is no migration framework or old-schema fixture because no schema transition exists yet. Add one with schema v2, not speculative code now. |
| Corruption/recovery | **Implemented** | bbolt/bstore corruption classification, derived-index rebuild guidance, README delete/reindex procedure | No `weave doctor --repair`; recovery is deliberate discard/rebuild. |
| Compaction/GC | **Implemented** | `storage.Compact`, `weave gc`, closed-database command test | Snapshot/content garbage collection is inapplicable until those stores exist. |
| Resource limits | **Implemented** | graph string limits; adapter byte/frame/fact/diagnostic/stderr/depth/time limits; SCIP byte/depth/document/fact/string/source limits; query and federation bounds | OS-level child memory limits are not implemented. |
| Git-native freshness and locking | **Implemented** for automatic providers | `internal/freshness` composite ownership; built-in and registered adapter input fingerprints; project activation; fail-closed missing adapters; manifest atomic publication | Stale-lock PID recovery is timeout/manual; explicit one-shot imports remain unmanaged. |
| Native Go provider | **Implemented** core semantic slice | genuine compiled fixture covers declarations/references/imports/dependencies/implementations/calls/build constraints/fingerprints; test source is loaded; deterministic rebuild test | Explicit `tests` edges and whole-program dynamic call-graph precision are absent. |
| Go toolchain/network safety | **Implemented** | `GOPROXY=off`, `-mod=readonly`, preserved user toolchain selection, regression tests; Go 1.26.5 build baseline and actionable too-new-target rejection | The selected compatible target toolchain must already be installed/cached; Weave must be built at least as new as target export data. |
| SCIP ingest | **Implemented** | bounded protobuf importer, path/symlink containment, selective replacement, strict position encodings, explicit legacy-producer overrides; Rust, C++, TypeScript, and JVM producer E2E | Explicit `.scip` file imports remain snapshots rather than freshness-owned producers. |
| Versioned external adapter protocol | **Partial** | strict one-shot `weave.adapter/v0`, independently implemented by .NET, Python, Rust, and thin C++/TypeScript/JVM/Ctags wrappers; golden/malformed fixtures; provider fact ownership, open endpoints, negotiation, permissions, cancellation, fuzzing | The language-neutral wire contract remains experimental v0; no standalone conformance command, bounded read-only enrichment anchors, or stable third-party compatibility promise. Protobuf is not required. |
| Adapter discovery and doctor | **Implemented** initial slice | Built-in .NET/Python/Rust/C++/TypeScript/JVM discovery plus explicit `weave.adapters/v1` registrations; literal argv, declarative inputs, exact provider negotiation, fail-closed config, bounded doctor; JVM and Ctags describe paths do not require their producers | Ctags remains explicit until overlap routing can exclude inputs claimed by exact providers; doctor does not install or repair adapters; package/signature/lock distribution is deferred. |
| C# precision | **Implemented** with automatic full refresh | Roslyn/MSBuild mixed-solution fixture; discovered adapter inventory/fingerprint lifecycle; unrelated-file reuse test | Any changed .NET semantic input causes the advertised full refresh. Visual Basic absent. |
| F# precision | **Partial** | .NET 10-hosted FCS typed definitions/references, MSBuild-evaluated dependency ordering, repository-only documents, ordered-file/project fingerprint, mixed solution tests, genuine `fkyeah` index/query | Calls and a formal binary-compatible API fingerprint are deferred; referenced outputs must be built before safe design-time indexing. |
| Python precision | **Implemented lexical slice** | Python 3.9+ subprocess uses compiler `symtable`; regular Git-visible UTF-8 modules, repeated definitions, pattern and PEP 695 bindings, lexical references, declared imports/dependencies, syntactic calls, topology/conservative-surface fingerprints, fsmonitor/symlink containment, installed-wheel E2E | `.pyi`, namespace/configured roots, attributes, inheritance, typing and runtime dispatch are deliberately absent; provider advertises full refresh. |
| Rust precision | **Implemented SCIP slice** | Rust-native adapter supervises `rust-analyzer scip`; toolchain-sensitive identity; exact definitions/references/implementations; permission/offline/generator policy; fake-process and real-RA CI | Upstream SCIP does not distinguish calls; build variants and changed-unit refresh are absent. |
| C/C++ precision | **Implemented SCIP slice** | `weave-cpp` supervises pinned `scip-clang` from a compilation database; exact SCIP facts; genuine fixture; real Linux indexer CI; explicit old-producer UTF-8 compatibility | One compilation database/full refresh only; ignored/generated databases are explicit; upstream binary coverage is incomplete. |
| TypeScript/JavaScript precision | **Implemented SCIP slice** | `weave-typescript` supervises pinned `scip-typescript`; compiler-derived definitions/references/implementations; explicit UTF-16 compatibility for its legacy unspecified ranges; root-project automatic freshness; genuine TS/JS fixtures and real producer CI | Automatic mode requires a root `tsconfig.json`/`jsconfig.json`; packages must already be available; monorepo project selection is explicit; upstream does not emit call relationships. |
| Java/Kotlin precision | **Implemented explicit SCIP slice** | Java-free `weave-jvm` negotiation supervises pinned `scip-java`; exact Java/Kotlin definitions/references/implementations; genuine mixed fixture and real producer CI; producer metadata must match the declared version | Real indexing needs JDK 17+ or a container shim and deliberately requires build, restore, network, and generator grants; Kotlin support is less mature upstream; automatic freshness requires explicit trusted registration. |
| Broad syntax fallback | **Implemented explicit definition slice** | `weave-ctags` supervises a pinned Universal Ctags process; fingerprints parser inventories; private bounded Git-visible snapshots; document units plus syntactic definitions across Lua/Proto/SQL/CMake/Sh fixtures; fake and genuine-producer tests; non-Universal/macOS Ctags rejection | No automatic overlap routing, references, calls, inheritance, or bundled producer. Those relationships remain absent rather than guessed. |
| Workspace/content provider | **Implemented** initial slice | One unit per Git-visible path plus bounded repository inventory; Markdown/GFM, front matter, raw HTML, headings, fences, links/embeds, assets, routes, series/topics, generated-from, persisted topology-only malformed/oversized diagnostics, incremental fingerprints, symlink/identity-race containment, real corpus smoke | Profile selection, first-class unresolved-reference diagnostics, and renderer-complete semantics are deliberately absent; full link-surface changes conservatively re-resolve all structured documents. |
| Cross-language/contextual relationships | **Implemented** authored slice | shared relationship builder used by Go, SCIP, workspace, and authored-link providers; `links add/update/remove/list`; locked strict atomic `.weave/bridges.json`; unique local/catalog resolution; heterogeneous `entity:` and intentional open `id:` endpoints; every edge kind; refresh/export/query/federation/policy tests | Notes remain declaration metadata rather than graph facts; no build-system adapters auto-author declarations. |
| Global catalog and bounded federation | **Partial** | catalog add/remove/list/status/sync; refresh-before-open, stale-member exclusion, exact-ID bounded federation, provenance and partial-failure tests; contextual authoring resolves unique queries across members then persists exact IDs | Runtime joins deliberately remain exact-ID; there is no hosted/shared catalog. |
| Architecture policy | **Implemented** initial slice | versioned checked-in config, allow/forbid edge rules, evidence paths, text/JSON/SARIF, exit 3 | Higher-level “must have test”, instantiation-count, and approved-boundary rule families are absent. |
| CI cache/artifact/verify/policy | **Implemented** | `ci key/index/check`, `.github/workflows/weave.yml`, SARIF integrity notifications, deterministic export artifact | Workflow does not compare normalized exports across platforms or run diff impact. |
| Cross-platform release packaging | **Implemented** configuration, not yet published | Commit-reproducible `.goreleaser.yaml`; exact archive/content/target validator; SHA-256 coverage; per-archive SPDX SBOMs; draft-first tagged workflow with keyless GitHub provenance; byte-identical double-snapshot evidence; C++/TypeScript/JVM/Ctags Go-wrapper archives; .NET tool package/six RID archives; pure Python wheel | No tag/release has exercised GitHub publication or draft promotion; Universal Ctags and other semantic producers remain separate; no Apple/Windows platform signing, companion-specific SBOMs, Homebrew/WinGet/Scoop/PyPI package, or NuGet.org push. |
| Repository/adapter trust boundary | **Partial** | Literal argv, explicit build/restore/network/generator permissions, no shell interpolation, path containment, strict/bounded input | The Go compiler may include an ignored `.go` file that participates in a package; explicit ignored-source exclusion and OS-level child memory limits are absent. |
| Security fuzzing | **Implemented** initial boundary set | adapter-frame and SCIP-import fuzz targets; bounded 5-second campaigns passed | Long-running scheduled/OSS-Fuzz integration and database-file fuzzing remain future work. |
| Performance baselines | **Partial** | storage microbenchmarks plus bounded reproducible harness and all four named attempts in `.ai/benchmarks/2026-08-06-repositories.md`; two material Go correctness/performance classes fixed; .NET 10 `fkyeah` follow-up succeeds | Cedar exceeds the .NET bound; dirty one-file incremental samples remain unrecorded; 1.11 GB Go database remains large. |

## CLI command audit

| Command surface | Status | Behavior |
| --- | --- | --- |
| `init`, `index`, `status` | **Implemented** | Git-resolved lifecycle; native Go refresh; explicit SCIP/native adapter ingest. |
| `symbols`, `definition`, `references`, `callers`, `callees` | **Implemented** | Bounded local/federated graph queries; local reads refresh first; definition returns every binding occurrence with singular-symbol fallback. |
| `dependencies` | **Implemented** | Resolves one symbol/package and emits direct `depends-on`/`imports` edges; command/fixture test. |
| `path`, `impact` | **Implemented** initial slice | Bounded symbol paths; symbol/file/package/Git-diff multi-root reverse impact; evidence-based affected-test projection; executable dirty-Go fixture | Test selection is limited to explicit `tests` edges and recognizable Go test declarations, not build targets. Deleted files absent from the current graph may be reported unmatched. |
| `graph` | **Implemented** human and machine slice | Bounded incoming/outgoing neighborhood; kind/provider/evidence filters; provider clusters; directional/evidence styling; escaped deterministic DOT to stdout/file; versioned JSON; `--interactive` tokenized loopback explorer with embedded Graphviz WASM, animated stable-ID transitions, refocus/history/pan/zoom, reduced-motion/large-view fallbacks, and headless enter/exit browser coverage | Provider clusters are provenance-oriented; no architecture-area grouping, source-detail panel, general graph editing, or static PNG/PDF renderer. |
| `links add/update/remove/list` | **Implemented** | Unique query-to-exact-ID authoring; all normalized predicates; notes; open endpoints; Git-private writer lock; atomic canonical source; immediate freshness; local and genuine two-repository catalog tests | No bulk transaction/import command. |
| `workspace find/outline/links/backlinks` | **Implemented** initial slice | Bounded local/federated lookup; strict ambiguity; recursive containment; section-link aggregation; command/application/provider fixtures | No `workspace check`, route collision, stale-generation, unused-asset, or canonical-representation command yet. |
| `architecture check` | **Implemented** | Text/JSON/SARIF and policy exit status. |
| `repos add/remove/list/status/sync` | **Implemented** | Explicit platform-data catalog with worktree identity and locking. |
| `adapters list/doctor` | **Implemented** initial slice | Side-effect-free built-in/registered listing plus bounded compatibility negotiation, literal argv, exact provider checks, and actionable config/missing status; installation/repair remains manual. |
| `export`, `verify`, `gc` | **Implemented** | Deterministic export, severity-aware logical verification, physical compaction. |
| `version` | **Implemented** | Release-injected or Go-embedded version/commit/date/dirty/runtime/platform metadata; text and versioned JSON. |
| Silent production stubs | **None found** | The command-level `noop` was removed; every command in the real `Local` application implements behavior. `application.Noop` remains only as an injected-service/test fallback and for lifecycle calls deliberately constructed without a freshness manager. |

## Useful first release criteria

| # | Criterion | Status | Evidence and exact limitation |
| ---: | --- | --- | --- |
| 1 | Install one executable | **Partial** | `go install` works and tagged archives are configured; no tagged release exists yet. |
| 2 | Enter Go, C#, or F# repository | **Partial** | Go is built in. Same-version .NET 10 `weave-dotnet` companion archives/tool package are configured; C#/F# is automatic once installed. Publication is untested and Cedar exceeds the bound. |
| 3 | Query without daemon | **Implemented** | Queries are one-shot local commands; the optional human explorer owns a temporary loopback server only for the foreground command's lifetime. |
| 4 | Current definitions/references/dependency paths/impact with source evidence | **Implemented for Go and C#; partial overall** | Go and installed C# facts refresh before local/federated reads; F# lacks calls; explicit SCIP imports remain unmanaged. |
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
4. Add build-system/schema adapters that can emit the existing exact bridge
   format without weakening its declarative evidence.
5. Add exact-provider input claims and overlap policy so the Ctags fallback can
   participate in automatic freshness only for otherwise unclaimed inputs.
6. Extend test evidence beyond Go declarations/explicit `tests` edges and model
   build targets only through compiler/build-system providers, never heuristics.
7. Resolve the measured Cedar adapter limit, add dirty-file samples, and
   reduce the measured 1.11 GB Go index without sacrificing bounded adjacency.
8. Add installation/recovery guidance to doctor, promote a stable
   language-neutral adapter wire specification and executable conformance suite
   when third-party compatibility is ready, and add an actual schema migration
   when schema 2 exists. Do not require protobuf without a measured need.
9. Exclude ignored/untrusted source explicitly where compiler project systems
   would otherwise include it, and add OS-level adapter memory/process limits.
