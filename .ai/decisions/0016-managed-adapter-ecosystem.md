# ADR 0016: Managed adapter ecosystem and pinned input claims

- Status: Accepted
- Date: 2026-08-07
- Extends: [ADR 0008](0008-compiler-plugin-process-model.md)
- Research: [adapter ecosystem prior art](../prior-art/adapter-ecosystem/README.md)

## Context

The language-neutral process boundary has implementations in Go, Python, Rust,
and .NET, but executable names and mutable `describe` output are not a safe
automatic-discovery policy. Users need a practical lifecycle, third parties
need one reusable conformance corpus, and broad fallback adapters must not
silently duplicate precise compiler ownership.

## Decision

1. Keep one-shot `weave.adapter/v0` stdin/stdout/stderr/process-exit behavior as
   the compatibility floor. No public adapter imports Go or loads in-process.
2. `describe` includes normalized input claims (extensions, exact filenames,
   project markers, and an explicit fallback bit) plus evidence classes.
   Existing languages, operations, requirements, and encodings remain
   capability fields. Provider names identify provenance, not routing behavior.
3. Precise active claims are exclusive. A conflict names both owners and the
   exact extension, filename, or concrete path. Fallback claims lose to precise
   claims. The host sends the concrete routed `input_paths`; fallback adapters
   must restrict their complete inventory to that allowlist, and the host
   rejects fallback documents outside it. This permits unclaimed languages in
   a polyglot repository without duplicating compiler-owned files.
4. `weave adapters install/update/remove/list/doctor` manages explicit local
   regular-file artifacts in platform user state. It never scans repositories
   or `PATH`, downloads code, extracts archives, invokes package managers, or
   uses a shell. Adapter-specific environment variables remain explicit legacy
   trust paths and participate in automatic indexing without restoring ambient
   executable discovery.
5. The bounded JSON manifest is serialized with the shared cross-process lock
   abstraction (backed by bbolt's portable file lock, not bbolt records) and
   replaced atomically, including a Windows-safe previous-file
   recovery step. Installed executable bytes and normalized capabilities are
   pinned with SHA-256. Symlink/non-regular paths, corruption, and drift fail
   closed before automatic execution. Updates preserve the existing literal
   arguments, permissions, and timeout unless their flags are supplied
   explicitly.
6. `WEAVE_ADAPTER_CONFIG` remains an explicit override. It replaces a managed
   registration only with the identical provider name. It must pin capabilities
   before automatic use; doctor reports the observed digest for configuration.
7. `list` reads metadata only. `doctor` verifies artifact integrity, current
   worktree activation, claim conflicts, and may run the bounded
   declarative handshake and resolve named external requirements, but never
   indexes, builds, restores, generates, installs, or accesses the network.
8. `protocol/adapter/v0/conformance` is the language-neutral source of truth.
   The CLI runner treats an executable as a black box and checks negotiation,
   genuine fixture indexing, deterministic replay, malformed/wrong-protocol
   exits, stderr separation, permissions, and host limits. A Python fixture
   proves no Go SDK dependency.

## Consequences

Automatic language support is now data-driven and independently packageable.
Local artifact installation is deliberately smaller than a package manager but
is useful for release archives, native builds, relocatable launchers, and
ecosystem tools. Environment-owned multi-file shims remain explicit paths.
Updates are explicit, so a provider version or claim expansion cannot
silently gain authority.

Full capability pins mean an intentional provider/toolchain upgrade requires
`adapters update` (or an explicit-registry digest change). Explicit companion
environment overrides are checked against their fixed compatibility claims on
every automatic refresh; explicit configuration overrides an identical name,
and managed state is the lowest-precedence source.
Remote catalogs, signatures, OCI/ORAS acquisition, sandboxing, and package
manager publication remain separate policy decisions rather than implied
security.
