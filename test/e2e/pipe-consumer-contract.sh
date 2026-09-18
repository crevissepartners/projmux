#!/usr/bin/env bash
# Pipe consumer contract for every test shell script.
#
# The e2e, integration, and install smokes run under `set -euo pipefail`, and
# the helpers they source inherit it. A pipe consumer that exits
# before reading all of its input lets the producer die with SIGPIPE (rc=141)
# on its next write, and pipefail plus `set -e` turns that into a smoke abort.
# Consumers must drain (`sed -n 1p`, awk with a `found` flag) and a file reader
# that wants the first match must stop itself (`sed -n '/RE/{s//\1/p;q;}' FILE`)
# with no pipe in front of it.
#
# Forbidden early-exit consumers, i.e. the command right after a single `|`:
#   head          any `| head`
#   awk-exit      `| awk` (gawk, mawk, nawk) whose command text has an `exit` word
#   sed-q         `| sed` whose script has a `q`/`Q` command
#   grep-m        `| grep` (egrep, fgrep) with `-m N`, `-mN`, or `--max-count`
#
# The scan is regex and quote heuristic based, not a shell parser. It scans
# code inside quoted strings too (for example `sh -c "... | head -n 1"`), skips
# whole-line comments, and joins backslash-continued lines and lines ending in a
# single `|` into one logical line before matching. A finding reports the
# physical line that holds the consumer command word, not the logical line
# start. `| grep -q` is out of scope and is not reported.
#
# The SIGPIPE controls run in a child with SIGPIPE restored to its default: CI
# runners can start jobs with SIGPIPE ignored, which bash cannot undo, and then
# `seq | head` fails with a write error (rc=1) instead of the 141 the e2e
# containers see. The inherited disposition is measured too, to show the drain
# form is safe either way.
#
# With no arguments the scan covers every `*.sh` file under test/, so a new
# script or sourced helper is covered without being registered here. The guard
# itself is the one exclusion: its detector self-test heredoc and SIGPIPE
# controls hold the forbidden forms on purpose. Discovery fails closed: an
# empty set, or one without the two linux-smoke.sh scripts, is an error rather
# than a silent pass.
#
# Usage: test/e2e/pipe-consumer-contract.sh [file...]
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
self_display="test/e2e/pipe-consumer-contract.sh"
contract_root="$(mktemp -d)"
trap 'rm -rf "$contract_root"' EXIT

# scan FILE DISPLAY prints one `DISPLAY:LINE: PATTERN: TEXT` per finding.
scan() {
  awk -v path="$2" -v sq="'" -v dq='"' '
    BEGIN {
      # Plain bracket sets only, so mawk, gawk, and BSD awk agree.
      awk_exit = "(^|[^A-Za-z0-9_])exit([^A-Za-z0-9_]|$)"
      sed_q = "(^|[;{} \t0-9$/" dq sq "])[qQ]([ \t]*[0-9]+)?[ \t]*([;}" dq sq "]|$)"
      grep_m = "(^|[ \t])(-[A-Za-z]*m[A-Za-z0-9]*|--max-count(=[^ \t]*)?)([ \t]|$)"
    }
    function line_of(pos,   s) {
      for (s = nseg; s > 1; s--) if (pos >= segpos[s]) break
      return s
    }
    function report(pos, name,   s) {
      s = line_of(pos)
      printf "%s:%d: %s: %s\n", path, segline[s], name, segtext[s]
    }
    # Walk the command that starts at buf[j] with fresh quoting and stop at
    # the first unquoted | ; & ) or backtick. Sets span and bare (span with
    # quoted text removed).
    function command_span(j,   k, ch, q) {
      span = ""; bare = ""; q = ""
      for (k = j; k <= length(buf); k++) {
        ch = substr(buf, k, 1)
        if (q == sq) { if (ch == sq) q = ""; span = span ch; continue }
        if (q == dq) {
          if (ch == "\\") { span = span ch substr(buf, k + 1, 1); k++; continue }
          if (ch == dq) q = ""
          span = span ch
          continue
        }
        if (ch == "\\") { span = span ch substr(buf, k + 1, 1); bare = bare ch substr(buf, k + 1, 1); k++; continue }
        if (ch == sq || ch == dq) { q = ch; span = span ch; continue }
        if (ch ~ /[|;&)`]/) break
        span = span ch; bare = bare ch
      }
    }
    function scan_logical(   i, j, c, word, base) {
      for (i = 1; i <= length(buf); i++) {
        c = substr(buf, i, 1)
        if (c != "|") continue
        if (substr(buf, i - 1, 1) == "|" || substr(buf, i + 1, 1) == "|") continue
        j = i + 1
        if (substr(buf, j, 1) == "&") j++
        while (substr(buf, j, 1) ~ /[ \t]/) j++
        word = substr(buf, j)
        if (!match(word, /^[A-Za-z0-9_.\/-]+/)) continue
        word = substr(word, 1, RLENGTH)
        base = word; sub(/.*\//, "", base)
        if (base == "head") { report(j, "head"); continue }
        if (base !~ /^(g|m|n)?awk$/ && base != "sed" && base !~ /^[ef]?grep$/) continue
        command_span(j)
        if (base ~ /awk$/ && span ~ awk_exit) report(j, "awk-exit")
        if (base == "sed" && span ~ sed_q) report(j, "sed-q")
        if (base ~ /grep$/ && bare ~ grep_m) report(j, "grep-m")
      }
    }
    function flush() {
      if (nseg > 0) scan_logical()
      buf = ""; nseg = 0
    }
    {
      text = $0
      if (nseg == 0 && text ~ /^[ \t]*#/) next
      nseg++
      segpos[nseg] = length(buf) + 1; segline[nseg] = NR; segtext[nseg] = text
      if (match(text, /\\+$/) && RLENGTH % 2 == 1) {
        buf = buf substr(text, 1, length(text) - 1) " "
        next
      }
      buf = buf text
      if (text ~ /(^|[^|])\|[ \t]*$/) { buf = buf " "; next }
      flush()
    }
    END { flush() }
  ' "$1"
}

# Detector self-test: each forbidden form is reported on the expected line and
# legitimate forms are not.
selftest="$contract_root/selftest.sh"
cat >"$selftest" <<'EOF'
a="$(tmux list-panes -F '#{pane_id}' | head -n 1)"
b="$(tmux list-panes -F '#{pane_id}|#{@kind}' | awk -F '|' '$2 == "Window" { print $1; exit }')"
c="$(pmx describe window w -o json | sed -n '/"uid"/{p;q;}')"
d="$(pmx get panes | sed 1q)"
e="$(pmx get panes | grep -m 1 pane)"
f="$(pmx get panes | grep --max-count=1 pane)"
g="$(pmx get panes \
  | head -n 1)"
h="$(pmx get panes |
  grep -Fm1 pane)"
  "test -n \"\$(tmux list-clients 2>/dev/null | head -n 1)\""
ok1="$(tmux list-panes -F '#{pane_id}' | sed -n 1p)"
ok2="$(sed -n '/.*"paneRef": "\([^"]*\)".*/{s//\1/p;q;}' "$file")"
ok3="$(tmux list-panes -F '#{pane_id}|#{@kind}' | awk -F '|' '$2 == "Window" && !found { print $1; found = 1 }')"
ok4="$(sed -n "/^x/{s//y/p;q;}" "$file" \
  | sed 's/,$//; s/^"//; s/"$//')"
if tmux list-panes | grep -Fq pane; then :; fi
ok5="$(tmux list-panes | awk -v exitrec="$x" '{ print $1 }')"
ok6="$(tmux list-panes | grep -c queue)" || exit 1
ok7="$(tmux list-panes | sed -n '/queue/p')"
# comment mentioning | head -n 1 is ignored
EOF
expected_selftest="$(
  cat <<'EOF'
selftest:1: head
selftest:2: awk-exit
selftest:3: sed-q
selftest:4: sed-q
selftest:5: grep-m
selftest:6: grep-m
selftest:8: head
selftest:10: grep-m
selftest:11: head
EOF
)"
actual_selftest="$(scan "$selftest" selftest | cut -d: -f1-3)"
if [[ "$actual_selftest" != "$expected_selftest" ]]; then
  echo "pipe-consumer-contract: detector self-test mismatch" >&2
  echo "expected:" >&2
  printf '%s\n' "$expected_selftest" >&2
  echo "actual:" >&2
  printf '%s\n' "$actual_selftest" >&2
  exit 1
fi

# with_default_sigpipe CMD... runs CMD with SIGPIPE restored to SIG_DFL.
with_default_sigpipe() {
  python3 -c 'import os, signal, sys; signal.signal(signal.SIGPIPE, signal.SIG_DFL); os.execvp(sys.argv[1], sys.argv[1:])' "$@"
}

# SIGPIPE controls under pipefail. With the default disposition the old form
# must fail with 141 and the drain must not. With the inherited disposition the
# old form fails either way (141, or 1 when SIGPIPE is ignored) and the drain
# must still succeed.
big="$contract_root/big.json"
seq 100000 | sed 's/.*/  "paneRef": "pane-&",/' >"$big"
head_rc=0
with_default_sigpipe bash -c 'set -o pipefail; seq 100000 | head -n 1 >/dev/null' || head_rc=$?
drain_rc=0
with_default_sigpipe bash -c 'set -o pipefail; seq 100000 | sed -n 1p >/dev/null' || drain_rc=$?
inherited_head_rc=0
(set -o pipefail && seq 100000 2>/dev/null | head -n 1 >/dev/null) || inherited_head_rc=$?
inherited_drain_rc=0
(set -o pipefail && seq 100000 | sed -n 1p >/dev/null) || inherited_drain_rc=$?
self_stop_rc=0
self_stop_out="$(sed -n '/.*"paneRef": "\([^"]*\)".*/{s//\1/p;q;}' "$big")" || self_stop_rc=$?
echo "control (default SIGPIPE): seq 100000 | head -n 1 rc=$head_rc (want 141)"
echo "control (default SIGPIPE): seq 100000 | sed -n 1p rc=$drain_rc (want 0)"
echo "control (inherited SIGPIPE): seq 100000 | head -n 1 rc=$inherited_head_rc (want non-zero)"
echo "control (inherited SIGPIPE): seq 100000 | sed -n 1p rc=$inherited_drain_rc (want 0)"
echo "control: self-stopping sed on 100000-line file rc=$self_stop_rc out=$self_stop_out (want 0, pane-1)"
if [[ "$head_rc" != "141" ]]; then
  echo "pipe-consumer-contract: control 'seq 100000 | head -n 1' returned rc=$head_rc, not 141; SIGPIPE may be ignored in this environment, so the controls prove nothing" >&2
  exit 1
fi
if [[ "$inherited_head_rc" == "0" ]]; then
  echo "pipe-consumer-contract: control 'seq 100000 | head -n 1' under the inherited SIGPIPE disposition returned rc=0; pipefail is not reporting the producer failure" >&2
  exit 1
fi
if [[ "$drain_rc" != "0" || "$inherited_drain_rc" != "0" || "$self_stop_rc" != "0" || "$self_stop_out" != "pane-1" ]]; then
  echo "pipe-consumer-contract: adopted forms failed: drain rc=$drain_rc inherited drain rc=$inherited_drain_rc self-stop rc=$self_stop_rc out=$self_stop_out" >&2
  exit 1
fi

targets=()
if [[ $# -gt 0 ]]; then
  targets=("$@")
else
  while IFS= read -r candidate; do
    if [[ "${candidate#"$root"/}" != "$self_display" ]]; then
      targets+=("$candidate")
    fi
  done < <(find "$root/test" -type f -name '*.sh' | LC_ALL=C sort)
  if [[ ${#targets[@]} -eq 0 ]]; then
    echo "pipe-consumer-contract: discovered no *.sh files under $root/test; refusing to pass an empty scan" >&2
    exit 1
  fi
  for required in test/e2e/linux-smoke.sh test/integration/linux-smoke.sh; do
    required_found=0
    for target in "${targets[@]}"; do
      if [[ "${target#"$root"/}" == "$required" ]]; then
        required_found=1
      fi
    done
    if [[ "$required_found" != "1" ]]; then
      echo "pipe-consumer-contract: discovery under $root/test did not find $required; refusing to pass a scan that misses it" >&2
      exit 1
    fi
  done
fi

findings=""
finding_files=0
for target in "${targets[@]}"; do
  display="${target#"$root"/}"
  if [[ ! -f "$target" ]]; then
    echo "pipe-consumer-contract: $display is not a file" >&2
    exit 1
  fi
  file_findings="$(scan "$target" "$display")"
  if [[ -n "$file_findings" ]]; then
    findings+="$file_findings"$'\n'
    finding_files=$((finding_files + 1))
  fi
done
file_count=${#targets[@]}
file_noun="files have"
if [[ "$file_count" -eq 1 ]]; then
  file_noun="file has"
fi
if [[ -n "$findings" ]]; then
  printf '%s' "$findings"
  finding_count=$(($(printf '%s' "$findings" | wc -l)))
  echo "FAIL pipe-consumer-contract: $finding_count early-exit pipe consumer(s) in $finding_files of $file_count scanned file(s); drain them (sed -n 1p, awk found flag) or let a file reader stop itself with sed q and no pipe in front" >&2
  exit 1
fi
echo "PASS pipe-consumer-contract: $file_count $file_noun 0 early-exit pipe consumers; control head rc=$head_rc, drain rc=$drain_rc (default SIGPIPE); inherited head rc=$inherited_head_rc, drain rc=$inherited_drain_rc; self-stop rc=$self_stop_rc"
