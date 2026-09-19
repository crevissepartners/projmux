#!/usr/bin/env bash
set -euo pipefail

# Real-tmux boundary for the hook half of a Project session that ends outside
# projmux.
#
# A raw `tmux kill-session` fires the generated `window-unlinked` hook on the
# server that lost the session, and nothing else. That hook's fast path now
# lowers `status.session.live` of a Project whose projection recorded this
# exact server and whose session is gone from it, with no `reconcile` or
# `config apply` in between. A raw `kill-window` of one of its Windows fires
# the same hook and must leave the still-present session live. The unit tests
# pin the stage against fakes; this script proves the built binary's installed
# hook does it on a real server. Every case prints a PASS line so a skipped or
# silently empty run cannot pass.
#
# Isolation: `TMUX`, `TMUX_PANE` and `__PROJMUX_RUNTIME_ANCHOR_PANE` are dropped
# from every call; `HOME` and the XDG variables move the state domain under one
# owned root, and the app server started here inherits them, so its hook
# children write the same isolated Registry. The server uses the default app
# socket name inside an isolated `TMUX_TMPDIR`, and that directory is created
# before the first tmux or projmux call (a missing `TMUX_TMPDIR` silently falls
# back to the live socket directory). The root is a short `/tmp` path because a
# longer one overruns the 108-byte `sun_path` limit. Cleanup reads back the
# exact `#{socket_path}` and ends only a socket under the owned root; there is
# no bare `kill-server`.
unset TMUX TMUX_PANE __PROJMUX_RUNTIME_ANCHOR_PANE

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
root="$(mktemp -d /tmp/pmx-hel.XXXXXX)"
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
mkdir -p "$HOME" "$HOME/p1"
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

p_uid="$(run_outside create project --root "$HOME/p1" -o uid)"
[[ "$p_uid" =~ ^proj-[a-z0-9]+$ ]] || fail "create project returned no uid: $p_uid"

# Bring the app server up through the product, which installs the generated
# hooks, and materialize the Project session on it. Materializing a closed
# Project opens its primary Window beside the created one, so one create
# already leaves the session with more than one Window.
run_outside config apply >"$root/config-apply.out" 2>&1 || fail "config apply failed: $(cat "$root/config-apply.out")"
run_outside create window -p "uid:$p_uid" >"$root/create-window.out" 2>&1 ||
  fail "create window failed: $(cat "$root/create-window.out")"
socket_path="$(iso_tmux_by_name display-message -p '#{socket_path}')"
case "$socket_path" in
"$root"/tmux/*) ;;
*) fail "isolated app server landed outside the owned root: $socket_path" ;;
esac
iso_tmux() { env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE "$real_tmux" -S "$socket_path" "$@"; }
[[ "$(iso_tmux show-options -gqv @projmux_app)" == "1" ]] || fail "the app server carries no @projmux_app marker"

# The hook is the only producer this script relies on, so prove it is the
# built binary's converge route before any raw kill.
unlink_hook="$(iso_tmux show-hooks -g window-unlinked 2>&1)" || fail "show-hooks window-unlinked failed: $unlink_hook"
# The generated body single-quotes the binary path.
grep -qF -- "'$bin' internal tmux converge" <<<"$unlink_hook" ||
  fail "window-unlinked hook does not run the built binary's converge route: $unlink_hook"
grep -qF -- "--reason window-unlinked" <<<"$unlink_hook" ||
  fail "window-unlinked hook does not state its reason: $unlink_hook"
echo "PASS: the isolated server's window-unlinked hook runs the built binary's internal tmux converge"

read -r p_session p_live p_socket <<<"$(session_projection "$p_uid")"
[[ -n "$p_session" && "$p_live" == true && "$p_socket" == "$socket_path" ]] ||
  fail "projection after create is name=$p_session live=$p_live socketPath=$p_socket, want live=true on $socket_path"
windows="$(iso_tmux list-windows -t "=$p_session" -F '#{window_id}')" || fail "list-windows of $p_session failed"
window_count="$(grep -c . <<<"$windows")"
[[ "$window_count" -ge 2 ]] || fail "session $p_session has $window_count Windows, want at least 2: $windows"
first_window="$(grep -m1 . <<<"$windows")"
[[ "$first_window" =~ ^@[0-9]+$ ]] || fail "first Window has no exact id: $first_window"
echo "PASS: the created Project session records live=true on the server's #{socket_path} and holds $window_count Windows"

# A keeper session keeps the server alive once the Project session ends.
iso_tmux new-session -d -s keeper 'exec sleep 3600'

# (1) kill-window of one of its Windows fires window-unlinked; the session is
# still present on the hook's server, so the projection stays live.
iso_tmux kill-window -t "$first_window"
iso_tmux has-session -t "=$p_session" 2>/dev/null || fail "kill-window of one Window ended session $p_session"
sleep 3
[[ "$(session_projection "$p_uid")" == "$p_session true $socket_path" ]] ||
  fail "the window-unlinked hook of a kill-window changed the projection: $(session_projection "$p_uid")"
echo "PASS: after a raw kill-window of one of the session's Windows the session projection stays live=true"

# (2) kill-session fires window-unlinked on the same server. No reconcile or
# config apply runs from here on: only the hook can lower the flag.
iso_tmux kill-session -t "=$p_session"
if iso_tmux has-session -t "=$p_session" 2>/dev/null; then
  fail "raw kill-session left $p_session live"
fi
# Bound the poll by wall clock: each probe runs `get projects`, so an
# iteration count would not bound the wait under load.
deadline=$((SECONDS + 10))
projection=""
while ((SECONDS < deadline)); do
  projection="$(session_projection "$p_uid")"
  [[ "$projection" == "$p_session false $socket_path" ]] && break
  sleep 0.1
done
[[ "$projection" == "$p_session false $socket_path" ]] ||
  fail "the window-unlinked hook did not lower the ended session within 10s: $projection"
echo "PASS: after a raw kill-session the hook alone lowers live to false, keeping the session name and socketPath"

status="$(project_status "$(project_name "$p_uid")")" || fail "get projects lists no STATUS for $p_uid"
[[ "$status" == offline ]] || fail "get projects STATUS for $p_uid is $status, want offline"
iso_tmux has-session -t '=keeper' 2>/dev/null || fail "the hook ended the keeper session"
echo "PASS: get projects shows the lowered Project offline and the keeper session survives"

echo "PASS: hook-ended-session-live-flag real-tmux boundary"
