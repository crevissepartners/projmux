from __future__ import annotations

import contextlib
import importlib.util
import io
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "deadcode_baseline.py"
SPEC = importlib.util.spec_from_file_location("deadcode_baseline", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
deadcode_baseline = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = deadcode_baseline
SPEC.loader.exec_module(deadcode_baseline)


class DeadcodeBaselineContractTest(unittest.TestCase):
    def run_gate(self, allowlist: str, must_keep: str, findings: str) -> tuple[int, str]:
        with tempfile.TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            paths = {
                "allowlist": root / "allowlist",
                "must_keep": root / "must-keep",
                "findings": root / "findings",
            }
            paths["allowlist"].write_text(allowlist, encoding="utf-8")
            paths["must_keep"].write_text(must_keep, encoding="utf-8")
            paths["findings"].write_text(findings, encoding="utf-8")
            stderr = io.StringIO()
            stdout = io.StringIO()
            with contextlib.redirect_stderr(stderr), contextlib.redirect_stdout(stdout):
                status = deadcode_baseline.main(
                    [
                        "--allowlist",
                        str(paths["allowlist"]),
                        "--must-keep",
                        str(paths["must_keep"]),
                        "--findings",
                        str(paths["findings"]),
                    ]
                )
            return status, stdout.getvalue() + stderr.getvalue()

    def test_exact_current_and_proactive_sets_pass(self) -> None:
        status, output = self.run_gate(
            "Current\n",
            "CompatibilityAPI\tRetained compatibility input.\n",
            "pkg/current.go:1:1: unreachable func: Current\n",
        )
        self.assertEqual(status, 0, output)
        self.assertIn("1 findings / 1 symbols, 1 current, 1 proactive", output)

    def test_duplicate_in_each_file_is_rejected(self) -> None:
        for allowlist, must_keep in (
            ("Current\nCurrent\n", "Keep\tReason.\n"),
            ("Current\n", "Keep\tReason.\nKeep\tReason again.\n"),
        ):
            with self.subTest(allowlist=allowlist, must_keep=must_keep):
                status, output = self.run_gate(
                    allowlist,
                    must_keep,
                    "pkg/current.go:1:1: unreachable func: Current\n",
                )
                self.assertEqual(status, 1)
                self.assertIn("duplicate symbol", output)

    def test_cross_file_duplicate_is_rejected(self) -> None:
        status, output = self.run_gate(
            "Current\n",
            "Current\tCompatibility proof.\n",
            "pkg/current.go:1:1: unreachable func: Current\n",
        )
        self.assertEqual(status, 1)
        self.assertIn("present in both current and proactive baselines", output)

    def test_proactive_symbol_may_become_a_finding(self) -> None:
        status, output = self.run_gate(
            "",
            "CompatibilityAPI\tRetained compatibility input.\n",
            "pkg/compat.go:1:1: unreachable func: CompatibilityAPI\n",
        )
        self.assertEqual(status, 0, output)
        self.assertIn("1 findings / 1 symbols, 0 current, 1 proactive", output)

    def test_proactive_finding_does_not_hide_stale_current_row(self) -> None:
        status, output = self.run_gate(
            "Stale\n",
            "CompatibilityAPI\tRetained compatibility input.\n",
            "pkg/compat.go:1:1: unreachable func: CompatibilityAPI\n",
        )
        self.assertEqual(status, 1)
        self.assertIn("stale current-baseline symbols: Stale", output)
        self.assertNotIn("new deadcode findings", output)

    def test_stale_current_and_new_finding_are_rejected(self) -> None:
        status, output = self.run_gate(
            "Stale\n",
            "Keep\tMigration seam.\n",
            "pkg/new.go:1:1: unreachable func: NewFinding\n",
        )
        self.assertEqual(status, 1)
        self.assertIn("stale current-baseline symbols: Stale", output)
        self.assertIn("new deadcode findings: NewFinding", output)

    def test_malformed_or_empty_must_keep_reason_is_rejected(self) -> None:
        for must_keep in ("Keep\n", "Keep\t\n", "Keep\t  \n", "Keep\tReason.\textra\n"):
            with self.subTest(must_keep=must_keep):
                status, output = self.run_gate(
                    "Current\n",
                    must_keep,
                    "pkg/current.go:1:1: unreachable func: Current\n",
                )
                self.assertEqual(status, 1)
                self.assertRegex(
                    output,
                    r"expected exactly|must not be empty|edge whitespace|leading/trailing whitespace",
                )

    def test_repeated_run_is_byte_identical(self) -> None:
        fixture = (
            "Current\n",
            "Keep\tProof seam.\n",
            "pkg/current.go:1:1: unreachable func: Current\n",
        )
        self.assertEqual(self.run_gate(*fixture), self.run_gate(*fixture))

    def test_unsorted_baseline_is_rejected(self) -> None:
        status, output = self.run_gate(
            "Zulu\nAlpha\n",
            "Keep\tProof seam.\n",
            "pkg/z.go:1:1: unreachable func: Zulu\n"
            "pkg/a.go:1:1: unreachable func: Alpha\n",
        )
        self.assertEqual(status, 1)
        self.assertIn("symbols must be sorted bytewise", output)


if __name__ == "__main__":
    unittest.main()
