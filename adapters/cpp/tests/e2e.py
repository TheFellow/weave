"""Real scip-clang -> weave-cpp -> Weave query smoke test."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


def run(arguments, cwd, environment):
    completed = subprocess.run(
        arguments,
        cwd=cwd,
        env=environment,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        encoding="utf-8",
    )
    if completed.returncode != 0:
        raise RuntimeError(
            "{} failed ({})\nstdout:\n{}\nstderr:\n{}".format(
                arguments, completed.returncode, completed.stdout, completed.stderr
            )
        )
    return completed


def main():
    if len(sys.argv) != 3:
        raise SystemExit("usage: e2e.py /path/to/weave /path/to/weave-cpp")
    weave = str(Path(sys.argv[1]).resolve())
    adapter = str(Path(sys.argv[2]).resolve())
    compiler = shutil.which("clang++")
    indexer = shutil.which("scip-clang")
    if compiler is None or indexer is None:
        raise RuntimeError("clang++ and scip-clang must be on PATH")
    environment = dict(os.environ)
    environment["WEAVE_CPP_ADAPTER"] = adapter

    fixture = Path(__file__).parent / "fixtures" / "geometry"
    with tempfile.TemporaryDirectory() as temporary:
        root = Path(temporary) / "cpp-project"
        shutil.copytree(fixture, root)
        source = root / "src" / "geometry.cpp"
        compilation_database = [
            {
                "directory": str(root),
                "arguments": [
                    compiler,
                    "-std=c++20",
                    "-I",
                    str(root / "include"),
                    "-c",
                    str(source),
                    "-o",
                    str(root / "geometry.o"),
                ],
                "file": str(source),
            }
        ]
        (root / "compile_commands.json").write_text(
            json.dumps(compilation_database, indent=2) + "\n", encoding="utf-8"
        )
        run(["git", "init", "--quiet"], root, environment)
        run(["git", "config", "user.email", "weave@example.test"], root, environment)
        run(["git", "config", "user.name", "Weave"], root, environment)
        run(["git", "add", "."], root, environment)
        run(["git", "commit", "--quiet", "-m", "fixture"], root, environment)

        run([weave, "index"], root, environment)
        symbols = json.loads(
            run([weave, "symbols", "Square", "--json"], root, environment).stdout
        ).get("symbols", [])
        semantic = [
            value for value in symbols if value.get("provider") == "scip:scip-clang"
        ]
        if not semantic or not any(value.get("kind") == "type" for value in semantic):
            raise RuntimeError("compiler-backed Square symbol was not queryable: {}".format(symbols))

        definitions = json.loads(
            run([weave, "definition", "scaled_area", "--json"], root, environment).stdout
        ).get("occurrences", [])
        if not any(
            value.get("role") == "definition"
            and value.get("provider") == "scip:scip-clang"
            for value in definitions
        ):
            raise RuntimeError("scaled_area definition was not preserved: {}".format(definitions))

        facts = json.loads(
            run([weave, "export", "--json"], root, environment).stdout
        )["facts"]
        cpp_documents = [
            value
            for value in facts["documents"]
            if value.get("provider") == "scip:scip-clang"
        ]
        references = [
            value
            for value in facts["occurrences"]
            if value.get("provider") == "scip:scip-clang"
            and value.get("role") == "reference"
        ]
        if not cpp_documents or not references:
            raise RuntimeError("SCIP documents/references missing from export")


if __name__ == "__main__":
    main()
