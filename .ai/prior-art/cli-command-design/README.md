# CLI command design prior art

This note records the command-composition, testing, and process-boundary prior
art used for Weave milestone 1. It intentionally narrows the research to the
CLI shell; storage and semantic behavior belong behind the application boundary
and arrive in later milestones.

## Findings

### urfave/cli v3 already supplies the command graph and stream seams

`cli.Command` is both the root application and each nested command. Its
`Commands` field composes subcommands recursively, while `Run` parses the
argument vector and invokes the selected `Action`. In v3 the action signature is
`func(context.Context, *cli.Command) error`, so cancellation and request-scoped
values can pass directly to application services without global state.

The root command owns explicit `Reader`, `Writer`, and `ErrWriter` fields. Help,
version, and command actions can therefore share injected streams, and tests can
execute the real command graph with buffers. urfave's own tests use this pattern
rather than replacing parsing with a mock.

An action-less leaf is not an inert placeholder: urfave prints help when it is
run. Weave placeholder leaves therefore need an explicit action which calls an
internal no-op application method. Parent namespaces such as `architecture`,
`repos`, and `adapters` should remain command groups; only their valid leaf
commands are promised silent placeholder behavior.

Arguments that exist should be declared with v3's typed `Arguments` definitions
and `Min`/`Max` bounds. A leaf with no argument declaration, however, still
receives unmatched positional values in its action. Until real arguments exist,
the leaf adapter must explicitly reject any remaining values before invoking the
application. Unknown commands and flags remain urfave errors instead of being
accepted by a permissive catch-all action.

Sources:

- [urfave/cli v3 `Command` API, including streams, actions, and `Run`](https://pkg.go.dev/github.com/urfave/cli/v3#Command)
- [official v3 subcommand example](https://cli.urfave.org/v3/examples/subcommands/basics/)
- [urfave/cli v3.10.1 argument implementation and tests](https://github.com/urfave/cli/blob/v3.10.1/args_test.go)
- [urfave/cli v3.10.1 help behavior tests](https://github.com/urfave/cli/blob/v3.10.1/help_test.go)

### Errors should cross the library boundary; only `main` terminates

`Command.Run` returns errors and does not itself require process termination.
urfave models intentional exit status with `ExitCoder`; this permits library and
behavioral tests to inspect failures without intercepting `os.Exit`. Go's
`os.Exit` terminates immediately and deferred functions do not run, so it belongs
only in the minimal executable entrypoint after command execution returns.

Weave will keep domain/application errors independent of urfave. The CLI layer
can map them to stable exit categories as those categories are introduced. For
milestone 1, parser errors are returned unchanged, successful actions return
`nil`, normal results are reserved for stdout, and diagnostics are reserved for
stderr. The injected writers make that split observable in tests.

Sources:

- [official urfave/cli v3 exit-code guidance](https://cli.urfave.org/v3/examples/exit-codes/)
- [Go `os.Exit` contract](https://pkg.go.dev/os#Exit)
- [Go `flag` error-handling modes and usage behavior](https://pkg.go.dev/flag#ErrorHandling)

### Put dependency injection at command construction, not in globals

Go interfaces are satisfied implicitly and are conventionally kept small.
`io.Writer` is the standard example: production and test implementations need
no framework or registration. The same approach fits Weave's application
boundary.

The command constructor should accept one application interface plus explicit
stdin/stdout/stderr streams. Actions delegate through that interface and do not
construct storage, adapters, Git clients, or global singletons. A no-op
implementation can make the initial command tree honest while keeping every
leaf wired through the seam that later milestones will fill.

The first interface should describe the current lifecycle generically rather
than prematurely defining one method per future query. A command identifier and
validated argument vector are sufficient for silent placeholders; concrete
typed request methods should replace or refine that boundary when real behavior
arrives. Tests should execute `Command.Run` table-wise for every leaf, asserting
the returned error and exact stdout/stderr bytes.

Sources:

- [Effective Go on small, implicitly satisfied interfaces](https://go.dev/doc/effective_go#interfaces)
- [Go `io.Writer` contract](https://pkg.go.dev/io#Writer)
- [Go testing package](https://pkg.go.dev/testing)
- [urfave/cli v3.10.1 examples exercising command execution](https://github.com/urfave/cli/blob/v3.10.1/examples_test.go)

## Adopted for Weave

- Pin `github.com/urfave/cli/v3` at the current stable v3 release.
- Build one recursive `*cli.Command` graph with injected streams.
- Give every valid placeholder leaf an explicit no-op action.
- Validate positional arity in the CLI declaration.
- Return errors from command execution and centralize process exit in `main`.
- Exercise the actual parser and command graph in behavioral tests.
- Keep output ownership explicit: results on stdout, diagnostics on stderr,
  successful empty results on neither.

## Deferred deliberately

- Stable domain exit-code taxonomy, until corresponding failures exist.
- JSON flags and envelopes, until commands return actual data.
- Shell completion, aliases, and suggestions, until the command vocabulary has
  survived implementation experience.
- One interface method per command, until typed requests and results exist.

This preserves a real, testable CLI without pretending that milestone 1 has
implemented indexing or queries.
