# Content corpus audit

Audit date: 2026-08-07

These local repositories are the design and adversarial-validation corpora for
the first workspace/content provider. Counts describe the audited commits, not
permanent product assumptions.

## `TheFellow.github.io`

Audited `docs/weave-walkthrough` at `8de08a2e1966`.

- 97 Git-visible Markdown files: 39 authored public pages across `_site_pages`,
  `_projects`, `_guides`, `_posts`, and `_reading_series`; 54 generated
  alternates/legacy shims; and repository documentation. `llms.txt` and
  `llms-full.txt` are Markdown-shaped generated text artifacts. Ignored `_site`
  output must not enter the index.
- All 39 authored public pages have YAML front matter. Meaningful fields include
  `title`, `permalink`, `redirect_from`, `series`, `series_order`, `topics`,
  `tags`, `language`, repository URLs, ordering, and publication status.
- Authored sources have 155 external Markdown links, 35 root-relative internal
  Markdown links, and significant raw-HTML/Liquid `href`/`src` use. All 35
  static internal root links resolved against the audited route/asset surface.
- Cross-repository links are core data: 98 authored links target
  `go-modular-monolith`, 15 target `weave`, and others target `cedar-dotnet`,
  `fkyeah`, `go-riblt`, `arch-lint`, `enumstruct`, `ValueTypes`, and `fluid`.
  All 52 audited `github.com/TheFellow/<repo>/{blob,tree}/main/...` deep targets
  existed in their local repositories.
- The source contains 22 Mermaid, 35 Go, 34 text, 19 F#, 14 bash, 7 shell, 2
  SQL, and 1 protobuf fence before generated duplication. They are examples,
  not compilation units.
- Images are normally raw HTML with a quoted Liquid `relative_url` operand.
  Sixteen TeX diagrams have matching PNGs, but a basename coincidence is not
  sufficient evidence for a `generates` edge.
- `_site_pages/resume.md` encodes substantial heading structure in raw HTML.
  Collection pages also contain dynamic Liquid loops. Static HTML is useful;
  evaluated template output is outside a source-only provider's evidence.
- Generated duplication is structural: an authored guide, canonical `articles`
  alternate, legacy `guides` shim, and `llms-full.txt` may represent the same
  content. Explicit generated-from declarations are evidence; byte similarity
  is not.

## `go-modular-monolith`

Audited `main` at `7b010542d7e0`.

- 30 tracked Markdown files, including 26 nested READMEs. Thirteen use
  `README.md` and thirteen use `readme.md`; exact Git casing must win even on a
  case-insensitive checkout.
- The corpus has 128 ATX headings, 102 repository-relative links (26 with
  fragments), and 3 external links. All audited path and Markdown-fragment
  targets resolved.
- Links connect documentation to code, policies, schemas, generated files, and
  generators. For example, `pkg/authz/README.md` links domain Cedar schemas and
  policies plus the Go entity model; the root README maps architecture,
  entrypoints, domains, surfaces, and toolkits.
- Fences include 26 Go, 22 shell, 11 text, and 2 Mermaid blocks. The CI badge is
  a nested Markdown image/link construct.
- README nesting is meaningful ownership and navigation, not simply text under
  a directory. A page and every heading require document-qualified identity.

## Nearby adversarial patterns

- The profile `README.md` uses centered raw HTML, nested badge images, emoji
  headings, project tables, website routes, and repository-root links.
- `fkyeah` has 242 Git-visible Markdown files, 204 of them README files, with
  many intentionally repeated one-heading conformance documents.
- `arch-lint` includes vendored README/changelog content with legitimate
  repeated headings, motivating an eventual include/exclude profile.
- `ValueTypes/README.md` contains the escaped generic heading
  `IEquatable\<T\>`; profile headings contain emoji.
- No audited repository uses wiki-style `[[...]]` links. Wiki syntax is an
  opt-in future profile rather than baseline GFM behavior.

## Resulting fixture obligations

The baseline tests cover exact-case paths, relative files and fragments,
duplicate/unicode/escaped headings, nested badges, HTML links/images and static
Liquid operands, front matter, generated families, GitHub cross-repository
URLs, Mermaid false positives, ignored files, large-input bounds, and symlink
containment. Later profiles must add renderer-specific anchors, route
collisions, static includes, wiki ambiguity, generated-output freshness, and
content diagnostics without weakening the safe baseline.
