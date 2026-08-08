#!/usr/bin/env python3
"""Summarize one Codex JSONL research run with a deterministic answer rubric."""

import argparse
import json
import re
from pathlib import Path


parser = argparse.ArgumentParser()
parser.add_argument("--arm", required=True, choices=("with-weave", "without-weave"))
parser.add_argument("--events", required=True)
parser.add_argument("--answer", required=True)
parser.add_argument("--sample", required=True)
parser.add_argument("--rubric", required=True)
parser.add_argument("--block-log", required=True)
parser.add_argument("--output", required=True)
args = parser.parse_args()


def command_text(item):
    for key in ("command", "cmd", "arguments", "input"):
        value = item.get(key)
        if isinstance(value, str):
            return value
        if value is not None:
            return json.dumps(value, sort_keys=True)
    return ""


events_path = Path(args.events)
events = []
for line in (events_path.read_text(encoding="utf-8", errors="replace") if events_path.exists() else "").splitlines():
    try:
        events.append(json.loads(line))
    except json.JSONDecodeError:
        continue

commands = []
for event in events:
    if event.get("type") != "item.completed":
        continue
    item = event.get("item") or {}
    if item.get("type") in {"command_execution", "mcp_tool_call", "tool_call"}:
        commands.append(command_text(item))

usage = {}
for event in events:
    candidate = event.get("usage")
    if isinstance(candidate, dict):
        usage = candidate

answer_path = Path(args.answer)
answer = answer_path.read_text(encoding="utf-8", errors="replace") if answer_path.exists() else ""
rubric = json.loads(Path(args.rubric).read_text(encoding="utf-8"))
checks = []
for check in rubric.get("required", []):
    matched = re.search(check["pattern"], answer, re.IGNORECASE | re.MULTILINE) is not None
    checks.append({"id": check["id"], "matched": matched})

sample = json.loads(Path(args.sample).read_text(encoding="utf-8"))
block_log = Path(args.block_log)
blocked_attempts = 0
if block_log.exists():
    blocked_attempts = len(block_log.read_text(encoding="utf-8", errors="replace").splitlines())

search_pattern = re.compile(r"(^|[;&|]\s*|\s)(rg|grep|find|fd)(\s|$)")
read_pattern = re.compile(r"(^|[;&|]\s*|\s)(sed|cat|head|tail|bat)(\s|$)")
weave_pattern = re.compile(r"(^|[/\s])weave(\s|$)")

result = {
    "schema": "weave.agent-research-sample/v1",
    "arm": args.arm,
    "elapsed_seconds": sample.get("elapsed_seconds"),
    "exit_code": sample.get("exit_code"),
    "timed_out": sample.get("timed_out"),
    "usage": usage,
    "answer_bytes": len(answer.encode("utf-8")),
    "answer_words": len(answer.split()),
    "command_executions": len(commands),
    "weave_commands": sum(bool(weave_pattern.search(value)) for value in commands),
    "filesystem_search_commands": sum(bool(search_pattern.search(value)) for value in commands),
    "source_read_commands": sum(bool(read_pattern.search(value)) for value in commands),
    "blocked_weave_attempts": blocked_attempts,
    "rubric": {
        "matched": sum(check["matched"] for check in checks),
        "total": len(checks),
        "checks": checks,
    },
}
Path(args.output).write_text(json.dumps(result, indent=2, sort_keys=True) + "\n", encoding="utf-8")
