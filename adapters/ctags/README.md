# weave-ctags

`weave-ctags` is Weave's broad, deliberately lower-fidelity semantic fallback.
It is a compiled Go adapter implementing `weave.adapter/v0` around a separately
installed [Universal Ctags](https://ctags.io/) executable. It adds useful
definition outlines for languages and formats that do not yet justify a
bespoke compiler or format-native provider; it does not replace those richer
providers.

The first increment emits documents, symbols, and definition occurrences only.
Every safely snapshotted Git-visible file receives a document unit; a tagless
file uses the honest language label `unknown` because ordinary Ctags JSON has
no per-file language record when it emits no tags. All symbol facts carry
`Syntactic` evidence. References, calls, inheritance, and other relationships
are omitted rather than guessed.

## Install and use

Install a known Universal Ctags release and build the Go wrapper:

```console
go install ./adapters/ctags/cmd/weave-ctags
weave-ctags describe --protocol weave.adapter/v0
```

Indexing strictly verifies that the selected helper is Universal Ctags, that
its release matches the configured release, and that it advertises JSON output
and at least one language parser. The default expected release is `6.2.1`.
Select an intentional alternate release explicitly:

```console
export WEAVE_CTAGS=/absolute/path/to/uctags
export WEAVE_CTAGS_VERSION=6.2.1

weave index \
  --adapter "$(command -v weave-ctags)" \
  --adapter-arg="--ctags=$WEAVE_CTAGS" \
  --adapter-arg="--producer-version=$WEAVE_CTAGS_VERSION"
```

`--ctags`/`WEAVE_CTAGS` may name the executable or provide its path. Without an
explicit selection, the wrapper looks for a sibling `uctags`, then `uctags`,
`universal-ctags`, and `ctags` on `PATH`; incompatible BSD/Exuberant variants
are rejected. `describe` remains declarative and never probes the helper.

Automatic fallback routing is intentionally not enabled in this increment.
Use `--adapter` explicitly while the ownership and overlap policy with exact
providers is exercised.

## Runtime contract

The Go wrapper asks Git for tracked and unignored files, accepts regular UTF-8
files within strict size/count limits, and copies the accepted bytes into a
private temporary mirror. Universal Ctags runs against only that mirror with:

- `--options=NONE` as its first argument;
- ambient `CTAGS` and `ETAGS` variables removed;
- JSON output, references and qualified duplicate tags disabled;
- literal bounded argument batches and no shell; and
- bounded stdout, stderr, execution time, facts, frames, and protocol bytes.

The temporary mirror is removed after the complete index has been validated.
Symlinks, non-regular files, binary/non-UTF-8 files, and oversized files are not
passed to the producer. A failed or malformed producer run emits no partial
fact stream.

Universal Ctags is GPL-2.0 licensed and is not copied into this repository or
linked into the Go binary. Distributors that bundle a sidecar must evaluate and
honor the upstream license independently.

## Test

The offline contract suite uses a fake producer and requires only Go and Git:

```console
go test ./adapters/ctags/...
```

It covers capability negotiation, strict producer probing, Git ignore rules,
private snapshots, deterministic IDs/fingerprints, representative Lua,
Protocol Buffers, SQL, CMake, and shell definitions, reference suppression,
and bounded protocol frames. Opt into a genuine installed producer smoke test:

```console
WEAVE_REAL_CTAGS=/absolute/path/to/uctags \
  go test ./adapters/ctags/... -run TestRealUniversalCtagsSmoke -count=1
```

See the [prior-art record](../../.ai/prior-art/general-semantic-mapper/README.md)
for evaluated alternatives, capability measurements, licensing notes, and the
curated Tree-sitter/format-native upgrade path.
