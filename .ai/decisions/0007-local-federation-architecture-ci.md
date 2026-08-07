# ADR 0007: Local federation, graph policy, and disposable CI state

Status: Accepted

Research: [federation, architecture, and CI prior art](../prior-art/federation-architecture-ci/README.md)

## Context

Weave needs cross-repository navigation and repeatable policy checks without a
service, daemon, filesystem crawler, or committed derived database. Repository
indexes already preserve stable semantic identities and evidence.

## Decision

1. Store the user catalog in platform application state, overridable by an
   absolute CLI flag or `WEAVE_CATALOG`. Use bstore transactions and its bounded
   file lock. Registration is explicit and worktree-aware.
2. Keep graph databases per worktree. Federated queries open only selected,
   bounded catalog members; merge/deduplicate canonically and return provenance
   plus partial-failure diagnostics. Identical stable symbol IDs are the only
   automatic cross-repository join.
3. Add checked-in `.weave/architecture.json` schema `weave.architecture/v1`.
   Layers select repository-relative document paths, unit/package IDs, or
   stable symbol IDs. Rules are `allow` or `forbid` over explicit edge kinds.
   A matched forbidden edge, or an edge outside a nonempty applicable allow
   set, is a violation. Every result preserves source evidence.
4. Emit deterministic text, `weave.architecture-result/v1` JSON, and SARIF
   2.1.0. Violations exit nonzero; malformed configuration and internal errors
   remain distinct failures.
5. CI state remains disposable. `weave ci key`, `weave ci index`, and
   `weave ci check` expose stable cache identity and composed local workflows.
   Examples cache or upload generated state but never commit it.

## Consequences

Catalog loss requires only explicit re-registration. Worktree entries can be
missing or stale independently. Query fan-out has an explicit upper bound and
may be partial. Exact provider identities can bridge repositories immediately;
names alone cannot. JSON is used initially to avoid introducing a YAML parser.
The first policy language is deliberately finite and auditable rather than a
general expression evaluator.

Deferred work includes catalog import/export, remote artifact discovery,
approximate package mapping, declared cross-language bridge configuration,
CODEOWNERS integration, richer predicates, and hosted coordination.
