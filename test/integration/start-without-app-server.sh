#!/usr/bin/env bash
set -euo pipefail

# Real-tmux boundary for `start project` run outside tmux with no app server.
#
# The lifecycle table promises `start project` materializes an offline Project.
# With no app server running at all, Continue must start the app server from
# the generated config -- the way `create window` does -- and replay the saved
# topology on it, instead of refusing before the replay for want of an existing
# server. A replay that is refused on the server it started must still leave
# nothing behind. Every case prints a PASS line so a skipped or silently empty
# run cannot pass.
#
# Isolation: `TMUX`, `TMUX_PANE` and `__PROJMUX_RUNTIME_ANCHOR_PANE` are dropped
# from every call; `HOME` and the XDG variables move the state domain under one
# owned root. The outside-tmux route is `-L projmux` resolved against
# `TMUX_TMPDIR`, which is created before the first tmux or projmux call (a
# missing `TMUX_TMPDIR` silently falls back to the live socket directory). The
# root is a short `/tmp` path because a longer one overruns the 108-byte
# `sun_path` limit. Every server stop reads back the exact `#{socket_path}` and
# ends only a socket under the owned root; there is no bare `kill-server`.
unset TMUX TMUX_PANE __PROJMUX_RUNTIME_ANCHOR_PANE

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
root="$(mktemp -d /tmp/pmx-swa.XXXXXX)"
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

server_running() {
  iso_tmux_by_name display-message -p '#{socket_path}' >/dev/null 2>&1
}

# stop_owned_server ends the app server only when its exact socket is under
# the owned root, and waits until it is gone.
stop_owned_server() {
  local actual
  actual="$(iso_tmux_by_name display-message -p '#{socket_path}' 2>/dev/null || true)"
  [[ -n "$actual" ]] || return 0
  case "$actual" in
  "$root"/tmux/*)
    env -u TMUX -u TMUX_PANE "$real_tmux" -S "$actual" kill-server >/dev/null 2>&1 || true
    ;;
  *)
    echo "refusing to stop a socket outside the owned root: $actual" >&2
    exit 1
    ;;
  esac
  for _ in $(seq 1 100); do
    server_running || return 0
    sleep 0.05
  done
  echo "the owned app server did not stop: $actual" >&2
  exit 1
}

cleanup() {
  local status=$?
  stop_owned_server
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

# Every call runs with no tmux environment of any kind.
run_outside() {
  env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE "$bin" "$@"
}

# The generated config a fresh app server starts from. `config apply` writes
# it and starts no server; `create window` needs it just the same.
run_outside config apply >"$root/config-apply.out" 2>&1 || fail "config apply failed: $(cat "$root/config-apply.out")"
generated_config="$XDG_CONFIG_HOME/projmux/tmux.conf"
[[ -f "$generated_config" ]] || fail "config apply wrote no generated tmux config at $generated_config"

project_uid="$(run_outside create project --root "$project_root" -o uid)"
[[ "$project_uid" =~ ^proj-[a-z0-9]+$ ]] || fail "create project returned no uid: $project_uid"
describe_json="$(run_outside describe project "uid:$project_uid" -o json)"
session="$(awk '/"session": \{/ { inside = 1 } inside && /"name":/ && !found { sub(/.*"name": "/, ""); sub(/".*/, ""); print; found = 1 }' <<<"$describe_json")"
[[ -n "$session" ]] || fail "describe project projects no status.session.name: $describe_json"

# start_without_server runs `start project` with no app server and requires the
# materialized receipt, an app-owned server, and the live Project session.
start_without_server() {
  local label="$1" out
  if server_running; then
    fail "$label: an app server is running before the no-server start"
  fi
  out="$(run_outside start project "uid:$project_uid" 2>"$root/start-$label.err")" ||
    fail "$label: start project with no app server failed: $(cat "$root/start-$label.err")"
  grep -qF "receipt operation=start.project" <<<"$out" || fail "$label: start project receipt changed: $out"
  grep -qF "runtime=materialized" <<<"$out" || fail "$label: start project with no app server did not materialize: $out"
  socket_path="$(iso_tmux_by_name display-message -p '#{socket_path}')" ||
    fail "$label: start project left no app server running"
  case "$socket_path" in
  "$root"/tmux/*) ;;
  *) fail "$label: isolated app server landed outside the owned root: $socket_path" ;;
  esac
  [[ "$(iso_tmux show-options -gqv @projmux_app)" == "1" ]] || fail "$label: the started server carries no @projmux_app marker"
  iso_tmux has-session -t "=$session" 2>/dev/null || fail "$label: start project did not materialize session $session"
}
iso_tmux() { env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE "$real_tmux" -S "$socket_path" "$@"; }

# (1) A new Project, and no app server at all: start brings the server up.
start_without_server new
echo "PASS: start project with no app server starts an app-owned server and materializes the new Project"

# (2) Grow the Project to three Windows and four Panes, end the server, and
# start again with no server: every saved Window and Pane comes back, each Pane
# recording the runtime id it now has.
first_window="$(run_outside get windows --project "uid:$project_uid" -o uid | sed -n 1p)"
[[ "$first_window" =~ ^win-[a-z0-9]+$ ]] || fail "the Project has no first Window: $first_window"
run_outside create window -p "uid:$project_uid" -o uid >/dev/null 2>"$root/create-window-2.err" ||
  fail "create window failed: $(cat "$root/create-window-2.err")"
run_outside create window -p "uid:$project_uid" -o uid >/dev/null 2>"$root/create-window-3.err" ||
  fail "create window failed: $(cat "$root/create-window-3.err")"
run_outside create pane -p "uid:$project_uid" -w "uid:$first_window" -o uid >/dev/null 2>"$root/create-pane.err" ||
  fail "create pane failed: $(cat "$root/create-pane.err")"

registry_windows() { run_outside get windows --project "uid:$project_uid" -o uid | sort; }
registry_panes() { run_outside get panes --project "uid:$project_uid" -o uid | sort; }
live_windows() { iso_tmux list-windows -t "=$session" -F '#{@projmux_window_uid}' | sort; }
live_panes() { iso_tmux list-panes -s -t "=$session" -F '#{@projmux_pane_uid}' | sort; }
windows_saved="$(registry_windows)"
panes_saved="$(registry_panes)"
[[ "$(grep -c . <<<"$windows_saved")" == 3 ]] || fail "the Project does not hold three Windows: $windows_saved"
[[ "$(grep -c . <<<"$panes_saved")" == 4 ]] || fail "the Project does not hold four Panes: $panes_saved"

stop_owned_server
start_without_server restart
[[ "$(live_windows)" == "$windows_saved" ]] || fail "restart: live Windows $(live_windows) differ from the saved $windows_saved"
[[ "$(live_panes)" == "$panes_saved" ]] || fail "restart: live Panes $(live_panes) differ from the saved $panes_saved"
iso_tmux list-panes -a -F '#{pane_id} #{@projmux_pane_uid}' >"$root/live-pane-ids"
run_outside get panes --project "uid:$project_uid" -o json >"$root/registry-panes.json"
python3 - "$root/registry-panes.json" "$root/live-pane-ids" <<'PY' || fail "restart: a Pane's recorded runtime id is not its live pane id"
import json, sys

registry = json.load(open(sys.argv[1]))
live = {}
for line in open(sys.argv[2]):
    pane_id, _, uid = line.strip().partition(" ")
    if uid:
        live[uid] = pane_id
recorded = {}
for pane in registry["items"]:
    uid = pane["metadata"]["uid"]
    runtime_id = ((pane.get("status") or {}).get("activation") or {}).get("runtimeID", "")
    if runtime_id != live.get(uid):
        print(f"{uid}: recorded {runtime_id!r}, live {live.get(uid)!r}", file=sys.stderr)
        sys.exit(1)
    recorded.setdefault(runtime_id, []).append(uid)
shared = {k: v for k, v in recorded.items() if len(v) > 1}
if shared:
    print(f"runtime ids shared by more than one Pane: {shared}", file=sys.stderr)
    sys.exit(1)
PY
echo "PASS: start project with no app server brings back all three Windows and four Panes with live runtime ids"

# (3) A replay refused on the server it started leaves nothing: the generated
# config's after-new-window hook fails the second Window after it exists.
stop_owned_server
cp "$generated_config" "$root/tmux.conf.saved"
printf '%s\n' "set-hook -g after-new-window 'run-shell \"exit 1\"'" >>"$generated_config"
status=0
run_outside start project "uid:$project_uid" >"$root/start-refused.out" 2>"$root/start-refused.err" || status=$?
cp "$root/tmux.conf.saved" "$generated_config"
[[ "$status" != 0 ]] || fail "refused: start project succeeded although its second Window was refused: $(cat "$root/start-refused.out")"
grep -qF "create tmux window in session" "$root/start-refused.err" ||
  fail "refused: start project failed for another reason: $(cat "$root/start-refused.err")"
if grep -qF "rollback stopped" "$root/start-refused.err"; then
  fail "refused: the rollback stopped on the server its own kill ended: $(cat "$root/start-refused.err")"
fi
if server_running; then
  fail "refused: the server the refused start brought up is still running: $(iso_tmux_by_name list-sessions -F '#{session_name}' 2>&1)"
fi
[[ "$(registry_windows)" == "$windows_saved" ]] || fail "refused: the Registry Windows changed"
[[ "$(registry_panes)" == "$panes_saved" ]] || fail "refused: the Registry Panes changed"
echo "PASS: a start refused on the app server it started ends that server without a rollback warning"

echo "PASS: start-without-app-server real-tmux boundary"
