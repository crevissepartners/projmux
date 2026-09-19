#!/usr/bin/env bash
set -euo pipefail

# Real-tmux boundary for a Project whose server stopped outside projmux.
#
# An exact `tmux kill-server` leaves the Registry's `status.session` saying
# `live: true`, and before this change `reconcile resources` against that
# socket failed at the runtime authority stage with `no server running`, so the
# flag stayed up forever. A server that is not running has no session, so
# every projection recorded live on its exact socket path has ended. This
# script proves the built binary lowers exactly those on a real server that is
# gone: from `--socket-path`, and from `--socket <name>` resolved through
# TMUX_TMPDIR the way tmux itself resolves it. A Project recorded on another,
# still-running server is untouched byte for byte, and once nothing is left to
# lower the command fails exactly as before without rewriting the Registry.
# Every case prints a PASS line so a skipped or silently empty run cannot pass.
#
# Isolation: `TMUX`, `TMUX_PANE` and `__PROJMUX_RUNTIME_ANCHOR_PANE` are dropped
# from every call; `HOME` and the XDG variables move the state domain under one
# owned root. Two app servers use the default app socket name inside two
# isolated TMUX_TMPDIR directories, each created before the first tmux or
# projmux call (a missing TMUX_TMPDIR silently falls back to the live socket
# directory). The root is a short `/tmp` path because a longer one overruns the
# 108-byte `sun_path` limit. Every kill targets an exact `#{socket_path}` read
# back from the server and checked to be under the owned root; there is no
# bare `kill-server`.
unset TMUX TMUX_PANE __PROJMUX_RUNTIME_ANCHOR_PANE

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
root="$(mktemp -d /tmp/pmx-ras.XXXXXX)"
real_tmux="$(command -v tmux)"
app_socket=projmux
tmpdir_a="$root/ta"
tmpdir_b="$root/tb"
socket_a=""
socket_b=""

for dir in "$tmpdir_a" "$tmpdir_b"; do
  mkdir -p "$dir"
  chmod 0700 "$dir"
  [[ -d "$dir" ]] || {
    echo "isolated TMUX_TMPDIR was not created: $dir" >&2
    exit 1
  }
done
export TMUX_TMPDIR="$tmpdir_a"

# kill_exact ends one server by its exact socket path, only under the owned root.
kill_exact() {
  local path="$1"
  [[ -n "$path" ]] || return 0
  case "$path" in
  "$root"/*)
    env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE "$real_tmux" -S "$path" kill-server >/dev/null 2>&1 || true
    ;;
  *)
    echo "refusing to kill a socket outside the owned root: $path" >&2
    return 1
    ;;
  esac
}

cleanup() {
  local status=$?
  kill_exact "$socket_a" || status=1
  kill_exact "$socket_b" || status=1
  for _ in $(seq 1 100); do
    if ! pgrep -af "$root" >/dev/null 2>&1; then
      break
    fi
    sleep 0.05
  done
  chmod -R u+w -- "$root" 2>/dev/null || true
  rm -rf -- "$root"
  exit "$status"
}
trap cleanup EXIT

fail() {
  echo "$*" >&2
  exit 1
}

# Build before the state domain moves, so the module cache stays outside it.
cd "$repo"
go build -o "$root/projmux" ./cmd/projmux

export HOME="$root/home"
export XDG_CONFIG_HOME="$HOME/.config"
export XDG_STATE_HOME="$HOME/.local/state"
export XDG_DATA_HOME="$HOME/.local/share"
export XDG_CACHE_HOME="$HOME/.cache"
export SHELL=/bin/sh
export LANG=C.UTF-8
mkdir -p "$HOME" "$HOME/p1" "$HOME/p2"
: >"$HOME/.zshrc"
cd "$HOME"

bin="$root/projmux"

# run_on <tmpdir> <args...> runs projmux outside tmux against the app server
# of one isolated TMUX_TMPDIR.
run_on() {
  local tmpdir="$1"
  shift
  env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE TMUX_TMPDIR="$tmpdir" "$bin" "$@"
}

tmux_by_name() {
  local tmpdir="$1"
  shift
  env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE TMUX_TMPDIR="$tmpdir" "$real_tmux" -L "$app_socket" "$@"
}

tmux_exact() {
  local path="$1"
  shift
  env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE "$real_tmux" -S "$path" "$@"
}

# session_projection prints `name live socketPath` of one Project's stored
# status.session, read through the public `get projects -o json` projection.
session_projection() {
  run_on "$tmpdir_a" get projects -o json | python3 -c '
import json, sys
uid = sys.argv[1]
for item in json.load(sys.stdin)["items"]:
    if item["metadata"]["uid"] == uid:
        session = item.get("status", {}).get("session") or {}
        print(session.get("name", ""), str(session.get("live", False)).lower(), session.get("socketPath", "<absent>"))
        break
else:
    sys.exit("Project %s is not listed" % uid)
' "$1"
}

# project_name prints one Project's Registry name.
project_name() {
  run_on "$tmpdir_a" get projects -o json | python3 -c '
import json, sys
uid = sys.argv[1]
for item in json.load(sys.stdin)["items"]:
    if item["metadata"]["uid"] == uid:
        print(item["metadata"]["name"])
        break
else:
    sys.exit("Project %s is not listed" % uid)
' "$1"
}

# project_status prints the STATUS column of one Project's `get projects` row.
project_status() {
  run_on "$tmpdir_a" get projects | awk -v name="$1" '
NR == 1 { for (i = 1; i <= NF; i++) if ($i == "STATUS") column = i; next }
$1 == name && column { print $column; found = 1 }
END { if (!column || !found) exit 1 }'
}

# registry_file prints the one Registry file of the isolated state domain.
registry_file() {
  local found
  found="$(find "$XDG_STATE_HOME" -path '*/metadata/registry.json' -type f -print)"
  [[ -n "$found" && "$(printf '%s\n' "$found" | wc -l)" -eq 1 ]] || fail "expected exactly one Registry file, found: $found"
  printf '%s\n' "$found"
}

# registry_project prints one Project's stored Registry record, canonically.
registry_project() {
  python3 -c '
import json, sys
with open(sys.argv[1]) as handle:
    registry = json.load(handle)
for project in registry.get("projects") or []:
    if project["metadata"]["uid"] == sys.argv[2]:
        print(json.dumps(project, sort_keys=True))
        break
else:
    sys.exit("Project %s is not in the Registry" % sys.argv[2])
' "$(registry_file)" "$1"
}

# wait_server_gone waits until the exact socket answers `no server running`,
# the state the product sees once a killed server has finished exiting.
wait_server_gone() {
  local path="$1" out=""
  for _ in $(seq 1 100); do
    out="$(tmux_exact "$path" display-message -p '#{socket_path}' 2>&1 || true)"
    if [[ "$out" == "no server running on $path" ]]; then
      return 0
    fi
    sleep 0.05
  done
  fail "server on $path did not finish exiting: $out"
}

p1_uid="$(run_on "$tmpdir_a" create project --root "$HOME/p1" -o uid)"
[[ "$p1_uid" =~ ^proj-[a-z0-9]+$ ]] || fail "create project p1 returned no uid: $p1_uid"
p2_uid="$(run_on "$tmpdir_a" create project --root "$HOME/p2" -o uid)"
[[ "$p2_uid" =~ ^proj-[a-z0-9]+$ ]] || fail "create project p2 returned no uid: $p2_uid"

# Server B holds p2 and server A holds p1. Each create runs the full
# reconciler pass on its own server, which records the other Project not live
# there; the explicit reconcile on each server afterwards restores its own
# Project's live projection from the session it observes, and leaves the
# Project recorded on the other server alone.
run_on "$tmpdir_b" config apply >"$root/config-apply-b.out" 2>&1 || fail "config apply B failed: $(cat "$root/config-apply-b.out")"
run_on "$tmpdir_b" create window -p "uid:$p2_uid" >"$root/create-window-p2.out" 2>&1 ||
  fail "create window p2 on B failed: $(cat "$root/create-window-p2.out")"
socket_b="$(tmux_by_name "$tmpdir_b" display-message -p '#{socket_path}')"
case "$socket_b" in
"$tmpdir_b"/*) ;;
*) fail "server B landed outside its owned TMUX_TMPDIR: $socket_b" ;;
esac
run_on "$tmpdir_a" config apply >"$root/config-apply-a.out" 2>&1 || fail "config apply A failed: $(cat "$root/config-apply-a.out")"
run_on "$tmpdir_a" create window -p "uid:$p1_uid" >"$root/create-window-p1.out" 2>&1 ||
  fail "create window p1 on A failed: $(cat "$root/create-window-p1.out")"
socket_a="$(tmux_by_name "$tmpdir_a" display-message -p '#{socket_path}')"
case "$socket_a" in
"$tmpdir_a"/*) ;;
*) fail "server A landed outside its owned TMUX_TMPDIR: $socket_a" ;;
esac
[[ "$socket_a" != "$socket_b" ]] || fail "the two servers share one socket: $socket_a"
run_on "$tmpdir_a" reconcile resources --socket-path "$socket_a" >"$root/reconcile-settle-a.out" 2>&1 ||
  fail "settling reconcile on A failed: $(cat "$root/reconcile-settle-a.out")"
run_on "$tmpdir_b" reconcile resources --socket-path "$socket_b" >"$root/reconcile-settle-b.out" 2>&1 ||
  fail "settling reconcile on B failed: $(cat "$root/reconcile-settle-b.out")"

read -r p1_session p1_live p1_socket <<<"$(session_projection "$p1_uid")"
read -r p2_session p2_live p2_socket <<<"$(session_projection "$p2_uid")"
[[ "$p1_live" == true && "$p1_socket" == "$socket_a" && -n "$p1_session" ]] ||
  fail "p1 projection is name=$p1_session live=$p1_live socketPath=$p1_socket, want live=true on $socket_a"
[[ "$p2_live" == true && "$p2_socket" == "$socket_b" && -n "$p2_session" ]] ||
  fail "p2 projection is name=$p2_session live=$p2_live socketPath=$p2_socket, want live=true on $socket_b"
tmux_exact "$socket_a" has-session -t "=$p1_session" 2>/dev/null || fail "server A has no session $p1_session"
tmux_exact "$socket_b" has-session -t "=$p2_session" 2>/dev/null || fail "server B has no session $p2_session"
echo "PASS: p1 is recorded live on server A and p2 live on server B, each with its server's exact #{socket_path}"

# (1) The exact server A is killed; the Registry does not notice by itself.
tmux_exact "$socket_a" kill-server
wait_server_gone "$socket_a"
[[ "$(session_projection "$p1_uid")" == "$p1_session true $socket_a" ]] ||
  fail "kill-server changed the Registry by itself: $(session_projection "$p1_uid")"
p2_record="$(registry_project "$p2_uid")"
echo "PASS: exact kill-server of server A leaves p1 recorded live=true"

absent_rc=0
absent_out="$(run_on "$tmpdir_a" reconcile resources --socket-path "$socket_a" 2>"$root/reconcile-absent.err")" || absent_rc=$?
[[ "$absent_rc" -eq 0 ]] || fail "reconcile on the absent server A exited $absent_rc: $(cat "$root/reconcile-absent.err") $absent_out"
echo "PASS: reconcile resources --socket-path on the absent server exits 0"
grep -qx -- "- exact server absent: $socket_a" <<<"$absent_out" || fail "receipt does not state the absent server: $absent_out"
grep -qx -- "- server absent: lowered 1 session projection(s) recorded on $socket_a" <<<"$absent_out" ||
  fail "receipt does not state lowered 1: $absent_out"
grep -qx -- "outcome: changed" <<<"$absent_out" || fail "absent-server reconcile outcome is not changed: $absent_out"
grep -qx -- "- Registry commit" <<<"$absent_out" || fail "absent-server reconcile made no Registry commit: $absent_out"
grep -qF -- "status.session.live true -> false (session \"$p1_session\" is recorded live on the exact socket $socket_a, whose server is not running" <<<"$absent_out" ||
  fail "absent-server reconcile did not describe lowering $p1_session: $absent_out"
echo "PASS: the receipt states the exact absent server, lowered 1, and the Registry commit"

read -r name live socket <<<"$(session_projection "$p1_uid")"
[[ "$name" == "$p1_session" && "$live" == false && "$socket" == "$socket_a" ]] ||
  fail "p1 after the absent-server lower is name=$name live=$live socketPath=$socket, want $p1_session false $socket_a"
status="$(project_status "$(project_name "$p1_uid")")" || fail "get projects lists no STATUS for p1"
[[ "$status" == offline ]] || fail "get projects STATUS for p1 is $status, want offline"
echo "PASS: p1 is live=false with its session name and socketPath kept, and get projects shows it offline"

[[ "$(registry_project "$p2_uid")" == "$p2_record" ]] || fail "the lower touched p2 on server B: $(registry_project "$p2_uid")"
tmux_exact "$socket_b" has-session -t "=$p2_session" 2>/dev/null || fail "server B lost session $p2_session"
echo "PASS: p2, recorded on the still-running server B, is byte-identical in the Registry"

# (2) Nothing left to lower: the same command fails exactly as before and
# does not rewrite the Registry.
registry_sha="$(sha256sum "$(registry_file)")"
repeat_rc=0
repeat_out="$(run_on "$tmpdir_a" reconcile resources --socket-path "$socket_a" 2>"$root/reconcile-repeat.err")" || repeat_rc=$?
# The tmux exit status carries the exit code, so the CLI prints nothing on
# stderr; the report's own error line is the failure message.
[[ "$repeat_rc" -eq 1 ]] || fail "repeat on the absent server exited $repeat_rc, want 1: $repeat_out"
grep -qx -- "outcome: failed" <<<"$repeat_out" || fail "repeat outcome is not failed: $repeat_out"
repeat_error="$(grep -m1 -- "^error: " <<<"$repeat_out" || true)"
grep -qF -- "no server running on $socket_a" <<<"$repeat_error" ||
  fail "repeat error does not name the missing server: $repeat_out $(cat "$root/reconcile-repeat.err")"
if grep -q -- "^completed stages:" <<<"$repeat_out"; then
  fail "the failing repeat reported completed stages: $repeat_out"
fi
if grep -qF -- "server absent" <<<"$repeat_out"; then
  fail "repeat reported another absent-server lower: $repeat_out"
fi
[[ "$(sha256sum "$(registry_file)")" == "$registry_sha" ]] || fail "the failing repeat rewrote the Registry"
echo "PASS: a second reconcile fails with rc=1 and no server running, as before, with the Registry sha unchanged"

# (3) A -L name resolves to tmux's own label path under TMUX_TMPDIR.
tmux_exact "$socket_b" kill-server
wait_server_gone "$socket_b"
label_out="$(run_on "$tmpdir_b" reconcile resources --socket "$app_socket" 2>"$root/reconcile-label.err")" ||
  fail "reconcile --socket on the absent server B failed: $(cat "$root/reconcile-label.err") $label_out"
grep -qx -- "- exact server absent: $socket_b" <<<"$label_out" || fail "--socket did not resolve server B's exact path: $label_out"
grep -qx -- "- server absent: lowered 1 session projection(s) recorded on $socket_b" <<<"$label_out" ||
  fail "--socket receipt does not state lowered 1: $label_out"
[[ "$(session_projection "$p2_uid")" == "$p2_session false $socket_b" ]] ||
  fail "p2 after the --socket lower: $(session_projection "$p2_uid")"
[[ "$(session_projection "$p1_uid")" == "$p1_session false $socket_a" ]] ||
  fail "the --socket lower on B changed p1: $(session_projection "$p1_uid")"
echo "PASS: reconcile resources --socket <name> resolves the absent server through TMUX_TMPDIR and lowers only p2"

echo "PASS: reconcile-absent-server-live-flag real-tmux boundary"
