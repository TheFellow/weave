# Explicit adapter discovery and input registration prior art

> Historical first increment. ADR 0016 and
> [adapter-ecosystem research](../adapter-ecosystem/README.md) now add managed
> local artifacts, persisted capability pins, and conflict/fallback routing.

Research date: 2026-08-07

## Question

How can any installed, language-native Weave adapter join automatic freshness
without adding its language or executable name to the Go core, while keeping
execution trustworthy, deterministic, and cross-platform?

## Primary sources

- Protobuf's official [compiler plugin API](https://protobuf.dev/reference/cpp/api-docs/google.protobuf.compiler.plugin/)
  resolves a conventional `protoc-gen-NAME` executable on `PATH` or accepts an
  explicit `--plugin=...=path` override. The executable boundary remains stdin,
  stdout, and literal arguments regardless of the implementation language.
- Kubernetes documents that [`kubectl plugin list`](https://kubernetes.io/docs/tasks/extend-kubectl/kubectl-plugins/)
  traverses `PATH` for executable `kubectl-*` files, reports shadowing, and does
  not audit third-party plugin safety. That is useful for explicitly invoked
  subcommands, but too broad for Weave because an ordinary query may trigger a
  freshness refresh and therefore execute every applicable semantic adapter.
- Terraform's [CLI configuration](https://developer.hashicorp.com/terraform/cli/config/config-file)
  makes provider installation sources an explicit user-level decision and can
  constrain discovery to filesystem mirrors. Its
  [dependency lock file](https://developer.hashicorp.com/terraform/language/files/dependency-lock)
  then makes selected artifacts and checksums reproducible. Weave does not need
  a package registry yet, but it needs the same separation between repository
  input and user trust.
- VS Code's [`contributes.languages`](https://code.visualstudio.com/api/references/contribution-points#contributes.languages)
  declaratively associates language IDs with extensions, exact filenames, and
  filename patterns. Extensions and exact base filenames cover Weave's initial
  conservative invalidation need without teaching the core about Rust, C++, or
  another language.
- The Language Server Protocol's [overview](https://microsoft.github.io/language-server-protocol/)
  demonstrates that language implementations and editor cores can evolve
  independently behind a language-neutral process protocol. Weave's atomic
  indexing transaction is different from LSP's live JSON-RPC lifecycle, so LSP
  is an adapter input, not the adapter discovery contract.

## Decision for this increment

Weave accepts an optional registry only when the user sets
`WEAVE_ADAPTER_CONFIG` to a JSON file. There is no repository-relative default,
home-directory default, PATH-prefix scan, download, installation, or config
mutation. Selecting the file is the trust decision. An invalid selected file is
visible in `weave adapters list` and `doctor`, and automatic freshness fails
closed.

The strict `weave.adapters/v1` document contains an array of registrations:

```json
{
  "schema": "weave.adapters/v1",
  "adapters": [
    {
      "name": "acme-rust-index",
      "command": ["acme-rust-index", "--all-targets"],
      "inputs": {
        "extensions": [".rs"],
        "filenames": ["Cargo.toml", "Cargo.lock", "rust-toolchain.toml"]
      },
      "permissions": {"build_tool": true},
      "timeout": "4m"
    },
    {
      "name": "acme-clang-index",
      "command": ["./bin/acme-clang-index", "--compdb", "compile_commands.json"],
      "inputs": {
        "extensions": [".c", ".cc", ".cpp", ".cxx", ".h", ".hh", ".hpp"],
        "filenames": ["CMakeLists.txt", "compile_commands.json"]
      },
      "permissions": {"build_tool": false}
    }
  ]
}
```

For an arbitrary third-party language, only data changes:

```json
{
  "name": "example-zig-index",
  "command": ["example-zig-index", "--target", "host debug"],
  "inputs": {
    "extensions": [".zig", ".zon"],
    "filenames": ["build.zig"]
  }
}
```

`name` is the exact provider identity that `describe` and indexing must report.
`command` is an argv array. Weave resolves only `command[0]`; values after it
are transmitted literally, including spaces, empty arguments, `$`, quotes, and
shell metacharacters. Relative command paths are resolved relative to the
registry file, while bare executable names use the host's normal executable
lookup, including Windows executable suffix behavior. No shell participates.

Input extensions and filenames are normalized, sorted, deduplicated, and
matched against Git-visible files. Matching symlinks are rejected so the host's
fingerprinted bytes cannot disagree with bytes an adapter might follow. A
registration's normalized content and registry path participate in that
provider's identity; changing an unrelated peer registration does not
invalidate it. Registration order is canonical by provider name.

Permissions remain denied unless the registry explicitly grants them. Registry
membership does not bypass capability negotiation, bounded stdout/stderr,
deadlines, atomic publication, provider-name checks, or protocol validation.

## Adopt, adapt, defer

Adopt:

- protoc's implementation-neutral executable and literal process boundary;
- Terraform's explicit user-controlled discovery source;
- VS Code's declarative extension and filename associations;
- deterministic provider selection and identity-sensitive freshness.

Adapt:

- `PATH` lookup is allowed for an explicitly registered command, but Weave does
  not scan `PATH` and automatically trust every matching filename;
- an adapter registry records execution policy and freshness inputs, not parser
  implementation details or host-language interfaces.

Defer:

- installation, downloads, registries, signatures, and lock files;
- repository-owned adapter activation before a separate consent/lock model;
- filename globs, shebang detection, and content sniffing until an adapter has a
  concrete need that extensions and filenames cannot express;
- persistent workers and dynamic callback registration.

## Resulting constraint

Adding a conformant language adapter requires no Go parser and no provider-name
branch in indexing. It requires an independently installed executable, a
language-neutral `weave.adapter/v0` implementation, and one explicit trusted
registration describing argv, conservative inputs, and permissions.
