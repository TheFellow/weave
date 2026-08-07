# Query-driven adapter freshness prior art

Research date: 2026-08-06

## Sources

- [Kythe Compilation Database specification](https://kythe.io/docs/kythe-compilation-database.html)
  treats a compiler invocation plus all behavior-affecting inputs as a compilation
  unit, uses SHA-256 content identities, and indexes complete revisions separately.
- [Kythe indexer guidance](https://kythe.io/docs/schema/writing-an-indexer.html)
  requires indexing a compilation unit twice to produce the same facts.
- [SCIP](https://github.com/scip-code/scip) keeps the storage protocol independent
  of compiler-native producers. Its ecosystem reinforces using the language's real
  compiler/indexer behind a language-neutral ingestion boundary.
- [LSP/LSIF overview](https://microsoft.github.io/language-server-protocol/)
  distinguishes a live language service from a stored code-intelligence format.

## Decision for Weave

Weave remains a one-shot, query-driven system. A composite freshness provider owns
an inventory per semantic producer. Go and native adapter units are refreshed and
published in one storage transaction, and a producer can remove only units from
its own prior inventory. The native adapter executable and its negotiated provider
identity participate in the composite identity.

For `weave-dotnet`, Weave fingerprints the repository-relative C#/F# compiler and
project inputs before invoking the adapter. Its advertised `full` refresh mode
means a changed fingerprint causes a complete adapter run; an unchanged fingerprint
reuses its persisted unit inventory without starting the compiler. We deliberately
do not manufacture incremental semantics that the adapter does not advertise.

Automatic discovery is bounded to the known `weave-dotnet` executable, configured
by `WEAVE_DOTNET_ADAPTER` or found on `PATH`, and only activates in repositories
with .NET semantic inputs. It grants build-tool evaluation (needed by MSBuild) but
never restore, network, or generator permissions. Arbitrary executables and SCIP
producers remain explicit.

