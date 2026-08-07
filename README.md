# Weave

Weave is a local-first, Git-aware semantic index for the knowledge encoded in a
workspace. It combines compiler-backed facts with files, structured documents,
sections, links, assets, routes, and metadata; keeps derived state outside
commits; and exposes bounded deterministic CLI queries for people and coding
agents. There is no model, hosted service, or required daemon in the indexing
path.

> **Status:** early alpha. Native Go indexing and the query/storage lifecycle
> are usable. Installed C#/F#, Python, Rust, C/C++, TypeScript/JavaScript, and
> configured JVM adapters can participate in query-driven freshness; a broad
> Universal Ctags fallback is available explicitly. Release companions are
> configured but have not been exercised by a tag, and external adapters
> currently advertise full rather than incremental refreshes. Output schemas are
> versioned, but compatibility before the first tagged release is not promised.

## Install

Build the current checkout with Go 1.26.5 or newer:

```sh
go install github.com/TheFellow/weave/cmd/weave@latest
weave version
```

Tagged releases publish checksummed archives for macOS, Linux, and Windows on
amd64 and arm64. Until the first tag exists, `go install` or `go build -o weave
./cmd/weave` is the supported path. Prerelease archives also include SPDX SBOMs
and keyless GitHub build provenance, but are not yet Apple- or Windows-signed;
see [release installation and verification](docs/release-installation.md).

## Go quickstart

From a Git repository containing one or more Go modules:

```sh
weave index
weave status
weave watch
weave symbols Handle --limit 20
weave definition Handle
weave references Handle --json
weave context Handle
weave callers Handle
weave dependencies github.com/example/project/package
weave path SymbolA SymbolB --kind calls --max-depth 8
weave impact Handle --limit 50
weave impact --file internal/service.go --limit 100
weave impact --package github.com/example/project/internal/service
weave impact --git-diff origin/main --json
weave diff graph --base origin/main --json
weave diff api --base origin/main --head HEAD
weave diff tests --base origin/main
weave graph Handle --kind calls --kind implements --output handle.dot
weave links add guide-documents-handler --from 'docs/guide.md#flow' --to Handle --kind documents
```

Every database-backed query performs a cheap Git freshness check first. A
tracked, untracked, renamed, deleted, branch, worktree, build-context, or
provider change refreshes the affected Go compilation units before the query
runs. Refresh notices go to stderr; results go to stdout. Empty text results are
silent. `--json` emits the `weave.query/v1` envelope and deterministic ordering.
Locations are repository-relative, one-based in text, and zero-based UTF-8 byte
coordinates in JSON facts.

## Optional watch warming

`weave watch` is an optional foreground latency warmer. It performs one
non-forced freshness refresh by default, then polls the exact Git/worktree
observation and invokes the same `Freshness.Ensure` and provider pipeline used
by queries. It does not install hooks, start a daemon, maintain another file
inventory, or write another database. Queries remain authoritative and perform
their normal freshness check whether the watcher is running or not.

```sh
weave watch
weave watch --poll-interval 2s
weave watch --initial=false
weave watch --json
```

The default 750 ms poll interval is also the edit coalescing window. Exact Git
reconciliation makes editor atomic renames, burst saves, branch/index changes,
and missed native filesystem events ordinary state observations. Ignored paths
remain ignored and Git-visible untracked provider inputs retain their existing
semantics. Polling does no refresh work while the observed source/manifest state
is current and no refresh for that same observation has failed. The initial
refresh and every query remain authoritative for graph-generation verification.

Human ready/refreshed lifecycle lines go to stdout and recoverable provider
errors or diagnostics go to stderr. `--json` writes newline-delimited
`weave.watch-event/v1` records (`ready`, `refreshed`, and `error`) to stdout;
unchanged polls emit nothing. Error text is UTF-8-safe and capped at 8 KiB.
Unchanged failures retry with exponential backoff capped at 30 seconds, while a
new exact observation bypasses that backoff. Interrupt and termination signals
cancel in-flight Git/provider work and stop the foreground process cleanly.
If another edit lands between a successful refresh and its follow-up
observation, the completion retains the exact refreshed status but omits the
observation token; the next poll then refreshes the new state.

Each linked worktree warms only its own Git-resolved derived index and writer
lock. `WEAVE_DATABASE` selects an unmanaged database snapshot, so watch mode is
intentionally unavailable in that mode.

`dependencies` returns direct `depends-on` and `imports` edges. Use `path` with
`--kind depends-on` for a bounded transitive route. Every edge includes its
provider and evidence class in JSON/export output.

## Source-rich context

`weave context TARGET` uniquely resolves any indexed code or workspace entity
and returns a compact one-hop dossier: the focus, definition/reference
evidence, current source excerpts, direct incoming and outgoing relationships,
materialized adjacent entities, repository provenance, freshness, and explicit
truncation metadata.

```sh
weave context HandleRequest
weave context README.md --context-lines 4
weave context docs/design.md#storage --json
weave context SharedType --scope catalog --repo github.com/example/service
```

The default independently caps occurrences and each relationship direction at
16, includes two surrounding source lines, and returns at most 64 KiB of source
text. Use `--limit`, `--context-lines`, and `--max-source-bytes` to lower or
raise those bounded ceilings. JSON retains the normal `weave.query/v1`
envelope and carries a `weave.context/v1` result under `context`.

Source is re-read from the owning current worktree, not cached in the graph.
Weave only serves canonical repository-relative, Git-visible, regular UTF-8
files; it verifies opened-file identity and an indexed content hash when one is
available. Missing, changed, ignored, external, oversized, non-UTF-8, unsafe,
or byte-budgeted source is reported with a status and no guessed excerpt.
Ambiguous names fail with exact graph IDs rather than silently choosing a
target. Catalog context uses the existing bounded refresh-before-open
federation and reports partial members through diagnostics and freshness
metadata.

## Graphviz DOT

`weave graph` renders a bounded neighborhood around any resolvable symbol,
package, file, document, section, route, or other indexed resource:

```sh
weave graph AuthService --kind calls --kind implements > auth.dot
weave graph README.md --direction outgoing --max-depth 3 --output docs.dot
weave graph Handle --scope catalog --repo github.com/example/service --json
weave graph README.md --interactive
dot -Tsvg auth.dot -o auth.svg
```

The focus is gold, incoming nodes are green, outgoing nodes are purple, and
nodes reachable in both directions are rose. Materialized nodes are clustered
by provider; unresolved external endpoints remain visible with dashed borders.
Edge labels show relationship kinds, while DOT tooltips retain stable names,
evidence, and providers. Output ordering and generated node names are stable.

The default traversal includes high-level code, dependency, hierarchy, content,
and provenance relationships while omitting noisy occurrence-level `defines`
and `references` edges. Repeat `--kind` to select any exact edge kind, including
those two, and use `--direction incoming|outgoing|both`, `--max-depth`,
`--limit`, and `--max-edges` to control the view. Equivalent parallel edges are
collapsed visually with a count; JSON retains every source edge. All limits are
mandatory and bounded even for catalog queries. `--output` writes DOT directly;
without it DOT is written to stdout. Weave does not invoke Graphviz, so DOT
generation works without a renderer installed. `--json` returns the same
bounded neighborhood in the `weave.query/v1` envelope for agents.

`--interactive` starts a temporary random loopback server and opens the same
bounded graph in a human-facing browser explorer. Clicking a node refocuses the
query; direction, depth, edge kind, provider, and evidence controls request new
current DOT snapshots, which animate using stable semantic node and edge IDs.
The view includes pan/zoom and focus history. D3, d3-graphviz, and Graphviz WASM
are pinned inside the Weave binary, so the explorer loads no CDN or remote
runtime assets. Use `--no-open` to print the tokenized local URL without
launching a browser. The server exists only for that command and stops with it.

## Workspace and content navigation

The built-in source-only workspace provider indexes every Git-visible path and
parses Markdown/GFM, YAML front matter, inert HTML headings and links, fenced
blocks, routes, topics, series, and explicit generated-from declarations. It
does not run Jekyll, Liquid, Mermaid, embedded examples, plugins, or network
requests. Malformed, non-UTF-8, or oversized structured files degrade to safe
file-topology facts, so prose cannot make compiler-backed queries unavailable.
That degradation is persisted with the current derived manifest and reported
on stderr (and in JSON freshness diagnostics); transient read/identity failures
abort publication and retry on a later invocation.

```sh
weave workspace find "presentation surfaces"
weave workspace outline README.md
weave workspace links README.md
weave workspace backlinks docs/design.md
weave workspace backlinks docs/design.md --scope catalog
```

`ws` is an alias for `workspace`. Documents, sections, directories, assets, and
routes have stable repository-qualified graph identities. Relative Markdown
links resolve against Git's exact path casing; known headings and declared
permalinks are targetable. GitHub URLs remain real URL resources, with an
inferred `resolves-to` edge only for unambiguous conventional refs so a
cataloged repository can join them without discarding the authored URL or ref.
Results retain `syntactic`, `declared`,
`generated`, or `exact` evidence instead of treating prose as compiler truth.
Compiler providers can join exact declarations to the same path anchors, so
default impact traversal includes incoming links and embeds: changing a source
file or asset can surface the READMEs and articles that explain or display it.
See [ADR 0010](.ai/decisions/0010-workspace-and-structured-content-index.md)
for the safe source-profile boundary and current renderer limitations.

## Contextual relationship authoring

Relationships that a compiler or content parser cannot establish can be
authored without editing graph IDs by hand:

```sh
weave links add guide-documents-handler \
  --from 'docs/guide.md#request-flow' \
  --to HandleRequest \
  --kind documents \
  --note 'The guide explains this entry point.'
weave links update guide-documents-handler --to 'id:git-commit:0123456789abcdef'
weave links list --json
weave links remove guide-documents-handler
```

`link` aliases `links`. Add/update resolve each human-facing query uniquely,
then persist exact endpoints in `.weave/bridges.json` and immediately refresh
them into ordinary graph edges. Every normalized edge kind is supported.
Authored facts are `declared`, except `generates`, which is `generated`; the CLI
cannot claim compiler-exact evidence.

Endpoints may be code, packages, files, documents, headings, routes, assets,
URLs, or any other indexed resource. Use `--scope catalog` and repeat `--repo`
to resolve a relationship across registered worktrees. `id:<exact-id>` creates
an intentional open endpoint for an immutable commit or resource that is not
materialized yet. Path, impact, DOT, export, architecture, and federated queries
consume these as the same edges emitted by built-in providers. See
[authored contextual relationships](docs/declared-bridges.md) and
[ADR 0012](.ai/decisions/0012-contextual-relationship-authoring.md).

## Impact analysis

Impact roots may be a symbol, repeated repository-relative `--file` and
`--package` values, or `--git-diff REVISION` (which compares that revision to
the current working tree). File roots use indexed definitions and references;
package roots use compiler-emitted package ownership. Output is bounded and
deterministic. An affected-tests projection is emitted only for explicit
`tests` edges or compiler-indexed Go test declarations—Weave does not guess
build targets from directory names.

## Semantic snapshot diffs

`weave diff graph|api|impact|tests --base REV [--head REV]` separates Git's
source inventory from normalized semantic changes. Omitting `--head` compares
against the current dirty worktree; supplying it compares two immutable refs.
Historical refs are rebuilt in disposable detached worktrees without switching
the user's branch, and their databases are removed afterward.

JSON uses `weave.snapshot-diff/v1` and identifies each side by exact commit,
tree, freshness observation, and a stable digest of the sorted normalized
snapshot. Graph changes retain before/after facts and stable-ID transition keys
for the local explorer. API results report only provider-owned surface
fingerprints and leave compatibility `unknown`; they do not guess breaking
changes from symbol names. Impact uses the normal reverse graph traversal, and
each affected test includes its selection reason and evidence.

## C# and F# adapter

The optional adapter uses Roslyn/MSBuild for C# and
FSharp.Compiler.Service for F#. Tagged releases include same-version NuGet
tool and host archives for every supported core platform. For a source build:

```sh
dotnet restore adapters/dotnet/Weave.Adapter.sln
dotnet build adapters/dotnet/Weave.Adapter.sln --no-restore
export WEAVE_DOTNET_ADAPTER="$PWD/adapters/dotnet/src/Weave.Adapter/bin/Debug/net10.0/Weave.Adapter"
weave symbols MyType
```

On Windows, use the generated `.exe`. The target repository must already be
restored. A discovered adapter is automatically invoked only when .NET compiler
or project inputs changed; its complete unit inventory is composed atomically
with Go facts. Automatic mode permits MSBuild project evaluation because that is
required for compiler truth, so only expose `weave-dotnet` for repositories you
trust. It never permits network, restore, or generators. The explicit `weave
index --adapter ...` path remains available for other permission choices.
`weave adapters doctor` runs a bounded native `describe`
handshake and reports adapter/runtime capabilities; it does not index, build,
restore, or install anything. See the [adapter
guide](adapters/dotnet/README.md) for coverage and current limitations.

This is an open process extension point, not a .NET-specific hook. Any
executable may implement the language-neutral
[`weave.adapter/v0` contract](protocol/adapter/v0/README.md) using its
ecosystem's compiler APIs and can be invoked with `weave index --adapter PATH`.
The core sends a bounded request on stdin, accepts protocol frames only on
stdout, keeps diagnostics on stderr, and atomically publishes a complete valid
inventory. The contract fixtures are exercised by the Go test suite so adapter
authors do not need to import Weave's Go internals.

### Automatic third-party adapters

An independently installed adapter can join normal query-driven freshness
without a core code change. Put its literal command, conservative input set,
and permissions in a user-controlled registry, then select that registry
explicitly:

```json
{
  "schema": "weave.adapters/v1",
  "adapters": [{
    "name": "example-zig-index",
    "command": ["example-zig-index", "--target", "host debug"],
    "inputs": {
      "extensions": [".zig", ".zon"],
      "filenames": ["build.zig"]
    }
  }]
}
```

```sh
export WEAVE_ADAPTER_CONFIG=/absolute/path/to/adapters.json
weave adapters doctor
```

Weave never discovers this configuration from a repository and never scans
`PATH` for arbitrary adapter prefixes. Selecting the registry is the trust
decision; bare executable names inside it use normal cross-platform executable
lookup. Command values are passed as an argv array without a shell, permissions
default to denied, and invalid or unavailable registrations make automatic
freshness fail closed. See the [discovery research and Rust/C++
examples](.ai/prior-art/adapter-discovery/README.md).

## Python adapter

The optional [`weave-python`](adapters/python/README.md) companion is itself
written in Python and uses CPython's parser and compiler symbol tables. Install
it from source with `python -m pip install ./adapters/python`; once the
`weave-python` executable is on `PATH`, normal queries refresh Git-visible `.py`
files automatically.

Lexical declarations and scope-slot references are exact facts about the
recorded interpreter. Repeated bindings retain every definition occurrence.
Imports are declared evidence and calls are syntactic because Python can replace
their runtime targets. The adapter never imports project modules or silently
upgrades dynamic behavior to compiler-exact evidence. Repository Git fsmonitor
commands and Python source symlinks are disabled so a read-only refresh cannot
execute repository configuration or hash different bytes than it indexes.

## Rust adapter

[`weave-rust`](adapters/rust/README.md) delegates semantic truth to the
maintained `rust-analyzer scip` exporter and implements the adapter protocol in
Rust. Install it with `cargo install --locked --path ./adapters/rust` and keep a
compatible `rust-analyzer`, Cargo, and rustc on `PATH`. Once discovered, normal
queries automatically index repositories containing `Cargo.toml` or
`rust-project.json`.

Automatic mode grants Cargo workspace evaluation but keeps network, restore,
build scripts, and procedural macros disabled. Only expose the adapter to
repositories you trust; explicit `weave index --adapter weave-rust` flags can
grant the additional capabilities when required. Definitions, references, and
explicit implementation relationships come from rust-analyzer; ordinary SCIP
references are not relabeled as calls.

## C and C++ adapter

[`weave-cpp`](adapters/cpp/README.md) is a thin wrapper around the maintained
Clang 21-based `scip-clang` indexer. Build the wrapper with
`go install ./adapters/cpp/cmd/weave-cpp` and install the pinned producer with
`adapters/cpp/scripts/install-scip-clang.sh ~/.local/bin`. A repository with one
Git-visible `compile_commands.json` then participates in automatic freshness.

Rust and C/C++ conservatively fingerprint every Git-visible file once their
project marker activates, because compiler includes and macros can consume
files with arbitrary extensions. Ignored/generated compilation databases are
intentionally outside Git freshness; select them explicitly with
`--adapter-arg=--compdb=...`. Multiple build variants also require an explicit
selection rather than silently merging incompatible semantics.

## TypeScript and JavaScript adapter

[`weave-typescript`](adapters/typescript/README.md) is a small Go protocol
wrapper around the maintained `@sourcegraph/scip-typescript` producer; it does
not parse either language. Install the locked producer closure with
`sh adapters/typescript/scripts/install-scip-typescript.sh`, build the wrapper
with `go install ./adapters/typescript/cmd/weave-typescript`, and select the
printed producer using `WEAVE_SCIP_TYPESCRIPT`. A repository-root `tsconfig.json`
or `jsconfig.json` then participates in automatic freshness.

The automatic path never installs packages, invokes a package manager, infers a
configuration, or runs generators. In particular, it does not use upstream's
`--infer-tsconfig` because that option writes a generated configuration into
the repository. Nested monorepo projects remain explicit selections. Once a
root project activates the adapter, Weave conservatively fingerprints every
Git-visible file because TypeScript configuration, project references, module
resolution, JSON modules, and declaration inputs can extend beyond source-file
suffixes.

## JVM adapter

[`weave-jvm`](adapters/jvm/README.md) delegates Java and Kotlin semantics to the
maintained compiler-backed `scip-java` producer. The Go wrapper, protocol tests,
`adapters list`, and `adapters doctor` do not need Java: `describe` reports a
pinned producer contract without starting the producer. Actual indexing can use
an externally installed `scip-java` launcher with JDK 17+ or a user-supplied
shim around the official container.

`scip-java` runs the repository's Gradle, Maven, or Bazel build and may restore
dependencies, access the network, or execute plugins, annotation processors,
and generators. Weave therefore does not grant it automatic query-time powers.
Run it explicitly for a trusted checkout with all four grants:

```console
weave index --adapter weave-jvm \
  --allow-build-tool --allow-restore --allow-network --allow-generators
```

An explicitly selected `WEAVE_ADAPTER_CONFIG` may opt `scip:scip-java` into
query-driven freshness with a repository-appropriate conservative input set
and the same permissions. Java and Kotlin are advertised; Scala is not, because
the current upstream project no longer claims Scala support.

## Broad Universal Ctags fallback

[`weave-ctags`](adapters/ctags/README.md) is a compiled Go protocol wrapper for
a separately installed, pinned Universal Ctags producer. It gives unsupported
languages and formats a conservative definition outline without pretending to
have compiler semantics. Every emitted symbol and definition occurrence is
`syntactic`; calls, references, inheritance, and guessed edges are deliberately
absent. Safely read tagless files still receive document units.

Build the wrapper and invoke it explicitly while overlap policy with exact
providers is still being evaluated:

```console
go install ./adapters/ctags/cmd/weave-ctags
weave index \
  --adapter "$(command -v weave-ctags)" \
  --adapter-arg=--ctags=/absolute/path/to/uctags \
  --adapter-arg=--producer-version=6.2.1
```

The wrapper rejects non-Universal variants such as macOS `/usr/bin/ctags`,
disables ambient Ctags option files, indexes a private bounded snapshot of
Git-visible regular UTF-8 files, and fingerprints the producer's parser
capabilities. Universal Ctags remains a separate GPL-2.0 process and is not
linked or bundled into the wrapper artifact.

## Derived data and recovery

Per-worktree state lives at the Git-resolved `git rev-parse --git-path weave`
location (normally `.git/weave`). The cross-repository catalog uses the
platform user state directory. Catalog-scoped symbol and graph queries also
materialize an immutable, generation-named machine aggregate beside the
catalog. It contains only symbols, token postings, edges, and worktree
provenance; per-worktree indexes remain the freshness authorities. None of
these files belongs in source control.

```sh
weave verify          # logical graph checks; warnings are non-fatal
weave export --json   # deterministic diagnostic export
weave gc              # compact a closed local database
rm -rf "$(git rev-parse --git-path weave)"  # discard derived state
weave index           # deterministic rebuild
```

Schema mismatch, checksum failure, and invalid physical storage are reported as
rebuildable derived-state errors. Back up source, not the index. The
[release installation and recovery guide](docs/release-installation.md) also
documents catalog recovery, upgrades, and rollback.

Per-worktree storage format 2 keeps normalized graph and CLI/JSON/DOT
identities unchanged while using compact numeric join keys internally. Repeated
provider, provider-version, language, symbol-kind, and occurrence-role values
are interned; symbol/occurrence/edge ranges and evidence live in separately
retrievable detail records. Both adjacency directions remain indexed. Intern
and external-entity references are released transactionally when a semantic
unit is replaced, and `weave verify` recomputes those invariants.

There is intentionally no v1-to-v2 migration: an old marker is inspected
read-only and rejected with remove-and-reindex guidance before bstore can alter
it. A clean rebuild is the upgrade. Authored intent is not lost because
`.weave/bridges.json` is source configuration, not part of the disposable
database; its provider recreates contextual edges during indexing. A second
“durable links” database would duplicate that canonical declaration.

## CI and architecture policy

`weave ci index` refreshes a cache-restored index; `weave ci check` verifies
graph integrity and checked-in architecture rules. SARIF and deterministic
exports are supported. See [CI](docs/ci.md), [architecture
rules](docs/architecture-rules.md), and the [example workflow](.github/workflows/weave.yml).

## Cross-repository queries

`weave repos add /path/to/worktree` registers explicit local worktrees. Queries
with `--scope catalog` or repeated `--repo` selectors refresh every selected
member before opening its database. A member that cannot refresh is excluded and
reported on stderr/JSON; its stale facts are never silently served. Healthy
members still return bounded partial results with repository provenance.

After those authoritative checks, `symbols`, `callers`, `callees`,
`dependencies`, `path`, `impact`, and `graph` reuse the exact machine aggregate
generation when available. A missing, mismatched, corrupt, interrupted, or
locked aggregate is rebuilt atomically or falls back to the already validated
worktree federation. Deleting the aggregate directory is always safe. Defaults:

- macOS: `~/Library/Application Support/weave/aggregate/`
- Linux: `${XDG_STATE_HOME:-~/.local/state}/weave/aggregate/`
- Windows: `%LOCALAPPDATA%\weave\aggregate\`

`WEAVE_AGGREGATE` accepts an absolute directory override. An explicit
`--catalog /absolute/path/catalog.db` relocates the default aggregate to the
adjacent `aggregate/` directory, which keeps isolated catalog installations
self-contained.

## Scope and roadmap

The native Go provider covers typed declarations/references, imports,
dependencies, interfaces/implementations, and direct static calls. C# covers
compiler-resolved calls and project relationships; the initial F# slice omits
call edges. Python covers compiler lexical bindings while deliberately omitting
dynamic attribute/type resolution. Rust, C/C++, TypeScript/JavaScript, Java, and
Kotlin consume compiler-native SCIP, and explicitly registered third-party
adapters can add another language without a Go-core change. Exact cross-language
relationships use checked-in
declared/generated bridges. The workspace provider covers Git-visible topology
and the initial CommonMark/GFM plus static Jekyll-shaped content slice.
Finer-grained compiler refresh, build variants, fuzzy search, optional hooks,
MCP, renderer-complete content profiles, and signed package-manager distribution
remain future work.

The complete product contract is [.ai/vision.md](.ai/vision.md). The honest
implementation traceability report is
[.ai/audits/vision-compliance.md](.ai/audits/vision-compliance.md).

MIT licensed.
