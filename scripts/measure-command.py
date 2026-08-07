#!/usr/bin/env python3
"""Run one bounded command and record portable timing/result JSON."""

import argparse
import json
import subprocess
import time


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

with open(args.result, "w", encoding="utf-8") as result:
    json.dump(
        {
            "schema": "weave.benchmark-sample/v1",
            "command": args.command,
            "elapsed_seconds": round(elapsed, 6),
            "exit_code": exit_code,
            "timed_out": timed_out,
        },
        result,
        sort_keys=True,
    )
    result.write("\n")

raise SystemExit(exit_code)
