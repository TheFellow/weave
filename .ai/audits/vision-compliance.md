# Vision compliance audit

Audit date: 2026-08-06  
Audited commit: `4f9779e` and its ancestry  
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
| Preserve prior art before major subsystems | **Implemented** | `.ai/prior-art/*` and ADRs 0001–0007, including release/security/performance research | Research must continue for each future subsystem. |
| Local, disposable, non-committed detailed indexes | **Implemented** | `repository.Discover`, Git-resolved storage, README recovery procedure, rebuild tests | Shared immutable snapshot storage is not implemented. |
| Fresh before every observed answer | **Partial** | Local database queries call `Freshness.Ensure`; `TestLifecycleAndQueriesRefreshRepositoryBeforeReading` and executable dirty-file smoke test | Federated catalog members are opened without refreshing; explicitly imported SCIP/.NET facts are not automatically refreshed on later source changes. |
| Incremental work proportional to change | **Partial** | Git overlay comparison; Go input/surface/inventory fingerprints; only changed unit batches are atomically replaced | Go still loads/analyzes the package universe to calculate fingerprints; no content-addressed fact reuse across branches; no measured RIBLT use. |
| Stable human and JSON CLI | **Partial** | urfave/cli v3 tree; injected streams; deterministic text; `weave.query/v1`; truncation; command end-to-end tests | Exit codes do not yet distinguish unavailable capability, stale/corrupt storage, and internal failure; JSON compatibility policy is not published beyond schema labels. |
| Cross-platform core and isolated native adapters | **Partial** | Pure-Go core; release build matrix for macOS/Linux/Windows amd64/arm64; one-shot adapter protocol; .NET CI on all three OSes | Core executable behavior is not tested on a three-OS CI matrix; .NET adapter is not shipped as a release artifact. |
| Bounded evidence rather than guesses | **Implemented** | `graph.Evidence`, validation, traversal/result bounds, protocol/import limits, provider-preserving JSON/export | Some external endpoints are intentionally unmaterialized and reported by `verify` as warnings. |

## System capability traceability

| Capability | Status | Evidence | Notes/gaps |
| --- | --- | --- | --- |
| Repository/worktree identity | **Implemented** | `internal/repository`; temporary Git tests for remotes, branches, renames, linked worktrees | Paths remain locations, canonical remote/root commit identity is used. |
| Snapshot and dirty overlay identity | **Partial** | freshness manifest records commit/tree/branch/overlay digest/provider | No immutable snapshot database or multiple build variants active at once. |
| Fact vocabulary and validation | **Implemented** | `internal/graph/model.go`, model property/table tests | Project and external-symbol entities are represented through units/stable IDs rather than first-class persisted records. |
| bstore storage, bidirectional adjacency, atomic unit replacement | **Implemented** | `internal/storage`; rollback, replacement, query, export and integrity tests | One database per worktree rather than shared snapshot databases. |
| Schema versioning and safe migration | **Partial** | Schema v1 marker and explicit `ErrSchema` rebuild guidance; unsupported-schema regression test | There is no migration framework or old-schema fixture because no schema transition exists yet. Add one with schema v2, not speculative code now. |
| Corruption/recovery | **Implemented** | bbolt/bstore corruption classification, derived-index rebuild guidance, README delete/reindex procedure | No `weave doctor --repair`; recovery is deliberate discard/rebuild. |
| Compaction/GC | **Implemented** | `storage.Compact`, `weave gc`, closed-database command test | Snapshot/content garbage collection is inapplicable until those stores exist. |
| Resource limits | **Implemented** | graph string limits; adapter byte/frame/fact/diagnostic/stderr/depth/time limits; SCIP byte/depth/document/fact/string/source limits; query and federation bounds | OS-level child memory limits are not implemented. |
| Git-native freshness and locking | **Implemented** for Go | `internal/freshness`, owner-diagnostic lock tests, manifest atomic publication | Stale-lock PID recovery is timeout/manual rather than automatic; external adapters lack query-driven refresh. |
| Native Go provider | **Implemented** core semantic slice | genuine compiled fixture covers declarations/references/imports/dependencies/implementations/calls/build constraints/fingerprints; test source is loaded; deterministic rebuild test | Explicit `tests` edges and whole-program dynamic call-graph precision are absent. |
| Go toolchain/network safety | **Implemented** | `GOPROXY=off`, `-mod=readonly`, preserved user toolchain selection, regression test; executable smoke found and fixed older-base-toolchain failure | The selected compatible Go toolchain must already be installed/cached. |
| SCIP ingest | **Implemented** | bounded protobuf importer, path/symlink containment, selective producer replacement, atomic malformed-input tests | No automatic discovery/invocation of arbitrary SCIP producers. |
| Versioned external adapter protocol | **Partial** | strict one-shot `weave.adapter/v0`, describe negotiation, literal argv, permissions, cancellation, partial diagnostics, fuzzing | Protocol remains newline JSON v0 rather than promised stable framed protobuf; no third-party compatibility promise. |
| Adapter discovery and doctor | **Partial** | PATH/environment discovery for `weave-dotnet` and `scip-dotnet`; runtime visibility; command tests | Doctor reports executable presence but does not probe native protocol compatibility or offer installation/repair. |
| C# precision | **Implemented** as explicit adapter | Roslyn/MSBuild implementation and mixed-solution compiler fixture | Full adapter run is explicit and full-refresh, not transparently freshness-managed. Visual Basic absent. |
| F# precision | **Partial** | FCS typed definitions/references, ordered-file/project fingerprint, mixed solution tests | Calls and a formal binary-compatible API fingerprint are deferred. |
| Cross-language bridges | **Gap** | — | Declared/generated schema, route, config, or project bridges are not normalized beyond .NET project dependencies. |
| Global catalog and bounded federation | **Partial** | catalog add/remove/list/status/sync; exact-ID bounded federation, provenance and partial-failure tests | No checked-in cross-repository link configuration; no freshness before federated reads; external-symbol resolution is exact-ID only. |
| Architecture policy | **Implemented** initial slice | versioned checked-in config, allow/forbid edge rules, evidence paths, text/JSON/SARIF, exit 3 | Higher-level “must have test”, instantiation-count, and approved-boundary rule families are absent. |
| CI cache/artifact/verify/policy | **Implemented** | `ci key/index/check`, `.github/workflows/weave.yml`, SARIF integrity notifications, deterministic export artifact | Workflow does not compare normalized exports across platforms or run diff impact. |
| Cross-platform release packaging | **Implemented** configuration, not yet published | `.goreleaser.yaml`, tagged release workflow, checksums, build metadata, `version` text/JSON tests | No tag/release has exercised GitHub publication; no signing, SBOM, Homebrew/WinGet/Scoop package. |
| Repository/adapter trust boundary | **Partial** | Literal argv, explicit build/restore/network/generator permissions, no shell interpolation, path containment, strict/bounded input | The Go compiler may include an ignored `.go` file that participates in a package; explicit ignored-source exclusion and OS-level child memory limits are absent. |
| Security fuzzing | **Implemented** initial boundary set | adapter-frame and SCIP-import fuzz targets; bounded 5-second campaigns passed | Long-running scheduled/OSS-Fuzz integration and database-file fuzzing remain future work. |
| Performance baselines | **Partial** | storage microbenchmarks and `.ai/benchmarks/2026-08-06-local.md` | Required real-repository baselines (`go-modular-monolith`, `arch-lint`, `cedar-dotnet`, `fkyeah`) and incremental wall-time tracking are not recorded. |

## CLI command audit

| Command surface | Status | Behavior |
| --- | --- | --- |
| `init`, `index`, `status` | **Implemented** | Git-resolved lifecycle; native Go refresh; explicit SCIP/native adapter ingest. |
| `symbols`, `definition`, `references`, `callers`, `callees` | **Implemented** | Bounded local/federated graph queries; local reads refresh first. |
| `dependencies` | **Implemented** | Resolves one symbol/package and emits direct `depends-on`/`imports` edges; command/fixture test. |
| `path`, `impact` | **Partial** | Bounded graph path and symbol-rooted reverse impact work | Git-diff/file/package roots and affected-test summaries are absent. |
| `architecture check` | **Implemented** | Text/JSON/SARIF and policy exit status. |
| `repos add/remove/list/status/sync` | **Implemented** | Explicit platform-data catalog with worktree identity and locking. |
| `adapters list/doctor` | **Partial** | Discovery diagnostics work; compatibility probing does not. |
| `export`, `verify`, `gc` | **Implemented** | Deterministic export, severity-aware logical verification, physical compaction. |
| `version` | **Implemented** | Release-injected or Go-embedded version/commit/date/dirty/runtime/platform metadata; text and versioned JSON. |
| Silent production stubs | **None found** | The command-level `noop` was removed; every command in the real `Local` application implements behavior. `application.Noop` remains only as an injected-service/test fallback and for lifecycle calls deliberately constructed without a freshness manager. |

## Useful first release criteria

| # | Criterion | Status | Evidence and exact limitation |
| ---: | --- | --- | --- |
| 1 | Install one executable | **Partial** | `go install` works and tagged archives are configured; no tagged release exists yet. |
| 2 | Enter Go, C#, or F# repository | **Partial** | Go is automatic. C#/F# requires building and explicitly selecting the adapter. |
| 3 | Query without daemon | **Implemented** | All operations are one-shot local commands. |
| 4 | Current definitions/references/dependency paths/impact with source evidence | **Implemented for Go; partial overall** | Go and C# fact coverage supports these classes; F# lacks calls; external adapter currency is manual. |
| 5 | Change a file and refresh without full rebuild | **Partial** | Go publishes only changed fingerprint units and dirty-file smoke passed, although package loading remains broad. .NET refresh is full/manual. |
| 6 | Switch branches/worktrees safely | **Implemented for correctness** | Git/worktree regression tests and per-worktree storage; reuse across snapshots is limited. |
| 7 | Delete/reconstruct all data | **Implemented** | Derived-state location and rebuild documented/tested. |
| 8 | Agent consumes CLI without another model | **Implemented** | Stable bounded JSON CLI; no embedded LLM. MCP is optional and absent. |
| 9 | Same index/policy checks in CI | **Implemented** | CI commands and GitHub workflow. |
| 10 | Provider known for every material edge | **Implemented** | Required graph field, validation, export and provider-specific tests. |

**Conclusion:** this is a credible Go-first alpha and a strong implementation
foundation, but it is not yet the complete useful first release as written.
The blocking differences are an actually published/tagged install artifact and
automatic fresh C#/F# lifecycle (including incremental behavior). If the first
release is explicitly scoped to Go, criteria 2/4/5 should be narrowed in the
vision through a conscious product decision rather than silently declared done.

## Deferred work in priority order

1. Integrate discovered native adapters into query-driven freshness, persist
   their inventories/fingerprints, and probe protocol compatibility in doctor.
2. Run and fix the first prerelease publication; then add core three-OS CI,
   provenance/SBOM/signing, and package-manager channels based on demand.
3. Refresh selected catalog repositories before federated reads or explicitly
   expose stale sources in the result contract.
4. Add Git-diff/file/package impact and affected-test selection.
5. Add declared/generated cross-language and cross-repository bridges.
6. Measure incremental refresh and database size on the four named real
   repositories; optimize compiler loading and prefix allocations from data.
7. Add protocol probing/recovery to doctor, stable adapter protobuf only when
   third-party compatibility is ready, and an actual schema migration when
   schema 2 exists.
8. Exclude ignored/untrusted source explicitly where compiler project systems
   would otherwise include it, and add OS-level adapter memory/process limits.
