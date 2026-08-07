# Vision compliance audit

Audit date: 2026-08-06  
Audited implementation: `main` through declared bridges, companion adapter
packaging, and the repository-scale hardening block recorded in this file.
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
| Compiler-native semantic truth with visible evidence/provider | **Partial** | Go uses `go/packages`/`go/types`; C# uses Roslyn/MSBuild; F# uses FCS; every normalized symbol/occurrence/edge has provider/evidence | Visual Basic and other languages absent; F# call edges absent; syntax fallback absent. |
| Preserve prior art before major subsystems | **Implemented** | `.ai/prior-art/*` and ADRs 0001–0008, including compiler-plugin, release/security/performance research | Research must continue for each future subsystem. |
| Local, disposable, non-committed detailed indexes | **Implemented** | `repository.Discover`, Git-resolved storage, README recovery procedure, rebuild tests | Shared immutable snapshot storage is not implemented. |
| Fresh before every observed answer | **Partial** | Local and selected federated worktrees call `Freshness.Ensure`; composite Go/.NET inventories refresh before reads; failed catalog members are explicitly excluded | Explicit SCIP imports and arbitrary one-shot adapter imports have no producer lifecycle and can become stale. |
| Incremental work proportional to change | **Partial** | Git overlay comparison; Go input/surface/inventory fingerprints; only changed unit batches are atomically replaced | Go still loads/analyzes the package universe to calculate fingerprints; no content-addressed fact reuse across branches; no measured RIBLT use. |
| Stable human and JSON CLI | **Partial** | urfave/cli v3 tree; injected streams; deterministic text; `weave.query/v1`; truncation; command end-to-end tests | Exit codes do not yet distinguish unavailable capability, stale/corrupt storage, and internal failure; JSON compatibility policy is not published beyond schema labels. |
| Cross-platform core and isolated native adapters | **Partial** | Pure-Go core; release build matrix for macOS/Linux/Windows amd64/arm64; one-shot adapter protocol; .NET 10 CI and dry-run packages on all three OSes; same-version NuGet/host companion release artifacts configured; genuine `fkyeah` indexing | Core executable behavior is not tested on a three-OS CI matrix; no tagged release has exercised publication. |
| Bounded evidence rather than guesses | **Implemented** | `graph.Evidence`, validation, traversal/result bounds, protocol/import limits, provider-preserving JSON/export | Some external endpoints are intentionally unmaterialized and reported by `verify` as warnings. |

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
| Git-native freshness and locking | **Implemented** for Go and discovered .NET | `internal/freshness` composite ownership, `internal/nativeindex` semantic-input fixture, owner-diagnostic lock tests, manifest atomic publication | Stale-lock PID recovery is timeout/manual; explicit SCIP/arbitrary adapter imports remain unmanaged. |
| Native Go provider | **Implemented** core semantic slice | genuine compiled fixture covers declarations/references/imports/dependencies/implementations/calls/build constraints/fingerprints; test source is loaded; deterministic rebuild test | Explicit `tests` edges and whole-program dynamic call-graph precision are absent. |
| Go toolchain/network safety | **Implemented** | `GOPROXY=off`, `-mod=readonly`, preserved user toolchain selection, regression tests; Go 1.26.5 build baseline and actionable too-new-target rejection | The selected compatible target toolchain must already be installed/cached; Weave must be built at least as new as target export data. |
| SCIP ingest | **Implemented** | bounded protobuf importer, path/symlink containment, selective producer replacement, atomic malformed-input tests | No automatic discovery/invocation of arbitrary SCIP producers. |
| Versioned external adapter protocol | **Partial** | strict one-shot `weave.adapter/v0`, published language-neutral golden/malformed fixtures, describe negotiation, literal argv, permissions, cancellation, partial diagnostics, fuzzing | Protocol remains newline JSON v0 rather than promised stable framed protobuf; no third-party compatibility promise or standalone executable conformance command. |
| Adapter discovery and doctor | **Implemented** initial slice | PATH/environment discovery for `weave-dotnet` and `scip-dotnet`; executable validation; bounded native protocol/capability handshake; runtime visibility; tests | Doctor does not install or repair adapters, and SCIP producers do not share the native protocol handshake. |
| C# precision | **Implemented** with automatic full refresh | Roslyn/MSBuild mixed-solution fixture; discovered adapter inventory/fingerprint lifecycle; unrelated-file reuse test | Any changed .NET semantic input causes the advertised full refresh. Visual Basic absent. |
| F# precision | **Partial** | .NET 10-hosted FCS typed definitions/references, MSBuild-evaluated dependency ordering, repository-only documents, ordered-file/project fingerprint, mixed solution tests, genuine `fkyeah` index/query | Calls and a formal binary-compatible API fingerprint are deferred; referenced outputs must be built before safe design-time indexing. |
| Cross-language bridges | **Implemented** initial explicit slice | strict `.weave/bridges.json` v1; exact `symbol:` endpoints; declared/generated depends/documents/generates facts; freshness/export/query/federation/policy tests | No build-system adapters auto-import bridge declarations; endpoint discovery remains explicit from export IDs by design. |
| Global catalog and bounded federation | **Partial** | catalog add/remove/list/status/sync; refresh-before-open, stale-member exclusion, exact-ID bounded federation, provenance and partial-failure tests; checked-in exact bridges join catalog members | External-symbol resolution and bridges are exact-ID only; there is no hosted/shared catalog. |
| Architecture policy | **Implemented** initial slice | versioned checked-in config, allow/forbid edge rules, evidence paths, text/JSON/SARIF, exit 3 | Higher-level “must have test”, instantiation-count, and approved-boundary rule families are absent. |
| CI cache/artifact/verify/policy | **Implemented** | `ci key/index/check`, `.github/workflows/weave.yml`, SARIF integrity notifications, deterministic export artifact | Workflow does not compare normalized exports across platforms or run diff impact. |
| Cross-platform release packaging | **Implemented** configuration, not yet published | `.goreleaser.yaml`, tagged release workflow, checksums, build metadata, `version` tests; same-version .NET tool package and six RID archives; three-OS package dry runs | No tag/release has exercised GitHub publication; no signing, SBOM, Homebrew/WinGet/Scoop package; no NuGet.org push by design. |
| Repository/adapter trust boundary | **Partial** | Literal argv, explicit build/restore/network/generator permissions, no shell interpolation, path containment, strict/bounded input | The Go compiler may include an ignored `.go` file that participates in a package; explicit ignored-source exclusion and OS-level child memory limits are absent. |
| Security fuzzing | **Implemented** initial boundary set | adapter-frame and SCIP-import fuzz targets; bounded 5-second campaigns passed | Long-running scheduled/OSS-Fuzz integration and database-file fuzzing remain future work. |
| Performance baselines | **Partial** | storage microbenchmarks plus bounded reproducible harness and all four named attempts in `.ai/benchmarks/2026-08-06-repositories.md`; two material Go correctness/performance classes fixed; .NET 10 `fkyeah` follow-up succeeds | Cedar exceeds the .NET bound; dirty one-file incremental samples remain unrecorded; 1.11 GB Go database remains large. |

## CLI command audit

| Command surface | Status | Behavior |
| --- | --- | --- |
| `init`, `index`, `status` | **Implemented** | Git-resolved lifecycle; native Go refresh; explicit SCIP/native adapter ingest. |
| `symbols`, `definition`, `references`, `callers`, `callees` | **Implemented** | Bounded local/federated graph queries; local reads refresh first. |
| `dependencies` | **Implemented** | Resolves one symbol/package and emits direct `depends-on`/`imports` edges; command/fixture test. |
| `path`, `impact` | **Implemented** initial slice | Bounded symbol paths; symbol/file/package/Git-diff multi-root reverse impact; evidence-based affected-test projection; executable dirty-Go fixture | Test selection is limited to explicit `tests` edges and recognizable Go test declarations, not build targets. Deleted files absent from the current graph may be reported unmatched. |
| `architecture check` | **Implemented** | Text/JSON/SARIF and policy exit status. |
| `repos add/remove/list/status/sync` | **Implemented** | Explicit platform-data catalog with worktree identity and locking. |
| `adapters list/doctor` | **Implemented** initial slice | Side-effect-free listing plus bounded native compatibility negotiation and actionable missing/incompatible status; installation/repair remains manual. |
| `export`, `verify`, `gc` | **Implemented** | Deterministic export, severity-aware logical verification, physical compaction. |
| `version` | **Implemented** | Release-injected or Go-embedded version/commit/date/dirty/runtime/platform metadata; text and versioned JSON. |
| Silent production stubs | **None found** | The command-level `noop` was removed; every command in the real `Local` application implements behavior. `application.Noop` remains only as an injected-service/test fallback and for lifecycle calls deliberately constructed without a freshness manager. |

## Useful first release criteria

| # | Criterion | Status | Evidence and exact limitation |
| ---: | --- | --- | --- |
| 1 | Install one executable | **Partial** | `go install` works and tagged archives are configured; no tagged release exists yet. |
| 2 | Enter Go, C#, or F# repository | **Partial** | Go is built in. Same-version .NET 10 `weave-dotnet` companion archives/tool package are configured; C#/F# is automatic once installed. Publication is untested and Cedar exceeds the bound. |
| 3 | Query without daemon | **Implemented** | All operations are one-shot local commands. |
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

1. Run and fix the first prerelease publication of the configured core and .NET
   companion artifacts, then add core three-OS CI,
   provenance/SBOM/signing, and package-manager channels based on demand.
2. Define an explicit producer/freshness lifecycle for imported SCIP and
   arbitrary adapters; do not conflate an imported snapshot with a live producer.
3. Add build-system/schema adapters that can emit the existing exact bridge
   format without weakening its declarative evidence.
4. Extend test evidence beyond Go declarations/explicit `tests` edges and model
   build targets only through compiler/build-system providers, never heuristics.
5. Resolve the measured Cedar adapter limit, add dirty-file samples, and
   reduce the measured 1.11 GB Go index without sacrificing bounded adjacency.
6. Add installation/recovery guidance to doctor, stable adapter protobuf only
   when third-party compatibility is ready, and an actual schema migration
   when schema 2 exists.
7. Exclude ignored/untrusted source explicitly where compiler project systems
   would otherwise include it, and add OS-level adapter memory/process limits.
