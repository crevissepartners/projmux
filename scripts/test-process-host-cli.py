#!/usr/bin/env python3
"""Run process fixtures against a fresh copied CLI, refusing missing evidence."""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
FILES = (
    "create_agent_process_cli_test.go",
    "codex_process_binding_test.go",
    "codex_process_ownership_test.go",
    "process_attention_wiring_test.go",
    "process_host_supervisor_test.go",
)

# This real-tmux test has its own unit/integration gate and opt-in variable.
EXCLUDED_FILES = {"internal/app/cli_window_rename_display_real_tmux_test.go"}


def check_fixture_files(root: Path) -> None:
    discovered = {str(path.relative_to(root))
                  for path in (root / "internal").rglob("*_test.go")
                  if '"PMX_TEST_CLI"' in path.read_text()}
    expected = {"internal/app/" + filename for filename in FILES}
    actual = discovered - EXCLUDED_FILES
    if actual != expected:
        raise ValueError("PMX_TEST_CLI file inventory changed: "
                         f"unlisted={sorted(actual - expected)}, "
                         f"no longer consuming={sorted(expected - actual)}")


def selected_tests(root: Path) -> list[str]:
    check_fixture_files(root)
    tests = []
    for filename in FILES:
        source = (root / "internal/app" / filename).read_text()
        names = re.findall(r"(?m)^func[ \t]+(Test[A-Z0-9_]\w*)\s*\(\s*\w+\s+\*testing\.T\s*,?\s*\)", source)
        if not names:
            raise ValueError(f"no tests selected from {filename}")
        tests.extend(names)
    if len(set(tests)) != len(tests):
        raise ValueError("duplicate selected test names")
    return sorted(tests)


def check_results(events: list[dict], expected: list[str], returncode: int) -> dict:
    passed = {e.get("Test") for e in events if e.get("Action") == "pass"}
    skipped = [e.get("Test", "package") for e in events if e.get("Action") == "skip"]
    failed = [e.get("Test", "package") for e in events if e.get("Action") == "fail"]
    missing = sorted(set(expected) - passed)
    packages = [e for e in events if "Test" not in e and e.get("Action") == "pass"]
    report = {"selected": len(expected), "passed": len(set(expected) & passed),
              "skip": skipped, "fail": failed, "missing": missing}
    if not expected or returncode or skipped or failed or missing or not packages:
        raise ValueError(f"process CLI evidence rejected: {json.dumps(report)}; exit={returncode}")
    return report


def isolated_env(root: Path, go: str) -> dict[str, str]:
    # Resolve cache paths before changing HOME; local runs reuse the caller's
    # cache and CI reuses setup-go's cache without changing provider homes.
    caches = json.loads(subprocess.check_output(
        [go, "env", "-json", "GOMODCACHE", "GOCACHE"], cwd=ROOT, text=True,
        env={**os.environ, "GOTOOLCHAIN": "local"}))
    env = {k: v for k, v in os.environ.items()
           if k not in {"HOME", "TMUX", "TMUX_PANE", "TMUX_TMPDIR", "CODEX_HOME",
                        "CLAUDE_CONFIG_DIR", "OPENAI_API_KEY", "CODEX_API_KEY",
                        "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}
           and not k.startswith(("PROJMUX_", "__PROJMUX_", "PMX_", "XDG_",
                                 "PROCESSHOST_TEST_", "ANTHROPIC_", "OPENAI_"))}
    env.update(caches)
    env.update(HOME=str(root / "home"), CODEX_HOME=str(root / "home/.codex"),
               CLAUDE_CONFIG_DIR=str(root / "home/.claude"),
               XDG_CONFIG_HOME=str(root / "config"), XDG_STATE_HOME=str(root / "state"),
               XDG_CACHE_HOME=str(root / "cache"), TMUX_TMPDIR=str(root / "tmux"),
               TMPDIR=str(root / "tmp"), GOTOOLCHAIN="local", GOMAXPROCS="1")
    for key in ("HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
                "TMUX_TMPDIR", "TMPDIR", "CODEX_HOME", "CLAUDE_CONFIG_DIR"):
        Path(env[key]).mkdir(parents=True, exist_ok=True)
    return env


def main() -> None:
    started = time.monotonic()
    expected = selected_tests(ROOT)
    go = os.environ.get("GO", "go")
    with tempfile.TemporaryDirectory(prefix="pmx-process-cli-", dir="/tmp") as temporary:
        root = Path(temporary)
        env = isolated_env(root, go)
        built, copy = root / "built-projmux", root / "projmux"
        build_started = time.monotonic()
        subprocess.run([go, "build", "-buildvcs=false", "-o", str(built), "./cmd/projmux"],
                       cwd=ROOT, env=env, check=True, timeout=180)
        shutil.copy2(built, copy)
        build_seconds = time.monotonic() - build_started
        env["PMX_TEST_CLI"] = str(copy)
        print(f"copied CLI sha256={hashlib.sha256(copy.read_bytes()).hexdigest()}", flush=True)
        selector = "^(" + "|".join(expected) + ")$"
        test_started = time.monotonic()
        result = subprocess.run([go, "test", "-json", "-count=1", "-cpu=1",
                                 "-timeout=180s", "-run", selector, "./internal/app"],
                                cwd=ROOT, env=env, text=True, stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, timeout=240)
        test_seconds = time.monotonic() - test_started
        events = []
        for line in result.stdout.splitlines():
            event = json.loads(line)
            events.append(event)
            if event.get("Output"):
                print(event["Output"], end="", flush=True)
        if result.stderr:
            print(result.stderr, end="", flush=True)
        report = check_results(events, expected, result.returncode)
        report["build_seconds"] = round(build_seconds, 3)
        report["test_command_seconds"] = round(test_seconds, 3)
        report["elapsed_seconds"] = round(time.monotonic() - started, 3)
        print("process CLI summary: " + json.dumps(report), flush=True)


if __name__ == "__main__":
    main()
