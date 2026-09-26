from __future__ import annotations

import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]

CANONICAL_PIN = Path(".security/security-current-findings.json")
PIN_MODULE = Path("scripts/security-baseline-pin.py")
PACKAGE_PIN_MODULE = Path("scripts/security-package-pin.py")
REFRESH_TARGET = "make security-pin-refresh"
PACKAGE_KEYS = ("package_count", "package_set_sha256")
# Masks the two package values so the bytes around them can be compared.
PACKAGE_VALUES = re.compile(
    r'("package_count"\s*:\s*)-?[0-9]+|("package_set_sha256"\s*:\s*")[^"]*'
)
CONTRACT_GATE = Path("test/security-contract.sh")

BASELINE_FILES = (
    Path(".security/gosec-baseline.json"),
    Path(".security/staticcheck-baseline.json"),
)

SHA256_LITERAL = re.compile(r"[0-9a-f]{64}")

# Audited trees for the negative sweep, and the only files inside them that may
# carry a 64-hex literal. Each entry names a digest that has nothing to do with
# the reviewed security baselines; a new entry needs the same justification.
AUDITED_TREES = ("test", "scripts")
SHA256_ALLOWLIST = {
    "scripts/agent-dialogue-codex-observation.py": "pinned Codex JSON-RPC schema digests",
    "scripts/agent-dialogue-native-policy.py": "pinned Codex config schema digests",
    "test/e2e/evidence-contract.sh": "aaaa/bbbb placeholder digests in a golden line",
    "test/integration/linux-smoke.sh": "all-zero checksum fixture for an update flow",
}


def tracked_files(*trees: str) -> list[Path]:
    output = subprocess.check_output(
        ["git", "ls-files", "-z", "--", *trees], cwd=ROOT, text=True
    )
    return [Path(name) for name in output.split("\0") if name]


def read_text(path: Path) -> str | None:
    try:
        return (ROOT / path).read_text(encoding="utf-8")
    except (UnicodeDecodeError, FileNotFoundError, IsADirectoryError):
        return None


class SecurityBaselinePinContractTest(unittest.TestCase):
    def test_reviewed_baseline_digest_has_exactly_one_definition(self) -> None:
        # The digest is read from the file, never restated here: a test that
        # spelled the value out would be the third copy of the thing this
        # contract exists to keep singular.
        everything = tracked_files(".")
        self.assertGreater(len(everything), 0, "git ls-files returned nothing")

        for baseline in BASELINE_FILES:
            digest = hashlib.sha256((ROOT / baseline).read_bytes()).hexdigest()
            with self.subTest(baseline=str(baseline)):
                holders = [
                    path
                    for path in everything
                    if digest in (read_text(path) or "")
                ]
                # Positive control: a sweep that cannot find the digest in the
                # canonical pin is broken, and its emptiness proves nothing.
                self.assertIn(
                    CANONICAL_PIN,
                    holders,
                    f"sweep is broken: {CANONICAL_PIN} does not hold {baseline}'s digest",
                )
                self.assertEqual(
                    [str(path) for path in holders],
                    [str(CANONICAL_PIN)],
                    f"{baseline} digest is defined in more than one place",
                )

    def test_audited_trees_carry_no_unlisted_sha256_literal(self) -> None:
        files = tracked_files(*AUDITED_TREES)
        # A sweep over zero files would pass vacuously.
        self.assertGreater(len(files), 50, f"audited sweep examined {len(files)} files")

        offenders = sorted(
            str(path)
            for path in files
            if str(path) not in SHA256_ALLOWLIST
            and SHA256_LITERAL.search(read_text(path) or "")
        )
        self.assertEqual(offenders, [], "unlisted sha256 literal in an audited tree")

        # Positive control: the regex and the reader still find a literal where
        # one is known to live.
        allowlisted = sorted(SHA256_ALLOWLIST)
        self.assertTrue(
            any(SHA256_LITERAL.search(read_text(Path(name)) or "") for name in allowlisted),
            "sweep is broken: no allowlisted file matched the sha256 pattern",
        )

        self.assertNotIn(str(CONTRACT_GATE), SHA256_ALLOWLIST)
        self.assertNotIn(str(PIN_MODULE), SHA256_ALLOWLIST)
        self.assertNotIn(str(PACKAGE_PIN_MODULE), SHA256_ALLOWLIST)

    def test_contract_gate_resolves_the_pin_through_the_shared_module(self) -> None:
        gate = (ROOT / CONTRACT_GATE).read_text(encoding="utf-8")
        self.assertIn(str(PIN_MODULE), gate)
        self.assertRegex(gate, r"security contract: reviewed baseline pin unresolved")

    def test_contract_gate_computes_the_package_set_only_through_the_shared_module(self) -> None:
        gate = (ROOT / CONTRACT_GATE).read_text(encoding="utf-8")
        self.assertIn(str(PACKAGE_PIN_MODULE), gate)
        self.assertNotIn('"go", "list"', gate)
        self.assertNotIn("go list", gate)

    def test_a_one_byte_baseline_change_fails_the_pin(self) -> None:
        for baseline in BASELINE_FILES:
            with self.subTest(baseline=str(baseline)):
                with tempfile.TemporaryDirectory() as temporary:
                    fixture = Path(temporary)
                    shutil.copytree(ROOT / ".security", fixture / ".security")
                    self.assertEqual(self.resolve(fixture).returncode, 0)

                    target = fixture / baseline
                    body = target.read_bytes()
                    # One byte, inside the JSON body, keeping the file parseable.
                    index = body.index(b'"')
                    target.write_bytes(body[:index] + b" " + body[index:])

                    drifted = self.resolve(fixture)
                    self.assertEqual(drifted.returncode, 1, drifted.stdout)
                    self.assertIn("reviewed baseline changed", drifted.stderr)
                    self.assertIn(str(baseline), drifted.stderr)

    def test_package_set_matches_the_pin(self) -> None:
        checked = self.package_pin("--root", str(ROOT))
        self.assertEqual(checked.returncode, 0, checked.stderr)
        pin = json.loads((ROOT / CANONICAL_PIN).read_text(encoding="utf-8"))
        self.assertEqual(
            json.loads(checked.stdout), {key: pin[key] for key in PACKAGE_KEYS}
        )

    def test_refresh_restores_a_drifted_package_pin_byte_for_byte(self) -> None:
        original = (ROOT / CANONICAL_PIN).read_bytes()
        pinned = json.loads(original)
        # A wrong digest derived from the real one, never spelled out.
        wrong_digest = hashlib.sha256(pinned["package_set_sha256"].encode()).hexdigest()
        self.assertNotEqual(wrong_digest, pinned["package_set_sha256"])
        drifted_text = original.decode("utf-8")
        drifted_text = drifted_text.replace(
            f'"package_count": {pinned["package_count"]}',
            f'"package_count": {pinned["package_count"] + 1}',
            1,
        ).replace(pinned["package_set_sha256"], wrong_digest, 1)
        drifted = drifted_text.encode("utf-8")
        self.assertNotEqual(drifted, original)

        with tempfile.TemporaryDirectory() as temporary:
            pin = Path(temporary) / "security-current-findings.json"
            pin.write_bytes(drifted)
            pin.chmod(0o640)

            checked = self.package_pin("--root", str(ROOT), "--pin", str(pin))
            self.assertEqual(checked.returncode, 1, checked.stdout)
            self.assertIn(REFRESH_TARGET, checked.stderr)
            self.assertEqual(pin.read_bytes(), drifted, "check mode wrote the pin")

            refreshed = self.package_pin("--root", str(ROOT), "--pin", str(pin), "--refresh")
            self.assertEqual(refreshed.returncode, 0, refreshed.stderr)
            self.assertIn("->", refreshed.stdout)
            body = pin.read_bytes()
            self.assertEqual(body, original, "refresh did not restore the pin byte for byte")
            self.assertEqual(pin.stat().st_mode & 0o7777, 0o640, "refresh changed the mode")
            self.assertEqual(
                PACKAGE_VALUES.sub(r"\1\2", body.decode("utf-8")),
                PACKAGE_VALUES.sub(r"\1\2", drifted_text),
                "refresh changed bytes outside the two package values",
            )
            self.assertEqual(json.loads(body)["scanners"], pinned["scanners"])
            self.assertEqual(list(json.loads(body)), list(pinned))
            self.assertEqual(os.listdir(temporary), [pin.name], "refresh left a temp file")

            before = pin.stat().st_mtime_ns
            again = self.package_pin("--root", str(ROOT), "--pin", str(pin), "--refresh")
            self.assertEqual(again.returncode, 0, again.stderr)
            self.assertIn("unchanged", again.stdout)
            self.assertEqual(pin.stat().st_mtime_ns, before, "a no-op refresh wrote the pin")
            self.assertEqual(pin.read_bytes(), original)

    def package_pin(self, *args: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [sys.executable, str(ROOT / PACKAGE_PIN_MODULE), *args],
            check=False,
            capture_output=True,
            text=True,
        )

    def resolve(self, root: Path) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [sys.executable, str(ROOT / PIN_MODULE), "--root", str(root)],
            check=False,
            capture_output=True,
            text=True,
        )


if __name__ == "__main__":
    unittest.main()
