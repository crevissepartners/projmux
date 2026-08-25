#!/usr/bin/env python3
"""Write and validate privacy-safe E2E contract-attempt evidence."""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import tempfile
from pathlib import Path


SCENARIO_RE = re.compile(r"^(?:L(?:0[1-9]|1[0-9])|C01|N01)$")
PHASE_RE = re.compile(r"^[a-z][a-z0-9-]{0,47}$")
OWNER_RE = re.compile(r"^[a-z][a-z0-9-]{0,47}$")
CLASS_NAMES = {
    "deterministic-regression",
    "product-authority-race",
    "harness-lifecycle",
    "observation-frame",
    "environment",
    "unattributed",
}
OUTCOMES = {"begin", "pass", "fail", "cancel"}
FIELDS = {
    "schema",
    "scenario_id",
    "suite",
    "attempt",
    "phase",
    "owner",
    "class",
    "outcome",
    "elapsed_ms",
    "binary_sha256",
    "route_socket",
    "state_sha256",
    "artifact",
    "replay",
}
FORBIDDEN_VALUE = re.compile(
    r"(?:-----BEGIN [A-Z ]*PRIVATE KEY-----|gh[pousr]_[A-Za-z0-9_]{20,}|"
    r"github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16}|(?i:password|token|secret)=)"
)


def validate(record: dict[str, object]) -> None:
    if set(record) != FIELDS:
        raise ValueError(f"field set mismatch: {sorted(set(record) ^ FIELDS)}")
    if record["schema"] != "projmux.e2e-attempt/v1":
        raise ValueError("unsupported schema")
    if not isinstance(record["scenario_id"], str) or not SCENARIO_RE.fullmatch(record["scenario_id"]):
        raise ValueError("invalid stable scenario ID")
    for field, pattern in (("phase", PHASE_RE), ("owner", OWNER_RE)):
        if not isinstance(record[field], str) or not pattern.fullmatch(record[field]):
            raise ValueError(f"invalid {field}")
    if record["class"] not in CLASS_NAMES:
        raise ValueError("invalid typed class")
    if record["outcome"] not in OUTCOMES:
        raise ValueError("invalid outcome")
    if not isinstance(record["attempt"], int) or record["attempt"] < 1:
        raise ValueError("attempt must be a positive integer")
    if not isinstance(record["elapsed_ms"], int) or record["elapsed_ms"] < 0:
        raise ValueError("elapsed_ms must be a non-negative integer")
    binary_sha256 = record["binary_sha256"]
    if not isinstance(binary_sha256, str) or not re.fullmatch(r"(?:[0-9a-f]{64})?", binary_sha256):
        raise ValueError("invalid binary_sha256")
    suite = record["suite"]
    if not isinstance(suite, str) or not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,47}", suite):
        raise ValueError("invalid suite")
    route_socket = record["route_socket"]
    if not isinstance(route_socket, str) or not re.fullmatch(r"[A-Za-z0-9_.-]{0,96}", route_socket):
        raise ValueError("invalid route_socket")
    state_sha256 = record["state_sha256"]
    if not isinstance(state_sha256, str) or not re.fullmatch(r"(?:[0-9a-f]{64})?", state_sha256):
        raise ValueError("invalid state_sha256")
    artifact = record["artifact"]
    expected_artifact = f"{record['scenario_id']}-attempt-{record['attempt']}.json"
    if not isinstance(artifact, str) or artifact != expected_artifact:
        raise ValueError("artifact must be the canonical relative filename")
    replay = record["replay"]
    expected_replay = f"make test-e2e E2E_SCENARIO={record['scenario_id']}"
    if replay != expected_replay:
        raise ValueError("replay must select exactly one stable scenario")
    serialized = json.dumps(record, sort_keys=True)
    if FORBIDDEN_VALUE.search(serialized):
        raise ValueError("evidence contains a secret-shaped value")


def atomic_append(path: Path, line: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a", encoding="utf-8") as stream:
        stream.write(line)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())


def write_artifact(directory: Path, record: dict[str, object]) -> None:
    directory.mkdir(parents=True, exist_ok=True)
    target = directory / str(record["artifact"])
    fd, temporary = tempfile.mkstemp(prefix=f".{target.name}.", dir=directory)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as stream:
            json.dump(record, stream, sort_keys=True, separators=(",", ":"))
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(temporary, 0o600)
        os.replace(temporary, target)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def record_command(args: argparse.Namespace) -> int:
    record = {
        "schema": "projmux.e2e-attempt/v1",
        "scenario_id": args.scenario_id,
        "suite": args.suite,
        "attempt": args.attempt,
        "phase": args.phase,
        "owner": args.owner,
        "class": args.typed_class,
        "outcome": args.outcome,
        "elapsed_ms": args.elapsed_ms,
        "binary_sha256": args.binary_sha256,
        "route_socket": args.route_socket,
        "state_sha256": args.state_sha256,
        "artifact": f"{args.scenario_id}-attempt-{args.attempt}.json",
        "replay": f"make test-e2e E2E_SCENARIO={args.scenario_id}",
    }
    validate(record)
    directory = Path(args.directory)
    write_artifact(directory, record)
    atomic_append(directory / "summary.jsonl", json.dumps(record, sort_keys=True, separators=(",", ":")))
    print(
        "E2E_CONTRACT "
        f"id={record['scenario_id']} attempt={record['attempt']} phase={record['phase']} "
        f"outcome={record['outcome']} class={record['class']} owner={record['owner']} "
        f"artifact={record['artifact']} replay='{record['replay']}'"
    )
    return 0


def append_attempt_record(
    seen: dict[tuple[str, int], list[dict[str, object]]],
    record: dict[str, object],
) -> None:
    key = (str(record["scenario_id"]), int(record["attempt"]))
    records = seen.setdefault(key, [])
    records.append(record)
    outcomes = [str(item["outcome"]) for item in records]
    if outcomes[0] != "begin" or outcomes.count("begin") != 1:
        raise ValueError("attempt must start with exactly one begin")
    if len(outcomes) > 2 or (len(outcomes) == 2 and outcomes[-1] not in {"pass", "fail", "cancel"}):
        raise ValueError("attempt must have exactly one terminal outcome")


def validate_command(args: argparse.Namespace) -> int:
    seen: dict[tuple[str, int], list[dict[str, object]]] = {}
    failures = 0
    with Path(args.path).open(encoding="utf-8") as stream:
        for line_number, line in enumerate(stream, 1):
            try:
                record = json.loads(line)
                if not isinstance(record, dict):
                    raise ValueError("record is not an object")
                validate(record)
                append_attempt_record(seen, record)
            except (ValueError, json.JSONDecodeError) as error:
                print(f"{args.path}:{line_number}: {error}", file=sys.stderr)
                failures += 1
    if failures:
        return 1
    if args.terminal:
        unterminated = [key for key, records in seen.items() if len(records) != 2]
        if unterminated:
            print(f"unterminated attempts: {unterminated}", file=sys.stderr)
            return 1
    return 0


def result_hash_command(args: argparse.Namespace) -> int:
    import hashlib

    seen: dict[tuple[str, int], list[dict[str, object]]] = {}
    expected = set(args.expected.split(",")) if args.expected else set()
    for path in sorted(Path(args.directory).rglob("summary.jsonl")):
        with path.open(encoding="utf-8") as stream:
            for line_number, line in enumerate(stream, 1):
                record = json.loads(line)
                if not isinstance(record, dict):
                    raise ValueError(f"{path}:{line_number}: record is not an object")
                validate(record)
                try:
                    append_attempt_record(seen, record)
                except ValueError as error:
                    raise ValueError(f"{path}:{line_number}: {error}") from error

    rows: list[str] = []
    ids: list[str] = []
    for key, records in seen.items():
        if len(records) != 2:
            raise ValueError(f"required result has unterminated attempt: {key}")
        terminal = records[1]
        if terminal["outcome"] != "pass":
            raise ValueError(
                f"required result terminal is not pass: scenario={key[0]} "
                f"attempt={key[1]} outcome={terminal['outcome']}"
            )
        if terminal["class"] == "unattributed":
            raise ValueError(
                f"required result terminal is unattributed: scenario={key[0]} attempt={key[1]}"
            )
        ids.append(key[0])
        rows.append("|".join(str(terminal[field]) for field in (
            "scenario_id", "outcome", "class", "phase", "owner", "binary_sha256"
        )))
    if len(ids) != len(set(ids)):
        raise ValueError("duplicate terminal scenario evidence")
    if expected and set(ids) != expected:
        raise ValueError(f"terminal inventory mismatch missing={sorted(expected - set(ids))} extra={sorted(set(ids) - expected)}")
    digest = hashlib.sha256(("\n".join(sorted(rows)) + "\n").encode()).hexdigest()
    print(digest)
    return 0


def route_command(args: argparse.Namespace) -> int:
    scenario = args.scenario_id
    if scenario == "C01":
        print("codex-lifecycle:C01")
        return 0
    if scenario == "N01":
        print("npm-staging:N01")
        return 0
    with Path(args.manifest).open(encoding="utf-8") as stream:
        for line in stream:
            shard, ids_text = line.rstrip("\n").split("\t", 1)
            ids = ids_text.split()
            if scenario in ids:
                # The shard is only the isolated fixture route. Replay remains
                # one contract body and its accepted terminal inventory is the
                # requested stable ID, never every contract sharing the shard.
                print(f"linux-{shard}:{scenario}")
                return 0
    raise ValueError(f"unknown required E2E scenario: {scenario}")


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser()
    subparsers = result.add_subparsers(dest="command", required=True)
    record_parser = subparsers.add_parser("record")
    record_parser.add_argument("--directory", required=True)
    record_parser.add_argument("--scenario-id", required=True)
    record_parser.add_argument("--suite", required=True)
    record_parser.add_argument("--attempt", required=True, type=int)
    record_parser.add_argument("--phase", required=True)
    record_parser.add_argument("--owner", required=True)
    record_parser.add_argument("--class", dest="typed_class", required=True, choices=sorted(CLASS_NAMES))
    record_parser.add_argument("--outcome", required=True, choices=sorted(OUTCOMES))
    record_parser.add_argument("--elapsed-ms", required=True, type=int)
    record_parser.add_argument("--binary-sha256", default="")
    record_parser.add_argument("--route-socket", default="")
    record_parser.add_argument("--state-sha256", default="")
    record_parser.set_defaults(function=record_command)
    validate_parser = subparsers.add_parser("validate")
    validate_parser.add_argument("--terminal", action="store_true")
    validate_parser.add_argument("path")
    validate_parser.set_defaults(function=validate_command)
    hash_parser = subparsers.add_parser("result-hash")
    hash_parser.add_argument("--expected", default="")
    hash_parser.add_argument("directory")
    hash_parser.set_defaults(function=result_hash_command)
    route_parser = subparsers.add_parser("route")
    route_parser.add_argument("--manifest", required=True)
    route_parser.add_argument("scenario_id")
    route_parser.set_defaults(function=route_command)
    return result


def main() -> int:
    args = parser().parse_args()
    try:
        return args.function(args)
    except (OSError, ValueError) as error:
        print(f"e2e-evidence: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
