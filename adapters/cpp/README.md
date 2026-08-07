# weave-cpp

`weave-cpp` is a thin compiler-native C, C++, and CUDA adapter for Weave. It
implements `weave.adapter/v0`, invokes the maintained Apache-2.0
[`scip-clang`](https://github.com/sourcegraph/scip-clang) indexer, and passes its
SCIP output through Weave's existing bounded normalizer. It does not parse C++
or interpret Clang AST JSON itself.

## Requirements

- Weave's Go toolchain to build the small wrapper;
- `scip-clang` v0.4.0 on `PATH` (or an explicit `--scip-clang` adapter argument);
- Git for freshness-safe implicit compilation-database discovery;
- exactly one Git-visible `compile_commands.json` under the repository, unless
  an explicit repository-contained `--compdb` argument is supplied; and
- generated headers already present on disk.

Upstream v0.4.0 binaries support x86-64 Linux and arm64 macOS. The pinned helper
installs either published artifact with checksum verification:

```console
adapters/cpp/scripts/install-scip-clang.sh ~/.local/bin
go install ./adapters/cpp/cmd/weave-cpp
```

Windows and x86-64 macOS currently require building `scip-clang` from source;
this is an upstream distribution limitation, not a parser fallback trigger.

## Use

Generate a compilation database with your existing build system. For CMake:

```console
cmake -S . -B build -DCMAKE_EXPORT_COMPILE_COMMANDS=ON
weave index --adapter "$(command -v weave-cpp)" --allow-build-tool
```

If exactly one `compile_commands.json` is Git-visible and `weave-cpp` plus
`scip-clang` are on `PATH`, ordinary queries refresh C/C++ facts automatically.
`WEAVE_CPP_ADAPTER` and `WEAVE_SCIP_CLANG` select explicit executable paths.
Automatic mode conservatively fingerprints every Git-visible file because
preprocessor and compiler flags can consume files with arbitrary extensions.
Only enable these tools for repositories you trust.

The build-tool grant is intentional. `weave-cpp` does not run CMake, Ninja, a
restore, or a generator, but `scip-clang` consumes compilation commands and may
probe the configured compiler. Generated build directories are commonly
Git-ignored. Use the explicit form for those one-shot imports; implicit
discovery intentionally matches the host's Git-visible freshness inventory and
will not silently cache an ignored database. If several visible databases
exist, select one without a shell or command string:

```console
weave index \
  --adapter "$(command -v weave-cpp)" \
  --adapter-arg=--compdb=build/debug/compile_commands.json \
  --allow-build-tool
```

For a nonstandard indexer location, repeat `--adapter-arg` with
`--scip-clang=/absolute/path/to/scip-clang`. All adapter arguments are passed as
literal argv. The selected database must resolve inside the repository and may
not be a symbolic link.

`weave-cpp` writes the SCIP index and all `scip-clang` temporary/supplementary
outputs beneath a private OS temporary directory and removes it after import.
It does not write `index.scip` into the repository. Compiler diagnostics remain
bounded stderr; stdout is protocol-only.

## Correctness boundary

Facts are exact for the selected compilation database and the recorded
`scip-clang`/Clang version. Different defines, targets, generated headers, or
build variants can produce a different graph. The adapter advertises full
refresh and refuses ambiguous database discovery rather than merging variants.

`scip-clang` v0.4.0 predates SCIP's required per-document range-encoding field.
The wrapper supplies an explicit, producer-specific UTF-8 byte-offset override
because Clang's source columns and `scip-clang`'s range construction use byte
columns. Weave's general SCIP importer continues to reject unspecified range
encodings unless its caller supplies such a known legacy-producer override;
source-file encoding metadata is intentionally not used to guess range units.

`scip-clang` currently produces C, C++, and CUDA facts. Objective-C may be
accepted by parts of Clang but is not advertised until covered by upstream and
our conformance fixtures.

## Test

```console
go test ./adapters/cpp/...
```

The unit suite covers strict protocol handling, permission denial before the
producer runs, deterministic/ambiguous database discovery, path containment,
literal subprocess arguments, bounded frames, and temporary cleanup. The
dedicated Linux CI job downloads the pinned real `scip-clang`, indexes the
genuine C++ fixture, imports it through the adapter, and queries it with Weave.
