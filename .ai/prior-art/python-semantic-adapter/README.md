# Python semantic adapter prior art

Research date: 2026-08-06

## Recommendation

Build Python support as a separate `weave-python` process, with CPython's
[`ast`][ast] and [`symtable`][symtable] modules as the dependency-free
correctness floor. `symtable` is produced by the compiler immediately before
bytecode generation and supplies Python's actual lexical scope decisions;
`ast` supplies syntax and UTF-8 byte ranges. This is enough for useful, honest
declarations, containment, scope-slot references, and declared imports without
reimplementing Python in Go.

Do not call this a whole-program Python resolver. Python resolves free names at
runtime, permits repeated bindings, allows arbitrary import hooks, and lets
`__getattribute__`, descriptors, metaclasses, decorators, and monkey-patching
change apparent targets. The first adapter should make these boundaries visible
through Weave's evidence model. Type-checker enrichment can follow as an
optional backend, with [`scip-python`][scip-python] as the quickest existing
experiment and Pyright/basedpyright as conformance oracles.

This also exposes a useful model requirement: a Python name is commonly a
*scope slot with several binding occurrences*, not one immutable declaration.
Weave can already attach multiple definition occurrences to one symbol, but its
singular `Symbol.Definition` and `definition` query should be documented as a
canonical display anchor or evolved to return every definition occurrence. An
adapter must not silently choose one declaration and label the result exact.

## Options compared

| Option | What it genuinely provides | Distribution and protocol | Fit for Weave |
| --- | --- | --- | --- |
| CPython `ast` + `symtable` | Official syntax tree plus compiler-calculated local, global, nonlocal, free, parameter, import, assignment, and namespace classifications. `ast.parse` alone does **not** perform scope checks, and its older-grammar mode is explicitly best effort. | Standard library on Linux, macOS, and Windows; a small wheel/zipapp can implement `weave.adapter/v0` directly. The selected interpreter implementation and version become semantic inputs. | **Adopt first.** Smallest trusted base and clearest evidence boundary. It deliberately cannot resolve general attributes, values, or dynamic calls. |
| Pyright / basedpyright | Mature static type evaluation, import resolution, definitions, references, and language-service navigation. Pyright's public batch JSON is diagnostics, not a semantic graph; its LSP is an interactive query API rather than a complete atomic inventory. | Pyright is MIT and Node-based. [basedpyright packages its CLI and language server on PyPI][basedpyright-install] and uses platform Node wheels, which is convenient but still ships the TypeScript engine. Both can speak LSP over stdio. | Strong enrichment/conformance oracle. Avoid coupling Weave to undocumented TypeScript internals. Driving definition/reference requests for every symbol through LSP is expensive and omits fingerprints and complete unit inventories. |
| Jedi | A stable Python API for `get_names`, `goto`, `infer`, and project references. Its own documentation says reference search may stop when it becomes too complicated. | MIT, pure Python and easy to embed in the adapter. Selecting another environment executes that environment's Python binary. | Useful fallback/oracle, especially for untyped code, but its best-effort inference is not grounds for `Exact` call edges. Its environment execution also needs an explicit trust boundary. |
| `scip-python` | A ready SCIP producer built by adding an indexer to a Pyright fork. It emits project and dependency symbols and can feed Weave's existing SCIP importer. | MIT npm package; requires Node and Python 3.10+, consults `pip` unless given an explicit environment inventory, and may need a large Node heap. | **Best short evaluation path, not the normative adapter yet.** At the inspected head its [Pyright sync marker][scip-sync] references a [2023 upstream commit][scip-upstream], it [chooses the first declaration when several exist][scip-first], and it [continues writing an index after repeated analysis failures][scip-partial]. Pin it, capture diagnostics, and test every evidence conversion rather than importing all SCIP facts as exact. |
| Astral `ty` | A fast MIT Rust type checker/LSP with definitions, references, workspace symbols, call hierarchy, and fine-grained incrementality. | Cross-platform native executable and stdio LSP. | Promising future backend, especially if it gains a batch index/export API. It is currently beta with no stable API, so integrating private Rust crates or issuing one LSP request per symbol would create avoidable coupling. |
| Zuban | Rust type checker/LSP from Jedi's author with definitions and references. | Native process, but AGPL. | Useful comparison corpus; lower-priority integration because of licensing and the lack of a batch fact export. |

LSP remains good upstream adapter plumbing: it standardizes JSON-RPC process
communication and interactive navigation. It is not a replacement for Weave's
complete unit inventory, fingerprints, evidence, bounds, and atomic terminal
response. SCIP is a closer interchange format; `scip-python` proves that
Pyright can produce it, while also demonstrating why Weave must retain its own
evidence and lifecycle contract.

## Honest evidence boundaries

`Exact` should mean exact about the asserted static fact, not a prediction of
all possible runtime behavior.

| Python fact | Evidence Weave may honestly emit |
| --- | --- |
| A compiler-accepted module, class/function syntax declaration, parameter, source containment, and its range | `Exact`, when parsed and symbol-tabled by the recorded matching interpreter. |
| A `Name` occurrence's local/free/nonlocal/global *scope slot* | `Exact`; the compiler defines this binding category. A global lookup can still obtain a different runtime value. |
| One of several assignments/definitions bound to the same slot | `Exact` definition occurrence on the slot; no claim that it is the unique or effective runtime value. |
| Import statement and dependency declared in source or a manifest | `Declared`. A resolved module-file edge may be `Exact` only under a completely recorded static environment with one target. |
| Annotation, decorator, base-class expression, `__all__`, or stub contract | `Declared` when represented as written; relationships derived by evaluating the typing model are `Inferred`. |
| Unique definition/member/call target returned by Pyright, Jedi, or another type evaluator | `Inferred`, unless the edge vocabulary is explicitly scoped to that evaluator's static semantics. Python runtime dispatch is not thereby exact. |
| Several valid definitions, overloads, union member targets, star imports, or unresolved package/attribute choices | `Ambiguous`, retaining candidates where bounded. |
| AST-only attribute or call spelling | `Syntactic`; do not manufacture an external symbol and upgrade it merely because the text matches. |
| `getattr` with dynamic names, module/object `__getattr__`, custom `__getattribute__`, descriptors, metaclass-created members, `exec`/`eval`, runtime imports, or monkey patches | Omit with a diagnostic, or emit bounded `Syntactic`/`Ambiguous` evidence. |

These limits follow Python itself: the execution model says free-name lookup
occurs at runtime; the data model permits computed attribute access and dynamic
class creation; and import hooks can override normal path lookup. Even
`symtable` notes that one name may bind multiple objects. A type checker's
answer remains highly valuable, but it is a deterministic inference under a
specific configured environment.

## Units, identity, and freshness

Use a source module as the initial atomic unit. Derive public identities from
repository identity, resolved module name, logical scope path, kind, and name.
Never persist `symtable.get_id()` as an identity; it has no cross-run stability
contract. Locals and anonymous scopes may add a document/declaration anchor and
are consequently less stable under edits. Repeated assignments in one scope
remain occurrences of the same slot; genuinely distinct nested declarations
need distinct logical scopes.

The unit environment and input fingerprint must include at least:

- Python implementation and exact version, target version/platform, adapter
  version, and position encoding;
- module/source-root and namespace-package topology, `pyproject.toml`, analyzer
  configuration, and relevant environment variables;
- source and `.pyi` content, `py.typed`, typeshed/stub version, ordered import
  search paths, and installed distribution identity/version when semantic
  enrichment is enabled;
- direct dependency surface fingerprints for type-derived facts.

Stable symbol IDs should not include adapter builds or interpreter patch
versions: those invalidate the fact inventory without changing the public
semantic identity. Explicit user-selected graph variants may remain part of
identity when variants must coexist.

Pyright's documented import order shows why these are semantic inputs: manual
stubs, workspace roots, extra paths, installed packages, inline stubs,
`py.typed`, library source, and typeshed can all change the answer. A source-only
AST/symtable run can cheaply replace one changed module. A type-enriched run
must invalidate reverse dependencies when the dependency's exported typing
surface changes; package/root/configuration changes should conservatively
re-evaluate the affected environment.

Do not import repository modules while indexing. Python's import machinery may
execute loaders and arbitrary hooks. Even probing a project interpreter can
run `sitecustomize`, user customization, or executable `.pth` entries. Safe
source-only mode should use the already-started adapter runtime and explicit
paths. Environment probing or package-tool execution must be explicit,
permissioned, bounded, and reflected in diagnostics and fingerprints; the
current adapter permission vocabulary may need a clearer “execute toolchain”
capability distinct from restore or generators.

## Practical first slice and tests

1. Ship `weave-python` as a Python process that implements the existing strict
   stdin/stdout adapter contract and never imports project modules.
2. Parse each supported `.py` with matching CPython `ast` and `symtable`; emit
   modules, scope-slot symbols, all definition/reference occurrences,
   containment, declared imports, diagnostics, and deterministic fingerprints.
3. Emit calls, attributes, inheritance, and resolved imports only at their
   honest evidence level. Add an optional, explicitly configured enrichment
   path later rather than making Node/Jedi a hidden dependency.
4. Evaluate the same fixture corpus with `scip-python`, current basedpyright,
   Jedi, and eventually `ty`; differences become conformance cases, not an
   excuse to select whichever answer looks richest.

Fixtures should include repeated definitions and overloads; decorators that
replace functions/classes; local/global/nonlocal and class/comprehension scope;
relative, star, namespace-package, stub, and `py.typed` imports; union and
Protocol dispatch; `__getattr__`, descriptors, metaclasses, monkey patches,
`exec`, and dynamic imports. Run supported interpreter versions on Linux,
macOS, and Windows, including non-ASCII identifiers to validate Python's UTF-8
byte columns against the Weave contract.

## Primary OSS sources

- [CPython `ast` documentation][ast]
- [CPython `symtable` documentation][symtable]
- [Python execution and name-resolution model][execution]
- [Python import hooks][imports]
- [Python customizable attribute and class behavior][data-model]
- [Pyright command-line contract][pyright-cli] and [import resolution][pyright-imports]
- [basedpyright packaging][basedpyright-package]
- [Jedi public API][jedi-api]
- [`scip-python` source and environment model][scip-python]
- [`ty` repository][ty] and [language-server capabilities][ty-lsp]
- [Python typing distribution and stub rules][typing-distribution]
- [Language Server Protocol][lsp]

[ast]: https://docs.python.org/3/library/ast.html
[symtable]: https://docs.python.org/3/library/symtable.html
[execution]: https://docs.python.org/3/reference/executionmodel.html
[imports]: https://docs.python.org/3/reference/import.html#import-hooks
[data-model]: https://docs.python.org/3/reference/datamodel.html#customizing-attribute-access
[pyright-cli]: https://github.com/microsoft/pyright/blob/main/docs/command-line.md
[pyright-imports]: https://github.com/microsoft/pyright/blob/main/docs/import-resolution.md
[basedpyright-install]: https://docs.basedpyright.com/latest/installation/command-line-and-language-server/
[basedpyright-package]: https://github.com/DetachHead/basedpyright/blob/main/pyproject.toml
[jedi-api]: https://jedi.readthedocs.io/en/latest/docs/api.html
[scip-python]: https://github.com/sourcegraph/scip-python
[scip-sync]: https://github.com/sourcegraph/scip-python/blob/8b60bbce1f2a4c7a517776cb395bbafb2e731e4f/pyright-last-commit
[scip-upstream]: https://github.com/microsoft/pyright/commit/fac54273791fde77408b1b5f7161117b9f934c22
[scip-first]: https://github.com/sourcegraph/scip-python/blob/8b60bbce1f2a4c7a517776cb395bbafb2e731e4f/packages/pyright-scip/src/treeVisitor.ts#L868-L880
[scip-partial]: https://github.com/sourcegraph/scip-python/blob/8b60bbce1f2a4c7a517776cb395bbafb2e731e4f/packages/pyright-scip/src/indexer.ts#L141-L155
[ty]: https://github.com/astral-sh/ty
[ty-lsp]: https://docs.astral.sh/ty/features/language-server/
[typing-distribution]: https://typing.python.org/en/latest/spec/distributing.html
[lsp]: https://microsoft.github.io/language-server-protocol/
