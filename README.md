# Weave

Weave is a local-first, Git-aware semantic index for the knowledge encoded in a
workspace. It combines compiler-backed facts with files, structured documents,
sections, links, assets, routes, and metadata; keeps derived state outside
commits; and exposes bounded deterministic CLI queries for people and coding
agents. There is no model, hosted service, or required daemon in the indexing
path.

> **Status:** early alpha. Native Go indexing and the query/storage lifecycle
> are usable. Installed C#/F#, Python, Rust, and C/C++ adapters participate in
> query-driven freshness; same-version release companions are configured but
> have not been exercised by a tag, and external adapters currently advertise
> full rather than incremental refreshes. Output schemas are
> versioned, but compatibility before the first tagged release is not promised.

## Install

Build the current checkout with Go 1.26.5 or newer:

```sh
go install github.com/TheFellow/weave/cmd/weave@latest
weave version
```

Tagged releases publish checksummed archives for macOS, Linux, and Windows on
amd64 and arm64. Until the first tag exists, `go install` or `go build -o weave
./cmd/weave` is the supported path.

## Go quickstart

From a Git repository containing one or more Go modules:

```sh
weave index
weave status
weave symbols Handle --limit 20
weave definition Handle
weave references Handle --json
weave callers Handle
weave dependencies github.com/example/project/package
weave path SymbolA SymbolB --kind calls --max-depth 8
weave impact Handle --limit 50
weave impact --file internal/service.go --limit 100
weave impact --package github.com/example/project/internal/service
weave impact --git-diff origin/main --json
weave graph Handle --kind calls --kind implements --output handle.dot
```

Every database-backed query performs a cheap Git freshness check first. A
tracked, untracked, renamed, deleted, branch, worktree, build-context, or
provider change refreshes the affected Go compilation units before the query
runs. Refresh notices go to stderr; results go to stdout. Empty text results are
silent. `--json` emits the `weave.query/v1` envelope and deterministic ordering.
Locations are repository-relative, one-based in text, and zero-based UTF-8 byte
coordinates in JSON facts.

`dependencies` returns direct `depends-on` and `imports` edges. Use `path` with
`--kind depends-on` for a bounded transitive route. Every edge includes its
provider and evidence class in JSON/export output.

## Graphviz DOT

`weave graph` renders a bounded neighborhood around any resolvable symbol,
package, file, document, section, route, or other indexed resource:

```sh
weave graph AuthService --kind calls --kind implements > auth.dot
weave graph README.md --direction outgoing --max-depth 3 --output docs.dot
weave graph Handle --scope catalog --repo github.com/example/service --json
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

Exact relationships that compilers cannot establish can be checked into
`.weave/bridges.json`. These declared/generated `depends-on`, `documents`, and
`generates` edges participate in the same local and federated queries and
architecture rules. See [declared semantic bridges](docs/declared-bridges.md).

Impact roots may be a symbol, repeated repository-relative `--file` and
`--package` values, or `--git-diff REVISION` (which compares that revision to
the current working tree). File roots use indexed definitions and references;
package roots use compiler-emitted package ownership. Output is bounded and
deterministic. An affected-tests projection is emitted only for explicit
`tests` edges or compiler-indexed Go test declarations—Weave does not guess
build targets from directory names.

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

## Derived data and recovery

Per-worktree state lives at the Git-resolved `git rev-parse --git-path weave`
location (normally `.git/weave`). The cross-repository catalog uses the
platform user data directory. Neither belongs in source control.

```sh
weave verify          # logical graph checks; warnings are non-fatal
weave export --json   # deterministic diagnostic export
weave gc              # compact a closed local database
rm -rf "$(git rev-parse --git-path weave)"  # discard derived state
weave index           # deterministic rebuild
```

Schema mismatch, checksum failure, and invalid physical storage are reported as
rebuildable derived-state errors. Back up source, not the index.

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

## Scope and roadmap

The native Go provider covers typed declarations/references, imports,
dependencies, interfaces/implementations, and direct static calls. C# covers
compiler-resolved calls and project relationships; the initial F# slice omits
call edges. Python covers compiler lexical bindings while deliberately omitting
dynamic attribute/type resolution. Rust and C/C++ consume compiler-native SCIP,
and explicitly registered third-party adapters can add another language without
a Go-core change. Exact cross-language relationships use checked-in
declared/generated bridges. The workspace provider covers Git-visible topology
and the initial CommonMark/GFM plus static Jekyll-shaped content slice.
Finer-grained compiler refresh, build variants, fuzzy search, hooks/watch mode,
MCP, renderer-complete content profiles, and signed package-manager distribution
remain future work.

The complete product contract is [.ai/vision.md](.ai/vision.md). The honest
implementation traceability report is
[.ai/audits/vision-compliance.md](.ai/audits/vision-compliance.md).

MIT licensed.
