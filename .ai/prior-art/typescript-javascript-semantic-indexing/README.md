# TypeScript and JavaScript semantic indexing prior art

Researched 2026-08-07 from upstream project sources. The implementation choice
is deliberately an adapter around an existing compiler-backed producer, not a
new parser or a partial reimplementation of TypeScript module resolution.

## Decision

Use Sourcegraph's Apache-2.0
[`scip-typescript`](https://github.com/sourcegraph/scip-typescript) as the
semantic producer and keep `weave-typescript` a small `weave.adapter/v0`
subprocess wrapper. Pin the currently published 0.4.0 package and its complete
npm dependency closure. The producer already uses Microsoft's TypeScript
compiler and emits the SCIP interchange Weave imports.

The selected boundary is:

1. Weave sends one bounded JSON index request to `weave-typescript`.
2. The wrapper invokes `scip-typescript` with literal argv and a private output
   file.
3. The existing bounded SCIP importer normalizes the protobuf into Weave facts.
4. The wrapper returns the ordinary bounded NDJSON adapter lifecycle.

No npm install, dependency restore, language parsing, package-manager command,
or repository write occurs during indexing.

## Upstream evidence

- The [upstream README at the reviewed
  revision](https://github.com/sourcegraph/scip-typescript/blob/891eb4293709a6a587bf4468dfa1b45a85182fd9/README.md)
  identifies the project as a SCIP indexer for both TypeScript and JavaScript,
  recommends `scip-typescript` over the older `lsif-node`, documents
  TypeScript-project and JavaScript-project operation, and says its CI supports
  Node 18 and 20.
- The [published 0.4.0 package
  manifest](https://github.com/sourcegraph/scip-typescript/blob/v0.4.0/package.json)
  is Apache-2.0 and depends on `typescript` rather than implementing a parser.
  At research time, 0.4.0 was also the latest npm publication. Its registry
  integrity is pinned in `adapters/typescript/toolchain/package-lock.json`.
- The [upstream command-line
  contract](https://github.com/sourcegraph/scip-typescript/blob/v0.4.0/src/CommandLineOptions.ts)
  accepts an explicit `--cwd`, `--output`, project list, progress control, and
  global-cache control. These are sufficient for a deterministic one-shot
  wrapper without shell interpolation.
- The [upstream project
  indexer](https://github.com/sourcegraph/scip-typescript/blob/v0.4.0/src/ProjectIndexer.ts)
  builds a real TypeScript `Program` and `TypeChecker`, indexes only configured
  source files, and emits paths relative to its working directory. This is the
  compiler-semantic boundary Weave wants.
- The [upstream main
  implementation](https://github.com/sourcegraph/scip-typescript/blob/v0.4.0/src/main.ts)
  writes SCIP to the requested output and records `scip-typescript` plus its
  version in `ToolInfo`. It also shows why the wrapper does not expose package
  manager workspace flags: those paths synchronously execute `yarn` or `pnpm`.
- The same implementation writes an inferred `tsconfig.json` into the project
  when `--infer-tsconfig` is enabled. That conflicts with Weave's read-only
  indexing contract, so `weave-typescript` never passes this flag. A repository
  needs a `tsconfig.json`/`jsconfig.json`, or the caller must select an existing
  repository-contained project explicitly.
- `scip-typescript` 0.4.0 emits the legacy SCIP shape without per-document
  `position_encoding`. Its ranges come from TypeScript's
  `SourceFile.getLineAndCharacterOfPosition`, hence JavaScript-string UTF-16
  code units. The wrapper supplies that producer-specific legacy encoding to
  Weave's importer; it does not guess from SCIP's source-byte encoding.
- The producer does not populate `Document.language`. The wrapper adds only
  extension-derived SCIP language metadata (`javascript`,
  `javascriptreact`, `typescript`, or `typescriptreact`) while leaving all
  semantic facts untouched.

## Safety and correctness boundaries

- The wrapper accepts only producer 0.4.0, uses a private OS temporary
  directory, bounds producer streams and the SCIP index, and validates all
  output through the existing importer before emitting a run.
- `--project` is passed as one literal argument only after canonical path,
  repository containment, file type, size, and symlink checks.
- The wrapper passes `--no-global-caches` to avoid a process-wide memory cache
  and `--no-progress-bar` to keep diagnostics bounded and predictable.
- No `--infer-tsconfig`, `--yarn-workspaces`, `--pnpm-workspaces`, restore, or
  generator path is exposed. TypeScript project references remain available
  through normal compiler configuration.
- Node and the selected semantic producer are subordinate local processes.
  Indexing does not request network or build-tool permission and applies npm
  and Yarn offline environment settings defensively; dependencies must already
  exist on disk.
- Definitions, references, symbol descriptions, and relationships are exact
  output of the selected TypeScript program. SCIP occurrences are not promoted
  to call edges merely because source syntax resembles a call.
- The recommended lock currently resolves TypeScript 5.9.3. The upstream
  producer records only its own 0.4.0 version in SCIP metadata; changing the
  transitive compiler beneath an independently installed 0.4.0 producer is an
  upstream identity limitation. Reproducible installations should use the
  checked-in lock.

## Alternatives considered

### Direct TypeScript compiler API integration

The [official compiler
API](https://github.com/microsoft/TypeScript/wiki/Using-the-Compiler-API) can
construct programs and type checkers, but adopting it directly would make
Weave responsible for symbol identity, project references, module resolution,
JS/JSX behavior, ranges, and SCIP generation already implemented upstream.
Keep it as an escape hatch only if `scip-typescript` becomes unmaintained or
cannot represent a required semantic fact.

### TypeScript language server or LSP navigation harvesting

`tsserver` is compiler-backed, but turning point-in-time editor requests into a
complete deterministic repository inventory would require project discovery,
symbol enumeration, batching, deduplication, and a new interchange contract.
SCIP is the purpose-built batch artifact and already matches Weave's import
model.

### Tree-sitter TypeScript and syntax-only parsers

The maintained
[`tree-sitter-typescript`](https://github.com/tree-sitter/tree-sitter-typescript)
grammars cover TypeScript and TSX syntax and are valuable for structural text
tools. They do not provide TypeScript's resolved types, package/module
resolution, overload identity, project references, or cross-file symbol
relationships. Using them here would recreate a weaker compiler.

### `lsif-node`

The selected producer's own migration guidance supersedes `lsif-node` with
`scip-typescript`. Starting a new adapter on the older LSIF producer would add
another interchange and choose the upstream-deprecated path.

## Upgrade triggers

Revisit the pin and adapter policy when upstream publishes a release that:

- records per-document position encoding;
- records document language and complete semantic toolchain identity;
- officially tests maintained Node releases;
- provides read-only config inference or safe workspace enumeration without
  invoking package managers through a shell; or
- adds semantic facts Weave can preserve without invention.
