#!/usr/bin/env bash
set -euo pipefail

# Real-tmux boundary for the transaction-scoped route identity cache.
#
# The unit tables prove reuse, invalidation and drift parity against an
# in-memory tmux server. What they cannot show is that the built binary, on a
# real server, still returns the same create result shape, still refuses a
# server without the app ownership marker with today's wording, and still reads
# the server identity from tmux before the first write of a transaction. Every
# case prints a PASS line so a skipped or silently empty run cannot pass.
#
# Isolation follows split-cwd-from-pane.sh. `TMUX`, `TMUX_PANE` and
# `__PROJMUX_RUNTIME_ANCHOR_PANE` are dropped; `HOME` and the XDG variables move
# the state domain under one owned root; `TMUX_TMPDIR` plus a run-unique `-L`
# socket keep the server under that root, which is a short `/tmp` path because
# a longer one overruns the 108-byte `sun_path` limit. The `TMUX`/`TMUX_PANE`
# pair the binary sees is built from this run's own server. Cleanup reads back
# the exact `#{socket_path}` and ends only a socket under the owned root; there
# is no bare `kill-server`.
unset TMUX TMUX_PANE __PROJMUX_RUNTIME_ANCHOR_PANE

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
root="$(mktemp -d /tmp/pmx-rgic.XXXXXX)"
socket="pmx-rgic-$$-$RANDOM"
real_tmux="$(command -v tmux)"

cleanup() {
  local status=$?
  local actual
  actual="$("$real_tmux" -L "$socket" display-message -p '#{socket_path}' 2>/dev/null || true)"
  if [[ -n "$actual" ]]; then
    case "$actual" in
    "$root"/tmux/*)
      "$real_tmux" -S "$actual" kill-server >/dev/null 2>&1 || true
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

# Build before the state domain moves, so the module cache stays outside it.
cd "$repo"
go build -o "$root/projmux" ./cmd/projmux

export HOME="$root/home"
export XDG_CONFIG_HOME="$HOME/.config"
export XDG_STATE_HOME="$HOME/.local/state"
export XDG_DATA_HOME="$HOME/.local/share"
export XDG_CACHE_HOME="$HOME/.cache"
export TMUX_TMPDIR="$root/tmux"
export SHELL=/bin/sh
project_root="$root/proj"
mkdir -p "$HOME" "$TMUX_TMPDIR" "$project_root" "$root/wrap"

bin="$root/projmux"
iso_tmux() { "$real_tmux" -L "$socket" "$@"; }

fail() {
  echo "$*" >&2
  exit 1
}

iso_tmux new-session -d -s host -c "$project_root" sleep 600
iso_tmux set-option -g @projmux_app 1 >/dev/null
iso_tmux set-option -g @projmux_socket_name "$socket" >/dev/null
socket_path="$(iso_tmux display-message -p '#{socket_path}')"
server_pid="$(iso_tmux display-message -p '#{pid}')"
host_pane="$(iso_tmux display-message -p -t host '#{pane_id}')"
case "$socket_path" in
"$root"/tmux/*) ;;
*) fail "isolated server landed outside the owned root: $socket_path" ;;
esac

run_projmux() {
  env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE \
    TMUX="$socket_path,$server_pid,0" TMUX_PANE="$host_pane" "$bin" "$@"
}

project_uid="$(run_projmux create project --root "$project_root" -o uid)"
[[ -n "$project_uid" ]] || fail "create project returned no uid"

# (a) create pane and create window keep today's result shape.
pane_id="$(run_projmux create pane --project "uid:$project_uid" --primary-window -o pane-id)"
[[ "$pane_id" =~ ^%[0-9]+$ ]] || fail "create pane -o pane-id stdout was not one pane id: $pane_id"
[[ -n "$(iso_tmux display-message -p -t "$pane_id" '#{@projmux_pane_uid}')" ]] || fail "created Pane $pane_id has no uid mirror"
pane_out="$(run_projmux create pane --project "uid:$project_uid" --primary-window)"
[[ "$(printf '%s\n' "$pane_out" | sed -n 1p)" =~ ^pane/pane-[a-z0-9]+\ created$ ]] || fail "create pane human line changed: $pane_out"
[[ "$(printf '%s\n' "$pane_out" | sed -n 2p)" == "receipt operation=create.pane identity=created address=allocated topology=established desired-state=created runtime=materialized focus=unchanged projects=0 windows=0 panes=1 agents=0" ]] ||
  fail "create pane receipt changed: $pane_out"
[[ "$(printf '%s\n' "$pane_out" | grep -c .)" == "2" ]] || fail "create pane printed more than two lines: $pane_out"
echo "PASS: create pane succeeds with the unchanged result shape"

window_out="$(run_projmux create window --project "uid:$project_uid" --name identity-w2)"
[[ "$(printf '%s\n' "$window_out" | sed -n 1p)" == "window/identity-w2 created" ]] || fail "create window human line changed: $window_out"
[[ "$(printf '%s\n' "$window_out" | sed -n 2p)" == "receipt operation=create.window identity=created address=allocated topology=established desired-state=created runtime=materialized focus=unchanged projects=0 windows=1 panes=0 agents=0" ]] ||
  fail "create window receipt changed: $window_out"
[[ "$(printf '%s\n' "$window_out" | grep -c .)" == "2" ]] || fail "create window printed more than two lines: $window_out"
iso_tmux list-windows -a -F '#{window_name}' | grep -qx identity-w2 || fail "created Window identity-w2 is not live"
window_uid="$(run_projmux create window --project "uid:$project_uid" --name identity-w3 -o uid)"
[[ "$window_uid" =~ ^win-[a-z0-9]+$ ]] || fail "create window -o uid stdout was not one Window uid: $window_uid"
echo "PASS: create window succeeds with the unchanged result shape"

# (c) The first identity proof of a transaction really executes: a PATH tmux
# wrapper logs every argv, and the socket path, server generation and app
# marker are read before the first write.
argv_log="$root/tmux-argv.log"
cat >"$root/wrap/tmux" <<WRAP
#!/bin/sh
printf '%s\n' "\$*" >>"$argv_log"
exec "$real_tmux" "\$@"
WRAP
chmod +x "$root/wrap/tmux"
wrapped_pane="$(env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE PATH="$root/wrap:$PATH" \
  TMUX="$socket_path,$server_pid,0" TMUX_PANE="$host_pane" "$bin" create pane --project "uid:$project_uid" --primary-window -o pane-id)"
[[ "$wrapped_pane" =~ ^%[0-9]+$ ]] || fail "wrapped create pane stdout was not one pane id: $wrapped_pane"
first_write="$(grep -nE ' (set-environment|set-option|split-window|new-window|new-session|resize-pane|rename-window|kill-session|kill-window|kill-pane|select-pane) ' "$argv_log" | head -1 | cut -d: -f1)"
[[ -n "$first_write" ]] || fail "wrapped create pane logged no tmux write"
before_write="$(head -n "$((first_write - 1))" "$argv_log")"
grep -qF 'display-message -p -F #{socket_path}' <<<"$before_write" || fail "no socket_path identity read before the first write"
grep -qF 'display-message -p -F #{pid}' <<<"$before_write" || fail "no server generation read before the first write"
grep -qF 'show-options -gqv @projmux_app' <<<"$before_write" || fail "no app ownership read before the first write"
echo "PASS: first identity proof reads socket_path from tmux before the first write"

# (b) Without the app ownership marker, create refuses with today's wording and
# changes nothing.
iso_tmux set-option -gu @projmux_app
want_refusal="runtime mutation route: exact invocation server is not app-owned; partial or foreign ownership markers refuse standalone classification"
panes_before="$(iso_tmux list-panes -a -F '#{pane_id}' | sort)"
windows_before="$(iso_tmux list-windows -a -F '#{window_id}' | sort)"
for kind in pane window; do
  refusal_out="$root/refusal-$kind.out"
  refusal_err="$root/refusal-$kind.err"
  args=(create "$kind" --project "uid:$project_uid")
  if [[ "$kind" == pane ]]; then
    args+=(--primary-window -o pane-id)
  else
    args+=(--name identity-refused -o uid)
  fi
  status=0
  run_projmux "${args[@]}" >"$refusal_out" 2>"$refusal_err" || status=$?
  [[ "$status" != 0 ]] || fail "create $kind succeeded on a server without the app ownership marker"
  [[ ! -s "$refusal_out" ]] || fail "refused create $kind wrote stdout: $(cat "$refusal_out")"
  [[ "$(cat "$refusal_err")" == "$want_refusal" ]] || fail "refused create $kind stderr changed: $(cat "$refusal_err")"
done
[[ "$(iso_tmux list-panes -a -F '#{pane_id}' | sort)" == "$panes_before" ]] || fail "refused create changed the Pane set"
[[ "$(iso_tmux list-windows -a -F '#{window_id}' | sort)" == "$windows_before" ]] || fail "refused create changed the Window set"
echo "PASS: create refuses a server without the app ownership marker with the existing wording"

echo "PASS: route-guard-identity-cache real-tmux boundary"
