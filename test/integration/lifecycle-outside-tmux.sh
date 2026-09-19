#!/usr/bin/env bash
set -euo pipefail

# Real-tmux boundary for `start project` / `stop project` run outside tmux.
#
# With no `$TMUX` (a web request, a script, an IDE terminal) the lifecycle verbs
# must read liveness from the app server their writes use -- the default app
# socket `-L projmux` -- and not from tmux's default server. The unit tests pin
# the route threading against fakes; this script proves the built binary does
# it on a real server, and that an invocation carrying `$TMUX` keeps today's
# behavior. Every case prints a PASS line so a skipped or silently empty run
# cannot pass.
#
# Isolation: `TMUX`, `TMUX_PANE` and `__PROJMUX_RUNTIME_ANCHOR_PANE` are dropped
# from every call; `HOME` and the XDG variables move the state domain under one
# owned root. The outside-tmux route is `-L projmux` resolved against
# `TMUX_TMPDIR`, so the server has to use the default app socket name inside an
# isolated `TMUX_TMPDIR`, and that directory is created before the first tmux
# or projmux call (a missing `TMUX_TMPDIR` silently falls back to the live
# socket directory). The root is a short `/tmp` path because a longer one
# overruns the 108-byte `sun_path` limit. Cleanup reads back the exact
# `#{socket_path}` and ends only a socket under the owned root; there is no
# bare `kill-server`.
unset TMUX TMUX_PANE __PROJMUX_RUNTIME_ANCHOR_PANE

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
root="$(mktemp -d /tmp/pmx-lot.XXXXXX)"
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
project_root="$HOME/p1"
mkdir -p "$HOME" "$project_root"
: >"$HOME/.zshrc"
cd "$HOME"

bin="$root/projmux"

# Every outside-tmux call runs with no tmux environment of any kind.
run_outside() {
  env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE "$bin" "$@"
}

project_uid="$(run_outside create project --root "$project_root" -o uid)"
[[ "$project_uid" =~ ^proj-[a-z0-9]+$ ]] || fail "create project returned no uid: $project_uid"
describe_json="$(run_outside describe project "uid:$project_uid" -o json)"
session="$(awk '/"session": \{/ { inside = 1 } inside && /"name":/ && !found { sub(/.*"name": "/, ""); sub(/".*/, ""); print; found = 1 }' <<<"$describe_json")"
[[ -n "$session" ]] || fail "describe project projects no status.session.name: $describe_json"

# (3) No server at all: outside-tmux stop keeps its zero-write refusal.
if iso_tmux_by_name display-message -p '#{socket_path}' >/dev/null 2>&1; then
  fail "an app server already exists before the no-server case"
fi
status=0
run_outside stop project "uid:$project_uid" >"$root/stop-noserver.out" 2>"$root/stop-noserver.err" || status=$?
[[ "$status" == 2 ]] || fail "outside-tmux stop without a server exited $status, want 2: $(cat "$root/stop-noserver.err")"
[[ ! -s "$root/stop-noserver.out" ]] || fail "a refused stop wrote stdout: $(cat "$root/stop-noserver.out")"
grep -qF "stop project: project/p1 has no live persistent session; nothing was changed" "$root/stop-noserver.err" ||
  fail "outside-tmux stop without a server changed its refusal: $(cat "$root/stop-noserver.err")"
if iso_tmux_by_name display-message -p '#{socket_path}' >/dev/null 2>&1; then
  fail "a refused stop started an app server"
fi
echo "PASS: outside-tmux stop project without an app server refuses with rc=2 and starts nothing"

# Bring the app server up through the product, then keep it alive with a keeper
# session so it survives the Project session's stop.
run_outside config apply >"$root/config-apply.out" 2>&1 || fail "config apply failed: $(cat "$root/config-apply.out")"
run_outside create window -p "uid:$project_uid" >"$root/create-window.out" 2>&1 ||
  fail "create window failed: $(cat "$root/create-window.out")"
socket_path="$(iso_tmux_by_name display-message -p '#{socket_path}')"
case "$socket_path" in
"$root"/tmux/*) ;;
*) fail "isolated app server landed outside the owned root: $socket_path" ;;
esac
iso_tmux() { env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE "$real_tmux" -S "$socket_path" "$@"; }
[[ "$(iso_tmux show-options -gqv @projmux_app)" == "1" ]] || fail "the app server carries no @projmux_app marker"
iso_tmux new-session -d -s keeper 'exec sleep 3600'
keeper_pane="$(iso_tmux display-message -p -t '=keeper:' '#{pane_id}')"
[[ "$keeper_pane" =~ ^%[0-9]+$ ]] || fail "keeper session has no exact pane id: $keeper_pane"
server_pid="$(iso_tmux display-message -p '#{pid}')"
[[ "$server_pid" =~ ^[0-9]+$ ]] || fail "app server pid is unreadable: $server_pid"
iso_tmux has-session -t "=$session" 2>/dev/null || fail "create window did not materialize session $session"

# Inside-tmux control calls carry this run's own server explicitly.
run_inside() {
  env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE \
    TMUX="$socket_path,$server_pid,0" TMUX_PANE="$keeper_pane" "$bin" "$@"
}

windows_live() { iso_tmux list-windows -t "=$session" -F '#{window_id} #{window_name} #{@projmux_window_uid}'; }
registry_windows() { run_outside get windows --project "uid:$project_uid" -o uid | sort; }

# (2) Outside tmux, a live Project session is already-live: nothing new is
# materialized on the server or in the Registry.
windows_before="$(windows_live)"
registry_before="$(registry_windows)"
[[ -n "$windows_before" ]] || fail "the live session lists no Windows"
[[ -n "$registry_before" ]] || fail "the Registry lists no Windows for the Project"
start_out="$(run_outside start project "uid:$project_uid" 2>"$root/start-outside.err")" ||
  fail "outside-tmux start project failed: $(cat "$root/start-outside.err")"
grep -qF "receipt operation=start.project" <<<"$start_out" || fail "outside-tmux start project receipt changed: $start_out"
grep -qF "runtime=already-live" <<<"$start_out" || fail "outside-tmux start project on a live session was not already-live: $start_out"
[[ "$(windows_live)" == "$windows_before" ]] || fail "outside-tmux start project changed the live Window list"
[[ "$(registry_windows)" == "$registry_before" ]] || fail "outside-tmux start project changed the Registry Windows"
echo "PASS: outside-tmux start project reports already-live for a live app-server session and changes nothing"

# (1) Outside tmux, stop ends exactly the Project session on the app server.
stop_out="$(run_outside stop project "uid:$project_uid" 2>"$root/stop-outside.err")" ||
  fail "outside-tmux stop project failed: $(cat "$root/stop-outside.err")"
grep -qF "receipt operation=stop.project" <<<"$stop_out" || fail "outside-tmux stop project receipt changed: $stop_out"
grep -qF "runtime=stopped" <<<"$stop_out" || fail "outside-tmux stop project did not report runtime=stopped: $stop_out"
if iso_tmux_by_name has-session -t "=$session" 2>/dev/null; then
  fail "outside-tmux stop project left session $session live"
fi
iso_tmux has-session -t '=keeper' 2>/dev/null || fail "outside-tmux stop project ended the keeper session"
[[ "$(registry_windows)" == "$registry_before" ]] || fail "outside-tmux stop project changed the Registry Windows"
echo "PASS: outside-tmux stop project stops the live app-server session"

# Once stopped, the outside-tmux stop is the offline refusal again.
status=0
run_outside stop project "uid:$project_uid" >"$root/stop-offline.out" 2>"$root/stop-offline.err" || status=$?
[[ "$status" == 2 ]] || fail "outside-tmux stop of a stopped Project exited $status, want 2"
grep -qF "has no live persistent session; nothing was changed" "$root/stop-offline.err" ||
  fail "outside-tmux stop of a stopped Project changed its refusal: $(cat "$root/stop-offline.err")"
echo "PASS: outside-tmux stop project refuses once the app-server session is gone"

# Control: with `$TMUX` naming this server, start and stop behave as before.
control_start="$(run_inside start project "uid:$project_uid" 2>"$root/start-inside.err")" ||
  fail "inside-tmux start project failed: $(cat "$root/start-inside.err")"
grep -qF "runtime=materialized" <<<"$control_start" || fail "inside-tmux start of a stopped Project did not materialize: $control_start"
iso_tmux has-session -t "=$session" 2>/dev/null || fail "inside-tmux start project did not materialize session $session"
control_live="$(run_inside start project "uid:$project_uid" 2>"$root/start-inside-live.err")" ||
  fail "inside-tmux start project on a live session failed: $(cat "$root/start-inside-live.err")"
grep -qF "runtime=already-live" <<<"$control_live" || fail "inside-tmux start of a live Project was not already-live: $control_live"
control_stop="$(run_inside stop project "uid:$project_uid" 2>"$root/stop-inside.err")" ||
  fail "inside-tmux stop project failed: $(cat "$root/stop-inside.err")"
grep -qF "receipt operation=stop.project" <<<"$control_stop" || fail "inside-tmux stop project receipt changed: $control_stop"
grep -qF "runtime=stopped" <<<"$control_stop" || fail "inside-tmux stop project did not report runtime=stopped: $control_stop"
if iso_tmux has-session -t "=$session" 2>/dev/null; then
  fail "inside-tmux stop project left session $session live"
fi
echo "PASS: inside-tmux start and stop project keep their results"

echo "PASS: lifecycle-outside-tmux real-tmux boundary"
