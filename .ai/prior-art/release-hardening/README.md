# Release and operational hardening prior art

Research date: 2026-08-06.

## Adopted

- [Go build information](https://pkg.go.dev/runtime/debug#ReadBuildInfo) embeds
  module and VCS settings in ordinary Go binaries. Weave reads it for local and
  `go install` builds, while release builds inject the tag, commit, and date.
- [GoReleaser's Go builder](https://goreleaser.com/customization/builds/builders/go/)
  already supplies a cross-platform build matrix, templated linker flags, and
  reproducible archive conventions. We use the OSS edition rather than writing
  a bespoke release script.
- [GoReleaser's GitHub Actions integration](https://goreleaser.com/customization/ci/actions/)
  publishes from tags with the repository token. The release job has only
  `contents: write`; normal test/index workflows remain read-only except for
  their documented SARIF permission.
- [`go install package@version`](https://go.dev/ref/mod#go-install) is the
  source-based fallback and does not mutate the caller's module.
- GitHub workflow artifacts are explicitly ephemeral build outputs, matching
  Weave's disposable-index model; see [GitHub's artifact
  documentation](https://docs.github.com/en/actions/concepts/workflows-and-actions/workflow-artifacts).

## Deliberate boundaries

- Release archives contain the core Go executable, README, and license. The
  .NET adapter is not silently bundled: it has a .NET runtime/toolchain boundary
  and is still built from source while its packaging contract stabilizes.
- Homebrew, WinGet, Scoop, package signing, SBOM generation, and provenance are
  valuable follow-ups after the first tagged prerelease proves the archive
  shape. Checksummed archives are the smallest portable starting point.
- Database migrations are not invented for schema version 1. A mismatch or
  physical corruption produces rebuild guidance because all index data is
  derived. A real migration framework should begin when a compatible schema 2
  exists and has a fixture representing schema 1.

## Security and recovery precedent already in use

The adapter boundary follows the same bounded framed-input principles used by
compiler protocols: literal argument arrays, strict JSON, byte/frame/fact
limits, timeouts, and opt-in build permissions. bbolt/bstore provides
transactions and checksum errors; Weave classifies corrupt derived state and
directs the operator to delete and deterministically rebuild it.
