#!/usr/bin/env python3
"""Validate projmux's exact deadcode baseline contract."""

from __future__ import annotations

import argparse
import re
import sys
from dataclasses import dataclass
from pathlib import Path


SYMBOL_RE = re.compile(r"[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)?")
FINDING_MARKER = "unreachable func: "


@dataclass(frozen=True)
class ParsedFile:
    symbols: frozenset[str]
    errors: tuple[str, ...]


def _source_lines(path: Path) -> list[tuple[int, str]]:
    try:
        text = path.read_text(encoding="utf-8")
    except (OSError, UnicodeError) as exc:
        raise ValueError(f"{path}: cannot read UTF-8 file: {exc}") from exc
    return list(enumerate(text.splitlines(), start=1))


def _validate_symbol(path: Path, line_number: int, symbol: str) -> str | None:
    if not SYMBOL_RE.fullmatch(symbol):
        return f"{path}:{line_number}: malformed symbol {symbol!r}"
    return None


def parse_allowlist(path: Path) -> ParsedFile:
    symbols: set[str] = set()
    ordered_symbols: list[str] = []
    errors: list[str] = []
    first_line: dict[str, int] = {}
    for line_number, line in _source_lines(path):
        if not line or line.startswith("#"):
            continue
        if line != line.strip():
            errors.append(f"{path}:{line_number}: leading/trailing whitespace is not allowed")
            continue
        if error := _validate_symbol(path, line_number, line):
            errors.append(error)
            continue
        if line in symbols:
            errors.append(
                f"{path}:{line_number}: duplicate symbol {line!r} "
                f"(first declared on line {first_line[line]})"
            )
            continue
        symbols.add(line)
        ordered_symbols.append(line)
        first_line[line] = line_number
    if ordered_symbols != sorted(ordered_symbols):
        errors.append(f"{path}: symbols must be sorted bytewise")
    return ParsedFile(frozenset(symbols), tuple(errors))


def parse_must_keep(path: Path) -> ParsedFile:
    symbols: set[str] = set()
    ordered_symbols: list[str] = []
    errors: list[str] = []
    first_line: dict[str, int] = {}
    for line_number, line in _source_lines(path):
        if not line or line.startswith("#"):
            continue
        if line != line.strip():
            errors.append(f"{path}:{line_number}: leading/trailing whitespace is not allowed")
            continue
        if line.count("\t") != 1:
            errors.append(
                f"{path}:{line_number}: expected exactly 'symbol<TAB>maintenance reason'"
            )
            continue
        symbol, reason = line.split("\t")
        if error := _validate_symbol(path, line_number, symbol):
            errors.append(error)
            continue
        if not reason.strip():
            errors.append(f"{path}:{line_number}: maintenance reason must not be empty")
            continue
        if reason != reason.strip():
            errors.append(f"{path}:{line_number}: maintenance reason has edge whitespace")
            continue
        if symbol in symbols:
            errors.append(
                f"{path}:{line_number}: duplicate symbol {symbol!r} "
                f"(first declared on line {first_line[symbol]})"
            )
            continue
        symbols.add(symbol)
        ordered_symbols.append(symbol)
        first_line[symbol] = line_number
    if ordered_symbols != sorted(ordered_symbols):
        errors.append(f"{path}: symbols must be sorted bytewise")
    return ParsedFile(frozenset(symbols), tuple(errors))


def parse_findings(path: Path) -> tuple[frozenset[str], tuple[str, ...], int]:
    symbols: set[str] = set()
    errors: list[str] = []
    finding_count = 0
    for line_number, line in _source_lines(path):
        if not line:
            continue
        prefix, marker, symbol = line.rpartition(FINDING_MARKER)
        if not marker or not prefix or symbol != symbol.strip():
            errors.append(f"{path}:{line_number}: malformed deadcode finding {line!r}")
            continue
        if error := _validate_symbol(path, line_number, symbol):
            errors.append(error)
            continue
        finding_count += 1
        symbols.add(symbol)
    return frozenset(symbols), tuple(errors), finding_count


def validate(allowlist_path: Path, must_keep_path: Path, findings_path: Path) -> list[str]:
    allowlist = parse_allowlist(allowlist_path)
    must_keep = parse_must_keep(must_keep_path)
    findings, finding_errors, _ = parse_findings(findings_path)
    errors = [*allowlist.errors, *must_keep.errors, *finding_errors]

    overlap = sorted(allowlist.symbols & must_keep.symbols)
    if overlap:
        errors.append(
            "symbols present in both current and proactive baselines: " + ", ".join(overlap)
        )

    stale = sorted(allowlist.symbols - findings)
    if stale:
        errors.append("stale current-baseline symbols: " + ", ".join(stale))

    new = sorted(findings - (allowlist.symbols | must_keep.symbols))
    if new:
        errors.append("new deadcode findings: " + ", ".join(new))

    return errors


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--allowlist", required=True, type=Path)
    parser.add_argument("--must-keep", required=True, type=Path)
    parser.add_argument("--findings", required=True, type=Path)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        errors = validate(args.allowlist, args.must_keep, args.findings)
        findings, _, finding_count = parse_findings(args.findings)
        allowlist = parse_allowlist(args.allowlist)
        must_keep = parse_must_keep(args.must_keep)
    except ValueError as exc:
        print(f">> deadcode baseline: {exc}", file=sys.stderr)
        return 1

    if errors:
        print(">> deadcode baseline: contract violation", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1

    print(
        ">> deadcode baseline: exact "
        f"({finding_count} findings / {len(findings)} symbols, "
        f"{len(allowlist.symbols)} current, {len(must_keep.symbols)} proactive)"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
