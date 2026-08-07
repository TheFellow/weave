"""Opt-in real scip-java -> weave-jvm -> Weave smoke test."""

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
        raise SystemExit("usage: e2e.py /path/to/weave /path/to/weave-jvm")
    weave = str(Path(sys.argv[1]).resolve())
    adapter = str(Path(sys.argv[2]).resolve())
    producer = shutil.which("scip-java")
    java = shutil.which("java")
    gradle = shutil.which("gradle")
    if producer is None or java is None or gradle is None:
        raise RuntimeError("scip-java v0.13.1, JDK 17+, and Gradle must be on PATH")

    environment = dict(os.environ)
    environment["WEAVE_SCIP_JAVA"] = producer
    fixture = Path(__file__).parent / "fixtures" / "mixed"
    with tempfile.TemporaryDirectory() as temporary:
        root = Path(temporary) / "jvm-project"
        shutil.copytree(fixture, root)
        run(["git", "init", "--quiet"], root, environment)
        run(["git", "config", "user.email", "weave@example.test"], root, environment)
        run(["git", "config", "user.name", "Weave"], root, environment)
        run(["git", "add", "."], root, environment)
        run(["git", "commit", "--quiet", "-m", "fixture"], root, environment)

        run(
            [
                weave,
                "index",
                "--adapter",
                adapter,
                "--adapter-arg=--build-tool=gradle",
                "--allow-build-tool",
                "--allow-restore",
                "--allow-network",
                "--allow-generators",
            ],
            root,
            environment,
        )
        symbols = json.loads(
            run([weave, "symbols", "FriendlyGreeter", "--json"], root, environment).stdout
        ).get("symbols", [])
        semantic = [
            symbol
            for symbol in symbols
            if symbol.get("provider") == "scip:scip-java"
        ]
        if not semantic or not any(
            symbol.get("kind") in {"class", "type"}
            and symbol.get("evidence") == "exact"
            for symbol in semantic
        ):
            raise RuntimeError(
                "compiler-backed Kotlin symbol was not queryable: {}".format(symbols)
            )

        facts = json.loads(run([weave, "export", "--json"], root, environment).stdout)[
            "facts"
        ]
        documents = [
            document
            for document in facts["documents"]
            if document.get("provider") == "scip:scip-java"
        ]
        implementations = [
            edge
            for edge in facts["edges"]
            if edge.get("provider") == "scip:scip-java"
            and edge.get("kind") == "implements"
            and edge.get("evidence") == "exact"
        ]
        languages = {document.get("language") for document in documents}
        if not {"java", "kotlin"}.issubset(languages) or not implementations:
            raise RuntimeError(
                "Java/Kotlin documents or implementation edge missing: {} / {}".format(
                    documents, implementations
                )
            )


if __name__ == "__main__":
    main()
