import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import weave_python.adapter as adapter_module

from weave_python.adapter import (
    AdapterError,
    Analysis,
    FACT_ENCODING,
    PROTOCOL,
    PROVIDER,
    Source,
    _module_inventory,
    _read_request,
    index,
    provider_version,
)


FIXTURE = Path(__file__).parent / "fixtures" / "sample"


def request(root, **extra):
    value = {
        "protocol": PROTOCOL,
        "request_id": "python-test",
        "repository_root": str(root),
        "repository_identity": "example.com/python-fixture",
        "variant": "test",
        "changed_paths": [],
        "environment": {},
        "permissions": {
            "network": False,
            "restore": False,
            "build_tool": False,
            "run_generators": False,
        },
        "limits": {
            "max_frame_bytes": 4096,
            "max_total_bytes": 4 << 20,
            "max_frames": 10000,
            "max_facts": 100000,
        },
    }
    value.update(extra)
    return value


class AnalysisTests(unittest.TestCase):
    def analyze(self, relative="package/service.py"):
        paths = sorted(
            path.relative_to(FIXTURE).as_posix()
            for path in FIXTURE.rglob("*.py")
        )
        modules = _module_inventory(paths)
        return Analysis(
            Source(FIXTURE, relative),
            "example.com/python-fixture",
            "test",
            modules,
        ).run()

    def test_compiler_bindings_and_evidence_are_honest(self):
        facts = self.analyze()
        symbols = {symbol["display_name"]: symbol for symbol in facts["symbols"]}
        self.assertEqual("exact", symbols["greet"]["evidence"])
        self.assertEqual("function", symbols["greet"]["kind"])
        self.assertEqual("class", symbols["Greeter"]["kind"])
        parameter = next(
            symbol
            for symbol in facts["symbols"]
            if symbol["display_name"] == "value"
            and "<normalize@" in symbol["stable_name"]
        )
        self.assertEqual("parameter", parameter["kind"])

        default_id = symbols["DEFAULT"]["id"]
        definitions = [
            value
            for value in facts["occurrences"]
            if value["symbol_id"] == default_id and value["role"] == "definition"
        ]
        self.assertEqual(2, len(definitions), definitions)

        references = [
            edge for edge in facts["edges"] if edge["kind"] == "references"
        ]
        calls = [edge for edge in facts["edges"] if edge["kind"] == "calls"]
        imports = [edge for edge in facts["edges"] if edge["kind"] == "imports"]
        self.assertTrue(references)
        self.assertTrue(calls)
        self.assertTrue(imports)
        self.assertTrue(all(edge["evidence"] == "exact" for edge in references))
        self.assertTrue(all(edge["evidence"] == "syntactic" for edge in calls))
        self.assertTrue(all(edge["evidence"] == "declared" for edge in imports))

        value = next(
            symbol
            for symbol in facts["symbols"]
            if symbol["display_name"] == "value" and "<outer@" in symbol["stable_name"]
        )
        self.assertTrue(
            any(
                occurrence["symbol_id"] == value["id"]
                and occurrence["role"] == "reference"
                for occurrence in facts["occurrences"]
            )
        )

        source_values = [
            symbol
            for symbol in facts["symbols"]
            if symbol["display_name"] == "source_values"
        ]
        self.assertEqual(2, len(source_values), source_values)
        module_value = next(
            symbol for symbol in source_values if "<listcomp@" not in symbol["stable_name"]
        )
        comprehension_value = next(
            symbol for symbol in source_values if "<listcomp@" in symbol["stable_name"]
        )
        referenced_ids = {
            occurrence["symbol_id"]
            for occurrence in facts["occurrences"]
            if occurrence["role"] == "reference"
        }
        self.assertIn(module_value["id"], referenced_ids)
        self.assertIn(comprehension_value["id"], referenced_ids)

        lambda_parameters = [
            symbol
            for symbol in facts["symbols"]
            if symbol["display_name"] == "item" and "<lambda@" in symbol["stable_name"]
        ]
        self.assertEqual(2, len(lambda_parameters), lambda_parameters)
        self.assertEqual(2, len({symbol["id"] for symbol in lambda_parameters}))

    def test_utf8_columns_and_output_are_deterministic(self):
        first = self.analyze()
        second = self.analyze()
        self.assertEqual(first, second)
        pi = next(symbol for symbol in first["symbols"] if symbol["display_name"] == "π")
        self.assertEqual(2, pi["definition"]["end"]["column"])
        self.assertEqual(
            first["unit"]["inventory_digest"], second["unit"]["inventory_digest"]
        )
        self.assertIn(".code.", provider_version())

    def test_nfkc_normalized_identifiers_keep_exact_token_ranges(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "normalized.py").write_text(
                "def K():\n    return 1\nclass Å:\n    pass\n",
                encoding="utf-8",
            )
            facts = Analysis(
                Source(root, "normalized.py"),
                "example.com/python-fixture",
                "test",
                {"normalized.py": "normalized"},
            ).run()
        for name in ("K", "Å"):
            symbol = next(
                value for value in facts["symbols"] if value["display_name"] == name
            )
            location = symbol["definition"]
            self.assertEqual(3, location["end"]["byte"] - location["start"]["byte"])

    def test_class_comprehension_skips_class_namespace(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "scope.py").write_text(
                'x = "module"\nclass C:\n    x = "class"\n    class Nested:\n'
                '        y = [x for _ in (1,)]\n',
                encoding="utf-8",
            )
            facts = Analysis(
                Source(root, "scope.py"),
                "example.com/python-fixture",
                "test",
                {"scope.py": "scope"},
            ).run()
        xs = [value for value in facts["symbols"] if value["display_name"] == "x"]
        module_x = next(value for value in xs if ".<C@" not in value["stable_name"])
        class_x = next(value for value in xs if ".<C@" in value["stable_name"])
        references = {
            value["symbol_id"]
            for value in facts["occurrences"]
            if value["role"] == "reference"
        }
        self.assertIn(module_x["id"], references)
        self.assertNotIn(class_x["id"], references)

    def test_fingerprints_include_topology_and_complete_source_surface(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            path = root / "src" / "pkg" / "m.py"
            path.parent.mkdir(parents=True)
            path.write_text("def public(value):\n    return value\n", encoding="utf-8")
            before = Analysis(
                Source(root, "src/pkg/m.py"),
                "example.com/python-fixture",
                "test",
                _module_inventory(["src/pkg/m.py"]),
            ).run()["unit"]
            topology = Analysis(
                Source(root, "src/pkg/m.py"),
                "example.com/python-fixture",
                "test",
                _module_inventory(["src/pkg/__init__.py", "src/pkg/m.py"]),
            ).run()["unit"]
            path.write_text(
                "def public(value, other=None):\n    return value\n", encoding="utf-8"
            )
            changed = Analysis(
                Source(root, "src/pkg/m.py"),
                "example.com/python-fixture",
                "test",
                _module_inventory(["src/pkg/m.py"]),
            ).run()["unit"]
        self.assertNotEqual(before["input_fingerprint"], topology["input_fingerprint"])
        self.assertNotEqual(before["surface_fingerprint"], changed["surface_fingerprint"])

    def test_duplicate_module_names_are_rejected(self):
        with self.assertRaisesRegex(AdapterError, "provided by both"):
            _module_inventory(["foo.py", "foo/__init__.py"])

    def test_delete_is_not_a_definition(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "delete.py").write_text("value = 1\ndel value\n", encoding="utf-8")
            facts = Analysis(
                Source(root, "delete.py"),
                "example.com/python-fixture",
                "test",
                {"delete.py": "delete"},
            ).run()
        value = next(
            symbol for symbol in facts["symbols"] if symbol["display_name"] == "value"
        )
        definitions = [
            occurrence
            for occurrence in facts["occurrences"]
            if occurrence["symbol_id"] == value["id"]
            and occurrence["role"] == "definition"
        ]
        self.assertEqual(1, len(definitions))

    @unittest.skipUnless(sys.version_info >= (3, 10), "match requires Python 3.10")
    def test_match_star_and_mapping_rest_are_bindings(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "patterns.py").write_text(
                "match value:\n    case [*rest]:\n        print(rest)\n"
                "    case {**remaining}:\n        print(remaining)\n",
                encoding="utf-8",
            )
            facts = Analysis(
                Source(root, "patterns.py"),
                "example.com/python-fixture",
                "test",
                {"patterns.py": "patterns"},
            ).run()
        symbols = {value["display_name"]: value for value in facts["symbols"]}
        self.assertIn("rest", symbols)
        self.assertIn("remaining", symbols)
        referenced = {
            value["symbol_id"]
            for value in facts["occurrences"]
            if value["role"] == "reference"
        }
        self.assertIn(symbols["rest"]["id"], referenced)
        self.assertIn(symbols["remaining"]["id"], referenced)

    @unittest.skipUnless(sys.version_info >= (3, 12), "PEP 695 requires Python 3.12")
    def test_type_parameter_scopes_are_exact_bindings(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "generic.py").write_text(
                'T = "outer"\n'
                "def identity[T](value=T) -> T:\n    return T\n"
                "class Box[U](list[U]):\n    item = U\n"
                "type Vector[V] = list[V]\n",
                encoding="utf-8",
            )
            facts = Analysis(
                Source(root, "generic.py"),
                "example.com/python-fixture",
                "test",
                {"generic.py": "generic"},
            ).run()
        symbols = facts["symbols"]
        self.assertTrue(any(symbol["display_name"] == "identity" for symbol in symbols))
        self.assertTrue(any(symbol["display_name"] == "Box" for symbol in symbols))
        self.assertTrue(
            any(symbol["display_name"] == "Vector" and symbol["kind"] == "type-alias" for symbol in symbols)
        )
        parameters = [symbol for symbol in symbols if symbol["kind"] == "type-parameter"]
        self.assertEqual({"T", "U", "V"}, {symbol["display_name"] for symbol in parameters})
        referenced = {
            occurrence["symbol_id"]
            for occurrence in facts["occurrences"]
            if occurrence["role"] == "reference"
        }
        self.assertTrue(all(symbol["id"] in referenced for symbol in parameters))


class ProtocolTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        shutil.copytree(FIXTURE, self.root, dirs_exist_ok=True)
        (self.root / ".gitignore").write_text("ignored.py\n", encoding="utf-8")
        (self.root / "ignored.py").write_text("secret = True\n", encoding="utf-8")
        subprocess.run(
            ["git", "init", "--quiet"], cwd=self.root, check=True, stdout=subprocess.PIPE
        )

    def tearDown(self):
        self.temporary.cleanup()

    def test_index_emits_complete_bounded_lifecycle(self):
        output = io.StringIO()
        index(io.StringIO(json.dumps(request(self.root))), output)
        frames = [json.loads(line) for line in output.getvalue().splitlines()]
        self.assertEqual("run.begin", frames[0]["kind"])
        self.assertEqual("run.end", frames[-1]["kind"])
        self.assertEqual(PROTOCOL, frames[0]["protocol"])
        self.assertEqual(PROVIDER, frames[0]["payload"]["provider"]["name"])
        self.assertEqual(FACT_ENCODING, frames[0]["payload"]["fact_encoding"])
        self.assertTrue(all(frame["request_id"] == "python-test" for frame in frames))
        paths = [
            document["path"]
            for frame in frames
            if frame["kind"] == "facts"
            for document in frame["payload"].get("documents", [])
        ]
        self.assertIn("package/service.py", paths)
        self.assertNotIn("ignored.py", paths)
        self.assertLessEqual(
            max(len((json.dumps(frame, separators=(",", ":")) + "\n").encode()) for frame in frames),
            request(self.root)["limits"]["max_frame_bytes"],
        )

    def test_unknown_request_fields_fail_closed(self):
        value = request(self.root)
        value["surprise"] = True
        with self.assertRaisesRegex(AdapterError, "unknown fields"):
            _read_request(io.StringIO(json.dumps(value)))

    @unittest.skipIf(os.name == "nt", "fsmonitor fixture uses a POSIX script")
    def test_git_inventory_does_not_execute_repository_fsmonitor(self):
        marker = self.root / "fsmonitor-ran"
        hook = self.root / "fsmonitor.sh"
        hook.write_text(
            "#!/bin/sh\nprintf ran > {!r}\n".format(str(marker)), encoding="utf-8"
        )
        hook.chmod(0o755)
        subprocess.run(
            ["git", "config", "core.fsmonitor", str(hook)],
            cwd=self.root,
            check=True,
        )
        adapter_module._git_python_files(self.root.resolve())
        self.assertFalse(marker.exists())

    def test_total_source_limit_fails_before_protocol_output(self):
        output = io.StringIO()
        with mock.patch.object(adapter_module, "MAX_TOTAL_SOURCE_BYTES", 1):
            with self.assertRaisesRegex(AdapterError, "sources exceed"):
                index(io.StringIO(json.dumps(request(self.root))), output)
        self.assertEqual("", output.getvalue())

    def test_relative_repository_root_is_rejected(self):
        value = request(self.root)
        value["repository_root"] = "."
        output = io.StringIO()
        with self.assertRaisesRegex(AdapterError, "absolute directory"):
            index(io.StringIO(json.dumps(value)), output)
        self.assertEqual("", output.getvalue())

    def test_inventory_limit_is_enforced_while_reading(self):
        output = io.StringIO()
        with mock.patch.object(adapter_module, "MAX_GIT_INVENTORY", 1):
            with self.assertRaisesRegex(AdapterError, "git stdout exceeds"):
                index(io.StringIO(json.dumps(request(self.root))), output)
        self.assertEqual("", output.getvalue())

    def test_per_source_limit_is_enforced_while_reading(self):
        with mock.patch.object(adapter_module, "MAX_SOURCE_BYTES", 1):
            with self.assertRaisesRegex(AdapterError, "exceeds 1 bytes"):
                Source(self.root, "package/service.py")

    @unittest.skipIf(os.name == "nt", "symlink creation may require privileges")
    def test_python_source_symlink_is_rejected(self):
        (self.root / "target.txt").write_text("value = 1\n", encoding="utf-8")
        (self.root / "link.py").symlink_to("target.txt")
        output = io.StringIO()
        with self.assertRaisesRegex(AdapterError, "source is a symlink"):
            index(io.StringIO(json.dumps(request(self.root))), output)
        self.assertEqual("", output.getvalue())

    def test_syntax_failure_emits_no_partial_protocol(self):
        (self.root / "broken.py").write_text("def broken(:\n", encoding="utf-8")
        output = io.StringIO()
        with self.assertRaises((AdapterError, SyntaxError)):
            index(io.StringIO(json.dumps(request(self.root))), output)
        self.assertEqual("", output.getvalue())


if __name__ == "__main__":
    unittest.main()
