# Reviewed sources and revisions

Research date: 2026-08-06 (America/Los_Angeles)

| Project | Inspected revision/release | License | Relevant material |
| --- | --- | --- | --- |
| `mjl-/bstore` | [`v0.0.11`](https://github.com/mjl-/bstore/tree/v0.0.11) (`80fb847`) | MIT | package docs, schema registration, compound indexes, query planning, transaction errors, export API |
| `etcd-io/bbolt` | [`v1.4.3`](https://github.com/etcd-io/bbolt/tree/v1.4.3) | MIT | ACID transactions, locking, freelist behavior, `Compact`, `Tx.Check` |
| `colbymchenry/codegraph` | [documentation](https://colbymchenry.github.io/codegraph/core-concepts/how-it-works/) | MIT | local SQLite symbol/edge store, callers/callees/impact query shape |
| `sourcegraph/zoekt` | [`design.md`](https://github.com/sourcegraph/zoekt/blob/main/doc/design.md) | Apache-2.0 | positional trigram posting lists, deterministic shard/index design |
| Go standard library | Go 1.24 [`encoding/json`](https://pkg.go.dev/encoding/json) and [`sort`](https://pkg.go.dev/sort) | BSD-3-Clause | JSON key behavior and explicit stable ordering |

## Reproducibility notes

- `go list -m -versions github.com/mjl-/bstore` reported v0.0.1 through
  v0.0.11; `@latest` resolved to v0.0.11, published 2026-06-27.
- `go list -m -versions codeberg.org/mjl/bstore` failed because the Codeberg
  VCS endpoint requested credentials, and no package exists at that module path
  on pkg.go.dev. This is why the implementation uses the published GitHub
  module path.
- bstore v0.0.11 currently resolves bbolt v1.4.3 through its module graph. Weave
  pins both when direct bbolt compaction is introduced.
