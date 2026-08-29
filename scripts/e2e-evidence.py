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
    "flake",
    "unowned-nondeterminism",
    "unrepeated-failure",
    "unattributed",
}
# The classification axis is not "does a retry pass" but "do we own the source of
# the nondeterminism". L06/L08 flip because of the Registry lock we ship, so a
# retry that passes names a defect we can remove and an owner who can be held to
# a deadline. C01 drives the real Codex app-server and observes the model's own
# judgement; a retry that flips there is an observation we do not own and cannot
# schedule away. Folding both into one `flake` value would hand a quarantine
# policy rows whose expiry nobody can meet.
UNOWNED_NONDETERMINISM_OWNERS = {"codex-appserver-adapter"}
CLASS_BASES = {
    "observed-nondeterminism",
    "reproduced-failure",
    "declared",
    "single-observation",
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
# Terminal attribution and diagnostics only exist for an attempt that actually
# ended in a failure, so they are additive and optional. A begin/pass record
# keeps the exact field set - and therefore the exact serialized bytes - it had
# before this schema grew.
OPTIONAL_FIELDS = {
    "terminal_line",
    "terminal_status",
    "terminal_source",
    "diagnostic",
    "class_basis",
    "prior_outcomes",
}
SOURCE_RE = re.compile(r"^(?:(?:test/e2e|test/lib)/[a-z0-9][a-z0-9._/-]*\.sh)?$")
FORBIDDEN_VALUE = re.compile(
    r"(?:-----BEGIN [A-Z ]*PRIVATE KEY-----|gh[pousr]_[A-Za-z0-9_]{20,}|"
    r"github_pat_[A-Za-z0-9_]{20,}|AKIA[0-9A-Z]{16}|(?i:password|token|secret)=)"
)


TERMINAL_OUTCOMES = {"pass", "fail", "cancel"}


def attempt_key(path: Path, root: Path) -> str:
    """Name the retry an evidence file belongs to.

    ``test-e2e-docker.sh`` gives every attempt its own
    ``attempt-<run>-<run-attempt>-<pid>`` directory, so the directory name is the
    one durable statement of which retry produced a record - the ``attempt``
    field inside the record is whatever the harness was told, and a rerun that
    never learns ``GITHUB_RUN_ATTEMPT`` reports ``1`` forever. When the root is
    itself a single attempt directory there is nothing above it to compare
    against and every record shares the empty key.
    """
    try:
        parts = path.relative_to(root).parts
    except ValueError:
        return ""
    for part in parts:
        if part.startswith("attempt-"):
            return part
    return ""


def read_summary(path: Path) -> list[dict[str, object]]:
    records: list[dict[str, object]] = []
    try:
        text = path.read_text(encoding="utf-8")
    except OSError:
        return records
    for line in text.splitlines():
        if not line.strip():
            continue
        try:
            record = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(record, dict):
            records.append(record)
    return records


def prior_terminal_outcomes(
    root: Path,
    current: str,
    scenario_id: str,
    binary_sha256: str,
) -> list[str]:
    """Terminal outcomes other attempts recorded for this exact binary.

    An empty ``binary_sha256`` is not evidence about a commit, so it never
    matches: comparing two runs is only meaningful when the same product build
    produced both.
    """
    if not binary_sha256:
        return []
    outcomes: set[str] = set()
    for path in sorted(root.rglob("summary.jsonl")):
        if attempt_key(path, root) == current:
            continue
        for record in read_summary(path):
            if record.get("scenario_id") != scenario_id:
                continue
            if record.get("binary_sha256") != binary_sha256:
                continue
            outcome = record.get("outcome")
            if outcome in TERMINAL_OUTCOMES:
                outcomes.add(str(outcome))
    return sorted(outcomes)


def classify(
    outcome: str,
    declared_class: str,
    owner: str,
    prior_outcomes: list[str],
) -> tuple[str, str]:
    """Derive the failure class from what the attempts were observed to do.

    A constant is not a predicate. ``deterministic-regression`` is only earned by
    a failure that was actually seen again on the same build; a single failure
    says nothing yet about reproduction and must not borrow that claim. When the
    same build was observed both failing and passing, the split is by whether we
    own the source of the nondeterminism, never by the retry outcome alone.
    """
    prior = set(prior_outcomes)
    if outcome not in {"fail", "pass"}:
        return declared_class or "unattributed", "declared"
    flipped = ("pass" in prior) if outcome == "fail" else ("fail" in prior)
    if flipped:
        if owner in UNOWNED_NONDETERMINISM_OWNERS:
            return "unowned-nondeterminism", "observed-nondeterminism"
        return "flake", "observed-nondeterminism"
    if outcome == "fail" and "fail" in prior:
        return "deterministic-regression", "reproduced-failure"
    if declared_class:
        return declared_class, "declared"
    if outcome == "fail":
        return "unrepeated-failure", "single-observation"
    return "environment", "single-observation"


def validate_terminal_attribution(record: dict[str, object]) -> None:
    if "terminal_status" in record:
        terminal_status = record["terminal_status"]
        if not isinstance(terminal_status, int) or isinstance(terminal_status, bool):
            raise ValueError("terminal_status must be an integer")
        if not 0 <= terminal_status <= 255:
            raise ValueError("terminal_status must be a wait-status byte")
    if "terminal_line" in record:
        terminal_line = record["terminal_line"]
        if not isinstance(terminal_line, int) or isinstance(terminal_line, bool):
            raise ValueError("terminal_line must be an integer")
        if terminal_line < 0:
            raise ValueError("terminal_line must be non-negative")
    if "terminal_source" in record:
        terminal_source = record["terminal_source"]
        if not isinstance(terminal_source, str) or not SOURCE_RE.fullmatch(terminal_source):
            raise ValueError("terminal_source is not an allowlisted repository E2E shell path")
        if ".." in terminal_source:
            raise ValueError("terminal_source must not traverse")
    if "diagnostic" in record and not isinstance(record["diagnostic"], dict):
        raise ValueError("diagnostic must be an object")
    if "class_basis" in record and record["class_basis"] not in CLASS_BASES:
        raise ValueError("invalid class_basis")
    if "prior_outcomes" in record:
        prior_outcomes = record["prior_outcomes"]
        if not isinstance(prior_outcomes, list) or not prior_outcomes:
            raise ValueError("prior_outcomes must be a non-empty array")
        if any(item not in TERMINAL_OUTCOMES for item in prior_outcomes):
            raise ValueError("prior_outcomes must be terminal outcomes")
        if list(prior_outcomes) != sorted(set(prior_outcomes)):
            raise ValueError("prior_outcomes must be sorted and unique")
        if "class_basis" not in record:
            raise ValueError("prior_outcomes requires class_basis")


def validate(record: dict[str, object]) -> None:
    present = set(record)
    missing = FIELDS - present
    extra = present - FIELDS - OPTIONAL_FIELDS
    if missing or extra:
        raise ValueError(f"field set mismatch: {sorted(missing | extra)}")
    validate_terminal_attribution(record)
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


def decode_object(text: str, label: str) -> dict[str, object]:
    try:
        value = json.loads(text)
    except json.JSONDecodeError as error:
        raise ValueError(f"{label} is not JSON: {error}") from error
    if not isinstance(value, dict):
        raise ValueError(f"{label} must be an object")
    return value


def terminal_attribution(text: str) -> dict[str, object]:
    """Project the emitted terminal record onto the attempt evidence fields.

    The terminal record written to stderr by ``e2e-first-failure.py`` is the one
    authority for what was actually attributed, degraded fields included. Reading
    it back here keeps the artifact and the log from disagreeing.
    """
    if not text:
        return {}
    terminal = decode_object(text, "terminal")
    projected: dict[str, object] = {}
    for source_field, target_field in (
        ("line", "terminal_line"),
        ("status", "terminal_status"),
        ("source", "terminal_source"),
    ):
        if source_field in terminal:
            projected[target_field] = terminal[source_field]
    return projected


def record_command(args: argparse.Namespace) -> int:
    directory = Path(args.directory)
    prior_outcomes: list[str] = []
    if args.evidence_root and args.outcome in {"fail", "pass"}:
        root = Path(args.evidence_root)
        prior_outcomes = prior_terminal_outcomes(
            root,
            attempt_key(directory / "summary.jsonl", root),
            args.scenario_id,
            args.binary_sha256,
        )
    typed_class, class_basis = classify(
        args.outcome, args.declared_class, args.owner, prior_outcomes
    )
    record = {
        "schema": "projmux.e2e-attempt/v1",
        "scenario_id": args.scenario_id,
        "suite": args.suite,
        "attempt": args.attempt,
        "phase": args.phase,
        "owner": args.owner,
        "class": typed_class,
        "outcome": args.outcome,
        "elapsed_ms": args.elapsed_ms,
        "binary_sha256": args.binary_sha256,
        "route_socket": args.route_socket,
        "state_sha256": args.state_sha256,
        "artifact": f"{args.scenario_id}-attempt-{args.attempt}.json",
        "replay": f"make test-e2e E2E_SCENARIO={args.scenario_id}",
    }
    # The derivation trail is only additive where a derivation actually happened.
    # An attempt with nothing to compare against keeps the exact field set - and
    # therefore the exact serialized bytes - it had before this schema grew.
    if prior_outcomes:
        record["class_basis"] = class_basis
        record["prior_outcomes"] = prior_outcomes
    record.update(terminal_attribution(args.terminal_json))
    if args.diagnostic:
        record["diagnostic"] = decode_object(args.diagnostic, "diagnostic")
    validate(record)
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


def classify_command(args: argparse.Namespace) -> int:
    """Expose the classification predicate so its truth table is testable alone."""
    typed_class, basis = classify(
        args.outcome, args.declared_class, args.owner, sorted(set(args.prior))
    )
    print(f"class={typed_class} basis={basis}")
    return 0


def flake_rate_command(args: argparse.Namespace) -> int:
    """Report per-scenario flake rate across every attempt under a root.

    Record-time classification only sees the attempts a runner can reach. A CI
    rerun starts on a fresh machine, so the durable cross-attempt view is this
    pass over downloaded evidence, and it is also where a class recorded by one
    attempt is confronted with what the other attempts went on to show.
    """
    root = Path(args.directory)
    observed: dict[tuple[str, str], dict[str, dict[str, str]]] = {}
    owners: dict[tuple[str, str], str] = {}
    for path in sorted(root.rglob("summary.jsonl")):
        key_attempt = attempt_key(path, root)
        for record in read_summary(path):
            outcome = record.get("outcome")
            if outcome not in TERMINAL_OUTCOMES:
                continue
            scenario = str(record.get("scenario_id", ""))
            binary = str(record.get("binary_sha256", ""))
            if not scenario:
                continue
            key = (scenario, binary)
            attempt = key_attempt or f"attempt-{record.get('attempt', 1)}"
            observed.setdefault(key, {})[attempt] = {
                "outcome": str(outcome),
                "class": str(record.get("class", "")),
            }
            owners[key] = str(record.get("owner", ""))

    flips = 0
    nondeterministic = 0
    for key in sorted(observed):
        scenario, binary = key
        attempts = observed[key]
        outcomes = {entry["outcome"] for entry in attempts.values()}
        failed = sum(1 for entry in attempts.values() if entry["outcome"] == "fail")
        passed = sum(1 for entry in attempts.values() if entry["outcome"] == "pass")
        if "fail" in outcomes and "pass" in outcomes:
            verdict = (
                "unowned-nondeterminism"
                if owners.get(key) in UNOWNED_NONDETERMINISM_OWNERS
                else "flake"
            )
            nondeterministic += 1
        elif failed:
            verdict = "deterministic-regression" if failed > 1 else "unrepeated-failure"
        else:
            verdict = "stable"
        total = len(attempts)
        rate = failed / total if total else 0.0
        print(
            f"E2E_FLAKE scenario={scenario} binary={binary[:8] or 'unknown'} "
            f"attempts={total} pass={passed} fail={failed} "
            f"flake_rate={rate:.3f} verdict={verdict}"
        )
        # A record whose stored class disagrees with what every attempt together
        # shows is the operator signal this report exists for: the earlier
        # attempt is not silently overwritten, it is named.
        for attempt in sorted(attempts):
            recorded = attempts[attempt]["class"]
            if verdict in {"flake", "unowned-nondeterminism"} and recorded != verdict:
                flips += 1
                print(
                    f"E2E_FLAKE_FLIP scenario={scenario} binary={binary[:8] or 'unknown'} "
                    f"attempt={attempt} recorded={recorded} verdict={verdict}"
                )
    print(
        f">> E2E flake report scenarios={len(observed)} "
        f"nondeterministic={nondeterministic} flips={flips}"
    )
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
    # The class a caller supplies is a declaration, not a verdict. It is used
    # only where the attempts themselves say nothing, and never outranks an
    # observed fail/pass split on the same build.
    record_parser.add_argument("--class", dest="declared_class", default="", choices=sorted(CLASS_NAMES) + [""])
    record_parser.add_argument("--evidence-root", default="")
    record_parser.add_argument("--outcome", required=True, choices=sorted(OUTCOMES))
    record_parser.add_argument("--elapsed-ms", required=True, type=int)
    record_parser.add_argument("--binary-sha256", default="")
    record_parser.add_argument("--route-socket", default="")
    record_parser.add_argument("--state-sha256", default="")
    record_parser.add_argument("--terminal-json", default="")
    record_parser.add_argument("--diagnostic", default="")
    record_parser.set_defaults(function=record_command)
    validate_parser = subparsers.add_parser("validate")
    validate_parser.add_argument("--terminal", action="store_true")
    validate_parser.add_argument("path")
    validate_parser.set_defaults(function=validate_command)
    hash_parser = subparsers.add_parser("result-hash")
    hash_parser.add_argument("--expected", default="")
    hash_parser.add_argument("directory")
    hash_parser.set_defaults(function=result_hash_command)
    classify_parser = subparsers.add_parser("classify")
    classify_parser.add_argument("--outcome", required=True, choices=sorted(OUTCOMES))
    classify_parser.add_argument("--class", dest="declared_class", default="", choices=sorted(CLASS_NAMES) + [""])
    classify_parser.add_argument("--owner", default="")
    classify_parser.add_argument("--prior", action="append", default=[], choices=sorted(TERMINAL_OUTCOMES))
    classify_parser.set_defaults(function=classify_command)
    flake_parser = subparsers.add_parser("flake-rate")
    flake_parser.add_argument("directory")
    flake_parser.set_defaults(function=flake_rate_command)
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
