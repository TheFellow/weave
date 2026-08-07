# Federation, architecture, and CI prior art

Research date: 2026-08-06. This review favors specifications and maintained
project documentation. The implementation below adopts formats and lifecycle
ideas, not internal hosted-service machinery.

## Per-user state and locking

The [XDG Base Directory specification](https://specifications.freedesktop.org/basedir-spec/latest/)
distinguishes durable application state (`XDG_STATE_HOME`, default
`~/.local/state`) from portable user data and disposable cache. It requires
environment-provided paths to be absolute. macOS convention is
`~/Library/Application Support`; Windows exposes `LocalAppData` for local
application state. Weave therefore uses an explicit `--catalog`/`WEAVE_CATALOG`
override, then the platform state convention. A relative override is rejected.

The catalog is a bstore/bbolt database rather than an ad-hoc JSON file.
[bbolt](https://pkg.go.dev/go.etcd.io/bbolt) supplies transactions, a single
cross-process writer, concurrent readers, atomic page publication, and a
bounded open timeout. That is simpler and more portable than inventing stale
PID-file recovery. Catalog mutations are one transaction and callers receive a
lock-timeout diagnostic.

## Git identity and worktrees

[`git rev-parse`](https://git-scm.com/docs/git-rev-parse) is authoritative for
the worktree root, absolute Git directory, common directory, object format, and
Git-managed paths. [`git worktree`](https://git-scm.com/docs/git-worktree)
documents that linked worktrees share most repository state while retaining
per-worktree state. A filesystem path is consequently a checkout location, not
a repository identity. Weave records canonical remote-derived identity plus
worktree ID, root, branch/tree state, and the exact per-repository database path.
Registration is always explicit; there is no home-directory crawl.

## Content-addressed catalogs and disposable artifacts

[Bazel remote caching](https://bazel.build/remote/caching) separates a content
addressable store from an action cache and keys blobs by digest. The
[Nix content-addressing model](https://nix.dev/manual/nix/latest/store/store-object/content-address)
similarly makes content identity independent of a mutable source pathname.
Weave borrows the important boundary: catalog records locate independent graph
databases; immutable CI cache keys derive from schema/provider/tree/config
inputs. It does not copy all facts into one global database or treat a cache as
authoritative.

## Cross-repository semantic federation

SCIP symbols encode scheme, package manager/name/version, and descriptors; this
is the open exact-identity mechanism already preserved by Weave. Sourcegraph's
[precise navigation documentation](https://sourcegraph.com/docs/code-navigation/precise-code-navigation)
describes independently produced repository indexes, while its
[code-navigation overview](https://sourcegraph.com/docs/code-navigation)
describes compiler-precise cross-repository definitions and references.
Sourcegraph is evidence that exact global symbol IDs can join indexes, not a
reason to recreate its service.

Weave federates bounded reads over selected registered databases. It merges
facts deterministically, attaches repository/worktree provenance, and joins
only identical provider-issued symbol IDs. Display names never reconcile
symbols. Missing, stale, locked, or corrupt members produce explicit partial
diagnostics instead of suppressing healthy results.

## Architecture-policy tools

Existing tools converge on checked-in declarative rules over a dependency
model:

- [dependency-cruiser](https://github.com/sverweij/dependency-cruiser) validates
  JavaScript/TypeScript dependency rules and can report violations.
- [ArchUnit](https://www.archunit.org/userguide/html/000_Index.html) checks Java
  package/class dependencies and layered architecture in tests.
- [NetArchTest](https://github.com/BenMorris/NetArchTest) provides analogous
  fluent .NET assembly rules.
- [Import Linter](https://import-linter.readthedocs.io/en/stable/) defines Python
  architecture contracts such as layers and forbidden imports.
- [CODEOWNERS](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners)
  demonstrates repository-relative glob ownership, but is not a dependency
  policy engine.

Weave's first schema stays intentionally small: named layers select path,
package/unit, or symbol patterns; rules allow or forbid selected semantic edge
kinds from one layer to another. Matching uses documented path-style globs.
Violations retain the original edge and source range, so policy reports are
evidence, not inferred prose.

## SARIF and GitHub code scanning

[SARIF 2.1.0](https://docs.oasis-open.org/sarif/sarif/v2.1.0/sarif-v2.1.0.html)
defines `version`, the 2.1.0 schema URI, runs, tool/rule descriptors, results,
artifact locations, and one-based regions. Weave emits this standard shape and
stable rule IDs. [GitHub's SARIF upload documentation](https://docs.github.com/en/code-security/code-scanning/integrating-with-code-scanning/uploading-a-sarif-file-to-github)
requires SARIF 2.1.0 and documents supported result/location limits. Local
bounds remain lower and deterministic.

## Deterministic CI

GitHub Actions caches are immutable after creation and support keys composed
from runner context and input hashes, as documented by
[dependency caching](https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching).
[Workflow artifacts](https://docs.github.com/en/actions/tutorials/store-and-share-data)
are suitable for SARIF and deterministic textual exports. Weave's CI commands
therefore print a stable cache key, force/update the local derived index, verify
it, and check policy. The example workflow caches `.git/weave`, uploads SARIF,
and never commits a database.

## Adopted boundaries

1. One explicitly managed transactional catalog in platform state.
2. One entry per repository identity and worktree; paths remain locations.
3. Query-time bounded fan-out over independent databases.
4. Exact-ID federation only; no fuzzy cross-repository identity.
5. Checked-in JSON architecture configuration with deterministic text, JSON,
   and SARIF results.
6. Content-derived CI keys and disposable databases/artifacts; no daemon and no
   generated-state commits.

Deferred: catalog replication, automatic filesystem discovery, hosted artifact
exchange, approximate symbol reconciliation, YAML configuration, ownership
rules, and broad architecture DSL operators.
