# Release-readiness prior art

Research snapshot: 2026-08-07. Versions are pinned so that a future maintainer
can distinguish an intentional toolchain change from ambient `latest` drift.
Only primary project and platform documentation is used below.

## Decision summary

| Concern | Adopt now | Defer |
| --- | --- | --- |
| Go release construction | GoReleaser OSS 2.17.1 through `goreleaser-action` 7.2.3 | Package-manager repositories and installers |
| Targets | Native `CGO_ENABLED=0` binaries for macOS, Linux, and Windows on amd64 and arm64 | Other architectures until demand is measured |
| Integrity | SHA-256 over every published archive, SBOM, and companion package | A second checksum algorithm |
| Inventory | Syft 1.50.0 SPDX JSON for every GoReleaser archive | Artifact-specific SBOMs for the separately built .NET and Python companions |
| Provenance | GitHub `actions/attest` 4.2.2 keyless build provenance for checksum-listed release subjects | A separate self-hosted transparency/signing service |
| Platform signing | Clearly documented unsigned prereleases | Apple Developer ID/notarization and Windows code signing until credentials, cost, and audience justify them |
| Publication safety | Build a draft release, validate its exact local artifacts, attest them, then make the draft visible | GoReleaser Pro's prepare/publish split |

## GoReleaser

Pinned components:

- [GoReleaser 2.17.1](https://github.com/goreleaser/goreleaser/releases/tag/v2.17.1)
- [`goreleaser-action` 7.2.3](https://github.com/goreleaser/goreleaser-action/releases/tag/v7.2.3), pinned in workflows to commit `f06c13b6b1a9625abc9e6e439d9c05a8f2190e94`
- [GoReleaser snapshot and non-publishing modes](https://goreleaser.com/getting-started/quick-start/)
- [archive metadata controls](https://goreleaser.com/customization/package/archives/)
- [reproducible Go build guidance](https://goreleaser.com/customization/builds/builders/go/)
- [artifact catalog](https://goreleaser.com/customization/general/artifacts/)
- [checksum extra files](https://goreleaser.com/customization/package/checksum/)

GoReleaser already models the build matrix, archive composition, checksums,
SBOM artifact relationships, release upload, and build metadata. Replacing it
with repository-owned packaging code would add platform edge cases without
adding a Weave capability.

Adaptations for Weave:

- Pin both the action and the GoReleaser binary rather than accepting a moving
  `~> v2` tool during a tag build.
- Keep `CGO_ENABLED=0`, `-trimpath`, and the existing six OS/architecture
  targets. Use commit time, rather than invocation time, in binaries and archive
  entries so repeated construction of the same tag is inspectable.
- Validate a snapshot on ordinary pushes and pull requests. The validator reads
  GoReleaser's `artifacts.json`, opens every archive without extraction, checks
  its exact allowlisted contents and safe paths, validates executable modes and
  SBOM shape, and recomputes the published checksums.
- Include prebuilt .NET and Python companions in GoReleaser's checksum through
  `checksum.extra_files`; they are already uploaded through
  `release.extra_files`.

GoReleaser OSS cannot separate prepare and publish; that facility is documented
as a [GoReleaser Pro capability](https://goreleaser.com/pro/). We therefore
adapt the ordinary SCM release to remain a draft until the workflow validates
and attests the exact locally produced files. A failed validation leaves a
recoverable draft instead of a public release.

## SBOM generation

Pinned components:

- [Syft 1.50.0](https://github.com/anchore/syft/releases/tag/v1.50.0)
- [`anchore/sbom-action` 0.24.0](https://github.com/anchore/sbom-action/releases/tag/v0.24.0), pinned in workflows to commit `e22c389904149dbc22b58101806040fa8d37a610`
- [GoReleaser SBOM pipeline](https://goreleaser.com/customization/sbom/)
- [Syft supported formats and targets](https://github.com/anchore/syft#readme)

GoReleaser delegates archive inventory to Syft and can emit SPDX or CycloneDX.
Weave adopts one SPDX JSON document per archive because it is standardized,
machine-readable, and accepted by GitHub's SBOM attestation flow. Syft is
downloaded at an exact version; it is not silently installed by product code.

An archive SBOM records what the user actually downloads, including bundled
license/readme material and all four Go adapter wrappers in the companion
archive. The NuGet package and Python wheel already carry ecosystem metadata,
but they are external files from GoReleaser's perspective. Separate SBOMs for
those artifacts are deferred until their packaging is integrated into a single
artifact pipeline or a measured consumer needs them.

## Checksums and build provenance

Pinned components:

- [`actions/attest` 4.2.2](https://github.com/actions/attest/releases/tag/v4.2.2), pinned in workflows to commit `1e69f48acb82d1966a394da916b4c1698aa569d6`
- [GitHub artifact-attestation documentation](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations)
- [`actions/attest` checksum subject input](https://github.com/actions/attest#identify-subjects-with-checksums-file)
- [GitHub CLI attestation verification](https://cli.github.com/manual/gh_attestation_verify)

SHA-256 answers “are these bytes the bytes named by the release?” but a checksum
downloaded from the same compromised location does not independently establish
who built them. GitHub artifact attestations bind artifact digests to the
workflow identity using an ephemeral OIDC/Sigstore certificate and publish the
attestation through GitHub's API. The action needs only job-scoped
`id-token: write`, `attestations: write`, and artifact-metadata permissions; it
does not require a long-lived signing secret.

Weave passes the complete GoReleaser checksum file as the attestation subject
inventory. This keeps checksum verification and provenance verification about
the same filenames and prevents a hand-maintained glob from omitting a new
artifact. Users can verify downloaded bytes with `sha256sum`/`shasum` and verify
workflow provenance separately with `gh attestation verify`.

## Unsigned prereleases versus platform signing

Primary platform guidance:

- [Apple Developer ID certificates](https://developer.apple.com/help/glossary/developer-id-certificate/) and [notarization](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)
- [Microsoft SmartScreen reputation for app developers](https://learn.microsoft.com/en-us/windows/apps/package-and-deploy/smartscreen-reputation)

GitHub provenance is supply-chain evidence, not an operating-system code
signature. Apple expects directly distributed software to use Developer ID and
notarization. Microsoft explains that unsigned files cannot carry publisher
reputation between versions and may trigger SmartScreen; even a newly signed
binary can initially warn while reputation develops.

For an early CLI prerelease, checksums, SBOMs, reproducible metadata, a public
source build, and keyless workflow provenance provide useful verifiability
without creating certificate secrets or pretending to remove OS warnings. The
download/install documentation must call the binaries unsigned. Apple
notarization and Windows signing become release gates when Weave is offered to a
broad non-developer audience or through installers/package managers. They
should be added together with secure credential custody, rotation, and a test
that inspects the resulting signature—not as an unverified workflow stanza.

## Package-manager distribution

Homebrew, Scoop, Winget, Chocolatey, `.deb`, and `.rpm` can reduce installation
friction, but each adds publishing ownership and update policy. They are
deferred until the first archive prerelease proves naming, capabilities, and
upgrade expectations. `go install` and checksummed archives remain sufficient
for developer evaluation; no package-manager formula should imply a stability
promise the alpha does not yet make.

## Revisit triggers

Revisit these decisions when any of the following becomes true:

- GoReleaser, Syft, or an action version changes; rerun the snapshot verifier
  and record the new pinned release here.
- A consumer needs an SBOM for an individual NuGet package or wheel rather than
  for the Go archive set.
- Downloads reach users for whom Gatekeeper or SmartScreen warnings are a
  material blocker.
- Release construction moves off GitHub Actions, requiring another provenance
  issuer or downloadable attestation bundle.
- A package manager is selected; document ownership, rollback, update delay,
  and checksum/provenance preservation before publishing it.
