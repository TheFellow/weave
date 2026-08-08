# Cross-repository indexing soak

Measured 2026-08-08 America/Los_Angeles (2026-08-08T10:47:23Z). These are
development-machine measurements, not service-level objectives.

## Environment and method

- Candidate: branch worktree based on `f0e638b09bc1d47a71124ba60e41438cd9a7523e`
- Host: macOS 15.7 / Darwin 24.6.0, Intel Core i5-1038NG7, amd64
- Go: 1.26.5; .NET SDK: 10.0.302; Rust/Cargo: 1.97.1
- Per-command bound: 900 seconds
- Harness: `scripts/benchmark-repositories.sh`

The harness built Weave and the .NET adapter from the candidate worktree, used
the candidate Rust release adapter explicitly, and made detached local clones
without hard links. Dependency download/restore and required F# reference
builds happened before timing. The timed adapter requests remained offline and
restore-free. Each successful repository ran a cold index, five no-change
queries, `weave verify`, and a full JSON export. All source-repository statuses
were unchanged.

```sh
WEAVE_RUST_ADAPTER="$PWD/adapters/rust/target/release/weave-rust" \
WEAVE_BENCHMARK_TIMEOUT=900 \
scripts/benchmark-repositories.sh .. RESULTS \
  TheFellow.github.io TheFellow ValueTypes arch-lint cedar-dotnet enumstruct \
  fkyeah fluid go-modular-monolith go-riblt weave ai-rigidbody
```

The strict final invocation exited 0. Every verification exited 0.

## Final results

Peak RSS is the platform-reported maximum resident set of the measured process
tree. Dependency preparation is excluded from cold-index time.

| Repository (commit) | Cold index | Peak RSS | Warm query range | DB bytes | Units | Documents | Symbols | Occurrences | Edges |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `TheFellow.github.io` (`aa20c241`) | 48.465 s | 310 MiB | 0.750-0.778 s | 146,608,128 | 212 | 138 | 2,486 | 2,751 | 3,638 |
| `TheFellow` (`5abd8e76`) | 1.412 s | 20 MiB | 0.713-0.720 s | 1,048,576 | 2 | 1 | 38 | 38 | 49 |
| `ValueTypes` (`22eab344`) | 10.100 s | 168 MiB | 0.706-0.757 s | 43,679,744 | 78 | 64 | 922 | 4,221 | 5,296 |
| `arch-lint` (`eaf4346a`) | 11.456 s | 149 MiB | 0.879-0.921 s | 67,198,976 | 379 | 59 | 1,316 | 1,904 | 3,195 |
| `cedar-dotnet` (`1d0cf8ef`) | 51.338 s | 520 MiB | 0.756-0.782 s | 397,537,280 | 287 | 261 | 7,629 | 69,794 | 88,135 |
| `enumstruct` (`2a35e493`) | 4.866 s | 106 MiB | 0.711-0.775 s | 47,013,888 | 212 | 8 | 703 | 1,362 | 2,079 |
| `fkyeah` (`875d56ee`) | 132.146 s | 1,118 MiB | 0.774-0.783 s | 418,791,424 | 915 | 314 | 15,932 | 65,436 | 66,436 |
| `fluid` (`8432c047`) | 4.112 s | 135 MiB | 0.816-0.876 s | 63,754,240 | 29 | 22 | 1,575 | 9,253 | 11,261 |
| `go-modular-monolith` (`7b010542`) | 82.122 s | 1,129 MiB | 0.834-1.001 s | 903,421,952 | 892 | 673 | 25,678 | 184,826 | 247,013 |
| `go-riblt` (`4227b660`) | 3.832 s | 103 MiB | 0.772-0.806 s | 51,290,112 | 36 | 25 | 1,350 | 6,506 | 8,856 |
| `weave` (`f0e638b0`) | 81.763 s | 770 MiB | 0.918-0.966 s | 655,515,648 | 405 | 248 | 20,657 | 130,196 | 162,767 |
| `ai-rigidbody` (`734a0249`) | 82.406 s | 1,978 MiB | 0.708-0.768 s | 197,996,544 | 215 | 120 | 8,388 | 61,453 | 1,882 |

## Findings addressed

1. Go dependency ASTs and type-info maps were retained recursively even though
   Weave emits facts only for repository packages. Removing `NeedDeps` keeps
   compiler export-data resolution while avoiding dependency syntax retention.
   The large monolith's exact fact counts and 903,421,952-byte database are
   unchanged. Its observed Weave RSS fell from roughly 2.45 GB in the adjacent
   baseline to 1.13 GiB in the strict run.
2. C# edge de-duplication scanned every prior edge for every insertion. A
   per-project ID set removes that quadratic path. Cedar now completes in
   51.338 seconds; the old path was still running before publication when an
   exploratory sample was stopped after 87 seconds. SDK-generated source files
   outside the repository are now diagnosed and omitted instead of aborting.
3. .NET discovery recursively opened project references twice and included
   intentionally malformed deep fixtures when the primary solution was nested.
   The nearest solution now defines the project boundary, while solutionless
   repositories still de-duplicate auto-loaded project references.
4. Buildalyzer defaults caused F# design-time builds to perform `/restore`
   despite the adapter contract. Restore is now explicitly disabled. fkyeah's
   complete 915-unit index includes standalone fixture projects and runs only
   after the harness prepares all projects.
5. Rust position normalization rescanned each source file from byte zero for
   every occurrence. Per-document line indexes make coordinate lookup constant
   time; validated symbols and stable symbol IDs are memoized. Nested Cargo
   projects are discovered and their SCIP paths are rebased to repository
   paths. Duplicate global symbols emitted for Rust crate/test targets receive
   deterministic first-document ownership while all occurrences remain.
6. Adapter entry points had divergent environment allowlists. One bounded
   allowlist now carries the required .NET, Rust, Java, and TypeScript toolchain
   variables without leaking unrelated credentials.
7. The benchmark harness had a fixed `net9.0` adapter path, collided with a
   repository named `weave`, skipped orphan project preparation, could not
   select an arbitrary corpus, and did not report memory. It now resolves the
   active target dynamically, prepares the complete clone, preserves and
   compares source status, records peak RSS, and returns failure after testing
   the full corpus if any repository fails.

## Installed-binary incremental smoke

After `go install ./cmd/weave`, an isolated `go-riblt` clone was indexed and
verified with the installed binary. Adding one comment to `codec.go` produced a
new generation with `dirty: true`, `change_count: 1`, and the diagnostic
`index: refreshed 1 changed paths`. The refresh completed in 2.990 seconds at
142 MiB peak RSS, and the resulting index passed `weave verify`. The original
`go-riblt` source repository remained clean.

## Tradeoffs and residuals

- Avoiding recursive Go syntax retention shifts a truly cold machine toward
  compiler export-data work: the adjacent cold baseline was 33.461 seconds,
  versus 82.122 seconds here. Warm queries remain below 1.01 seconds. The
  bounded-memory behavior is preferable to the prior multi-gigabyte peak, but
  compiler-cache-sensitive cold time should continue to be tracked.
- ai-rigidbody's 1.98 GiB peak is dominated by rust-analyzer while producing
  SCIP. Adapter normalization no longer has the occurrence-by-source-length
  scan, but controlling rust-analyzer's own working set is upstream of Weave.
- Dependency preparation can update an incomplete `go.sum` inside an isolated
  benchmark clone. The original source repository remains untouched, and timed
  Weave indexing continues to use `-mod=readonly` with networking disabled.
