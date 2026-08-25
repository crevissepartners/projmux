#!/usr/bin/env python3
"""Fail closed unless every declared required child completed successfully."""

from __future__ import annotations

import argparse
import json
import sys


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--results-json", required=True)
    parser.add_argument("--required", action="append", required=True)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        results = json.loads(args.results_json)
    except json.JSONDecodeError as exc:
        print(f"required gate: malformed child results: {exc}", file=sys.stderr)
        return 2
    if not isinstance(results, dict):
        print("required gate: child results must be an object", file=sys.stderr)
        return 2

    expected = set(args.required)
    actual = set(results)
    missing = sorted(expected - actual)
    extra = sorted(actual - expected)
    failed: list[str] = []
    for name in sorted(expected & actual):
        child = results[name]
        result = child.get("result") if isinstance(child, dict) else None
        print(f"required_child={name} result={result}")
        if result != "success":
            failed.append(f"{name}={result}")

    if missing or extra or failed:
        if missing:
            print(f"required gate: missing children: {','.join(missing)}", file=sys.stderr)
        if extra:
            print(f"required gate: unexpected children: {','.join(extra)}", file=sys.stderr)
        if failed:
            print(f"required gate: unsuccessful children: {','.join(failed)}", file=sys.stderr)
        return 1
    print("required gate: all children succeeded")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
