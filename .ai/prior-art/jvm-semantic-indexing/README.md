# JVM semantic indexing prior art

Accessed: 2026-08-07

## Decision

Use [`scip-code/scip-java`](https://github.com/scip-code/scip-java) as the
semantic producer behind a thin Weave process adapter. The adapter owns process
isolation, permission checks, bounded output, SCIP normalization, and protocol
framing. It does not parse Java or Kotlin and it does not embed a second JVM
language model.

The first increment advertises Java and Kotlin only. Current upstream calls
itself a Java and Kotlin indexer. Java is automatically supported for Gradle,
Maven, and Bazel; Kotlin is automatically supported only for Gradle and is
explicitly described as less mature. Older Sourcegraph material mentions Scala,
but current `scip-code` documentation does not. Scala therefore needs a separate
producer decision rather than an unsupported compatibility claim.

The adapter's `describe` operation is declarative and does not execute
`scip-java` or Java. The configured/pinned producer version is part of that
contract. Indexing resolves and runs the external producer, then verifies the
actual `index.scip` tool name and version before emitting any facts. This keeps
`weave adapters list`, configuration inspection, ordinary Go builds, and unit
tests independent of a JDK while preserving exact producer provenance at the
semantic boundary.

## Selected producer: scip-code/scip-java

Primary sources:

- [Repository and current support statement](https://github.com/scip-code/scip-java)
- [Getting started and support matrix](https://github.com/scip-code/scip-java/blob/main/docs/getting-started.md)
- [Manual compiler-plugin pipeline](https://github.com/scip-code/scip-java/blob/main/docs/manual-configuration.md)
- [v0.13.1 release](https://github.com/scip-code/scip-java/releases/tag/v0.13.1)
- [SCIP protocol](https://github.com/scip-code/scip)

Why it fits:

- Apache-2.0 and actively maintained under the `scip-code` organization. The
  repository was not archived and had activity on 2026-08-06 when checked.
- Its Java facts come from a javac compiler plugin and its Kotlin facts from a
  Kotlin compiler plugin, rather than syntax-only parsing.
- It already performs build-tool integration, emits the shared SCIP protobuf,
  aggregates per-source shards, and records its exact tool version in SCIP
  metadata.
- Version 0.13.1 was published on 2026-07-02 with one checksum-addressable
  standalone launcher. Maven Central/Coursier and a container image are also
  official distribution routes.
- The normal `index` command accepts an explicit output and private temporary
  directory, which lets the Weave wrapper keep the final interchange file out
  of the repository.
- Version 0.13.1 emits legacy SCIP documents without their newer position
  encoding field. Its [javac visitor](https://github.com/scip-code/scip-java/blob/v0.13.1/scip-javac/src/main/java/org/scip_code/scip_java/javac/ScipVisitor.java)
  and [Kotlin line map](https://github.com/scip-code/scip-java/blob/v0.13.1/scip-kotlinc/src/main/kotlin/org/scip_code/scip_java/kotlinc/LineMap.kt)
  compute columns from JVM/Kotlin string offsets, which are UTF-16 code units.
  The wrapper must supply that producer-specific legacy contract rather than
  guessing from the top-level source-byte encoding.
- The official v0.13.1 launcher also embeds `0.0.0-SNAPSHOT` as its SCIP
  `ToolInfo.version`. The wrapper validates this observed embedded value, then
  normalizes it to the independently pinned distribution version before facts
  and stable IDs are created. Keeping both values explicit avoids treating a
  non-unique snapshot label as durable producer identity.

Important boundary conditions:

- The Coursier-built launcher still requires JDK 17 or newer. The official
  container bundles supported JDKs, at substantially greater download/runtime
  cost. Weave should expose the external executable requirement, not silently
  install a JDK or download a container.
- `scip-java index` invokes a repository build and upstream warns that it may
  clean compilation caches. Gradle or Maven may resolve dependencies, run
  plugins, annotation processors, and generators. The adapter must require
  explicit build-tool, restore, network, and generator grants before invocation.
- The producer currently targets Java 17, 21, and 25. Java 8 and 11 are not in
  the current supported-version table.
- Java/Kotlin cross-repository navigation needs additional build metadata. That
  is orthogonal to Weave's user-authored contextual links.
- Upstream output is exact compiler-derived evidence for the selected build and
  producer version. Weave must not relabel missing or guessed relationships as
  exact.

## Other maintained approaches considered

### Compiler shards and the scip-java aggregator

The upstream [manual configuration
guide](https://github.com/scip-code/scip-java/blob/main/docs/manual-configuration.md)
splits indexing into compiler-plugin emission and aggregation. This is useful
prior art for a future mode that consumes already-produced shards in CI, but it
does not remove the need to configure the compiler plugin. Invoking the official
top-level producer is the smallest correct first adapter.

### Kythe Java indexer

[Kythe](https://kythe.io/docs/kythe-overview.html) has compiler-aware Java
indexing and a durable graph model. Its
[indexing documentation](https://kythe.io/docs/kythe-indexing.html) describes
the extraction/indexing pipeline. Adopting it here would require a Kythe-to-Weave
or Kythe-to-SCIP semantic mapping plus build extraction, substantially more
surface area than importing an existing SCIP index. It remains strong prior art
for corpus identity and compilation-unit provenance.

### Eclipse JDT Language Server

[Eclipse JDT LS](https://github.com/eclipse-jdtls/eclipse.jdt.ls) is a maintained,
compiler-backed Java language server. Its public contract is interactive LSP,
not a complete deterministic repository snapshot. Driving workspace lifecycle,
querying every symbol, and reconstructing a replacement inventory would create
an indexer inside Weave. It is a fallback for editor interaction, not the best
batch producer.

### Kotlin Analysis API and compiler plugins

The [Kotlin Analysis API](https://kotlin.github.io/analysis-api/) and the
[Kotlin compiler-plugin API](https://kotlinlang.org/docs/custom-compiler-plugins.html)
are the native semantic foundations. `scip-java` already adapts the Kotlin
compiler path to SCIP. Building another direct integration would duplicate its
version compatibility and symbol-model work.

### SemanticDB / Metals for Scala

[Scalameta SemanticDB](https://scalameta.org/docs/semanticdb/guide.html) records
compiler-derived symbols and occurrences, and
[Metals](https://scalameta.org/metals/docs/) consumes it for Scala tooling. This
is the leading direction for a later Scala extension. Current `scip-java` no
longer advertises Scala, so Scala should get its own researched adapter or a
maintained SemanticDB-to-SCIP path rather than being smuggled into this adapter.

### JavaParser, OpenRewrite, and tree-sitter

[JavaParser](https://github.com/javaparser/javaparser),
[OpenRewrite](https://github.com/openrewrite/rewrite), and the
[tree-sitter Java grammar](https://github.com/tree-sitter/tree-sitter-java) are
valuable maintained parsers/transformation foundations. Using them as the main
semantic producer would make Weave responsible for classpaths, overload
resolution, generated sources, Kotlin interoperability, and stable symbol
identity. They are appropriate for structural fallbacks whose evidence is
marked inferred, not a replacement for compiler-backed exact facts.

## Implementation consequences

1. The Go wrapper is independently buildable and testable with a fake producer.
2. `describe` reports the pinned contract without probing Java.
3. `index` passes only literal argv to an explicitly resolved executable and
   requires all side-effect permissions before process discovery or execution.
   It disables upstream's Bazel sandbox-command autorun, which otherwise
   reconstructs and executes a diagnostic command through `bash -c`.
4. Producer stdout/stderr are bounded diagnostics; adapter stdout is reserved
   for `weave.adapter/v0` frames.
5. The producer writes `index.scip` beneath a private OS temporary directory.
6. Weave's shared SCIP importer validates paths, ranges, fact limits, metadata,
   and exact evidence. The wrapper additionally rejects producer name,
   distribution-version, or embedded-version drift from the negotiated
   contract.
7. Java and Kotlin fixtures exercise normalization without making the Go suite
   depend on Java. A separate opt-in end-to-end test can run the real producer
   where JDK 17+, Gradle, and dependency access are intentionally provisioned.
