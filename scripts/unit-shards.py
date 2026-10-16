#!/usr/bin/env python3
"""Partition ordinary Go tests; preserve package/name and execution evidence."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess

APP = "github.com/crevissepartners/projmux/internal/app"
SHARDS = ("app-0", "app-1", "app-2", "rest")
NAME = re.compile(r"^(?:Test|Example|Fuzz)\w*$")
PREFIX_WIDTH = 10
MAX_SELECTOR_BYTES = 4096


def selected(shard: str, package: str, name: str) -> bool:
    if package != APP:
        return shard == "rest"
    return shard == f"app-{int(hashlib.sha256(name[:PREFIX_WIDTH].encode()).hexdigest(), 16) % 3}"


def app_pattern(names: list[str]) -> str:
    # A full-name alternation is ~90KB today. Procfs identity tests deliberately
    # refuse argv over 8KiB, so hash stable prefix cohorts and compress their
    # selector instead. Short names stay exact to avoid prefix neighbours.
    tree = {}
    for name in names:
        node = tree
        for character in name[:PREFIX_WIDTH]:
            node = node.setdefault(character, {})
        node[None] = "$" if len(name) < PREFIX_WIDTH else ""

    def emit(node: dict) -> str:
        parts = [node[None]] if None in node else []
        parts += [re.escape(character) + emit(child)
                  for character, child in sorted((key, value) for key, value in node.items() if key is not None)]
        return parts[0] if len(parts) == 1 else "(?:" + "|".join(parts) + ")"

    pattern = "^" + emit(tree)
    if len(pattern.encode()) > MAX_SELECTOR_BYTES:
        raise ValueError("app selector exceeds its 4KiB argv budget")
    return pattern


def go(*args: str) -> list[str]:
    return [os.environ.get("GO", "go"), *args]


def run(shard: str, directory: Path) -> None:
    directory.mkdir(parents=True, exist_ok=True)
    packages = sorted(subprocess.check_output(go("list", "./..."), text=True).splitlines())
    if APP not in packages or len(packages) != len(set(packages)):
        raise ValueError("invalid complete package inventory")
    owned = [p for p in packages if (p == APP) == (shard != "rest")]
    # -list reports the unfiltered top-level Test/Example/Fuzz inventory on this
    # very head. No fuzzing is enabled: normal go test executes Fuzz seed corpora.
    listing = subprocess.check_output(go("test", "-json", "-list", ".", *owned), text=True)
    inventory = {p: [] for p in owned}
    for line in listing.splitlines():
        event = json.loads(line)
        name = event.get("Output", "").strip()
        if NAME.fullmatch(name):
            inventory[event["Package"]].append(name)
    for names in inventory.values():
        names.sort()
        if len(names) != len(set(names)):
            raise ValueError("duplicate name in unpartitioned inventory")
    expected = {p: [n for n in names if selected(shard, p, n)] for p, names in inventory.items()}
    args = ["test", "-json", "-count=1"]
    pattern = None
    if shard != "rest":
        names = expected[APP]
        if not names:
            raise ValueError("empty app shard")
        pattern = app_pattern(names)
        if [name for name in inventory[APP] if re.match(pattern, name)] != names:
            raise ValueError("compact app selector differs from selected inventory")
        args += ["-run", pattern]
    args += owned
    command = go(*args)
    command_bytes = sum(len(os.fsencode(argument)) + 1 for argument in command)
    if shard != "rest" and command_bytes > 8192:
        raise ValueError("app Go command exceeds its 8KiB argv budget")
    seen = {p: [] for p in owned}
    completed = set()
    with (directory / "events.jsonl").open("w") as output:
        process = subprocess.Popen(command, stdout=subprocess.PIPE, text=True)
        assert process.stdout is not None
        for line in process.stdout:
            output.write(line)
            event = json.loads(line)
            if event.get("Action") == "run" and "/" not in event.get("Test", ""):
                seen[event["Package"]].append(event["Test"])
            if event.get("Action") in ("pass", "skip") and not event.get("Test"):
                completed.add(event["Package"])
            if "Output" in event:
                print(event["Output"], end="", flush=True)
        status = process.wait()
    evidence = dict(shard=shard, run_id=os.environ.get("GITHUB_RUN_ID", "local"),
                    run_attempt=int(os.environ.get("GITHUB_RUN_ATTEMPT", "1")), head=subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip(),
                    selector=pattern, selector_bytes=len(pattern.encode()) if pattern else 0,
                    go_argv_bytes=command_bytes, packages=packages, inventory=inventory, selected=expected,
                    executed={p: sorted(v) for p, v in seen.items()},
                    completed=sorted(completed), status=status)
    (directory / "manifest.json").write_text(json.dumps(evidence, indent=2) + "\n")
    if status:
        raise SystemExit(status)
    validate_execution(evidence)


def validate_execution(evidence: dict) -> None:
    if evidence["status"] != 0 or evidence["selected"] != evidence["executed"]:
        raise ValueError("selected/executed test inventory differs or Go tests failed")
    if sorted(evidence["inventory"]) != evidence["completed"]:
        raise ValueError("not every selected package completed")


def verify(directory: Path, head: str) -> None:
    manifests = [json.loads(p.read_text()) for p in directory.glob("*/manifest.json")]
    manifests.sort(key=lambda m: m["shard"])
    if sorted(m["shard"] for m in manifests) != sorted(SHARDS):
        raise ValueError("missing, duplicate, or unexpected shard evidence")
    packages = manifests[0]["packages"]
    if not packages or APP not in packages or len(packages) != len(set(packages)):
        raise ValueError("invalid package inventory")
    baseline = {}
    union = {}
    run_id = os.environ.get("GITHUB_RUN_ID", "local")
    run_attempt = int(os.environ.get("GITHUB_RUN_ATTEMPT", "1"))
    for m in manifests:
        validate_execution(m)
        if m["run_id"] != run_id or not 1 <= m["run_attempt"] <= run_attempt:
            raise ValueError("stale run or future attempt evidence")
        if m["head"] != head or m["packages"] != packages:
            raise ValueError("shards must describe the same head and complete package set")
        owned = {p for p in packages if (p == APP) == (m["shard"] != "rest")}
        if set(m["inventory"]) != owned or set(m["selected"]) != owned:
            raise ValueError("shard package ownership differs")
        for package, names in m["inventory"].items():
            if names != sorted(set(names)) or any(not NAME.fullmatch(n) for n in names):
                raise ValueError("invalid unpartitioned test inventory")
            if package in baseline and baseline[package] != names:
                raise ValueError("app shards disagree on unpartitioned inventory")
            baseline[package] = names
            expected = [n for n in names if selected(m["shard"], package, n)]
            if m["selected"][package] != expected:
                raise ValueError("selection differs from deterministic partition")
            union.setdefault(package, []).extend(expected)
    if sorted(baseline) != packages or {p: sorted(v) for p, v in union.items()} != baseline:
        raise ValueError("partition union differs from unpartitioned package/test inventory")
    summary = dict(head=head, run_id=run_id, run_attempt=run_attempt,
                   shards=manifests, packages=len(packages), names=sum(map(len, baseline.values())),
                   kinds={kind: sum(n.startswith(kind) for names in baseline.values() for n in names)
                          for kind in ("Test", "Example", "Fuzz")}, inventory=baseline)
    (directory / "comparison.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(f"same-head inventory PASS: {summary['packages']} packages, {summary['names']} names; {summary['kinds']}")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("shard", choices=(*SHARDS, "verify"))
    parser.add_argument("--evidence", type=Path, required=True)
    parser.add_argument("--head")
    args = parser.parse_args()
    if args.shard == "verify":
        if not args.head:
            parser.error("verify requires --head")
        verify(args.evidence, args.head)
    else:
        run(args.shard, args.evidence)


if __name__ == "__main__":
    main()
