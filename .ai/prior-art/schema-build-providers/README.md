# Schema and build provider prior art

Research date: 2026-08-07. This record precedes the built-in source-only
schema/build increment. Version observations came from the Go proxy and the
upstream repositories on that date. Primary links are retained so a future
upgrade can re-check licenses, maintenance, and parser behavior instead of
relying on this snapshot.

## Selection constraints

The provider must be deterministic from bounded Git-visible regular files. It
must not run generators, builds, package managers, template evaluators,
Terraform initialization, database connections, repository code, or network
resolution. A parsed declaration is `Declared`; syntax-only navigation is
`Syntactic`; `Generated` requires an explicit source/output declaration. None
of these parsers establishes compiler `Exact` evidence.

One malformed cross-file schema category is omitted atomically while
independent categories remain usable. Each category is one conservative
invalidation unit. Independently parseable build manifests degrade per file so
a deliberately malformed fixture cannot hide valid projects; a build category
with no valid manifest is omitted. A corpus hash lets freshness reuse an
unchanged unit without invoking its parser. This trades finer incremental
parsing for no stale cross-file resolution and a small, auditable first
implementation.

## Protobuf

Chosen: [Buf protocompile](https://github.com/bufbuild/protocompile), pinned at
v0.14.1, Apache-2.0 ([license](https://github.com/bufbuild/protocompile/blob/main/LICENSE)).
It is the pure-Go parsing and linking engine used by Buf, emits standard
protobuf descriptors and source information, and supports standard well-known
imports without requiring `protoc`. The repository remains active; the pinned
release is older than some other dependencies but is the public release used by
the current Buf ecosystem rather than an invented grammar.

We compile only an in-memory map of Git-visible `.proto` bytes, plus
protocompile's embedded standard imports, with parallelism capped at two.
Repository imports resolve to local descriptor identities; well-known or other
non-local descriptors remain open endpoints. Messages, nested messages, enums,
values, services, RPCs, fields, imports, and linked field/RPC types come from
descriptors and source locations. Synthetic map-entry descriptors are not
published as user declarations.

Limitations: Buf module/dependency configuration is not evaluated and no BSR or
network dependency is loaded. A missing non-standard imported schema makes the
Protobuf category incomplete rather than producing a partially linked graph.
Generator options such as `go_package` do not prove a generated output path and
therefore do not create `Generates` edges.

## OpenAPI

Chosen model: [getkin/kin-openapi](https://github.com/getkin/kin-openapi),
pinned at v0.146.0, MIT
([license](https://github.com/getkin/kin-openapi/blob/master/LICENSE)); source
locations and reference discovery use the maintained
[go-yaml v3 fork](https://github.com/yaml/go-yaml) already used by Weave.
kin-openapi has current releases and active OpenAPI 3.0/3.1/3.2 work. Its loader
also deliberately supports external URI resolution, which is useful in servers
but is the wrong automatic-index trust boundary.

We decode root mappings into kin-openapi's OpenAPI 3 model without invoking its
URI loader, then retain YAML/JSON nodes for exact local source anchors. Local
`$ref` targets resolve only through normalized repository-relative paths that
are already in the bounded Git-visible OpenAPI corpus. Remote, missing,
absolute, backslash, malformed, or root-escaping references stay stable open
endpoints with diagnostics; they never trigger a read or make an otherwise
valid root disappear. Operations, paths, component schemas, properties, and
operation/schema reference edges are indexed.

Limitations: the first slice supports OpenAPI 3 roots, not Swagger/OpenAPI 2
conversion. Component fragment filenames must be identifiable OpenAPI files
(`openapi*.yaml|yml|json` or `*.openapi.*`) to enter the automatic corpus.
Full JSON Schema vocabulary analysis, discriminator semantics, callbacks, and
security-flow resolution remain future work.

## GraphQL

Chosen: [vektah/gqlparser v2](https://github.com/vektah/gqlparser), pinned at
v2.5.36, MIT
([license](https://github.com/vektah/gqlparser/blob/master/LICENSE)). It is a Go
port of the reference graphql-js parser, has current v2 releases, source
positions, bounded token parsing, schema validation, and executable-document
validation.

We parse and validate the complete local SDL corpus, then parse executable
documents with a token bound. Types, interfaces, unions, inputs, enums,
scalars, fields, arguments, enum values, operations, fragments, type
relationships, and fragment spreads are retained. Query fields are linked to
local schema fields only when gqlparser validation succeeds; otherwise the
operation/fragment syntax remains available and a diagnostic explains that
field relationships were not promoted.

Limitations: Weave does not fetch remote schemas, apply server-specific schema
transforms, or execute queries. Directives are parsed by upstream but are not
yet first-class symbols. Executable documents without a local schema retain
syntactic structure rather than guessed field resolution.

## SQL migrations

Chosen dialect: [Bytebase Omni](https://github.com/bytebase/omni), pinned to
commit `8e82223f7635` (2026-07-30), MIT
([license](https://github.com/bytebase/omni/blob/main/LICENSE)). Omni is a
current pure-Go SQL infrastructure project with full ASTs and byte source
locations. Its upstream status table marks PostgreSQL complete while MySQL,
SQL Server, and Oracle parsers are still in progress. Bytebase itself adopted
the Omni parsers in current releases. This is stronger evidence than selecting
an abandoned generic grammar, but the lack of tagged Omni releases is an
upgrade risk that must remain visible.

The automatic provider deliberately models PostgreSQL migration streams only:
Git-visible `.sql` beneath `migration`, `migrations`, or `migrate` directories,
plus conventional versioned `V*__*.sql`/numeric `*__*.sql` files. It models
deterministic filename order per directory as `Inferred` evidence, plus schemas,
tables, columns, foreign keys, inheritance, views, indexes, sequences, enums,
functions, and supported dependencies. Parsed-but-unmodeled PostgreSQL
statements produce bounded diagnostics. A parser failure (including an
unsupported dialect) omits only the SQL category rather than inventing DDL
meaning.

Limitations: no database is contacted and no migration engine's templating,
version comparator, repeatable ordering, transaction rules, or dialect
configuration is executed. Filename-order edges are navigation hints rather
than a claim about a selected migration engine.
MySQL, SQLite, SQL Server, Oracle, and vendor migration DSLs require separate
maintained dialect providers or adapters before receiving semantic facts.

## Terraform / HCL

Chosen: [HashiCorp HCL v2](https://github.com/hashicorp/hcl), pinned at
v2.24.0, MPL-2.0
([license](https://github.com/hashicorp/hcl/blob/main/LICENSE)). HCL is the
authoritative parser library used by Terraform and remains maintained. Its
native-syntax AST exposes block, attribute, expression, traversal, and byte
range information without evaluating Terraform.
[go-cty](https://github.com/zclconf/go-cty), pinned at v1.16.3, MIT
([license](https://github.com/zclconf/go-cty/blob/main/LICENSE)), is HCL's value
model and is used only to recognize already-literal strings.

We parse `.tf` only and model module directories, resources, data sources,
variables, outputs, locals, module calls, provider configurations, required
provider sources, static local module sources, ordinary traversals, and
`depends_on`. Literal extraction uses HCL expression values only when evaluation
requires no variables. Meta roots (`each`, `count`, `path`, `terraform`,
`self`) are not fabricated as resource addresses.

Limitations: `.tf.json`, dynamic blocks, expression evaluation, provider
schemas, module registry resolution, state, plans, and implicit provider
semantics are absent. No `terraform init`, provider plugin, credential, or
network path is reachable.

## Declarative build manifests

The supported set represents Weave's existing language ecosystems without
claiming universal build support:

- Go `go.mod`: official
  [`golang.org/x/mod/modfile`](https://pkg.go.dev/golang.org/x/mod/modfile),
  BSD-3-Clause, maintained with Go. It parses the actual Go module grammar;
  Weave reads module, require, and explicit local replace declarations.
- Rust `Cargo.toml`: [pelletier/go-toml v2](https://github.com/pelletier/go-toml),
  pinned at v2.4.3, MIT. It is a current TOML 1.0 parser. Fields follow the
  official [Cargo manifest reference](https://doc.rust-lang.org/cargo/reference/manifest.html):
  packages/workspaces, dependency tables and paths, and explicit lib/bin
  targets.
- TypeScript/JavaScript `package.json`: Go's maintained standard-library
  [`encoding/json`](https://pkg.go.dev/encoding/json), BSD-3-Clause. We model
  package identity, dependency classes, file dependencies, workspace arrays or
  `{packages: [...]}`, and explicitly named scripts. The npm documentation is
  the format reference: [package.json](https://docs.npmjs.com/cli/v11/configuring-npm/package-json).
- JVM Maven `pom.xml` and .NET MSBuild `*.csproj|fsproj|vbproj`: Go's maintained
  standard-library [`encoding/xml`](https://pkg.go.dev/encoding/xml),
  BSD-3-Clause, over the declarative subsets documented by
  [Maven POM](https://maven.apache.org/pom.html) and
  [MSBuild project files](https://learn.microsoft.com/visualstudio/msbuild/msbuild-project-file-schema-reference).
  Maven modules/parents/dependencies and MSBuild project/package references and
  explicit targets are modeled. Only MSBuild `Compile Include|Update` with
  `AutoGen=true` and `DependentUpon` proves a `Generated` source/output edge.

Local dependency resolution requires one exact, contained manifest path and an
indexed project at that path. Names, version ranges, SDK targets, registry
packages, and globbed workspaces remain stable open endpoints. No script,
target, plugin, SDK import, lifecycle, restore, or build is executed.

Gradle and CMake are deliberately omitted: their common files are executable
DSLs whose accurate graph depends on evaluation. A future provider should use
a maintained safe model/export or an explicitly permissioned adapter, not a
project-owned regex grammar. Bazel, Pants, Meson, Ninja, lockfiles, and CI
workflow graphs are also outside this first high-value set.

## Rejected shortcuts

- One giant hand-written parser or regex grammar across all formats.
- Running `protoc`, Terraform, Maven, Gradle, Cargo, npm, MSBuild, or generators
  during automatic freshness.
- Letting kin-openapi's default URI loader read files or HTTP references.
- Treating all `.sql` as one portable dialect.
- Promoting parser success to compiler `Exact` evidence.
- Writing discovered relationships into `.weave/bridges.json`; automatic facts
  use the same `relationship.Builder` contract but remain provider-owned,
  rebuildable graph data.
