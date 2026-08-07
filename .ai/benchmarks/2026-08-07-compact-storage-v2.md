# Compact storage v2 measurements — 2026-08-07

Host: macOS/darwin amd64, Intel Core i5-1038NG7 2.00 GHz, Go 1.26.5.
All comparisons use checked-in test code. `legacy_v1_test.go` retains the exact
v1 record/index layout rather than estimating it, and v2 exercises the public
storage API. Results are local measurements, not portable service-level claims.

## Before touching storage

The original v1 microbenchmark (one unit, one document, 2,000 symbols, 1,999
edges) was captured before implementation:

```console
go test ./internal/storage -run '^$' \
  -bench BenchmarkIndexedGraph -benchmem -count=3
```

| Operation | v1 time | v1 bytes/op | v1 allocs/op |
|---|---:|---:|---:|
| bounded prefix, 50 | 463–479 µs | 336,610–336,612 | 5,298 |
| one outgoing edge | 8.20–8.29 µs | 7,400 | 87 |
| complete export | 5.04–5.08 ms | 4,886,694–4,886,700 | 44,962 |

Existing TheFellow v1 files were observed without rebuilding or modifying them:

| Worktree | v1 bytes |
|---|---:|
| weave | 957,681,664 |
| go-modular-monolith | 1,117,290,496 |
| fkyeah | 494,403,584 |

There is intentionally no “after” claim for those worktrees. Rebuilding a
developer's current derived index was unnecessary and would have been a
destructive benchmark side effect. They can be remeasured safely after the
owner chooses to discard v1.

## Representative retained v1/v2 fixture

The comparison fixture has one unit, 200 documents with long paths and hashes,
5,000 long SCIP-like symbol IDs, 5,000 definition occurrences, 4,999 call edges,
long fingerprints, full ranges, and deliberately repeated categorical strings.

```console
go test ./internal/storage -run '^$' \
  -bench '^BenchmarkStorageV1V2Representative$' \
  -benchtime=1x -benchmem -count=1
```

### Physical size

| Layout | Bytes | Change |
|---|---:|---:|
| v1 | 96,784,384 | baseline |
| v2 | 63,553,536 | **-34.3%** |

The checked-in non-short test requires v2 to remain smaller than v1 but does
not require an exact byte ratio because bbolt grows in coarse page/file steps.

The earlier design spike reached a smaller file by letting candidate indexes
tie-break on auto numeric IDs. Review caught that this could change bounded
results after replacement. V2 intentionally spends space on stable-ID ordering
keys in compound search indexes; exact query/truncation behavior is worth more
than the discarded size win.

### Bounded queries

Repeated exact-semantics search/traversal run:

```console
go test ./internal/storage -run '^$' \
  -bench 'BenchmarkStorageV1V2Representative/(v1|v2)/(Prefix50|Adjacency)$' \
  -benchtime=20x -benchmem -count=3
```

| Operation | v1 | v2 | Observation |
|---|---:|---:|---|
| full `FindSymbols`, limit 50 | 660–766 µs; 414,993 B; 7,164 allocs | 639–853 µs; ~416,102 B; ~5,590 allocs | Time overlaps/no clear change; allocations fall about 22%. V2 sorts bounded hot candidates before hydrating only the final 50. |
| full-evidence one-edge adjacency | 9.2–13.4 µs; 7,368 B; 90 allocs | 17.9–42.6 µs; 13,672 B; 194 allocs | V2 is slower because the public edge still requires a separately fetched cold detail. Numeric adjacency lookup itself remains index-backed. |

The final original microfixture, five runs, measured prefix at 461–476 µs,
338,602–338,614 B and 4,441 allocations: essentially the original v1 latency,
0.6% more bytes, and 16% fewer allocations. Its one-edge result was
16.8–17.1 µs. This confirms the same cold-detail cost at a second fixture size.

### Export, replacement, open, and compaction

Representative one-shot/targeted runs:

| Operation | v1 | v2 | Observation |
|---|---:|---:|---|
| complete export (1x) | 33.4 ms; 32.0 MB; 264,867 allocs | 44.9 ms; 29.6 MB; 413,658 allocs | 34% slower; output bytes allocated fall 7.5%, but joining detail/dictionary tables costs allocations. Export is diagnostic/unbounded, not the hot query. |
| replace 5,000-symbol/~15,200-fact unit (3x) | 1.063 s; 451.0 MB; 5.81M allocs | 1.247 s; 490.9 MB; 7.72M allocs | 17% slower. A transaction-local retention cache reduced repeated dictionary updates; the remaining cost is extra hot/detail rows and indexes. |
| open existing fixture (3x) | 41.2 ms; 214,749 B; 1,494 allocs | 40.1 ms; 347,906 B; 2,379 allocs | Time is equivalent/slightly lower after one-shot type registration; v2 validates more types/indexes and allocates more metadata. |
| physical compaction (1x) | 697 ms; 776 MB; 5.11M allocs | 533 ms; 453 MB; 5.98M allocs | About 24% faster and 42% fewer bytes allocated, with more allocations. |

Commands for the targeted measurements:

```console
go test ./internal/storage -run '^$' \
  -bench 'BenchmarkStorageV1V2Representative/(v1|v2)/Replace$' \
  -benchtime=3x -benchmem -count=1

go test ./internal/storage -run '^$' \
  -bench 'BenchmarkStorageV1V2Representative/(v1|v2)/Open$' \
  -benchtime=3x -benchmem -count=1
```

## Decision

Adopt v2. It materially reduces the representative file while preserving every
stable external identity, deterministic export, bounded ordering/truncation,
provenance, aggregate hot scan, generation, and unit-replacement contract.
Prefix lookup remains interactive and allocation-leaner. The cold evidence
split makes full edge materialization and whole export slower; that is recorded
rather than “optimized” by dropping evidence or re-inlining verbose ranges into
every hot record/index.

The next useful measurements are opt-in clean rebuilds of the named real
repositories and profiles of bstore detail hydration. Possible upstream bstore
projection support is noted in the prior-art report, but no external repository
or PR was changed.
