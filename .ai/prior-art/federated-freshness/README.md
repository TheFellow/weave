# Federated freshness prior art

Research date: 2026-08-06

## Sources

- [Kythe Compilation Database specification](https://kythe.io/docs/kythe-compilation-database.html)
  records complete revisions separately from compilation units, making the revision
  used by a read an explicit part of index state.
- [Kythe overview](https://kythe.io/docs/kythe-overview.html) recommends graceful
  partial results while preferring incomplete data over incorrect data.
- [Git diff documentation](https://git-scm.com/docs/git-diff.html) defines precise
  commit, index, and working-tree comparison endpoints.

## Decision for Weave

The CLI refreshes every selected, locally available catalog worktree before opening
its database. Refresh failures are reported per repository and that member is
excluded; stale facts are never silently queried. Other healthy members may still
produce deterministic partial results with provenance and diagnostics.

The lower-level federation store retains a non-refreshing constructor for isolated
storage tests and embedding. The user-facing application uses only the freshness-
enforcing constructor. A catalog entry is location metadata, not proof of currency.

