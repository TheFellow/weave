# CI caching and runner conservation

Weave's CI should spend runner time validating behavior, not repeatedly fetching
unchanged toolchains or completing obsolete workflow runs.

## Prior art

- GitHub's dependency-cache guidance recommends the ecosystem `setup-*` actions
  where available, lockfile-derived keys, and trusted/default-branch cache writes.
  Cache entries are immutable and subject to repository eviction limits, so a
  small number of stable dependency caches is preferable to caching arbitrary
  build output.
  <https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching>
- `actions/setup-dotnet` natively caches NuGet's global package directory when
  supplied committed `packages.lock.json` files. It recommends a repository-local
  `NUGET_PACKAGES` path to avoid caching the runner's large preinstalled package
  set. <https://github.com/actions/setup-dotnet#caching-nuget-packages>
- `Swatinem/rust-cache` keys Cargo dependencies by the Rust toolchain and Cargo
  manifests, removes workspace and incremental artifacts, and supports stable
  keys shared by multiple jobs. <https://github.com/Swatinem/rust-cache>
- `gradle/actions/setup-gradle` caches dependency and transformed-artifact state.
  Its `basic` provider is the fully open-source implementation over
  `actions/cache`; Weave selects that provider deliberately.
  <https://github.com/gradle/actions/blob/main/docs/setup-gradle.md>
- GitHub workflow concurrency groups can cancel superseded runs. For Weave,
  workflow plus PR/ref is the right boundary: unrelated PRs proceed independently,
  while a newer commit stops consuming runners for an obsolete result.
  <https://docs.github.com/en/actions/using-jobs/using-concurrency>

## Applied policy

- Keep path filters so unrelated adapter workflows do not start.
- Cancel superseded runs per workflow and PR/ref.
- Use native Go, pip, npm, NuGet, and Gradle dependency caches.
- Cache the large, checksum-pinned SCIP producer binaries by OS, architecture,
  and installer-script digest.
- Cache Cargo dependencies and dependency build artifacts, but not Weave's own
  workspace outputs or failed builds.
- Write Rust and Gradle caches from `main`; feature/PR runs primarily restore.
- Never cache credentials, mutable user state, or Weave's authoritative source
  declarations. The semantic index cache remains derived and content-keyed.
