#!/usr/bin/env python3
"""Run one bounded command and record portable timing/result JSON."""

import argparse
import json
import subprocess
import sys
import time

try:
    import resource
except ImportError:  # pragma: no cover - unavailable on native Windows Python
    resource = None


parser = argparse.ArgumentParser()
parser.add_argument("--timeout", type=float, required=True)
parser.add_argument("--stdout", required=True)
parser.add_argument("--stderr", required=True)
parser.add_argument("--result", required=True)
parser.add_argument("command", nargs=argparse.REMAINDER)
args = parser.parse_args()
if args.command and args.command[0] == "--":
    args.command = args.command[1:]
if not args.command:
    parser.error("a command is required after --")

started = time.perf_counter()
timed_out = False
with open(args.stdout, "wb") as stdout, open(args.stderr, "wb") as stderr:
    try:
        completed = subprocess.run(
            args.command,
            stdout=stdout,
            stderr=stderr,
            timeout=args.timeout,
            check=False,
        )
        exit_code = completed.returncode
    except subprocess.TimeoutExpired:
        timed_out = True
        exit_code = 124
elapsed = time.perf_counter() - started
peak_rss_bytes = None
if resource is not None:
    peak_rss = resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss
    # Darwin reports bytes; Linux and the BSDs report KiB.
    peak_rss_bytes = peak_rss if sys.platform == "darwin" else peak_rss * 1024

with open(args.result, "w", encoding="utf-8") as result:
    sample = {
        "schema": "weave.benchmark-sample/v1",
        "command": args.command,
        "elapsed_seconds": round(elapsed, 6),
        "exit_code": exit_code,
        "timed_out": timed_out,
    }
    if peak_rss_bytes is not None:
        sample["peak_rss_bytes"] = peak_rss_bytes
    json.dump(sample, result, sort_keys=True)
    result.write("\n")

raise SystemExit(exit_code)
