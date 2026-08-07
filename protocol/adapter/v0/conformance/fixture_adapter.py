#!/usr/bin/env python3
"""Black-box fixture adapter: deliberately shares no Weave implementation code."""

import json
import pathlib
import sys

PROTOCOL = "weave.adapter/v0"
PROVIDER = {"name": "fixture-python-adapter", "version": "1.0.0"}


def write(kind, request_id, payload):
    print(json.dumps({"protocol": PROTOCOL, "request_id": request_id, "kind": kind, "payload": payload}, separators=(",", ":"), sort_keys=True))


def main():
    if sys.argv[1:] == ["describe", "--protocol", PROTOCOL]:
        print(json.dumps({
            "protocols": [PROTOCOL], "provider": PROVIDER, "languages": ["fixture"],
            "operations": ["index"], "refresh_modes": ["full"], "fact_encoding": "weave.facts/v0",
            "position_encodings": ["utf8-byte"], "requires": {"executables": [], "may_run_build_tool": False},
            "claims": {"inputs": {"extensions": [".fixture"], "project_markers": ["fixture.project"]}, "evidence": ["exact"]},
        }, separators=(",", ":"), sort_keys=True))
        return 0
    if sys.argv[1:] != ["index", "--protocol", PROTOCOL]:
        print("unsupported operation or protocol", file=sys.stderr)
        return 2
    try:
        request = json.load(sys.stdin)
        root = pathlib.Path(request["repository_root"])
        request_id = request["request_id"]
        source = root / "src" / "main.fixture"
        if request.get("protocol") != PROTOCOL or not source.is_file():
            raise ValueError("invalid conformance request")
    except Exception as error:
        print(str(error), file=sys.stderr)
        return 2
    unit = {"id": "fixture-python:unit", "provider": PROVIDER["name"], "provider_version": PROVIDER["version"], "language": "fixture", "variant": "conformance", "input_fingerprint": "sha256:fixture-input", "surface_fingerprint": "sha256:fixture-surface", "inventory_digest": "sha256:fixture-inventory"}
    document = {"id": "fixture-python:document", "unit_id": unit["id"], "path": "src/main.fixture", "language": "fixture", "content_hash": "sha256:fixture-content", "provider": PROVIDER["name"], "provider_version": PROVIDER["version"]}
    symbol = {"id": "fixture-python:symbol", "unit_id": unit["id"], "stable_name": "fixture FixtureSymbol", "display_name": "FixtureSymbol", "normalized_name": "fixturesymbol", "kind": "function", "document_id": document["id"], "definition": {"start": {"line": 0, "column": 7, "byte": 7}, "end": {"line": 0, "column": 20, "byte": 20}}, "provider": PROVIDER["name"], "evidence": "exact"}
    write("run.begin", request_id, {"provider": PROVIDER, "fact_encoding": "weave.facts/v0"})
    write("unit.begin", request_id, {"unit": unit})
    write("facts", request_id, {"documents": [document], "symbols": [symbol]})
    write("diagnostic", request_id, {"severity": "info", "message": "fixture indexed", "unit_id": unit["id"]})
    write("unit.end", request_id, {"status": "complete", "counts": {"documents": 1, "symbols": 1, "occurrences": 0, "edges": 0}})
    write("run.end", request_id, {"status": "complete", "units": [unit["id"]]})
    print("fixture diagnostic", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
