# Release installation and verification

Weave prereleases provide developer-oriented archives for macOS, Linux, and
Windows on amd64 and arm64. The executables are currently **not** signed with
Apple Developer ID or a Windows code-signing certificate. macOS Gatekeeper and
Windows SmartScreen may therefore warn even when the checksum and GitHub build
provenance verify. Do not bypass an operating-system warning unless you have
independently verified the download and trust this repository.

## Select and verify an archive

Download the archive for the host, its matching `.spdx.json` SBOM, and
`checksums.txt` from one GitHub release. Do not mix files from different tags.

Verify the archive bytes before extracting them:

```sh
# Linux
grep '  weave_VERSION_OS_ARCH\.tar\.gz$' checksums.txt | sha256sum -c -

# macOS
grep '  weave_VERSION_OS_ARCH\.tar\.gz$' checksums.txt | shasum -a 256 -c -
```

PowerShell can perform the same comparison:

```powershell
$archive = "weave_VERSION_windows_ARCH.zip"
$expected = ((Select-String -Path checksums.txt -Pattern "  $([regex]::Escape($archive))$").Line -split '\s+')[0]
if ((Get-FileHash -Algorithm SHA256 $archive).Hash.ToLowerInvariant() -ne $expected) { throw "checksum mismatch" }
```

The checksum proves byte integrity relative to the release manifest. For a
separate check that GitHub Actions built those bytes from this repository, use
the GitHub CLI:

```sh
gh attestation verify weave_VERSION_OS_ARCH.tar.gz --repo TheFellow/weave
```

The matching `*.spdx.json` file is the Syft-generated SPDX software bill of
materials for that exact archive. It is also listed in `checksums.txt`.

## Install

On macOS or Linux, extract the archive and place `weave` in a directory already
on `PATH`:

```sh
tar -xzf weave_VERSION_OS_ARCH.tar.gz
install -m 0755 weave "$HOME/.local/bin/weave"
weave version
```

On Windows, expand the zip and copy `weave.exe` into a user-controlled directory
on `PATH`, then run `weave version`. The separately published adapter archives
are optional; the core reports unavailable adapters rather than silently
installing their runtimes or semantic producers.

Building from reviewed source remains supported:

```sh
go install github.com/TheFellow/weave/cmd/weave@VERSION
```

## Upgrade and rollback

Weave has no background service to stop. Replace the executable atomically (or
install the new file beside the old one and update `PATH`) and run `weave
version`. A newer executable may require rebuilding disposable indexes. To roll
back, restore the previous executable and rebuild rather than attempting to
downgrade a database schema.

## Derived-state recovery

Preserve source and checked-in Weave configuration; databases can be removed.
Export graph facts first when they will help diagnose a bug:

```sh
weave verify
weave export --json > weave-export.json
weave status
```

The current worktree's derived state is found through Git, including linked
worktrees:

```sh
weave_path=$(git rev-parse --git-path weave)
rm -rf "$weave_path"
weave index
```

Review the resolved path before deleting it. Never substitute an empty variable
or a repository root. The cross-repository catalog is also rebuildable from
explicit registrations and defaults to:

- macOS: `~/Library/Application Support/weave/catalog.db`
- Linux: `${XDG_STATE_HOME:-~/.local/state}/weave/catalog.db`
- Windows: `%LOCALAPPDATA%\weave\catalog.db` (falling back to the platform user configuration directory when `LOCALAPPDATA` is unavailable)

`WEAVE_CATALOG` or `--catalog /absolute/path/catalog.db` may override that
location. Prefer `weave repos remove` for one stale registration. If the catalog
itself is corrupt, move that one file aside, then re-register worktrees with
`weave repos add`; per-worktree indexes remain independently rebuildable.

When reporting a packaging or recovery failure, include `weave version --json`,
the host OS/architecture, the archive filename and checksum, and the failing
command. Do not attach an index that may contain private repository identities
without reviewing its contents.
