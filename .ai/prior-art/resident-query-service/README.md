# Resident query service prior art

Weave already had the durable graph and language-provider seams, but repeated
CLI invocations still paid process startup, exact Git inspection, bstore open,
schema validation, and intern-dictionary hydration. IDE performance comes from
keeping those project services alive and routing bounded queries to them.

## Useful precedents

- [IntelliJ file-based indexes](https://plugins.jetbrains.com/docs/intellij/file-based-indexes.html)
  use key/value indexes to locate project facts without rescanning every file.
  Index implementations are versioned and rebuilt when their format changes.
- [IntelliJ indexing and PSI stubs](https://plugins.jetbrains.com/docs/intellij/indexing-and-psi-stubs.html)
  separates compact serialized declaration stubs from full ASTs and exposes an
  explicit not-ready mode during background indexing. The useful lesson is a
  hot project service plus purpose-built projections, not copying PSI.
- [IntelliJ's VFS](https://plugins.jetbrains.com/docs/intellij/virtual-file-system.html)
  maintains an application-level persistent snapshot and reconciles external
  changes asynchronously. Weave retains exact Git observations as authority,
  but moves them off the request's hot path.
- [Language Server Protocol](https://microsoft.github.io/language-server-protocol/)
  demonstrates a long-lived language-neutral process queried over standard
  inter-process messages. Weave's session is graph-wide and provider-neutral;
  LSP remains an upstream adapter source rather than its public graph schema.
- [Bazel persistent workers](https://bazel.build/remote/persistent) amortize
  process and tool initialization across request IDs while reserving stdout for
  protocol data. This is closer to the desired lifecycle than a fresh CLI for
  every query.
- [MCP stdio transport](https://modelcontextprotocol.io/specification/draft/basic/transports)
  confirms that an owning client launching a newline-framed stdin/stdout server
  is broadly usable by agent hosts. Weave does not need to implement MCP in its
  core; a facade can translate to the smaller query-session contract later.

## Weave-specific conclusions

1. Keep one process and one bstore handle alive across a sequence of bounded
   local research queries.
2. Serialize queries and refresh transitions because the current database is a
   single-owner read/write file, not a multi-process service.
3. Observe exact Git state periodically in the background. Once a new state is
   detected, close the database before running the existing freshness pipeline,
   then reopen the published generation before answering the next query.
4. Keep source verification on each source-rich result. A polling window may
   briefly retain the preceding graph generation, but changed source must never
   be presented as if its old ranges were current.
5. Use a versioned bounded NDJSON request/response contract with request IDs,
   protocol-only stdout, and no maintenance or mutation operations.
6. Retain the ordinary one-shot CLI for humans, scripts, recovery, and clients
   that do not need a session. Do not introduce a mandatory daemon or a second
   index authority.

## Deferred deliberately

- a discoverable local broker or socket that multiplexes unrelated clients;
- an MCP facade and tool descriptions;
- resident catalog aggregates across many repositories;
- parallel query execution over the single handle; and
- changing bstore/bbolt locking or storage behavior.
