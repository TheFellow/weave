# ADR 0010: Workspace and structured-content graph

- Status: Accepted
- Date: 2026-08-07
- Research: [Content semantic-indexing prior art](../prior-art/content-semantic-indexing/README.md)

## Context

Compiler graphs describe only part of a repository's knowledge. The
`TheFellow.github.io` corpus has authored Jekyll collections, YAML front matter,
canonical and generated Markdown representations, HTML/Liquid image links,
Mermaid and language fences, and deep links into many other repositories. The
`go-modular-monolith` corpus has a nested README architecture whose relative
links connect prose directly to source, policies, generators, and packages.
Neither repository becomes navigable merely by indexing what compiles.

The broader product goal is a local-machine knowledge graph that can answer
structural questions faster and with more semantic context than repeated
filesystem discovery. Content is not a weak fallback for code. It is another
source of declared and syntactic truth that joins compiler facts through stable
repository/path identities.

Prior art supports a source-only parser and resolver rather than executing a
site generator. Goldmark provides a maintained CommonMark/GFM AST with source
segments; `go-yaml` decodes front matter; `x/net/html` tokenizes inert raw HTML.
Git remains the authority for the visible file inventory. Jekyll, Liquid,
Mermaid, fenced examples, plugins, and URLs must not execute during indexing.

## Decision

1. Compose an always-on `weave-workspace` provider with compiler and bridge
   providers. It inventories tracked and nonignored untracked paths with
   `core.fsmonitor=false`. It uses `Lstat`, records symlinks without following
   them, and never enters `.git` or ignored build output.
2. Model the repository root, directories, regular files, symlinks, assets,
   structured documents, sections, code blocks, routes, series, topics, and
   absolute URLs as ordinary stable graph symbols. Exact repository paths keep
   Git's slash spelling and case. Every symbol ID hashes a versioned domain,
   canonical repository identity, entity kind, path, and local key; it never
   includes an absolute checkout path, branch, content digest, or provider
   version.
3. Use one unit per Git-visible file and one repository-inventory unit.
   Markdown, Markdown-shaped `llms.txt` files, front matter, headings, links,
   images, inert HTML, and fences are parsed into per-document units. The
   inventory unit owns repository/directory and shared route/topic/series/URL
   symbols. This avoids duplicate ownership while preserving atomic document
   replacement.
4. Add honest `links-to`, `embeds`, and `member-of` edges. Reuse `contains` for
   structure, `exposes` for declared routes, and `generates` only when an
   artifact explicitly names its source. Content syntax and containment are
   `Syntactic`; authored destinations and front matter are `Declared`; explicit
   generator provenance is `Generated`; Git-observed path existence is
   `Exact`. An ordinary hyperlink is not a compiler reference or dependency.
   Compiler providers may emit exact `defines` edges from the provider-neutral
   workspace path anchor to declarations they resolve in that file. The Go
   provider does so in this increment; executable adapters can adopt the same
   documented ID formula without owning or duplicating the path symbol.
5. Resolve relative paths and fragments against the exact case-sensitive Git
   inventory and the repository heading surface. Recognize statically declared
   front-matter permalinks and redirects. Recognize simple quoted
   `relative_url` operands without evaluating Liquid. GitHub repository,
   `blob`, and `tree` URLs remain declared URL resources; only unambiguous
   conventional refs gain an inferred `resolves-to` alias to the
   repository/path ID another catalog member may produce. Other absolute URLs
   remain URL resources, and Weave never claims they are reachable.
   Unsupported, ambiguous, or missing local destinations produce no fake
   endpoint or edge; a future content-reference fact will retain their raw
   syntax and diagnostic.
6. Treat fenced blocks as targetable syntactic objects with their info string
   in the display name. Do not feed partial snippets to compiler providers or
   execute Mermaid. A future adapter capability may accept virtual documents
   with origin maps, but that is a separate explicit contract.
7. Fingerprint each document from its path, bytes, provider/profile version,
   and repository link surface. A prose-only edit replaces one document unit.
   A route or heading-surface change conservatively re-resolves documents that
   may point at it. Inventory changes replace only the inventory and affected
   file units when document targets remain stable.
8. Expose `workspace find`, `workspace outline`, `workspace links`, and
   `workspace backlinks`. These queries are bounded, work locally or through
   the existing catalog, reject ambiguous content names, and aggregate
   section-owned links for a document. Existing graph export, path, integrity,
   policy, and federation machinery remains applicable.
9. Keep graph storage schema version 1. New symbol/edge strings fit the existing
   open normalized fact representation and bstore records. Future support for
   raw destinations, resolution status, dialect attributes, and bounded
   ambiguity candidates should add an explicit content-reference fact rather
   than encoding attributes into fake symbols or edge kinds.
10. Correct repository freshness inspection to hash only regular files. A
    changed or untracked symlink must never cause pre-provider code to follow
    and hash bytes outside the worktree.
11. Degrade deterministic per-document parse, encoding, and resource-limit
    failures to topology-only facts, and persist a bounded diagnostic in the
    current freshness manifest. Transient reads and path-identity races abort
    publication. Repository-wide inventory overflow also aborts rather than
    publishing a graph that silently omits containment.

## Profile boundary

The initial provider is deliberately a practical source profile, not a promise
to reproduce every renderer. It parses CommonMark/GFM, YAML front matter,
selected inert HTML, conventional GitHub-style heading fragments, and static
Jekyll-shaped routes. Provider version 1 names that behavior and participates in
fingerprints. Dynamic Liquid loops, includes, themes, config defaults, Hugo,
mdBook, wiki links, MDX execution, and custom heading renderers remain inert or
unresolved until an explicit, versioned profile defines them.

## Consequences

Weave can navigate repositories with no buildable artifact, connect docs to
assets and code paths, find cross-repository backlinks, inspect document
outlines, and distinguish authored sources from declared generated copies.
Compiler and content providers converge through stable IDs without one parsing
the other's domain.

Generated copies with one explicit leading source retain provenance and remain
separate documents, so consumers can choose whether to collapse those
representations. Segment provenance and collapse for multi-source full-text
aggregations remain deferred. Missing or unsupported local destinations are
intentionally omitted until a first-class
content-reference fact can retain their raw syntax and resolution status
honestly. A future `workspace check` can then provide content-specific
broken-link and route diagnostics. Site-generator-complete route truth and
first-class reference attributes remain future work.
