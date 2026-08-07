# weave-python

`weave-python` is the compiler-native lexical Python adapter for Weave. It is a
separate Python process implementing `weave.adapter/v0`; the Go core does not
parse Python or load a Python runtime.

The initial provider uses CPython's standard-library `ast` and compiler-produced
`symtable`. It never imports repository modules, executes project code, installs
dependencies, accesses the network, or runs a build tool.

## Install and use

Python 3.9 or newer and Git are required. Use an isolated tool environment; for
example, on macOS/Linux:

```console
python -m venv ~/.local/share/weave/python-adapter
~/.local/share/weave/python-adapter/bin/python -m pip install ./adapters/python
export PATH="$HOME/.local/share/weave/python-adapter/bin:$PATH"
weave adapters doctor
cd /path/to/python/repository
weave symbols MyClass
weave definition repeated_name --json
```

PowerShell uses the same wheel in a Windows venv:

```powershell
py -m venv "$env:LOCALAPPDATA\weave\python-adapter"
& "$env:LOCALAPPDATA\weave\python-adapter\Scripts\python.exe" -m pip install .\adapters\python
$env:PATH = "$env:LOCALAPPDATA\weave\python-adapter\Scripts;$env:PATH"
weave adapters doctor
```

When `weave-python` is on `PATH`, or `WEAVE_PYTHON_ADAPTER` points to its
launcher, ordinary queries automatically refresh Python facts after a tracked
or non-ignored `.py` file changes. The adapter reports its exact Python
implementation and patch version, so changing interpreters invalidates the
cached inventory. It advertises full refresh today, while returning one atomic
unit per source module.

For editable source development:

```console
python -m pip install --editable ./adapters/python
export WEAVE_PYTHON_ADAPTER="$(command -v weave-python)"
weave adapters doctor
```

Tagged Weave releases include the platform-independent Python wheel as a
companion artifact; Python itself remains an explicit runtime dependency.

## Evidence contract

Python names are binding slots, not immutable declarations. Repeated `def`,
assignment, import, or pattern bindings in one lexical scope produce one symbol
with every compiler-observed definition occurrence retained. `Symbol.Definition`
is the first canonical display anchor; `weave definition` returns all definition
occurrences.

- Modules, compiler-accepted declarations, lexical containment, scope slots,
  and local/global/free/nonlocal references are `Exact` facts about the recorded
  interpreter's static compilation model. This includes pattern captures and,
  on Python 3.12+, PEP 695 type-parameter scopes.
- Import and dependency edges are `Declared`: the statement exists, but Python
  import hooks can change the runtime target.
- Calls through a lexical name are `Syntactic`: the binding's runtime value can
  be replaced before the call.
- Attribute/member calls, type relationships, decorators' effects, dynamic
  imports, and runtime-generated symbols are omitted rather than guessed.

This distinction is intentional. A future Pyright, `scip-python`, Jedi, or `ty`
enrichment backend can add bounded inferred/ambiguous facts without relabeling
the lexical baseline or becoming a hidden dependency.

## Current boundaries

- Only regular, Git-visible UTF-8 `.py` files are indexed. Symlinks are rejected
  so host freshness bytes and adapter input bytes cannot diverge. Ignored files, `.pyi` stubs,
  notebooks, non-UTF-8 source encodings, and UTF-8 BOM files are not yet
  supported.
- Module names honor conventional `__init__.py` package roots. Namespace-package
  and configured source-root semantics are not yet modeled; ambiguous duplicate
  module names fail rather than being selected heuristically.
- The selected interpreter is the target grammar and symbol-table authority;
  cross-version parsing is not attempted.
- One source file is limited to 16 MiB, aggregate source to 512 MiB, Git
  inventory to 16 MiB, and all output
  additionally obeys host-negotiated fact, frame, stream, and process bounds.

These are correctness boundaries, not silent best-effort behavior. Unsupported
source fails the refresh and leaves the prior database untouched.

## Test

```console
PYTHONPATH=adapters/python/src \
  python -m unittest discover -s adapters/python/tests -v
```

The fixtures cover repeated bindings, nested/nonlocal and class-comprehension
scopes, PEP 695 parameters, pattern captures, calls, imports, ignored files,
module topology, conservative surface fingerprints, normalized UTF-8 byte
coordinates, malformed requests, and the complete process lifecycle. CI also
installs the wheel and runs a real Weave query-driven refresh on Linux, macOS,
and Windows.
