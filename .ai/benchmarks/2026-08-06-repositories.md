# Repository-scale baseline

Measured 2026-08-06 America/Los_Angeles (2026-08-07T06:03:48Z). These are
development-machine baselines, not service-level objectives.

## Environment

- Candidate: Weave `29d8de7497979faa9c53119c96beb409310be806`
- Host: macOS 15.7 / Darwin 24.6.0, amd64
- CPU: Intel Core i5-1038NG7 @ 2.00 GHz
- Go: 1.26.5 darwin/amd64
- .NET SDKs: 9.0.316 and 10.0.300; adapter targets .NET 9
- Per-command bound: 300 seconds; native adapter internal bound: 240 seconds
- Harness: `scripts/benchmark-repositories.sh`

Exact invocation (dependencies were prepared in isolated clones before timing):

```sh
export PATH=/tmp/weave-dotnet-sdk:$PATH
export DOTNET_ROOT=/tmp/weave-dotnet-sdk
export DOTNET_CLI_TELEMETRY_OPTOUT=1
scripts/benchmark-repositories.sh /Users/ryan/src/github.com/TheFellow RESULTS
```

The harness built the candidate once, made local `--no-hardlinks` detached
clones at the commits below, ran `go mod download` or `dotnet restore` outside
the samples, and stored indexes only inside each clone's `.git/weave`. All four
source repositories were clean before and after; successful measurement clones
were also clean. Every successful verification returned exit 0.

## Results

| Repository (commit) | Cold index | No-change query, 5 samples | DB bytes | Units | Documents | Symbols | Occurrences | Edges |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `go-modular-monolith` (`7b010542`) | 37.785 s | 0.536–0.610 s | 1,106,579,456 | 165 | 642 | 24,150 | 185,277 | 223,454 |
| `arch-lint` (`eaf4346a`) | 2.610 s | 0.531–0.549 s | 16,777,216 | 32 | 41 | 356 | 1,621 | 2,024 |
| `cedar-dotnet` (`1d0cf8ef`) | failed at 240.733 s | not run | no manifest published | — | — | — | — | — |
| `fkyeah` (`875d56ee`) | failed at 4.470 s | not run | no manifest published | — | — | — | — | — |

Additional complete-graph costs:

| Repository | Verify | JSON export | Export bytes |
| --- | ---: | ---: | ---: |
| `go-modular-monolith` | 2.590 s | 3.415 s | 232,060,158 |
| `arch-lint` | 0.585 s | 0.601 s | 2,163,046 |

The no-change command was exactly:

```sh
weave symbols __weave_benchmark_no_match__ --json
```

It includes Git inspection, manifest/provider comparison, database open, and a
bounded empty lookup. Cold samples used `weave index --json`; diagnostics and
payloads were captured separately. Fact counts came from `weave export --json`.

## Defects found and fixed

1. A Go 1.25-built provider could not decode Go 1.26 compiler export data.
   Weave now builds with Go 1.26.5, rejects a still-newer target before loading
   with an actionable message, and continues to prohibit runtime downloads.
2. Repository-wide interface analysis produced duplicate global implementation
   edges and then compared every concrete type with every interface. Exact
   deterministic edge ownership and a required-method candidate index fixed
   the persistence collision and removed the quadratic scan while retaining
   `go/types.Implements` as the final authority.
3. One transaction for 433,523 semantic facts exceeded the five-minute bound
   and used multiple gigabytes of memory. Prevalidated, per-unit atomic chunks
   reduced measured storage publication to a bounded practical path; the
   freshness manifest remains unpublished until every chunk succeeds, so an
   interrupted refresh is deterministically replayed.
4. Verbose recursively encoded Go identities inflated the database. Fixed-size
   domain-separated SHA-256 identities reduced the same graph database from
   7,536,160,768 bytes to 1,106,579,456 bytes (85.3% smaller) and the cold run
   from 52.740 seconds to 37.785 seconds in adjacent harness runs.

Before fixes, the same `go-modular-monolith` cold command exceeded the exact
300-second harness bound (`301.719062` seconds including termination). This is
preserved here because omitting the failed baseline would hide why the storage
and identity changes were necessary.

## Honest residuals

- 1.11 GB for 433,523 semantic facts remains too large to call compact. Edge
  and occurrence adjacency indexes deliberately trade space for bounded query
  time; schema-level compression/content reuse needs further measurement.
- `cedar-dotnet` restored successfully but its full Roslyn solution analysis
  exceeded the 240-second adapter bound. No partial inventory was published.
- `fkyeah` restored successfully, then the .NET 9 Buildalyzer host attempted to
  evaluate .NET 10 SDK tasks and failed loading `System.Runtime, Version=10.0`.
  Supporting target SDK 10 requires an adapter/toolchain compatibility update,
  not parser fallback or suppressed diagnostics.
- These samples measure cold build and no-change freshness. A reproducible
  one-file dirty-overlay matrix is still needed; current Go invalidation also
  reloads the compiler package universe before selecting changed publications.

## Follow-up: .NET 10 host

On 2026-08-07 the adapter, tests, CI, and release companion moved to `net10.0`.
After a normal trusted-repository build, a fresh `fkyeah` index completed within
the existing four-minute adapter bound and an exact `JsonRpc` query returned FCS
facts from the resulting current manifest. The fix also orders F# projects from
MSBuild-evaluated `ProjectReference` edges (dependents first), retains prebuilt
reference outputs during Buildalyzer design-time evaluation, and excludes
SDK-injected package-cache source from repository documents. This supersedes the
SDK-host incompatibility above without rewriting the historical baseline.
