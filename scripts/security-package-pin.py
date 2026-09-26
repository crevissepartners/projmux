#!/usr/bin/env python3
"""Check or refresh the pinned ./... package set against its canonical pin.

`.security/security-current-findings.json` pins `package_count` and
`package_set_sha256`: the sorted `go list ./...` import paths, joined with a
newline and terminated by one, hashed with sha256. This module is the only place
that computes that set. The contract gate and `make security-pin-contract` check
through it, and `make security-pin-refresh` rewrites the two values through it
after a Go package is added or removed.

A refresh changes only the two values. Every other byte of the pin (other keys,
key order, indentation, trailing newline) stays as it was, and a refresh that
finds both values current does not write the file at all.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import re
import subprocess
import sys
import tempfile

CANONICAL_PIN = ".security/security-current-findings.json"
REFRESH_TARGET = "make security-pin-refresh"

COUNT_KEY = "package_count"
DIGEST_KEY = "package_set_sha256"

# Each pattern captures the text up to the value, the value, and nothing after
# it, so a replacement touches the value bytes only.
VALUE_PATTERNS = {
    COUNT_KEY: re.compile(r'("package_count"\s*:\s*)(-?[0-9]+)'),
    DIGEST_KEY: re.compile(r'("package_set_sha256"\s*:\s*")([^"\\]*)(?=")'),
}


def compute(root: pathlib.Path) -> dict[str, object]:
    """Return {package_count, package_set_sha256} for `go list ./...` in root."""
    go = os.environ.get("GO") or "go"
    output = subprocess.check_output(
        [go, "list", "-f", "{{.ImportPath}}", "./..."], cwd=root, text=True
    )
    packages = sorted(output.splitlines())
    package_bytes = ("\n".join(packages) + "\n").encode()
    return {
        COUNT_KEY: len(packages),
        DIGEST_KEY: hashlib.sha256(package_bytes).hexdigest(),
    }


def pinned(pin: pathlib.Path) -> dict[str, object]:
    current = json.loads(pin.read_text(encoding="utf-8"))
    return {key: current.get(key) for key in (COUNT_KEY, DIGEST_KEY)}


def describe(values: dict[str, object]) -> str:
    return f"{values[COUNT_KEY]} packages, sha256 {str(values[DIGEST_KEY])[:12]}"


def check(root: pathlib.Path, pin: pathlib.Path) -> dict[str, object]:
    """Return the computed set once it matches the pin; exit non-zero otherwise."""
    actual = compute(root)
    expected = pinned(pin)
    if actual != expected:
        raise SystemExit(
            "security contract: canonical ./... package set differs from the pin in "
            f"{pin} (go list: {describe(actual)}; pin: {describe(expected)}). "
            f"If you added or removed a Go package, run `{REFRESH_TARGET}` "
            "and commit the resulting diff."
        )
    return actual


def rewrite(text: str, actual: dict[str, object]) -> str:
    """Replace the two pinned values in text, leaving every other byte alone."""
    replacements = {COUNT_KEY: str(actual[COUNT_KEY]), DIGEST_KEY: str(actual[DIGEST_KEY])}
    for key, pattern in VALUE_PATTERNS.items():
        matches = list(pattern.finditer(text))
        if len(matches) != 1:
            raise SystemExit(
                f"security pin: expected exactly one {key!r} value, found {len(matches)}"
            )
        start, end = matches[0].span(2)
        text = text[:start] + replacements[key] + text[end:]
    return text


def refresh(root: pathlib.Path, pin: pathlib.Path) -> bool:
    """Rewrite the two values when they drifted. Return whether the file changed."""
    actual = compute(root)
    original = pin.read_bytes()
    document = json.loads(original)
    before = {key: document.get(key) for key in (COUNT_KEY, DIGEST_KEY)}
    if before == actual:
        print(f"security pin: package set unchanged ({describe(actual)})")
        return False

    updated = rewrite(original.decode("utf-8"), actual).encode("utf-8")
    # The rewrite is textual; prove it changed the two values and nothing else.
    expected = dict(document)
    expected.update(actual)
    if json.loads(updated) != expected or list(json.loads(updated)) != list(document):
        raise SystemExit("security pin: refresh would change more than the package set")

    mode = pin.stat().st_mode & 0o7777
    handle, temporary = tempfile.mkstemp(prefix=f".{pin.name}.", dir=pin.parent)
    try:
        with os.fdopen(handle, "wb") as stream:
            stream.write(updated)
        os.chmod(temporary, mode)
        os.replace(temporary, pin)
    except BaseException:
        pathlib.Path(temporary).unlink(missing_ok=True)
        raise
    print(f"security pin: package set refreshed: {describe(before)} -> {describe(actual)}")
    return True


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=".", help="Go tree where `go list ./...` runs")
    parser.add_argument(
        "--pin", help=f"pin file to check or refresh (default: <root>/{CANONICAL_PIN})"
    )
    parser.add_argument(
        "--refresh",
        action="store_true",
        help="rewrite package_count and package_set_sha256 in the pin when they drifted",
    )
    args = parser.parse_args(argv)
    root = pathlib.Path(args.root)
    pin = pathlib.Path(args.pin) if args.pin else root / CANONICAL_PIN
    if args.refresh:
        refresh(root, pin)
        return 0
    json.dump(check(root, pin), sys.stdout, sort_keys=True, separators=(",", ":"))
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
