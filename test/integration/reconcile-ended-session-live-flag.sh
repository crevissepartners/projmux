#!/usr/bin/env bash
set -euo pipefail

# Real-tmux boundary for a Project session that ends outside projmux.
#
# A raw `tmux kill-session` leaves the Registry's `status.session` saying
# `live: true`. The Project recorded the exact socket path of the server its
# session was created on, so `reconcile resources` against that same server can
# attribute the absence and lower the flag, keeping the session name and
# socketPath. The unit tests pin the judgement against fakes; this script
# proves the built binary does it on a real server, both from outside tmux with
# `--socket-path` and from inside tmux with the inherited `$TMUX`, and that the
# next pass is a Registry no-op. Every case prints a PASS line so a skipped or
# silently empty run cannot pass.
#
# Isolation: `TMUX`, `TMUX_PANE` and `__PROJMUX_RUNTIME_ANCHOR_PANE` are dropped
# from every call; `HOME` and the XDG variables move the state domain under one
# owned root. The server uses the default app socket name inside an isolated
# `TMUX_TMPDIR`, and that directory is created before the first tmux or projmux
# call (a missing `TMUX_TMPDIR` silently falls back to the live socket
# directory). The root is a short `/tmp` path because a longer one overruns the
# 108-byte `sun_path` limit. Cleanup reads back the exact `#{socket_path}` and
# ends only a socket under the owned root; there is no bare `kill-server`.
unset TMUX TMUX_PANE __PROJMUX_RUNTIME_ANCHOR_PANE

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
root="$(mktemp -d /tmp/pmx-rel.XXXXXX)"
real_tmux="$(command -v tmux)"
app_socket=projmux

# TMUX_TMPDIR must exist before anything can talk to tmux.
export TMUX_TMPDIR="$root/tmux"
mkdir -p "$TMUX_TMPDIR"
chmod 0700 "$TMUX_TMPDIR"
[[ -d "$TMUX_TMPDIR" ]] || {
  echo "isolated TMUX_TMPDIR was not created: $TMUX_TMPDIR" >&2
  exit 1
}

iso_tmux_by_name() {
  env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE "$real_tmux" -L "$app_socket" "$@"
}

cleanup() {
  local status=$?
  local actual
  actual="$(iso_tmux_by_name display-message -p '#{socket_path}' 2>/dev/null || true)"
  if [[ -n "$actual" ]]; then
    case "$actual" in
    "$root"/tmux/*)
      env -u TMUX -u TMUX_PANE "$real_tmux" -S "$actual" kill-server >/dev/null 2>&1 || true
      ;;
    *)
      echo "refusing cleanup of a socket outside the owned root: $actual" >&2
      exit 1
      ;;
    esac
  fi
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

# Every outside-tmux call runs with no tmux environment of any kind.
run_outside() {
  env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE "$bin" "$@"
}

# session_projection prints `name live socketPath` of one Project's stored
# status.session, read through the public `get projects -o json` projection.
session_projection() {
  run_outside get projects -o json | python3 -c '
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
  run_outside get projects -o json | python3 -c '
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
  run_outside get projects | awk -v name="$1" '
NR == 1 { for (i = 1; i <= NF; i++) if ($i == "STATUS") column = i; next }
$1 == name && column { print $column; found = 1 }
END { if (!column || !found) exit 1 }'
}

p1_uid="$(run_outside create project --root "$HOME/p1" -o uid)"
[[ "$p1_uid" =~ ^proj-[a-z0-9]+$ ]] || fail "create project p1 returned no uid: $p1_uid"
p2_uid="$(run_outside create project --root "$HOME/p2" -o uid)"
[[ "$p2_uid" =~ ^proj-[a-z0-9]+$ ]] || fail "create project p2 returned no uid: $p2_uid"

# Bring the app server up through the product and materialize both Project
# sessions on it.
run_outside config apply >"$root/config-apply.out" 2>&1 || fail "config apply failed: $(cat "$root/config-apply.out")"
run_outside create window -p "uid:$p1_uid" >"$root/create-window-p1.out" 2>&1 ||
  fail "create window p1 failed: $(cat "$root/create-window-p1.out")"
run_outside create window -p "uid:$p2_uid" >"$root/create-window-p2.out" 2>&1 ||
  fail "create window p2 failed: $(cat "$root/create-window-p2.out")"
socket_path="$(iso_tmux_by_name display-message -p '#{socket_path}')"
case "$socket_path" in
"$root"/tmux/*) ;;
*) fail "isolated app server landed outside the owned root: $socket_path" ;;
esac
iso_tmux() { env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE "$real_tmux" -S "$socket_path" "$@"; }
[[ "$(iso_tmux show-options -gqv @projmux_app)" == "1" ]] || fail "the app server carries no @projmux_app marker"

read -r p1_session p1_live p1_socket <<<"$(session_projection "$p1_uid")"
read -r p2_session p2_live p2_socket <<<"$(session_projection "$p2_uid")"
[[ -n "$p1_session" && -n "$p2_session" && "$p1_session" != "$p2_session" ]] ||
  fail "the two Projects project no distinct session names: p1=$p1_session p2=$p2_session"
[[ "$p1_live" == true && "$p1_socket" == "$socket_path" ]] ||
  fail "p1 projection after create is live=$p1_live socketPath=$p1_socket, want live=true on $socket_path"
[[ "$p2_live" == true && "$p2_socket" == "$socket_path" ]] ||
  fail "p2 projection after create is live=$p2_live socketPath=$p2_socket, want live=true on $socket_path"
iso_tmux has-session -t "=$p1_session" 2>/dev/null || fail "create window did not materialize session $p1_session"
iso_tmux has-session -t "=$p2_session" 2>/dev/null || fail "create window did not materialize session $p2_session"
echo "PASS: created Project sessions record live=true with socketPath equal to the server's #{socket_path}"

# A keeper session keeps the server alive once the Project sessions end.
iso_tmux new-session -d -s keeper 'exec sleep 3600'
keeper_pane="$(iso_tmux display-message -p -t '=keeper:' '#{pane_id}')"
[[ "$keeper_pane" =~ ^%[0-9]+$ ]] || fail "keeper session has no exact pane id: $keeper_pane"
server_pid="$(iso_tmux display-message -p '#{pid}')"
[[ "$server_pid" =~ ^[0-9]+$ ]] || fail "app server pid is unreadable: $server_pid"

# Inside-tmux calls carry this run's own server explicitly.
run_inside() {
  env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE \
    TMUX="$socket_path,$server_pid,0" TMUX_PANE="$keeper_pane" "$bin" "$@"
}

# assert_lowered_by <label> <reconcile output> <uid> <session>
assert_lowered_by() {
  local label="$1" out="$2" uid="$3" session="$4"
  grep -qx -- "outcome: changed" <<<"$out" || fail "$label reconcile did not report a change: $out"
  grep -qx -- "- Registry commit" <<<"$out" || fail "$label reconcile made no Registry commit: $out"
  grep -qF -- "status.session.live true -> false (session \"$session\" is absent on the exact socket $socket_path" <<<"$out" ||
    fail "$label reconcile did not describe lowering $session: $out"
  local name live socket status
  read -r name live socket <<<"$(session_projection "$uid")"
  [[ "$name" == "$session" && "$live" == false && "$socket" == "$socket_path" ]] ||
    fail "$label lowered projection is name=$name live=$live socketPath=$socket, want $session false $socket_path"
  status="$(project_status "$(project_name "$uid")")" || fail "$label: get projects lists no STATUS for $uid"
  [[ "$status" == offline ]] || fail "$label: get projects STATUS for $uid is $status, want offline"
}

# (1) Outside tmux, with --socket-path naming the server.
iso_tmux kill-session -t "=$p1_session"
if iso_tmux has-session -t "=$p1_session" 2>/dev/null; then
  fail "raw kill-session left $p1_session live"
fi
[[ "$(session_projection "$p1_uid")" == "$p1_session true $socket_path" ]] ||
  fail "the raw kill-session changed the Registry by itself: $(session_projection "$p1_uid")"
outside_out="$(run_outside reconcile resources --socket-path "$socket_path" 2>"$root/reconcile-outside.err")" ||
  fail "outside-tmux reconcile resources failed: $(cat "$root/reconcile-outside.err") $outside_out"
assert_lowered_by "outside-tmux" "$outside_out" "$p1_uid" "$p1_session"
[[ "$(session_projection "$p2_uid")" == "$p2_session true $socket_path" ]] ||
  fail "outside-tmux reconcile touched the still-live p2: $(session_projection "$p2_uid")"
echo "PASS: outside-tmux reconcile resources --socket-path lowers the ended session to live=false, keeps name and socketPath, STATUS offline"

repeat_out="$(run_outside reconcile resources --socket-path "$socket_path" 2>"$root/reconcile-repeat.err")" ||
  fail "repeat reconcile resources failed: $(cat "$root/reconcile-repeat.err") $repeat_out"
grep -qx -- "- Registry commit (no-op)" <<<"$repeat_out" || fail "repeat reconcile was not a Registry no-op: $repeat_out"
grep -qx -- "outcome: no-op" <<<"$repeat_out" || fail "repeat reconcile outcome changed: $repeat_out"
echo "PASS: a second reconcile resources is a Registry commit (no-op)"

# (2) Inside tmux, with the inherited $TMUX naming the server and no flag.
iso_tmux kill-session -t "=$p2_session"
if iso_tmux has-session -t "=$p2_session" 2>/dev/null; then
  fail "raw kill-session left $p2_session live"
fi
inside_out="$(run_inside reconcile resources 2>"$root/reconcile-inside.err")" ||
  fail "inside-tmux reconcile resources failed: $(cat "$root/reconcile-inside.err") $inside_out"
assert_lowered_by "inside-tmux" "$inside_out" "$p2_uid" "$p2_session"
[[ "$(session_projection "$p1_uid")" == "$p1_session false $socket_path" ]] ||
  fail "inside-tmux reconcile changed the already-lowered p1: $(session_projection "$p1_uid")"
inside_repeat="$(run_inside reconcile resources 2>"$root/reconcile-inside-repeat.err")" ||
  fail "inside-tmux repeat reconcile failed: $(cat "$root/reconcile-inside-repeat.err") $inside_repeat"
grep -qx -- "- Registry commit (no-op)" <<<"$inside_repeat" || fail "inside-tmux repeat was not a Registry no-op: $inside_repeat"
iso_tmux has-session -t '=keeper' 2>/dev/null || fail "reconcile ended the keeper session"
echo "PASS: inside-tmux reconcile resources lowers the ended session and its repeat is a Registry commit (no-op)"

echo "PASS: reconcile-ended-session-live-flag real-tmux boundary"
