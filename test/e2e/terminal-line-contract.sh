#!/usr/bin/env bash
set -euo pipefail

# Pins the source line the smoke harness records in E2E_TERMINAL for each
# failure shape a smoke script can take. Every failing line below carries a
# `terminal-line:` marker comment, and the expected line is read from this file,
# so moving a line never needs a second edit.
#
# The function-internal shape has no ERR trap without errtrace, and errtrace is
# deliberately off (it would run the trap in every `$(...)` subshell too). That
# case is pinned as "no terminal record": never a wrong line, never a duplicate.

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
self="$root/test/e2e/terminal-line-contract.sh"

if [[ -n "${PROJMUX_TERMINAL_LINE_CASE:-}" ]]; then
  # shellcheck disable=SC1091 # The shared library is sourced at runtime; scripts/security.sh lints it on its own.
  source "$root/test/lib/smoke.sh"
  smoke_setup_env
  PROJMUX_E2E_SUITE="intentional-failure"
  export PROJMUX_E2E_SUITE
  smoke_contract_install_trap
  trap smoke_cleanup_env EXIT
  smoke_contract_begin L06 create-materialize resource-controller
  fail_return() { return 1; }
  fail_inside() {
    true
    false
  }
  case "$PROJMUX_TERMINAL_LINE_CASE" in
    top-assign)
      # shellcheck disable=SC2034
      value="$(false)" # terminal-line: top-assign
      ;;
    top-pipe)
      # shellcheck disable=SC2034
      value="$(printf 'a\nb\n' | false)" # terminal-line: top-pipe
      ;;
    top-return)
      fail_return # terminal-line: top-return
      ;;
    exit)
      exit 1 # terminal-line: exit
      ;;
    func-internal)
      fail_inside # terminal-line: func-internal
      ;;
    sourced-top)
      # The L20 shape: the contract began here, then a sourced file fails at its
      # own top level. The record names this file, so the line must be the
      # `source` call in this file, not a line of the sourced one.
      sourced="$(dirname "$PROJMUX_E2E_ARTIFACTS")/sourced-top.inc.sh"
      # shellcheck disable=SC2016
      printf '%s\n' 'true' 'value="$(false)"' >"$sourced"
      # shellcheck source=/dev/null
      source "$sourced" # terminal-line: sourced-top
      ;;
  esac
  exit 99
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

marker_line() {
  local name="$1" text number=0 found=0 found_count=0
  while IFS= read -r text || [[ -n "$text" ]]; do
    number=$((number + 1))
    if [[ "$text" == *"# terminal-line: $name" ]]; then
      found="$number"
      found_count=$((found_count + 1))
    fi
  done <"$self"
  if [[ "$found_count" != "1" ]]; then
    echo "FAIL terminal-line-contract: marker '$name' found $found_count times, want 1" >&2
    exit 1
  fi
  printf '%s\n' "$found"
}

cases=0
failed=0

# check_case NAME WANT: WANT is "terminal" (exactly one record at the marker
# line with status 1 and this file as source) or "none" (no record at all).
check_case() {
  local name="$1" want="$2" line status=0 verdict
  line="$(marker_line "$name")"
  PROJMUX_TERMINAL_LINE_CASE="$name" \
    PROJMUX_E2E_ARTIFACTS="$tmp/$name" \
    "$self" >"$tmp/$name.out" 2>"$tmp/$name.err" || status=$?
  verdict="$(python3 - "$tmp/$name.err" "$tmp/$name/L06-attempt-1.json" "$want" "$line" "$status" <<'PY'
import json
import pathlib
import sys

err_path, artifact_path, want, line, status = sys.argv[1:6]
line, status = int(line), int(status)
lines = pathlib.Path(err_path).read_text().splitlines()
records = [json.loads(text.split(" ", 1)[1]) for text in lines if text.startswith("E2E_TERMINAL ")]
problems = []
if want == "none":
    if status == 0:
        problems.append("child exited 0, want non-zero")
    if records:
        problems.append(f"want no E2E_TERMINAL record, got {len(records)}: {records}")
else:
    if status != 1:
        problems.append(f"child exit status: expected 1, got {status}")
    if len(records) != 1:
        problems.append(f"E2E_TERMINAL records: expected 1, got {len(records)}")
    else:
        record = records[0]
        if record["line"] != line:
            problems.append(f"line: expected {line}, got {record['line']}")
        if record["status"] != 1:
            problems.append(f"status: expected 1, got {record['status']}")
        if record["source"] != "test/e2e/terminal-line-contract.sh":
            problems.append(f"source: expected test/e2e/terminal-line-contract.sh, got {record['source']!r}")
        artifact = pathlib.Path(artifact_path)
        if not artifact.is_file():
            problems.append(f"missing attempt artifact {artifact.name}")
        elif json.loads(artifact.read_text()).get("terminal_line") != record["line"]:
            problems.append("attempt artifact terminal_line differs from the logged record")
print("; ".join(problems))
PY
)"
  if [[ -n "$verdict" ]]; then
    echo "FAIL terminal-line-contract case $name: $verdict" >&2
    echo "--- child stderr ($name) ---" >&2
    cat "$tmp/$name.err" >&2
    failed=$((failed + 1))
  fi
  cases=$((cases + 1))
}

check_case top-assign terminal
check_case top-pipe terminal
check_case top-return terminal
check_case exit terminal
check_case func-internal none
check_case sourced-top terminal

if [[ "$failed" != "0" ]]; then
  echo "FAIL terminal-line-contract ($failed of $cases cases)" >&2
  exit 1
fi
echo "PASS terminal-line-contract ($cases cases)"
