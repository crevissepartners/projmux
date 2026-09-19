#!/usr/bin/env bash
set -euo pipefail

# Real-tmux boundary for two app servers that share one Registry.
#
# Two app servers run under one HOME/XDG state domain and differ only by
# `TMUX_TMPDIR`: the app socket name is `projmux` on both. Every full reconcile
# pass (a create, `config apply`, the generated `after-new-window` hook) observes
# only its own server. A Project whose session was recorded live on the other
# server is absent from that pass, and that absence says nothing about it: its
# `status.session` must stay live on the server it was recorded on. A session
# that ends on its own recorded server is still lowered by a pass on that
# server, keeping its name and socketPath. The unit tests pin the rule against
# fakes; this script proves it on two real servers. Every step prints a PASS
# line so a skipped or silently empty run cannot pass.
#
# Isolation: `TMUX`, `TMUX_PANE` and `__PROJMUX_RUNTIME_ANCHOR_PANE` are dropped
# from every call; `HOME` and the XDG variables move the state domain under one
# owned root, and both servers started here inherit them, so their hook
# children write the same isolated Registry. Every `TMUX_TMPDIR` this script
# names is created before the first tmux or projmux call (a missing
# `TMUX_TMPDIR` silently falls back to the live socket directory), and every
# call names one of them explicitly. The root is a short `/tmp` path because a
# longer one overruns the 108-byte `sun_path` limit. Cleanup reads back each
# exact `#{socket_path}` and ends only a socket under the owned root; there is
# no bare `kill-server`.
unset TMUX TMUX_PANE __PROJMUX_RUNTIME_ANCHOR_PANE

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
root="$(mktemp -d /tmp/pmx-xsl.XXXXXX)"
real_tmux="$(command -v tmux)"
app_socket=projmux

# a and b are the two app servers; idle is the directory every projmux call
# that must not reach either server runs under. All exist before any tmux call.
for dir in a b idle; do
  mkdir -p "$root/$dir"
  chmod 0700 "$root/$dir"
  [[ -d "$root/$dir" ]] || {
    echo "isolated TMUX_TMPDIR was not created: $root/$dir" >&2
    exit 1
  }
done
export TMUX_TMPDIR="$root/idle"

# tmux_by_name DIR ARGS... talks to the app socket name under $root/DIR.
tmux_by_name() {
  local dir="$1"
  shift
  env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE TMUX_TMPDIR="$root/$dir" "$real_tmux" -L "$app_socket" "$@"
}

cleanup() {
  local status=$?
  local dir actual
  for dir in a b idle; do
    actual="$(tmux_by_name "$dir" display-message -p '#{socket_path}' 2>/dev/null || true)"
    [[ -n "$actual" ]] || continue
    case "$actual" in
    "$root"/*)
      env -u TMUX -u TMUX_PANE "$real_tmux" -S "$actual" kill-server >/dev/null 2>&1 || true
      ;;
    *)
      echo "refusing cleanup of a socket outside the owned root: $actual" >&2
      exit 1
      ;;
    esac
  done
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
mkdir -p "$HOME" "$HOME/pa" "$HOME/pb"
: >"$HOME/.zshrc"
cd "$HOME"

bin="$root/projmux"

# on DIR ARGS... runs projmux outside tmux against the app server under
# $root/DIR, with no tmux environment of any kind.
on() {
  local dir="$1"
  shift
  env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE TMUX_TMPDIR="$root/$dir" "$bin" "$@"
}

# session_projection prints `name live socketPath` of one Project's stored
# status.session, read through the public `get projects -o json` projection.
session_projection() {
  on idle get projects -o json | python3 -c '
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

# server_socket DIR prints the exact #{socket_path} of the app server under
# $root/DIR and refuses one outside it.
server_socket() {
  local actual
  actual="$(tmux_by_name "$1" display-message -p '#{socket_path}')" || fail "no app server under $root/$1"
  case "$actual" in
  "$root/$1"/*) printf '%s\n' "$actual" ;;
  *) fail "app server under $root/$1 reports a socket outside it: $actual" ;;
  esac
}

tmux_at() {
  local socket="$1"
  shift
  env -u TMUX -u TMUX_PANE -u __PROJMUX_RUNTIME_ANCHOR_PANE "$real_tmux" -S "$socket" "$@"
}

# violations counts every observation, across the run, of a Project whose
# session is alive on the server its projection records but is stored live=false.
violations=0
check_invariant() {
  local uid name live socket
  for uid in "$pa_uid" "$pb_uid"; do
    read -r name live socket <<<"$(session_projection "$uid")"
    [[ -n "$name" && "$socket" == /* ]] || continue
    if [[ "$live" == false ]] && tmux_at "$socket" has-session -t "=$name" 2>/dev/null; then
      violations=$((violations + 1))
      echo "violation after $1: $uid session $name is alive on $socket but stored live=false" >&2
    fi
  done
}

# wait_converge_idle waits until no hook-started converge of the built binary
# is running. A converge observes its server when it starts and commits when it
# ends, so one still in flight could commit a stale observation over a later
# step; each step below starts from a settled Registry.
wait_converge_idle() {
  local deadline=$((SECONDS + 15))
  while pgrep -f -- "$bin internal tmux converge" >/dev/null 2>&1; do
    ((SECONDS < deadline)) || fail "a hook converge of $bin was still running after 15s"
    sleep 0.1
  done
}

# expect_live UID NAME SOCKET STEP asserts the stored projection and the
# session's presence on that exact server.
expect_live() {
  local projection
  projection="$(session_projection "$1")"
  [[ "$projection" == "$2 true $3" ]] || fail "$4: projection of $1 is '$projection', want '$2 true $3'"
  tmux_at "$3" has-session -t "=$2" 2>/dev/null || fail "$4: session $2 is not on $3"
}

pa_uid="$(on idle create project --root "$HOME/pa" -o uid)"
[[ "$pa_uid" =~ ^proj-[a-z0-9]+$ ]] || fail "create project returned no uid: $pa_uid"
pb_uid="$(on idle create project --root "$HOME/pb" -o uid)"
[[ "$pb_uid" =~ ^proj-[a-z0-9]+$ ]] || fail "create project returned no uid: $pb_uid"

# Server A: bring it up through the product (installing the generated hooks),
# materialize PA on it, and keep it alive with a keeper session.
on a config apply >"$root/a-config-apply.out" 2>&1 || fail "A config apply failed: $(cat "$root/a-config-apply.out")"
on a create window -p "uid:$pa_uid" >"$root/a-create.out" 2>&1 || fail "A create window failed: $(cat "$root/a-create.out")"
sock_a="$(server_socket a)"
tmux_at "$sock_a" new-session -d -s keeper 'exec sleep 3600'
read -r pa_session _ _ <<<"$(session_projection "$pa_uid")"
[[ -n "$pa_session" ]] || fail "PA has no session projection after its create"
expect_live "$pa_uid" "$pa_session" "$sock_a" "A create"
check_invariant "A create"
echo "PASS: A's create records PA live=true on A's #{socket_path} $sock_a"

# Server B: the same for PB. Both its config apply and its create run full
# passes that do not see PA's session.
on b config apply >"$root/b-config-apply.out" 2>&1 || fail "B config apply failed: $(cat "$root/b-config-apply.out")"
on b create window -p "uid:$pb_uid" >"$root/b-create.out" 2>&1 || fail "B create window failed: $(cat "$root/b-create.out")"
sock_b="$(server_socket b)"
[[ "$sock_a" != "$sock_b" ]] || fail "the two servers share one socket: $sock_a"
tmux_at "$sock_b" new-session -d -s keeper 'exec sleep 3600'
read -r pb_session _ _ <<<"$(session_projection "$pb_uid")"
[[ -n "$pb_session" ]] || fail "PB has no session projection after its create"
expect_live "$pb_uid" "$pb_session" "$sock_b" "B create"
expect_live "$pa_uid" "$pa_session" "$sock_a" "B create"
tmux_at "$sock_b" has-session -t "=$pa_session" 2>/dev/null && fail "PA's session $pa_session is also on B"
check_invariant "B create"
echo "PASS: after B's config apply and create, PA stays live=true on A ($sock_a) and PB is live=true on B ($sock_b)"

# A's config apply is a full pass on A that does not see PB's session.
wait_converge_idle
on a config apply >"$root/a-config-apply-2.out" 2>&1 || fail "A config apply failed: $(cat "$root/a-config-apply-2.out")"
expect_live "$pb_uid" "$pb_session" "$sock_b" "A config apply"
expect_live "$pa_uid" "$pa_session" "$sock_a" "A config apply"
check_invariant "A config apply"
echo "PASS: after A's config apply, PB stays live=true on B"

# A create on A runs its full passes on A.
wait_converge_idle
on a create window -p "uid:$pa_uid" --name second >"$root/a-create-2.out" 2>&1 ||
  fail "A create window --name second failed: $(cat "$root/a-create-2.out")"
expect_live "$pb_uid" "$pb_session" "$sock_b" "A create --name second"
expect_live "$pa_uid" "$pa_session" "$sock_a" "A create --name second"
check_invariant "A create --name second"
echo "PASS: after A's create window --name second, PB stays live=true on B"

# A raw new-window on A fires A's generated after-new-window hook, which runs
# the built binary's converge. Prove the hook is that route, then watch PB
# across the window in which the hook runs.
new_window_hook="$(tmux_at "$sock_a" show-hooks -g after-new-window 2>&1)" || fail "show-hooks after-new-window failed: $new_window_hook"
grep -qF -- "'$bin' internal tmux converge" <<<"$new_window_hook" ||
  fail "A's after-new-window hook does not run the built binary's converge route: $new_window_hook"
tmux_at "$sock_a" new-window -d -t '=keeper'
deadline=$((SECONDS + 3))
while ((SECONDS < deadline)); do
  expect_live "$pb_uid" "$pb_session" "$sock_b" "A after-new-window hook"
  check_invariant "A after-new-window hook"
  sleep 0.2
done
wait_converge_idle
expect_live "$pb_uid" "$pb_session" "$sock_b" "A after-new-window hook"
check_invariant "A after-new-window hook"
echo "PASS: after a raw new-window on A fires its after-new-window hook, PB stays live=true on B"

# PA's session ends on its own recorded server; A's next full pass lowers it,
# keeping the name and socketPath, and still leaves PB alone. A kill-session
# also fires A's window-unlinked hook, whose converge lowers the same Project
# (hook-ended-session-live-flag.sh covers that path) and races the config
# apply. Unset that one hook first so only the config apply's full pass can
# lower PA here; the config apply reinstalls it.
wait_converge_idle
tmux_at "$sock_a" set-hook -gu window-unlinked
if grep -qF -- "internal tmux converge" <<<"$(tmux_at "$sock_a" show-hooks -g window-unlinked 2>&1)"; then
  fail "A's window-unlinked hook is still set"
fi
tmux_at "$sock_a" kill-session -t "=$pa_session"
if tmux_at "$sock_a" has-session -t "=$pa_session" 2>/dev/null; then
  fail "raw kill-session left $pa_session on A"
fi
tmux_at "$sock_a" has-session -t '=keeper' 2>/dev/null || fail "A's keeper session ended with PA's session"
on a config apply >"$root/a-config-apply-3.out" 2>&1 || fail "A config apply failed: $(cat "$root/a-config-apply-3.out")"
[[ "$(session_projection "$pa_uid")" == "$pa_session false $sock_a" ]] ||
  fail "A's config apply after PA's session ended left '$(session_projection "$pa_uid")', want '$pa_session false $sock_a'"
expect_live "$pb_uid" "$pb_session" "$sock_b" "A config apply after kill-session"
grep -qF -- "'$bin' internal tmux converge" <<<"$(tmux_at "$sock_a" show-hooks -g window-unlinked 2>&1)" ||
  fail "A's config apply did not reinstall the window-unlinked hook"
check_invariant "A config apply after kill-session"
echo "PASS: after a raw kill-session of PA on A, A's config apply lowers PA to live=false keeping name $pa_session and socketPath $sock_a"

[[ "$violations" -eq 0 ]] || fail "$violations observations had a session alive on its recorded server but stored live=false"
echo "PASS: no Project was ever stored live=false while its session was alive on its recorded server"

echo "PASS: cross-server-live-flag real-tmux boundary"
