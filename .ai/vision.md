# Weave: a deterministic semantic index for local knowledge

## Status

This document is the guiding vision for Weave. It defines the problem, product
principles, architecture, behavioral contracts, delivery sequence, and criteria
for completion. Implementation decisions may refine the mechanisms, but changes
that weaken these principles require an explicit update to this document with a
written rationale.

## The idea

Weave is a local-first, Git-aware semantic index for the knowledge encoded on
disk. It assembles precise facts from language-native compiler tooling,
structured content, repository topology, and existing open standards, stores
those facts in a compact deterministic database, incrementally refreshes only
what changed, and exposes a stable command-line interface for humans and coding
agents.

Weave does not contain an LLM. It gives an LLM better evidence.

The desired experience is deliberately boring:

```text
$ weave callers AuthService
index: refreshed 3 changed files in 84ms

AuthService.Handle
  called by  api.Login             internal/api/login.go:42
  called by  api.Refresh           internal/api/refresh.go:31
  implements auth.Handler.Handle  internal/auth/service.go:77
```

The same query is available as stable structured output:

```sh
weave callers AuthService --json
```

An agent can translate a user's intent into these deterministic queries without
shipping source code to another indexing service, constructing embeddings, or
rediscovering repository structure through repeated file reads.

## The problem

Coding agents are good at reasoning about relevant source and bad at repeatedly
discovering which source is relevant. Most compensate with loops of filename
search, text search, and whole-file reads. Those loops are slow, spend context on
navigation, and reconstruct relationships that compilers already know.

Existing approaches leave a useful gap:

- Tree-sitter provides broad syntactic coverage, but syntax alone cannot resolve
  all symbols, overloads, implementations, build variants, or generated edges.
- Language servers provide semantic answers, but generally do not expose a
  durable, portable, Git-aware graph with a composable CLI.
- SCIP and related standards normalize code-navigation facts, but do not provide
  the complete local lifecycle, compact storage, worktree overlays, cross-repo
  catalog, impact queries, or agent-oriented product experience.
- Hosted code-intelligence systems solve a broader organizational problem and
  introduce a service where a disposable local index would suffice.
- Uniform parser-based agent tools gain breadth by owning name resolution for
  every language. Weave should instead reuse language-native truth wherever it
  exists.

The opportunity is the glue between compiler-native analysis, open semantic
index formats, Git state, compact local storage, and a precise CLI.

## Product principles

### Deterministic before intelligent

Index construction and graph queries must not require an LLM, embeddings, or a
network connection. Given identical source, build configuration, toolchain, and
extractor versions, Weave must produce equivalent facts.

### Compiler-native precision

Use the semantic tooling maintained with a language whenever practical:

- Go through `go/packages`, `go/types`, and selected `x/tools` analysis.
- C# through Roslyn or a proven Roslyn-backed SCIP producer.
- F# through `FSharp.Compiler.Service`, not Roslyn.
- Other ecosystems through maintained compiler-backed SCIP producers, language
  services, or standardized indexes.
- Tree-sitter only as a clearly labeled syntactic fallback.

Every fact records its provider and evidence quality. A syntactic guess must
never masquerade as a compiler-resolved edge.

### A compiler-driver core with language-native plugins

Treat language support like `protoc` treats code generators: the core owns
discovery, version negotiation, process supervision, normalization, storage,
and queries; an open-ended set of subordinate executables owns language truth.
The normal extension boundary is a bounded request on stdin, protocol-only
facts on stdout, bounded diagnostics on stderr, and a terminal process status.
Adapters may be written in any language and use the compiler, build system, or
language service native to their ecosystem.

No public adapter contract may require importing a Go package, sharing Go
memory, or reimplementing a compiler in Go. The bundled Go provider is a
language-native convenience, not a precedent for absorbing other languages
into the core. New language work begins by finding or wrapping maintained
semantic tooling and implementing the process contract.

One-shot execution is the correctness baseline. Persistent workers,
multiplexing, and richer transports are optional negotiated optimizations; an
adapter that implements only the simple request/response lifecycle remains a
first-class implementation.

### Reuse prior art

Before implementing a major capability, research existing open-source work and
record the findings under `.ai/prior-art/<topic>/`. Prefer adopting a stable
format, library, algorithm, or executable adapter over recreating it. Record why
an option was adopted, wrapped, or rejected.

### Local-first and disposable

Detailed indexes are derived state. They belong under Git-managed local storage
or the user's data directory, not in commits. They can be deleted and rebuilt
from source at any time.

### Fresh when observed

A query must not silently answer from stale data. Before returning an answer,
the CLI cheaply compares the stored baseline with the current repository and
refreshes the necessary semantic units. A daemon or hook may reduce latency, but
correctness never depends on one.

### Incremental by construction

Work must scale with the difference whenever correctness allows. Reuse facts by
content hash across commits, branches, and worktrees. Propagate invalidation only
when a compilation unit's public surface or semantic context changes.

RIBLT is relevant where two inventories need compact set reconciliation, such as
comparing a large known document/fact inventory with an uncertain local cache.
It is not mandatory where Git already supplies an exact and cheaper difference.
Use `go-riblt` only when measurement shows that reconciliation reduces work or
data transfer without weakening determinism.

### The CLI is the product

Human output should be concise and legible. JSON output must be versioned,
stable, complete, and designed for automated consumers. MCP is a thin optional
adapter over the same application boundary, never the primary implementation.

Focused graph queries also emit deterministic Graphviz DOT without requiring
Graphviz. DOT output must use readable labels rather than opaque identities,
retain full identities and evidence in metadata, distinguish traversal roles,
remain explicitly bounded, and work across the same local and catalog scopes as
the underlying query. Rendering DOT to SVG, PNG, PDF, or an interactive viewer
is the responsibility of Graphviz or another consumer.

A later presentation-layer tool may turn the same bounded graph results into
reproducible animations. It should reuse Graphviz/DOT coordinates where they
improve legibility and drive 3Blue1Brown's open-source Manim engine from a
versioned, deterministic scene/timeline description. Useful scenes include a
dependency traversal unfolding, impact propagating through callers, a graph
changing between commits, and contextual links weaving repositories together.
Manim remains an optional consumer: indexing, querying, and DOT/JSON export do
not acquire a Python, LaTeX, FFmpeg, or renderer dependency.

### Cross-platform without pretending runtimes do not exist

The core is an idiomatic Go executable. Language adapters may use their native
runtimes and are isolated behind a versioned protocol. Packaging must support
macOS, Linux, and Windows, with clear capability discovery when a toolchain or
adapter is unavailable.

Host platform, target platform, toolchain, build variant, and adapter version
are explicit inputs. Adapters should use compiler cross-targeting support when
available and must not assume that indexing requires running produced target
binaries. Cross-platform support means supervising the appropriate native
toolchain consistently, not translating every language into Go.

### Evidence over confidence theater

Every relationship carries a kind, source location when available, provider,
and confidence class. Results distinguish exact compiler facts, declared build
relationships, generated relationships, inferences, and ambiguities.

### The workspace is semantic, even when it does not compile

Files, directories, documents, headings, routes, topics, links, embeds, and
generated representations are first-class graph entities. A repository may be
a website, a profile, a book, a policy corpus, or a collection of structured
notes and still have a valuable semantic index. Buildability is not the
admission test for knowledge.

Structured-content providers use maintained parsers for their formats, retain
source ranges, and label syntactic and declared evidence honestly. They do not
execute site generators, templates, includes, diagrams, notebooks, or embedded
code during safe automatic indexing. Compiler providers remain authoritative
for compilable source; content providers connect prose and topology to those
facts through stable repository/path identities.

The long-term product is an index of selected local worktrees that lets a user,
CLI, or agent navigate the semantics of files on disk faster than rediscovering
them through raw filesystem traversal. Cross-repository catalog queries are a
core part of that experience, not a hosted service.

## Explicit non-goals

The initial product is not:

- An autonomous coding agent.
- A chat client or model gateway.
- A vector database or semantic embedding pipeline.
- A hosted SaaS service.
- An IDE replacement.
- A full build system.
- A universal parser maintained by this project.
- An interactive graph visualization product or Graphviz renderer.
- A source-control system or replacement for Git.
- A guarantee that unrelated languages can be semantically connected without
  build metadata, schemas, configuration, or declared bridges.

Interactive visualization, natural-language query translation, and hosted
artifact exchange may be built later by other tools consuming Weave's stable
JSON or DOT interfaces.

## System shape

```text
                  Language-native semantic providers
        ┌────────────────┬────────────────┬────────────────┐
        │ Bundled Go     │ Roslyn / SCIP  │ F# Compiler    │
        │ analysis       │ executables    │ Service process│
        ├────────────────┼────────────────┼────────────────┤
        │ Existing SCIP indexers          │ Syntax fallback│
        └────────────────┴────────────────┴────────────────┘
                               │
       provider contract; processes use stdin · stdout · stderr
                               │
                               ▼
                    Weave normalization core
             identity · evidence · reconciliation · validation
                               │
                               ▼
                      compact bstore database
          repositories · snapshots · documents · symbols · edges
                               │
                 ┌─────────────┼─────────────┐
                 ▼             ▼             ▼
            CLI queries    MCP adapter   CI verification
```

The core owns:

- Repository discovery and identity.
- Git state and worktree awareness.
- Adapter discovery and invocation.
- Fact normalization and validation.
- Stable symbol and document identity.
- Compact persistence and schema migration.
- Incremental invalidation.
- Graph traversal and search.
- Output contracts.
- Local and cross-repository catalogs.

Adapters own language-specific truth. They should be replaceable processes, not
plugins loaded into the Go address space. This keeps runtime, dependency, crash,
and licensing boundaries explicit.

The process architecture is modeled after compiler plugin systems rather than
an in-process extension API. The core sends repository, variant, permissions,
limits, and prior-inventory context. An adapter returns only normalized facts,
fingerprints, capabilities, and diagnostics. It does not write the database or
call another adapter. The core validates the complete response and publishes it
atomically.

Every public adapter generation includes:

- a language-neutral wire specification and compatibility rules;
- recorded request, response, and malformed-stream fixtures;
- a reusable conformance suite runnable against any executable;
- capability negotiation rather than provider-name conditionals;
- literal executable plus argument discovery on macOS, Linux, and Windows;
- explicit permissions, resource bounds, cancellation, and stderr rules.

Adapters are independently implementable and distributable. Bundled companions
may share Weave's release version, while compatible third-party adapters may
ship on their own cadence. Provider identity and semantic-toolchain versions
remain part of every freshness decision.

## Open semantic formats

SCIP is the first interchange format to support. It is language-neutral,
protobuf-based, Apache-licensed, and already has compiler-backed producers for
several ecosystems. Weave should ingest SCIP rather than reproduce those
indexers.

SCIP is not necessarily Weave's complete internal model. Code-navigation facts
do not express every build target, repository relationship, dirty overlay,
confidence class, API fingerprint, or architecture rule Weave needs. The core
will normalize SCIP and native adapter output into its own versioned fact model.

The experimental adapter v0 uses newline-delimited JSON to accelerate
experimentation and publishes cross-language fixtures under
`protocol/adapter/v0/`. Before third-party adapters are promised stable
compatibility, define a framed protobuf protocol with:

- Handshake and capability negotiation.
- Schema and adapter versions.
- Repository and compilation-unit context.
- Documents, symbols, occurrences, and edges.
- Public-surface and dependency fingerprints.
- Diagnostics and partial-failure reporting.
- Cancellation and bounded resource behavior.

The protobuf schema, not generated Go types, will be the v1 source of truth.
Generated bindings may be offered for convenience, but any implementation that
obeys the wire contract is equally valid. Preserve the simple protoc-like
one-request/one-response mode even if a Bazel-worker-like persistent mode is
added later.

## Language strategy

### Go

The native Go adapter is the reference implementation.

Use `go/packages` to respect modules, workspaces, build tags, generated files,
and the selected target environment. Use `go/types` for definitions, references,
methods, interfaces, and implementations. Evaluate `go/ssa` only for call edges
where its additional cost and approximation are justified.

The Go adapter must expose:

- Modules, packages, files, declarations, and stable symbols.
- Definitions and typed references.
- Imports and package dependencies.
- Methods, receivers, interfaces, and implementations.
- Direct calls supported by the selected analysis mode.
- Tests and their relationship to packages and source.
- Public API fingerprints at package granularity.
- Build configuration fingerprints.

### C# and Visual Basic

Prefer an existing maintained Roslyn-backed SCIP producer. If it does not expose
required facts or incremental behavior, wrap Roslyn through a small .NET adapter
using `MSBuildWorkspace`. Do not reproduce C# parsing or overload resolution.

### F#

F# is not analyzed by Roslyn. Build a focused adapter on
`FSharp.Compiler.Service` unless a maintained semantic indexer becomes
available. FCS provides typed project checks, resolved symbols, and project-wide
symbol uses. Its API can evolve, so pin and record compatible versions.

F# compilation order is semantically significant. Invalidation must operate on
the project dependency and file-order graph rather than treating files as
independent syntax units.

### Python

The baseline Python adapter is a Python subprocess using CPython `ast` plus the
compiler-generated `symtable`. Model names as lexical binding slots with one or
more definition occurrences. Compiler declarations and local/global/free/
nonlocal slot resolution can be exact facts about the recorded interpreter;
runtime object identity, attribute dispatch, imports, decorators, and calls
cannot.

Use `Declared` evidence for import statements, `Syntactic` for direct call
spelling, and omit unsupported dynamic relationships. Do not import repository
modules during indexing. Type-checker enrichment through pinned open-source
Pyright/`scip-python`, Jedi, or `ty` may add inferred or ambiguous facts later,
but it must not weaken the dependency-free lexical correctness floor.

The adapter and exact interpreter version, source/package-root topology, stubs,
configuration, and environment are semantic inputs as their corresponding
features become supported. Toolchain patch changes invalidate facts without
churning stable symbol identity. Dynamic Python exports require conservative
surface invalidation until a richer static export model can safely narrow it.
Test installed adapters on macOS, Linux, and Windows.

### Other languages

Adopt existing SCIP or compiler-native producers behind executable adapters.
Prefer implementing each adapter in the ecosystem best supported by its
compiler APIs: for example, JVM tooling for JVM languages, Rust tooling for
Rust, and TypeScript tooling where the TypeScript compiler API is authoritative.
The Go core coordinates these tools; it does not become their parser host.
A Tree-sitter adapter may
provide best-effort declarations, imports, and syntactic calls for otherwise
unsupported languages. Its provider and `Syntactic` evidence class are visible
in every affected result.

### Structured content and non-compiling artifacts

Use source-only, format-native parsers for structured artifacts. The baseline
workspace provider inventories Git-visible files and parses CommonMark/GFM,
YAML front matter, selected inert HTML, headings, links, embeds, fenced blocks,
routes, series, topics, and explicit generator provenance. Stable document and
path IDs join those facts to compiler documents and across cataloged
repositories.

Renderer dialects are versioned profiles. GitHub repository Markdown, GitHub
Pages/Jekyll, Hugo, mdBook, wikis, MDX, and note-taking syntax do not silently
share heading, route, template, or include semantics. A provider may statically
interpret a declared subset, but it must not run a builder or claim rendered
truth it did not establish.

Because this provider is always present, a malformed, non-UTF-8, oversized, or
unsupported structured artifact degrades to bounded topology-only facts rather
than blocking unrelated compiler queries. Unsupported and ambiguous references
do not become invented graph endpoints; a future first-class content-reference
fact will retain raw syntax, status, candidates, and diagnostics.

## Fact and evidence model

Core entities include:

- Repository: stable source identity and known local roots.
- Worktree: one checked-out Git worktree and its local state.
- Snapshot: immutable committed tree plus an optional dirty overlay.
- Project: build-system compilation project or target.
- Compilation unit: the smallest semantic invalidation boundary supplied by an
  adapter.
- Document: repository-relative path, content hash, language, and provider.
- Symbol: stable semantic identity, display name, kind, definition, and owner.
- Occurrence: definition or reference at a source range.
- Edge: directed typed relationship with evidence.
- External symbol: referenced dependency not defined in an indexed repository.
- Rule: a deterministic architecture constraint evaluated over facts.
- Workspace resource: a repository, directory, file, asset, route, topic, or
  external URL that can participate in navigation without compiling.
- Structured section: a source-addressable heading or code block within a
  content document.

Evidence classes:

```text
Exact       resolved by a compiler or authoritative semantic index
Declared    stated by a build file, manifest, or Weave configuration
Generated   connected through a shared schema or generated artifact
Inferred    deterministically inferred from weaker evidence
Syntactic   extracted from syntax without semantic resolution
Ambiguous   multiple valid targets remain
```

Edges initially include:

```text
Defines      References      Calls          Imports
Contains     Extends         Implements     Instantiates
DependsOn    Tests           Generates      Documents
Exposes       Handles         Reads          Writes
```

The vocabulary may grow, but new edges require explicit semantics and evidence
rules.

## Compact storage

Use `bstore` over bbolt for the first implementation. It is a compact embedded
Go-native store with transactional behavior and indexed records. The database is
derived, so periodic rewrite/compaction is acceptable.

The schema should favor explicit indexed adjacency records over clever object
graphs. Representative records:

```go
type Repository struct {
    ID       uint64 `bstore:"typename Repository"`
    Identity string `bstore:"unique"`
}

type Snapshot struct {
    ID          uint64 `bstore:"typename Snapshot"`
    Repository  uint64 `bstore:"index RepositoryTree,unique"`
    Tree        string  `bstore:"index RepositoryTree,unique"`
    Commit      string
    DirtyDigest string
}

type Document struct {
    ID          uint64 `bstore:"typename Document"`
    Snapshot    uint64 `bstore:"index SnapshotPath,unique"`
    Path        string `bstore:"index SnapshotPath,unique"`
    ContentHash [32]byte
    Language    string
    Provider    string
    ProviderVer string
}

type Symbol struct {
    ID          uint64 `bstore:"typename Symbol"`
    Repository  uint64 `bstore:"index RepositoryStableName,unique"`
    StableName  string `bstore:"index RepositoryStableName,unique"`
    DisplayName string `bstore:"index"`
    Kind        string `bstore:"index"`
    Document    uint64 `bstore:"index"`
    Start       Position
    End         Position
}

type Edge struct {
    ID         uint64 `bstore:"typename Edge"`
    Snapshot   uint64 `bstore:"index"`
    From       uint64 `bstore:"index FromKind"`
    To         uint64 `bstore:"index ToKind"`
    Kind       string `bstore:"index FromKind,index ToKind"`
    Evidence   string `bstore:"index"`
    Occurrence uint64
}
```

The exact tags and decomposition must be validated against bstore behavior.
Traversal needs efficient indexes in both directions. Search should begin with
normalized tokens, prefixes, and compact trigrams rather than adding a separate
search service.

The store layer must provide:

- Schema versioning and safe migrations.
- Atomic replacement of one compilation unit's facts.
- Consistent concurrent readers with one bounded writer.
- Corruption detection and rebuild guidance.
- Deterministic export for tests and diagnostics.
- Database compaction and garbage collection.
- Resource limits on records, strings, traversals, and result sets.

## Location and repository identity

Do not check detailed databases into source control. Binary derived state causes
opaque diffs, merge conflicts, repository growth, platform churn, and incorrect
indexes after branch changes.

For a Git repository, resolve storage through Git rather than assuming `.git` is
a directory:

```sh
git rev-parse --git-common-dir
git rev-parse --git-path weave
```

Linked worktrees use a `.git` file, and each worktree needs its own mutable
overlay while sharing immutable facts where possible.

Candidate layout:

```text
<git-common-dir>/weave/
  repository.db
  manifests/
  worktrees/<worktree-id>.db
  snapshots/<tree-oid>.db
```

Cross-repository state belongs under the platform's user data directory:

```text
~/.local/share/weave/catalog.db
~/.local/share/weave/repos/<repository-id>.db
```

Use the appropriate platform directory on macOS and Windows. Repository identity
should derive from a normalized canonical remote when available, with a root
commit or generated local identity as fallback. Paths are locations, not
identities.

## Git, branches, and worktrees

Source state is identified by:

```text
repository + worktree + tree/commit + dirty overlay + build variant
```

Committed snapshots are immutable. Dirty files form a small mutable overlay.
When switching branches:

1. Activate an existing snapshot for the target tree when present.
2. Reuse document and fact records with matching content and context hashes.
3. Reindex only changed semantic units.
4. Apply the current worktree's dirty overlay.

Freshness fingerprints include:

- Source contents.
- Build manifests and workspace configuration.
- Dependency lock state.
- Build tags, target framework, target OS, and architecture.
- Relevant environment selected explicitly by the adapter.
- Adapter and compiler/toolchain versions.
- Weave schema and normalization versions.

The same repository may legitimately have multiple graph variants.

## Freshness without an agent

Correctness is query-driven. Every read command performs a cheap freshness
check, incrementally updates if necessary, and then queries a consistent
snapshot.

The core freshness sequence:

1. Locate the repository and worktree.
2. Read the stored baseline and adapter configuration.
3. Use Git plus explicit content hashes to identify tracked changes, renames,
   deletions, untracked source, and build-context changes.
4. Ask adapters to refresh affected semantic units.
5. Propagate invalidation when public-surface or dependency fingerprints change.
6. Replace affected facts atomically.
7. Record the new baseline.
8. Execute the requested query.

Hooks such as `post-commit`, `post-checkout`, and `post-merge` may warm indexes,
but hooks are optional and never authoritative. A `weave watch` process may be
provided for sub-second interactive latency, but no background process is
required.

Concurrent invocations use a repository-scoped writer lock. Readers may continue
on the last complete transaction, but a command promising fresh results waits
for or performs the refresh. Locks must have owner diagnostics and safe stale
recovery.

## Incremental invalidation

File-level invalidation is insufficient for semantic changes. Adapters report:

- The semantic compilation unit.
- Direct dependencies.
- Public-surface fingerprint.
- Full semantic-input fingerprint.
- Produced document and fact inventory.

An implementation-only change can replace one unit's local facts. A public API
change invalidates dependent compilation units. A build configuration change may
invalidate a complete variant.

Invalidation must be conservative: extra work is acceptable; stale semantic
answers are not.

Use exact Git differences when Git already knows both inventories. Evaluate
RIBLT for cases where Weave must reconcile large independently maintained fact
or artifact inventories without transferring or rescanning all identities. Any
RIBLT integration must benchmark against sorted hashes, Git diffs, and direct
set comparison before adoption.

## Cross-language relationships

Compilers resolve relationships inside their semantic worlds. Cross-language
edges require explicit evidence from:

- Build project references and package manifests.
- Generated code and shared schema fingerprints.
- Protobuf, OpenAPI, GraphQL, and database schemas.
- Declared HTTP/gRPC routes and generated clients.
- Configuration keys and command registrations.
- Explicit user-maintained bridge declarations.

Cross-language inference is not part of the initial core. Exact, declared, and
generated relationships come first. The contextual linking CLI resolves
human-facing local or catalog queries once, then stores exact graph entity IDs;
it must support heterogeneous code and content resources without weakening the
evidence carried by compiler-produced edges.

## Cross-repository operation

A global catalog records repositories, local checkouts, indexed snapshots,
declared relationships, and resolved external symbols. Per-repository databases
remain independent to limit locks and make deletion straightforward.

Example checked-in configuration, containing declarations rather than derived
facts:

```yaml
version: 1
repositories:
  - identity: github.com/TheFellow/go-modular-monolith
  - identity: github.com/TheFellow/TheFellow.github.io

links:
  - from: repo:github.com/TheFellow/go-modular-monolith
    to: page:/projects/go-modular-monolith/
    kind: Documents
```

The global query layer federates bounded subqueries over relevant repository
indexes. A local repository query never needs to acquire every repository lock.

## CLI contract

Use `github.com/urfave/cli/v3`. Commands are thin presentation adapters over an
application service boundary. Construction must accept dependencies and output
writers so behavior is directly testable.

Initial tree:

```text
weave
  init
  index
  status
  symbols
  definition
  references
  callers
  callees
  path
  impact
  dependencies
  links
    add
    update
    remove
    list
  workspace
    find
    outline
    links
    backlinks
  architecture
    check
  repos
    add
    remove
    list
  adapters
    list
    doctor
  export
  verify
  gc
  version
```

Authored relationships are an SDK-like delivery surface over the same
normalized relationship contract used by built-in providers. Human-facing
endpoint queries resolve once and persist exact graph entity IDs. Endpoints may
be compiler symbols, packages, files, documents, sections, routes, assets,
URLs, or intentional open resources; their heterogeneous shape must not create
a parallel edge store or a document-only predicate vocabulary. Catalog-scoped
authoring may connect registered worktrees, while evidence remains declared or
generated rather than pretending to be compiler-exact.

The first CLI commit should establish this tree with concrete behavioral tests.
Commands whose application capability is not yet implemented return success and
write nothing when invoked in their valid empty form. Unknown commands, invalid
flags, and malformed arguments remain errors. This gives later work stable
surfaces to grow into organically.

Output rules:

- Normal results go to stdout.
- Diagnostics and refresh notices go to stderr.
- Successful empty results write nothing unless a command explicitly promises a
  status document.
- `--json` uses a versioned envelope.
- Paths are repository-relative by default.
- Source locations use one documented coordinate convention.
- Results have deterministic ordering.
- Queries are bounded by default and report truncation explicitly.
- Exit codes distinguish usage, unavailable capability, stale/corrupt index,
  failed architecture rules, and internal failure.

Example JSON envelope:

```json
{
  "schema": "weave.query/v1",
  "repository": "github.com/TheFellow/weave",
  "snapshot": {
    "commit": "...",
    "tree": "...",
    "dirty": false
  },
  "freshness": "current",
  "results": []
}
```

## Query semantics

Initial deterministic queries:

- Find symbols by exact name, stable identity, prefix, token, or bounded fuzzy
  match.
- Resolve definitions and occurrences.
- Traverse callers, callees, references, imports, implementations, and package
  dependencies.
- Find bounded shortest paths with allowed edge kinds.
- Compute an impact closure from symbols, files, packages, or a Git diff.
- Identify affected tests through declared and semantic dependency edges.
- Evaluate architecture rules over the same graph.

Natural-language querying is intentionally delegated to the consuming agent.

## Architecture rules

Weave should eventually enforce graph facts, not merely display them. Rules may
express constraints such as:

- Package A may not import package B.
- Presentation targets may depend on application contracts but not storage.
- Only one module may instantiate a concrete adapter.
- A public handler must have an associated test or documented exemption.
- A cross-domain call must traverse an approved boundary.

Rules are checked-in text configuration. Evaluation is deterministic and usable
locally or in CI. Every violation includes an evidence path.

## CI model

CI validates reproducibility and policy. It does not require committing the
binary database.

Representative commands:

```sh
weave index --at HEAD
weave verify
weave architecture check
weave impact --diff origin/main...HEAD --json
```

CI may cache an index by tree and configuration fingerprint or upload it as a
build artifact for remote consumers. A small textual manifest may be committed
if useful, containing only format versions, extractor versions, tree identity,
and graph digest.

CI responsibilities:

- Rebuild from clean source.
- Verify deterministic normalized exports.
- Check architecture rules.
- Produce bounded impact summaries.
- Exercise adapters against genuine fixture projects.
- Cache or publish disposable artifacts.

## Security and trust boundaries

Repositories and adapter output are untrusted input.

- Do not execute repository scripts merely to index source unless the user opts
  into the build behavior and the adapter documents it.
- Bound files, records, strings, nesting, processes, time, memory, and traversal.
- Validate every adapter frame and reject unknown incompatible versions.
- Use argument arrays, never shell interpolation, to launch adapters.
- Keep network access disabled by default.
- Record when an adapter invokes a build tool that may restore dependencies.
- Escape all exported display formats.
- Avoid storing source outside the repository unless required for concise query
  evidence; prefer ranges and bounded snippets.
- Never ingest secrets excluded by Git or explicit ignore configuration unless
  the user opts in.

No telemetry is required for core behavior.

## Testing philosophy

Tests establish behavior, not a coverage score.

Use:

- Table-driven unit tests for identities, normalization, traversal, output, and
  invalidation decisions.
- Temporary real Git repositories for branches, dirty overlays, renames, linked
  worktrees, and repository discovery.
- Small genuine source fixtures that are compiled or checked by their native
  toolchains.
- Golden normalized fact exports only where reviewability is high.
- Contract suites shared by all storage implementations and adapters.
- Property tests for stable identity, set reconciliation, and graph invariants.
- Fuzz tests at adapter protocol and database ingestion boundaries.
- End-to-end CLI tests using injected streams and actual command execution.
- Cross-platform CI on macOS, Linux, and Windows as soon as path and process
  behavior exists.

Do not mock compiler semantics. Provide tiny source projects that demonstrate
overloads, generics, interface implementations, build variants, F# file order,
and cross-project references.

Every bug in normalization, freshness, branch handling, or adapter ingestion
earns a focused regression test.

## Performance and resource goals

Targets are hypotheses until measured and recorded.

- Query startup should feel immediate on a fresh index.
- A one-file implementation change should normally refresh in less than a second
  on repositories of the current TheFellow scale.
- Empty freshness checks should avoid walking and hashing every source file.
- Memory usage should be bounded independently of total graph size where
  practical.
- Result size and graph depth are bounded by default.
- Database size should remain materially smaller than source plus build output.
- Reusing an existing tree snapshot should avoid semantic reindexing.
- Clean rebuild output should normalize identically across supported platforms,
  excluding explicitly recorded platform variants.

Benchmarks must include `go-modular-monolith`, `arch-lint`, `cedar-dotnet`, and
`fkyeah` once their adapters exist.

## Delivery sequence

### Milestone 0: research and contracts

- Preserve prior-art research for SCIP, compiler APIs, local graph stores, Git
  caches, incremental build systems, and agent-facing code indexes.
- Define vocabulary, evidence classes, stable identities, and adapter protocol.
- Publish language-neutral adapter fixtures and a cross-language conformance
  contract modeled on protoc plugins.
- Record architectural decisions.

### Milestone 1: CLI skeleton

- Establish the complete urfave/cli v3 command tree.
- Inject application dependencies and output streams.
- Add behavioral tests proving valid placeholder commands succeed silently.
- Add usage and failure tests.

### Milestone 2: storage and query kernel

- Implement bstore lifecycle and schema versioning.
- Ingest deterministic fixture facts.
- Implement symbols, definitions, references, traversal, path, and impact.
- Add JSON contracts and deterministic text output.

### Milestone 3: Git-native freshness

- Discover repositories, common dirs, and linked worktrees.
- Model committed snapshots and dirty overlays.
- Implement lazy freshness and exact changed-file discovery.
- Reuse content-addressed facts across branches.
- Add locking, diagnostics, compaction, and garbage collection.

### Milestone 4: native Go indexing

- Build the Go reference adapter.
- Index packages, typed symbols, references, interfaces, and calls.
- Fingerprint compilation inputs and public surfaces.
- Implement conservative incremental propagation.
- Validate on real TheFellow repositories.

### Milestone 5: SCIP interoperability

- Ingest SCIP protobuf indexes.
- Discover and run supported external indexers.
- Preserve provider identity and evidence.
- Add contract fixtures from multiple languages.

### Milestone 6: .NET precision

- Integrate C# through existing Roslyn-backed prior art where sufficient.
- Implement the focused FCS adapter for F# gaps.
- Validate the language-neutral process model with the Python-native lexical
  adapter and honest dynamic-language evidence.
- Model MSBuild projects, target frameworks, and cross-project dependencies.
- Test mixed C#/F# solutions.

### Milestone 7: cross-repository catalog and CI

- Register repositories and local checkouts.
- Federate bounded queries.
- Resolve exact external symbols and declared bridges.
- Add deterministic architecture rules.
- Provide CI cache, artifact, verify, and impact workflows.

### Milestone 8: hardening

- Cross-platform releases and adapter discovery.
- Schema migration, doctor, recovery, and resource limits.
- Performance baselines and incremental benchmarks.
- Security review and malformed-input fuzzing.
- Full vision audit.

## Definition of a useful first release

The first release is useful when a user can:

1. Install one Weave executable.
2. Enter a Go, C#, or F# repository.
3. Run a query without starting a daemon.
4. Receive current definitions, references, dependency paths, or impact results
   with file and source evidence.
5. Change a file and receive refreshed results without a full rebuild.
6. Switch branches or worktrees without corrupting or confusing the index.
7. Delete all Weave data and deterministically reconstruct it.
8. Connect Codex or another agent through the CLI or a thin MCP adapter without
   configuring another model.
9. Run the same index and architecture checks in CI.
10. Understand exactly which provider established every material edge.

## Decisions we should resist revisiting casually

- No LLM in the indexing or query core.
- No required daemon.
- No checked-in binary database.
- No universal hand-maintained language parser.
- No public language adapter API that requires Go or in-process loading.
- No provider-name special case where a negotiated capability suffices.
- No Roslyn claim for F#.
- No syntax-derived relationship labeled as compiler-exact.
- No separate implementation behind MCP.
- No stale answer presented as current.
- No global rebuild when a bounded correct invalidation is available.
- No major subsystem without preserved prior-art research.

## Open questions

These require measured spikes or prior-art review:

- Whether SCIP stable symbols can serve directly as internal symbol identities or
  require a repository-qualified wrapper.
- Whether one bstore database or immutable snapshot databases plus overlays give
  better compaction and branch reuse.
- Which Go call-analysis mode gives the best precision/cost tradeoff.
- Whether the existing C# SCIP producer exposes enough project and call detail.
- The smallest stable FCS surface needed for F# facts.
- How much source text, if any, should be duplicated for fast agent responses.
- Whether compact trigrams are sufficient for symbol search.
- Where RIBLT improves real reconciliation compared with Git and sorted hashes.
- Which cross-repository identities remain stable across forks and remote moves.
- How CI artifacts are discovered locally without introducing a service.

Resolve these through focused experiments, record results under `.ai/prior-art/`
or `.ai/decisions/`, and choose the smallest mechanism that preserves the product
principles.

## Closing standard

Weave succeeds when repository structure becomes cheap, local, precise evidence.
It should disappear into the workflow: no server to tend, no model to configure,
no binary artifact to commit, and no stale graph to remember to rebuild.

The source remains authoritative. Compiler tooling explains it. Git identifies
what changed. Weave stores the minimum durable facts needed to make those truths
readily queryable. Humans and agents do the reasoning.
