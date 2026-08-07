"""Real scip-typescript -> weave-typescript -> Weave query smoke test."""

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
        raise SystemExit("usage: e2e.py /path/to/weave /path/to/weave-typescript")
    weave = str(Path(sys.argv[1]).resolve())
    adapter = str(Path(sys.argv[2]).resolve())
    indexer = shutil.which("scip-typescript")
    if indexer is None:
        raise RuntimeError("scip-typescript must be on PATH")
    environment = dict(os.environ)
    environment["WEAVE_SCIP_TYPESCRIPT"] = indexer

    fixture = Path(__file__).parent / "fixtures" / "polyglot"
    with tempfile.TemporaryDirectory() as temporary:
        root = Path(temporary) / "typescript-project"
        environment["WEAVE_ADAPTER_HOME"] = str(Path(temporary) / "adapter-state")
        run([weave, "adapters", "install", adapter], Path(temporary), environment)
        shutil.copytree(fixture, root)
        before = sorted(path.relative_to(root) for path in root.rglob("*"))
        run(["git", "init", "--quiet"], root, environment)
        run(["git", "config", "user.email", "weave@example.test"], root, environment)
        run(["git", "config", "user.name", "Weave"], root, environment)
        run(["git", "add", "."], root, environment)
        run(["git", "commit", "--quiet", "-m", "fixture"], root, environment)

        symbols = json.loads(
            run([weave, "symbols", "FriendlyGreeter", "--json"], root, environment).stdout
        ).get("symbols", [])
        semantic = [
            value
            for value in symbols
            if value.get("provider") == "scip:scip-typescript"
        ]
        if not semantic:
            raise RuntimeError(
                "TypeScript symbol was not queryable: {}".format(symbols)
            )

        definitions = json.loads(
            run([weave, "definition", "legacyGreeting", "--json"], root, environment).stdout
        ).get("occurrences", [])
        if not any(
            value.get("role") == "definition"
            and value.get("provider") == "scip:scip-typescript"
            for value in definitions
        ):
            raise RuntimeError(
                "JavaScript definition was not preserved: {}".format(definitions)
            )

        facts = json.loads(run([weave, "export", "--json"], root, environment).stdout)[
            "facts"
        ]
        documents = [
            value
            for value in facts["documents"]
            if value.get("provider") == "scip:scip-typescript"
        ]
        languages = {value.get("language") for value in documents}
        expected = {
            "javascript",
            "javascriptreact",
            "typescript",
            "typescriptreact",
        }
        if not expected.issubset(languages):
            raise RuntimeError(
                "JS/JSX/TS/TSX documents were not preserved: {}".format(documents)
            )
        references = [
            value
            for value in facts["occurrences"]
            if value.get("provider") == "scip:scip-typescript"
            and value.get("role") == "reference"
        ]
        if not references:
            raise RuntimeError("semantic references are missing from export")

        after = sorted(path.relative_to(root) for path in root.rglob("*") if ".git" not in path.parts)
        before_without_git = [path for path in before if ".git" not in path.parts]
        if after != before_without_git:
            raise RuntimeError(
                "adapter mutated the repository: before={} after={}".format(
                    before_without_git, after
                )
            )


if __name__ == "__main__":
    main()
