#!/usr/bin/env python3
"""Resolve the reviewed security baselines against their single canonical pin.

`.security/security-current-findings.json` is the only place in the repository
that defines a reviewed baseline digest. Every consumer resolves the digest
through this module instead of carrying its own copy; a second copy is what let
the gosec pin drift unnoticed from 2026-09-18 while no CI job ran the gate.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import sys

CANONICAL_PIN = ".security/security-current-findings.json"

BASELINE_FILES = {
    "gosec": ".security/gosec-baseline.json",
    "staticcheck": ".security/staticcheck-baseline.json",
}


def resolve(root: pathlib.Path) -> dict[str, str]:
    """Return {baseline path: sha256} once every file matches the canonical pin.

    Raises SystemExit when a baseline file and its pin disagree, which is the
    drift this contract exists to catch.
    """
    pin = json.loads((root / CANONICAL_PIN).read_text(encoding="utf-8"))
    digests: dict[str, str] = {}
    for scanner, name in BASELINE_FILES.items():
        digest = hashlib.sha256((root / name).read_bytes()).hexdigest()
        pinned = pin["scanners"][scanner]["baseline_sha256"]
        if digest != pinned:
            raise SystemExit(
                f"security contract: reviewed baseline changed: {name} "
                f"(file {digest}, {CANONICAL_PIN} pins {pinned})"
            )
        digests[name] = digest
    return digests


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=".", help="repository root to resolve")
    args = parser.parse_args(argv)
    digests = resolve(pathlib.Path(args.root))
    json.dump(digests, sys.stdout, sort_keys=True, separators=(",", ":"))
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
