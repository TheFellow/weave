"""One-shot weave.adapter/v0 implementation using Python's compiler frontend."""

import ast
import functools
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import stat
import subprocess
import symtable
import sys
import threading
import tokenize
import unicodedata

from . import __version__


PROTOCOL = "weave.adapter/v0"
FACT_ENCODING = "weave.facts/v0"
PROVIDER = "weave-python"
MAX_GIT_INVENTORY = 16 << 20
MAX_GIT_STDERR = 64 << 10
MAX_SOURCE_BYTES = 16 << 20
MAX_TOTAL_SOURCE_BYTES = 512 << 20


class AdapterError(Exception):
    pass


@functools.lru_cache(maxsize=1)
def provider_version():
    version = sys.version_info
    digest = hashlib.sha256()
    for path in sorted(Path(__file__).parent.glob("*.py")):
        digest.update(path.name.encode("utf-8"))
        digest.update(b"\0")
        digest.update(path.read_bytes())
        digest.update(b"\0")
    return "{}+{}.{}.{}.{}.code.{}".format(
        __version__,
        sys.implementation.name,
        version.major,
        version.minor,
        version.micro,
        digest.hexdigest()[:12],
    )


def describe(output):
    value = {
        "protocols": [PROTOCOL],
        "provider": {"name": PROVIDER, "version": provider_version()},
        "languages": ["python"],
        "operations": ["index"],
        "refresh_modes": ["full"],
        "fact_encoding": FACT_ENCODING,
        "position_encodings": ["utf8-byte"],
        "requires": {
            "executables": ["python>=3.9", "git"],
            "may_run_build_tool": False,
        },
    }
    output.write(_json(value) + "\n")


def index(input_stream, output):
    request = _read_request(input_stream)
    requested_root = Path(request["repository_root"])
    if not requested_root.is_absolute():
        raise AdapterError("repository_root must be an absolute directory")
    root = requested_root.resolve(strict=True)
    if not root.is_dir():
        raise AdapterError("repository_root must be an absolute directory")
    paths = _git_python_files(root)
    modules = _module_inventory(paths)
    sources = []
    total_source_bytes = 0
    for relative in paths:
        source = Source(root, relative)
        total_source_bytes += len(source.content)
        if total_source_bytes > MAX_TOTAL_SOURCE_BYTES:
            raise AdapterError(
                "Python sources exceed {} bytes".format(MAX_TOTAL_SOURCE_BYTES)
            )
        sources.append(source)
    units = []
    for source in sources:
        units.append(
            Analysis(
                source,
                request.get("repository_identity") or str(root),
                request.get("variant") or "default",
                modules,
            ).run()
        )
    units.sort(key=lambda facts: facts["unit"]["id"])
    fact_count = sum(
        len(facts[kind])
        for facts in units
        for kind in ("documents", "symbols", "occurrences", "edges")
    )
    if fact_count > request["limits"]["max_facts"]:
        raise AdapterError("semantic facts exceed max_facts")
    writer = ProtocolWriter(output, request)
    writer.run_begin()
    for facts in units:
        writer.unit(facts)
    writer.run_end([facts["unit"]["id"] for facts in units])


def _read_request(input_stream):
    try:
        request = json.load(input_stream)
    except json.JSONDecodeError as error:
        raise AdapterError("invalid index request: {}".format(error))
    _object_fields(
        request,
        {
            "protocol",
            "request_id",
            "repository_root",
            "repository_identity",
            "variant",
            "changed_paths",
            "environment",
            "permissions",
            "limits",
        },
        "request",
    )
    if request.get("protocol") != PROTOCOL:
        raise AdapterError("unsupported protocol")
    for field in ("request_id", "repository_root"):
        if not isinstance(request.get(field), str) or not request[field]:
            raise AdapterError("{} must be a nonempty string".format(field))
    _object_fields(
        request.get("permissions"),
        {"network", "restore", "build_tool", "run_generators"},
        "permissions",
    )
    if any(not isinstance(value, bool) for value in request["permissions"].values()):
        raise AdapterError("permission values must be boolean")
    limits = request.get("limits")
    _object_fields(
        limits,
        {"max_frame_bytes", "max_total_bytes", "max_frames", "max_facts"},
        "limits",
    )
    for name in ("max_frame_bytes", "max_total_bytes", "max_frames", "max_facts"):
        if not isinstance(limits.get(name), int) or limits[name] <= 0:
            raise AdapterError("limits.{} must be a positive integer".format(name))
    changed = request.get("changed_paths", [])
    if not isinstance(changed, list) or any(not isinstance(path, str) for path in changed):
        raise AdapterError("changed_paths must contain strings")
    environment = request.get("environment", {})
    if not isinstance(environment, dict) or any(
        not isinstance(key, str) or not isinstance(value, str)
        for key, value in environment.items()
    ):
        raise AdapterError("environment must map strings to strings")
    return request


def _object_fields(value, allowed, name):
    if not isinstance(value, dict):
        raise AdapterError("{} must be an object".format(name))
    unknown = sorted(set(value) - allowed)
    if unknown:
        raise AdapterError("{} has unknown fields: {}".format(name, ", ".join(unknown)))


def _git_python_files(root):
    command = [
        "git",
        "-c",
        "core.fsmonitor=false",
        "-C",
        str(root),
        "ls-files",
        "-co",
        "--exclude-standard",
        "-z",
        "--",
    ]
    returncode, stdout, stderr = _run_bounded(
        command, MAX_GIT_INVENTORY, MAX_GIT_STDERR
    )
    if returncode != 0:
        message = stderr.decode("utf-8", "replace").strip()
        raise AdapterError("git file inventory failed: {}".format(message))
    result = []
    for encoded in stdout.split(b"\0"):
        if not encoded:
            continue
        try:
            relative = encoded.decode("utf-8")
        except UnicodeDecodeError as error:
            raise AdapterError("Python path is not UTF-8: {}".format(error))
        relative = PurePosixPath(relative).as_posix()
        if not relative.endswith(".py"):
            continue
        if not _local_path(relative):
            raise AdapterError("git returned unsafe path {!r}".format(relative))
        candidate = root / Path(relative)
        if candidate.is_symlink():
            raise AdapterError("Python source is a symlink: {}".format(relative))
        full = candidate.resolve(strict=True)
        try:
            full.relative_to(root)
        except ValueError:
            raise AdapterError("Python source escapes repository: {}".format(relative))
        if full.is_file():
            result.append(relative)
    return sorted(set(result))


def _run_bounded(command, stdout_limit, stderr_limit):
    process = subprocess.Popen(
        command, stdout=subprocess.PIPE, stderr=subprocess.PIPE
    )
    values = {"stdout": bytearray(), "stderr": bytearray()}
    exceeded = []

    def drain(name, stream, limit):
        while True:
            chunk = stream.read(64 << 10)
            if not chunk:
                return
            target = values[name]
            remaining = limit - len(target)
            if remaining < len(chunk):
                target.extend(chunk[: max(0, remaining)])
                exceeded.append((name, limit))
                try:
                    process.kill()
                except OSError:
                    pass
                return
            target.extend(chunk)

    threads = [
        threading.Thread(target=drain, args=("stdout", process.stdout, stdout_limit)),
        threading.Thread(target=drain, args=("stderr", process.stderr, stderr_limit)),
    ]
    started = []
    try:
        for thread in threads:
            thread.start()
            started.append(thread)
        returncode = process.wait()
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
        for thread in started:
            thread.join()
        process.stdout.close()
        process.stderr.close()
    if exceeded:
        name, limit = exceeded[0]
        raise AdapterError("git {} exceeds {} bytes".format(name, limit))
    return returncode, bytes(values["stdout"]), bytes(values["stderr"])


def _local_path(value):
    path = PurePosixPath(value)
    return bool(value) and not path.is_absolute() and ".." not in path.parts


def _module_inventory(paths):
    path_set = set(paths)
    modules = {}
    module_paths = {}
    for value in paths:
        path = PurePosixPath(value)
        directory = list(path.parent.parts) if str(path.parent) != "." else []
        if path.name == "__init__.py":
            components = directory
        else:
            components = directory + [path.stem]
        package_start = len(directory)
        while package_start > 0:
            marker = PurePosixPath(*directory[:package_start], "__init__.py").as_posix()
            if marker not in path_set:
                break
            package_start -= 1
        if directory and package_start < len(directory):
            components = components[package_start:]
        module = ".".join(component for component in components if component)
        if not module:
            module = "__root__"
        previous = module_paths.get(module)
        if previous is not None:
            raise AdapterError(
                "Python module {!r} is provided by both {!r} and {!r}".format(
                    module, previous, value
                )
            )
        modules[value] = module
        module_paths[module] = value
    return modules


class Source:
    def __init__(self, root, relative):
        self.root = root
        self.relative = relative
        self.path = root / Path(relative)
        info = os.lstat(self.path)
        if stat.S_ISLNK(info.st_mode):
            raise AdapterError("Python source is a symlink: {}".format(relative))
        if not stat.S_ISREG(info.st_mode):
            raise AdapterError(
                "Python source is not a regular file: {}".format(relative)
            )
        if info.st_size > MAX_SOURCE_BYTES:
            raise AdapterError("{} exceeds {} bytes".format(relative, MAX_SOURCE_BYTES))
        with self.path.open("rb") as source:
            self.content = source.read(MAX_SOURCE_BYTES + 1)
        if len(self.content) > MAX_SOURCE_BYTES:
            raise AdapterError("{} exceeds {} bytes".format(relative, MAX_SOURCE_BYTES))
        encoding, _ = tokenize.detect_encoding(io.BytesIO(self.content).readline)
        normalized = encoding.lower().replace("_", "-")
        if normalized not in ("utf-8", "utf8") or self.content.startswith(b"\xef\xbb\xbf"):
            raise AdapterError(
                "{} uses unsupported source encoding {}; UTF-8 without BOM is required".format(
                    relative, encoding
                )
            )
        self.text = self.content.decode("utf-8")
        self.lines = self.text.splitlines(keepends=True)
        self.line_bytes = self.content.splitlines(keepends=True)
        if not self.lines:
            self.lines = [""]
            self.line_bytes = [b""]
        self.offsets = []
        offset = 0
        for line in self.line_bytes:
            self.offsets.append(offset)
            offset += len(line)
        self.tokens = {}
        try:
            for token in tokenize.generate_tokens(io.StringIO(self.text).readline):
                if token.type != tokenize.NAME:
                    continue
                key = (token.start[0], unicodedata.normalize("NFKC", token.string))
                self.tokens.setdefault(key, []).append(token)
        except tokenize.TokenError as error:
            raise AdapterError("{} cannot be tokenized: {}".format(relative, error))

    def ast_range(self, node):
        if not hasattr(node, "lineno"):
            return self.byte_range(1, 0, 1, 0)
        end_line = getattr(node, "end_lineno", node.lineno)
        end_column = getattr(node, "end_col_offset", node.col_offset)
        return self.byte_range(node.lineno, node.col_offset, end_line, end_column)

    def name_range(self, node, name):
        candidates = self.tokens.get((node.lineno, name), [])
        minimum = self._character_column(node.lineno, node.col_offset)
        for token in candidates:
            if token.start[1] >= minimum:
                return self.token_range(token)
        return self.ast_range(node)

    def token_range(self, token):
        start = len(self.lines[token.start[0] - 1][: token.start[1]].encode("utf-8"))
        end = len(self.lines[token.end[0] - 1][: token.end[1]].encode("utf-8"))
        return self.byte_range(token.start[0], start, token.end[0], end)

    def _character_column(self, line, byte_column):
        prefix = self.line_bytes[line - 1][:byte_column]
        return len(prefix.decode("utf-8"))

    def byte_range(self, start_line, start_column, end_line, end_column):
        return {
            "start": {
                "line": start_line - 1,
                "column": start_column,
                "byte": self.offsets[start_line - 1] + start_column,
            },
            "end": {
                "line": end_line - 1,
                "column": end_column,
                "byte": self.offsets[end_line - 1] + end_column,
            },
        }


def _table_type(table):
    value = table.get_type()
    return getattr(value, "value", value)


def _table_matches(table, expected_type, name, line):
    return (
        _table_type(table) == expected_type
        and table.get_name() == name
        and table.get_lineno() == line
    )


def _nested_table(table, expected_type, name, line):
    transparent = {
        "annotation",
        "type alias",
        "type parameter",
        "type parameters",
        "type variable",
    }
    if _table_type(table) not in transparent:
        return None
    for child in table.get_children():
        if _table_matches(child, expected_type, name, line):
            return child
        nested = _nested_table(child, expected_type, name, line)
        if nested is not None:
            return nested
    return None


class Scope:
    def __init__(
        self,
        table,
        parent,
        key,
        owner_symbol,
        callable_symbol,
        synthetic_locals=None,
    ):
        self.table = table
        self.parent = parent
        self.key = key
        self.owner_symbol = owner_symbol
        self.callable_symbol = callable_symbol
        self.synthetic_locals = set(synthetic_locals or ())
        self.bindings = {}
        self.children = list(table.get_children()) if table is not None else []

    def take_child(self, expected_type, name, line):
        for index, child in enumerate(self.children):
            if _table_matches(child, expected_type, name, line):
                return self.children.pop(index)
            nested = _nested_table(child, expected_type, name, line)
            if nested is not None:
                self.children.pop(index)
                return nested
        if self.table is None and self.parent is not None:
            return self.parent.take_child(expected_type, name, line)
        raise AdapterError(
            "compiler symbol table has no {} scope {} at line {}".format(
                expected_type, name, line
            )
        )

    def binding_scope(self, name):
        if self.parent is None:
            return self
        if self.table is None:
            if name in self.synthetic_locals:
                return self
            parent = self.parent
            # PEP 709 inlines comprehensions, so recent symtable versions no
            # longer expose their logical scope. A comprehension nested in a
            # class still skips the class namespace for free-name lookup.
            while (
                parent is not None
                and parent.table is not None
                and _table_type(parent.table) == "class"
            ):
                parent = parent.parent
            return parent.binding_scope(name) if parent is not None else self
        try:
            symbol = self.table.lookup(name)
        except KeyError:
            return self
        if symbol.is_global():
            scope = self
            while scope.parent is not None:
                scope = scope.parent
            return scope
        if symbol.is_nonlocal() or symbol.is_free():
            scope = self.parent
            while scope is not None:
                if name in scope.bindings:
                    return scope
                scope = scope.parent
        return self

    def resolve(self, name):
        target = self.binding_scope(name)
        if name in target.bindings:
            return target.bindings[name]
        scope = target.parent
        while scope is not None:
            if name in scope.bindings:
                return scope.bindings[name]
            scope = scope.parent
        return None


class Analysis:
    def __init__(self, source, repository, requested_variant, modules):
        self.source = source
        self.repository = repository
        self.module = modules[source.relative]
        self.modules = modules
        self.module_symbols = {
            name: _id("symbol", "python-module", repository, name)
            for name in set(modules.values())
        }
        self.version = provider_version()
        self.variant = "{}-{}.{}.{}:{}".format(
            sys.implementation.name,
            sys.version_info.major,
            sys.version_info.minor,
            sys.version_info.micro,
            requested_variant,
        )
        self.identity_variant = requested_variant
        self.unit_id = _id("unit", repository, source.relative, self.variant)
        self.document_id = _id("document", self.unit_id, source.relative)
        self.module_symbol = self.module_symbols[self.module]
        self.symbols = {}
        self.symbol_kinds = {}
        self.occurrences = {}
        self.edges = {}
        self.scopes = {}
        self.tree = ast.parse(source.text, filename=source.relative, type_comments=True)
        table = symtable.symtable(source.text, source.relative, "exec")
        self.root_scope = Scope(
            table, None, self.module, self.module_symbol, self.module_symbol
        )
        self.scopes[id(self.tree)] = self.root_scope

    def run(self):
        self._add_module()
        Definitions(self).visit(self.tree)
        References(self).visit(self.tree)
        documents = [
            {
                "id": self.document_id,
                "unit_id": self.unit_id,
                "path": self.source.relative,
                "language": "python",
                "content_hash": "sha256:" + hashlib.sha256(self.source.content).hexdigest(),
                "provider": PROVIDER,
                "provider_version": self.version,
            }
        ]
        symbols = sorted(self.symbols.values(), key=lambda value: value["id"])
        occurrences = sorted(self.occurrences.values(), key=lambda value: value["id"])
        edges = sorted(self.edges.values(), key=lambda value: value["id"])
        inventory = {
            "documents": documents,
            "symbols": symbols,
            "occurrences": occurrences,
            "edges": edges,
        }
        unit = {
            "id": self.unit_id,
            "provider": PROVIDER,
            "provider_version": self.version,
            "language": "python",
            "variant": self.variant,
            "input_fingerprint": _fingerprint(
                "weave-python-input/v1",
                self.version,
                self.variant,
                self.source.relative,
                self.module,
                _json(sorted(self.modules.items())),
                self.source.content,
            ),
            "surface_fingerprint": _fingerprint(
                # Python's exported surface is dynamic. Hashing the complete
                # module is deliberately conservative: implementation-only
                # edits can invalidate dependants, but signature and dynamic
                # export changes can never be silently missed.
                "weave-python-surface/v1",
                self.module,
                self.source.content,
            ),
            "inventory_digest": _fingerprint(
                "weave-python-inventory/v1", _json(inventory)
            ),
        }
        return {"unit": unit, **inventory}

    def _add_module(self):
        location = self.source.byte_range(1, 0, 1, 0)
        self.symbols[self.module_symbol] = {
            "id": self.module_symbol,
            "unit_id": self.unit_id,
            "stable_name": "python {} module {}".format(self.repository, self.module),
            "display_name": self.module,
            "normalized_name": self.module.lower(),
            "kind": "module",
            "document_id": self.document_id,
            "definition": location,
            "provider": PROVIDER,
            "evidence": "exact",
        }

    def define(self, scope, name, location, kind):
        target_scope = scope.binding_scope(name)
        symbol_id = target_scope.bindings.get(name)
        if symbol_id is None:
            # Interpreter and adapter patch versions invalidate facts through
            # the unit fingerprint, but must not churn public symbol identity.
            symbol_id = _id(
                "symbol",
                "python",
                self.repository,
                self.identity_variant,
                target_scope.key,
                name,
            )
            target_scope.bindings[name] = symbol_id
            stable = "python {} {} {}".format(self.repository, target_scope.key, name)
            self.symbols[symbol_id] = {
                "id": symbol_id,
                "unit_id": self.unit_id,
                "stable_name": stable,
                "display_name": name,
                "normalized_name": unicodedata.normalize("NFKC", name).casefold(),
                "kind": kind,
                "document_id": self.document_id,
                "definition": location,
                "provider": PROVIDER,
                "evidence": "exact",
            }
            self.symbol_kinds[symbol_id] = {kind}
        else:
            kinds = self.symbol_kinds[symbol_id]
            kinds.add(kind)
            if len(kinds) > 1:
                self.symbols[symbol_id]["kind"] = "binding"
        self._occurrence("definition", symbol_id, location, "exact")
        self._edge(target_scope.owner_symbol, symbol_id, "defines", location, "exact")
        return symbol_id

    def reference(self, scope, name, location):
        symbol_id = scope.resolve(name)
        if symbol_id is None:
            return None
        self._occurrence("reference", symbol_id, location, "exact")
        self._edge(scope.callable_symbol, symbol_id, "references", location, "exact")
        return symbol_id

    def contains(self, owner, target, location):
        self._edge(owner, target, "contains", location, "exact")

    def imported(self, scope, module, location):
        target = self.module_symbols.get(module)
        if target is None:
            target = _id("external-symbol", "python-module", module)
        self._edge(scope.callable_symbol, target, "imports", location, "declared")
        self._edge(scope.callable_symbol, target, "depends-on", location, "declared")

    def called(self, scope, target, location):
        if target is not None:
            self._edge(scope.callable_symbol, target, "calls", location, "syntactic")

    def _occurrence(self, role, symbol, location, evidence):
        occurrence_id = _id(
            "occurrence", role, symbol, self.document_id, _json(location)
        )
        self.occurrences[occurrence_id] = {
            "id": occurrence_id,
            "unit_id": self.unit_id,
            "symbol_id": symbol,
            "document_id": self.document_id,
            "role": role,
            "range": location,
            "provider": PROVIDER,
            "evidence": evidence,
        }

    def _edge(self, source, target, kind, location, evidence):
        edge_id = _id("edge", kind, source, target, self.document_id, _json(location))
        self.edges[edge_id] = {
            "id": edge_id,
            "unit_id": self.unit_id,
            "from": source,
            "to": target,
            "kind": kind,
            "evidence": evidence,
            "document_id": self.document_id,
            "range": location,
            "provider": PROVIDER,
        }

    def child_scope(self, parent, node, expected_type, name, owner, callable_symbol):
        table = parent.take_child(expected_type, name, node.lineno)
        key = "{}.<{}@{}:{}>".format(
            parent.key, name, node.lineno, node.col_offset
        )
        scope = Scope(table, parent, key, owner, callable_symbol)
        self.scopes[id(node)] = scope
        return scope

    def type_parameter_scope(self, parent, node, name, owner):
        parameters = list(getattr(node, "type_params", ()))
        if not parameters:
            return parent
        table = None
        for index, child in enumerate(parent.children):
            if (
                _table_type(child) in ("type parameter", "type parameters")
                and child.get_name() == name
                and child.get_lineno() == node.lineno
            ):
                table = parent.children.pop(index)
                break
        if table is None:
            raise AdapterError(
                "compiler symbol table has no type-parameter scope {} at line {}".format(
                    name, node.lineno
                )
            )
        key = "{}.<{} type-parameters@{}:{}>".format(
            parent.key, name, node.lineno, node.col_offset
        )
        scope = Scope(table, parent, key, owner, owner)
        for parameter in parameters:
            parameter_name = parameter.name
            location = self.source.name_range(parameter, parameter_name)
            symbol = self.define(
                scope, parameter_name, location, "type-parameter"
            )
            self.contains(owner, symbol, location)
        return scope

    def comprehension_scope(self, parent, node, name):
        table = None
        for index, child in enumerate(parent.children):
            if _table_matches(child, "function", name, node.lineno):
                table = parent.children.pop(index)
                break
        locals_ = {
            value.id
            for generator in node.generators
            for value in ast.walk(generator.target)
            if isinstance(value, ast.Name)
        }
        key = "{}.<{}@{}:{}>".format(
            parent.key, name, node.lineno, node.col_offset
        )
        scope = Scope(
            table,
            parent,
            key,
            parent.owner_symbol,
            parent.callable_symbol,
            synthetic_locals=locals_ if table is None else None,
        )
        self.scopes[id(node)] = scope
        return scope

    def relative_import(self, module, level):
        if level == 0:
            return module or ""
        parts = self.module.split(".")
        if not self.source.relative.endswith("/__init__.py") and len(parts) > 1:
            parts = parts[:-1]
        remove = max(0, level - 1)
        if remove:
            parts = parts[: max(0, len(parts) - remove)]
        if module:
            parts.extend(module.split("."))
        return ".".join(parts)


class ScopedVisitor(ast.NodeVisitor):
    def __init__(self, analysis):
        self.analysis = analysis
        self.scope = analysis.root_scope

    def in_scope(self, scope, values):
        previous = self.scope
        self.scope = scope
        try:
            for value in values:
                self.visit(value)
        finally:
            self.scope = previous

    def visit_comprehension_scope(self, node, name, results):
        child = self.analysis.scopes.get(id(node))
        if child is None:
            child = self.analysis.comprehension_scope(self.scope, node, name)
        generators = node.generators
        if not generators:
            self.in_scope(child, results)
            return
        # Python evaluates the outermost iterable in the enclosing scope. The
        # targets, filters, remaining generators, and result run in the logical
        # comprehension scope, including on PEP 709 runtimes that inline it.
        self.visit(generators[0].iter)
        inner = [generators[0].target] + list(generators[0].ifs)
        for generator in generators[1:]:
            inner.extend([generator.iter, generator.target])
            inner.extend(generator.ifs)
        inner.extend(results)
        self.in_scope(child, inner)

    def visit_ListComp(self, node):
        self.visit_comprehension_scope(node, "listcomp", [node.elt])

    def visit_SetComp(self, node):
        self.visit_comprehension_scope(node, "setcomp", [node.elt])

    def visit_DictComp(self, node):
        self.visit_comprehension_scope(node, "dictcomp", [node.key, node.value])

    def visit_GeneratorExp(self, node):
        self.visit_comprehension_scope(node, "genexpr", [node.elt])


class Definitions(ScopedVisitor):
    def visit_Module(self, node):
        self.in_scope(self.analysis.root_scope, node.body)

    def _function(self, node):
        location = self.analysis.source.name_range(node, node.name)
        symbol = self.analysis.define(self.scope, node.name, location, "function")
        self.analysis.contains(self.scope.owner_symbol, symbol, location)
        outer = list(node.decorator_list) + list(node.args.defaults)
        outer += [value for value in node.args.kw_defaults if value is not None]
        if node.returns is not None and not getattr(node, "type_params", ()):
            outer.append(node.returns)
        self.in_scope(self.scope, outer)
        parent = self.analysis.type_parameter_scope(
            self.scope, node, node.name, symbol
        )
        child = self.analysis.child_scope(
            parent, node, "function", node.name, symbol, symbol
        )
        self._arguments(child, node.args)
        self.in_scope(child, node.body)

    def visit_FunctionDef(self, node):
        self._function(node)

    def visit_AsyncFunctionDef(self, node):
        self._function(node)

    def visit_ClassDef(self, node):
        location = self.analysis.source.name_range(node, node.name)
        symbol = self.analysis.define(self.scope, node.name, location, "class")
        self.analysis.contains(self.scope.owner_symbol, symbol, location)
        self.in_scope(self.scope, node.decorator_list)
        parent = self.analysis.type_parameter_scope(
            self.scope, node, node.name, symbol
        )
        self.in_scope(parent, node.bases)
        child = self.analysis.child_scope(
            parent,
            node,
            "class",
            node.name,
            symbol,
            self.scope.callable_symbol,
        )
        self.in_scope(child, node.body)

    def visit_TypeAlias(self, node):
        name = node.name.id
        location = self.analysis.source.name_range(node.name, name)
        symbol = self.analysis.define(self.scope, name, location, "type-alias")
        self.analysis.contains(self.scope.owner_symbol, symbol, location)
        parent = self.analysis.type_parameter_scope(self.scope, node, name, symbol)
        child = self.analysis.child_scope(
            parent, node, "type alias", name, symbol, symbol
        )
        self.analysis.scopes[id(node)] = child
        self.in_scope(child, [node.value])

    def visit_Lambda(self, node):
        child = self.analysis.child_scope(
            self.scope,
            node,
            "function",
            "lambda",
            self.scope.owner_symbol,
            self.scope.callable_symbol,
        )
        self._arguments(child, node.args)
        self.in_scope(child, [node.body])

    def _arguments(self, scope, arguments):
        values = list(arguments.posonlyargs) + list(arguments.args)
        if arguments.vararg is not None:
            values.append(arguments.vararg)
        values += list(arguments.kwonlyargs)
        if arguments.kwarg is not None:
            values.append(arguments.kwarg)
        for argument in values:
            self.analysis.define(
                scope,
                argument.arg,
                self.analysis.source.ast_range(argument),
                "parameter",
            )

    def visit_Name(self, node):
        if isinstance(node.ctx, ast.Store):
            self.analysis.define(
                self.scope,
                node.id,
                self.analysis.source.ast_range(node),
                "variable",
            )

    def visit_Import(self, node):
        for alias in node.names:
            name = alias.asname or alias.name.split(".")[0]
            anchor = alias if hasattr(alias, "lineno") else node
            location = self.analysis.source.name_range(anchor, name)
            self.analysis.define(self.scope, name, location, "import")
            self.analysis.imported(
                self.scope, alias.name, self.analysis.source.ast_range(anchor)
            )

    def visit_ImportFrom(self, node):
        module = self.analysis.relative_import(node.module, node.level)
        self.analysis.imported(self.scope, module, self.analysis.source.ast_range(node))
        for alias in node.names:
            if alias.name == "*":
                continue
            name = alias.asname or alias.name
            anchor = alias if hasattr(alias, "lineno") else node
            self.analysis.define(
                self.scope,
                name,
                self.analysis.source.name_range(anchor, name),
                "import",
            )

    def visit_ExceptHandler(self, node):
        if node.type is not None:
            self.visit(node.type)
        if node.name:
            self.analysis.define(
                self.scope,
                node.name,
                self.analysis.source.name_range(node, node.name),
                "variable",
            )
        self.in_scope(self.scope, node.body)

    def visit_MatchAs(self, node):
        pattern = getattr(node, "pattern", None)
        if pattern is not None:
            self.visit(pattern)
        name = getattr(node, "name", None)
        if name:
            self.analysis.define(
                self.scope,
                name,
                self.analysis.source.name_range(node, name),
                "variable",
            )

    def visit_MatchStar(self, node):
        if node.name:
            self.analysis.define(
                self.scope,
                node.name,
                self.analysis.source.name_range(node, node.name),
                "variable",
            )

    def visit_MatchMapping(self, node):
        for key in node.keys:
            self.visit(key)
        for pattern in node.patterns:
            self.visit(pattern)
        if node.rest:
            self.analysis.define(
                self.scope,
                node.rest,
                self.analysis.source.name_range(node, node.rest),
                "variable",
            )


class References(ScopedVisitor):
    def visit_Module(self, node):
        self.in_scope(self.analysis.root_scope, node.body)

    def _function(self, node):
        outer = list(node.decorator_list) + list(node.args.defaults)
        outer += [value for value in node.args.kw_defaults if value is not None]
        if node.returns is not None and not getattr(node, "type_params", ()):
            outer.append(node.returns)
        self.in_scope(self.scope, outer)
        self.in_scope(self.analysis.scopes[id(node)], node.body)

    def visit_FunctionDef(self, node):
        self._function(node)

    def visit_AsyncFunctionDef(self, node):
        self._function(node)

    def visit_ClassDef(self, node):
        self.in_scope(self.scope, node.decorator_list)
        child = self.analysis.scopes[id(node)]
        parent = child.parent
        self.in_scope(parent, node.bases)
        self.in_scope(self.analysis.scopes[id(node)], node.body)

    def visit_TypeAlias(self, node):
        self.in_scope(self.analysis.scopes[id(node)], [node.value])

    def visit_Lambda(self, node):
        self.in_scope(self.analysis.scopes[id(node)], [node.body])

    def visit_Name(self, node):
        if isinstance(node.ctx, ast.Load):
            self.analysis.reference(
                self.scope, node.id, self.analysis.source.ast_range(node)
            )

    def visit_Call(self, node):
        self.generic_visit(node)
        if isinstance(node.func, ast.Name):
            target = self.scope.resolve(node.func.id)
            self.analysis.called(
                self.scope, target, self.analysis.source.ast_range(node.func)
            )


class ProtocolWriter:
    def __init__(self, output, request):
        self.output = output
        self.request_id = request["request_id"]
        self.limits = request["limits"]
        self.frames = 0
        self.total = 0

    def run_begin(self):
        self.emit(
            "run.begin",
            {
                "provider": {"name": PROVIDER, "version": provider_version()},
                "fact_encoding": FACT_ENCODING,
            },
        )

    def unit(self, facts):
        self.emit("unit.begin", {"unit": facts["unit"]})
        batch = {"documents": [], "symbols": [], "occurrences": [], "edges": []}
        for kind in ("documents", "symbols", "occurrences", "edges"):
            for fact in facts[kind]:
                candidate = {key: list(value) for key, value in batch.items()}
                candidate[kind].append(fact)
                candidate = {key: value for key, value in candidate.items() if value}
                if self.encoded_size("facts", candidate) > self.limits["max_frame_bytes"]:
                    self._flush(batch)
                    batch = {"documents": [], "symbols": [], "occurrences": [], "edges": []}
                    batch[kind].append(fact)
                    if self.encoded_size("facts", {kind: [fact]}) > self.limits["max_frame_bytes"]:
                        raise AdapterError("one semantic fact exceeds max_frame_bytes")
                else:
                    batch[kind].append(fact)
        self._flush(batch)
        self.emit(
            "unit.end",
            {
                "status": "complete",
                "counts": {
                    "documents": len(facts["documents"]),
                    "symbols": len(facts["symbols"]),
                    "occurrences": len(facts["occurrences"]),
                    "edges": len(facts["edges"]),
                },
            },
        )

    def _flush(self, batch):
        payload = {key: value for key, value in batch.items() if value}
        if payload:
            self.emit("facts", payload)

    def run_end(self, units):
        self.emit("run.end", {"status": "complete", "units": units})

    def encoded_size(self, kind, payload):
        return len(self.encode(kind, payload))

    def encode(self, kind, payload):
        return (
            _json(
                {
                    "protocol": PROTOCOL,
                    "request_id": self.request_id,
                    "kind": kind,
                    "payload": payload,
                }
            )
            + "\n"
        ).encode("utf-8")

    def emit(self, kind, payload):
        encoded = self.encode(kind, payload)
        if len(encoded) > self.limits["max_frame_bytes"]:
            raise AdapterError("{} frame exceeds max_frame_bytes".format(kind))
        if self.frames + 1 > self.limits["max_frames"]:
            raise AdapterError("adapter response exceeds max_frames")
        if self.total + len(encoded) > self.limits["max_total_bytes"]:
            raise AdapterError("adapter response exceeds max_total_bytes")
        self.frames += 1
        self.total += len(encoded)
        self.output.write(encoded.decode("utf-8"))
        self.output.flush()


def _id(kind, *values):
    digest = hashlib.sha256()
    for value in (kind,) + values:
        if not isinstance(value, bytes):
            value = str(value).encode("utf-8")
        digest.update(len(value).to_bytes(8, "big"))
        digest.update(value)
    return "{}:{}".format(kind, digest.hexdigest())


def _fingerprint(domain, *values):
    return "sha256:" + _id(domain, *values).split(":", 1)[1]


def _json(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
