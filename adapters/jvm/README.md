# weave-jvm

`weave-jvm` is a thin Java and Kotlin adapter for Weave. It implements
`weave.adapter/v0`, invokes the maintained Apache-2.0
[`scip-code/scip-java`](https://github.com/scip-code/scip-java) compiler-backed
indexer, and imports the resulting SCIP protobuf through Weave's shared bounded
normalizer. It does not contain a Java parser, Kotlin parser, symbol solver, or
language server client.

Scala is intentionally not advertised. Historical versions of `scip-java`
supported it, but the current upstream project claims Java and Kotlin only.
Scala needs its own maintained SemanticDB/compiler-backed producer decision.

## Install

Build the small Go protocol wrapper with the normal Weave toolchain:

```console
go install ./adapters/jvm/cmd/weave-jvm
```

Indexing additionally requires `scip-java` v0.13.1 and JDK 17 or newer. The
helper downloads the checksum-verified upstream launcher but deliberately does
not install Java or modify `PATH`:

```console
adapters/jvm/scripts/install-scip-java.sh ~/.local/bin
```

The current upstream release asset is a POSIX launcher. On Windows, use the
Maven Central/Coursier route or a compatible container shim with a JDK rather
than this shell installer; the Go protocol wrapper itself remains portable.

Alternatively, use Coursier, Maven Central, or the official upstream container.
Container mounting and path translation are deployment policy, so the wrapper
accepts an executable path rather than attempting to construct a privileged
container command. A trusted shim can provide that boundary when needed.

Configuration inspection does not require any of these runtime dependencies.
`weave-jvm describe --protocol weave.adapter/v0` is declarative: it neither
finds nor runs `scip-java`, Java, Gradle, Maven, or Bazel.

## Use

Run the adapter explicitly from a Gradle Java/Kotlin project or a supported Java
Maven/Bazel project:

```console
weave index \
  --adapter "$(command -v weave-jvm)" \
  --allow-build-tool \
  --allow-restore \
  --allow-network \
  --allow-generators
```

The broad grants are intentional. `scip-java index` integrates with an
untrusted repository build. Upstream warns that it may clean compilation caches;
the build can resolve dependencies and execute plugins, annotation processors,
or generators. If any grant is absent, `weave-jvm` fails before it resolves or
runs the producer. It never downloads Java or `scip-java` on the user's behalf.

Select an upstream executable, expected version, or build tool with literal
adapter arguments:

```console
weave index \
  --adapter "$(command -v weave-jvm)" \
  --adapter-arg=--scip-java=/opt/scip-java/bin/scip-java \
  --adapter-arg=--producer-version=0.13.1 \
  --adapter-arg=--build-tool=gradle \
  --allow-build-tool --allow-restore --allow-network --allow-generators
```

`WEAVE_SCIP_JAVA` and `WEAVE_SCIP_JAVA_VERSION` are the equivalent producer
settings. Supported build-tool selectors are `auto`, `gradle`, `maven`, and
`bazel`. Omitting the selector lets upstream perform its normal detection.

### Explicit automatic freshness

JVM builds cannot safely receive broad permissions merely because a binary was
found on `PATH`. A user-controlled adapter registry can make that trust decision
explicit and declare the repository inputs that invalidate the full refresh:

```json
{
  "schema": "weave.adapters/v1",
  "adapters": [{
    "name": "scip:scip-java",
    "command": ["weave-jvm"],
    "inputs": {
      "extensions": [".java", ".kt", ".kts", ".gradle", ".xml", ".properties", ".toml", ".bzl"],
      "filenames": ["pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts", "gradlew", "module.bazel", "workspace", "workspace.bazel", "build", "build.bazel", "scip-java.json"]
    },
    "permissions": {
      "build_tool": true,
      "restore": true,
      "network": true,
      "run_generators": true
    },
    "timeout": "30m"
  }]
}
```

Select it with `WEAVE_ADAPTER_CONFIG`. The input list is deliberately explicit:
extend it for custom build logic, version catalogs, lock files, or generated
configuration used by the target repository. Missing a semantic input can make
automatic results stale, so prefer the explicit one-shot command until the
declaration conservatively covers the build.

The adapter invokes the producer with literal argv, never a shell command:

```text
scip-java index \
  --cwd=<canonical repository root> \
  --output=<private temporary directory>/index.scip \
  --temporary-directory=<private temporary directory>/work \
  --no-bazel-autorun-sandbox-command
```

The optional `--build-tool` selector is appended. Producer stdout and stderr are
bounded and routed to adapter diagnostics; stdout from `weave-jvm` remains
protocol-only. The final SCIP file and producer temporary directory are removed
after a successful or failed import. The repository's own build outputs and
caches remain under build-tool control. The Bazel sandbox-command autorun is
disabled because upstream implements that diagnostic helper through `bash -c`;
the original error remains available for intentional manual reproduction.

## Correctness boundary

The wrapper advertises the pinned producer version without executing it, then
requires the imported SCIP metadata to be exactly `scip-java` at that version.
Use `--producer-version` only when intentionally selecting another upstream
release. A mismatch emits no partial graph facts.

`scip-java` 0.13.1 emits the legacy SCIP document shape without a per-document
position encoding. Its javac and Kotlin compiler integrations calculate columns
from JVM/Kotlin string offsets, so the wrapper supplies the known UTF-16
code-unit contract and Weave converts those ranges to byte positions. It still
validates any explicit encoding, bounds the protobuf and fact inventory,
validates repository-contained document paths, and preserves
compiler-derived relationships as exact evidence. Missing relationships are not
invented. Results represent the selected build variant, classpath, generated
sources, and producer release.

Current upstream support is:

- Java with Gradle, Maven, or Bazel automatic integration;
- Kotlin with Gradle automatic integration, marked less mature upstream; and
- Java 17, 21, and 25 in the current version matrix.

## Test

The ordinary suite needs only Go:

```console
go test ./adapters/jvm/...
```

It uses genuine Java and Kotlin sources plus a fake process that emits a
representative `scip-java` protobuf. Tests cover declarative discovery without
Java, strict protocol requests, permission denial before producer discovery,
literal argv, private cleanup, bounded diagnostics/frames, exact Java/Kotlin
facts, and producer metadata mismatch. No syntax fixture is treated as proof of
semantic support by itself; the semantic model comes from upstream SCIP.

An opt-in smoke test runs the real producer when its intentionally heavy
runtime is provisioned:

```console
go build -o /tmp/weave ./cmd/weave
go build -o /tmp/weave-jvm ./adapters/jvm/cmd/weave-jvm
python3 adapters/jvm/tests/e2e.py /tmp/weave /tmp/weave-jvm
```
