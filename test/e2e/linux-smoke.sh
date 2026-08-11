#!/usr/bin/env bash
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/test/lib/smoke.sh"

smoke_setup_env
trap smoke_cleanup_env EXIT
cd "$smoke_root"

smoke_build_binary
bin="$PROJMUX_SMOKE_BIN"

# Exercise the opt-in direct launch contract against a real isolated tmux
# server, then prove that the returned handle names the pane that is live.
PROJMUX_SMOKE_TMUX_SOCKET="projmux-pane-id-e2e"
export PROJMUX_SMOKE_TMUX_SOCKET
pane_id_socket="$PROJMUX_SMOKE_TMUX_SOCKET"
pane_id_session="pane-id-e2e"
tmux -L "$pane_id_socket" new-session -d -s "$pane_id_session" -c "$smoke_root" sleep 300
pane_id_target="$(tmux -L "$pane_id_socket" display-message -p -t "$pane_id_session:0.0" '#{pane_id}')"
pane_id_socket_path="$(tmux -L "$pane_id_socket" display-message -p -t "$pane_id_target" '#{socket_path}')"
pane_id_server_pid="$(tmux -L "$pane_id_socket" display-message -p -t "$pane_id_target" '#{pid}')"
pane_id_output="$(
  TMUX="$pane_id_socket_path,$pane_id_server_pid,0" \
    TMUX_PANE="$pane_id_target" \
    TMUX_SPLIT_TARGET_PANE="$pane_id_target" \
    TMUX_SPLIT_CONTEXT_DIR="$smoke_root" \
    SHELL="/bin/sh" \
    "$bin" ai split --agent shell --print-pane-id right
)"
if [[ ! "$pane_id_output" =~ ^%[0-9]+$ ]]; then
  echo "expected exactly one %N pane id line, got: $pane_id_output" >&2
  exit 1
fi
pane_id_live="$(tmux -L "$pane_id_socket" display-message -p -t "$pane_id_output" '#{pane_id}')"
if [[ "$pane_id_live" != "$pane_id_output" ]]; then
  echo "returned pane id is not live: returned=$pane_id_output resolved=$pane_id_live" >&2
  exit 1
fi
tmux -L "$pane_id_socket" kill-server

project_a="$PROJMUX_SMOKE_WORKDIR/projects/alpha"
project_b="$PROJMUX_SMOKE_WORKDIR/projects/beta"
mkdir -p "$project_a" "$project_b"

tmux new-session -d -s e2e-alpha -c "$project_a" sleep 300
tmux new-session -d -s e2e-beta -c "$project_b" sleep 300
tmux split-window -d -t e2e-alpha:0 -c "$project_a" sleep 300

"$bin" tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket projmux >"$PROJMUX_SMOKE_WORKDIR/apply-empty.out"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/apply-empty.out" "skipped reload: no live tmux server -L projmux"

tmux source-file "$XDG_CONFIG_HOME/projmux/tmux.conf"
app_flag="$(tmux show-options -gqv @projmux_app)"
if [[ "$app_flag" != "1" ]]; then
  echo "expected sourced app config to set @projmux_app=1, got: $app_flag" >&2
  exit 1
fi

pane_id="$(tmux list-panes -t e2e-alpha:0 -F '#{pane_id}' | head -n 1)"
tmux set-option -p -t "$pane_id" @projmux_ai_agent codex
tmux set-option -p -t "$pane_id" @projmux_ai_topic "docker e2e"
tmux set-option -p -t "$pane_id" @projmux_attention_state reply

# A user rename owns only the persistent pane label. It must not mutate the
# independent AI topic or raw runtime title, and empty confirmation clears it.
raw_title_before="$(tmux display-message -p -t "$pane_id" '#{pane_title}')"
"$bin" tmux rename-pane "$pane_id" "docker user label"
if [[ "$(tmux show-options -pqv -t "$pane_id" @projmux_pane_label)" != "docker user label" ]]; then
  echo "expected real tmux pane label write" >&2
  exit 1
fi
if [[ "$(tmux show-options -pqv -t "$pane_id" @projmux_ai_topic)" != "docker e2e" ]] ||
  [[ "$(tmux display-message -p -t "$pane_id" '#{pane_title}')" != "$raw_title_before" ]]; then
  echo "pane label rename mutated AI topic or raw title" >&2
  exit 1
fi
tmux select-pane -T "runtime title changed" -t "$pane_id"
if [[ "$(tmux show-options -pqv -t "$pane_id" @projmux_pane_label)" != "docker user label" ]] ||
  [[ "$(tmux show-options -pqv -t "$pane_id" @projmux_ai_topic)" != "docker e2e" ]]; then
  echo "native raw-title change mutated pane label or AI topic" >&2
  exit 1
fi
"$bin" tmux rename-pane "$pane_id" ""
if [[ -n "$(tmux show-options -pqv -t "$pane_id" @projmux_pane_label)" ]]; then
  echo "expected empty pane label rename to clear the option" >&2
  exit 1
fi

"$bin" notify reconcile --json >"$PROJMUX_SMOKE_WORKDIR/reconcile.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/reconcile.json" '"pushed": 1'
"$bin" notify list --json >"$PROJMUX_SMOKE_WORKDIR/reconcile-list.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/reconcile-list.json" "docker e2e"

notify_hook="$PROJMUX_SMOKE_WORKDIR/notify-hook.sh"
cat >"$notify_hook" <<'HOOK'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" >>"$PROJMUX_SMOKE_WORKDIR/focus-hook.log"
HOOK
chmod 0755 "$notify_hook"
export PROJMUX_NOTIFY_HOOK="$notify_hook"

"$bin" focus --target e2e-beta --json >"$PROJMUX_SMOKE_WORKDIR/focus.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/focus.json" '"ok":true'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/focus.json" 'notify-only'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/focus-hook.log" "session ready: e2e-beta"

status_notify="$("$bin" status notify --max-width 80)"
smoke_assert_output_contains "$status_notify" "docker e2e"

"$bin" statusbar click notify >"$PROJMUX_SMOKE_WORKDIR/statusbar-notify-click.out"
"$bin" notify list --json >"$PROJMUX_SMOKE_WORKDIR/after-statusbar-click.json"
if grep -Fq "docker e2e" "$PROJMUX_SMOKE_WORKDIR/after-statusbar-click.json"; then
  echo "expected statusbar notify focus to ack docker e2e" >&2
  cat "$PROJMUX_SMOKE_WORKDIR/after-statusbar-click.json" >&2
  exit 1
fi

# Exercise the Settings key recorder through a real attached tmux client and
# display-popup. The pseudo-terminal feeds the popup's existing native-picker
# reader; no user HOME, keymap, or tmux socket is reused.
PROJMUX_SMOKE_TMUX_SOCKET="projmux-recorder-e2e"
export PROJMUX_SMOKE_TMUX_SOCKET
recorder_socket="$PROJMUX_SMOKE_TMUX_SOCKET"
recorder_session="recorder-e2e"
recorder_log="$PROJMUX_SMOKE_WORKDIR/recorder-client.log"
recorder_input="$PROJMUX_SMOKE_WORKDIR/recorder-client.in"
mkfifo "$recorder_input"
tmux -L "$recorder_socket" new-session -d -s "$recorder_session" sleep 300
exec 9<>"$recorder_input"
TERM=xterm-256color script -qefc \
  "TERM=xterm-256color tmux -L '$recorder_socket' attach-session -t '$recorder_session'" \
  "$recorder_log" <"$recorder_input" >/dev/null 2>&1 &
recorder_client_pid=$!

smoke_wait_for() {
  local description="$1"
  shift
  for _ in {1..100}; do
    if "$@"; then
      return 0
    fi
    sleep 0.05
  done
  echo "timed out waiting for $description" >&2
  tail -c 12000 "$recorder_log" >&2 || true
  return 1
}

recorder_client=""
smoke_wait_for "attached recorder tmux client" sh -c \
  "test -n \"\$(tmux -L '$recorder_socket' list-clients -F '#{client_name}' 2>/dev/null | head -n 1)\""
recorder_client="$(tmux -L "$recorder_socket" list-clients -F '#{client_name}' | head -n 1)"
tmux -L "$recorder_socket" display-popup -c "$recorder_client" -T "Recorder E2E" -w 72 -h 20 -E \
  "env PROJMUX_PICKER_BACKEND=native '$bin' settings" &
recorder_popup_pid=$!

smoke_wait_for "Settings root" grep -aFq "Settings >" "$recorder_log"
printf 'Keybindings\r' >&9
smoke_wait_for "Keybindings action list" grep -aFq "Settings > Keybindings >" "$recorder_log"
printf 'Toggle Project Sidebar\r' >&9
smoke_wait_for "keybinding Action detail" grep -aFq "Settings > Keybindings > Action >" "$recorder_log"
printf '+ Add key\r' >&9
smoke_wait_for "Recording state" grep -aFq "Press a key combination" "$recorder_log"
if [[ -e "$XDG_CONFIG_HOME/projmux/keymap.toml" ]]; then
  echo "recorder wrote keymap before candidate confirmation" >&2
  exit 1
fi
printf '\022' >&9
smoke_wait_for "staged C-r preview" grep -aFq "Staged: C-r" "$recorder_log"
if [[ -e "$XDG_CONFIG_HOME/projmux/keymap.toml" ]]; then
  echo "recorder wrote keymap while C-r was only staged" >&2
  exit 1
fi
printf '\r' >&9
smoke_wait_for "confirmed C-r keymap write" grep -Fq '"C-r"' "$XDG_CONFIG_HOME/projmux/keymap.toml"

# Re-enter from the returned Action detail, stage a replacement, and cancel.
printf '+ Add key\r' >&9
printf '\033s' >&9
smoke_wait_for "staged M-s replacement" grep -aFq "Staged: M-s" "$recorder_log"
if grep -Fq '"M-s"' "$XDG_CONFIG_HOME/projmux/keymap.toml"; then
  echo "recorder persisted M-s before confirmation" >&2
  exit 1
fi
recorder_cancel_log_offset="$(stat -c %s "$recorder_log")"
printf '\033' >&9
# Wait until tmux's own Escape disambiguation window has elapsed and the
# recorder has returned to a newly rendered Action detail. Sending the next
# byte before this boundary would correctly turn the pair into a modified key.
smoke_wait_for "Action detail after Esc cancellation" sh -c \
  "tail -c +$((recorder_cancel_log_offset + 1)) '$recorder_log' | grep -aFq '+ Add key'"
if grep -Fq '"M-s"' "$XDG_CONFIG_HOME/projmux/keymap.toml"; then
  echo "recorder persisted M-s after Esc cancellation" >&2
  exit 1
fi
printf '\003' >&9
smoke_wait_for "Settings popup exit" sh -c "! kill -0 '$recorder_popup_pid' 2>/dev/null"
wait "$recorder_popup_pid"
tmux -L "$recorder_socket" kill-server
wait "$recorder_client_pid" || true
exec 9>&-
