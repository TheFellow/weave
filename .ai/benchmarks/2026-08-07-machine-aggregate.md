# Machine aggregate measurements — 2026-08-07

Host: macOS/darwin amd64, Intel Core i5-1038NG7 2.00 GHz. The fixture contains
eight independent Git worktrees and 5,000 total symbols. Each worktree also
contains the same traversable edge so federation must query and deduplicate all
eight providers. Measurements use the checked-in benchmarks and do not include
an LLM, daemon, network service, or system Graphviz process.

## Warm query comparison

```console
go test -json ./internal/federation -run '^$' \
  -bench BenchmarkFederatedVsMachineAggregateSearch \
  -benchtime=300x -benchmem -count=1
```

| Query | Authoritative federation | Aggregate | Observation |
|---|---:|---:|---|
| bounded symbol prefix | 533,513 ns/op; 408,840 B/op; 4,529 allocs/op | 635,301 ns/op; 439,954 B/op; 5,269 allocs/op | Aggregate is 19% slower after all stores are already open because it deliberately reproduces per-worktree collision/truncation semantics. |

```console
go test -json ./internal/federation -run '^$' \
  -bench BenchmarkFederatedVsMachineAggregateTraversal \
  -benchtime=1000x -benchmem -count=3
```

| Query | Authoritative federation, three runs | Aggregate, three runs | Observation |
|---|---:|---:|---|
| reverse edge adjacency | 73,170–88,698 ns/op; ~59,002 B/op; 556 allocs/op | 21,278–23,435 ns/op; ~15,256 B/op; 213 allocs/op | Aggregate is 3.1–4.2x faster and allocates about 74% fewer bytes. |

The symbol result is intentionally recorded rather than optimized by weakening
behavior. Storing one canonical symbol would be smaller/faster, but is wrong
when branches/worktrees contain the same stable ID with different searchable
names. The aggregate retains compact per-source symbol variants and produces the
same ordering, deduplication, provenance, and truncation as federation.

## Open plus search

```console
go test -json ./internal/federation -run '^$' \
  -bench 'BenchmarkFederatedVsMachineAggregateOpenAndSearch/(federated|aggregate)' \
  -benchtime=2x -benchmem -count=1
```

| Path | Time |
|---|---:|
| open eight worktree stores + search | 3.225 s/op |
| validate/open one aggregate generation + search | 3.016 s/op |

This synthetic fixture is dominated by required live Git/catalog checks, so the
cache improves end-to-end time by only about 6%. The benchmark uses deterministic
generation callbacks to isolate database fan-out; production still opens each
worktree database briefly to compare its atomic generation marker. A cache hit
does not retain those handles through the query or fan the query across them.
Freshness is not skipped to improve the number.

## Size projection

`TestHotProjectionDoesNotExceedVerboseWorktreeFixture` builds one worktree with
1,600 documents, definition occurrences, long paths, and symbols, then compares
physical bstore files:

| Store | Bytes |
|---|---:|
| authoritative worktree | 16,777,216 |
| aggregate hot projection | 8,388,608 |

The aggregate is 50% of this deliberately verbose fixture. It stores symbols,
token postings, edges, and normalized provenance only; units, documents,
occurrences, source text, and freshness manifests remain solely in worktrees.
The portable test permits equal physical allocation because bbolt grows files
in coarse platform-dependent steps; it never permits the projection to exceed
the authoritative fixture.

## Decision

Adopt the aggregate for catalog-scoped symbol and graph commands because it
materially accelerates traversal, avoids multi-database lock fan-out on hits,
and is smaller than duplicating full graphs. Keep context/workspace queries on
authoritative federation. Defer incremental aggregate layering and RIBLT until
real repository measurements show rebuild cost is material. Continue profiling
symbol search; do not collapse source variants or relax stable JSON semantics to
win a microbenchmark.
