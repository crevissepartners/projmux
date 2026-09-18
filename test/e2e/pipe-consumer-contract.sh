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
#   grep-q        `| grep` (egrep, fgrep) with a short-option cluster holding
#                 `q` (`-q`, `-Fq`, `-Fxq`, `-qx`, ...), `--quiet`, or `--silent`
#
# `grep -q` exits at its first match, so a producer that writes again dies with
# SIGPIPE (or EPIPE rc=1 when SIGPIPE is ignored), and pipefail makes
# `if producer | grep -q X` false and `if ! producer | grep -q X` true although
# X is present. The adopted forms are the drain `producer | grep ... >/dev/null`,
# which reads to EOF and keeps a producer failure visible to pipefail, and a
# here-string on a captured variable (`grep -q X <<<"$out"`), which has no pipe.
#
# The scan is regex and quote heuristic based, not a shell parser. The first
# four patterns scan code inside quoted strings too (for example
# `sh -c "... | head -n 1"`). grep-q is reported only when the `|` is in bash
# code context: a quote-aware pass tracks single quotes, double quotes, `$(...)`,
# backticks, and trailing comments, and skips pipes inside quoted strings such
# as `sh -c "... | grep -q x"`, which run under sh without pipefail. The scan
# skips whole-line comments, and joins backslash-continued lines and lines
# ending in a single `|` into one logical line before matching. A finding
# reports the physical line that holds the consumer command word, not the
# logical line start.
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
      grep_q = "(^|[ \t])(-[A-Za-z0-9]*q[A-Za-z0-9]*|--quiet|--silent)([ \t]|$)"
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
    # grep_q_at reports buf[i] (a pipe in bash code context) when the command
    # after it is grep, egrep, or fgrep with a quiet option.
    function grep_q_at(i,   j, word, base) {
      if (substr(buf, i - 1, 1) == "|" || substr(buf, i + 1, 1) == "|") return
      j = i + 1
      if (substr(buf, j, 1) == "&") j++
      while (substr(buf, j, 1) ~ /[ \t]/) j++
      word = substr(buf, j)
      if (!match(word, /^[A-Za-z0-9_.\/-]+/)) return
      word = substr(word, 1, RLENGTH)
      base = word; sub(/.*\//, "", base)
      if (base !~ /^[ef]?grep$/) return
      command_span(j)
      if (bare ~ grep_q) report(j, "grep-q")
    }
    # scan_grep_q walks buf with a context stack: c (code), b (backtick code),
    # s (single-quoted), d (double-quoted). pd counts plain parentheses per
    # code frame so the right `)` closes a `$(` frame. Only a pipe whose top
    # frame is code or backtick code is bash context.
    function scan_grep_q(   i, n, c, top, s, prev, d, st, pd) {
      d = 1; st[1] = "c"; pd[1] = 0
      n = length(buf)
      for (i = 1; i <= n; i++) {
        c = substr(buf, i, 1)
        top = st[d]
        if (top == "s") { if (c == sq) d--; continue }
        if (top == "d") {
          if (c == "\\") { i++; continue }
          if (c == dq) { d--; continue }
          if (c == "$" && substr(buf, i + 1, 1) == "(") { d++; st[d] = "c"; pd[d] = 0; i++; continue }
          if (c == "`") { d++; st[d] = "b"; pd[d] = 0 }
          continue
        }
        if (c == "\\") { i++; continue }
        if (c == "#") {
          prev = (i == 1) ? " " : substr(buf, i - 1, 1)
          if (prev ~ /[ \t;&|(]/) {
            s = line_of(i)
            if (s < nseg) { i = segpos[s + 1] - 1; continue }
            break
          }
        }
        if (c == sq) { d++; st[d] = "s"; continue }
        if (c == dq) { d++; st[d] = "d"; continue }
        if (c == "`") {
          if (top == "b") d--
          else { d++; st[d] = "b"; pd[d] = 0 }
          continue
        }
        if (c == "$" && substr(buf, i + 1, 1) == "(") { d++; st[d] = "c"; pd[d] = 0; i++; continue }
        if (c == "(") { pd[d]++; continue }
        if (c == ")") {
          if (pd[d] > 0) pd[d]--
          else if (d > 1) d--
          continue
        }
        if (c == "|") grep_q_at(i)
      }
    }
    function flush() {
      if (nseg > 0) { scan_logical(); scan_grep_q() }
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
if tmux list-panes -F '#{pane_id}|x' \
  | grep -Fqx "$p"; then :; fi
x="$(pmx get panes | grep -q pane && echo y)"
pmx get panes | grep --quiet pane
pmx get panes | grep --silent pane
if ! pmx get panes | egrep -q pane; then :; fi
wait_for "d" sh -c "tail -c +1 '$log' | grep -aFq 'x'"
wait_for "d" sh -c \
  "tmux list-keys | grep -Fq 'x'"
sh -c 'a | grep -q b'
  "test -n \"\$(tmux list-clients | grep -q x)\""
pmx get panes | grep -Fx pane >/dev/null
grep -Fxq "$p" <<<"$uids"
grep -q pane "$file"
true # a | grep -q b
pmx get panes | grep -s pane >/dev/null
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
selftest:17: grep-q
selftest:23: grep-q
selftest:24: grep-q
selftest:25: grep-q
selftest:26: grep-q
selftest:27: grep-q
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

# grep-q controls under pipefail. The producer writes pane-1 first and then
# keeps writing line by line, far past a pipe buffer, so `grep -q` exits while
# the producer still writes. With the default disposition the old positive form
# must judge the present match false and the old negative form must judge it
# true; the drain and the captured here-string must judge it correctly. With
# the inherited disposition the old form is only reported and the drain must
# still judge the match present.
grepq_produce="for ((i = 1; i <= 50000; i++)); do printf 'pane-%d\\n' \"\$i\"; done 2>/dev/null"
# grepq_judge_script CONDITION prints a bash script that echoes how `if`
# judges CONDITION under pipefail.
grepq_judge_script() {
  printf 'set -o pipefail; if %s; then echo true; else echo false; fi' "$1"
}
grepq_old="$(with_default_sigpipe bash -c "$(grepq_judge_script "$grepq_produce | grep -Fxq pane-1")")"
grepq_drain="$(with_default_sigpipe bash -c "$(grepq_judge_script "$grepq_produce | grep -Fx pane-1 >/dev/null")")"
grepq_capture="$(with_default_sigpipe bash -c "$(grepq_judge_script "out=\"\$($grepq_produce)\"; grep -Fxq pane-1 <<<\"\$out\"")")"
grepq_old_negative="$(with_default_sigpipe bash -c "$(grepq_judge_script "! $grepq_produce | grep -Fxq pane-1")")"
grepq_drain_negative="$(with_default_sigpipe bash -c "$(grepq_judge_script "! $grepq_produce | grep -Fx pane-1 >/dev/null")")"
inherited_grepq_old="$(bash -c "$(grepq_judge_script "$grepq_produce | grep -Fxq pane-1")")"
inherited_grepq_drain="$(bash -c "$(grepq_judge_script "$grepq_produce | grep -Fx pane-1 >/dev/null")")"
echo "control (default SIGPIPE): if producer | grep -Fxq pane-1 -> $grepq_old (want false: present match judged absent)"
echo "control (default SIGPIPE): if producer | grep -Fx pane-1 >/dev/null -> $grepq_drain (want true)"
echo "control (default SIGPIPE): out=\"\$(producer)\"; grep -Fxq pane-1 <<<\"\$out\" -> $grepq_capture (want true)"
echo "control (default SIGPIPE): if ! producer | grep -Fxq pane-1 -> $grepq_old_negative (want true: present match judged absent)"
echo "control (default SIGPIPE): if ! producer | grep -Fx pane-1 >/dev/null -> $grepq_drain_negative (want false)"
echo "control (inherited SIGPIPE): if producer | grep -Fxq pane-1 -> $inherited_grepq_old (reported only)"
echo "control (inherited SIGPIPE): if producer | grep -Fx pane-1 >/dev/null -> $inherited_grepq_drain (want true)"
if [[ "$grepq_old" != "false" || "$grepq_old_negative" != "true" ]]; then
  echo "pipe-consumer-contract: grep-q control old forms judged positive=$grepq_old negative=$grepq_old_negative, not false/true; the producer did not outlive grep -q, so the controls prove nothing" >&2
  exit 1
fi
if [[ "$grepq_drain" != "true" || "$grepq_capture" != "true" || "$grepq_drain_negative" != "false" || "$inherited_grepq_drain" != "true" ]]; then
  echo "pipe-consumer-contract: adopted grep-q forms failed: drain=$grepq_drain capture=$grepq_capture drain negative=$grepq_drain_negative inherited drain=$inherited_grepq_drain" >&2
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
  echo "FAIL pipe-consumer-contract: $finding_count early-exit pipe consumer(s) in $finding_files of $file_count scanned file(s); drain them (sed -n 1p, awk found flag) or let a file reader stop itself with sed q and no pipe in front; for grep-q drop q and send grep output to /dev/null, or grep -q a captured variable through a here-string" >&2
  exit 1
fi
echo "PASS pipe-consumer-contract: $file_count $file_noun 0 early-exit pipe consumers; control head rc=$head_rc, drain rc=$drain_rc (default SIGPIPE); inherited head rc=$inherited_head_rc, drain rc=$inherited_drain_rc; self-stop rc=$self_stop_rc; grep-q old=$grepq_old drain=$grepq_drain capture=$grepq_capture old negative=$grepq_old_negative drain negative=$grepq_drain_negative (default SIGPIPE); inherited grep-q old=$inherited_grepq_old drain=$inherited_grepq_drain"
