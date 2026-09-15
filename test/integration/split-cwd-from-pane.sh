#!/usr/bin/env bash
set -euo pipefail

# Real-tmux boundary for `--cwd-from pane`.
#
# The unit tables fix the root rule and the argv against an in-memory tmux
# server. What they cannot show is that the directory projmux reads back from a
# live pane is the directory the next pane really starts in, because both halves
# -- `#{pane_current_path}` and `split-window -c` -- are tmux behaviors. This
# script drives the built binary end to end against a real server: a Pane whose
# live cwd is a Project subdirectory, one whose live cwd is outside the Project
# root, and the unchanged default.
#
# Isolation is three-fold, and every assertion is a PASS line so a skipped or
# silently-empty run cannot pass. `TMUX`, `TMUX_PANE` and
# `__PROJMUX_RUNTIME_ANCHOR_PANE` are dropped so no inherited client identity
# leaks in; `HOME` and the XDG variables move the whole state domain under one
# owned root; and `TMUX_TMPDIR` plus a run-unique `-L` socket keep every tmux
# server under that root. The root is a short `/tmp` path because a longer one
# overruns the 108-byte `sun_path` limit of the socket. The `TMUX`/`TMUX_PANE`
# pair the binary is invoked with is constructed from this run's own server,
# never inherited. Cleanup reads back the exact `#{socket_path}` and ends only a
# socket proven to be under the owned root; there is no bare `kill-server`.
unset TMUX TMUX_PANE __PROJMUX_RUNTIME_ANCHOR_PANE

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
root="$(mktemp -d "${TMPDIR:-/tmp}/pmx-scwd.XXXXXX")"
socket="pmx-scwd-$$-$RANDOM"

cleanup() {
  local status=$?
  local actual
  actual="$(tmux -L "$socket" display-message -p '#{socket_path}' 2>/dev/null || true)"
  if [[ -n "$actual" ]]; then
    case "$actual" in
    "$root"/tmux/*)
      tmux -S "$actual" kill-server >/dev/null 2>&1 || true
      ;;
    *)
      echo "refusing cleanup of a socket outside the owned root: $actual" >&2
      exit 1
      ;;
    esac
  fi
  # Supervisors and hook routes write into the root as they exit; wait for the
  # owned tree to go quiet before removing it.
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

# Build before the state domain moves. A go command run under the isolated
# HOME would populate a module cache inside the owned root, and its read-only
# files then block cleanup.
cd "$repo"
go build -o "$root/projmux" ./cmd/projmux

export HOME="$root/home"
export XDG_CONFIG_HOME="$HOME/.config"
export XDG_STATE_HOME="$HOME/.local/state"
export XDG_DATA_HOME="$HOME/.local/share"
export XDG_CACHE_HOME="$HOME/.cache"
export TMUX_TMPDIR="$root/tmux"
project_root="$root/proj"
sub="$project_root/services/api"
outside="$root/outside"
mkdir -p "$HOME" "$TMUX_TMPDIR" "$sub" "$outside"

bin="$root/projmux"

iso_tmux() { tmux -L "$socket" "$@"; }

# One isolated app-owned server. The markers are what let the binary bind this
# exact route instead of looking for the default app socket.
iso_tmux new-session -d -s host -c "$project_root" sleep 600
iso_tmux set-option -g @projmux_app 1 >/dev/null
iso_tmux set-option -g @projmux_socket_name "$socket" >/dev/null
socket_path="$(iso_tmux display-message -p '#{socket_path}')"
server_pid="$(iso_tmux display-message -p '#{pid}')"
host_pane="$(iso_tmux display-message -p -t host '#{pane_id}')"
case "$socket_path" in
"$root"/tmux/*) ;;
*)
  echo "isolated server landed outside the owned root: $socket_path" >&2
  exit 1
  ;;
esac

# Every projmux invocation states this run's own server. The value is
# constructed here, never inherited.
run_projmux() {
  env TMUX="$socket_path,$server_pid,0" TMUX_PANE="$host_pane" "$bin" "$@"
}

fail() {
  echo "$*" >&2
  exit 1
}

# wait_for_pane_path waits for a pane's live directory, which a supervised
# child reaches a moment after the split returns.
wait_for_pane_path() {
  local pane="$1" want="$2" got=""
  for _ in $(seq 1 200); do
    got="$(iso_tmux display-message -p -t "$pane" '#{pane_current_path}' 2>/dev/null || true)"
    if [[ "$got" == "$want" ]]; then
      return 0
    fi
    sleep 0.05
  done
  fail "pane $pane live directory = $got, want $want"
}

assert_pane_path() {
  local pane="$1" want="$2" label="$3" got
  got="$(iso_tmux display-message -p -t "$pane" '#{pane_current_path}')"
  [[ "$got" == "$want" ]] || fail "$label: new Pane started in $got, want $want"
}

assert_one_pane_id() {
  local out="$1" label="$2"
  [[ "$(printf '%s' "$out" | grep -c .)" == "1" ]] || fail "$label: stdout was not one line: $out"
  [[ "$out" =~ ^%[0-9]+$ ]] || fail "$label: stdout was not a pane id: $out"
}

project_uid="$(run_projmux create project --root "$project_root" -o uid)"
[[ -n "$project_uid" ]] || fail "create project returned no uid"

# An anchor whose live directory is a Project subdirectory, and one whose live
# directory is outside the Project root entirely.
subdir_anchor="$(run_projmux create pane --project "uid:$project_uid" --primary-window --name subdir-anchor -o pane-id \
  -- sh -c "cd '$sub' && exec sleep 600")"
wait_for_pane_path "$subdir_anchor" "$sub"
outside_anchor="$(run_projmux create pane --project "uid:$project_uid" --primary-window --name outside-anchor -o pane-id \
  -- sh -c "cd '$outside' && exec sleep 600")"
wait_for_pane_path "$outside_anchor" "$outside"

# 1. The default is unchanged: the Project root, with nothing on stderr.
default_err="$root/default.err"
default_pane="$(run_projmux create pane --project "uid:$project_uid" --pane subdir-anchor -o pane-id 2>"$default_err")"
assert_one_pane_id "$default_pane" "default"
assert_pane_path "$default_pane" "$project_root" "default"
[[ -s "$default_err" ]] && fail "the default split wrote to stderr: $(cat "$default_err")"
echo "PASS: default split starts in the Project root"

# 2. `--cwd-from pane` inside the root keeps the active Pane's directory.
inside_err="$root/inside.err"
inside_pane="$(run_projmux create pane --project "uid:$project_uid" --pane subdir-anchor --cwd-from pane -o pane-id 2>"$inside_err")"
assert_one_pane_id "$inside_pane" "--cwd-from pane inside the root"
assert_pane_path "$inside_pane" "$sub" "--cwd-from pane inside the root"
[[ -s "$inside_err" ]] && fail "a usable Pane directory wrote to stderr: $(cat "$inside_err")"
echo "PASS: --cwd-from pane starts in the active Pane directory"

# 3. Outside the root it falls back to the root and says so once.
outside_err="$root/outside.err"
outside_pane="$(run_projmux create pane --project "uid:$project_uid" --pane outside-anchor --cwd-from pane -o pane-id 2>"$outside_err")"
assert_one_pane_id "$outside_pane" "--cwd-from pane outside the root"
assert_pane_path "$outside_pane" "$project_root" "--cwd-from pane outside the root"
[[ "$(grep -c . "$outside_err")" == "1" ]] || fail "stderr was not one line: $(cat "$outside_err")"
grep -qF "is outside it" "$outside_err" || fail "stderr did not name the reason: $(cat "$outside_err")"
grep -qF "$project_root" "$outside_err" || fail "stderr did not name the Project root: $(cat "$outside_err")"
echo "PASS: an outside Pane directory falls back to the Project root with one notice"

# 4. The project config selects the same source with no flag at all.
mkdir -p "$project_root/.projmux"
printf '[ai]\nsplit_cwd_from = "pane"\n' >"$project_root/.projmux/config.toml"
config_err="$root/config.err"
config_pane="$(run_projmux create pane --project "uid:$project_uid" --pane subdir-anchor -o pane-id 2>"$config_err")"
assert_one_pane_id "$config_pane" "configured pane source"
assert_pane_path "$config_pane" "$sub" "configured pane source"
[[ -s "$config_err" ]] && fail "the configured pane source wrote to stderr: $(cat "$config_err")"
echo "PASS: [ai] split_cwd_from = \"pane\" starts in the active Pane directory"

echo "PASS: split-cwd-from-pane real-tmux boundary"
