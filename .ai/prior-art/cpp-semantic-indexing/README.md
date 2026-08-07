# C and C++ semantic indexing prior art

Weave should consume compiler-produced C-family semantics rather than attempt to
parse C, C++, Objective-C, or CUDA in Go. Preprocessing, conditional compilation,
templates, overload resolution, generated headers, target ABI, and compiler flags
are semantic inputs. A useful index therefore starts with the translation unit's
real compilation command, not only the bytes in a source file.

## Decision

The first C/C++ adapter is a thin `weave.adapter/v0` wrapper around
[`sourcegraph/scip-clang`][scip-clang]. `scip-clang` is Apache-2.0 licensed,
actively maintained, based on Clang 21, accepts the standard JSON compilation
database, and emits language-neutral SCIP. Weave already has a bounded SCIP
protobuf importer and normalization layer. Reusing both projects gives us
compiler-resolved declarations, references, inheritance, macros, includes, and
cross-repository symbols without maintaining a second C++ frontend.

The wrapper is deliberately orchestration rather than a parser:

1. locate one unambiguous Git-visible `compile_commands.json` beneath the
   repository, or use one explicitly supplied as a literal adapter argument;
2. require build-tool permission, because `scip-clang` replays compilation
   configuration and may probe the compiler named by that database;
3. run a fixed `scip-clang` executable with literal argv, repository cwd, bounded
   output, and all generated index/shard/supplementary files in a private
   temporary directory;
4. import `index.scip` through Weave's existing validated SCIP normalizer; and
5. stream the normalized units through the existing adapter lifecycle.

No project build, dependency restore, generator, shell, network request, compiler
plugin, or source-code execution is introduced by the wrapper. Generated headers
must already exist. The adapter advertises full refresh while SCIP documents
remain its atomic replacement units.

## Options evaluated

| Option | Strength | Why it is not the first implementation |
| --- | --- | --- |
| `scip-clang` | Purpose-built precise C/C++/CUDA batch indexer, Clang semantics, compilation database input, SCIP output, Apache-2.0. | Selected. Its binary distribution currently covers x86-64 Linux and arm64 macOS, so other platforms must build it or wait for upstream artifacts. |
| Clang LibTooling | Full C++ AST and the canonical compilation-database APIs. | It would require Weave to design symbol identities, merge translation units, model macros/templates, serialize facts, and track Clang's evolving C++ API—all work `scip-clang` already performs. |
| libclang C API | Intentionally smaller and relatively stable traversal/token API. | Better than a source parser, but intentionally does not expose all information in Clang's C++ AST. We would still own a new semantic indexer and cross-TU merger. |
| clangd background index | Mature incremental index with declarations, references, and relations. | Its on-disk shards and internal C++ APIs are clangd implementation details rather than a stable batch interchange contract. It is valuable future prior art for persistent/incremental operation. |
| Raw `clang -ast-dump=json` | Compiler-produced AST available in ordinary Clang installations. | The JSON dump is a debugging surface, lacks a stable cross-TU symbol/index contract, and would make Weave own AST interpretation and identity. |
| Tree-sitter or a Go C++ parser | Fast syntax trees and broad deployment. | Useful only as an explicitly syntactic fallback. It cannot honestly reproduce configured compiler name lookup, preprocessing, template instantiation, or overload resolution. |

## Compilation database and correctness boundary

Clang's [JSON Compilation Database specification][compdb] exists because AST
tools require the complete command for each translation unit. `arguments` is
preferred over shell-escaped `command`; every command records its working
directory and source file. CMake, Meson, Bazel extractors, Bear, and compiler
`-MJ` fragments can produce this format.

The initial adapter refuses zero or multiple discovered databases instead of
guessing between build variants. Implicit discovery only considers the same
tracked or non-ignored files as host freshness; an ignored build database cannot
silently make an automatic index stale. An explicit repository-contained path
can be provided for a one-shot import with `--adapter-arg=--compdb=...`. The
database and every source/header surface affecting it must participate in host
freshness before that explicit mode becomes automatic. A later host profile
can model several named build variants, but silently combining Debug, Release,
host, cross-compiled, or CUDA databases would make symbol truth incoherent.

SCIP symbol identities and occurrences are `Exact` with respect to the recorded
`scip-clang`/Clang version and compilation database. That does not claim that an
inactive preprocessor branch, another build variant, a runtime virtual dispatch,
or generated code absent from disk has the same answer. Weave records the
producer version and compilation inputs so those facts can be replaced when the
environment changes.

`scip-clang` v0.4.0 emits the older index shape: it declares UTF-8 source files
in metadata but leaves the newer per-document `position_encoding` unspecified.
Those fields are not interchangeable—the SCIP schema explicitly says source
encoding is unrelated to range character units. The wrapper therefore supplies
a producer-specific UTF-8 byte-offset compatibility override, matching Clang's
byte-based source columns and SCIP's guidance for C++ indexers. The shared
importer still rejects unspecified encodings by default; it never guesses from
metadata for an arbitrary producer.

## Distribution and operational constraints

As of `scip-clang` v0.4.0, upstream publishes an x86-64 Linux binary and an arm64
macOS binary. Windows and x86-64 macOS have no release artifact. Weave should pin
the release and checksum in CI/install helpers while keeping `scip-clang` an
explicit external dependency rather than vendoring a roughly 70–140 MiB
compiler binary into the Go CLI.

The upstream system guidance is material: indexing can need about 2 GiB RAM per
worker and temporary/shared-memory space per translation unit. The wrapper caps
parallel workers conservatively and uses host time/output/fact bounds. Large
repository performance should be measured before adding persistent workers or
changed-unit claims.

## Follow-on opportunities

- Generalize the thin “run a SCIP producer, import its result, speak the adapter
  protocol” bridge for other maintained producers such as rust-analyzer,
  scip-java, scip-typescript, scip-python, and scip-ruby.
- Add named compilation-database variants and include their content plus relevant
  generated headers in freshness fingerprints.
- Investigate clangd's background-index scheduling and shard invalidation once
  Weave's v1 adapter contract can negotiate persistent or changed-unit workers.
- Preserve compiler diagnostics as structured bounded diagnostics without
  treating recoverable diagnostics as precise semantic facts.

## Primary sources

- [`sourcegraph/scip-clang` README and operational guidance][scip-clang]
- [`scip-clang` v0.4.0 release artifacts][scip-clang-release]
- [`scip-clang` Apache-2.0 license][scip-clang-license]
- [SCIP protocol and maintained indexer list][scip]
- [SCIP change specifying range-offset encodings][scip-position-encoding]
- [Clang JSON Compilation Database specification][compdb]
- [Clang LibTooling documentation][libtooling]
- [Clang tooling interface comparison][tooling]
- [libclang stable C interface tutorial][libclang]
- [Clang AST introduction and AST dump][clang-ast]
- [clangd background index design][clangd-index]

[scip-clang]: https://github.com/sourcegraph/scip-clang
[scip-clang-release]: https://github.com/sourcegraph/scip-clang/releases/tag/v0.4.0
[scip-clang-license]: https://github.com/sourcegraph/scip-clang/blob/main/LICENSE
[scip]: https://github.com/scip-code/scip
[scip-position-encoding]: https://github.com/scip-code/scip/commit/19f7d9eaa678e5decd19fe0ec7a6133c5163dbf5
[compdb]: https://clang.llvm.org/docs/JSONCompilationDatabase.html
[libtooling]: https://clang.llvm.org/docs/LibTooling.html
[tooling]: https://clang.llvm.org/docs/Tooling.html
[libclang]: https://clang.llvm.org/docs/LibClang.html
[clang-ast]: https://clang.llvm.org/docs/IntroductionToTheClangAST.html
[clangd-index]: https://clangd.llvm.org/design/indexing
