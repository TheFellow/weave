# Change impact and test selection prior art

Research date: 2026-08-06

## Sources

- [Git diff documentation](https://git-scm.com/docs/git-diff.html) defines a named
  commit versus working-tree comparison and recommends `-z` for unambiguous paths.
- [Kythe schema](https://kythe.io/docs/schema/) models semantic references, calls,
  dependencies, and an `influences` relation rather than guessing from text.
- [Kythe influences relation](https://kythe.io/docs/schema/influences-relation.html)
  explicitly bounds what an indexer can prove and excludes relationships requiring
  unsupported whole-program alias reasoning.

## Decision for Weave

Impact accepts one of four root forms: a symbol, repository-relative files,
packages, or paths changed between a Git revision and the current working tree.
Files seed the traversal from graph symbols defined or referenced in their indexed
documents. Packages seed it from matching package symbols and owned documents.
Multiple seeds share one deterministic bounded reverse traversal.

Affected tests are a projection of impacted graph nodes, reported only when stored
facts identify a test declaration/document or an explicit `tests` edge. Weave does
not claim build-system target selection and does not infer a test from directory
names alone. Git path collection uses `git diff --name-only -z <revision> --` and
is therefore safe for unusual filenames; untracked files are added from the
repository overlay inventory.
