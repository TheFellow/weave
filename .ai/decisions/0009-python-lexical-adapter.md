# ADR 0009: Python-native lexical adapter and binding-slot identity

- Status: Accepted
- Date: 2026-08-06
- Research: [Python semantic adapter prior art](../prior-art/python-semantic-adapter/README.md)

## Context

Python is the first third implementation of Weave's provider model and the first
adapter written outside Go and .NET. It must prove that the process contract is
language-neutral while avoiding false precision in a dynamic language.

CPython exposes its parser through `ast` and the compiler's lexical decisions
through `symtable`. Pyright, basedpyright, Jedi, `scip-python`, and `ty` offer
richer static inference, but none turns Python's runtime attribute, import, or
call dispatch into universally exact targets.

Python also challenges the initial singular-definition model. One lexical name
can be assigned, imported, or defined repeatedly while compiling to one scope
slot.

## Decision

1. Ship `weave-python` as a Python 3.9+ subprocess implementing the same strict
   `weave.adapter/v0` request/frame contract as the .NET adapter. It uses only
   standard-library `ast` and `symtable` in the correctness baseline.
2. Never import repository modules or execute project/package tooling. Discover
   regular `.py` files through Git with repository fsmonitor execution disabled;
   reject ignored paths, symlinks, repository escapes, duplicate module names,
   and fail atomically on unsupported encoding or syntax.
3. Model a Python lexical binding slot as one symbol. Every binding statement is
   a definition occurrence. The symbol's singular definition range is only its
   canonical display anchor.
4. Mark compiler declarations, scope classification, containment, definitions,
   and lexical-name references `Exact`. This is exact about bytecode binding
   slots, not about the runtime object currently stored in a slot.
5. Mark import/dependency statements `Declared`, call spelling through a known
   lexical slot `Syntactic`, and omit unresolved attributes and dynamic behavior.
6. Make `weave definition` prefer all stored definition occurrences, falling
   back to `Symbol.Definition` only for older providers that emit no definition
   occurrences.
7. Include adapter and exact interpreter implementation/version in provider and
   unit fingerprints. A runtime-backed automatic provider performs a bounded
   capability probe before accepting cached freshness. Keep symbol identity
   independent of adapter and interpreter patch upgrades; requested graph
   variants remain an explicit identity input.
8. Generalize automatic native-provider freshness by provider name, input
   profile, permissions, and capability-version probing. `.NET` remains one
   configured profile rather than the architecture of the abstraction.
9. Package a pure-Python wheel and test its installed console executable on
   Linux, macOS, and Windows. The adapter remains independently distributable.
10. Fingerprint module/package topology as an input. Until Python export
    analysis is richer, conservatively treat every module-source edit as a
    possible public-surface change so reverse invalidation cannot miss dynamic
    API changes.

## Consequences

Weave gains useful Python declarations, definitions, references, calls, imports,
and dependencies without a Node service, hidden installation, or Python logic in
the Go core. Repeated definitions become visible in all languages that emit
definition occurrences. Interpreter changes cannot silently reuse old facts.

The initial adapter is lexical rather than type-enriched. It does not emit
attribute calls, inheritance, protocols, runtime decorators, `.pyi` facts, or
configured namespace-package resolution. Full provider refresh is conservative;
per-module units prepare a later changed-unit capability. PEP 695 type parameters
and structural-pattern captures are lexical compiler bindings and are included.

## Deferred enrichment

Evaluate pinned `scip-python` first because it can feed Weave's existing SCIP
normalizer, but do not globally promote its output to `Exact`. Compare it with
current Pyright/basedpyright, Jedi, and `ty` on adversarial fixtures. Any backend
that probes another environment or package manager requires explicit execution
permission, bounded diagnostics, and complete environment fingerprints.
