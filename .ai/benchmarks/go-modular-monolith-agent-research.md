# Agent research dogfood: menu readiness

Date: 2026-08-07

Repository: `github.com/TheFellow/go-modular-monolith`

Question: explain how menu readiness and blocker state flow from domain
computation into the GUI and TUI; identify the central composition function,
its direct callers and callees, and the tests most relevant to a behavior
change.

## Method

A fresh agent received the question, the repository path, and instructions to
use the installed `weave` CLI before filesystem tools. It had no implementation
context from the parent session. It recorded every Weave and fallback command
and was required to criticize the tool rather than assume it was useful.

This was a representative dogfood run, not a controlled benchmark. There was no
without-Weave arm, so it does not support a numeric token- or time-savings
claim. Future repetitions should follow the controlled-arm requirements in the
vision.

## Result

The agent correctly traced:

- `AvailabilityCalculator.Readiness` through `queries.Readiness` and
  `Module.Readiness`;
- the shared `ReadinessReport`, `HasBlockers`, and `RequireReady` contract;
- `ApplyReadiness` into `Presenter.loadReadiness`,
  `Presenter.permissionsFor`, and `ListViewModel.Update`;
- independent publish-time enforcement through `Commands.Publish`; and
- the directly relevant unit, GUI, TUI, and stale-result tests.

`weave callers ApplyReadiness` and `weave callees ApplyReadiness` produced the
exact central topology with source locations. Symbol search and field
references quickly located the domain, GUI, TUI, and test seams. Most queries
completed in under 1.5 seconds.

Fallback was limited to targeted `sed` reads over ranges discovered by Weave,
one `rg` validation of references/tests, and `git status`. The agent reported
that Weave materially reduced broad filesystem discovery.

## Friction discovered

- Ambiguous methods pushed the agent toward opaque graph IDs because concise
  names such as `AvailabilityCalculator.Readiness` were not resolvable.
- `context` repeated call and reference edges for the same endpoint.
- Low-value variable and builtin references crowded useful flow relationships
  and caused truncation.
- A batch of broad context queries produced roughly 16,000 tokens, so context
  ingestion improved only moderately despite strong discovery savings.
- Default impact traversal escaped through generic methods such as `Update` and
  `Select`, producing a noisy, truncated blast radius.

## Resulting product changes

- Human-qualified `Type.Method` resolution now falls back through the terminal
  symbol name and matches compiler-qualified stable names deterministically.
- Ambiguity errors show stable names and kinds before opaque IDs.
- Context relationships collapse duplicate endpoints, prefer semantic flow
  edges over their underlying reference edges, and apply limits after that
  compaction. Local-variable and language-builtin references are omitted from
  the bounded dossier while remaining available through exhaustive graph data.
- Text output prefers stable semantic names and removes repeated repository and
  compiler-category prefixes inside a context dossier; JSON preserves the full
  names and internal IDs.
- `weave explore` turns a research phrase into a bounded, ranked set of the same
  source-rich context dossiers so agents have one obvious first tool without
  creating a second persisted index or semantic query implementation.

The default impact policy remains an open tuning problem. Call-only impact is
already available through `--kind calls`; changing the default requires broader
language and repository evidence.

## Second task and command-consolidation finding

A second fresh agent researched how menu publishing is authorized and enforced
from GUI/TUI action availability through the module, command middleware,
readiness checks, and persistence. It produced a correct cross-layer account
and kept the repository clean.

The run used 32 Weave commands, two filesystem searches, one source-file read,
and one cleanliness check. That is strong evidence that the graph avoided broad
filesystem discovery, but it is not yet the one-to-four-call product experience
demonstrated by CodeGraph. The initial research-phrase invocation failed because
the then-current `explore` name was only an alias for exact `context`.

That finding caused `explore` to become a real composition surface. It now
performs deterministic lexical candidate retrieval, ranks compiler-backed
entities using symbol-name, kind, and path-scope evidence, and returns multiple
ordinary context dossiers under one shared byte budget. A representative menu
publish question returns the GUI publish boundary, `Module.Publish`, and
`Commands.Publish` in one roughly 2-second call on this repository. This is a
promising functional check, not by itself a controlled agent benchmark.

The reproducible paired-arm harness now lives at
`scripts/benchmark-agent-research.sh`, with this question and its deterministic
correctness rubric under `.ai/benchmarks/agent-research/menu-publish/`. It
retains raw event logs and answers so tool-count and token claims remain
auditable instead of being inferred from the final prose.
