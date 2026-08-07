# Git-aware semantic graph diffs

- Research date: 2026-08-07
- Scope: deterministic ref/worktree comparison, normalized graph deltas, API
  honesty, reverse impact, affected tests, and animated graph consumers.

## Prior art

### Git plumbing is the source-change authority

Git's official [`git diff` raw/name-status format][git-diff] distinguishes
additions, deletions, modifications, copies, and renames, and its `-z` form is
unambiguous for unusual path names. `git diff-tree` compares committed trees;
`git diff <tree>` compares a tree with the index and working tree. Weave should
use this inventory rather than infer source changes from semantic facts.

The official [`git worktree` interface][git-worktree] provides detached linked
worktrees and a stable porcelain format. A temporary detached worktree lets the
same provider pipeline index a historical commit without checking out the
user's branch or mixing historical facts into the current worktree database.
Cleanup must remove both the temporary directory and Git's linked-worktree
metadata, including after cancellation or provider failure.

`git worktree add` performs a checkout, so repository hooks and configured
checkout filters are executable-code boundaries. Weave must override
`core.hooksPath` with an empty private directory for materialization. There is
no generic Git switch that disables every named clean/smudge/process filter;
configured filters therefore remain an explicit local Git trust limitation and
must be documented rather than silently described as inert parsing.

### Precise facts are snapshots, not compatibility claims

Sourcegraph's [precise code navigation documentation][sourcegraph-precise]
models SCIP data as language-neutral, commit-associated indexes. Its
[upload lifecycle][sourcegraph-uploads] keeps commit-to-index eligibility
explicit and treats unavailable indexes as a state, rather than silently using
unrelated data. Weave should likewise identify both graph snapshots and fail
when a requested ref cannot be indexed.

A normalized before/after graph can accurately report added, removed, and
changed facts. It cannot by itself prove source or binary compatibility. The
only cross-language API signal currently common to Weave providers is the
provider-owned public-surface fingerprint on a unit. Therefore API output
should report added/removed/changed provider surfaces with compatibility
`unknown`; it must not label a lexical name change as breaking.

### Affected tests are a projection of impact

CodeGraph's pinned [affected-test design audit][weave-codegraph] found useful
CLI ergonomics in accepting Git changes, but also the need to preserve the
reason and evidence for every selected test. Nx's official
[`affected` model][nx-affected] combines a Git base/head change set with its
project graph. Weave should use the same broad composition while retaining its
stronger fact evidence: changed graph/source roots feed the existing reverse
impact traversal, and tests are projected from the resulting nodes. Explicit
`tests` edges retain their evidence; provider-classified test symbols retain
provider evidence; filename/name recognition is labeled syntactic.

### Stable identities enable browser transitions

The existing explorer uses pinned [d3-graphviz][d3-graphviz], whose keyed data
join and path/shape tweening preserve surviving SVG nodes between DOT renders.
The semantic diff contract should therefore expose stable entity/edge IDs plus
before and after facts. The explorer can animate `added`, `removed`, and
`changed` states without introducing a second graph model or diff engine.

## Decision

1. Add a versioned `weave.snapshot-diff/v1` application contract and a
   `weave diff {graph,api,impact,tests}` CLI family.
2. Require `--base`; omit `--head` to compare against the current dirty
   worktree, or provide a second Git revision for a clean ref-to-ref comparison.
3. Resolve revisions to exact commit/tree object IDs before indexing. Index
   historical sides in temporary detached worktrees through the configured
   freshness provider and delete that derived state afterward.
4. Keep Git source changes separate from normalized unit/document/symbol/
   occurrence/edge changes. Sort every collection by stable identity and bound
   each emitted collection.
5. Report provider public-surface changes with exact opaque fingerprints and
   compatibility `unknown`. A future provider may add a language-specific ABI
   classifier, but the core never guesses.
6. Seed the existing reverse-impact traversal from added/changed current graph
   facts and current documents matched by Git changes. Removed-only facts remain
   visible in the graph delta but cannot be traversed in the head graph; report
   this limitation diagnostically.
7. Return affected tests with selection reason, evidence, and any supporting
   edge ID. Empty output is not promoted to a completeness claim when providers
   report diagnostics.

## Rejected approaches

- Checking out a ref in the user's worktree: mutates user state and races edits.
- Parsing blobs with a second lightweight mapper: produces facts that are not
  comparable with the authoritative provider graph.
- Treating file diffs as graph diffs: misses semantic stability and invents
  removals for provider-irrelevant files.
- Calling every removed public-looking symbol a breaking change: the normalized
  core has no language-specific compatibility contract for that assertion.
- Maintaining a second impact/test walker: traversal bounds and edge semantics
  would drift from the query CLI.

[git-diff]: https://git-scm.com/docs/git-diff#_raw_output_format
[git-worktree]: https://git-scm.com/docs/git-worktree
[sourcegraph-precise]: https://sourcegraph.com/docs/code-navigation/precise-code-navigation
[sourcegraph-uploads]: https://sourcegraph.com/docs/code-navigation/explanations/uploads
[nx-affected]: https://nx.dev/ci/features/affected
[d3-graphviz]: https://github.com/magjac/d3-graphviz/tree/355158dc789ff8556549018a0c0f7a567ac0bfc3
[weave-codegraph]: ../codegraph-and-animated-explorer/README.md#affected-tests
