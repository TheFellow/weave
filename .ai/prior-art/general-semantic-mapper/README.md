# General semantic mapper prior art

Research date: 2026-08-07

## Recommendation

Build a compiled Go adapter named `weave-ctags` around a pinned Universal
Ctags executable as Weave's first broad fallback mapper. This is a pragmatic
first slice, not a substitute for compiler-backed providers.

The adapter should speak the same bounded Weave extension protocol as Roslyn,
SCIP, and future native providers. It should emit provider-owned declarations,
document structure, and only those reference roles that the selected Ctags
parser actually reports. Every material Ctags-derived fact is `Syntactic`,
`Inferred`, or `Ambiguous`; it is never `Exact`. Precise providers remain free
to add richer facts without the fallback deleting, relabeling, or impersonating
their facts.

Use a layered support model:

1. Weave's Go core inventories every Git-visible file and directory, even when
   no parser recognizes it.
2. `weave-ctags` supplies a cheap, broad symbol outline for otherwise unclaimed
   programming languages, build files, configuration, and prose formats.
3. Curated Tree-sitter grammar plus `tags.scm` packages can improve a language
   where Ctags is weak but a compiler integration is not justified.
4. Compiler/type-checker-backed SCIP or native adapters remain the exact tier.
5. Small format-native Go mappers handle formats where keys, aliases, links, or
   evaluation-free traversals matter more than a tag outline.

The key architectural point is that these are additive evidence providers, not
competing attempts to own one canonical parser. The core owns persistence,
freshness, endpoint resolution, evidence policy, and atomic replacement.

## Why Universal Ctags is the right first fallback

[Universal Ctags][uctags] is a maintained, GPL-2.0 implementation whose stated
purpose is indexing named language objects across many languages. It already
solves the tedious long tail that Weave should not reproduce: file-to-language
selection, language-specific declaration kinds, scopes, signatures, inheritance
text, embedded guest parsers, and extensible optlib parsers.

Its [JSON output][ctags-json] is JSON Lines and exposes long kind names,
language, line, optional end line, scope/scope kind, signature, type reference,
extras, and roles. Its [machine-readable capability commands][ctags-man] expose
the parsers, mappings, kinds, roles, fields, extras, features, and output
formats compiled into the selected executable. This lets an adapter report its
real capabilities instead of freezing an optimistic language list in Go.

An empirical check of the official 2026-08-04 macOS x86-64 nightly advertised
169 parser names. That count includes disabled parsers and internal/subordinate
parsers, so it must not be marketed as 169 equally complete languages. The
same build reported JSON, interactive mode, iconv, XML/XPath, YAML, regex,
wildcard, PEG, and optscript features. Representative advertised parser names
include:

- Ada, Assembly, Awk, Clojure, COBOL, D, Elixir, Elm, Erlang, Fortran,
  Haskell, Julia, Lua, OCaml, Pascal, Perl, PHP, PowerShell, Prolog, R, Ruby,
  Scheme, Shell, SQL, V, Verilog/VHDL, and Vim;
- CMake, Make, Meson, Autoconf/Automake, Maven, Go modules, pkg-config,
  RPM specs, systemd units, Terraform/HCL, Kconfig, linker scripts, and
  Protocol Buffers; and
- AsciiDoc, HTML, JSON, Markdown, OpenAPI, Org, reStructuredText, RMarkdown,
  XML/XSLT/SVG, and YAML.

Cargo and TOML parser names were also present but disabled in the inspected
build. This is why the adapter must discover actual enabled capabilities rather
than translate a parser-name count into a support promise.

This is exactly the desired broad-but-imperfect floor. A single maintained
upstream supplies more useful definitions than a collection of regular
expressions written and maintained in Weave.

There is production precedent. Sourcegraph's Apache-2.0
[`go-ctags` wrapper][go-ctags] drives Universal Ctags from Go, passes source
content through its interactive protocol, and normalizes JSON entries.
Sourcegraph's [Zoekt Ctags driver][zoekt-ctags] reuses that wrapper, keeps a
parser process alive, and places a timeout around each document because some
inputs can hang Ctags. These are useful implementation and test references.
They should not be imported blindly: `go-ctags` explicitly says it is for
Sourcegraph use, publishes no releases, carries Sourcegraph-specific parser
options, and allowlists languages because some parsers have caused problems.

## What Ctags does not provide

Universal Ctags is declaration-oriented search navigation, not name binding or
type checking. Its own documentation says definition tags are primary and
reference tags are secondary. The [roles documentation][ctags-man] also notes
that reference roles are not implemented widely.

Important limitations for Weave are:

- kinds and fields are language-specific and cannot be treated as one universal
  semantic ontology;
- the standard fields include a one-based line and sometimes an end line, but
  no general symbol column or byte offset;
- a scope is normally a parser-produced string, not a globally resolved symbol
  identity;
- imports, inheritance, calls, and references are absent or incomplete for many
  parsers;
- extension/name mapping and eager guessing can select the wrong parser;
- Ctags does not load dependencies, compiler flags, generated sources, build
  graphs, macro environments, or type information; and
- tolerant parsing can return a useful partial outline while missing valid
  constructs, especially as a language evolves.

Sourcegraph describes this distinction directly: search-based navigation uses
tools such as Ctags and Tree-sitter and can have false positives or negatives,
while precise navigation requires compiler-oriented indexes such as
[SCIP][scip-announcement]. Weave should preserve that distinction in every fact.

## Alternatives and complementary tools

| Tool | Strength | Why it is not the universal first slice |
| --- | --- | --- |
| [Universal Ctags][uctags] | Mature broad declaration extraction, native executables, dynamic parser/kind discovery, JSON Lines, scopes and selected roles. | **Adopt first**, but constrain evidence and isolate the native process. |
| [Tree-sitter][tree-sitter] | Fast error-tolerant concrete syntax trees with exact byte/point ranges and an embeddable C11 runtime. | The runtime includes no grammars or semantic mapping. Every grammar, external scanner, license, ABI, and query set must be pinned and tested. |
| Tree-sitter `tags.scm` | The official [tag-query convention][tree-sitter-tags] standardizes `@definition.*`, `@reference.*`, `@name`, and optional docs. GitHub demonstrates useful search-based navigation with this model. | Many grammars have no tags query, and grammar/query quality varies. A CST alone is not a reasonable generic graph mapper. |
| [`tree-sitter-language-pack`][tree-sitter-pack] | A promising MIT packaging layer claiming 300+ precompiled grammars and bundled query types with Go and CLI access. | It is a newer, large supply-chain surface; parsers download on first use unless prefetched, tag-query coverage is incomplete, Go uses native bindings, and every grammar/query still needs provenance. Evaluate as a later curated provider, not a transparent runtime download. |
| [SCIP][scip] | Typed protobuf schema, stable symbols, occurrences, relationships, generated bindings, and established compiler-backed indexers. | SCIP is the interchange format, not a parser. Broad support still requires one producer per language/toolchain. It remains Weave's preferred precise input. |
| LSIF | Existing code-intelligence graph ecosystem. | Sourcegraph removed native LSIF storage and recommends SCIP for new indexers. Its opaque graph IDs and incremental-writing complexity add no value to a new Weave adapter. |
| LSP | Reuses many installed language servers and supports symbols, definitions, references, and diagnostics. | Interactive queries do not promise one deterministic, complete repository inventory. Servers require per-language startup/configuration and often execute build or restore behavior. Use behind a deliberate provider, never as an implicit fallback. |
| Language regexes in Go | Small initial prototype. | Reimplements the weakest form of Ctags, expands maintenance with every language version, and tempts the core to overstate regex matches. Avoid. |

Tree-sitter remains the best second tier. Its [official code-navigation
convention][tree-sitter-tags] gives Weave a sensible normalized vocabulary when
a grammar actually ships a maintained, tested `queries/tags.scm`. The official
[Go binding][go-tree-sitter] can link selected grammar modules or load native
grammar libraries at runtime. Neither route is a universal pure-Go binary:
linked grammars use C/cgo, while runtime libraries introduce platform artifacts
and ABI management. A curated Tree-sitter adapter should therefore be another
compiled provider package with an explicit grammar/query manifest, not code in
the Weave core.

## Provider and enrichment contract

The extension boundary should follow `protoc`'s durable lesson: an executable
and a language-neutral wire contract are normative; an SDK is only a
convenience. A provider built in Go, C#, Rust, Python, or another runtime must be
able to add the same bounded fact types.

This does **not** require a protobuf rewrite of Weave's adapter protocol.
`protoc` is the process-model inspiration: discover or select an executable,
negotiate capabilities, send one bounded request on stdin, receive protocol-only
output on stdout, and let the host own the result. Weave's existing one-shot
NDJSON contract already carries complete provider units and edges whose endpoint
IDs may remain external. It can host this mapper after tightening provider
ownership and routing; SCIP remains one possible producer payload behind an
adapter, not the mandatory extension transport.

The contract needs these properties for additive enrichment:

- **Provider ownership.** A run declares one provider identity and version.
  Every unit and emitted fact belongs to it; a process cannot spoof ownership
  by putting another provider name on a fact. The current v0 runner negotiates
  and validates the unit provider, but the host should also reject a document,
  symbol, occurrence, or edge whose provider (and applicable provider version)
  differs from that unit.
- **One-shot baseline.** One bounded request on stdin and one complete framed
  response on stdout remain mandatory. Stderr is bounded diagnostics. A helper
  such as Ctags may be persistent only inside that one adapter run.
- **Independent endpoints.** An edge may point to a provider-local symbol, an
  existing entity ID known to the adapter, or an external/open ID that a later
  resolver can satisfy. Edge ownership must not imply endpoint ownership. The
  current graph model already permits external edge endpoint IDs.
- **Optional read-only anchors.** A later request extension may pass a bounded
  set of existing graph anchors relevant to selected files. They are lookup
  context, never facts the adapter may replace. ID, provider, kind, stable name,
  document path, and source range are sufficient for conservative matching;
  they are not required for the initial Ctags mapper.
- **Complete provider inventory.** Each document is an atomic unit. A successful
  complete run may remove only absent units previously owned by the same
  provider. It cannot remove facts from any other provider.
- **Honest failures.** Under the current all-or-nothing v0 lifecycle, a parser
  hang or malformed response fails the run and preserves the prior database.
  A future backward-compatible lifecycle may add per-unit failed/skipped
  outcomes, but only if it also says whether the returned provider inventory is
  complete so partial output can never imply deletion.
- **Evidence on every claim.** Evidence is supplied at fact granularity and
  validated by the host against a built-in or registered provider evidence
  ceiling.
- **Deterministic inputs.** The request contains the host-selected repository
  identity, relative input inventory/digests, changed paths, prior unit
  fingerprints, resource limits, and granted permissions. Providers do not
  discover arbitrary new files behind the host's back.
- **Atomic publication.** The host validates the whole response, resolves or
  preserves endpoints, and commits. Providers never write the database.

The host should union observations rather than destructively canonicalize them.
If a Ctags symbol and a compiler symbol appear to describe the same declaration,
retain both provider-owned observations. A deterministic reconciliation pass
may emit a `resolves-to`/equivalence edge when path, name, kind family, and
source anchor make the match unique. An uncertain match is `Ambiguous` or is
left unresolved; it is never silently merged.

Default routing should minimize noisy duplicates: a precise provider can claim
specific inputs, and the broad fallback handles the remainder. Users or future
composition policies may still enable a lower-tier overlay when it contributes
facts the precise provider omits. Provider priority affects presentation and
file routing, not fact ownership or truth.

### Provider implementation families

The executable contract deliberately permits several internal strategies:

- a mostly-Go native provider for Go syntax/build semantics and inert content or
  structured-data formats;
- a thin compiled Go wrapper that invokes a maintained SCIP producer and imports
  its compiler-grade index;
- a runtime-native provider such as Roslyn where the language compiler API is
  the valuable implementation primitive;
- an LSP-backed provider only when that server can produce a bounded,
  deterministic workspace snapshot under explicit build/network permissions;
  and
- the Universal Ctags fallback for selected files without a better provider.

The host and CLI consume one normalized evidence graph regardless of which
strategy produced a fact. An adapter SDK should make framing, limits,
fingerprints, diagnostics, and stable-ID construction easy, but providers must
remain implementable from the wire/process contract alone.

## Evidence policy for a Ctags mapper

The adapter registration and host policy should set `Syntactic` as its maximum
evidence. A practical mapping is:

| Ctags observation | Weave representation | Evidence |
| --- | --- | --- |
| Parser-selected declaration tag with name, kind, and source line | Provider-owned symbol and definition occurrence | `Syntactic` |
| Scope text retained as metadata | Authored/parser-observed scope name | `Syntactic` |
| Scope text uniquely matched to one containing declaration in the same document | `contains` edge | `Inferred` unless the parser exposes a structural parent identity |
| Import/include/package tag with an imported/reference role | Open endpoint and `imports` or `depends-on` edge | Source statement `Declared`; endpoint resolution `Inferred` or `Ambiguous` |
| Call/reference role emitted by the parser | Open reference edge, or an occurrence only when it names a provider-owned symbol | `Syntactic`; resolving the name to a target is `Inferred`/`Ambiguous` |
| Inheritance/type-reference string | Open `extends`, `implements`, or type endpoint only for parser kinds with documented semantics | `Declared` for the text, `Inferred`/`Ambiguous` for the selected target |
| Qualified-name extra generated by Ctags | Alias/alternate lookup key, not a second declaration | `Generated` |
| Extension/name-based language selection | Document language hint | `Inferred` |
| Exact Git path, bytes, and digest supplied by the host | Document inventory | `Exact`, but this is a host fact rather than a Ctags semantic fact |

Never infer a call merely because a name appears on a line. Never turn every
reference into a dependency edge. Never label a Ctags definition `Exact` just
because the parser returned it without warning.

### Positions and stable identities

Request `line` and `end`, but do not pretend they are identifier ranges. A safe
initial definition anchor is the reported source line or construct line span,
converted to Weave's zero-based, half-open UTF-8 convention after reading the
same source bytes. If the tag name occurs exactly once on that line and a
language-specific fixture proves the rule, the adapter may emit a narrower
`Inferred` name range. It must not execute or trust the emitted Ex search
pattern. Disabling the `pattern` field also reduces output and accidental source
disclosure.

Use a document-scoped provider identity based on:

```text
repository identity
+ normalized repository-relative path
+ selected Ctags language
+ scope path and scope-kind sequence when available
+ tag kind and name
+ normalized signature/type discriminator when available
+ deterministic source-order collision suffix only when still required
```

Parser version, provider version, Git commit, and absolute checkout path belong
in provenance/fingerprints, not the stable symbol name. Line number alone must
not be a stable identity because inserting lines above a declaration would
churn every downstream edge. Repeated identical declarations may still require
a source-order suffix; that limitation should remain visible as syntactic
evidence rather than be hidden by false canonicalization.

Ctags kind names should be preserved in a namespaced raw form. A small tested
table can map uncontroversial kinds such as class, interface, function, method,
module, package, field, variable, constant, enum, and heading to Weave display
kinds. Unknown or language-specific kinds remain `ctags/<language>/<kind>`.
Do not map solely by a one-letter kind, whose meaning is parser-specific.

## Safe invocation design

The Go wrapper should own all process, filesystem, normalization, and protocol
logic. Universal Ctags remains an isolated helper executable.

At capability discovery:

1. Resolve an explicit environment/flag path, then a sibling packaged
   `uctags[.exe]`, then a PATH candidate. Never assume `/usr/bin/ctags` is
   Universal Ctags; macOS commonly provides another implementation.
2. Validate `--version` identifies Universal Ctags and record the exact version.
3. Require JSON support through `--list-features` and JSON in
   `--list-output-formats`.
4. Capture the machine-readable language, map, kind, role, field, extra, and
   feature inventories. Their deterministic digest participates in the provider
   fingerprint.
5. Report only capabilities present in that binary. Optional XML/YAML/iconv
   features can change parser behavior and therefore freshness.

At indexing:

- put `--options=NONE` first. The Ctags manual documents this as the way to
  disable automatic user and repository option loading;
- never load `.ctags.d`, `ctags.d`, arbitrary optlib, or an environment-provided
  parser definition during automatic indexing;
- index only host-selected, repository-contained, regular Git-visible files;
- do not recurse, follow symlinks, evaluate build tools, fetch dependencies, or
  run generators;
- request unsorted JSON, long kind names, language, line/end, scope/scope kind,
  roles, extras, signature, inheritance, and type-reference fields as supported;
- enable reference extras only as lower-confidence observations; do not enable
  qualified extras as duplicate definitions;
- enforce per-file bytes, tags, time, stderr, and JSON-frame limits in addition
  to the host's total run limits; and
- treat malformed JSON, an impossible path/line, process crash, timeout, or
  count overflow as an explicit unit failure, not as successful empty output.

Two helper modes are viable:

1. **Interactive inline input** sends one filename and bounded source body at a
   time to one Ctags child. The [official interactive protocol][ctags-interactive]
   supports this, Sourcegraph uses it, and it keeps Ctags from independently
   choosing filesystem bytes. It is officially experimental, so Weave must pin
   the producer, test its handshake/output, and restart it after a per-file
   timeout or crash.
2. **Ordinary one-shot JSON** invokes Ctags on validated absolute paths. This
   uses a more established interface but creates command-line/list-file edge
   cases and allows a file to change between host hashing and parsing. It is a
   useful compatibility fallback when interactive support is absent.

Prefer interactive inline input for the packaged pinned build. Keep ordinary
mode as an explicitly tested fallback. Do not use `-L -` with raw repository
paths: the Ctags manual says options are accepted in list input, and newline is
not representable in its line-oriented list format.

Linux builds may advertise Ctags' seccomp-backed interactive sandbox. Use it
when available, but do not make it the cross-platform security model because it
is Linux-only. Process isolation, disabled option loading, inline source,
resource limits, and denied network/build permissions remain mandatory on every
platform.

## Cross-platform binary distribution

The distributable provider should be a platform archive, not an instruction to
install whatever `ctags` happens to be on PATH:

```text
weave-ctags[.exe]   # Go protocol adapter
uctags[.exe]        # pinned Universal Ctags helper
adapter-manifest    # versions, features, parser inventory digest, checksums
LICENSES/           # Weave wrapper and all helper/dependency notices
SOURCE/NOTICE       # GPL-compliance source/provenance instructions
```

The Go wrapper can be cross-compiled normally. Universal Ctags and its optional
JSON/XML/YAML dependencies need native release jobs for each supported OS and
architecture. Build a pinned upstream release or commit reproducibly, retain
the build configuration, produce checksums/SBOMs, and run the same contract
corpus on every artifact. Ordinary indexing must be completely offline.

Universal Ctags is GPL-2.0. Invoking it as a separate process avoids linking its
C implementation into the Go adapter, but distributing its binary still carries
GPL notice/source obligations. Preserve that clean process boundary and satisfy
the binary's license obligations explicitly. This is an engineering constraint,
not a legal conclusion.

Upstream currently publishes stable source releases, official nightly native
archives for Unix-like platforms, and a separate official Windows build project.
The [nightly build repository][ctags-nightly] states that Linux/BSD artifacts
are statically linked and macOS artifacts link only the system library
dynamically. Weave may use those builds as provenance and CI oracles, but a
release should pin exact hashes rather than follow a moving nightly.

## Consumption by the CLI and LLMs

The resulting graph supports the useful part of CodeGraph-style navigation
without pretending all facts have compiler precision. CLI queries should:

- search symbols and document outlines across every provider, returning
  provider and evidence with each result;
- rank `Exact` compiler facts before `Declared`, `Syntactic`, `Inferred`, and
  `Ambiguous` observations while still allowing explicit evidence/provider
  filters;
- answer definition/reference/dependency queries from precise facts when
  present and fall back to Ctags definitions or unresolved name candidates when
  they are not;
- expose per-file structure and compact neighborhoods so an LLM can request the
  few relevant source ranges instead of reading whole repositories;
- preserve open endpoints and diagnostics so “unknown target” is different from
  “no relationship”; and
- allow contextual-link commands to connect documentation, generated artifacts,
  schemas, configuration, and code across repositories without teaching the
  generic mapper application-specific semantics.

For example, a precise SCIP provider may define a method and all bound
references, the content provider may define a documentation section, and a
manually authored contextual edge may connect them. A Ctags provider can still
outline an adjacent Meson, SQL, Vim, or legacy-language file. These facts are
useful together because the query layer keeps their provenance and confidence,
not because they were forced through one AST or one symbol namespace.

The compact graph is an index into source, not a lossy replacement for it. A
CLI response should include stable IDs, path/range, kind, provider, evidence,
and bounded neighbors; the LLM can then open source only when text is required
to answer or make a change.

## Structured data and non-code formats

Universal Ctags should provide a useful outline where it already has a parser,
but not every structured file should be forced through a tag abstraction.

- Keep the existing Goldmark-based Markdown/content provider authoritative for
  pages, headings, links, routes, code fences, and front matter. Ctags Markdown
  headings are a fallback outline, not equivalent site semantics.
- JSON and XML can be mapped conservatively in pure Go with the standard
  token-stream decoders. XML exposes token offsets/positions; modern Go's JSON
  token layer can retain syntax and offsets. JSON Pointer/XPath-like paths are
  more useful stable identities than arbitrary Ctags tags when full structural
  navigation is desired.
- YAML needs a maintained node parser that retains mappings, sequences, aliases,
  anchors, tags, line, and column. The current [`yaml/go-yaml`][go-yaml]
  organization provides the continuation of the established Go implementation.
  Alias expansion, duplicate keys, merge keys, and resource limits need explicit
  policy.
- HCL/Terraform merits its native [`hashicorp/hcl/v2`][hcl] parser because it
  exposes blocks, attributes, ranges, and unevaluated traversals without running
  Terraform. Ctags remains a broad fallback for adjacent formats.
- TOML, INI, CSV, lockfiles, manifests, and workflow files should gain native
  mappers only when their relationships have concrete graph value. A generic
  key/value explosion can make an LLM's queries worse rather than better.

The fallback for an unsupported or intentionally unparsed file is still useful:
an exact file node, content hash, repository containment, language/media hint,
and links declared elsewhere. “No symbols” must never mean “the file does not
exist.”

## Incrementality

Use one Ctags unit per document. The input fingerprint includes source bytes,
repository-relative path, selected language/map, Ctags version, compiled feature
inventory, parser/kind/role inventory digest, adapter mapping version, and any
explicit trusted parser configuration.

A changed document reparses independently. A changed Ctags binary, feature set,
language mapping, or adapter normalization version invalidates affected units.
Cross-document name reconciliation runs only for changed symbol surfaces and
recorded dependents. The adapter does not need an agent and does not need to
scan every repository on every query.

Do not initially implement parser-specific dependency cones. Ctags extraction
is fast and document-local; deterministic per-file refresh is a better first
correctness boundary. Batch or persistent workers can be optimized after real
repository benchmarks.

## Initial implementation sequence

1. Define conformance fixtures for a diverse small corpus: declarations,
   duplicate names, scopes, imports, inheritance text, Unicode, CRLF, malformed
   syntax, mixed/embedded languages, huge lines, empty files, ignored files,
   symlinks, spaces, and adversarial filenames.
2. Implement `weave-ctags describe` and zero-fact protocol lifecycle tests in a
   compiled Go command before integrating the helper.
3. Add strict producer discovery and capability fingerprinting against a fake
   Ctags executable.
4. Add bounded JSON normalization and one document unit, initially definitions
   only and always `Syntactic`.
5. Run the real pinned producer corpus on Linux, macOS, and Windows. Snapshot
   facts by producer version without asserting that every parser has equal
   coverage.
6. Add scope containment, imports, inheritance/type text, and references one
   documented parser family at a time with evidence tests.
7. Package native provider archives with checksums, SBOM/license material, and
   an offline installation/doctor path.
8. Benchmark real repositories before adding a persistent adapter mode,
   concurrency, or a Tree-sitter language pack.

Success is not “every file emitted a symbol.” Success is that nearly every
repository obtains a useful conservative outline immediately, exact providers
can enrich it through the same contract, and queries can always tell how each
claim was established.

## Primary sources

- [Universal Ctags repository and build/distribution guidance][uctags]
- [Universal Ctags 6.2.1 release][ctags-release]
- [Universal Ctags JSON output contract][ctags-json]
- [Universal Ctags command, field, kind, role, mapping, and option-file contract][ctags-man]
- [Universal Ctags interactive/inline-input protocol and Linux sandbox][ctags-interactive]
- [Universal Ctags optlib extension model][ctags-optlib]
- [Universal Ctags nightly binary construction and dependency policy][ctags-nightly]
- [Sourcegraph `go-ctags` process wrapper][go-ctags]
- [Sourcegraph Zoekt Ctags process timeout/restart precedent][zoekt-ctags]
- [Tree-sitter goals and runtime/binding model][tree-sitter]
- [Tree-sitter code-navigation/tag query convention][tree-sitter-tags]
- [Official Tree-sitter Go bindings][go-tree-sitter]
- [Tree-sitter language pack packaging experiment][tree-sitter-pack]
- [SCIP schema and generated binding repository][scip]
- [Sourcegraph SCIP indexer guidance][scip-indexers]
- [Sourcegraph's Ctags/Tree-sitter versus precise-index distinction][scip-announcement]
- [Sourcegraph LSIF-to-SCIP migration notice][lsif-migration]
- [Go XML token/offset API][go-xml]
- [`yaml/go-yaml` project listing and v4 module][go-yaml]
- [HashiCorp HCL v2 native syntax parser][hcl]

[uctags]: https://github.com/universal-ctags/ctags
[ctags-release]: https://github.com/universal-ctags/ctags/releases/tag/v6.2.1
[ctags-json]: https://docs.ctags.io/en/stable/man/ctags-json-output.5.html
[ctags-man]: https://docs.ctags.io/en/latest/man/ctags.1.html
[ctags-interactive]: https://docs.ctags.io/en/stable/interactive-mode.html
[ctags-optlib]: https://docs.ctags.io/en/latest/man/ctags-optlib.7.html
[ctags-nightly]: https://github.com/universal-ctags/ctags-nightly-build
[go-ctags]: https://github.com/sourcegraph/go-ctags
[zoekt-ctags]: https://github.com/sourcegraph/zoekt/blob/main/internal/ctags/parser.go
[tree-sitter]: https://tree-sitter.github.io/tree-sitter/
[tree-sitter-tags]: https://tree-sitter.github.io/tree-sitter/4-code-navigation.html
[go-tree-sitter]: https://github.com/tree-sitter/go-tree-sitter
[tree-sitter-pack]: https://github.com/kreuzberg-dev/tree-sitter-language-pack
[scip]: https://github.com/scip-code/scip
[scip-indexers]: https://sourcegraph.com/docs/code-navigation/writing-an-indexer
[scip-announcement]: https://sourcegraph.com/blog/announcing-scip
[lsif-migration]: https://sourcegraph.com/docs/admin/how-to/lsif-scip-migration
[go-xml]: https://go.dev/pkg/encoding/xml/
[go-yaml]: https://yaml.com/projects/go-yaml/
[hcl]: https://pkg.go.dev/github.com/hashicorp/hcl/v2/hclsyntax
