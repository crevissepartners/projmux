#!/usr/bin/env python3
"""Emit bounded, privacy-safe E2E first-failure records to stderr."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
import time
from pathlib import Path
from typing import Any


SCENARIO_RE = re.compile(r"^(?:L(?:0[1-9]|1[0-9])|C01|N01)$")
NAME_RE = re.compile(r"^[a-z][a-z0-9-]{0,47}$")
SHARD_RE = re.compile(r"^(?:fixture-[1-4]|codex-lifecycle|npm-staging|contract|all)$")
SOURCE_RE = re.compile(r"^(?:test/e2e|test/lib)/[a-z0-9][a-z0-9._/-]*\.sh$")
SHA_RE = re.compile(r"^(?:[0-9a-f]{64})?$")
SECRET_RE = re.compile(
    r"(?:-----BEGIN [A-Z ]*PRIVATE KEY-----|gh[pousr]_[A-Za-z0-9_]{20,}|"
    r"github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16}|(?i:password|token|secret)=)"
)
SAFE_OPERATION_RE = re.compile(r"^[a-z][a-z0-9-]{0,47}$")
SAFE_TMUX_ID_RE = re.compile(r"^(?:\$[0-9]+|@[0-9]+|%[0-9]+|[0-9]+|root|[a-z][a-z0-9_-]{0,31})?$")
ANSI_RE = re.compile(r"\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))")
TAIL_WORDS = {
    "attached", "bootstrap", "candidate", "client", "current", "dead", "discovery",
    "error", "exact", "exit", "failed", "failure", "for", "frame", "missing", "open",
    "pane", "present", "process", "session", "state", "status", "terminated", "timed",
    "tmux", "waiting", "window",
}


def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def file_fact(path_text: str) -> dict[str, Any]:
    if not path_text:
        return {"exists": False, "size": 0, "sha256": ""}
    path = Path(path_text)
    try:
        data = path.read_bytes()
    except OSError:
        return {"exists": False, "size": 0, "sha256": ""}
    return {"exists": True, "size": len(data), "sha256": digest(data)}


def private_json(record: dict[str, Any]) -> str:
    serialized = json.dumps(record, sort_keys=True, separators=(",", ":"))
    if SECRET_RE.search(serialized):
        raise ValueError("first-failure record contains a secret-shaped value")
    return serialized


def emit(prefix: str, record: dict[str, Any]) -> None:
    print(f"{prefix} {private_json(record)}", file=sys.stderr)


def command_for(scenario: str) -> str:
    return f"make test-e2e E2E_SCENARIO={scenario}"


def terminal_command(args: argparse.Namespace) -> int:
    if not SCENARIO_RE.fullmatch(args.scenario):
        raise ValueError("invalid scenario")
    if not NAME_RE.fullmatch(args.phase) or not NAME_RE.fullmatch(args.owner):
        raise ValueError("invalid phase or owner")
    if not SHARD_RE.fullmatch(args.shard):
        raise ValueError("invalid shard")
    # Source line, wait status, and source path are the degradable half of the
    # attribution. Bash reports BASH_LINENO[0] as 0 for a top-level failure, and a
    # trap can fire from a path outside the shell allowlist. Raising there used to
    # discard the scenario, phase, owner, and shard along with it, which is the
    # whole record. Drop only the field that failed its own rule and say so.
    attribution = "complete"
    source = args.source
    if not SOURCE_RE.fullmatch(source) or ".." in source:
        source = ""
        attribution = "partial"
    status = args.status
    if not 1 <= status <= 255:
        status = 0
        attribution = "partial"
    line = args.line
    if line < 1:
        line = 0
        attribution = "partial"
    expected_command = command_for(args.scenario)
    if args.command != expected_command:
        raise ValueError("command is not the sanitized single-scenario allowlist shape")
    if not SHA_RE.fullmatch(args.binary_sha256) or not SHA_RE.fullmatch(args.state_sha256):
        raise ValueError("invalid binary or state digest")
    emit("E2E_TERMINAL", {
        "attribution": attribution,
        "binary_sha256": args.binary_sha256,
        "command": args.command,
        "line": line,
        "owner": args.owner,
        "phase": args.phase,
        "replay": expected_command,
        "scenario": args.scenario,
        "schema": "projmux.e2e-terminal/v1",
        "shard": args.shard,
        "source": source,
        "state_sha256": args.state_sha256,
        "status": status,
    })
    return 0


def parse_racer(value: str) -> dict[str, Any]:
    fields = value.split(":", 3)
    if len(fields) != 4:
        raise ValueError("racer must be index:pid:status:outcome")
    index, pid, status = (int(fields[position]) for position in range(3))
    outcome = fields[3]
    if index not in range(1, 9) or pid < 0 or status not in range(0, 256):
        raise ValueError("invalid racer scalar")
    if outcome not in {"not-started", "completed", "converged", "deferred", "exhausted", "other"}:
        raise ValueError("invalid racer outcome")
    return {"index": index, "outcome": outcome, "pid": pid, "status": status}


def l06_command(args: argparse.Namespace) -> int:
    if not SAFE_OPERATION_RE.fullmatch(args.operation):
        raise ValueError("invalid L06 operation")
    if args.holder_pid < 0 or args.holder_started_ms < 0:
        raise ValueError("invalid L06 holder state")
    racers = sorted((parse_racer(value) for value in args.racer), key=lambda item: item["index"])
    if [item["index"] for item in racers] != list(range(1, 9)):
        raise ValueError("L06 diagnostics require exactly racers 1 through 8")
    if args.acquire not in {"not-started", "acquired", "contended", "unknown"}:
        raise ValueError("invalid L06 acquire state")
    if args.release not in {"not-started", "held", "released", "unknown"}:
        raise ValueError("invalid L06 release state")
    now_ms = int(time.time() * 1000)
    holder_started_ms = args.holder_started_ms
    # GNU date implementations differ on whether a width on %N is honored.
    # Normalize the nanosecond-shaped value used by the portable smoke helper.
    if holder_started_ms > now_ms * 100:
        holder_started_ms //= 1_000_000
    age_ms = max(0, now_ms - holder_started_ms) if holder_started_ms else 0
    emit("E2E_DIAGNOSTIC", {
        "attribution": "l06-lock-exhaustion",
        "holder": {
            "acquire": args.acquire,
            "age_ms": age_ms,
            "operation": args.operation,
            "pid": args.holder_pid,
            "release": args.release,
        },
        "racers": racers,
        "scenario": "L06",
        "schema": "projmux.e2e-diagnostic/v1",
    })
    return 0


def json_value(path_text: str) -> Any:
    if not path_text:
        return None
    path = Path(path_text)
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return None
    except (OSError, json.JSONDecodeError):
        return {"_invalid_json_sha256": file_fact(path_text)["sha256"]}


def safe_segment(segment: str) -> str:
    if re.fullmatch(r"[A-Za-z][A-Za-z0-9_-]{0,47}", segment) and not SECRET_RE.search(segment):
        return segment
    return f"key-sha256-{digest(segment.encode())[:12]}"


def changed_paths(before: Any, after: Any, prefix: str = "$") -> list[str]:
    if type(before) is not type(after):
        return [prefix]
    if isinstance(before, dict):
        result: list[str] = []
        for key in sorted(set(before) | set(after), key=str):
            child = f"{prefix}.{safe_segment(str(key))}"
            if key not in before or key not in after:
                result.append(child)
            else:
                result.extend(changed_paths(before[key], after[key], child))
        return result
    if isinstance(before, list):
        result = []
        for index in range(max(len(before), len(after))):
            child = f"{prefix}[{index}]"
            if index >= len(before) or index >= len(after):
                result.append(child)
            else:
                result.extend(changed_paths(before[index], after[index], child))
        return result
    return [] if before == after else [prefix]


def l08_command(args: argparse.Namespace) -> int:
    for count in (args.controller_entries, args.hook_processes, args.owned_processes, args.socket_entries):
        if count < 0:
            raise ValueError("invalid L08 pending count")
    before_fact = file_fact(args.before)
    after_fact = file_fact(args.after)
    paths = sorted(set(changed_paths(json_value(args.before), json_value(args.after))))
    total = len(paths)
    paths = paths[:32]
    emit("E2E_DIAGNOSTIC", {
        "attribution": "l08-state-drift",
        "changed_json_paths": paths,
        "changed_json_paths_omitted": max(0, total - len(paths)),
        "pending": {
            "controller_entries": args.controller_entries,
            "hook_processes": args.hook_processes,
            "owned_processes": args.owned_processes,
            "socket_entries": args.socket_entries,
        },
        "registry_after_sha256": after_fact["sha256"],
        "registry_before_sha256": before_fact["sha256"],
        "scenario": "L08",
        "schema": "projmux.e2e-diagnostic/v1",
    })
    return 0


def parse_state_rows(path_text: str, fields: tuple[str, ...]) -> list[dict[str, Any]]:
    if not path_text:
        return []
    try:
        lines = Path(path_text).read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError:
        return []
    rows: list[dict[str, Any]] = []
    for line in lines[:16]:
        values = line.split("|")
        if len(values) != len(fields) or not all(SAFE_TMUX_ID_RE.fullmatch(value) for value in values):
            continue
        rows.append(dict(zip(fields, values)))
    return rows


def process_fact(pid: int) -> dict[str, Any]:
    result: dict[str, Any] = {"alive": False, "pid": max(pid, 0), "state": ""}
    if pid <= 0:
        return result
    try:
        stat = Path(f"/proc/{pid}/stat").read_text(encoding="ascii")
        result["alive"] = True
        result["state"] = stat.rsplit(") ", 1)[1].split()[0]
    except (OSError, IndexError):
        pass
    return result


def sanitized_tail(path_text: str) -> dict[str, Any]:
    if not path_text:
        return {"line_count": 0, "lines": [], "source_sha256": "", "truncated": False}
    try:
        data = Path(path_text).read_bytes()
    except OSError:
        return {"line_count": 0, "lines": [], "source_sha256": "", "truncated": False}
    bounded = data[-4096:]
    text = ANSI_RE.sub("", bounded.decode("utf-8", errors="replace"))
    raw_lines = text.splitlines()[-20:]
    lines: list[dict[str, str]] = []
    for raw in raw_lines:
        words = re.findall(r"[A-Za-z]+", raw.lower())
        summary = " ".join(word for word in words if word in TAIL_WORDS)[:160]
        lines.append({"sha256": digest(raw.encode("utf-8", errors="replace")), "summary": summary or "redacted-line"})
    return {
        "line_count": len(raw_lines),
        "lines": lines,
        "source_sha256": digest(data),
        "truncated": len(data) > len(bounded) or len(text.splitlines()) > 20,
    }


def l16_command(args: argparse.Namespace) -> int:
    emit("E2E_DIAGNOSTIC", {
        "attribution": "l16-semantic-timeout",
        "child": process_fact(args.child_pid),
        "files": {
            "err": file_fact(args.err),
            "out": file_fact(args.out),
            "rc": file_fact(args.rc),
        },
        "scenario": "L16",
        "schema": "projmux.e2e-diagnostic/v1",
        "sanitized_tail": sanitized_tail(args.tail),
        "tmux": {
            "clients": parse_state_rows(
                args.clients,
                ("client_pid", "session_id", "window_id", "pane_id", "key_table"),
            ),
            "panes": parse_state_rows(
                args.panes,
                ("pane_pid", "session_id", "window_id", "pane_id", "active", "dead", "dead_status"),
            ),
        },
    })
    return 0


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser()
    commands = result.add_subparsers(dest="command_name", required=True)

    terminal = commands.add_parser("terminal")
    terminal.add_argument("--scenario", required=True)
    terminal.add_argument("--phase", required=True)
    terminal.add_argument("--owner", required=True)
    terminal.add_argument("--shard", required=True)
    terminal.add_argument("--status", required=True, type=int)
    terminal.add_argument("--source", required=True)
    terminal.add_argument("--line", required=True, type=int)
    terminal.add_argument("--command", required=True)
    terminal.add_argument("--binary-sha256", default="")
    terminal.add_argument("--state-sha256", default="")
    terminal.set_defaults(function=terminal_command)

    l06 = commands.add_parser("l06")
    l06.add_argument("--holder-pid", required=True, type=int)
    l06.add_argument("--holder-started-ms", required=True, type=int)
    l06.add_argument("--operation", required=True)
    l06.add_argument("--acquire", required=True)
    l06.add_argument("--release", required=True)
    l06.add_argument("--racer", action="append", default=[])
    l06.set_defaults(function=l06_command)

    l08 = commands.add_parser("l08")
    l08.add_argument("--before", default="")
    l08.add_argument("--after", default="")
    l08.add_argument("--controller-entries", required=True, type=int)
    l08.add_argument("--hook-processes", required=True, type=int)
    l08.add_argument("--owned-processes", required=True, type=int)
    l08.add_argument("--socket-entries", required=True, type=int)
    l08.set_defaults(function=l08_command)

    l16 = commands.add_parser("l16")
    l16.add_argument("--child-pid", required=True, type=int)
    l16.add_argument("--rc", default="")
    l16.add_argument("--out", default="")
    l16.add_argument("--err", default="")
    l16.add_argument("--clients", default="")
    l16.add_argument("--panes", default="")
    l16.add_argument("--tail", default="")
    l16.set_defaults(function=l16_command)
    return result


def main() -> int:
    args = parser().parse_args()
    try:
        return args.function(args)
    except (OSError, ValueError) as error:
        print(f"e2e-first-failure: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
