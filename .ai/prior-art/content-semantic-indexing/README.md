# Content semantic indexing prior art

Research date: 2026-08-07

## Recommendation

Add a built-in, source-only content provider to Weave. Start with Markdown by
parsing tracked UTF-8 source with [`goldmark`][goldmark], then layer explicitly
selected dialect and site-generator profiles over the CommonMark/GFM syntax
tree. The provider should extract pages, headings, links, reference
definitions, code fences, front matter, selected inert HTML attributes, and
diagnostics. It must not render a site, run a plugin or preprocessor, evaluate
a template or diagram, fetch a URL, or execute fenced code.

`goldmark` is the best first parser because it is pure Go, MIT licensed,
CommonMark 0.31.2 compliant, extensible, and exposes an AST with source
positions. Its GFM extension covers tables, strikethrough, linkification, and
task lists. Use [`cmark-gfm`][cmark-gfm] and the CommonMark/GFM specifications
as conformance oracles, not as a required C/cgo runtime dependency. Use
[`go.abhg.dev/goldmark/frontmatter`][goldmark-frontmatter] selectively for YAML
and TOML framing and decoding; retain the raw span and impose Weave's own
limits before decoding.

Do not define one implicit “Markdown” behavior. CommonMark, GitHub repository
Markdown, GitHub Pages/Jekyll, Hugo, mdBook, GitHub wikis, and Obsidian/Foam
notes disagree about heading IDs, page URLs, root-relative paths, wiki links,
front matter, includes, and generated pages. A provider profile must state the
rules it applies. Profile, configuration, parser version, and rule-set version
belong in fingerprints and provenance, not in durable page identity.

The implementation should have two deterministic stages:

1. **Document extraction** parses one file and emits its local structure and
   unresolved outgoing references. This is the atomic replacement unit.
2. **Repository resolution** builds a compact “link surface” from every page's
   path, logical URL, aliases, headings, explicit IDs, and assets, then resolves
   outgoing references against it. Backlinks are inverse query results, not
   separately authoritative facts.

This division is the key to useful incrementality. A changed page is reparsed;
only documents whose recorded target-surface dependencies changed need link
resolution repeated. No agent or LLM is needed for extraction, resolution,
freshness, or query.

Before implementation, evolve the fact model deliberately. Weave's current
edges connect symbols, so a page needs a real page symbol. The existing edge
vocabulary does not distinguish links, embeds, aliases, includes, or section
membership, and an edge cannot retain a raw destination, normalized
destination, profile, resolution status, or bounded candidate set. Reusing
`references`, `contains`, and `depends-on` for all of those would make queries
misleading. Content indexing is therefore both a provider and a small schema
exercise.

## Scope and non-goals

The first useful slice should index:

- Markdown pages and sections, including ATX and Setext headings;
- inline, reference-style, image, autolink, and profile-enabled wiki links;
- link-reference definitions and their document-wide scopes;
- fenced and indented code blocks, with the fence info string retained as a
  declaration rather than trusted as a language truth;
- selected front matter such as title, permalink/URL/slug, aliases, tags,
  categories, layout, draft, and weight;
- raw HTML `id`, `name`, `href`, `src`, and heading structure where it can be
  extracted without rendering;
- repository-local assets and unresolved/external destinations; and
- source ranges, diagnostics, provenance, evidence, and deterministic
  fingerprints for all material facts.

It should not initially claim to model:

- the final DOM, CSS, JavaScript, client-side routes, or browser behavior;
- evaluated Liquid, Go templates, shortcodes, macros, includes, or theme code;
- pages synthesized by plugins, data files, remote modules, or a CMS;
- the result of executing Mermaid, Graphviz, notebooks, examples, or code
  fences;
- the availability or content of network URLs;
- exact GitHub Pages output when arbitrary Jekyll plugins or themes participate;
  or
- compiler semantics for incomplete code snippets embedded in prose.

Those omissions are honest boundaries, not missing parser features. A static
source graph remains highly useful for navigation, impact, orphan detection,
broken local links, documentation coverage, and joining prose to code.

## Parser and tool options

| Option | What it provides | Fit for Weave |
| --- | --- | --- |
| [`goldmark`][goldmark] | Pure-Go CommonMark parser, source-positioned AST, GFM extensions, custom parsers/transformers, fuzz testing, and no non-standard dependency for the core. | **Adopt.** It gives the smallest safe and portable correctness floor. Use AST traversal only; rendering is unnecessary. |
| [`cmark-gfm`][cmark-gfm] | GitHub's C implementation and testable GFM behavior. | Use as a pinned conformance oracle. Shipping it would add cgo/native distribution work without solving GitHub's post-render heading IDs or site-generator semantics. |
| [`tree-sitter-markdown`][tree-sitter-markdown] | Incremental concrete syntax, separate block/inline grammars, optional GFM/front-matter/wiki extensions, and injection-oriented nodes. | Do not use as the normative parser. Its own README says Markdown's grammar does not fit Tree-sitter cleanly, the output still has inaccuracies, and it is not recommended where correctness matters. Revisit for MDX/editor-grade incremental parsing or language injections. |
| [`goldmark-frontmatter`][goldmark-frontmatter] | Goldmark extension for YAML and TOML front matter, custom formats, typed decoding, and raw metadata access; BSD-3-Clause. | Adopt selectively. Validate delimiters, size, nesting, aliases, scalar lengths, and allowed fields independently. Hugo JSON front matter still needs a profile-specific path. |
| [`x/net/html` tokenizer][x-net-html] | Inert HTML token stream, fragment tokenization, lower-cased tag/attribute names, raw token bytes, and a maximum tokenizer buffer. | Use for bounded extraction from raw-HTML source spans. Do not build or sanitize a rendered DOM, and never follow `src`/`href`. |
| [`Marksman`][marksman] | MIT F# Markdown LSP with inline/reference/wiki links, definitions, references/backlinks, completion, and duplicate/ambiguous-heading diagnostics. | Excellent fixture and behavior oracle. LSP is interactive and does not provide Weave's complete atomic inventory, evidence, or freshness contract. |
| [`markdown-oxide`][markdown-oxide], [`zk`][zk], and [`Foam`][foam] | Local Markdown knowledge-base navigation, wiki links, headings/blocks, backlinks, ambiguity handling, and editor integration. | Mine their fixtures and resolution lessons. Their note-taking dialects are useful profiles, not universal Markdown semantics; editor/LSP state is not the durable Weave graph. |
| SCIP/LSIF | Static code-navigation symbols, occurrences, relationships, ranges, and documents. | Keep for code. A heading can be forced into a SCIP symbol, but SCIP has no native page URL/profile/front-matter/link-resolution model, no Weave evidence classes, and no unit freshness contract. Round-tripping content through it would discard the facts that matter. |
| LSP | Standard stdio JSON-RPC operations for document symbols, links, definitions, references, diagnostics, and workspace symbols. | Useful for conformance and optional editor integration. Querying every link/symbol does not yield a bounded complete snapshot, surface fingerprints, or an atomic unit terminal state. |

The parser choice does not determine link semantics. `goldmark` supports custom
heading-ID logic, but its default ID algorithm is not a promise of GitHub,
Jekyll, Hugo, or mdBook compatibility. In particular, Goldmark's current
default lowercases ASCII, retains ASCII letters/digits, maps selected separators
to hyphens, drops other characters, and suffixes duplicates. Treat any slugger
as a versioned profile component with fixtures, not as generic Markdown.

## Profiles: parse syntax once, apply explicit semantics

The provider should select profiles from repository configuration and
well-known files, while allowing an explicit override. Detection may suggest a
profile; it must not merge conflicting ecosystems silently.

| Profile | Syntax and source rules | Resolution consequences |
| --- | --- | --- |
| `commonmark` | CommonMark only. Reference definitions have document scope. CommonMark deliberately says nothing about generated heading IDs. | Resolve file paths and explicit HTML IDs; auto-heading fragments remain unavailable unless a separate slug policy is configured. |
| `gfm` | CommonMark plus the four GFM extensions. Fenced-code content is literal; the first info-string word is conventionally a language, but its treatment is not specified. | GFM still does not specify GitHub's generated section-anchor algorithm or repository URL rewriting. |
| `github-repository` | GitHub rendering of README and `.md` files, including GitHub's documented section-link rules and repository-relative links. | Relative links are based on the current source file; leading `/` is repository-root-relative, and rendered branch/blob URLs are contextual. This is distinct from GitHub Pages. |
| `github-pages-jekyll` | Jekyll YAML front matter, Markdown plus Liquid text, `_posts`, collections, and selected GitHub Pages defaults. GitHub Pages runs Jekyll in safe mode with a supported plugin set, but themes and Liquid still affect output. | Index declared permalinks, titles, tags/categories, and literal `{% link %}`/`post_url` targets where statically parseable. Label derived routes appropriately; never run Jekyll, Bundler, Liquid, plugins, or theme code in safe source-only mode. |
| `hugo` | Goldmark-oriented Markdown plus YAML/TOML/JSON front matter, `index.md`/`_index.md` page bundles, multilingual/config rules, shortcodes, and render hooks. | Source path, section, slug, `url`, aliases, language, cascade, and config can all affect the public URL. Custom link render hooks, templates, themes, and modules mean source-only resolution may be inferred rather than rendered-exact. |
| `mdbook` | CommonMark plus mdBook extensions. `SUMMARY.md` is a strict authored hierarchy; `book.toml` selects the source tree and preprocessors. | `.md` generally maps to `.html`, `README.md` maps to `index.html`, and fragments target headings. Parse `SUMMARY.md` and `book.toml`; never execute preprocessors, commands, or includes during safe indexing. |
| `github-wiki` | Pages live in a separate `<repo>.wiki.git` repository and GitHub chooses a converter from the extension. Markdown and MediaWiki-style links may coexist. | Federation must attach the wiki as another repository. Filename/title lookup and extension omission can yield multiple candidates; retain ambiguity. GitHub documents unsupported transclusion rather than a universal include model. |
| `notes-wikilink` | Explicit opt-in `[[page]]`, `[[page#heading]]`, aliases, embeds, and possibly block references. Obsidian, Foam, zk, and other tools differ. | Resolve exact paths first, then profile-defined aliases or shortest unique names. A basename/title collision is `Ambiguous`, never an arbitrary winner. Block references are profile-specific. |

GitHub itself demonstrates why profiles matter. [`github/markup`][github-markup]
selects a markup converter, after which GitHub sanitizes HTML, highlights code,
adds named anchors, and autolinks references in separate steps. The GFM grammar
therefore cannot reproduce the whole GitHub page pipeline. GitHub's own writing
documentation gives practical heading-anchor rules, including duplicate
suffixes, but this remains GitHub-renderer behavior rather than GFM syntax.

## Proposed content fact model

### Symbols

Introduce content kinds without pretending that prose is compiler-resolved
code:

- `page`: one source page and the endpoint for incoming/outgoing page links;
- `section`: a heading-addressable region within a page;
- `link-definition`: a document-scoped reference-label definition;
- `code-block`: a fenced or indented literal block;
- `asset`: a repository-local non-page link target;
- optionally `tag`, `category`, and `collection` when they have query value.

Every document gets exactly one page symbol, even if it has no heading or title.
Contain sections and blocks under the page/nearest containing section. A title
is display metadata, not the page's durable identity.

### Edges

Add a small explicit vocabulary rather than overloading code edges:

- `links-to`: a navigational hyperlink from a page or section;
- `embeds`: an image, media, attachment, or profile-defined transclusion;
- `includes`: a statically declared source inclusion, distinct from an embed;
- `section-of`: structural ownership of a section by a page/section;
- `aliases`: an alternate route or name declared for a page;
- `tagged-with`: a declared tag/category relationship.

`documents` remains useful for prose-to-code relationships that another
provider establishes. `references` can represent a code symbol mentioned by a
resolved language-aware construct, but an ordinary Markdown destination should
remain `links-to`.

Each resolved or unresolved link also needs bounded provenance. The durable
model should retain, directly or through a content-reference record:

- source document/range and owning page/section;
- raw destination and normalized destination;
- syntax kind: inline, reference, autolink, image, HTML attribute, wiki link,
  generator link, or front-matter route;
- provider, profile, profile version, and evidence;
- resolution status: local, external, unresolved, escaped-root, unsupported,
  or ambiguous;
- one target or a bounded sorted candidate list; and
- diagnostics/truncation when candidates or text exceed limits.

The current `Edge` cannot express unresolved references or per-edge attributes,
and every endpoint must already be a symbol. Inventing fake external symbols
for arbitrary raw URLs would inflate and obscure the graph. A first-class
bounded reference/fact-attribute record is cleaner; only resolved targets need
become graph edges.

### Stable identity

Use identities that survive parser upgrades but change when the authored
address itself changes:

```text
page     = repository identity + normalized repository-relative source path
section  = page identity + profile-resolved explicit/generated fragment
refdef   = page identity + CommonMark-normalized reference label
asset    = repository identity + normalized repository-relative asset path
block    = page/section identity + source anchor + content digest + disambiguator
```

Important qualifications:

- Do not put parser/provider versions, Git commit IDs, titles, public URLs, or
  front-matter display data in stable IDs. They are semantic inputs or aliases.
- A source-path rename changes page identity unless a future Git-aware rename
  layer records continuity. Guessing continuity from similar text is not exact.
- A generated heading fragment is renderer/profile-specific. Heading text,
  punctuation, Unicode handling, or duplicate order can change the address, so
  changing section identity is correct. An explicit ID is stronger.
- Repeated headings require the profile's duplicate disambiguation. Do not
  collapse them onto one section symbol.
- Fence ordinals alone churn when an earlier block is inserted. Scope plus
  source anchor, content digest, and a deterministic collision disambiguator is
  a better compromise. Blocks are less stable than public pages/sections and
  should be documented as such.
- Windows separators never enter identities. Normalize to validated
  repository-relative `/` paths without case-folding. On a case-insensitive
  worktree, report collisions rather than merging authored paths.

### Evidence

Apply Weave's existing evidence classes conservatively:

| Fact | Evidence |
| --- | --- |
| Heading, fence, link-reference definition, literal inline link, raw HTML attribute, and source containment parsed under a recorded grammar | `Syntactic` for structure; the destination as authored is `Declared`. |
| Front-matter title, permalink, alias, tag, category, layout, or explicitly configured route | `Declared`. |
| A local link resolved uniquely under a known profile | `Declared`: the author declared the link and Weave deterministically resolved its target. Do not upgrade it to compiler `Exact`. |
| Backlink/inverse adjacency or a virtual document projected from a source block | `Generated`, with the originating declared/syntactic fact retained. |
| URL/slug predicted from Jekyll/Hugo/mdBook source without executing the effective build pipeline | `Inferred` unless the selected profile fully determines that route. |
| Basename, title, alias, case, Unicode, or extension lookup with several valid targets | `Ambiguous`, retaining bounded candidates. |
| Unsupported template/shortcode/include expression | No edge; retain a diagnostic and the raw declared occurrence. |

Calling every parser result `Exact` would erase the distinction that makes
Weave trustworthy. A parser can be exact about syntax while the associated
page address remains renderer-dependent.

## Link extraction and resolution

### Local extraction

Walk the Goldmark AST and record source spans before any resolution:

1. Establish the page and profile from repository configuration, not front
   matter alone.
2. Parse front matter only when it occupies a profile-valid leading position.
   Preserve delimiter/body spans and emit bounded diagnostics for malformed or
   unsupported data.
3. Record headings, explicit IDs, heading hierarchy, visible text, and the
   selected slugger's generated fragment.
4. Record inline/reference links, images, autolinks, and their raw destinations.
   CommonMark reference definitions affect the whole document even when they
   appear inside a block container.
5. Record fences and indented blocks as literal content. Parse the info string
   into bounded tokens without executing or trusting it.
6. Tokenize raw HTML source spans only for allowlisted structural/link
   attributes. Preserve raw offsets; do not parse scripts/styles as Markdown.
7. Recognize profile-specific wiki links, generator link tags, or shortcodes as
   separate syntax kinds. Unsupported dynamic expressions remain unresolved.

Never regex the entire file for Markdown links. Code spans, fences, escapes,
raw HTML, nested brackets, reference labels, and block structure make such a
scanner observably wrong. A small scanner is still appropriate for a narrowly
specified profile construct the core AST deliberately treats as text, provided
it consumes only the relevant AST span.

### Link surface

Each page publishes a sorted, compact surface containing only resolution inputs:

- normalized source path and eligible extensionless/index forms;
- page title and explicit aliases where the profile permits name lookup;
- logical/public route candidates with evidence;
- explicit IDs and profile-generated heading fragments;
- local assets/resources owned by page bundles; and
- collection/language identity needed by the active profile.

Hash this surface independently from the full document. A body-only prose edit
does not force inbound links to re-resolve. A changed path, route, alias, or
fragment does.

### Resolution order

Resolution must be deterministic and profile-specific. A safe common skeleton
is:

1. Classify URI schemes before path handling. `http`, `https`, `mailto`, and
   other permitted schemes are external and are never fetched.
2. Split path/query/fragment using URI rules; percent-decode only where the
   profile defines it, reject invalid encodings, and never convert encoded
   separators into an escape opportunity.
3. Resolve an empty path against the current page, a relative path against the
   current source page/profile base, and a root-relative path against the
   profile's declared root.
4. Lexically clean the candidate and prove it remains inside the indexed
   repository/content root before looking it up.
5. Apply only the profile's extension, `index`/`README`, bundle, route, and
   wiki-name rules in a documented order.
6. Resolve a fragment against the selected page's explicit/generated section
   surface. Preserve a missing fragment as unresolved even when the page
   exists.
7. Emit one edge for one target, no edge plus candidates for ambiguity, and a
   diagnostic for unresolved or escaped-root destinations.

Do not probe the host filesystem for fallback spellings. Resolve against the
tracked inventory. This avoids symlink escapes, platform-dependent casing, and
results that differ between a developer laptop and CI.

## Front matter and site generators

Front matter is data with ecosystem semantics, not generic YAML truth.

Jekyll requires YAML front matter to begin at the first line between `---`
delimiters. Fields such as `permalink`, `tags`, and `categories` affect page
relationships and routes, while `layout` causes template participation.
Jekyll's default Markdown stack and GitHub Pages plugin set can vary from
GitHub's repository Markdown renderer. Liquid variables, includes, collections,
themes, defaults, and plugins can all alter effective output.

Hugo accepts YAML, TOML, or JSON front matter and combines source paths,
`slug`, `url`, aliases, page bundles, language configuration, cascades,
templates, render hooks, themes, and modules. Static source analysis should
index declared metadata and deterministic configuration, then downgrade route
predictions whenever effective templates or hooks can override them.

mdBook gives unusually useful static structure: `SUMMARY.md` declares chapter
order and hierarchy, and `book.toml` declares the source directory. Its
preprocessors are executable commands using JSON over stdin/stdout, and include
directives can read other files. This protocol is excellent prior art for
extensibility but is precisely why safe indexing must parse configuration
without running the configured programs.

For all front-matter formats:

- bound bytes, lines, nesting depth, collection sizes, scalar lengths, aliases,
  and total decoded nodes;
- project only recognized scalar/list fields into facts; retain unknown keys as
  a count/diagnostic rather than an unbounded object graph;
- disable or reject unsafe/custom YAML tags and pathological alias expansion;
- sort maps and normalized lists before hashing;
- retain raw author text and source ranges where possible; and
- include the selected profile and relevant repository configuration content
  in the unit input fingerprint.

## Fenced code and virtual documents

A fence is useful immediately as a `code-block` symbol with source range,
literal content hash, and declared info string. It is not automatically a
compilable unit. GFM says code-fence contents are literal and leaves treatment
of the info string to renderers; `go`, `python`, or `csharp` is only an authored
hint.

Semantic snippet indexing should be a later protocol feature:

- the core sends bounded virtual-document bytes, a declared language, a stable
  virtual URI, and an exact origin-range map to a capable language adapter;
- capability negotiation must distinguish full compilation units from snippet
  mode;
- returned ranges are mapped back to the fence while preserving the adapter's
  evidence and diagnostics;
- incomplete snippets, ellipses, hidden setup, doctest prompts, or omitted
  imports are not silently wrapped into compilable programs; and
- no temporary file, compiler, interpreter, package restore, or build tool is
  invoked without an explicit adapter capability and permission.

The current adapter request is path/repository oriented and does not carry
virtual source plus origin maps. Extend that contract before delegating fences.
Until then, the native content provider should expose block text/ranges for
query and leave code semantics unclaimed.

## Freshness and atomic updates

Use one source document as the initial content unit. Its full input fingerprint
should include:

- source bytes and normalized repository-relative path;
- provider/parser/profile/rule-set versions;
- relevant site configuration and front-matter interpretation mode;
- root, collection, language, base-URL, permalink, and wiki-resolution rules;
- the source inventory entries needed for path resolution; and
- the exact target-surface fingerprints consulted by its outgoing references.

Publish a separate surface fingerprint over path, routes, aliases, heading
fragments, and asset names. Maintain a reverse dependency from a destination
surface to sources that consulted it. Then:

1. reparse added/changed documents and remove deleted units;
2. rebuild surfaces only for changed inventory/configuration inputs;
3. compare old and new surface fingerprints;
4. re-resolve the changed document's outgoing links;
5. re-resolve only sources dependent on changed surfaces or lookup namespaces;
6. atomically replace each source-owned fact unit; and
7. derive backlinks from incoming `links-to`/`embeds` edges.

A naive global basename/wiki-title lookup depends on the whole namespace: adding
one duplicate can turn a formerly unique link ambiguous. Record namespace
bucket fingerprints (for example normalized basename/title) so that only
sources consulting the affected bucket are invalidated. Likewise, a site-wide
base URL/configuration change may legitimately invalidate the whole profile.

Dirty worktrees and branches naturally follow Weave's existing snapshot model.
The database remains derived local state; content facts do not belong beside
source under version control. Cross-repository public URLs should resolve only
through explicitly known local repository identities/federation mappings, not
network discovery.

## Safe local-only indexing

Treat documentation repositories as untrusted input. Markdown itself is inert,
but documentation build systems routinely expose code execution and network
paths.

The default provider must:

- enumerate Git-tracked files and explicit dirty overlays, not recursively
  follow arbitrary filesystem entries;
- reject or report symlinks and resolved paths outside the repository/content
  root;
- validate UTF-8 and normalize line endings only for parsing, never identity;
- cap file size, AST nodes/depth, headings, links, candidates, attributes,
  front-matter nodes, raw HTML token buffers, fence bytes, diagnostics, and
  total unit output;
- use cancellation/deadlines and deterministic truncation order;
- perform no DNS, HTTP, Git remote, submodule, theme, module, package-manager,
  or external-link validation;
- never invoke Jekyll, Hugo, mdBook, Bundler, Ruby, Node, Python, a shell,
  preprocessors, plugins, filters, syntax highlighters, diagram tools, or code
  fences;
- never evaluate Liquid, Go templates, shortcodes, HTML, JavaScript, CSS, SVG,
  YAML tags, or front-matter expressions;
- keep stdout/protocol output separate from diagnostics if it later becomes an
  external provider; and
- fail a malformed unit atomically rather than publishing a plausible partial
  graph without an explicit partial/truncated state.

Parsing raw HTML is not sanitizing it. Goldmark's renderer blocks unsafe raw
HTML by default, and GFM's tagfilter affects rendering; neither property makes
source HTML safe to execute. `x/net/html` should be used only as a tokenizer
over bounded source spans.

## Prior-art lessons

### CommonMark and GFM

CommonMark supplies a rigorous block/inline grammar and examples. It also
illustrates why an AST matters: link-reference definitions can appear before
or after uses and inside block containers while applying to the whole document.
GFM adds four syntax extensions and a tagfilter, but not GitHub's full web
rendering, generated heading anchors, or repository navigation rules.

The official CommonMark and GFM example suites should be imported as upstream
conformance tests subject to their licenses, with Weave-specific assertions
over facts and ranges layered on top.

### GitHub Markup, README files, Pages, and wikis

GitHub's pipeline separates markup conversion from sanitization, highlighting,
anchor generation, and autolinking. Its README documentation defines source
relative and repository-root-relative navigation. GitHub Pages is a separate
Jekyll site pipeline, and GitHub wikis are separate Git repositories whose file
extensions choose markup converters. Weave should therefore never infer
`github-pages-jekyll` merely because a repository is hosted on GitHub.

### Marksman and Markdown knowledge tools

Marksman proves that cross-file Markdown navigation, reference links,
wiki-links, backlinks, and ambiguity diagnostics are valuable without a site
build. Foam's “minimum unique identifier” and ambiguity behavior and Obsidian's
configurable shortest/relative/absolute link forms show that wiki resolution is
a policy over a file index, not syntax alone. zk demonstrates a local CLI/LSP
index for Markdown links, tags, and YAML front matter. These are strong behavior
and fixture sources, but their dialect choices must stay named.

### Static site generators

Jekyll, Hugo, and mdBook all mix declarative content with executable extension
points. The declarative subset is worth indexing; attempting to reproduce the
fully rendered site would either execute untrusted code or reimplement each
generator and its plugin ecosystem. Weave should expose the difference as
evidence and diagnostics.

### SCIP, LSIF, LSP, and Tree-sitter

SCIP/LSIF optimize code navigation interchange; LSP optimizes interactive
editor queries; Tree-sitter optimizes incremental concrete syntax and editor
features. None supplies content URL/profile semantics plus Weave's atomic
freshness/evidence model. Reuse their process, range, occurrence, and
conformance ideas, but keep content facts native.

## Practical implementation sequence

1. Write an ADR for page/section symbols, content edge kinds, unresolved
   references/attributes, provenance, bounds, and surface dependencies.
2. Add a built-in `content-markdown` provider using Goldmark in `commonmark`
   and `gfm` modes. Emit only pages, sections, link definitions, literal links,
   fences, containment, ranges, and diagnostics.
3. Add the repository link-surface index and deterministic local path/fragment
   resolution. Prove narrow re-resolution with surface and namespace-bucket
   fingerprints.
4. Add inert raw HTML extraction and bounded YAML/TOML front matter, then the
   `github-repository` profile.
5. Add Jekyll/GitHub Pages, Hugo, and mdBook profiles one at a time, each with
   golden fixtures and explicit unsupported dynamic constructs.
6. Add opt-in wiki-link profiles and federate GitHub wiki repositories. Do not
   choose a global wiki dialect.
7. Extend the adapter protocol with virtual-source/origin-map/snippet
   capabilities before asking language adapters to index fences.
8. Join content to code through explicit links, declared bridges, and later
   language-aware documentation references; do not match prose and code merely
   by equal spelling.

This ordering produces useful page/section/link queries early and leaves every
step independently testable.

## Test and validation plan

### Parser and range fixtures

- Run the upstream CommonMark examples and relevant GFM examples through the
  selected pinned parser.
- Cover ATX/Setext headings, inline markup in headings, non-ASCII and emoji,
  duplicate headings, punctuation, explicit HTML IDs, and empty headings.
- Cover inline/full/collapsed/shortcut references, definitions after use,
  definitions inside containers, nested/escaped brackets, angle destinations,
  titles, autolinks, images, and invalid syntax.
- Cover fenced/indented code, long info strings, nested block quotes/lists,
  misleading fence languages, and Markdown-looking content inside fences.
- Assert UTF-8 byte ranges against source, including CRLF and multibyte text.

### Resolution fixtures

- Relative, root-relative, current-page, extensionless, `README`/`index`, query,
  fragment-only, percent-encoded, and external-scheme destinations.
- Missing pages, missing fragments, duplicate aliases/titles/basenames,
  case-only and Unicode-normalization collisions, and platform path separators.
- `..`, encoded traversal, absolute filesystem paths, symlinks, Git submodules,
  and paths that exist locally but are untracked.
- Profile-specific GitHub repository, Jekyll permalink/post, Hugo bundle/alias,
  mdBook SUMMARY, and wiki-link cases.

### Freshness and determinism

- A body-only target edit does not re-resolve inbound links.
- Adding/removing/renaming a heading invalidates sources consulting its page
  surface; adding a duplicate wiki name invalidates its lookup bucket.
- Deleting a page removes its unit and converts inbound edges to unresolved
  references without disturbing unrelated pages.
- Configuration/profile/provider changes invalidate affected units while stable
  IDs remain unchanged when authored addresses do.
- Full rebuild and incremental rebuild export byte-for-byte identical sorted
  logical snapshots on Linux, macOS, and Windows.

### Safety and robustness

- Fuzz AST extraction, link normalization, front-matter framing/decoding, HTML
  tokenization, and origin-range calculations.
- Exercise every configured byte/node/depth/count limit and verify deterministic
  diagnostics/truncation.
- Put executable canaries in Jekyll plugins, Hugo shortcodes/templates, mdBook
  preprocessors, Liquid includes, Mermaid fences, YAML tags, and shell examples;
  indexing must never trigger them.
- Deny network access in integration tests and verify external links remain
  recorded without egress.
- Crash/cancel between fact batches and prove the prior unit remains intact.

### Oracles

- Compare Markdown HTML structure selectively with pinned `cmark-gfm`, but test
  Weave facts rather than rendered byte equality.
- Compare GitHub heading slugs with a pinned corpus based on GitHub's documented
  rules and [`github-slugger`][github-slugger] as a non-authoritative oracle.
- Compare navigation/ambiguity fixtures with Marksman and selected
  Markdown-knowledge tools. Differences become named profile cases, not parser
  behavior chosen opportunistically.

## Principal risks

1. **False universality.** The biggest risk is naming profile-specific route or
   slug behavior “Markdown.” Require a profile on every resolved content fact.
2. **Schema dilution.** Mapping all content relations onto `references` or
   `depends-on` would make impact and architecture queries unreliable. Add the
   minimum honest vocabulary and provenance.
3. **Hidden execution.** A “more accurate” site build can execute repository or
   dependency code. Keep it outside default indexing and behind explicit future
   permission/capability boundaries.
4. **Global invalidation.** Wiki aliases and generator routes can create
   repository-wide lookup dependencies. Surface and namespace-bucket
   fingerprints keep common edits narrow while remaining correct.
5. **Identity churn.** Titles and URLs are attractive IDs but mutable; ordinals
   are fragile. Base pages on source paths and sections on actual addressable
   fragments, with documented weaker block identity.
6. **Overclaiming snippets.** A fence language is metadata, and snippets are
   often intentionally incomplete. Require virtual-document protocol support
   and preserve evidence.
7. **Unbounded adversarial input.** YAML aliases, enormous HTML tokens, huge
   link sets, deep nesting, and ambiguity explosions need limits at every
   boundary.
8. **Cross-platform drift.** Filesystem case behavior, separators, Unicode, and
   symlinks can change results. Resolve over the normalized tracked inventory,
   not host fallback lookups.
9. **Rendered-site mismatch.** Themes, templates, plugins, client routes, and
   hosted-platform post-processing can change output. Report source-derived
   routes as declared/inferred and never imply a successful build.

## Primary sources

- [CommonMark specification][commonmark]
- [GitHub Flavored Markdown specification][gfm]
- [`goldmark` parser and extension documentation][goldmark]
- [GitHub's `cmark-gfm` implementation][cmark-gfm]
- [`goldmark-frontmatter` documentation and source][goldmark-frontmatter]
- [`golang.org/x/net/html` tokenizer documentation][x-net-html]
- [`tree-sitter-markdown` limitations and extensions][tree-sitter-markdown]
- [GitHub Markup pipeline][github-markup]
- [GitHub writing/section-link rules][github-writing]
- [GitHub README relative-link behavior][github-readmes]
- [Jekyll front matter][jekyll-frontmatter], [Markdown configuration][jekyll-markdown], and [plugin model][jekyll-plugins]
- [GitHub Pages and Jekyll behavior][github-pages-jekyll]
- [Hugo front matter][hugo-frontmatter], [page bundles][hugo-bundles], [URLs][hugo-urls], and [link resolution/render hooks][hugo-relref]
- [mdBook SUMMARY format][mdbook-summary], [Markdown mapping][mdbook-markdown], and [preprocessor protocol][mdbook-preprocessors]
- [GitHub wiki repository/page behavior][github-wikis]
- [Marksman][marksman], [markdown-oxide][markdown-oxide], [zk][zk], [Foam][foam], and [Obsidian internal-link rules][obsidian-links]
- [SCIP schema][scip], [Language Server Protocol][lsp], and [LSIF overview][lsif]

[commonmark]: https://spec.commonmark.org/0.31.2/
[gfm]: https://github.github.com/gfm/
[goldmark]: https://github.com/yuin/goldmark
[cmark-gfm]: https://github.com/github/cmark-gfm
[goldmark-frontmatter]: https://github.com/abhinav/goldmark-frontmatter
[x-net-html]: https://pkg.go.dev/golang.org/x/net/html
[tree-sitter-markdown]: https://github.com/tree-sitter-grammars/tree-sitter-markdown
[github-markup]: https://github.com/github/markup
[github-writing]: https://docs.github.com/en/get-started/writing-on-github/getting-started-with-writing-and-formatting-on-github/basic-writing-and-formatting-syntax#section-links
[github-readmes]: https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-readmes
[github-slugger]: https://github.com/Flet/github-slugger
[jekyll-frontmatter]: https://jekyllrb.com/docs/front-matter/
[jekyll-markdown]: https://jekyllrb.com/docs/configuration/markdown/
[jekyll-plugins]: https://jekyllrb.com/docs/plugins/
[github-pages-jekyll]: https://docs.github.com/en/pages/setting-up-a-github-pages-site-with-jekyll/about-github-pages-and-jekyll
[hugo-frontmatter]: https://gohugo.io/content-management/front-matter/
[hugo-bundles]: https://gohugo.io/content-management/page-bundles/
[hugo-urls]: https://gohugo.io/content-management/urls/
[hugo-relref]: https://gohugo.io/shortcodes/relref/
[mdbook-summary]: https://rust-lang.github.io/mdBook/format/summary.html
[mdbook-markdown]: https://rust-lang.github.io/mdBook/format/markdown.html
[mdbook-preprocessors]: https://rust-lang.github.io/mdBook/for_developers/preprocessors.html
[github-wikis]: https://docs.github.com/en/communities/documenting-your-project-with-wikis/adding-or-editing-wiki-pages
[marksman]: https://github.com/artempyanykh/marksman
[markdown-oxide]: https://github.com/Feel-ix-343/markdown-oxide
[zk]: https://github.com/zk-org/zk
[foam]: https://github.com/foambubble/foam
[obsidian-links]: https://obsidian.md/help/links
[scip]: https://github.com/scip-code/scip/blob/main/scip.proto
[lsp]: https://microsoft.github.io/language-server-protocol/
[lsif]: https://microsoft.github.io/language-server-protocol/overviews/lsif/overview/
