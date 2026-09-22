from __future__ import annotations

import hashlib
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

    def test_contract_gate_resolves_the_pin_through_the_shared_module(self) -> None:
        gate = (ROOT / CONTRACT_GATE).read_text(encoding="utf-8")
        self.assertIn(str(PIN_MODULE), gate)
        self.assertRegex(gate, r"security contract: reviewed baseline pin unresolved")

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

    def resolve(self, root: Path) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [sys.executable, str(ROOT / PIN_MODULE), "--root", str(root)],
            check=False,
            capture_output=True,
            text=True,
        )


if __name__ == "__main__":
    unittest.main()
