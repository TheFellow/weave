# Structured-content discovery prior art

Weave initially indexed Markdown files, headings, links, routes, and
containment, but symbol search only posted display-name tokens. A prose question
could therefore retrieve a section only when its wording resembled the heading.
That is materially weaker than the project-wide word indexes users already
experience in an IDE.

## Relevant precedents

- [IntelliJ file-based indexes](https://plugins.jetbrains.com/docs/intellij/file-based-indexes.html)
  use a Map/Reduce-shaped key/value projection. The word index maps a word to
  files and records a small context mask instead of persisting full source.
- [IntelliJ indexing and PSI stubs](https://plugins.jetbrains.com/docs/intellij/indexing-and-psi-stubs.html)
  distinguishes file-content indexes from compact declaration stubs. Weave
  likewise keeps lexical discovery separate from semantic evidence while both
  resolve to the same entity identity.
- SCIP and LSP navigation remain declaration/reference protocols rather than
  general prose search. They should continue supplying compiler truth; asking
  every language server to index documentation bodies would blur provider
  contracts and still omit non-code content.

## Decision carried into Weave

1. A provider may attach a bounded sorted token set to an exact normalized
   entity. The workspace provider does this for Markdown document preludes,
   direct section bodies, fenced code blocks, and bounded regular UTF-8 files
   of any extension.
2. bstore token postings index display-name tokens and these additional terms
   together. Search returns the original entity with its provider, evidence,
   source range, and stable identity; no second full-text database is added.
3. Search terms are lexical hints only. Their presence cannot create an edge,
   upgrade syntactic evidence, or claim that prose is executable code.
4. Source text is not copied into the graph. A source-rich result still opens
   the current Git-visible file and verifies its identity, hash, UTF-8 encoding,
   range, and byte budget.
5. Terms are normalized, unique, deterministic, at most 128 bytes each, and
   bounded to 2,048 per entity. Binary/assets, files over 2 MiB, and content
   after a deterministic 512 MiB corpus ceiling remain topology-only.
6. The terms survive logical export and machine-aggregate materialization, so
   local and catalog discovery have the same semantics.
7. After the initial build, exact Git diff paths select generic files to reread;
   previous complete units for unchanged files are carried forward untouched.
8. Agent retrieval uses the same inexpensive signals common to lexical search:
   uncommon bounded postings carry more weight, broad entity vocabularies carry
   less, and sections in the strongest matching document receive a modest
   coherence boost. This is deterministic ranking over bstore postings, not a
   second full-text service.
9. Generated Markdown representations and `llms*.txt` aggregations remain in
   the graph with generated evidence but do not displace the smaller authored
   section. Generic files have no stored term positions, so a winning dossier
   safely reopens current source and selects its best matching line.

Storage is disposable, so this extends the normalized model and advances the
private storage marker without an in-place migration. Existing worktree indexes
are removed and deterministically rebuilt.
