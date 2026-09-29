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

# test/lib/table.sh reads `-o wide` tables. wide_line renders one line the way
# the views pad it: every column but the last is padded by rune to its width
# plus two spaces, so a blank cell is only spaces. printf pads by byte, so it
# only pads the spaces.
wide_line() {
  local LC_ALL=C.UTF-8
  local line="" cell width
  for width in 6 8 8 7 7 13; do
    cell="$1"
    shift
    line+="$cell$(printf '%*s' "$((width - ${#cell}))" '')"
  done
  printf '%s\n' "$line$1"
}
wide_table() {
  local row
  local -a cells
  wide_line KIND NAME STATUS WINDOW AGENT TERMINATION AGE
  for row in "$@"; do
    IFS='|' read -r -a cells <<<"$row"
    wide_line "${cells[@]}"
  done
}

# check_table LABEL same|different LEFT RIGHT compares two tables with the AGE
# column removed.
check_table() {
  local label="$1" expected="$2" status=0 got
  bash -c 'source "$1"; [[ "$(table_without_age <<<"$2")" == "$(table_without_age <<<"$3")" ]]' \
    _ "$root/test/lib/table.sh" "$3" "$4" >"$workdir/stdout" 2>"$workdir/stderr" || status=$?
  case "$status" in
  0) got=same ;;
  1) got=different ;;
  *) got="exit $status" ;;
  esac
  if [[ "$got" == "$expected" && ! -s "$workdir/stdout" && ! -s "$workdir/stderr" ]]; then
    echo "PASS $label: $got"
    return
  fi
  echo "FAIL $label: $got, expected $expected" >&2
  failures=1
  cat "$workdir/stdout" "$workdir/stderr" >&2
  bash -c 'source "$1"; printf -- "--- left\n"; table_without_age <<<"$2"; printf -- "--- right\n"; table_without_age <<<"$3"' \
    _ "$root/test/lib/table.sh" "$3" "$4" >&2
}

check_table 'blank AGENT and TERMINATION cells, only AGE differs' same \
  "$(wide_table 'pane|pane-a|live|win-a|||1s' 'pane|pane-b|live|win-b|||0s')" \
  "$(wide_table 'pane|pane-a|live|win-a|||0s' 'pane|pane-b|live|win-b|||0s')"
check_table 'no blank cells, only AGE differs' same \
  "$(wide_table 'pane|pane-a|live|win-a|a1|exited|1s')" \
  "$(wide_table 'pane|pane-a|live|win-a|a1|exited|0s')"
check_table 'a cell other than AGE differs' different \
  "$(wide_table 'pane|pane-a|live|win-a|||0s')" \
  "$(wide_table 'pane|pane-a|offline|win-a|||0s')"
check_table 'the blank cell sits in another column' different \
  "$(wide_table 'pane|pane-a|live|win-a|x||0s')" \
  "$(wide_table 'pane|pane-a|live|win-a||x|0s')"
# The views pad by rune. Counted in bytes, the four extra bytes of the NAME
# cell move every later offset four characters left, so the end of the
# TERMINATION cell would fall into the AGE column and be dropped with it.
LC_ALL=C check_table 'multibyte cell before AGE under LC_ALL=C, only AGE differs' same \
  "$(wide_table 'pane|pääää|live|win-a||exit-code-1|1s')" \
  "$(wide_table 'pane|pääää|live|win-a||exit-code-1|0s')"
LC_ALL=C check_table 'multibyte cell before AGE under LC_ALL=C, the cell before AGE differs' different \
  "$(wide_table 'pane|pääää|live|win-a||exit-code-1|0s')" \
  "$(wide_table 'pane|pääää|live|win-a||exit-code-2|0s')"

# check_output LABEL EXPECTED FUNCTION ARGS... runs a test/lib/table.sh function
# and expects exit 0, EXPECTED on stdout, and nothing on stderr.
check_output() {
  local label="$1" expected="$2" status=0
  shift 2
  bash -c 'source "$1"; shift; "$@"' _ "$root/test/lib/table.sh" "$@" \
    >"$workdir/stdout" 2>"$workdir/stderr" || status=$?
  if [[ "$status" == 0 && "$(cat "$workdir/stdout")" == "$expected" && ! -s "$workdir/stderr" ]]; then
    echo "PASS $label"
    return
  fi
  echo "FAIL $label: exit $status, expected exit 0 and stdout: $expected" >&2
  failures=1
  cat "$workdir/stdout" "$workdir/stderr" >&2
}

# The second row has a blank cell before NAME, which shifts NAME out of its
# whitespace field.
named="$(wide_table 'pane|pane-a|live|win-a|||0s' '|pane-b|offline|win-b|||0s')"
check_output 'row by NAME cell behind a blank cell' \
  "$(sed -n 3p <<<"$named")" table_row "$named" pane-b
check_output 'row by NAME cell matches the whole cell' '' table_row "$named" pane
check_output 'NAME column keeps a row behind a blank cell' \
  $'pane-a\npane-b' table_column "$named" NAME
check_output 'column with no such header' '' table_column "$named" MISSING
exit "$failures"
