# Adapter ecosystem, lifecycle, and conformance prior art

Research snapshot: 2026-08-07. This increment is intentionally local-first:
the sources below inform executable discovery, compatibility, installation,
integrity, and testing, but do not justify inventing a remote package registry.

## Compiler-driver and capability contracts

- Protobuf's official [compiler plugin API](https://protobuf.dev/reference/cpp/api-docs/google.protobuf.compiler.plugin/)
  establishes the durable minimum: a separately compiled executable receives a
  request on stdin and writes a response on stdout. `protoc` supports both a
  conventional `protoc-gen-NAME` executable and an explicit `--plugin` path.
  Weave keeps the process shape, but automatic indexing uses only an explicitly
  trusted installation/configuration rather than scanning `PATH`.
- The [Language Server Protocol](https://microsoft.github.io/language-server-protocol/specifications/lsp/3.18/specification/)
  negotiates client and server capabilities during initialization. Its dynamic
  registration model also shows why advertised support is not executable-name
  policy. Weave adapts capability negotiation to a one-shot atomic index and
  persists the claims used for routing so later mutable handshakes cannot
  silently expand authority.
- OCI's [distribution conformance model](https://github.com/opencontainers/distribution-spec/blob/main/spec.md#conformance)
  separates a normative specification from a reusable black-box test suite and
  reports supported categories. Weave follows that split with checked-in,
  language-neutral fixtures plus a host command runnable against any executable.

## Managed executable lifecycle

- [rustup proxies](https://rust-lang.github.io/rustup/concepts/proxies.html)
  put stable commands in one user-owned directory and dispatch to managed
  toolchains. Its [custom toolchain linking](https://rust-lang.github.io/rustup/concepts/toolchains.html#custom-toolchains)
  demonstrates that a useful manager can accept local artifacts as well as
  remote channels. Weave copies an explicitly selected local executable into a
  managed user-state directory; it does not create ambient `PATH` proxies.
- [pipx](https://pipx.pypa.io/stable/) treats applications, their installation
  records, and clean removal as one lifecycle. Isolation is ecosystem-specific,
  so Weave adopts list/update/remove ownership but not Python environments or
  implicit package acquisition. Environment-owned multi-file launchers are not
  relocatable single-file artifacts and remain explicit paths rather than being
  copied incompletely into Weave's store.
- [`go install package@version`](https://go.dev/ref/mod#go-install) builds a
  named version independently of the current module. It remains excellent for
  producing an adapter artifact, but invoking language package managers from
  Weave would add network, compiler, and trust policy. The initial manager
  therefore accepts the resulting local executable only.
- The [XDG Base Directory specification](https://specifications.freedesktop.org/basedir/latest/)
  separates durable per-user state from repository content. Weave uses the same
  platform state-root policy as its catalog: XDG state on Linux, Application
  Support on macOS, and LocalAppData on Windows.

## Integrity and atomic metadata

- OCI [content descriptors](https://github.com/opencontainers/image-spec/blob/main/descriptor.md)
  use a digest as content identity, and the distribution specification requires
  downloaded bytes to be checked against it. A managed Weave registration pins
  SHA-256 for the installed executable and for its normalized capability
  document. Doctor verifies both without indexing.
- Terraform's [dependency lock file](https://developer.hashicorp.com/terraform/language/files/dependency-lock)
  distinguishes provider selection from checksums of accepted packages. Weave's
  managed manifest makes the same distinction: provider/claims select and route;
  an artifact digest detects replacement. `WEAVE_ADAPTER_CONFIG` remains a
  deliberate higher-precedence override rather than repository configuration.
- Go's [`os.Rename`](https://pkg.go.dev/os#Rename) provides the local atomic
  replacement primitive, while bbolt's documented single-writer file locking
  gives the already-depended-on cross-platform serialization mechanism. A
  shared internal process-lock abstraction hides that implementation detail:
  bridge and adapter callers store no records or buckets in it. Weave writes a
  bounded temporary manifest, syncs it, and atomically renames it.

## Conformance suites

- Go's own [`cmd/go` script tests](https://go.dev/src/cmd/go/testdata/script/README)
  keep fixtures textual and executable from an external process boundary. Weave
  similarly publishes request/malformed-input fixtures and fixture metadata,
  not Go-only helper APIs.
- OCI's repository includes a standalone conformance runner and machine-readable
  results. Weave's smaller runner checks describe negotiation, a real fixture
  index, deterministic replay, malformed request rejection, wrong-protocol
  rejection, stderr/host bounds, and process exit semantics.

## Decision summary

1. Keep `weave.adapter/v0` as a language-neutral one-shot subprocess ABI.
2. Add normalized `claims.inputs` (extensions, exact filenames, and project
   markers) plus evidence classes to the existing language, operation, and
   requirement capabilities.
3. Persist the exact normalized capabilities and their digest when installing.
   Automatic execution fails closed if the executable, provider, or claims
   drift. Provider versions may change only through an explicit update.
4. Managed artifacts are local regular files copied into platform user state.
   No repository scan, `PATH` prefix scan, remote download, archive extraction,
   package-manager invocation, or shell interpolation occurs.
5. Explicit `WEAVE_ADAPTER_CONFIG` entries override a same-named managed entry.
   Explicit companion environment paths override same-named managed entries,
   and the registry has final same-name precedence. Different precise providers
   whose active claims overlap are a routing error; a fallback receives only
   the remaining concrete paths through the request allowlist.
6. `list` reads metadata only. `doctor` may run the bounded declarative
   handshake, but never indexes, builds, restores, installs, or uses the network.
7. Remote catalogs, OCI/ORAS transport, signing policy, runtime isolation, and
   package-manager integration remain deferred until there is a real publisher
   and threat/update policy. Content digests are useful now; a pretend remote
   package manager is not.
