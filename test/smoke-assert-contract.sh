#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
fixture="$workdir/fixture.txt"
printf '%s\n' '--smoke-grep-probe' 'ordinary.present' 'ordinaryXabsent' >"$fixture"
failures=0

check_contains() {
  local label="$1" needle="$2" expected_status="$3" status=0
  # The helper exits on assertion failure, so observe it in a fresh shell.
  bash -c 'source "$1"; smoke_assert_file_contains "$2" "$3"' \
    _ "$root/test/lib/smoke.sh" "$fixture" "$needle" \
    >"$workdir/stdout" 2>"$workdir/stderr" || status=$?

  : >"$workdir/expected-stderr"
  if [[ "$expected_status" == 1 ]]; then
    printf 'expected %s to contain: %s\n' "$fixture" "$needle" >"$workdir/expected-stderr"
  fi

  if [[ "$status" != "$expected_status" ]]; then
    echo "FAIL $label: exit $status, expected $expected_status" >&2
    failures=1
  elif [[ -s "$workdir/stdout" ]] || ! cmp -s "$workdir/expected-stderr" "$workdir/stderr"; then
    echo "FAIL $label: exit $status, unexpected stdout/stderr (expected only the assertion diagnostic on failure)" >&2
    failures=1
  else
    echo "PASS $label: exit $status, stdout empty, stderr matches expected bytes"
    return
  fi
  cat "$workdir/stdout" "$workdir/stderr" >&2
}

check_contains 'present -- needle' '--smoke-grep-probe' 0
check_contains 'absent -- needle' '--smoke-grep-absent' 1
check_contains 'ordinary present needle' 'ordinary.present' 0
# A regex match would find ordinaryXabsent; fixed-string matching must not.
check_contains 'ordinary absent needle (fixed string)' 'ordinary.absent' 1
exit "$failures"
