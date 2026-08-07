# weave-typescript

`weave-typescript` is a thin compiler-semantic JavaScript, JSX, TypeScript, and
TSX adapter for Weave. It implements `weave.adapter/v0`, invokes Sourcegraph's
Apache-2.0 [`scip-typescript`](https://github.com/sourcegraph/scip-typescript),
and passes its SCIP output through Weave's bounded normalizer. It does not parse
these languages or reproduce TypeScript's type checker in Go.

## Requirements and installation

- Weave's Go toolchain to build the wrapper;
- Node.js and the pinned `scip-typescript` 0.4.0 producer; and
- an existing `tsconfig.json` or `jsconfig.json` describing the files to index.

Upstream 0.4.0 documents Node 18 and 20 as its tested runtimes. The pinned npm
closure is provided for reproducible local and CI installs:

```console
sh adapters/typescript/scripts/install-scip-typescript.sh
go install ./adapters/typescript/cmd/weave-typescript
export WEAVE_SCIP_TYPESCRIPT="$PWD/adapters/typescript/toolchain/node_modules/.bin/scip-typescript"
```

Installation is the only step that contacts the npm registry. `npm ci` verifies
the integrity values in the checked-in lock and disables package lifecycle
scripts. Ordinary indexing runs entirely against files already on disk.

## Use

For a configured project:

```console
weave index --adapter "$(command -v weave-typescript)"
```

`WEAVE_TYPESCRIPT_ADAPTER` selects the wrapper when automatic discovery is
enabled by the host. `WEAVE_SCIP_TYPESCRIPT` selects the producer; the literal
adapter equivalent is
`--adapter-arg=--scip-typescript=/absolute/path/to/scip-typescript`.

The wrapper chooses a root `tsconfig.json`, then a root `jsconfig.json`. Select
a nested repository-contained project or config explicitly when necessary:

```console
weave index \
  --adapter "$(command -v weave-typescript)" \
  --adapter-arg=--project=packages/web/tsconfig.json
```

TypeScript project references are followed by the upstream compiler. Package
manager workspace flags are intentionally not exposed because upstream
implements them by invoking Yarn or pnpm; a solution/config project is the
read-only, compiler-native composition boundary.

For JavaScript and JSX, use an ordinary compiler configuration with `allowJs`
and the desired `jsx` mode. Upstream's `--infer-tsconfig` writes a generated
`tsconfig.json` into the target repository, so this adapter never enables it.
It fails with a concrete configuration error instead of silently changing the
checkout.

## Runtime and evidence contract

The producer receives literal argv equivalent to:

```console
scip-typescript index \
  --cwd /absolute/repository \
  --output /private/temporary/index.scip \
  --no-progress-bar \
  --no-global-caches \
  /absolute/repository/tsconfig.json
```

The output and all temporary state stay outside the repository and are removed
after import. Producer stdout and stderr become bounded adapter diagnostics;
adapter stdout remains protocol-only. The wrapper never runs npm, Yarn, pnpm,
a generator, or a project build, and advertises that build-tool permission is
not required. Defensive npm/Yarn offline settings are present in the child
environment, but the producer itself has no restore path.

Definitions, references, symbol identities, and relationships are exact facts
from the TypeScript program selected by the config. The wrapper does not infer
calls from generic references. `scip-typescript` 0.4.0 emits legacy UTF-16
ranges without the modern per-document encoding field; the adapter supplies
that known producer contract, and Weave converts coordinates to UTF-8 bytes.
The producer also omits document language, so the wrapper annotates only the
unambiguous JS/JSX/TS/TSX language family from each source extension.

Use the checked-in npm lock when reproducible semantic compiler identity
matters: the producer metadata identifies `scip-typescript` 0.4.0, while its
upstream dependency range otherwise permits several TypeScript compiler
versions.

## Test

The offline unit and process suite requires only Go:

```console
go test ./adapters/typescript/...
```

With the pinned real producer installed, run the end-to-end fixture:

```console
go build -o /tmp/weave ./cmd/weave
go build -o /tmp/weave-typescript ./adapters/typescript/cmd/weave-typescript
PATH="$PWD/adapters/typescript/toolchain/node_modules/.bin:$PATH" \
  python3 adapters/typescript/tests/e2e.py /tmp/weave /tmp/weave-typescript
```

The corpus exercises all four source forms, compiler-resolved definitions and
references, complete host protocol validation, UTF-16 range conversion,
literal subprocess arguments, config containment, bounded diagnostics, and the
read-only repository contract.

See the [prior-art record](../../.ai/prior-art/typescript-javascript-semantic-indexing/README.md)
for the alternatives, upstream limitations, and upgrade triggers.
