#!/usr/bin/env python3
"""Compare scanner output with reviewed, line-independent finding baselines."""

from __future__ import annotations

import argparse
import collections
import json
import pathlib
import sys
from typing import Any, Iterable


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--tool", choices=("gosec", "staticcheck"), required=True)
    parser.add_argument("--root", required=True)
    parser.add_argument("--report", required=True)
    parser.add_argument("--baseline", required=True)
    parser.add_argument("--scanner-status", type=int, default=0)
    parser.add_argument("--write", action="store_true")
    return parser.parse_args()


def load_report(tool: str, path: pathlib.Path) -> list[dict[str, Any]]:
    if tool == "gosec":
        document = json.loads(path.read_text(encoding="utf-8"))
        scanner_errors = document.get("Golang errors", document.get("Errors", {}))
        if scanner_errors:
            raise ValueError(f"gosec reported analyzer/build errors: {scanner_errors}")
        return document.get("Issues", [])
    findings = []
    for line in path.read_text(encoding="utf-8").splitlines():
        if line.strip():
            findings.append(json.loads(line))
    return findings


def relative_path(root: pathlib.Path, raw: str) -> str:
    path = pathlib.Path(raw)
    if not path.is_absolute():
        path = root / path
    try:
        return path.resolve().relative_to(root.resolve()).as_posix()
    except ValueError as error:
        raise ValueError(f"finding path is outside repository: {raw}") from error


def source_line(root: pathlib.Path, path: str, line: int) -> str:
    lines = (root / path).read_text(encoding="utf-8").splitlines()
    if line < 1 or line > len(lines):
        return ""
    return " ".join(lines[line - 1].strip().split())


def normalized(tool: str, root: pathlib.Path, finding: dict[str, Any]) -> dict[str, str]:
    if tool == "gosec":
        path = relative_path(root, str(finding["file"]))
        line = int(str(finding["line"]).split("-", 1)[0])
        return {
            "rule": str(finding["rule_id"]),
            "path": path,
            "message": " ".join(str(finding["details"]).split()),
            "source": source_line(root, path, line),
        }
    location = finding["location"]
    path = relative_path(root, str(location["file"]))
    return {
        "rule": str(finding["code"]),
        "path": path,
        "message": " ".join(str(finding["message"]).split()),
        "source": source_line(root, path, int(location["line"])),
    }


def entries(findings: Iterable[dict[str, Any]], tool: str, root: pathlib.Path) -> list[dict[str, Any]]:
    counter: collections.Counter[tuple[str, str, str, str]] = collections.Counter()
    for finding in findings:
        item = normalized(tool, root, finding)
        counter[(item["rule"], item["path"], item["message"], item["source"])] += 1
    return [
        {
            "rule": key[0],
            "path": key[1],
            "message": key[2],
            "source": key[3],
            "max_count": count,
        }
        for key, count in sorted(counter.items())
    ]


def metadata(tool: str, generated: list[dict[str, Any]]) -> dict[str, Any]:
    notes: list[dict[str, str]] = []
    if tool == "gosec":
        notes = [
            {
                "rule": "G101",
                "scope": "audit-parity catalog/theme token-name fixtures",
                "reason": "reviewed non-secret identifiers; the 2026-07-29 audit did not enable gosec test-file scanning",
            },
            {
                "rule": "G404",
                "scope": "notify and recent-window store lock jitter",
                "reason": "non-cryptographic retry timing",
            },
            {
                "rule": "G110",
                "scope": "internal/app/update.go release extraction",
                "reason": "archive entry and total expanded bytes are bounded before io.Copy",
            },
            {
                "rule": "G703",
                "scope": "internal/app/ai.go isDir os.Stat",
                "reason": "read-only directory existence check",
            },
            {
                "rule": "G703",
                "scope": "internal/ui/picker/backend.go native debug log",
                "reason": "explicit operator-provided debug output path",
            },
            {
                "rule": "G118",
                "scope": "internal/app/setup.go key reader",
                "reason": "reviewed in the completed resource-lifetime phase; no current finding",
            },
        ]
    elif tool == "staticcheck":
        notes = [
            {
                "rule": "multiple",
                "scope": "47 pre-existing findings from the 2026-07-29 audit",
                "reason": "reviewed per rule, path, message, source fingerprint, and maximum count",
            }
        ]
    return {
        "schema": 1,
        "tool": tool,
        "fingerprint": "rule + repository-relative path + normalized message + normalized source line",
        "reviewed_suppressions": notes,
        "entries": generated,
    }


def main() -> int:
    args = parse_args()
    root = pathlib.Path(args.root)
    report = pathlib.Path(args.report)
    baseline_path = pathlib.Path(args.baseline)
    if args.scanner_status not in (0, 1):
        raise ValueError(f"{args.tool} exited with unexpected status {args.scanner_status}")
    findings = load_report(args.tool, report)
    if args.scanner_status != 0 and not findings:
        raise ValueError(f"{args.tool} failed with status {args.scanner_status} and produced no findings")
    generated = entries(findings, args.tool, root)

    if args.write:
        baseline_path.parent.mkdir(parents=True, exist_ok=True)
        baseline_path.write_text(
            json.dumps(metadata(args.tool, generated), indent=2, ensure_ascii=False) + "\n",
            encoding="utf-8",
        )
        print(f">> {args.tool}: wrote {len(generated)} baseline fingerprints")
        return 0

    baseline = json.loads(baseline_path.read_text(encoding="utf-8"))
    allowed = {
        (item["rule"], item["path"], item["message"], item["source"]): int(item["max_count"])
        for item in baseline["entries"]
    }
    new_findings = []
    for item in generated:
        key = (item["rule"], item["path"], item["message"], item["source"])
        if item["max_count"] > allowed.get(key, 0):
            new_findings.append(item)

    if new_findings:
        print(f"{args.tool}: NEW findings outside the reviewed baseline:", file=sys.stderr)
        for item in new_findings:
            print(
                f"  {item['path']}: {item['rule']}: {item['message']} "
                f"(current={item['max_count']}, allowed={allowed.get((item['rule'], item['path'], item['message'], item['source']), 0)})",
                file=sys.stderr,
            )
            print(f"    source: {item['source']}", file=sys.stderr)
        return 1

    total = sum(item["max_count"] for item in generated)
    print(f">> {args.tool}: clean ({total} findings covered by reviewed baseline)")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (KeyError, OSError, ValueError) as error:
        print(f"security baseline: {error}", file=sys.stderr)
        sys.exit(2)
