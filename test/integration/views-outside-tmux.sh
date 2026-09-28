#!/usr/bin/env bash
set -euo pipefail

# Real-tmux boundary for the Registry views (`get`, `describe`) run outside tmux.
#
# With no `$TMUX` and no socket flag (a script, an IDE terminal, another
# terminal) a view must observe the app server -- the default app socket
# `-L projmux` that `create` and `start project` run resources on -- and render
# the same rows it renders inside that server's tmux. With no server behind the
# app socket the view reads offline, and with `$TMUX` naming another server the
# view keeps observing that server. The unit tests pin the transport choice
# against fakes; this script proves the built binary does it on a real server.
# Every case prints a PASS line so a skipped or silently empty run cannot pass.
#
# Isolation: `TMUX`, `TMUX_PANE` and `__PROJMUX_RUNTIME_ANCHOR_PANE` are dropped
# from every call; `HOME` and the XDG variables move the state domain under one
# owned root. The outside-tmux route is `-L projmux` resolved against
# `TMUX_TMPDIR`, so that directory is created before the first tmux or projmux
# call (a missing `TMUX_TMPDIR` silently falls back to the live socket
# directory). The root is a short `/tmp` path because a longer one overruns the
# 108-byte `sun_path` limit. Cleanup reads back each exact `#{socket_path}` and
# ends only a socket under the owned root; there is no bare `kill-server`.
unset TMUX TMUX_PANE __PROJMUX_RUNTIME_ANCHOR_PANE

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
root="$(mktemp -d /tmp/pmx-vot.XXXXXX)"
real_tmux="$(command -v tmux)"
app_socket=projmux
other_socket=pmx-other

# TMUX_TMPDIR must exist before anything can talk to tmux.
export TMUX_TMPDIR="$root/tmux"
mkdir -p "$TMUX_TMPDIR"
chmod 0700 "$TMUX_TMPDIR"
[[ -d "$TMUX_TMPDIR" ]] || {
  echo "isolated TMUX_TMPDIR was not created: $TMUX_TMPDIR" >&2
  exit 1
}

iso_tmux_by_name() {
  local socket="$1"
  shift
  env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE "$real_tmux" -L "$socket" "$@"
}

# end_server ends the server behind one socket name, only when tmux reports its
# socket inside the owned root.
end_server() {
  local actual
  actual="$(iso_tmux_by_name "$1" display-message -p '#{socket_path}' 2>/dev/null || true)"
  [[ -n "$actual" ]] || return 0
  case "$actual" in
  "$root"/tmux/*)
    env -u TMUX -u TMUX_PANE "$real_tmux" -S "$actual" kill-server >/dev/null 2>&1 || true
    ;;
  *)
    echo "refusing cleanup of a socket outside the owned root: $actual" >&2
    return 1
    ;;
  esac
}

cleanup() {
  local status=$?
  end_server "$app_socket" || status=1
  end_server "$other_socket" || status=1
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

# The AGE column ticks between two calls; every other cell must match.
without_age() {
  awk 'NR == 1 { for (i = 1; i <= NF; i++) if ($i == "AGE") age = i }
    { line = ""; for (i = 1; i <= NF; i++) if (i != age) line = line (line == "" ? "" : " ") $i; print line }'
}

# row prints the row whose NAME cell is $2 out of the table in $1.
row() {
  awk -v name="$2" 'NR == 1 { for (i = 1; i <= NF; i++) if ($i == "NAME") col = i; next }
    col && $col == name { print; exit }' <<<"$1"
}

project_uid="$(run_outside create project --root "$project_root" -o uid)"
[[ "$project_uid" =~ ^proj-[a-z0-9]+$ ]] || fail "create project returned no uid: $project_uid"

# Bring the app server up through the product.
run_outside config apply >"$root/config-apply.out" 2>&1 || fail "config apply failed: $(cat "$root/config-apply.out")"
window_uid="$(run_outside create window -p "uid:$project_uid" -o uid 2>"$root/create-window.err")" ||
  fail "create window failed: $(cat "$root/create-window.err")"
[[ "$window_uid" =~ ^win-[a-z0-9]+$ ]] || fail "create window returned no uid: $window_uid"
socket_path="$(iso_tmux_by_name "$app_socket" display-message -p '#{socket_path}')"
case "$socket_path" in
"$root"/tmux/*) ;;
*) fail "isolated app server landed outside the owned root: $socket_path" ;;
esac
iso_tmux() { env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE "$real_tmux" -S "$socket_path" "$@"; }
[[ "$(iso_tmux show-options -gqv @projmux_app)" == "1" ]] || fail "the app server carries no @projmux_app marker"
server_pid="$(iso_tmux display-message -p '#{pid}')"
[[ "$server_pid" =~ ^[0-9]+$ ]] || fail "app server pid is unreadable: $server_pid"
live_pane="$(iso_tmux list-panes -a -F '#{pane_id}' | sed -n 1p)"
[[ "$live_pane" =~ ^%[0-9]+$ ]] || fail "the app server lists no pane: $live_pane"
window_name="$(run_outside get windows -p "uid:$project_uid" -o name | sed -n 1p)"
[[ -n "$window_name" ]] || fail "the Registry lists no Window for the Project"

# Inside-tmux calls carry this run's own app server explicitly.
run_inside() {
  env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE \
    TMUX="$socket_path,$server_pid,0" TMUX_PANE="$live_pane" "$bin" "$@"
}

# (1) Outside tmux, `get windows -o wide` renders the live row it renders inside.
outside_windows="$(run_outside get windows -p "uid:$project_uid" -o wide)"
inside_windows="$(run_inside get windows -p "uid:$project_uid" -o wide)"
[[ "$(without_age <<<"$outside_windows")" == "$(without_age <<<"$inside_windows")" ]] ||
  fail "outside-tmux get windows differs from inside:
--- outside
$outside_windows
--- inside
$inside_windows"
window_row="$(row "$outside_windows" "$window_name")"
for want in " live " "open,delete" "live-window-name" "true"; do
  grep -qF -- "$want" <<<" $window_row " || fail "outside-tmux live Window row lacks '$want': $window_row"
done
echo "PASS: outside-tmux get windows -o wide renders the live app-server row it renders inside tmux"

# (2) describe window and get panes agree too.
outside_describe="$(run_outside describe window "uid:$window_uid")"
inside_describe="$(run_inside describe window "uid:$window_uid")"
[[ "$outside_describe" == "$inside_describe" ]] || fail "outside-tmux describe window differs from inside:
--- outside
$outside_describe
--- inside
$inside_describe"
grep -qw live <<<"$outside_describe" || fail "outside-tmux describe window reports no live status: $outside_describe"
outside_panes="$(run_outside get panes -p "uid:$project_uid" -o wide)"
inside_panes="$(run_inside get panes -p "uid:$project_uid" -o wide)"
[[ "$(without_age <<<"$outside_panes")" == "$(without_age <<<"$inside_panes")" ]] ||
  fail "outside-tmux get panes differs from inside:
--- outside
$outside_panes
--- inside
$inside_panes"
pane_rows="$(awk 'NR > 1' <<<"$outside_panes")"
grep -qw live <<<"$pane_rows" || fail "outside-tmux get panes lists no live Pane: $outside_panes"
echo "PASS: outside-tmux describe window and get panes match inside tmux and report live"

# (4) Control: with `$TMUX` naming another server, the view keeps observing that
# server, where none of the Project's runtime lives.
iso_tmux_by_name "$other_socket" new-session -d -s other 'exec sleep 3600'
other_path="$(iso_tmux_by_name "$other_socket" display-message -p '#{socket_path}')"
case "$other_path" in
"$root"/tmux/*) ;;
*) fail "the other server landed outside the owned root: $other_path" ;;
esac
other_pid="$(iso_tmux_by_name "$other_socket" display-message -p '#{pid}')"
other_windows="$(env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE \
  TMUX="$other_path,$other_pid,0" "$bin" get windows -p "uid:$project_uid" -o wide)"
other_row="$(row "$other_windows" "$window_name")"
grep -qF " offline " <<<" $other_row " || fail "a view inside another tmux did not keep that server: $other_row"
end_server "$other_socket"
echo "PASS: a view inside another tmux keeps observing the inherited server"

# (5) A server that is running but cannot be read is not an empty machine: with
# the app socket refusing connections the view cannot observe anything, so the
# Window and the Project read unknown and offer neither start nor open. The
# socket mode is restored before anything else talks to the server.
socket_mode="$(stat -c '%a' "$socket_path")"
chmod 000 "$socket_path"
unread_windows="$(run_outside get windows -p "uid:$project_uid" -o wide)"
unread_projects="$(run_outside get projects -o wide)"
unread_describe="$(run_outside describe window "uid:$window_uid")"
chmod "$socket_mode" "$socket_path"
unread_row="$(row "$unread_windows" "$window_name")"
unread_project_row="$(row "$unread_projects" "$(basename "$project_root")")"
for checked in "$unread_row" "$unread_project_row"; do
  [[ " $(tr -s ' ' <<<"$checked") " == *" unknown delete "* ]] || fail "unreadable app server row is not unknown with delete only: $checked"
done
[[ "$unread_describe" == *unknown* ]] || fail "describe window with an unreadable app server is not unknown: $unread_describe"
[[ "$(iso_tmux display-message -p '#{pid}')" == "$server_pid" ]] || fail "the app server did not survive the unreadable window"
echo "PASS: outside-tmux view of an unreadable app server reads unknown and offers only delete"

# (3) No server behind the app socket: outside tmux the view reads offline.
end_server "$app_socket"
if iso_tmux_by_name "$app_socket" display-message -p '#{socket_path}' >/dev/null 2>&1; then
  fail "the app server survived its cleanup"
fi
absent_windows="$(run_outside get windows -p "uid:$project_uid" -o wide)"
absent_row="$(row "$absent_windows" "$window_name")"
grep -qF " offline start,delete " <<<" $(tr -s ' ' <<<"$absent_row") " || fail "outside-tmux view without an app server is not offline with start: $absent_row"
if iso_tmux_by_name "$app_socket" display-message -p '#{socket_path}' >/dev/null 2>&1; then
  fail "an outside-tmux view started an app server"
fi
echo "PASS: outside-tmux view without an app server reads offline and starts nothing"

echo "PASS: views-outside-tmux real-tmux boundary"
