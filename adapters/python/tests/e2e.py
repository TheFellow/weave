"""Installed-adapter smoke test against the real Weave executable."""

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
    if len(sys.argv) != 2:
        raise SystemExit("usage: e2e.py /path/to/weave")
    weave = str(Path(sys.argv[1]).resolve())
    adapter = shutil.which("weave-python")
    if adapter is None:
        raise RuntimeError("installed weave-python executable is not on PATH")
    environment = dict(os.environ)
    environment["WEAVE_PYTHON_ADAPTER"] = adapter

    with tempfile.TemporaryDirectory() as temporary:
        root = Path(temporary)
        (root / "example.py").write_text(
            "VALUE = 1\n"
            "VALUE = 2\n\n"
            "def greet(name):\n"
            "    return name.strip()\n\n"
            "def run():\n"
            "    return greet('weave')\n",
            encoding="utf-8",
        )
        run(["git", "init", "--quiet"], root, environment)
        run(["git", "config", "user.email", "weave@example.test"], root, environment)
        run(["git", "config", "user.name", "Weave"], root, environment)
        run(["git", "add", "."], root, environment)
        run(["git", "commit", "--quiet", "-m", "fixture"], root, environment)

        definitions = json.loads(
            run([weave, "definition", "VALUE", "--json"], root, environment).stdout
        )
        occurrences = definitions.get("occurrences", [])
        if len(occurrences) != 2 or any(
            value["role"] != "definition" for value in occurrences
        ):
            raise RuntimeError("repeated definitions were not preserved: {}".format(definitions))

        exported = json.loads(
            run([weave, "export", "--json"], root, environment).stdout
        )["facts"]
        calls = [edge for edge in exported["edges"] if edge["kind"] == "calls"]
        references = [
            edge for edge in exported["edges"] if edge["kind"] == "references"
        ]
        if not calls or any(edge["evidence"] != "syntactic" for edge in calls):
            raise RuntimeError("call evidence is not syntactic: {}".format(calls))
        if not references or any(
            edge["evidence"] != "exact" for edge in references
        ):
            raise RuntimeError("lexical reference evidence is not exact: {}".format(references))

        with (root / "example.py").open("a", encoding="utf-8") as source:
            source.write("\ndef newly_added():\n    return VALUE\n")
        refreshed = json.loads(
            run([weave, "symbols", "newly_added", "--json"], root, environment).stdout
        )
        if not refreshed.get("symbols") or not refreshed["freshness"]["current"]:
            raise RuntimeError("dirty Python source did not refresh: {}".format(refreshed))


if __name__ == "__main__":
    main()
