# Optional watch-mode warming prior art

Research date: 2026-08-07

## Question

How should an optional foreground `weave watch` process reduce query latency
without becoming a freshness authority, missing editor save patterns, or adding
an operationally fragile native-watcher subsystem?

## Native filesystem notification libraries

[`fsnotify`](https://github.com/fsnotify/fsnotify) is the established pure-Go
cross-platform notification library for Linux, macOS, BSD, Windows, and
illumos. Its own [package documentation](https://pkg.go.dev/github.com/fsnotify/fsnotify)
records the important limits for this use case:

- directory watches are not recursive, so a caller must discover, add, remove,
  and cap every directory watch;
- a watched file ceases to be watched when it is moved, so robust editor
  atomic-save handling requires watching parents and reconciling names;
- one logical write may produce many events, and platform event kinds differ;
- network/FUSE filesystems may not support notifications; and
- Linux watch counts and Windows buffers are finite, so overflow/degradation is
  part of the contract rather than an exceptional impossibility.

[watchexec](https://github.com/watchexec/watchexec) demonstrates a mature
higher-level response: recursively monitor, coalesce editor swap/backup event
bursts, apply ignore rules, and restart work after a quiet period. Adopting its
Rust engine in a Go library would, however, create a large cross-language
runtime/distribution boundary for an optional latency optimization.

[CodeGraph's watcher](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/sync/watcher.ts)
combines native recursive facilities where available with per-directory Linux
watches, directory limits, batch-sensitive debounce, scan reconciliation after
event storms, and bounded retry. Its
[watch policy](https://github.com/colbymchenry/codegraph/blob/969ea1ec371dc62d056cbeb3920fa79036128842/src/sync/watch-policy.ts)
is useful precedent for treating events as hints and a rescan as truth.

## Git is already Weave's observation boundary

The official [`git status` documentation](https://git-scm.com/docs/git-status)
defines porcelain v2 as a stable script format and distinguishes tracked,
worktree, index, untracked, and ignored state. `--untracked-files=all` enumerates
the same candidate universe that Weave's freshness coordinator already hashes.
It also warns that background status should use `--no-optional-locks` so a cache
refresh does not contend with foreground Git operations.

The official [`git worktree` documentation](https://git-scm.com/docs/git-worktree)
explains that linked worktrees share objects and most refs but retain separate
`HEAD`, index, and private Git directories. It recommends resolving Git paths
through plumbing rather than assuming `.git` is a directory. Weave already does
this and publishes a separate manifest/database/lock for each worktree.

Polling the existing exact Git observation has useful properties that raw file
events do not:

- an atomic rename is observed as the resulting repository state;
- event loss and queue overflow are irrelevant because every poll reconciles;
- ignored files stay ignored and untracked inputs retain existing semantics;
- branch/index/worktree changes outside the source directory are included;
- linked-worktree identity and storage require no special watcher paths; and
- refresh still uses the one authoritative provider/freshness pipeline.

The cost is a periodic `git status` plus hashes for changed paths. The official
documentation notes that untracked enumeration can be expensive in very large
worktrees. A configurable, nonzero interval and no work between ticks keep this
cost explicit. Native event hints remain a future optimization if measurement
shows polling is material at TheFellow repository scale.

### Local timing

On the 2026-08-07 Weave checkout on Darwin 24.6 x86-64 with Apple Git 2.50.1,
the repository had 14 visible dirty/untracked records while this increment was
being developed. Twenty-five consecutive commands equivalent to the poller's
Git status observation took 1.08 seconds wall time (about 43 ms each; 0.99
seconds combined user+system time). This is a deliberately rough lower-bound
measurement because the freshness observation also hashes changed regular
files. At the 750 ms default it is noticeable but bounded foreground work,
supports sub-second warming, and can be raised with `--poll-interval` for a
larger worktree. Repository-scale monitoring should remeasure this before
adding native event hints; the native path would still retain periodic exact
reconciliation.

## Coalescing, retry, and shutdown

Go's [`time.NewTicker` contract](https://pkg.go.dev/time#NewTicker) permits
dropped ticks for a slow receiver. That is desirable here: only one refresh may
run, and a later exact observation subsumes intermediate edit events. Stopping
the ticker on return makes lifecycle ownership explicit.

Transient refresh failures must not publish a manifest; the existing freshness
coordinator already enforces that. The watcher can report the error, keep
observing, retry immediately for a new observation, and otherwise use capped
exponential backoff. A single loop supplies stronger no-concurrent-refresh
behavior than a worker pool.

Go's [`signal.NotifyContext`](https://pkg.go.dev/os/signal#NotifyContext) turns
interrupt and termination signals into the same cancellation propagated through
Git and provider subprocesses. The returned stop function restores the normal
signal behavior and must be called by the process entry point.

## Decision

Adopt a bounded polling warmer first:

1. `weave watch` is an optional foreground process; queries still call
   `Freshness.Ensure` and no daemon or hook is installed.
2. Poll the exact existing repository/published-manifest observation rather than
   maintain a second recursive filesystem inventory. Do not reopen the graph
   database every interval; initial and query-time `Ensure` remain generation
   authorities.
3. Treat the poll interval as the coalescing window. Run at most one refresh and
   let slow work collapse ticker events.
4. Refresh only through `Freshness.Ensure(false)`. Failed work leaves the last
   complete manifest authoritative.
5. Retry a changed observation promptly and an unchanged failing observation
   with bounded exponential backoff.
6. Emit versioned newline-delimited events for machines and concise lifecycle
   lines for humans; errors are events in JSON mode and diagnostics on stderr in
   text mode.
7. Use signal-derived context cancellation and stop every owned ticker.
8. Add no native watcher dependency until benchmarks show that exact polling is
   too expensive. If native hints are added, periodic reconciliation remains
   necessary for correctness after loss/overflow.

## Rejected for this increment

- **Recursive `fsnotify` management:** mature primitive, but Weave would still
  need a directory crawler, ignore parity, atomic-rename repair, overflow
  recovery, Git metadata watches, and periodic exact reconciliation.
- **Platform-specific APIs:** duplicates the work intentionally isolated by
  `fsnotify` and expands the release/test matrix.
- **Hooks or a resident service:** opt-in warming does not justify mutating a
  repository or adding process supervision.
- **Refreshing every event concurrently:** creates provider storms and can only
  increase work because a later exact state subsumes the intermediate states.
