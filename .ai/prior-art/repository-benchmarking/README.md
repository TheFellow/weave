# Repository-scale benchmark methodology: prior art

Research date: 2026-08-06

- Go's [`testing` benchmark format](https://pkg.go.dev/testing#hdr-Benchmarks)
  and [`benchstat`](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat) distinguish
  repeatable samples from anecdotes and preserve configuration labels beside
  results. We retain the existing microbenchmarks for implementation A/B work.
- [hyperfine](https://github.com/sharkdp/hyperfine) provides warmups, repeated
  command samples, outlier detection, and machine-readable exports. It is an
  excellent optional runner, but requiring another binary would make the first
  repository baseline harder to reproduce. The checked-in harness therefore
  uses portable shell plus `/usr/bin/time -p` and records every raw sample.
- [Git worktrees](https://git-scm.com/docs/git-worktree) provide independent
  worktree state with repository-local administrative data. For measurements,
  an even stronger isolation boundary is a local no-hardlink clone at an exact
  commit: target repositories and their existing `.git/weave` indexes remain
  untouched, while Weave still exercises real Git storage discovery.

## Method adopted

Build one candidate executable, clone each named source locally at its current
HEAD, prepare compiler dependencies outside the timed section, and measure:

1. a cold forced index into the clone's `.git/weave`;
2. five no-change queries, each of which executes the freshness check;
3. deterministic export/verification and database size/fact counts.

Keep stdout payloads outside timing stderr, preserve the raw samples, report
failures rather than silently dropping repositories, and record OS, CPU,
toolchains, candidate commit, source commits, and exact commands. The harness
has a wall-clock timeout per command and never edits the source repositories.

