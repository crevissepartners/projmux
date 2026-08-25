#!/usr/bin/env bash
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/test/lib/smoke.sh"

smoke_setup_env
trap smoke_cleanup_env EXIT
cd "$smoke_root"

smoke_build_binary
bin="$PROJMUX_SMOKE_BIN"

# A genuinely absent app socket must survive the complete ControlSession
# bootstrap before the foreground attach. Run the user-facing, selector-free
# command from exact HOME with an empty .git directory: HOME is the upward-walk
# boundary for this shell surface, so its own marker cannot register HOME as a
# Project. Nested project markers below HOME remain eligible. This invocation
# has no terminal, so the final attach is expected to fail after preparation;
# the exact real-tmux server/session/route marker and ControlSession mirrors
# must remain inspectable. This closes both ordering hazards that scripted
# runners previously hid: the logical marker is absent before its own planned
# write, and tmux may render the containment row separator as literal \037.
fresh_shell_root="$PROJMUX_SMOKE_WORKDIR/fresh-shell"
fresh_shell_socket="projmux-shell-fresh-e2e"
fresh_shell_home="$fresh_shell_root/home"
fresh_shell_config="$fresh_shell_root/config"
fresh_shell_state="$fresh_shell_root/state"
fresh_shell_runtime="$fresh_shell_root/runtime"
fresh_shell_cache="$fresh_shell_root/cache"
fresh_shell_usage="$fresh_shell_root/usage"
fresh_shell_stub="$fresh_shell_root/persistent-shell"
mkdir -p "$fresh_shell_home/.git" "$fresh_shell_config" "$fresh_shell_state" \
  "$fresh_shell_runtime" "$fresh_shell_cache" "$fresh_shell_usage"
chmod 0700 "$fresh_shell_runtime"
cat >"$fresh_shell_stub" <<'FRESH_SHELL_STUB'
#!/bin/sh
while :; do sleep 60; done
FRESH_SHELL_STUB
chmod 0755 "$fresh_shell_stub"
PROJMUX_SMOKE_TMUX_SOCKET="$fresh_shell_socket"
export PROJMUX_SMOKE_TMUX_SOCKET
set +e
(
  cd "$fresh_shell_home"
  printf '\n' | env -u TMUX -u TMUX_PANE \
    HOME="$fresh_shell_home" \
    XDG_CACHE_HOME="$fresh_shell_cache" \
    XDG_CONFIG_HOME="$fresh_shell_config" \
    XDG_RUNTIME_DIR="$fresh_shell_runtime" \
    XDG_STATE_HOME="$fresh_shell_state" \
    TMUX_TMPDIR="$PROJMUX_SMOKE_TMUX_ROOT" \
    PROJMUX_USAGE_STATE_DIR="$fresh_shell_usage" \
    PROJMUX_SHELL_UPDATE_CHECK_TIMEOUT_MS=1 \
    SHELL="$fresh_shell_stub" \
    "$bin" shell --socket "$fresh_shell_socket" --bin "$bin" \
    >"$fresh_shell_root/shell.out" 2>"$fresh_shell_root/shell.err"
)
fresh_shell_status=$?
set -e
if [[ "$fresh_shell_status" != "1" ]]; then
  echo "non-terminal fresh projmux shell exited $fresh_shell_status, want attach failure 1 after bootstrap" >&2
  cat "$fresh_shell_root/shell.err" >&2 || true
  exit 1
fi
fresh_shell_tmux() {
  env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$PROJMUX_SMOKE_TMUX_ROOT" tmux -L "$fresh_shell_socket" "$@"
}
fresh_shell_actual="$(fresh_shell_tmux display-message -p -t '=home' '#{socket_path}')"
case "$fresh_shell_actual" in
  "$PROJMUX_SMOKE_TMUX_ROOT"/*) ;;
  *)
    echo "fresh shell socket escaped smoke root: $fresh_shell_actual" >&2
    exit 1
    ;;
esac
fresh_shell_control="$(fresh_shell_tmux list-sessions -F '#{session_name}|#{@projmux_session_role}')"
if [[ "$fresh_shell_control" != "home|control" ]]; then
  echo "fresh shell did not preserve the exact Home ControlSession: $fresh_shell_control" >&2
  exit 1
fi
if [[ "$(fresh_shell_tmux show-options -gqv @projmux_socket_name)" != "$fresh_shell_socket" ]]; then
  echo "fresh shell logical route marker is absent or drifted" >&2
  exit 1
fi
fresh_shell_identity="$(fresh_shell_tmux list-panes -t '=home' -F '#{@projmux_window_uid}|#{@projmux_pane_uid}')"
if [[ "$fresh_shell_identity" != *"|"* ]] || [[ "$fresh_shell_identity" == "|" ]] || \
  [[ -z "${fresh_shell_identity%%|*}" ]] || [[ -z "${fresh_shell_identity#*|}" ]]; then
  echo "fresh shell ControlSession has incomplete Window/Pane identity: $fresh_shell_identity" >&2
  exit 1
fi
fresh_shell_projects="$(
  env -u TMUX -u TMUX_PANE \
    HOME="$fresh_shell_home" \
    XDG_CONFIG_HOME="$fresh_shell_config" \
    XDG_RUNTIME_DIR="$fresh_shell_runtime" \
    XDG_STATE_HOME="$fresh_shell_state" \
    PROJMUX_USAGE_STATE_DIR="$fresh_shell_usage" \
    "$bin" get projects -o uid
)"
if [[ -n "$fresh_shell_projects" ]]; then
  echo "HOME boundary registered HOME/.git as a Project: $fresh_shell_projects" >&2
  exit 1
fi
smoke_cleanup_tmux_server "$fresh_shell_socket"
unset PROJMUX_SMOKE_TMUX_SOCKET
echo ">> fresh projmux shell e2e bootstrapped Home on $fresh_shell_actual"

# An unattributed pane is not a Projmux scope. `create` used to fall back to a
# runtime-only split here; it now refuses and names --project, and it must leave
# the server exactly as it found it. The positive implicit-scope path is
# exercised further down, from a pane that really carries a managed identity.
PROJMUX_SMOKE_TMUX_SOCKET="projmux-pane-id-e2e"
export PROJMUX_SMOKE_TMUX_SOCKET
pane_id_socket="$PROJMUX_SMOKE_TMUX_SOCKET"
pane_id_session="pane-id-e2e"
tmux -L "$pane_id_socket" new-session -d -s "$pane_id_session" -c "$smoke_root" sleep 300
pane_id_target="$(tmux -L "$pane_id_socket" display-message -p -t "$pane_id_session:0.0" '#{pane_id}')"
pane_id_socket_path="$(tmux -L "$pane_id_socket" display-message -p -t "$pane_id_target" '#{socket_path}')"
pane_id_server_pid="$(tmux -L "$pane_id_socket" display-message -p -t "$pane_id_target" '#{pid}')"
pane_id_panes_before="$(tmux -L "$pane_id_socket" list-panes -a -F '#{pane_id}' | wc -l)"
set +e
pane_id_output="$(
  TMUX="$pane_id_socket_path,$pane_id_server_pid,0" \
    TMUX_PANE="$pane_id_target" \
    SHELL="/bin/sh" \
    "$bin" create pane -o pane-id --placement right 2>"$PROJMUX_SMOKE_WORKDIR/unattributed-create.err"
)"
pane_id_status=$?
set -e
if [[ "$pane_id_status" != "2" ]]; then
  echo "create from an unattributed pane exited $pane_id_status, want 2" >&2
  cat "$PROJMUX_SMOKE_WORKDIR/unattributed-create.err" >&2 || true
  exit 1
fi
if [[ -n "$pane_id_output" ]]; then
  echo "a refused create wrote to stdout: $pane_id_output" >&2
  exit 1
fi
# The needle deliberately starts with a word: the helper passes it to grep as a
# pattern, so a leading `--` would be read as an option.
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/unattributed-create.err" "pass --project <ref>"
if [[ "$(tmux -L "$pane_id_socket" list-panes -a -F '#{pane_id}' | wc -l)" != "$pane_id_panes_before" ]]; then
  echo "a refused create still split a pane" >&2
  exit 1
fi
tmux -L "$pane_id_socket" kill-server

project_a="$PROJMUX_SMOKE_WORKDIR/projects/alpha"
project_b="$PROJMUX_SMOKE_WORKDIR/projects/beta"
mkdir -p "$project_a" "$project_b"

tmux new-session -d -s e2e-alpha -c "$project_a" sleep 300
tmux new-session -d -s e2e-beta -c "$project_b" sleep 300
tmux split-window -d -t e2e-alpha:0 -c "$project_a" sleep 300

"$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket projmux >"$PROJMUX_SMOKE_WORKDIR/apply-empty.out"
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
"$bin" internal tmux rename-pane "$pane_id" "docker user label"
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
"$bin" internal tmux rename-pane "$pane_id" ""
if [[ -n "$(tmux show-options -pqv -t "$pane_id" @projmux_pane_label)" ]]; then
  echo "expected empty pane label rename to clear the option" >&2
  exit 1
fi

"$bin" notification reconcile --json >"$PROJMUX_SMOKE_WORKDIR/reconcile.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/reconcile.json" '"pushed": 1'
"$bin" get notifications --json >"$PROJMUX_SMOKE_WORKDIR/reconcile-list.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/reconcile-list.json" "docker e2e"

notify_hook="$PROJMUX_SMOKE_WORKDIR/notify-hook.sh"
cat >"$notify_hook" <<'HOOK'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" >>"$PROJMUX_SMOKE_WORKDIR/focus-hook.log"
HOOK
chmod 0755 "$notify_hook"
export PROJMUX_NOTIFY_HOOK="$notify_hook"

"$bin" internal focus --target e2e-beta --json >"$PROJMUX_SMOKE_WORKDIR/focus.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/focus.json" '"ok":true'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/focus.json" 'notify-only'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/focus-hook.log" "session ready: e2e-beta"

status_notify="$("$bin" internal status notify --max-width 80)"
smoke_assert_output_contains "$status_notify" "docker e2e"

"$bin" internal statusbar click notify >"$PROJMUX_SMOKE_WORKDIR/statusbar-notify-click.out"
"$bin" get notifications --json >"$PROJMUX_SMOKE_WORKDIR/after-statusbar-click.json"
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
recorder_bootstrap_session="recorder-bootstrap-e2e"
recorder_root="$PROJMUX_SMOKE_WORKDIR/recorder-e2e"
recorder_bootstrap_root="$PROJMUX_SMOKE_WORKDIR/recorder-bootstrap"
recorder_log="$PROJMUX_SMOKE_WORKDIR/recorder-client.log"
recorder_input="$PROJMUX_SMOKE_WORKDIR/recorder-client.in"
mkdir -p "$recorder_root" "$recorder_bootstrap_root"
mkfifo "$recorder_input"
tmux -L "$recorder_socket" new-session -d -s "$recorder_bootstrap_session" -c "$recorder_bootstrap_root" sleep 300
exec 9<>"$recorder_input"
TERM=xterm-256color script -qefc \
  "TERM=xterm-256color tmux -L '$recorder_socket' attach-session -t '$recorder_bootstrap_session'" \
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

# Reuse this attached client and FIFO for the canonical rename-pane-label
# command-prompt contract.
recorder_pane="$(tmux -L "$recorder_socket" display-message -p -c "$recorder_client" '#{pane_id}')"
tmux -L "$recorder_socket" select-pane -T "recorder raw title" -t "$recorder_pane"
tmux -L "$recorder_socket" set-option -p -t "$recorder_pane" @projmux_pane_label "existing label"
tmux -L "$recorder_socket" set-option -p -t "$recorder_pane" @projmux_ai_agent codex
tmux -L "$recorder_socket" set-option -p -t "$recorder_pane" @projmux_ai_topic "recorder AI topic"
tmux -L "$recorder_socket" set-option -p -t "$recorder_pane" @projmux_ai_topic_manual 1
recorder_raw_title="$(tmux -L "$recorder_socket" display-message -p -t "$recorder_pane" '#{pane_title}')"

# Retired picker-selection artifacts are inert: the Settings popup stays on the
# native path without reading, deleting, rewriting, propagating, or warning
# about either value.
retired_picker_file="$XDG_CONFIG_HOME/projmux/picker-backend"
mkdir -p "$(dirname "$retired_picker_file")"
printf 'retired-value\n' >"$retired_picker_file"
retired_picker_inode="$(stat -c %i "$retired_picker_file")"

recorder_rename_prompt() {
  tmux -L "$recorder_socket" command-prompt -b -t "$recorder_client" \
    -p "pane label:" -I '#{@projmux_pane_label}' \
    "if-shell -F '#{==:%1,}' 'set-option -p -u @projmux_pane_label' 'set-option -p @projmux_pane_label \"%1\"'"
}
recorder_label_is() {
  [[ "$(tmux -L "$recorder_socket" show-options -pqv -t "$recorder_pane" @projmux_pane_label)" == "$1" ]]
}
recorder_label_empty() {
  [[ -z "$(tmux -L "$recorder_socket" show-options -pqv -t "$recorder_pane" @projmux_pane_label)" ]]
}
assert_recorder_identity_metadata() {
  if [[ "$(tmux -L "$recorder_socket" show-options -pqv -t "$recorder_pane" @projmux_ai_topic)" != "recorder AI topic" ]] ||
    [[ "$(tmux -L "$recorder_socket" show-options -pqv -t "$recorder_pane" @projmux_ai_topic_manual)" != "1" ]] ||
    [[ "$(tmux -L "$recorder_socket" display-message -p -t "$recorder_pane" '#{pane_title}')" != "$recorder_raw_title" ]]; then
    echo "rename-pane-label command-prompt mutated topic/manual/raw title" >&2
    exit 1
  fi
}

# Enter with no edits confirms the prompt's captured current-label initial value.
recorder_rename_prompt
tmux -L "$recorder_socket" set-option -p -t "$recorder_pane" @projmux_pane_label "initial value probe"
printf '\r' >&9
smoke_wait_for "restored initial pane label" recorder_label_is "existing label"
assert_recorder_identity_metadata

# Ctrl-U replaces the initial value and Enter confirms it.
recorder_rename_prompt
printf '\025confirmed label\r' >&9
smoke_wait_for "confirmed pane label" recorder_label_is "confirmed label"
assert_recorder_identity_metadata

# Native Esc cancels a staged edit and leaves the prior label unchanged.
recorder_rename_prompt
printf '\025cancelled label\033' >&9
# Wait past tmux's Escape disambiguation window before checking state or
# starting the next command prompt.
sleep 0.6
if ! recorder_label_is "confirmed label"; then
  echo "rename-pane-label Esc cancellation changed the label" >&2
  exit 1
fi
assert_recorder_identity_metadata

# Empty input plus Enter clears only the label.
recorder_rename_prompt
printf '\025\r' >&9
smoke_wait_for "cleared pane label" recorder_label_empty
assert_recorder_identity_metadata

# Settings live apply is an app-owned product surface. Move the raw command-
# prompt transport checks above this boundary, then establish the recorder's
# exact app/logical route through the canonical config apply path.
"$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$recorder_socket" \
  >"$PROJMUX_SMOKE_WORKDIR/recorder-e2e-apply.out"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/recorder-e2e-apply.out" "reloaded tmux server -L $recorder_socket: 1 sessions"
# Ctrl-N is the terminal-level window.create transport, so the tmux config
# retires any stale raw C-n binding. The successful apply above plus this exact
# retirement distinguishes an invalid source from a popup authority failure;
# configured tmux chords reach the canonical exact-anchor handler in the
# integration reassignment proof.
smoke_assert_file_contains "$XDG_CONFIG_HOME/projmux/tmux.conf" "unbind-key -q -n C-n"

# display-popup does not promise to synthesize TMUX_PANE for its child. Capture
# the active client Pane from this fully marked app route, then reobserve the
# complete $/@/% containment through the exact physical socket before passing
# the product-private anchor to Settings. Nothing is exported to later commands.
recorder_popup_receipt="$(tmux -L "$recorder_socket" display-message -p -c "$recorder_client" \
  '#{socket_path}|#{pid}|#{session_id}|#{window_id}|#{pane_id}')"
IFS='|' read -r recorder_socket_path recorder_server_pid recorder_popup_session \
  recorder_popup_window recorder_popup_anchor <<<"$recorder_popup_receipt"
case "$recorder_socket_path" in
  "$PROJMUX_SMOKE_TMUX_ROOT"/*) ;;
  *)
    echo "Settings recorder socket escaped its isolated root: $recorder_popup_receipt" >&2
    exit 1
    ;;
esac
if [[ ! "$recorder_server_pid" =~ ^[1-9][0-9]*$ ]] ||
  [[ ! "$recorder_popup_session" =~ ^\$[0-9]+$ ]] ||
  [[ ! "$recorder_popup_window" =~ ^@[0-9]+$ ]] ||
  [[ ! "$recorder_popup_anchor" =~ ^%[0-9]+$ ]] ||
  [[ "$(tmux -L "$recorder_socket" show-options -gqv @projmux_app)" != "1" ]] ||
  [[ "$(tmux -L "$recorder_socket" show-options -gqv @projmux_socket_name)" != "$recorder_socket" ]] ||
  [[ "$(env -u TMUX -u TMUX_PANE tmux -S "$recorder_socket_path" display-message -p \
    -t "$recorder_popup_anchor" '#{socket_path}|#{pid}|#{session_id}|#{window_id}|#{pane_id}')" != "$recorder_popup_receipt" ]]; then
  echo "Settings recorder has no exact app-owned client Pane receipt: $recorder_popup_receipt" >&2
  exit 1
fi

tmux -L "$recorder_socket" display-popup -c "$recorder_client" \
	-T "Recorder E2E" -w 72 -h 20 -E \
	"env __PROJMUX_RUNTIME_ANCHOR_PANE='$recorder_popup_anchor' PROJMUX_PICKER_BACKEND=retired-value '$bin' settings" &
recorder_popup_pid=$!

smoke_wait_for "Settings root" grep -aFq "Settings >" "$recorder_log"

# Deep search lands on the owning View and never executes a control on the way.
# Searching a Status Bar component name from the root walks Appearance > Status
# Bar; reaching the container must not write a decoration value. Each wait is
# anchored to a byte offset so a frame from an earlier screen cannot satisfy it.
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'Status Bar\r' >&9
smoke_wait_for "Appearance view" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Settings > Appearance > '"
# Rows that carry a search key are matched on that key, which is lower case.
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'status bar components\r' >&9
smoke_wait_for "Status Bar container" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Settings > Appearance > Status Bar > '"
if [[ -e "$XDG_CONFIG_HOME/projmux/statusbar-decoration-git" ]]; then
  echo "navigating to the Status Bar container wrote a decoration value" >&2
  exit 1
fi
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'Back\r' >&9
smoke_wait_for "Appearance view after Back" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Agent attention badge style'"
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'Back\r' >&9
smoke_wait_for "Settings root after deep search" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Keybindings'"

# Notification ownership walk. The tmux bell is an event source rather than an
# Agent Provider, so it has its own destination: a deep search from the root
# lands on the owning Notifications View, and entering the row reaches the flat
# bell wiring state. Reaching either destination is navigation, so it must not
# write the Agent event behavior runtime file. Every wait is byte-offset
# anchored so an earlier frame cannot satisfy it.
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'Provider Integrations\r' >&9
smoke_wait_for "Notifications view" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Settings > Notifications > '"
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'tmux event source bell producer\r' >&9
smoke_wait_for "tmux event source view" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Settings > Notifications > tmux event source > '"
# The flat bell state renders here rather than behind a Provider item detail.
# The full "not an Agent Provider" source text is asserted by the unit tests; a
# 72-column popup truncates it, so the smoke checks the row itself.
smoke_wait_for "bell wiring state" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Bell wiring status'"
if [[ -e "$XDG_CONFIG_HOME/projmux/ai-hook-actions.json" ]]; then
  echo "navigating to the tmux event source wrote an Agent event behavior override" >&2
  exit 1
fi
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'Back\r' >&9
smoke_wait_for "Notifications view after Back" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Provider Integrations'"
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'Back\r' >&9
smoke_wait_for "Settings root after notification walk" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Keybindings'"

# Agent Usage HUD is a three-depth Settings View. Walk root -> Appearance ->
# Status Bar -> HUD -> Claude, save the Weekly leaf, and prove the Settings
# mutation regenerated the config and source-applied it to this exact recorder
# socket without collapsing the overall usage range. Toggle it back on so the
# rest of the e2e suite retains compatibility defaults.
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'Appearance\r' >&9
smoke_wait_for "Appearance view for Agent Usage HUD" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Settings > Appearance > '"
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'agent usage hud providers windows\r' >&9
smoke_wait_for "Status Bar view for Agent Usage HUD" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Settings > Appearance > Status Bar > '"
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'agent usage hud providers windows\r' >&9
smoke_wait_for "Agent Usage HUD view" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Settings > Appearance > Status Bar > Agent Usage HUD > '"
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'claude\r' >&9
smoke_wait_for "Claude usage provider view" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Agent Usage HUD > Claude'"
claude_weekly_visibility="$XDG_CONFIG_HOME/projmux/statusbar-visibility-agent-usage-window-claude-weekly"
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'weekly\r' >&9
smoke_wait_for "Claude Weekly visibility saved off" grep -Fxq 'off' "$claude_weekly_visibility"
smoke_wait_for "Claude visibility mutation feedback" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Feedback'"
smoke_assert_file_contains "$XDG_CONFIG_HOME/projmux/tmux.conf" 'internal status usage --max-width'
recorder_row0="$(tmux -L "$recorder_socket" show-options -gqv 'status-format[0]')"
[[ "$recorder_row0" == *'range=user|usage'* ]] || { echo "leaf Settings save collapsed live usage range: $recorder_row0" >&2; exit 1; }
printf 'weekly\r' >&9
smoke_wait_for "Claude Weekly visibility restored on" grep -Fxq 'on' "$claude_weekly_visibility"
for destination in "Agent Usage HUD" "Status Bar" "Appearance" "Settings root"; do
  settings_nav_offset="$(stat -c %s "$recorder_log")"
  printf 'Back\r' >&9
  smoke_wait_for "Back from $destination" sh -c \
    "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Settings >'"
done
smoke_wait_for "Settings root after usage visibility walk" grep -aFq "Keybindings" "$recorder_log"

printf 'Keybindings\r' >&9
smoke_wait_for "Keybindings category list" grep -aFq "Settings > Keybindings >" "$recorder_log"
# Phase 0 nests the actions under navigation categories: search reaches the
# owning category from the root, and the action itself one level down. The
# search text below is an action name, which proves search crosses the category
# boundary rather than only matching the category label.
printf 'Open / close Project Sidebar\r' >&9
smoke_wait_for "Launch & popups category" grep -aFq "Settings > Keybindings > Launch & popups >" "$recorder_log"
printf 'Open / close Project Sidebar\r' >&9
smoke_wait_for "keybinding Action detail" grep -aFq "Settings > Keybindings > Action >" "$recorder_log"
printf '+ Add binding\r' >&9
smoke_wait_for "Recording state" grep -aFq "Press 1 to 4 strokes" "$recorder_log"
if [[ -e "$XDG_CONFIG_HOME/projmux/keymap.toml" ]]; then
  echo "recorder wrote keymap before candidate confirmation" >&2
  exit 1
fi
printf '\022' >&9
smoke_wait_for "staged C-r preview" grep -aFq "Recorded: C-r" "$recorder_log"
if [[ -e "$XDG_CONFIG_HOME/projmux/keymap.toml" ]]; then
  echo "recorder wrote keymap while C-r was only staged" >&2
  exit 1
fi
printf '\r' >&9
smoke_wait_for "confirmed C-r keymap write" grep -Fq '"C-r"' "$XDG_CONFIG_HOME/projmux/keymap.toml"

# Re-enter from the returned Action detail, stage a replacement, and cancel.
printf '+ Add binding\r' >&9
printf '\033s' >&9
smoke_wait_for "staged M-s replacement" grep -aFq "Recorded: M-s" "$recorder_log"
if grep -Fq '"M-s"' "$XDG_CONFIG_HOME/projmux/keymap.toml"; then
  echo "recorder persisted M-s before confirmation" >&2
  exit 1
fi
recorder_cancel_log_offset="$(stat -c %s "$recorder_log")"
printf '\033' >&9
# Wait until tmux's own Escape disambiguation window has elapsed and the
# recorder has returned to a newly rendered Action detail. Sending the next
# byte before this boundary would correctly turn the pair into a modified key.
# The returned Action detail carries the cancellation as an observable
# feedback row, which is also the zero-silent-no-op contract.
smoke_wait_for "Action detail after Esc cancellation" sh -c \
  "tail -c +$((recorder_cancel_log_offset + 1)) '$recorder_log' | grep -aFq 'Keybinding cancelled'"
if grep -Fq '"M-s"' "$XDG_CONFIG_HOME/projmux/keymap.toml"; then
  echo "recorder persisted M-s after Esc cancellation" >&2
  exit 1
fi

# Interaction-first action-detail correctness on the same real client. Current
# bindings and actions remain visible, passive semantic/handler rows and
# teaching containers are gone, and the key detail's Test delivery row reports
# a result instead of returning to the same loop.
recorder_detail_offset="$recorder_cancel_log_offset"
for marker in 'Single Keys' 'Sequences' '+ Add binding'; do
  smoke_wait_for "action detail $marker" sh -c \
    "tail -c +$((recorder_detail_offset + 1)) '$recorder_log' | grep -aFq '$marker'"
done
if tail -c +$((recorder_detail_offset + 1)) "$recorder_log" | grep -aEq 'Target kind|Result kind|Placement|Anchor|Handler|Options|Troubleshooting|Advanced\.\.\.'; then
  echo "action detail still renders passive internal copy or a teaching container" >&2
  exit 1
fi
recorder_keydetail_offset="$(stat -c %s "$recorder_log")"
# The row value is part of the searchable text, so `key:C-r` addresses the
# C-r key row exactly. Searching the readable chord would also match the
# passive Keys summary row, which lists every active chord.
printf 'key:C-r\r' >&9
smoke_wait_for "key detail" sh -c \
  "tail -c +$((recorder_keydetail_offset + 1)) '$recorder_log' | grep -aFq 'Settings > Keybindings > Action > Key > '"
smoke_wait_for "key detail Test delivery row" sh -c \
  "tail -c +$((recorder_keydetail_offset + 1)) '$recorder_log' | grep -aFq 'Test delivery'"
if tail -c +$((recorder_keydetail_offset + 1)) "$recorder_log" | grep -aEq 'Canonical key|Delivery path'; then
  echo "key detail still renders canonical-storage or delivery-path teaching" >&2
  exit 1
fi
keymap_before_test="$(cat "$XDG_CONFIG_HOME/projmux/keymap.toml")"
recorder_testdelivery_offset="$(stat -c %s "$recorder_log")"
printf 'Test delivery\r' >&9
smoke_wait_for "Test delivery reader" sh -c \
  "tail -c +$((recorder_testdelivery_offset + 1)) '$recorder_log' | grep -aFq 'Press 1 to 4 strokes'"
recorder_teststage_offset="$(stat -c %s "$recorder_log")"
printf '\022' >&9
smoke_wait_for "Test delivery observed C-r" sh -c \
  "tail -c +$((recorder_teststage_offset + 1)) '$recorder_log' | grep -aFq 'Recorded: C-r'"
recorder_testresult_offset="$(stat -c %s "$recorder_log")"
printf '\r' >&9
smoke_wait_for "Test delivery observable result" sh -c \
  "tail -c +$((recorder_testresult_offset + 1)) '$recorder_log' | grep -aFq 'Test delivery complete'"
if [[ "$keymap_before_test" != "$(cat "$XDG_CONFIG_HOME/projmux/keymap.toml")" ]]; then
  echo "Test delivery mutated keymap.toml" >&2
  exit 1
fi

# Phase 1 sequence authoring uses the same attached Settings popup and the same
# continuous recorder as one-stroke bindings. Record C-o,o, pop/re-add the
# second stroke without closing the recorder, confirm once, prove the
# generated/live trie exists, then remove it and prove stale state is gone.
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'Back\r' >&9
smoke_wait_for "Action detail after key delivery test" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Settings > Keybindings > Action > '"
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf '+ Add binding\r' >&9
smoke_wait_for "unified sequence recorder" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Press 1 to 4 strokes'"
if grep -Fq 'sequences = ' "$XDG_CONFIG_HOME/projmux/keymap.toml"; then
  echo "sequence recorder wrote keymap before capture" >&2
  exit 1
fi

settings_nav_offset="$(stat -c %s "$recorder_log")"
printf '\017' >&9
smoke_wait_for "first sequence stroke accumulated" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Recorded: C-o'"
if grep -Fq 'sequences = ' "$XDG_CONFIG_HOME/projmux/keymap.toml"; then
  echo "first sequence stroke mutated keymap" >&2
  exit 1
fi

settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'o' >&9
smoke_wait_for "two-stroke sequence draft" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Recorded: C-o,o'"
if grep -Fq 'sequences = ' "$XDG_CONFIG_HOME/projmux/keymap.toml"; then
  echo "sequence draft mutated keymap before Save" >&2
  exit 1
fi

settings_nav_offset="$(stat -c %s "$recorder_log")"
printf '\177' >&9
smoke_wait_for "Backspace popped only the last stroke" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Recorded: C-o'"
if grep -Fq 'sequences = ' "$XDG_CONFIG_HOME/projmux/keymap.toml"; then
  echo "sequence Backspace mutated keymap" >&2
  exit 1
fi
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'o\r' >&9
smoke_wait_for "saved sequence keymap" grep -Fq 'sequences = ["C-o o"]' "$XDG_CONFIG_HOME/projmux/keymap.toml"
smoke_wait_for "sequence save feedback" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Keybinding complete'"
sequence_table="$(tmux -L "$recorder_socket" show-options -gqv @projmux_sequence_tables)"
if [[ "$(tmux -L "$recorder_socket" show-options -gqv @projmux_sequence_roots)" != "C-o" ]] ||
  [[ -z "$sequence_table" ]] ||
  [[ "$(tmux -L "$recorder_socket" list-keys -T root C-o)" != *"switch-client -T $sequence_table"* ]] ||
  [[ "$(tmux -L "$recorder_socket" list-keys -T "$sequence_table" o)" != *"run-shell"* ]]; then
  echo "Settings sequence save did not install the expected live trie" >&2
  tmux -L "$recorder_socket" show-options -gqv @projmux_sequence_roots >&2 || true
  tmux -L "$recorder_socket" show-options -gqv @projmux_sequence_tables >&2 || true
  exit 1
fi

settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'sequence:C-o o\r' >&9
smoke_wait_for "saved sequence detail" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'C-o,o'"
if tail -c +$((settings_nav_offset + 1)) "$recorder_log" | grep -aEq 'Cancellation|authoring and saved bytes|saved logical strokes'; then
  echo "sequence detail still renders cancellation, delivery, or storage teaching" >&2
  exit 1
fi
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'Remove sequence\r' >&9
smoke_wait_for "removed sequence keymap" sh -c \
  "! grep -Fq 'sequences = ' '$XDG_CONFIG_HOME/projmux/keymap.toml'"
smoke_wait_for "sequence remove feedback" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Keybinding complete'"
if [[ -n "$(tmux -L "$recorder_socket" show-options -gqv @projmux_sequence_roots)" ]] ||
  [[ -n "$(tmux -L "$recorder_socket" show-options -gqv @projmux_sequence_tables)" ]] ||
  tmux -L "$recorder_socket" list-keys -T "$sequence_table" >/dev/null 2>&1; then
  echo "Settings sequence removal left stale live trie state" >&2
  echo "roots=$(tmux -L "$recorder_socket" show-options -gqv @projmux_sequence_roots)" >&2
  echo "tables=$(tmux -L "$recorder_socket" show-options -gqv @projmux_sequence_tables)" >&2
  tmux -L "$recorder_socket" list-keys -T root C-o >&2 || true
  tmux -L "$recorder_socket" list-keys -T "$sequence_table" >&2 || true
  exit 1
fi

# Exercise the remaining normal state matrix on the same exact client. Remove
# every single key, observe Unbound plus the reachable Use default action, then
# restore the shipped binding and prove the live root table follows both writes.
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'Unbind single keys\r' >&9
smoke_wait_for "unbound keymap bytes" grep -Fq 'keys = []' "$XDG_CONFIG_HOME/projmux/keymap.toml"
smoke_wait_for "unbound action detail" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Unbound'"
smoke_wait_for "use-default action" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Use default'"
if tmux -L "$recorder_socket" list-keys -T root M-1 >/dev/null 2>&1; then
  echo "Unbind single keys left the M-1 live root binding active" >&2
  exit 1
fi

settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'Use default\r' >&9
smoke_wait_for "default action detail" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Default'"
smoke_wait_for "restored default root binding" sh -c \
  "tmux -L '$recorder_socket' list-keys -T root M-1 | grep -Fq 'popup-toggle --client'"
if grep -Fq '[bindings.ProjectSidebarToggle]' "$XDG_CONFIG_HOME/projmux/keymap.toml"; then
  echo "Use default left a ProjectSidebarToggle override in keymap.toml" >&2
  exit 1
fi

# A protected picker action supplies both conditional cases that normal detail
# intentionally omits: the exact read-only reason, and an unavailable delivery
# observation with a short, immediately usable alternative. Navigation alone
# must not change the just-restored keymap bytes.
protected_keymap_before="$(cat "$XDG_CONFIG_HOME/projmux/keymap.toml")"
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'Back\r' >&9
smoke_wait_for "Launch category after default restore" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Settings > Keybindings > Launch & popups >'"
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'Back\r' >&9
smoke_wait_for "Keybindings root before protected action" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Settings > Keybindings >'"

protected_action='Focus source and acknowledge Notification'
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf '%s\r' "$protected_action" >&9
smoke_wait_for "protected action category" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Settings > Keybindings > Sidebar & picker actions >'"
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf '%s\r' "$protected_action" >&9
smoke_wait_for "protected action surface" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Sidebar & picker actions > Notificat'"
settings_nav_offset="$(stat -c %s "$recorder_log")"
printf '%s\r' "$protected_action" >&9
smoke_wait_for "protected action reason" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Editing locked'"
smoke_wait_for "protected trigger reason" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'shipped/default trigger'"
if tail -c +$((settings_nav_offset + 1)) "$recorder_log" | grep -aEq '\+ Add binding|Enter binding manually|Unbind single keys|Reset to default|Use default'; then
  echo "protected action exposed a mutation row" >&2
  exit 1
fi

settings_nav_offset="$(stat -c %s "$recorder_log")"
printf 'key:Enter\r' >&9
smoke_wait_for "protected unavailable key detail" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'Test delivery'"
smoke_wait_for "unavailable recorder reason" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'recorder uses Enter'"
smoke_wait_for "unavailable usable alternative" sh -c \
  "tail -c +$((settings_nav_offset + 1)) '$recorder_log' | grep -aFq 'projmux setup'"
if [[ "$protected_keymap_before" != "$(cat "$XDG_CONFIG_HOME/projmux/keymap.toml")" ]]; then
  echo "protected/unavailable detail navigation mutated keymap.toml" >&2
  exit 1
fi

printf '\003' >&9
smoke_wait_for "Settings popup exit" sh -c "! kill -0 '$recorder_popup_pid' 2>/dev/null"
wait "$recorder_popup_pid"
if [[ "$(cat "$retired_picker_file")" != "retired-value" ]] ||
  [[ "$(stat -c %i "$retired_picker_file")" != "$retired_picker_inode" ]]; then
  echo "retired picker backend file was deleted or rewritten" >&2
  exit 1
fi
if grep -aiEq 'picker backend|picker-backend|PROJMUX_PICKER_BACKEND' "$recorder_log"; then
  echo "retired picker backend artifact produced visible output" >&2
  exit 1
fi

# Exercise Notify Clear All against the same real attached client/FIFO. The
# queue JSON and a client-scoped command marker are completion signals; screen
# text is used only to know when the confirmation picker is ready for input.
for severity in info warn critical; do
  "$bin" create notification \
    --id "polish-$severity" \
    --text "polish $severity" \
    --target "$recorder_bootstrap_session:0.0" \
    --socket "$recorder_socket" \
    --severity "$severity" \
    --source external >/dev/null
done
notify_before="$PROJMUX_SMOKE_WORKDIR/notify-polish-before.json"
"$bin" get notifications --json >"$notify_before"
for severity in info warn critical; do
  smoke_assert_file_contains "$notify_before" "polish-$severity"
done

notify_focus_before="$(tmux -L "$recorder_socket" display-message -p -c "$recorder_client" '#{pane_id}')"
notify_cancel_marker="$PROJMUX_SMOKE_WORKDIR/notify-clear-cancel.done"
notify_cancel_offset="$(stat -c %s "$recorder_log")"
tmux -L "$recorder_socket" display-popup -c "$recorder_client" \
  -T "Notify E2E" -w 80 -h 24 -E \
  "'$bin' get notifications --ui=sidebar --client '$recorder_client'; printf done >'$notify_cancel_marker'" &
notify_cancel_pid=$!
smoke_wait_for "Notify sidebar before cancel" sh -c \
  "tail -c +$((notify_cancel_offset + 1)) '$recorder_log' | grep -aFq 'Pending Notifications'"
printf '\030' >&9
smoke_wait_for "Notify Clear All cancel confirmation" sh -c \
  "tail -c +$((notify_cancel_offset + 1)) '$recorder_log' | grep -aFq 'Clear all'"
printf '\033' >&9
smoke_wait_for "Notify cancel command marker" test -f "$notify_cancel_marker"
wait "$notify_cancel_pid"
notify_after_cancel="$PROJMUX_SMOKE_WORKDIR/notify-polish-after-cancel.json"
"$bin" get notifications --json >"$notify_after_cancel"
for severity in info warn critical; do
  smoke_assert_file_contains "$notify_after_cancel" "polish-$severity"
done
if [[ "$(tmux -L "$recorder_socket" display-message -p -c "$recorder_client" '#{pane_id}')" != "$notify_focus_before" ]]; then
  echo "Notify Clear All cancel did not restore the attached client focus" >&2
  exit 1
fi

notify_confirm_marker="$PROJMUX_SMOKE_WORKDIR/notify-clear-confirm.done"
notify_confirm_offset="$(stat -c %s "$recorder_log")"
tmux -L "$recorder_socket" display-popup -c "$recorder_client" \
  -T "Notify E2E" -w 80 -h 24 -E \
  "'$bin' get notifications --ui=sidebar --client '$recorder_client'; printf done >'$notify_confirm_marker'" &
notify_confirm_pid=$!
smoke_wait_for "Notify sidebar before confirm" sh -c \
  "tail -c +$((notify_confirm_offset + 1)) '$recorder_log' | grep -aFq 'Pending Notifications'"
printf '\030' >&9
smoke_wait_for "Notify Clear All confirm picker" sh -c \
  "tail -c +$((notify_confirm_offset + 1)) '$recorder_log' | grep -aFq 'Clear all'"
printf '\033[B\r' >&9
smoke_wait_for "Notify confirm command marker" test -f "$notify_confirm_marker"
wait "$notify_confirm_pid"
notify_after_confirm="$PROJMUX_SMOKE_WORKDIR/notify-polish-after-confirm.json"
"$bin" get notifications --json >"$notify_after_confirm"
for severity in info warn critical; do
  if grep -Fq "polish-$severity" "$notify_after_confirm"; then
    echo "Notify Clear All confirm left $severity fixture queued" >&2
    exit 1
  fi
done
if [[ "$(tmux -L "$recorder_socket" display-message -p -c "$recorder_client" '#{pane_id}')" != "$notify_focus_before" ]]; then
  echo "Notify Clear All confirm did not restore the attached client focus" >&2
  exit 1
fi

# Multi-stroke Phase 0 uses the same logical key stream through the terminal
# client and the Darwin broker's `send-keys -K -c` transport. A raw capture
# pane proves Escape and unknown continuation are consumed (zero pane input),
# while two shared-prefix actions prove one dispatch per completed sequence.
sequence_capture="$PROJMUX_SMOKE_WORKDIR/sequence-pane-input.bin"
install -m 0644 "$smoke_root/test/fixtures/keymaps/sequences-v2.toml" "$XDG_CONFIG_HOME/projmux/keymap.toml"
"$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$recorder_socket" \
  >"$PROJMUX_SMOKE_WORKDIR/sequence-e2e-apply.out"
recorder_project_uid="$("$bin" create project --root "$recorder_root" --name "$recorder_session" -o uid)"
"$bin" reconcile resources --socket "$recorder_socket" --materialize-project "uid:$recorder_project_uid" -o json \
  >"$PROJMUX_SMOKE_WORKDIR/sequence-e2e-materialize.json"
recorder_session="$(tmux -L "$recorder_socket" list-sessions -F '#{session_name}|#{@projmux_project_uid}' \
  | awk -F '|' -v uid="$recorder_project_uid" '$2 == uid { print $1 }')"
if [[ -z "$recorder_session" || "$recorder_session" == *$'\n'* ]] || \
  ! tmux -L "$recorder_socket" has-session -t "=$recorder_session" 2>/dev/null; then
  echo "canonical sequence Project materialization did not yield one exact UID-bound Session: uid=$recorder_project_uid session=$recorder_session" >&2
  cat "$PROJMUX_SMOKE_WORKDIR/sequence-e2e-materialize.json" >&2
  tmux -L "$recorder_socket" list-sessions -F '#{session_id}|#{session_name}|#{@projmux_project_uid}|#{@projmux_project_path}' >&2
  exit 1
fi
tmux -L "$recorder_socket" switch-client -c "$recorder_client" -t "=$recorder_session"
recorder_pane="$(tmux -L "$recorder_socket" display-message -p -c "$recorder_client" '#{pane_id}')"
if [[ -z "$(tmux -L "$recorder_socket" show-options -pqv -t "$recorder_pane" @projmux_pane_uid)" ]]; then
  echo "sequence recorder Pane did not acquire a managed Registry identity" >&2
  exit 1
fi
sequence_window="$(tmux -L "$recorder_socket" display-message -p -t "$recorder_pane" '#{window_id}')"
tmux -L "$recorder_socket" respawn-pane -k -t "$recorder_pane" \
  "/bin/sh -c 'stty raw -echo; cat >\"$sequence_capture\"'"
tmux -L "$recorder_socket" select-window -t "$sequence_window"
smoke_wait_for "sequence capture pane" test -e "$sequence_capture"

sequence_client_is_root() {
  [[ "$(tmux -L "$recorder_socket" display-message -p -c "$recorder_client" '#{client_key_table}')" == "root" ]]
}
sequence_capture_is_empty() {
  [[ ! -s "$sequence_capture" ]]
}

# Terminal delivery: both cancel paths return to root and write no byte to the
# raw application pane. Unknown C-x is caught by the generated `Any` binding.
printf '\013\033' >&9
smoke_wait_for "Escape sequence cancel to root" sequence_client_is_root
sequence_capture_is_empty || {
  echo "Escape sequence cancel leaked pane input" >&2
  od -An -tx1 "$sequence_capture" >&2
  exit 1
}
printf '\013\030' >&9
smoke_wait_for "unknown sequence cancel to root" sequence_client_is_root
sequence_capture_is_empty || {
  echo "unknown sequence cancel leaked pane input" >&2
  od -An -tx1 "$sequence_capture" >&2
  exit 1
}

# The terminal stream toggles mouse exactly once. The native broker-equivalent
# logical stream toggles the same action exactly once back to its prior value.
mouse_before="$(tmux -L "$recorder_socket" show-options -gqv mouse)"
printf '\013\015' >&9
mouse_after_terminal=""
smoke_wait_for "terminal sequence action" sh -c \
  "test \"\$(tmux -L '$recorder_socket' show-options -gqv mouse)\" != '$mouse_before'"
mouse_after_terminal="$(tmux -L "$recorder_socket" show-options -gqv mouse)"
tmux -L "$recorder_socket" send-keys -K -c "$recorder_client" C-k Enter
smoke_wait_for "native logical sequence action" sh -c \
  "test \"\$(tmux -L '$recorder_socket' show-options -gqv mouse)\" = '$mouse_before'"
if [[ "$mouse_after_terminal" == "$mouse_before" ]]; then
  echo "terminal sequence did not dispatch mouse.toggle exactly once" >&2
  exit 1
fi

windows_before="$(tmux -L "$recorder_socket" list-windows -t "$recorder_session" -F '#{window_id}' | wc -l)"
printf '\013\027' >&9
smoke_wait_for "shared-prefix window.create action" sh -c \
  "test \"\$(tmux -L '$recorder_socket' list-windows -t '$recorder_session' -F '#{window_id}' | wc -l)\" -eq $((windows_before + 1))"
windows_after="$(tmux -L "$recorder_socket" list-windows -t "$recorder_session" -F '#{window_id}' | wc -l)"
if [[ "$windows_after" -ne $((windows_before + 1)) ]]; then
  echo "window.create sequence dispatched more or less than once: before=$windows_before after=$windows_after" >&2
  exit 1
fi
smoke_wait_for "completed sequence return to root" sequence_client_is_root

smoke_cleanup_tmux_server "$recorder_socket"
wait "$recorder_client_pid" || true
exec 9>&-

# Save, destroy, and replay a shell/startup/agent field matrix through a
# disposable exact tmux server. The Go harness also changes pane-base-index
# between save and restore to prove replay uses returned %pane_id targets.
PROJMUX_REAL_TMUX_TEST=1 go test ./internal/integrations/tmux \
  -run '^TestRealTmuxSessionStateSaveDestroyReplayFieldFidelity$' -count=1

# ---------------------------------------------------------------------------
# Detached Project runtime materialization and Window/Pane create.
#
# The registry file, pre-v2 runtime binding convergence, and the detached split have
# never run against a real tmux server in production, so this block does all
# three end to end on a dedicated exact socket.
#
# Isolation follows the four mandatory conditions: inherited TMUX/TMUX_PANE are
# stripped from every call, the server lives under a run-unique TMUX_TMPDIR with
# its own -L name, the real #{socket_path} is queried and proven to sit inside
# the smoke root, and only that exact socket is killed.
# ---------------------------------------------------------------------------
create_root="$PROJMUX_SMOKE_WORKDIR/create-e2e"
# Canonical resource deletion intentionally inventories the app-owned `projmux`
# socket. TMUX_TMPDIR keeps this exact name isolated below the smoke root.
create_socket="projmux"
create_foreign_socket="projmux-create-foreign-$$-$RANDOM"
mkdir -p "$create_root/tt" "$create_root/state" "$create_root/config" \
  "$create_root/legacy/alpha" "$create_root/legacy/sibling" "$create_root/work/beta"
create_real_tmux="$(command -v tmux)"

ctx() { env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$create_root/tt" "$create_real_tmux" -L "$create_socket" "$@"; }
cfx() { env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$create_root/tt" "$create_real_tmux" -L "$create_foreign_socket" "$@"; }

# projmux shells out to a bare `tmux`, so route it onto this exact socket.
create_shim="$create_root/shim"
mkdir -p "$create_shim"
cat >"$create_shim/tmux" <<CREATE_SHIM
#!/usr/bin/env bash
create_args=("\$@")
if [[ "\${create_args[0]:-}" == "-S" || "\${create_args[0]:-}" == "-L" ]] && [[ \${#create_args[@]} -ge 2 ]]; then
  create_args=("\${create_args[@]:2}")
fi
if [[ "\${PROJMUX_CREATE_FAIL_SPLIT:-}" == "1" && "\${create_args[0]:-}" == "split-window" ]]; then
  echo "injected split failure" >&2
  exit 1
fi
exec env TMUX_TMPDIR=$(printf %q "$create_root/tt") $(printf %q "$create_real_tmux") -L $(printf %q "$create_socket") "\$@"
CREATE_SHIM
chmod 0755 "$create_shim/tmux"

pmx() {
  env -u TMUX -u TMUX_PANE \
    PATH="$create_shim:$PATH" \
    TMUX_TMPDIR="$create_root/tt" \
    XDG_STATE_HOME="$create_root/state" \
    XDG_CONFIG_HOME="$create_root/config" \
    PROJMUX_MANAGED_ROOTS="$create_root/legacy:$create_root/work" \
    SHELL=/bin/sh \
    "$bin" "$@"
}

create_registry="$create_root/state/projmux/metadata/registry.json"
if [[ -e "$create_registry" ]]; then
  echo "create e2e did not start from an empty registry" >&2
  exit 1
fi

# A pre-v2 session for explicit Registry convergence: window_name, automatic
# rename, a user pane label, and a raw pane title must never become the stable
# Window metadata.name. window_name is retained as displayName.
ctx new-session -d -s legacy-alpha -c "$create_root/legacy/alpha" sleep 600
ctx set-option -t legacy-alpha -q @projmux_project_path "$create_root/legacy/alpha"
legacy_window_name_expected="pre-v2-display-sentinel"
ctx set-option -w -t legacy-alpha:0 automatic-rename-format "$legacy_window_name_expected"
ctx set-option -w -t legacy-alpha:0 automatic-rename on
legacy_pane="$(ctx display-message -p -t legacy-alpha '#{pane_id}')"
legacy_window_id="$(ctx display-message -p -t legacy-alpha '#{window_id}')"
ctx set-option -p -t "$legacy_pane" @projmux_pane_label "buildlog"
ctx select-pane -T "raw title must not win" -t "$legacy_pane"
legacy_window_name_before=""
for _ in $(seq 1 200); do
  legacy_window_name_before="$(ctx display-message -p -t "$legacy_window_id" '#{window_name}')"
  if [[ "$legacy_window_name_before" == "$legacy_window_name_expected" ]]; then
    break
  fi
  sleep 0.01
done
if [[ "$legacy_window_name_before" != "$legacy_window_name_expected" ]]; then
  echo "pre-v2 automatic rename did not settle on $legacy_window_name_expected: $legacy_window_name_before" >&2
  exit 1
fi
cfx new-session -d -s foreign-agent -c "$create_root/work/beta" sleep 600
foreign_agent_pane="$(cfx display-message -p -t foreign-agent '#{pane_id}')"
cfx select-pane -T codex -t "$foreign_agent_pane"
cfx set-option -p -t "$foreign_agent_pane" @projmux_ai_topic foreign-sentinel
cfx set-option -p -t "$foreign_agent_pane" @projmux_ai_state foreign-state
foreign_agent_before="$(cfx display-message -p -t "$foreign_agent_pane" '#{pane_title}|#{@projmux_ai_topic}|#{@projmux_ai_state}')"

create_socket_path="$(ctx display-message -p -t legacy-alpha '#{socket_path}')"
create_server_pid="$(ctx display-message -p -t legacy-alpha '#{pid}')"
case "$create_socket_path" in
  "$create_root"/*) ;;
  *)
    echo "create e2e socket escaped the smoke root: $create_socket_path" >&2
    exit 1
    ;;
esac
if [[ ! "$create_server_pid" =~ ^[1-9][0-9]*$ ]]; then
  echo "create e2e server has no exact positive pid: $create_server_pid" >&2
  exit 1
fi
echo ">> create e2e socket=$create_socket path=$create_socket_path pid=$create_server_pid"
create_foreign_socket_path="$(cfx display-message -p -t foreign-agent '#{socket_path}')"
case "$create_foreign_socket_path" in
  "$create_root"/*) ;;
  *)
    echo "foreign create e2e socket escaped the smoke root: $create_foreign_socket_path" >&2
    exit 1
    ;;
esac

# Every following operation is a managed product mutation. Establish the same
# app/logical route markers instead of treating a raw tmux fixture as Projmux-
# owned authority. This block tests synchronous create transaction boundaries,
# so it deliberately omits the generated config's asynchronous hooks; the
# canonical config-apply surface and those hooks are exercised by the recorder.
ctx set-option -gq @projmux_app 1
ctx set-option -gq @projmux_socket_name "$create_socket"

create_cleanup() {
  local actual
  actual="$(ctx display-message -p '#{socket_path}' 2>/dev/null || true)"
  if [[ -n "$actual" ]]; then
    case "$actual" in
      "$create_root"/*)
        env -u TMUX -u TMUX_PANE "$create_real_tmux" -S "$actual" kill-server >/dev/null 2>&1 || true
        ;;
      *)
        echo "refusing create e2e cleanup outside the smoke root: $actual" >&2
        ;;
    esac
  fi

	actual="$(cfx display-message -p '#{socket_path}' 2>/dev/null || true)"
	if [[ -n "$actual" ]]; then
		case "$actual" in
			"$create_root"/*)
				env -u TMUX -u TMUX_PANE "$create_real_tmux" -S "$actual" kill-server >/dev/null 2>&1 || true
				;;
			*)
				echo "refusing foreign create e2e cleanup outside the smoke root: $actual" >&2
				;;
		esac
	fi
}
trap 'create_cleanup; smoke_cleanup_env' EXIT

# 0. D2 is L0 even when the live session carries the old project-path anchor.
# The explicit bootstrap then establishes authority for both Projects; alpha's
# managed-root projection intentionally remains `legacy-alpha`, preserving this
# fixture's existing runtime name while removing the automatic-import premise.
pmx reconcile resources --socket "$create_socket" --dry-run -o json >"$create_root/alpha-d2.json"
smoke_assert_file_contains "$create_root/alpha-d2.json" '"outcome": "no-op"'
if [[ -e "$create_registry" ]]; then
  echo "D2 e2e reconcile created a Registry before explicit authority" >&2
  exit 1
fi
pmx create project --root "$create_root/legacy/alpha" --name alpha >"$create_root/register-alpha.out"
smoke_assert_file_contains "$create_root/register-alpha.out" "project/alpha created"
pmx create project --root "$create_root/work/beta" >"$create_root/register-beta.out"
smoke_assert_file_contains "$create_root/register-beta.out" "project/beta created"
if [[ ! -f "$create_registry" ]]; then
  echo "the explicit Project bootstrap did not write the registry" >&2
  exit 1
fi
if [[ "$(stat -c '%a' "$create_registry")" != "600" ]]; then
  echo "registry mode = $(stat -c '%a' "$create_registry"), want 600" >&2
  exit 1
fi
# Siblings under either discovery root stay unregistered.
pmx get projects -o json >"$create_root/projects-after-bootstrap.json"
if grep -Fq "$create_root/legacy/sibling" "$create_root/projects-after-bootstrap.json"; then
  echo "the explicit bootstrap also registered an alpha sibling candidate" >&2
  exit 1
fi

alpha_window_name="$(pmx get windows --project alpha -o name)"
if [[ -z "$alpha_window_name" ]] || [[ "$(printf '%s\n' "$alpha_window_name" | wc -l)" != "1" ]]; then
  echo "explicit alpha bootstrap did not create exactly one authoritative Window: $alpha_window_name" >&2
  exit 1
fi
alpha_converged=0
for alpha_pass in 1 2 3 4; do
  pmx reconcile resources --socket "$create_socket" -o json >"$create_root/alpha-converge-$alpha_pass.json"
  if grep -Fq '"outcome": "changed"' "$create_root/alpha-converge-$alpha_pass.json"; then
    continue
  fi
  if grep -Fq '"outcome": "no-op"' "$create_root/alpha-converge-$alpha_pass.json"; then
    if [[ "$alpha_pass" == "1" ]]; then
      echo "explicit alpha authority made no initial convergence progress" >&2
      cat "$create_root/alpha-converge-$alpha_pass.json" >&2
      exit 1
    fi
    smoke_assert_file_contains "$create_root/alpha-converge-$alpha_pass.json" '"items": []'
    alpha_converged=1
    break
  fi
  echo "explicit alpha convergence reported neither changed nor no-op on pass $alpha_pass" >&2
  cat "$create_root/alpha-converge-$alpha_pass.json" >&2
  exit 1
done
if [[ "$alpha_converged" != "1" ]]; then
  echo "explicit alpha authority did not converge within four public passes" >&2
  exit 1
fi

# 1. A pre-create hook refusal aborts with zero mutations.
mkdir -p "$create_root/config/projmux"
cat >"$create_root/config/projmux/config.toml" <<'PRECREATE'
[hooks.pre-create]
run = "exit 7"
PRECREATE
create_sessions_before="$(ctx list-sessions -F '#{session_name}' | wc -l)"
cp "$create_registry" "$create_root/precreate.registry"
set +e
pmx create window --project beta >"$create_root/precreate.out" 2>"$create_root/precreate.err"
precreate_status=$?
set -e
if [[ "$precreate_status" == "0" ]]; then
  echo "a pre-create hook refusal still created the Window" >&2
  exit 1
fi
if [[ -s "$create_root/precreate.out" ]]; then
  echo "a pre-create hook refusal wrote to stdout" >&2
  exit 1
fi
cmp "$create_root/precreate.registry" "$create_registry"
if [[ "$(ctx list-sessions -F '#{session_name}' | wc -l)" != "$create_sessions_before" ]]; then
  echo "a pre-create hook refusal materialized a session" >&2
  exit 1
fi
smoke_assert_file_contains "$create_root/precreate.err" "status 7"

# 2. A post-create hook failure is a logged success.
cat >"$create_root/config/projmux/config.toml" <<'POSTCREATE'
[hooks.post-create]
run = "exit 9"
POSTCREATE
pmx create window --project beta >"$create_root/postcreate.out" 2>"$create_root/postcreate.err"
smoke_assert_file_contains "$create_root/postcreate.out" "created"
smoke_assert_file_contains "$create_root/postcreate.err" "post-create"
rm -f "$create_root/config/projmux/config.toml"
if [[ "$(stat -c '%a' "$create_registry")" != "600" ]]; then
  echo "registry mode = $(stat -c '%a' "$create_registry"), want 600" >&2
  exit 1
fi

# A registered Project never adopts a same-name session that appeared outside
# Projmux. Exercise both missing identity and an explicit foreign identity;
# each refusal must precede reconcile, lease, mirror, Registry write, and any
# subsequent Window creation on this exact socket.
phase0_selected_session=""
while IFS= read -r phase0_candidate_session; do
  if [[ "$(ctx show-options -qv -t "$phase0_candidate_session" @projmux_project_path)" == "$create_root/work/beta" ]]; then
    phase0_selected_session="$phase0_candidate_session"
    break
  fi
done < <(ctx list-sessions -F '#{session_id}')
if [[ -z "$phase0_selected_session" ]]; then
  echo "could not resolve beta's exact created session before foreign-session smoke" >&2
  exit 1
fi
phase0_selected_name="$(ctx display-message -p -t "$phase0_selected_session" '#{session_name}')"
ctx kill-session -t "$phase0_selected_session"
for phase0_identity in blank foreign; do
  ctx new-session -d -s "$phase0_selected_name" -c "$create_root/work/beta" sleep 600
  phase0_session="$(ctx display-message -p -t "$phase0_selected_name" '#{session_id}')"
  phase0_window="$(ctx display-message -p -t "$phase0_selected_name" '#{window_id}')"
  phase0_pane="$(ctx display-message -p -t "$phase0_selected_name" '#{pane_id}')"
  ctx set-option -t "$phase0_session" -q @phase0_session_sentinel "$phase0_identity"
  ctx set-option -w -t "$phase0_window" -q @phase0_window_sentinel "$phase0_identity"
  ctx set-option -p -t "$phase0_pane" -q @phase0_pane_sentinel "$phase0_identity"
  if [[ "$phase0_identity" == foreign ]]; then
    ctx set-option -t "$phase0_session" -q @projmux_project_uid prj-foreign
    ctx set-option -t "$phase0_session" -q @projmux_project_path "$create_root/legacy/alpha"
  fi
  cp "$create_registry" "$create_root/phase0-$phase0_identity.registry"
  ctx show-options -t "$phase0_session" >"$create_root/phase0-$phase0_identity.session-options"
  ctx show-options -w -t "$phase0_window" >"$create_root/phase0-$phase0_identity.window-options"
  ctx show-options -p -t "$phase0_pane" >"$create_root/phase0-$phase0_identity.pane-options"
  set +e
  pmx create window --project beta --name "phase0-$phase0_identity" \
    >"$create_root/phase0-$phase0_identity.out" 2>"$create_root/phase0-$phase0_identity.err"
  phase0_status=$?
  set -e
  if [[ "$phase0_status" == 0 ]] || [[ -s "$create_root/phase0-$phase0_identity.out" ]]; then
    echo "$phase0_identity same-name session was reused or wrote stdout" >&2
    exit 1
  fi
  if ! cmp "$create_root/phase0-$phase0_identity.registry" "$create_registry" ||
    ! ctx show-options -t "$phase0_session" | cmp "$create_root/phase0-$phase0_identity.session-options" - ||
    ! ctx show-options -w -t "$phase0_window" | cmp "$create_root/phase0-$phase0_identity.window-options" - ||
    ! ctx show-options -p -t "$phase0_pane" | cmp "$create_root/phase0-$phase0_identity.pane-options" -; then
    echo "$phase0_identity same-name refusal changed Registry or existing tmux options" >&2
    exit 1
  fi
  if [[ "$(ctx list-windows -t "$phase0_session" -F '#{window_id}' | wc -l)" != 1 ]] ||
    [[ "$(ctx list-panes -s -t "$phase0_session" -F '#{pane_id}' | wc -l)" != 1 ]]; then
    echo "$phase0_identity same-name refusal created a Window or Pane" >&2
    exit 1
  fi
  smoke_assert_file_contains "$create_root/phase0-$phase0_identity.err" "refuse"
  ctx kill-session -t "$phase0_session"
done

# 3. Explicit Registry authority keeps its stable Window name independent of
#    every runtime attribute. The managed rebind path (not D2 import) projects
#    window_name as displayName, turns automatic-rename off, and mirrors the
#    allocated uids back.
legacy_window_name="$(ctx display-message -p -t legacy-alpha:0 '#{window_name}')"
if [[ "$legacy_window_name" != "$legacy_window_name_before" ]]; then
  echo "explicit authority runtime display = $legacy_window_name, want preserved $legacy_window_name_before" >&2
  exit 1
fi
if [[ "$(ctx show-options -wqv -t legacy-alpha:0 automatic-rename)" != "off" ]]; then
  echo "explicit authority left automatic-rename on for a managed Window" >&2
  exit 1
fi
if [[ -z "$(ctx display-message -p -t legacy-alpha:0 '#{@projmux_window_uid}')" ]]; then
  echo "explicit authority did not mirror the Window uid" >&2
  exit 1
fi
if [[ -z "$(ctx display-message -p -t "$legacy_pane" '#{@projmux_pane_uid}')" ]]; then
  echo "explicit authority did not mirror the Pane uid" >&2
  exit 1
fi
pmx get windows --project alpha -o name >"$create_root/alpha-windows.out"
smoke_assert_file_contains "$create_root/alpha-windows.out" "$alpha_window_name"
if [[ "$legacy_window_name_before" == "$alpha_window_name" ]]; then
  echo "legacy Window display fixture did not differ from its stable name" >&2
  exit 1
fi
pmx get windows --project alpha >"$create_root/alpha-windows-table.out"
# Tabular spacing expands when the display sentinel is wider than the header;
# the parser below locates the stable NAME column from the header instead of
# assuming the minimum two-space gap.
smoke_assert_file_contains "$create_root/alpha-windows-table.out" "DISPLAY NAME"
if ! awk -v display="$legacy_window_name_before" -v stable="$alpha_window_name" '
  NR == 1 {
    name_column = index($0, "  NAME  ") + 2
    next
  }
  NR == 2 {
    display_cell = substr($0, 1, name_column - 3)
    sub(/[[:space:]]+$/, "", display_cell)
    name_cell = substr($0, name_column)
    sub(/[[:space:]].*$/, "", name_cell)
    found = display_cell == display && name_cell == stable
  }
  END { exit !found }
' "$create_root/alpha-windows-table.out"; then
  echo "legacy Window table did not show displayName first and stable name second:" >&2
  cat "$create_root/alpha-windows-table.out" >&2
  exit 1
fi
pmx describe window "$alpha_window_name" -p alpha >"$create_root/alpha-window.describe"
smoke_assert_file_contains "$create_root/alpha-window.describe" "DisplayName:"
smoke_assert_file_contains "$create_root/alpha-window.describe" "$legacy_window_name_before"

# Canonical focus resolves the short Project/Window scopes on this exact
# guarded socket. This server deliberately has no attached client, so success
# is the observable notify-only dispatch rather than a fabricated focus move.
PROJMUX_NOTIFY_HOOK=/bin/true pmx focus pane "$legacy_pane" -p legacy-alpha -w "$legacy_window_id" --json \
  >"$create_root/short-focus.json"
smoke_assert_file_contains "$create_root/short-focus.json" '"ok":true'
smoke_assert_file_contains "$create_root/short-focus.json" '"dispatch":"notify-only"'
smoke_assert_file_contains "$create_root/short-focus.json" "legacy-alpha:$legacy_window_id.$legacy_pane"

# The explicit repair route is now a convergent repeat over already-established
# Registry authority before subsequent creates select it.
pmx reconcile resources --socket "$create_socket" -o json >"$create_root/alpha-reconcile.json"
if [[ -z "$(ctx show-options -qv -t legacy-alpha @projmux_project_uid)" ]]; then
  echo "explicit resource reconcile did not establish legacy-alpha Project ownership" >&2
  cat "$create_root/alpha-reconcile.json" >&2
  exit 1
fi

# tmux 3.4 reports one @N/%N row for every session that links the Window. A
# pre-existing linked Window must remain one handle with an owner set, while a
# newly returned @N/%N is still uniquely attributable to legacy-alpha.
ctx new-session -d -s linked-observer -c "$create_root/legacy/alpha" sleep 600
ctx link-window -s "$legacy_window_id" -t linked-observer:
linked_window_uid_before="$(ctx show-options -wqv -t "$legacy_window_id" @projmux_window_uid)"
linked_pane_uid_before="$(ctx show-options -pqv -t "$legacy_pane" @projmux_pane_uid)"
pmx create window --project alpha --name linked-owner-set >"$create_root/linked-owner-set.out"
linked_owner_set_pane="$(ctx display-message -p -t legacy-alpha:linked-owner-set '#{pane_id}')"
if [[ "$(head -c 7 "$create_root/linked-owner-set.out")" != "window/" ]] ||
  [[ -z "$(ctx show-options -wqv -t legacy-alpha:linked-owner-set @projmux_window_uid)" ]] ||
  [[ -z "$(ctx show-options -pqv -t "$linked_owner_set_pane" @projmux_pane_uid)" ]]; then
  echo "linked-window owner rows prevented exact new Window attribution" >&2
  cat "$create_root/linked-owner-set.out" >&2
  exit 1
fi
if [[ "$(ctx show-options -wqv -t "$legacy_window_id" @projmux_window_uid)" != "$linked_window_uid_before" ]] ||
  [[ "$(ctx show-options -pqv -t "$legacy_pane" @projmux_pane_uid)" != "$linked_pane_uid_before" ]]; then
  echo "linked-window attribution changed pre-existing identity" >&2
  exit 1
fi
ctx kill-session -t linked-observer

# 4. right and down produce the two split axes, detached, with no focus change.
#    The create runs inside a real pane and the completion signal is the exit
#    code marker the runner writes, never pane contents and never send-keys.
create_markers="$create_root/markers"
mkdir -p "$create_markers"
run_in_pane() {
  local label="$1"
  shift
  local script="$create_root/run-$label.sh"
  {
    printf '#!/usr/bin/env bash\n'
    # shellcheck disable=SC2016 # $PATH must stay unexpanded: it is resolved
    # inside the generated runner, not here.
    printf 'PATH=%q:$PATH \\\n' "$create_shim"
    printf '  TMUX_TMPDIR=%q \\\n' "$create_root/tt"
    printf '  XDG_STATE_HOME=%q \\\n' "$create_root/state"
    printf '  XDG_CONFIG_HOME=%q \\\n' "$create_root/config"
    printf '  PROJMUX_MANAGED_ROOTS=%q \\\n' "$create_root/work"
    printf '  SHELL=/bin/sh \\\n'
    printf '  %q' "$bin"
    printf ' %q' "$@"
    printf ' >%q 2>%q\n' "$create_root/run-$label.out" "$create_root/run-$label.err"
    printf 'printf "%%s" "$?" >%q\n' "$create_markers/$label.code"
  } >"$script"
  chmod 0755 "$script"
  ctx new-window -d -t legacy-alpha: -n "runner-$label" "$script"
  for _ in {1..200}; do
    if [[ -f "$create_markers/$label.code" ]]; then
      return 0
    fi
    sleep 0.05
  done
  echo "timed out waiting for the $label runner exit-code marker" >&2
  cat "$create_root/run-$label.err" >&2 || true
  return 1
}

create_active_window_before="$(ctx display-message -p -t legacy-alpha '#{window_id}')"
create_active_pane_before="$(ctx display-message -p -t legacy-alpha '#{pane_id}')"
create_panes_before="$(ctx list-panes -t "$legacy_window_id" -F '#{pane_id}' | wc -l)"

run_in_pane right create pane --project alpha --window "$alpha_window_name" --placement right -o pane-id
run_in_pane down create pane --project alpha --window "$alpha_window_name" --placement down -o pane-id
for label in right down; do
  if [[ "$(cat "$create_markers/$label.code")" != "0" ]]; then
    echo "create pane --placement $label exited $(cat "$create_markers/$label.code")" >&2
    cat "$create_root/run-$label.err" >&2 || true
    exit 1
  fi
done
right_pane="$(tr -d '[:space:]' <"$create_root/run-right.out")"
down_pane="$(tr -d '[:space:]' <"$create_root/run-down.out")"
if [[ ! "$right_pane" =~ ^%[0-9]+$ ]] || [[ ! "$down_pane" =~ ^%[0-9]+$ ]]; then
  echo "expected raw %N pane ids, got right=$right_pane down=$down_pane" >&2
  exit 1
fi
create_panes_after="$(ctx list-panes -t "$legacy_window_id" -F '#{pane_id}' | wc -l)"
if [[ "$create_panes_after" != "$((create_panes_before + 2))" ]]; then
  echo "expected two new panes, got $create_panes_before -> $create_panes_after" >&2
  exit 1
fi
# right splits on the horizontal axis (the new pane starts to the right of the
# anchor); down splits on the vertical axis (same column, lower row).
anchor_left="$(ctx display-message -p -t "$legacy_pane" '#{pane_left}')"
anchor_top="$(ctx display-message -p -t "$legacy_pane" '#{pane_top}')"
right_left="$(ctx display-message -p -t "$right_pane" '#{pane_left}')"
down_left="$(ctx display-message -p -t "$down_pane" '#{pane_left}')"
down_top="$(ctx display-message -p -t "$down_pane" '#{pane_top}')"
if [[ "$right_left" -le "$anchor_left" ]]; then
  echo "--placement right did not split horizontally: anchor_left=$anchor_left new_left=$right_left" >&2
  exit 1
fi
if [[ "$down_left" != "$anchor_left" ]] || [[ "$down_top" -le "$anchor_top" ]]; then
  echo "--placement down did not split vertically: anchor=($anchor_left,$anchor_top) new=($down_left,$down_top)" >&2
  exit 1
fi
# The client never moved: the session's active window and active pane are the
# ones that were active before either split, and neither new pane is active.
if [[ "$(ctx display-message -p -t legacy-alpha '#{window_id}')" != "$create_active_window_before" ]] ||
  [[ "$(ctx display-message -p -t legacy-alpha '#{pane_id}')" != "$create_active_pane_before" ]]; then
  echo "a detached create moved the active window or pane" >&2
  exit 1
fi
for pane in "$right_pane" "$down_pane"; do
  if [[ "$(ctx display-message -p -t "$pane" '#{?pane_active,1,0}')" != "0" ]]; then
    echo "detached split left $pane active" >&2
    exit 1
  fi
done

# Repeated same-axis canonical creates converge within one cell and do not
# disturb a different Window. Each ensured Window starts from its own primary
# Pane, so the measurement is isolated from the mixed topology above.
ctx list-panes -t "$legacy_window_id" \
  -F '#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}' \
  >"$create_root/unrelated-before.geometry"

pmx create pane --project alpha --window even-right --create-window --placement right -o pane-id \
  >"$create_root/even-right-1.out"
pmx create pane --project alpha --window even-right --placement right -o pane-id \
  >"$create_root/even-right-2.out"
pmx create pane --project alpha --window even-right --placement right -o pane-id \
  >"$create_root/even-right-3.out"

pmx create pane --project alpha --window even-down --create-window --placement down -o pane-id \
  >"$create_root/even-down-1.out"
pmx create pane --project alpha --window even-down --placement down -o pane-id \
  >"$create_root/even-down-2.out"
pmx create pane --project alpha --window even-down --placement down -o pane-id \
  >"$create_root/even-down-3.out"

ctx list-panes -t legacy-alpha:even-right -F '#{pane_width}' >"$create_root/even-right.widths"
if ! awk '
  NR == 1 { min=$1; max=$1 }
  { if ($1 < min) min=$1; if ($1 > max) max=$1 }
  END { exit !(NR == 4 && max-min <= 1) }
' "$create_root/even-right.widths"; then
  echo "repeated right creates did not converge within one cell:" >&2
  cat "$create_root/even-right.widths" >&2
  exit 1
fi

ctx list-panes -t legacy-alpha:even-down -F '#{pane_height}' >"$create_root/even-down.heights"
if ! awk '
  NR == 1 { min=$1; max=$1 }
  { if ($1 < min) min=$1; if ($1 > max) max=$1 }
  END { exit !(NR == 4 && max-min <= 1) }
' "$create_root/even-down.heights"; then
  echo "repeated down creates did not converge within one cell:" >&2
  cat "$create_root/even-down.heights" >&2
  exit 1
fi

for output in "$create_root"/even-{right,down}-{1,2,3}.out; do
  pane="$(tr -d '[:space:]' <"$output")"
  if [[ ! "$pane" =~ ^%[0-9]+$ ]] ||
    [[ -z "$(ctx display-message -p -t "$pane" '#{@projmux_pane_uid}')" ]]; then
    echo "same-axis create lost pane identity: output=$output pane=$pane" >&2
    exit 1
  fi
  if [[ "$(ctx display-message -p -t "$pane" '#{?pane_active,1,0}')" != "0" ]]; then
    echo "same-axis create left $pane active" >&2
    exit 1
  fi
done

if [[ "$(ctx display-message -p -t legacy-alpha '#{window_id}')" != "$create_active_window_before" ]] ||
  [[ "$(ctx display-message -p -t legacy-alpha '#{pane_id}')" != "$create_active_pane_before" ]]; then
  echo "same-axis creates moved the active window or pane" >&2
  exit 1
fi

ctx list-panes -t "$legacy_window_id" \
  -F '#{pane_id}\t#{pane_left}\t#{pane_top}\t#{pane_width}\t#{pane_height}' \
  >"$create_root/unrelated-after.geometry"
if ! cmp "$create_root/unrelated-before.geometry" "$create_root/unrelated-after.geometry"; then
  echo "same-axis create changed unrelated Window topology" >&2
  exit 1
fi

# Concurrent same-shape creates serialize through the Registry lock and retain
# exact transport identity: one stable Window, one primary Pane, and one exact
# claimed Pane per racer, with the pre-existing Window/Pane bindings unchanged.
phase0_stress_window_uid_before="$(ctx show-options -wqv -t "$legacy_window_id" @projmux_window_uid)"
phase0_stress_pane_uid_before="$(ctx show-options -pqv -t "$legacy_pane" @projmux_pane_uid)"
phase0_stress_project_uid_before="$(ctx show-options -qv -t legacy-alpha @projmux_project_uid)"
phase0_stress_pids=()
for phase0_racer in {1..8}; do
  pmx create pane --project alpha --window phase0-stress --create-window -o pane-id \
    >"$create_root/phase0-stress-$phase0_racer.out" \
    2>"$create_root/phase0-stress-$phase0_racer.err" &
  phase0_stress_pids+=("$!")
done
phase0_stress_failed=0
for phase0_racer in {1..8}; do
  if ! wait "${phase0_stress_pids[$((phase0_racer - 1))]}"; then
    phase0_stress_failed=1
    cat "$create_root/phase0-stress-$phase0_racer.err" >&2 || true
  fi
done
if [[ "$phase0_stress_failed" != 0 ]]; then
  echo "concurrent exact-socket create stress had a failed racer" >&2
  exit 1
fi
if [[ "$(ctx list-windows -t legacy-alpha -F '#{window_name}' | grep -cx phase0-stress)" != 1 ]] ||
  [[ "$(ctx list-panes -t legacy-alpha:phase0-stress -F '#{pane_id}' | wc -l)" != 9 ]]; then
  echo "concurrent create stress did not converge on one Window with nine Panes" >&2
  exit 1
fi
for phase0_racer in {1..8}; do
  phase0_stress_pane="$(tr -d '[:space:]' <"$create_root/phase0-stress-$phase0_racer.out")"
  if [[ ! "$phase0_stress_pane" =~ ^%[0-9]+$ ]] ||
    [[ -z "$(ctx show-options -pqv -t "$phase0_stress_pane" @projmux_pane_uid)" ]]; then
    echo "concurrent create stress lost exact Pane attribution: racer=$phase0_racer pane=$phase0_stress_pane" >&2
    exit 1
  fi
done
if [[ "$(ctx list-panes -t legacy-alpha:phase0-stress -F '#{@projmux_pane_uid}' | sed '/^$/d' | sort -u | wc -l)" != 9 ]] ||
  [[ -z "$(ctx show-options -wqv -t legacy-alpha:phase0-stress @projmux_window_uid)" ]]; then
  echo "concurrent create stress produced blank or duplicate Registry mirrors" >&2
  exit 1
fi
if [[ "$(ctx show-options -wqv -t "$legacy_window_id" @projmux_window_uid)" != "$phase0_stress_window_uid_before" ]] ||
  [[ "$(ctx show-options -pqv -t "$legacy_pane" @projmux_pane_uid)" != "$phase0_stress_pane_uid_before" ]] ||
  [[ "$(ctx show-options -qv -t legacy-alpha @projmux_project_uid)" != "$phase0_stress_project_uid_before" ]]; then
  echo "concurrent create stress contaminated a pre-existing identity" >&2
  exit 1
fi

# 5. --create-window is the opt-in Window ensure, and the result is still a Pane.
pmx create pane --project alpha --window review --create-window -o ref >"$create_root/ensure.out"
if [[ "$(head -c 5 "$create_root/ensure.out")" != "pane/" ]]; then
  echo "--create-window returned $(cat "$create_root/ensure.out"), want a pane/ ref" >&2
  exit 1
fi
if [[ "$(ctx list-windows -t legacy-alpha -F '#{window_name}' | grep -cx review)" != "1" ]]; then
  echo "--create-window did not converge on exactly one review Window" >&2
  exit 1
fi

# 6. A stale anchorPaneRef puts the persisted Registry in degraded mode: the
#    ordinary create is exit 1 with exact repair guidance, zero mutations, and
#    zero stdout.
cp "$create_registry" "$create_root/registry.intact"
# The registry is written with MarshalIndent, so every anchorPaneRef sits alone
# on its own line and the substitution cannot reach any other field.
sed -i 's/"anchorPaneRef": "pane-[a-z0-9]*"/"anchorPaneRef": "pane-doesnotexist"/' "$create_registry"
if ! grep -Fq '"anchorPaneRef": "pane-doesnotexist"' "$create_registry"; then
  echo "the stale anchorPaneRef fixture did not apply" >&2
  exit 1
fi
stale_before="$(md5sum "$create_registry" | cut -d' ' -f1)"
stale_panes_before="$(ctx list-panes -s -t legacy-alpha -F '#{pane_id}' | wc -l)"
set +e
pmx create pane --project alpha --window review >"$create_root/stale.out" 2>"$create_root/stale.err"
stale_status=$?
set -e
if [[ "$stale_status" != "1" ]]; then
  echo "stale anchorPaneRef exit = $stale_status, want degraded-mode exit 1" >&2
  cat "$create_root/stale.err" >&2 || true
  exit 1
fi
if [[ -s "$create_root/stale.out" ]]; then
  echo "stale anchorPaneRef wrote to stdout" >&2
  exit 1
fi
if [[ "$(md5sum "$create_registry" | cut -d' ' -f1)" != "$stale_before" ]]; then
  echo "stale anchorPaneRef mutated the registry" >&2
  exit 1
fi
if [[ "$(ctx list-panes -s -t legacy-alpha -F '#{pane_id}' | wc -l)" != "$stale_panes_before" ]]; then
  echo "stale anchorPaneRef fell back to a live pane" >&2
  exit 1
fi
smoke_assert_file_contains "$create_root/stale.err" "anchorPaneRef"
smoke_assert_file_contains "$create_root/stale.err" "resource registry is in degraded mode"
smoke_assert_file_contains "$create_root/stale.err" "run exactly: projmux reconcile registry --dry-run"
cp "$create_root/registry.intact" "$create_registry"

# 7. A tmux failure after the Window exists rolls the operation back to exactly
#    the state it started from.
rollback_windows_before="$(ctx list-windows -t legacy-alpha -F '#{window_id}' | wc -l)"
rollback_registry_before="$(md5sum "$create_registry" | cut -d' ' -f1)"
set +e
env PROJMUX_CREATE_FAIL_SPLIT=1 \
  PATH="$create_shim:$PATH" \
  TMUX_TMPDIR="$create_root/tt" \
  XDG_STATE_HOME="$create_root/state" \
  XDG_CONFIG_HOME="$create_root/config" \
  PROJMUX_MANAGED_ROOTS="$create_root/legacy:$create_root/work" \
  SHELL=/bin/sh \
  env -u TMUX -u TMUX_PANE "$bin" create pane --project alpha --window rollback --create-window \
  >"$create_root/rollback.out" 2>"$create_root/rollback.err"
rollback_status=$?
set -e
if [[ "$rollback_status" == "0" ]]; then
  echo "the injected split failure did not fail the create" >&2
  exit 1
fi
if [[ -s "$create_root/rollback.out" ]]; then
  echo "a rolled back create wrote to stdout" >&2
  exit 1
fi
if [[ "$(ctx list-windows -t legacy-alpha -F '#{window_id}' | wc -l)" != "$rollback_windows_before" ]]; then
  echo "rollback left the Window it created behind" >&2
  ctx list-windows -t legacy-alpha -F '#{window_index} #{window_name}' >&2
  exit 1
fi
if [[ "$(md5sum "$create_registry" | cut -d' ' -f1)" != "$rollback_registry_before" ]]; then
  echo "a rolled back create committed registry state" >&2
  exit 1
fi
smoke_assert_file_contains "$create_root/rollback.err" "injected split failure"

# 8. Agent create composition: a detached Agent split that really launches the
#    provider, allocates a Window-owned Agent plus its managed Pane, never
#    reuses an existing Agent, and never moves the client.
#
#    The provider is a stub binary rather than a real agent CLI. That is the
#    point of the check: the launch construction, the detached split, and the
#    managed-pane binding are what this Phase owns, and a stub proves all three
#    ran by leaving a marker with its own cwd and argv.
agent_home="$create_root/agent-home"
mkdir -p "$agent_home/.local/bin"
cat >"$agent_home/.local/bin/codex" <<AGENT_STUB
#!/usr/bin/env bash
{
  printf 'cwd=%s\n' "\$PWD"
  printf 'args=%s\n' "\$*"
} >>$(printf %q "$create_root/agent-launch.log")
# A payload-bearing create now requires bounded provider/hook acknowledgement.
# Model the public hook path itself so acknowledgement is committed through the
# Agent Registry authority, without reading or synthesizing pane content.
if [[ " \$* " == *" --topic release triage "* ]]; then
  # The provider can start before the create transaction commits its exact Pane
  # binding. Wait for that durable identity instead of guessing how long the
  # typed plan/guard transaction takes under concurrent-create load.
  [[ "\${TMUX_PANE:-}" =~ ^%[0-9]+$ ]] || exit 1
  [[ -n "\${PMX_INTERNAL_ACTIVATION_PANE_UID:-}" ]] || exit 1
  [[ -n "\${PMX_INTERNAL_ACTIVATION_GENERATION:-}" ]] || exit 1
  activation_ready=0
  for _ in {1..200}; do
    if HOME=$(printf %q "$agent_home") \
      PATH=$(printf %q "$create_shim:$agent_home/.local/bin"):\$PATH \
      TMUX_TMPDIR=$(printf %q "$create_root/tt") \
      XDG_STATE_HOME=$(printf %q "$create_root/state") \
      XDG_CONFIG_HOME=$(printf %q "$create_root/config") \
      PROJMUX_MANAGED_ROOTS=$(printf %q "$create_root/legacy:$create_root/work") \
      $(printf %q "$bin") describe pane "uid:\$PMX_INTERNAL_ACTIVATION_PANE_UID" -o uid \
      >$(printf %q "$create_root/agent-activation-poll.out") 2>/dev/null &&
      [[ "\$(tr -d '[:space:]' <$(printf %q "$create_root/agent-activation-poll.out"))" == "\$PMX_INTERNAL_ACTIVATION_PANE_UID" ]]; then
      activation_ready=1
      break
    fi
    sleep 0.05
  done
  [[ "\$activation_ready" == "1" ]] || exit 1
  printf '{"hook_event_name":"UserPromptSubmit","thread_id":"phase6-activation-thread","session_id":"phase6-activation-session","cwd":"%s"}' "\$PWD" |
    HOME=$(printf %q "$agent_home") \
    PATH=$(printf %q "$create_shim:$agent_home/.local/bin"):\$PATH \
    TMUX_TMPDIR=$(printf %q "$create_root/tt") \
    XDG_STATE_HOME=$(printf %q "$create_root/state") \
    XDG_CONFIG_HOME=$(printf %q "$create_root/config") \
    PROJMUX_MANAGED_ROOTS=$(printf %q "$create_root/legacy:$create_root/work") \
    $(printf %q "$bin") internal agent-hook ingest codex-hook
fi
# Stay alive so the pane survives long enough to be inspected.
exec sleep 600
AGENT_STUB
chmod 0755 "$agent_home/.local/bin/codex"

pmx_agent() {
  env -u TMUX -u TMUX_PANE \
    PATH="$create_shim:$agent_home/.local/bin:$PATH" \
    HOME="$agent_home" \
    TMUX_TMPDIR="$create_root/tt" \
    XDG_STATE_HOME="$create_root/state" \
    XDG_CONFIG_HOME="$create_root/config" \
    PROJMUX_MANAGED_ROOTS="$create_root/legacy:$create_root/work" \
    SHELL=/bin/bash \
    "$bin" "$@"
}

pmx_agent_live() {
  PATH="$agent_home/.local/bin:$PATH" \
    HOME="$agent_home" \
    TMUX="$create_socket_path,$create_server_pid,0" \
    TMUX_PANE="$agent_pane" \
    TMUX_TMPDIR="$create_root/tt" \
    XDG_STATE_HOME="$create_root/state" \
    XDG_CONFIG_HOME="$create_root/config" \
    PROJMUX_MANAGED_ROOTS="$create_root/legacy:$create_root/work" \
    SHELL=/bin/bash \
    "$bin" "$@"
}

pmx_agent_hook_at() {
  local hook_pane="$1"
  shift
  PATH="$agent_home/.local/bin:$PATH" \
    HOME="$agent_home" \
    TMUX="$create_socket_path,$create_server_pid,0" \
    TMUX_PANE="$hook_pane" \
    TMUX_TMPDIR="$create_root/tt" \
    XDG_STATE_HOME="$create_root/state" \
    XDG_CONFIG_HOME="$create_root/config" \
    PROJMUX_MANAGED_ROOTS="$create_root/legacy:$create_root/work" \
    SHELL=/bin/bash \
    "$bin" "$@"
}

agent_session_before="$(ctx display-message -p -t legacy-alpha '#{session_id}')"
agent_window_before="$(ctx display-message -p -t legacy-alpha '#{window_id}')"
agent_pane_before="$(ctx display-message -p -t legacy-alpha '#{pane_id}')"
agent_window_uid_before="$(ctx show-options -wqv -t "$agent_window_before" @projmux_window_uid)"
agent_host_pane_uid_before="$(ctx show-options -pqv -t "$agent_pane_before" @projmux_pane_uid)"
agent_primary_pane_uid="$(pmx_agent describe window "$alpha_window_name" -p alpha -o json | sed -n 's/.*"defaultShellPaneRef": "\([^"]*\)".*/\1/p' | head -n 1)"
if [[ ! "$agent_session_before" =~ ^\$[0-9]+$ ]] ||
  [[ ! "$agent_window_before" =~ ^@[0-9]+$ ]] ||
  [[ ! "$agent_pane_before" =~ ^%[0-9]+$ ]] ||
  [[ -z "$agent_window_uid_before" || -z "$agent_host_pane_uid_before" ]] ||
  [[ "$agent_host_pane_uid_before" != "$agent_primary_pane_uid" ]]; then
  echo "Agent fixture has no exact managed primary host receipt: $agent_session_before/$agent_window_before/$agent_pane_before window_uid=$agent_window_uid_before pane_uid=$agent_host_pane_uid_before primary=$agent_primary_pane_uid" >&2
  exit 1
fi

pmx_agent create agent --provider codex -p alpha -w "$alpha_window_name" -o pane-id \
  >"$create_root/agent.out" 2>"$create_root/agent.err"
agent_pane="$(tr -d '[:space:]' <"$create_root/agent.out")"
if [[ ! "$agent_pane" =~ ^%[0-9]+$ ]]; then
  echo "create agent -o pane-id = $agent_pane, want a raw %N handle" >&2
  cat "$create_root/agent.err" >&2 || true
  exit 1
fi

# The provider really launched inside the pane the create made.
for _ in {1..200}; do
  [[ -s "$create_root/agent-launch.log" ]] && break
  sleep 0.05
done
if ! grep -Fq "cwd=$create_root/legacy/alpha" "$create_root/agent-launch.log"; then
  echo "the provider stub did not launch in the project root" >&2
  cat "$create_root/agent-launch.log" >&2 || true
  exit 1
fi

# The managed pane carries both identities: the Projmux Pane uid mirror and the
# AI managed-pane options the statusbar and attention tracker read.
if [[ -z "$(ctx display-message -p -t "$agent_pane" '#{@projmux_pane_uid}')" ]]; then
  echo "the managed Agent pane has no Projmux uid mirror" >&2
  exit 1
fi
if [[ "$(ctx display-message -p -t "$agent_pane" '#{@projmux_ai_managed}')" != "1" ]]; then
  echo "the managed Agent pane is not marked as an AI pane" >&2
  exit 1
fi
if [[ "$(ctx display-message -p -t "$agent_pane" '#{@projmux_ai_agent}')" != "codex" ]]; then
  echo "the managed Agent pane does not record its provider" >&2
  exit 1
fi

# The client never moved and the new pane is not active.
if [[ "$(ctx display-message -p -t legacy-alpha '#{window_id}')" != "$agent_window_before" ]] ||
  [[ "$(ctx display-message -p -t legacy-alpha '#{pane_id}')" != "$agent_pane_before" ]]; then
  echo "a detached Agent create moved the active window or pane" >&2
  exit 1
fi
if [[ "$(ctx display-message -p -t "$agent_pane" '#{?pane_active,1,0}')" != "0" ]]; then
  echo "the detached Agent split left $agent_pane active" >&2
  exit 1
fi

# 9. The shortcut normalizes onto the same route, and a second create allocates a
#    new Agent instead of reusing the first.
pmx_agent create codex -p alpha -w "$alpha_window_name" -o name >"$create_root/agent-second.out"
if [[ "$(tr -d '[:space:]' <"$create_root/agent-second.out")" != "codex-1" ]]; then
  echo "the second Agent name = $(cat "$create_root/agent-second.out"), want codex-1" >&2
  exit 1
fi
pmx_agent get agents -p alpha -o name >"$create_root/agent-list.out"
for want in codex codex-1; do
  if ! grep -qx "$want" "$create_root/agent-list.out"; then
    echo "get agents is missing $want:" >&2
    cat "$create_root/agent-list.out" >&2
    exit 1
  fi
done

# 10. The payload after -- reaches the provider and never the naming.
: >"$create_root/agent-launch.log"
pmx_agent create agent --provider codex -p alpha -w "$alpha_window_name" -o name \
  -- -p payload-project -w payload-window --topic "release triage" >"$create_root/agent-payload.out"
if [[ "$(tr -d '[:space:]' <"$create_root/agent-payload.out")" != "codex-2" ]]; then
  echo "a payload changed the Agent name: $(cat "$create_root/agent-payload.out")" >&2
  exit 1
fi
for _ in {1..200}; do
  [[ -s "$create_root/agent-launch.log" ]] && break
  sleep 0.05
done
if ! grep -Fq -- "args=-C $create_root/legacy/alpha -p payload-project -w payload-window --topic release triage" "$create_root/agent-launch.log"; then
  echo "the payload did not reach the provider:" >&2
  cat "$create_root/agent-launch.log" >&2 || true
  exit 1
fi

# 11. A missing provider is exit 2 with zero mutations and zero stdout.
agent_registry_before="$(md5sum "$create_registry" | cut -d' ' -f1)"
set +e
pmx_agent create agent --project alpha --window "$alpha_window_name" \
  >"$create_root/agent-noprovider.out" 2>"$create_root/agent-noprovider.err"
agent_noprovider_status=$?
set -e
if [[ "$agent_noprovider_status" != "2" ]]; then
  echo "create agent without --provider exit = $agent_noprovider_status, want 2" >&2
  cat "$create_root/agent-noprovider.err" >&2 || true
  exit 1
fi
if [[ -s "$create_root/agent-noprovider.out" ]]; then
  echo "create agent without --provider wrote to stdout" >&2
  exit 1
fi
if [[ "$(md5sum "$create_registry" | cut -d' ' -f1)" != "$agent_registry_before" ]]; then
  echo "create agent without --provider mutated the registry" >&2
  exit 1
fi
smoke_assert_file_contains "$create_root/agent-noprovider.err" "requires --provider"

# 12. An explicit name that collides inside the target Window is exit 2 with no
#     implicit suffix and no new pane.
agent_panes_before="$(ctx list-panes -s -t legacy-alpha -F '#{pane_id}' | wc -l)"
set +e
pmx_agent create agent --provider codex --project alpha --window "$alpha_window_name" --name codex \
  >"$create_root/agent-collide.out" 2>"$create_root/agent-collide.err"
agent_collide_status=$?
set -e
if [[ "$agent_collide_status" != "2" ]]; then
  echo "an explicit Agent name collision exit = $agent_collide_status, want 2" >&2
  cat "$create_root/agent-collide.err" >&2 || true
  exit 1
fi
if [[ -s "$create_root/agent-collide.out" ]]; then
  echo "an explicit Agent name collision wrote to stdout" >&2
  exit 1
fi
if [[ "$(md5sum "$create_registry" | cut -d' ' -f1)" != "$agent_registry_before" ]]; then
  echo "an explicit Agent name collision mutated the registry" >&2
  exit 1
fi
if [[ "$(ctx list-panes -s -t legacy-alpha -F '#{pane_id}' | wc -l)" != "$agent_panes_before" ]]; then
  echo "an explicit Agent name collision created a pane" >&2
  exit 1
fi

# 13. Outside tmux an omitted --project is a refusal, not a fallback. There is
#     no runtime-only split left to reach and no server to guess, so the command
#     names the flag and mutates nothing.
agent_outside_registry_before="$(md5sum "$create_registry" | cut -d' ' -f1)"
set +e
pmx_agent create agent --provider codex -o uid \
  >"$create_root/agent-outside.out" 2>"$create_root/agent-outside.err"
agent_outside_status=$?
set -e
if [[ "$agent_outside_status" != "2" ]]; then
  echo "an outside-tmux create with no --project exit = $agent_outside_status, want 2" >&2
  cat "$create_root/agent-outside.err" >&2 || true
  exit 1
fi
if [[ -s "$create_root/agent-outside.out" ]]; then
  echo "an outside-tmux create with no --project wrote to stdout" >&2
  exit 1
fi
if [[ "$(md5sum "$create_registry" | cut -d' ' -f1)" != "$agent_outside_registry_before" ]]; then
  echo "an outside-tmux create with no --project mutated the registry" >&2
  exit 1
fi
# The needle deliberately starts with a word: the helper passes it to grep as a
# pattern, so a leading `--` would be read as an option.
smoke_assert_file_contains "$create_root/agent-outside.err" "not inside a tmux client"
smoke_assert_file_contains "$create_root/agent-outside.err" "pass --project <ref>"

# 14. Phase 6 Agent authority runs on the inherited absolute socket only. The
#     foreign socket deliberately carries matching title-like text and semantic
#     options; no selector or mirror may infer identity from them or touch it.
agent_uid="$(pmx_agent get agents --project alpha -o uid | head -n 1)"
agent_pane_uid="$(ctx display-message -p -t "$agent_pane" '#{@projmux_pane_uid}')"
if [[ -z "$agent_uid" || -z "$agent_pane_uid" ]]; then
  echo "Phase 6 e2e could not resolve Agent/Pane uid" >&2
  exit 1
fi
ctx select-pane -T foreign-sentinel -t "$agent_pane"
pmx_agent_live agent topic set "canonical topic" "uid:$agent_uid"
pmx_agent_live agent status set approval_required "uid:$agent_uid"
if [[ "$(ctx show-options -pqv -t "$agent_pane" @projmux_ai_topic)" != "canonical topic" ]] ||
  [[ "$(ctx show-options -pqv -t "$agent_pane" @projmux_ai_badge_kind)" != approval_required ]]; then
  echo "canonical Agent topic/status did not project immediately" >&2
  exit 1
fi

# Drive each provider through the sole canonical ingress on an unbound shell.
# State remains a transient hook observation, while the managed/provider and
# launch-authorship options stay absent so the hook cannot mint topology.
claude_hook_pane="$(ctx split-window -d -P -F '#{pane_id}' -t legacy-alpha -c "$create_root/legacy/alpha" sleep 600)"
printf '%s' '{"hook_event_name":"UserPromptSubmit","session_id":"phase6-claude","cwd":"'"$create_root"'/legacy/alpha"}' |
  pmx_agent_hook_at "$claude_hook_pane" internal agent-hook ingest claude-hook >"$create_root/agent-claude-ingest.out"
if [[ -s "$create_root/agent-claude-ingest.out" ]] ||
  [[ -n "$(ctx show-options -pqv -t "$claude_hook_pane" @projmux_ai_agent)" ]] ||
  [[ -n "$(ctx show-options -pqv -t "$claude_hook_pane" @projmux_ai_managed)" ]] ||
  [[ -n "$(ctx show-options -pqv -t "$claude_hook_pane" @projmux_ai_launch_authorship)" ]] ||
  [[ "$(ctx show-options -pqv -t "$claude_hook_pane" @projmux_ai_state)" != thinking ]]; then
  echo "canonical Claude hook ingest crossed the hook-only authority boundary" >&2
  exit 1
fi
antigravity_hook_pane="$(ctx split-window -d -P -F '#{pane_id}' -t legacy-alpha -c "$create_root/legacy/alpha" sleep 600)"
printf '%s' '{"conversationId":"phase6-antigravity","workspacePaths":["'"$create_root"'/legacy/alpha"]}' |
  pmx_agent_hook_at "$antigravity_hook_pane" internal agent-hook ingest antigravity-hook --event PreInvocation \
    >"$create_root/agent-antigravity-ingest.out"
smoke_assert_file_contains "$create_root/agent-antigravity-ingest.out" '{}'
if [[ -n "$(ctx show-options -pqv -t "$antigravity_hook_pane" @projmux_ai_agent)" ]] ||
  [[ -n "$(ctx show-options -pqv -t "$antigravity_hook_pane" @projmux_ai_managed)" ]] ||
  [[ -n "$(ctx show-options -pqv -t "$antigravity_hook_pane" @projmux_ai_launch_authorship)" ]] ||
  [[ "$(ctx show-options -pqv -t "$antigravity_hook_pane" @projmux_ai_state)" != thinking ]]; then
  echo "canonical Antigravity hook ingest crossed the hook-only authority boundary" >&2
  exit 1
fi

# Seed the durable Codex conversation through the same canonical ingress, then
# take the Agent offline, change its Registry-only topic, and resume it.
printf '%s' '{"hook_event_name":"UserPromptSubmit","thread_id":"phase6-thread","session_id":"phase6-session","turn_id":"phase6-turn","cwd":"'"$create_root"'/legacy/alpha"}' |
  pmx_agent_live internal agent-hook ingest codex-hook
agent_deleted_pane="$agent_pane"
agent_target_receipt="$(ctx display-message -p -t "$agent_deleted_pane" '#{socket_path}|#{pid}|#{session_id}|#{window_id}|#{pane_id}')"
IFS='|' read -r agent_target_socket agent_target_pid agent_target_session agent_target_window agent_target_pane <<<"$agent_target_receipt"
agent_target_pane_uid="$(ctx show-options -pqv -t "$agent_deleted_pane" @projmux_pane_uid)"
if [[ "$agent_target_socket" != "$create_socket_path" ]] ||
  [[ "$agent_target_pid" != "$create_server_pid" ]] ||
  [[ "$agent_target_session" != "$agent_session_before" ]] ||
  [[ "$agent_target_window" != "$agent_window_before" ]] ||
  [[ "$agent_target_pane" != "$agent_deleted_pane" ]] ||
  [[ "$agent_target_pane" == "$agent_pane_before" ]] ||
  [[ "$agent_target_pane_uid" != "$agent_pane_uid" ]]; then
  echo "Agent delete target is not the exact managed sibling: got=$agent_target_socket/$agent_target_pid/$agent_target_session/$agent_target_window/$agent_target_pane pane_uid=$agent_target_pane_uid" >&2
  exit 1
fi

agent_host_receipt_is_exact() {
  agent_host_receipt="$(ctx display-message -p -t "$agent_pane_before" '#{socket_path}|#{pid}|#{session_id}|#{window_id}|#{pane_id}')"
  IFS='|' read -r agent_host_socket agent_host_pid agent_host_session agent_host_window agent_host_pane <<<"$agent_host_receipt"
  agent_host_window_uid="$(ctx show-options -wqv -t "$agent_window_before" @projmux_window_uid)"
  agent_host_pane_uid="$(ctx show-options -pqv -t "$agent_pane_before" @projmux_pane_uid)"
  [[ "$agent_host_socket" == "$create_socket_path" ]] &&
    [[ "$agent_host_pid" == "$create_server_pid" ]] &&
    [[ "$agent_host_session" == "$agent_session_before" ]] &&
    [[ "$agent_host_window" == "$agent_window_before" ]] &&
    [[ "$agent_host_pane" == "$agent_pane_before" ]] &&
    [[ "$agent_host_window_uid" == "$agent_window_uid_before" ]] &&
    [[ "$agent_host_pane_uid" == "$agent_host_pane_uid_before" ]]
}
if ! agent_host_receipt_is_exact; then
  echo "Agent fixture host receipt drifted before delete: got=$agent_host_socket/$agent_host_pid/$agent_host_session/$agent_host_window/$agent_host_pane window_uid=$agent_host_window_uid pane_uid=$agent_host_pane_uid" >&2
  exit 1
fi
# The external test shell is not the target Pane. Hand invocation authority to
# the exact managed primary host before delete so this is the synchronous
# non-self path; self-delete queue behavior is exercised by its own live block.
agent_pane="$agent_pane_before"
pmx_agent_live delete pane "uid:$agent_pane_uid" --yes
agent_deleted_target_absent() {
  local live_panes
  live_panes="$(ctx list-panes -a -F '#{pane_id}')" || return 1
  ! grep -Fxq -- "$agent_deleted_pane" <<<"$live_panes"
}
smoke_wait_for "exact Agent delete target $agent_deleted_pane to leave the runtime" agent_deleted_target_absent
if ! agent_host_receipt_is_exact; then
  echo "Agent fixture host receipt drifted after delete: got=$agent_host_socket/$agent_host_pid/$agent_host_session/$agent_host_window/$agent_host_pane window_uid=$agent_host_window_uid pane_uid=$agent_host_pane_uid" >&2
  exit 1
fi
pmx_agent_live agent topic set "offline resume topic" "uid:$agent_uid"
pmx_agent_live agent resume "uid:$agent_uid" >"$create_root/agent-resume.out"
smoke_assert_file_contains "$create_root/agent-resume.out" "resumed"
# This read runs from the revalidated managed host Pane rather than the killed
# Agent Pane. The explicit Project keeps the singular Agent reference scoped to
# the same durable Registry root while its replacement Pane is materialized.
resumed_pane_uid="$(pmx_agent_live describe agent "uid:$agent_uid" -p alpha -o json | sed -n 's/.*"paneRef": "\([^"]*\)".*/\1/p' | head -n 1)"
ctx list-panes -a -F '#{session_id}|#{window_id}|#{pane_id}|#{@projmux_pane_uid}' |
  awk -F '[|]' -v uid="$resumed_pane_uid" '$4 == uid { print }' >"$create_root/agent-resume.matches"
if [[ -z "$resumed_pane_uid" ]] || [[ "$(wc -l <"$create_root/agent-resume.matches")" != "1" ]]; then
  echo "Offline Agent resume Pane uid matched other than exactly one runtime Pane: uid=$resumed_pane_uid" >&2
  cat "$create_root/agent-resume.matches" >&2 || true
  exit 1
fi
IFS='|' read -r resumed_session resumed_window resumed_pane resumed_observed_uid <"$create_root/agent-resume.matches"
if [[ ! "$resumed_pane" =~ ^%[0-9]+$ ]] ||
  [[ "$resumed_session" != "$agent_session_before" ]] ||
  [[ "$resumed_window" != "$agent_window_before" ]] ||
  [[ "$resumed_observed_uid" != "$resumed_pane_uid" ]]; then
  echo "Offline Agent resume Pane has wrong exact containment: $resumed_session/$resumed_window/$resumed_pane uid=$resumed_observed_uid" >&2
  exit 1
fi
resumed_receipt="$(ctx display-message -p -t "$resumed_pane" '#{socket_path}|#{pid}|#{session_id}|#{window_id}|#{pane_id}|#{@projmux_pane_uid}')"
if [[ "$resumed_receipt" != "$create_socket_path|$create_server_pid|$agent_session_before|$agent_window_before|$resumed_pane|$resumed_pane_uid" ]] ||
  [[ "$(ctx show-options -pqv -t "$resumed_pane" @projmux_ai_topic)" != "offline resume topic" ]]; then
  echo "Offline Agent topic did not mirror to resumed Pane" >&2
  exit 1
fi
agent_pane="$resumed_pane"

# Inject one exact live projection failure. Registry remains committed, the
# public reconcile repairs the live option, and the next pass is a no-op.
agent_failure_shim="$create_root/agent-mutation-shim"
mkdir -p "$agent_failure_shim"
cat >"$agent_failure_shim/tmux" <<AGENT_MUTATION_SHIM
#!/usr/bin/env bash
phase6_args=("\$@")
if [[ "\${phase6_args[0]:-}" == "-S" && \${#phase6_args[@]} -ge 2 ]]; then
  phase6_args=("\${phase6_args[@]:2}")
fi
if [[ "\${phase6_args[0]:-}" == "set-option" ]]; then
  for phase6_arg in "\${phase6_args[@]:1}"; do
    if [[ "\$phase6_arg" == "@projmux_ai_topic" ]]; then
      exit 77
    fi
  done
fi
exec $(printf %q "$create_real_tmux") "\$@"
AGENT_MUTATION_SHIM
chmod 0755 "$agent_failure_shim/tmux"
set +e
PATH="$agent_failure_shim:$PATH" pmx_agent_live agent topic set "retry topic" "uid:$agent_uid" \
  >"$create_root/agent-mirror-failure.out" 2>"$create_root/agent-mirror-failure.err"
agent_mirror_failure_status=$?
set -e
if [[ "$agent_mirror_failure_status" == 0 ]] ||
  ! grep -Fq "committed Registry state" "$create_root/agent-mirror-failure.err" ||
  ! grep -Fq "projmux reconcile resources" "$create_root/agent-mirror-failure.err"; then
  echo "Agent projection failure did not expose committed/retry contract" >&2
  cat "$create_root/agent-mirror-failure.err" >&2 || true
  exit 1
fi
if [[ "$(pmx_agent_live agent topic get "uid:$agent_uid")" != "retry topic" ]]; then
  echo "Agent projection failure lost committed Registry topic" >&2
  exit 1
fi
pmx_agent_live reconcile resources --socket-path "$create_socket_path" >"$create_root/agent-reconcile.out"
if [[ "$(ctx show-options -pqv -t "$resumed_pane" @projmux_ai_topic)" != "retry topic" ]]; then
  echo "public reconcile did not repair Agent topic projection" >&2
  exit 1
fi
pmx_agent_live reconcile resources --socket-path "$create_socket_path" -o json >"$create_root/agent-reconcile-repeat.json"
smoke_assert_file_contains "$create_root/agent-reconcile-repeat.json" '"outcome": "no-op"'

# Cross-Project workspace changes the worker launch only; ownership stays in the
# explicitly selected alpha Window and only caller-provided roots reach argv.
: >"$create_root/agent-launch.log"
cross_agent_name="$(pmx_agent create agent --provider codex --project alpha --window "$alpha_window_name" \
  --cwd "$create_root/work/beta" --add-dir "$create_root/legacy/alpha" -o name)"
smoke_assert_file_contains "$create_root/agent-launch.log" "args=-C $create_root/work/beta --add-dir $create_root/legacy/alpha"
if ! pmx_agent get agents --project alpha -o name | grep -qx "$cross_agent_name" ||
  pmx_agent get agents --project beta -o name | grep -qx "$cross_agent_name"; then
  echo "cross-Project workspace changed Agent ownership" >&2
  exit 1
fi

# 15. The user reproduction, from inside a managed Pane. `create codex -w hi
#     --create-window` names no Project: the scope is derived from the managed
#     identity mirrored on the pane the command runs in, and the Window, the
#     Agent, and the Agent's managed Pane are created below that Project in one
#     transaction on the inherited exact socket.
foreign_state_before="$(cfx list-panes -a -F '#{session_name} #{window_id} #{pane_id} #{@projmux_pane_uid} #{@projmux_window_uid}')"
implicit_window_before="$(ctx display-message -p -t legacy-alpha '#{window_id}')"
implicit_pane_before="$(ctx display-message -p -t legacy-alpha '#{pane_id}')"
: >"$create_root/agent-launch.log"
pmx_agent_live create codex -w hi --create-window -o pane-id \
  >"$create_root/implicit-agent.out" 2>"$create_root/implicit-agent.err"
implicit_pane="$(tr -d '[:space:]' <"$create_root/implicit-agent.out")"
if [[ ! "$implicit_pane" =~ ^%[0-9]+$ ]]; then
  echo "implicit-scope create codex -o pane-id = $implicit_pane, want a raw %N handle" >&2
  cat "$create_root/implicit-agent.err" >&2 || true
  exit 1
fi
# The returned handle is live on the exact inherited socket and carries the
# managed Pane uid mirror.
if [[ "$(ctx display-message -p -t "$implicit_pane" '#{pane_id}')" != "$implicit_pane" ]]; then
  echo "the implicit-scope pane handle is not live on the exact socket" >&2
  exit 1
fi
if [[ -z "$(ctx display-message -p -t "$implicit_pane" '#{@projmux_pane_uid}')" ]]; then
  echo "the implicit-scope managed Pane has no Projmux uid mirror" >&2
  exit 1
fi
# The Window was created below the derived Project, not below any other one.
if ! pmx_agent get windows --project alpha -o name | grep -qx "hi"; then
  echo "implicit-scope --create-window did not create Window hi under the derived Project" >&2
  pmx_agent get windows --project alpha -o name >&2 || true
  exit 1
fi
if pmx_agent get windows --project beta -o name | grep -qx "hi"; then
  echo "implicit-scope create leaked a Window into another Project" >&2
  exit 1
fi
implicit_window_uid="$(ctx display-message -p -t "$implicit_pane" '#{@projmux_window_uid}')"
if [[ -z "$implicit_window_uid" ]]; then
  echo "the implicit-scope Window has no uid mirror" >&2
  exit 1
fi
if [[ "$(ctx display-message -p -t "$implicit_pane" '#{session_name}')" != "legacy-alpha" ]]; then
  echo "the implicit-scope Window landed outside the derived Project's session" >&2
  exit 1
fi
if [[ "$(ctx display-message -p -t "$implicit_pane" '#{window_name}')" != "hi" ]]; then
  echo "the implicit-scope Window is not named hi" >&2
  exit 1
fi
# The provider really launched, and the client never moved.
for _ in {1..200}; do
  [[ -s "$create_root/agent-launch.log" ]] && break
  sleep 0.05
done
if ! grep -Fq "cwd=$create_root/legacy/alpha" "$create_root/agent-launch.log"; then
  echo "the implicit-scope create did not launch the provider in the derived Project root" >&2
  cat "$create_root/agent-launch.log" >&2 || true
  exit 1
fi
if [[ "$(ctx display-message -p -t legacy-alpha '#{window_id}')" != "$implicit_window_before" ]] ||
  [[ "$(ctx display-message -p -t legacy-alpha '#{pane_id}')" != "$implicit_pane_before" ]]; then
  echo "an implicit-scope create moved the active window or pane" >&2
  exit 1
fi

# 15b. The same command on an app-owned host. Host mode is a runtime property,
#      not an authority boundary: the create mutates only the socket it
#      inherited, and the sibling server is byte-identical before and after both
#      halves of the matrix.
ctx set-option -gq @projmux_app 1
pmx_agent_live create pane -w app-owned-host --create-window -o pane-id \
  >"$create_root/app-owned.out" 2>"$create_root/app-owned.err"
app_owned_pane="$(tr -d '[:space:]' <"$create_root/app-owned.out")"
if [[ ! "$app_owned_pane" =~ ^%[0-9]+$ ]]; then
  echo "app-owned host create -o pane-id = $app_owned_pane, want a raw %N handle" >&2
  cat "$create_root/app-owned.err" >&2 || true
  exit 1
fi
if [[ "$(ctx display-message -p -t "$app_owned_pane" '#{session_name}')" != "legacy-alpha" ]]; then
  echo "the app-owned host create landed outside the derived Project's session" >&2
  exit 1
fi
if [[ "$(ctx show-options -gqv @projmux_app)" != "1" ]]; then
  echo "the app-owned host marker disappeared during the create" >&2
  exit 1
fi
ctx set-option -gqu @projmux_app
foreign_state_after="$(cfx list-panes -a -F '#{session_name} #{window_id} #{pane_id} #{@projmux_pane_uid} #{@projmux_window_uid}')"
if [[ "$foreign_state_after" != "$foreign_state_before" ]]; then
  echo "an inherited-socket create changed a sibling tmux server" >&2
  printf 'before:\n%s\nafter:\n%s\n' "$foreign_state_before" "$foreign_state_after" >&2
  exit 1
fi

# 16. A pane whose Window carries no managed identity is unattributed. It is a
#     refusal with zero mutations on the very server the command is running in;
#     nothing is adopted by proximity.
unattributed_pane="$(ctx new-window -d -P -F '#{pane_id}' -t legacy-alpha -c "$create_root/legacy/alpha" sleep 600)"
if [[ -n "$(ctx display-message -p -t "$unattributed_pane" '#{@projmux_window_uid}')" ]]; then
  echo "the unattributed fixture window carries a managed uid" >&2
  exit 1
fi
unattributed_registry_before="$(md5sum "$create_registry" | cut -d' ' -f1)"
unattributed_panes_before="$(ctx list-panes -a -F '#{pane_id}' | wc -l)"
set +e
pmx_agent_hook_at "$unattributed_pane" create pane -o pane-id \
  >"$create_root/unattributed.out" 2>"$create_root/unattributed.err"
unattributed_status=$?
set -e
if [[ "$unattributed_status" != "2" ]]; then
  echo "an unattributed-pane create exit = $unattributed_status, want 2" >&2
  cat "$create_root/unattributed.err" >&2 || true
  exit 1
fi
if [[ -s "$create_root/unattributed.out" ]]; then
  echo "an unattributed-pane create wrote to stdout" >&2
  exit 1
fi
if [[ "$(md5sum "$create_registry" | cut -d' ' -f1)" != "$unattributed_registry_before" ]]; then
  echo "an unattributed-pane create mutated the registry" >&2
  exit 1
fi
if [[ "$(ctx list-panes -a -F '#{pane_id}' | wc -l)" != "$unattributed_panes_before" ]]; then
  echo "an unattributed-pane create still split a pane" >&2
  exit 1
fi
smoke_assert_file_contains "$create_root/unattributed.err" "pass --project <ref>"
ctx kill-pane -t "$unattributed_pane"

# 17. A foreign server is not a scope either. The invocation inherits the other
#     socket, finds no managed identity there, and refuses without touching
#     either server or the registry.
foreign_registry_before="$(md5sum "$create_registry" | cut -d' ' -f1)"
foreign_panes_before="$(cfx list-panes -a -F '#{pane_id}' | wc -l)"
app_panes_before="$(ctx list-panes -a -F '#{pane_id}' | wc -l)"
set +e
env HOME="$agent_home" \
  TMUX="$create_foreign_socket_path,1,0" \
  TMUX_PANE="$foreign_agent_pane" \
  TMUX_TMPDIR="$create_root/tt" \
  XDG_STATE_HOME="$create_root/state" \
  XDG_CONFIG_HOME="$create_root/config" \
  PROJMUX_MANAGED_ROOTS="$create_root/legacy:$create_root/work" \
  SHELL=/bin/bash \
  "$bin" create pane -o pane-id \
  >"$create_root/foreign-create.out" 2>"$create_root/foreign-create.err"
foreign_create_status=$?
set -e
if [[ "$foreign_create_status" != "2" ]]; then
  echo "a foreign-server create exit = $foreign_create_status, want 2" >&2
  cat "$create_root/foreign-create.err" >&2 || true
  exit 1
fi
if [[ -s "$create_root/foreign-create.out" ]]; then
  echo "a foreign-server create wrote to stdout" >&2
  exit 1
fi
if [[ "$(md5sum "$create_registry" | cut -d' ' -f1)" != "$foreign_registry_before" ]]; then
  echo "a foreign-server create mutated the registry" >&2
  exit 1
fi
if [[ "$(cfx list-panes -a -F '#{pane_id}' | wc -l)" != "$foreign_panes_before" ]]; then
  echo "a foreign-server create split a pane on the foreign server" >&2
  exit 1
fi
if [[ "$(ctx list-panes -a -F '#{pane_id}' | wc -l)" != "$app_panes_before" ]]; then
  echo "a foreign-server create reached the app-owned server" >&2
  exit 1
fi
smoke_assert_file_contains "$create_root/foreign-create.err" "pass --project <ref>"

foreign_agent_after="$(cfx display-message -p -t "$foreign_agent_pane" '#{pane_title}|#{@projmux_ai_topic}|#{@projmux_ai_state}')"
if [[ "$foreign_agent_after" != "$foreign_agent_before" ]]; then
  echo "Phase 6 Agent mutations touched foreign/title-matching Pane: before=$foreign_agent_before after=$foreign_agent_after" >&2
  exit 1
fi

# A generic runtime provider marker is not authority to promote the canonical
# default shell Pane into an Agent-owned Pane. Run this install-acceptance
# follow-up after the create matrix because config apply installs asynchronous
# hooks that the transaction-boundary fixtures above deliberately omit.
canonical_shell_uid="$(ctx show-options -pqv -t "$legacy_pane" @projmux_pane_uid)"
canonical_default_shell_uid="$(pmx describe window "$alpha_window_name" -p alpha -o json | sed -n 's/.*"defaultShellPaneRef": "\([^"]*\)".*/\1/p' | head -n 1)"
if [[ -z "$canonical_shell_uid" || "$canonical_default_shell_uid" != "$canonical_shell_uid" ]]; then
  echo "canonical shell marker fixture is not the exact default shell: pane=$canonical_shell_uid default=$canonical_default_shell_uid" >&2
  exit 1
fi
# Settle unrelated status projection before taking the protected preimage.
pmx config apply >"$create_root/canonical-shell-config-baseline.out"
canonical_agents_before="$(pmx get agents -p alpha -o uid | sort)"
ctx set-option -p -t "$legacy_pane" @projmux_ai_agent codex
canonical_registry_before="$(sha256sum "$create_registry" | awk '{print $1}')"
cp "$create_registry" "$create_root/canonical-shell-registry-before.json"
canonical_live_before="$(ctx display-message -p -t "$legacy_pane" '#{session_id}|#{window_id}|#{pane_id}|#{@projmux_window_uid}|#{@projmux_pane_uid}|#{@projmux_ai_agent}')"
pmx config apply >"$create_root/canonical-shell-config-apply.out"
canonical_registry_after_apply="$(sha256sum "$create_registry" | awk '{print $1}')"
canonical_live_after_apply="$(ctx display-message -p -t "$legacy_pane" '#{session_id}|#{window_id}|#{pane_id}|#{@projmux_window_uid}|#{@projmux_pane_uid}|#{@projmux_ai_agent}')"
if [[ "$canonical_registry_after_apply" != "$canonical_registry_before" ]] ||
  [[ "$canonical_live_after_apply" != "$canonical_live_before" ]] ||
  [[ "$(pmx get agents -p alpha -o uid | sort)" != "$canonical_agents_before" ]]; then
  echo "config apply changed canonical shell Registry bytes, Agent set, or live handles/options: registry=$canonical_registry_before/$canonical_registry_after_apply live=$canonical_live_before/$canonical_live_after_apply" >&2
  diff -u "$create_root/canonical-shell-registry-before.json" "$create_registry" >&2 || true
  exit 1
fi
pmx reconcile resources --socket "$create_socket" --dry-run -o json >"$create_root/canonical-shell-d2-1.json"
pmx reconcile resources --socket "$create_socket" --dry-run -o json >"$create_root/canonical-shell-d2-2.json"
smoke_assert_file_contains "$create_root/canonical-shell-d2-1.json" '"divergence": "D2-unattributed"'
smoke_assert_file_contains "$create_root/canonical-shell-d2-1.json" 'runtime Agent marker cannot reparent the canonical Window default shell Pane'
smoke_assert_file_contains "$create_root/canonical-shell-d2-1.json" "\"target\": \"$legacy_pane\""
if grep -Fq 'registry:create:agent' "$create_root/canonical-shell-d2-1.json" ||
  grep -Fq 'registry:update:pane' "$create_root/canonical-shell-d2-1.json" ||
  ! cmp -s "$create_root/canonical-shell-d2-1.json" "$create_root/canonical-shell-d2-2.json"; then
  echo "canonical shell dry-run planned promotion or produced unstable refusal output" >&2
  exit 1
fi
set +e
pmx reconcile resources --socket "$create_socket" -o json >"$create_root/canonical-shell-execute.json" 2>"$create_root/canonical-shell-execute.err"
canonical_execute_status=$?
set -e
if [[ "$canonical_execute_status" == "0" ]] ||
  ! grep -Fq '"outcome": "refused"' "$create_root/canonical-shell-execute.json" ||
  [[ "$(sha256sum "$create_registry" | awk '{print $1}')" != "$canonical_registry_before" ]] ||
  [[ "$(pmx get agents -p alpha -o uid | sort)" != "$canonical_agents_before" ]] ||
  [[ "$(ctx display-message -p -t "$legacy_pane" '#{session_id}|#{window_id}|#{pane_id}|#{@projmux_window_uid}|#{@projmux_pane_uid}|#{@projmux_ai_agent}')" != "$canonical_live_before" ]]; then
  echo "canonical shell execute did not remain a zero-write typed refusal" >&2
  cat "$create_root/canonical-shell-execute.json" >&2 || true
  cat "$create_root/canonical-shell-execute.err" >&2 || true
  diff -u "$create_root/canonical-shell-registry-before.json" "$create_registry" >&2 || true
  echo "canonical live before=$canonical_live_before" >&2
  echo "canonical live after=$(ctx display-message -p -t "$legacy_pane" '#{session_id}|#{window_id}|#{pane_id}|#{@projmux_window_uid}|#{@projmux_pane_uid}|#{@projmux_ai_agent}')" >&2
  exit 1
fi
ctx set-option -p -u -t "$legacy_pane" @projmux_ai_agent

create_cleanup
trap smoke_cleanup_env EXIT
echo ">> create e2e passed: socket=$create_socket path=$create_socket_path"

# Explicit Registry authority converges Project -> Window -> Pane. Every
# fixture below keeps each public report and must reach an empty no-op within
# that three-level walk plus one confirming pass. Raw imports require initial
# progress; post-apply callers opt into accepting an already-converged pass.
e2e_bounded_reconcile_to_noop() {
  local allow_initial_noop=0
  if [[ "${1:-}" == "--allow-initial-noop" ]]; then
    allow_initial_noop=1
    shift
  fi
  local report_prefix="$1"
  shift
  local pass report
  for pass in 1 2 3 4; do
    report="$report_prefix-$pass.json"
    "$@" >"$report"
    if grep -Fq '"outcome": "changed"' "$report"; then
      continue
    fi
    if grep -Fq '"outcome": "no-op"' "$report"; then
      if ! grep -Fq '"items": []' "$report"; then
        echo "resource reconcile no-op retained nonempty items: $report" >&2
        cat "$report" >&2
        return 1
      fi
      if [[ "$pass" == "1" && "$allow_initial_noop" != "1" ]]; then
        echo "explicit authority made no initial progress: $report" >&2
        cat "$report" >&2
        return 1
      fi
      return 0
    fi
    echo "resource reconcile reported neither changed nor no-op: $report" >&2
    cat "$report" >&2
    return 1
  done
  echo "resource reconcile did not converge within four passes: $report_prefix" >&2
  cat "$report" >&2
  return 1
}

# Managed runtime binding convergence runs on its own two exact sockets. Every
# client call strips inherited TMUX/TMUX_PANE; only the implicit reads below
# receive a synthetic client identity after the binding has already converged.
binding_root="$PROJMUX_SMOKE_WORKDIR/managed-binding-phase3"
binding_socket="projmux-binding-$RANDOM-$$"
binding_second_socket="projmux-binding-second-$RANDOM-$$"
binding_session="work-alpha"
binding_beta_session="work-beta"
mkdir -p \
  "$binding_root/home" \
  "$binding_root/config" \
  "$binding_root/state" \
  "$binding_root/runtime" \
  "$binding_root/tmux" \
  "$binding_root/work/alpha" \
  "$binding_root/work/beta"
chmod 0700 "$binding_root/runtime" "$binding_root/tmux"

binding_tmux() {
  env -u TMUX -u TMUX_PANE \
    HOME="$binding_root/home" \
    XDG_CONFIG_HOME="$binding_root/config" \
    XDG_STATE_HOME="$binding_root/state" \
    XDG_RUNTIME_DIR="$binding_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$binding_root/work" \
    TMUX_TMPDIR="$binding_root/tmux" \
    SHELL=/bin/sh \
    tmux -L "$binding_socket" "$@"
}

binding_second_tmux() {
  env -u TMUX -u TMUX_PANE \
    HOME="$binding_root/home" \
    XDG_CONFIG_HOME="$binding_root/config" \
    XDG_STATE_HOME="$binding_root/state" \
    XDG_RUNTIME_DIR="$binding_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$binding_root/work" \
    TMUX_TMPDIR="$binding_root/tmux" \
    SHELL=/bin/sh \
    tmux -L "$binding_second_socket" "$@"
}

binding_pmx() {
  env -u TMUX -u TMUX_PANE \
    HOME="$binding_root/home" \
    XDG_CONFIG_HOME="$binding_root/config" \
    XDG_STATE_HOME="$binding_root/state" \
    XDG_RUNTIME_DIR="$binding_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$binding_root/work" \
    TMUX_TMPDIR="$binding_root/tmux" \
    SHELL=/bin/sh \
    "$bin" "$@"
}

binding_tmux new-session -d -s "$binding_session" -n initial -c "$binding_root/work/alpha" sleep 600
binding_tmux set-option -t "$binding_session" -q @projmux_project_path "$binding_root/work/alpha"
binding_tmux new-session -d -s "$binding_beta_session" -n initial -c "$binding_root/work/beta" sleep 600
binding_tmux set-option -t "$binding_beta_session" -q @projmux_project_path "$binding_root/work/beta"
binding_second_tmux new-session -d -s untouched -c "$binding_root/work/alpha" sleep 600
binding_second_tmux set-option -gq @projmux_phase3_sentinel unchanged

binding_socket_path="$(binding_tmux display-message -p -t "$binding_session" '#{socket_path}')"
binding_second_socket_path="$(binding_second_tmux display-message -p -t untouched '#{socket_path}')"
binding_session_id="$(binding_tmux display-message -p -t "$binding_session" '#{session_id}')"
for actual in "$binding_socket_path" "$binding_second_socket_path"; do
  case "$actual" in
    "$binding_root"/*) ;;
    *)
      echo "managed binding e2e socket escaped the smoke root: $actual" >&2
      exit 1
      ;;
  esac
done
echo ">> managed binding e2e sockets primary=$binding_socket_path second=$binding_second_socket_path"

binding_await_server_gone() {
  local actual="$1" label="$2"
  for _ in $(seq 1 200); do
    if ! env -u TMUX -u TMUX_PANE tmux -S "$actual" list-sessions >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.05
  done
  echo "managed binding cleanup left $label server live at $actual" >&2
  return 1
}

binding_background_routes() {
  ps -eo pid=,args= | PROJMUX_ROUTE_BIN="$bin" awk '
    index($0, ENVIRON["PROJMUX_ROUTE_BIN"]) > 0 { print }'
}

binding_await_background_routes() {
  local routes quiet_samples=0
  for _ in $(seq 1 200); do
    routes="$(binding_background_routes)"
    if [[ -z "$routes" ]]; then
      quiet_samples=$((quiet_samples + 1))
      if [[ "$quiet_samples" == "10" ]]; then
        return 0
      fi
    else
      quiet_samples=0
    fi
    sleep 0.05
  done
  echo "managed binding cleanup still has isolated background routes:" >&2
  binding_background_routes >&2
  return 1
}

binding_cleanup() {
  local actual hook label
  for actual in "$binding_socket_path" "$binding_second_socket_path"; do
    label="$binding_socket"
    if [[ "$actual" == "$binding_second_socket_path" ]]; then
      label="$binding_second_socket"
    fi
    case "$actual" in
      "$binding_root"/*)
        # The app config owns background pane-exit/focus hooks. Disable them on
        # this exact server before kill-server so no detached helper races the
        # smoke root cleanup by recreating state after the server is gone.
        for hook in pane-exited after-kill-pane window-unlinked pane-focus-out pane-focus-in after-select-pane after-select-window client-session-changed; do
          env -u TMUX -u TMUX_PANE tmux -S "$actual" set-hook -gu "$hook" >/dev/null 2>&1 || true
        done
        env -u TMUX -u TMUX_PANE tmux -S "$actual" kill-server >/dev/null 2>&1 || true
        binding_await_server_gone "$actual" "$label"
        ;;
      *)
        echo "refusing managed binding e2e cleanup outside the smoke root: $actual" >&2
        ;;
    esac
  done
  # pane-exit and supervisor routes may outlive the server process briefly.
  # Require a quiet window before the outer cleanup removes their state root.
  binding_await_background_routes
}
trap 'binding_cleanup; smoke_cleanup_env' EXIT

binding_second_before="$(binding_second_tmux show-options -gqv @projmux_phase3_sentinel):$(binding_second_tmux list-windows -a -F '#{session_name}:#{window_id}:#{@projmux_window_uid}')"
binding_tmux list-windows -a -F '#{session_name}:#{window_id}:#{@projmux_window_uid}' >"$binding_root/d2.before"
binding_pmx reconcile resources --socket "$binding_socket" --dry-run -o json >"$binding_root/d2.json"
smoke_assert_file_contains "$binding_root/d2.json" '"outcome": "no-op"'
if [[ -e "$binding_root/state/projmux/metadata/registry.json" ]]; then
  echo "managed binding D2 dry-run created a Registry" >&2
  exit 1
fi
binding_tmux list-windows -a -F '#{session_name}:#{window_id}:#{@projmux_window_uid}' >"$binding_root/d2.after"
cmp "$binding_root/d2.before" "$binding_root/d2.after"
if [[ "$(binding_second_tmux show-options -gqv @projmux_phase3_sentinel):$(binding_second_tmux list-windows -a -F '#{session_name}:#{window_id}:#{@projmux_window_uid}')" != "$binding_second_before" ]]; then
  echo "managed binding D2 dry-run touched the second socket" >&2
  exit 1
fi
binding_pmx create project --root "$binding_root/work/alpha" --name alpha >"$binding_root/register-alpha.out"
binding_pmx create project --root "$binding_root/work/beta" --name beta >"$binding_root/register-beta.out"
binding_config="$binding_root/config/projmux/tmux.conf"
binding_pmx internal tmux apply --bin "$bin" --config "$binding_config" --socket "$binding_socket" \
  >"$binding_root/first-apply.out"
smoke_assert_file_contains "$binding_root/first-apply.out" "reloaded tmux server -L $binding_socket"
# Config apply reaches the controller on the exact physical route and performs
# the parent-before-child repair. The explicit repeat must therefore be empty.
binding_pmx reconcile resources --socket "$binding_socket" -o json >"$binding_root/first-reconcile-1.json"
smoke_assert_file_contains "$binding_root/first-reconcile-1.json" '"outcome": "no-op"'
smoke_assert_file_contains "$binding_root/first-reconcile-1.json" '"items": []'

binding_window="$(binding_tmux display-message -p -t "$binding_session:0" '#{window_id}')"
binding_pane="$(binding_tmux display-message -p -t "$binding_session:0.0" '#{pane_id}')"
binding_beta_window="$(binding_tmux display-message -p -t "$binding_beta_session:0" '#{window_id}')"
binding_beta_pane="$(binding_tmux display-message -p -t "$binding_beta_session:0.0" '#{pane_id}')"
binding_window_uid="$(binding_tmux show-options -wqv -t "$binding_window" @projmux_window_uid)"
binding_pane_uid="$(binding_tmux show-options -pqv -t "$binding_pane" @projmux_pane_uid)"
binding_beta_window_uid="$(binding_tmux show-options -wqv -t "$binding_beta_window" @projmux_window_uid)"
binding_beta_pane_uid="$(binding_tmux show-options -pqv -t "$binding_beta_pane" @projmux_pane_uid)"
if [[ -z "$binding_window_uid" ]] || [[ -z "$binding_pane_uid" ]] || \
  [[ -z "$binding_beta_window_uid" ]] || [[ -z "$binding_beta_pane_uid" ]]; then
  echo "first exact-socket apply did not bind the managed Window/Pane" >&2
  exit 1
fi

# Deleting only the transport options must not change registry identity. The
# next normal apply restores the exact same Window and Pane uids.
binding_tmux set-option -wu -t "$binding_window" @projmux_window_uid
binding_tmux set-option -pu -t "$binding_pane" @projmux_pane_uid
binding_pmx internal tmux apply --bin "$bin" --config "$binding_config" --socket "$binding_socket" \
  >"$binding_root/repair-apply.out"
if [[ "$(binding_tmux show-options -wqv -t "$binding_window" @projmux_window_uid)" != "$binding_window_uid" ]]; then
  echo "apply did not restore the original Window uid $binding_window_uid" >&2
  exit 1
fi
if [[ "$(binding_tmux show-options -pqv -t "$binding_pane" @projmux_pane_uid)" != "$binding_pane_uid" ]]; then
  echo "apply did not restore the original Pane uid $binding_pane_uid" >&2
  exit 1
fi

binding_registry="$binding_root/state/projmux/metadata/registry.json"
cp "$binding_registry" "$binding_root/registry.before-repeat"
binding_registry_stat="$(stat -c '%i:%s:%y' "$binding_registry")"
cp "$binding_config" "$binding_root/config.before-repeat"
binding_config_stat="$(stat -c '%i:%s:%y' "$binding_config")"
binding_pmx internal tmux apply --bin "$bin" --config "$binding_config" --socket "$binding_socket" \
  >"$binding_root/repeat-apply.out"
cmp "$binding_root/registry.before-repeat" "$binding_registry"
if [[ "$(stat -c '%i:%s:%y' "$binding_registry")" != "$binding_registry_stat" ]]; then
  echo "repeat apply rewrote byte-identical registry content" >&2
  exit 1
fi
cmp "$binding_root/config.before-repeat" "$binding_config"
if [[ "$(stat -c '%i:%s:%y' "$binding_config")" != "$binding_config_stat" ]]; then
  echo "repeat apply rewrote byte-identical generated config" >&2
  exit 1
fi

# The generated hooks are synchronous, but raw tmux creation is still D2 and
# therefore L0. Prove both hooks return without minting identity or changing
# Registry authority, then remove the raw objects before building the managed
# lifecycle fixture through canonical creates.
cp "$binding_registry" "$binding_root/registry.before-raw-hooks"
raw_lifecycle_window="$(
  binding_tmux new-window -d -t "$binding_session:" -n raw-lifecycle -c "$binding_root/work/alpha" \
    -P -F '#{window_id}' sleep 600
)"
raw_lifecycle_initial_pane="$(binding_tmux display-message -p -t "$raw_lifecycle_window" '#{pane_id}')"
if [[ -n "$(binding_tmux show-options -wqv -t "$raw_lifecycle_window" @projmux_window_uid)" ]] ||
  [[ -n "$(binding_tmux show-options -pqv -t "$raw_lifecycle_initial_pane" @projmux_pane_uid)" ]]; then
  echo "after-new-window automatically bound a raw D2 Window/Pane" >&2
  exit 1
fi
cmp "$binding_root/registry.before-raw-hooks" "$binding_registry"

raw_lifecycle_split_pane="$(
  binding_tmux split-window -d -t "$raw_lifecycle_initial_pane" -c "$binding_root/work/alpha" \
    -P -F '#{pane_id}' sleep 600
)"
if [[ -n "$(binding_tmux show-options -pqv -t "$raw_lifecycle_split_pane" @projmux_pane_uid)" ]]; then
  echo "after-split-window automatically bound a raw D2 Pane" >&2
  exit 1
fi
cmp "$binding_root/registry.before-raw-hooks" "$binding_registry"
binding_tmux kill-window -t "$raw_lifecycle_window"
cmp "$binding_root/registry.before-raw-hooks" "$binding_registry"

binding_server_pid="$(binding_tmux display-message -p -t "$binding_pane" '#{pid}')"
binding_inside_pane="$binding_pane"
binding_inside_pmx() {
  env \
    HOME="$binding_root/home" \
    XDG_CONFIG_HOME="$binding_root/config" \
    XDG_STATE_HOME="$binding_root/state" \
    XDG_RUNTIME_DIR="$binding_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$binding_root/work" \
    TMUX_TMPDIR="$binding_root/tmux" \
    TMUX="$binding_socket_path,$binding_server_pid,0" \
    TMUX_PANE="$binding_inside_pane" \
    SHELL=/bin/sh \
    "$bin" "$@"
}

binding_inside_pmx create window --project alpha --name lifecycle >"$binding_root/create-lifecycle-window.out"
lifecycle_window_uid="$(binding_inside_pmx describe window lifecycle -p alpha -o uid | tr -d '[:space:]')"
lifecycle_window="$(
  binding_tmux list-windows -a -F '#{window_id}|#{@projmux_window_uid}' |
    awk -F '|' -v uid="$lifecycle_window_uid" '$2 == uid { print $1 }'
)"
lifecycle_initial_pane="$(binding_tmux display-message -p -t "$lifecycle_window" '#{pane_id}')"
lifecycle_initial_pane_uid="$(binding_tmux show-options -pqv -t "$lifecycle_initial_pane" @projmux_pane_uid)"
if [[ -z "$lifecycle_window" ]] || [[ -z "$lifecycle_window_uid" ]] || [[ -z "$lifecycle_initial_pane_uid" ]]; then
  echo "canonical create window left its managed Window/Pane identity empty" >&2
  exit 1
fi

lifecycle_split_pane="$(
  binding_inside_pmx create pane --project alpha --window "uid:$lifecycle_window_uid" \
    -o pane-id -- sleep 600
)"
lifecycle_split_uid="$(binding_tmux show-options -pqv -t "$lifecycle_split_pane" @projmux_pane_uid)"
if [[ -z "$lifecycle_split_uid" ]]; then
  echo "canonical create pane left its managed Pane identity empty" >&2
  exit 1
fi
binding_inside_pane="$lifecycle_split_pane"

# The immediately following selector-omitted read resolves that exact managed
# Pane through the inherited exact-socket client context.
binding_inside_pmx describe pane -o uid >"$binding_root/implicit-read.out"
if [[ "$(tr -d '[:space:]' <"$binding_root/implicit-read.out")" != "$lifecycle_split_uid" ]]; then
  echo "implicit read did not resolve the synchronously bound Pane" >&2
  cat "$binding_root/implicit-read.out" >&2
  exit 1
fi

# The plural registry reads use Project as a namespace-like default. The
# synthetic client sits in alpha: its default Pane and Window inventories must
# exclude beta, while --all-projects and the outside-tmux compatibility path
# must include both. Project inventory itself remains global.

# Canonical create holds the metadata transaction while tmux runs synchronous
# creation hooks. The session-scoped lease must prevent self-deadlock without
# weakening standalone hooks, and the second in-transaction reconcile must
# leave registry status aligned with the exact returned pane.
create_reentrant_pane="$(
  binding_inside_pmx create pane -p alpha -w roadmap --create-window \
    -o pane-id -- sleep 600
)"
if [[ ! "$create_reentrant_pane" =~ ^%[0-9]+$ ]]; then
  echo "reentrant --create-window returned invalid pane id: $create_reentrant_pane" >&2
  exit 1
fi
create_reentrant_uid="$(binding_tmux show-options -pqv -t "$create_reentrant_pane" @projmux_pane_uid)"
if [[ -z "$create_reentrant_uid" ]]; then
  echo "reentrant --create-window returned before pane uid mirror" >&2
  exit 1
fi
binding_inside_pmx get panes -p alpha -w roadmap -o json >"$binding_root/create-reentrant.json"
smoke_assert_file_contains "$binding_root/create-reentrant.json" "$create_reentrant_uid"
if grep -Fq '"type": "MissingRuntime"' "$binding_root/create-reentrant.json"; then
  echo "reentrant create committed stale MissingRuntime status" >&2
  exit 1
fi

# tmux may create and print %N before a later synchronous hook fails. The CLI
# must emit no success stdout, restore byte-identical registry content, and
# remove only that newly created pane.
cp "$binding_registry" "$binding_root/registry.before-hook-failure"
binding_tmux list-panes -a -F '#{pane_id}|#{@projmux_pane_uid}' >"$binding_root/panes.before-hook-failure"
binding_tmux set-hook -ag after-split-window 'run-shell "exit 7"'
set +e
binding_inside_pmx create pane --project alpha --window roadmap -o pane-id -- sleep 600 \
  >"$binding_root/create-hook-failure.out" 2>"$binding_root/create-hook-failure.err"
create_hook_failure_status=$?
set -e
if [[ "$create_hook_failure_status" == "0" ]] || [[ -s "$binding_root/create-hook-failure.out" ]]; then
  echo "object-created hook failure reported success or wrote stdout" >&2
  exit 1
fi
smoke_assert_file_contains "$binding_root/create-hook-failure.err" "returned 7"
cmp "$binding_root/registry.before-hook-failure" "$binding_registry"
binding_tmux list-panes -a -F '#{pane_id}|#{@projmux_pane_uid}' >"$binding_root/panes.after-hook-failure"
cmp "$binding_root/panes.before-hook-failure" "$binding_root/panes.after-hook-failure"
binding_tmux source-file "$binding_config"

binding_inside_pmx get panes -o uid >"$binding_root/project-panes.out"
smoke_assert_file_contains "$binding_root/project-panes.out" "$lifecycle_split_uid"
if grep -Fqx "$binding_beta_pane_uid" "$binding_root/project-panes.out"; then
  echo "active Project Pane inventory crossed into beta" >&2
  exit 1
fi

binding_inside_pmx get windows -o uid >"$binding_root/project-windows.out"
smoke_assert_file_contains "$binding_root/project-windows.out" "$lifecycle_window_uid"
if grep -Fqx "$binding_beta_window_uid" "$binding_root/project-windows.out"; then
  echo "active Project Window inventory crossed into beta" >&2
  exit 1
fi

binding_inside_pmx get panes -A -o uid >"$binding_root/all-project-panes.out"
smoke_assert_file_contains "$binding_root/all-project-panes.out" "$lifecycle_split_uid"
smoke_assert_file_contains "$binding_root/all-project-panes.out" "$binding_beta_pane_uid"
binding_pmx get panes -o uid >"$binding_root/outside-panes.out"
sort "$binding_root/all-project-panes.out" >"$binding_root/all-project-panes.sorted"
sort "$binding_root/outside-panes.out" >"$binding_root/outside-panes.sorted"
cmp "$binding_root/all-project-panes.sorted" "$binding_root/outside-panes.sorted"

binding_inside_pmx get panes -p beta -o uid >"$binding_root/explicit-beta-panes.out"
smoke_assert_file_contains "$binding_root/explicit-beta-panes.out" "$binding_beta_pane_uid"
if grep -Fqx "$lifecycle_split_uid" "$binding_root/explicit-beta-panes.out"; then
  echo "explicit --project beta included an alpha Pane" >&2
  exit 1
fi

binding_inside_pmx get projects -o name >"$binding_root/projects.out"
smoke_assert_file_contains "$binding_root/projects.out" "alpha"
smoke_assert_file_contains "$binding_root/projects.out" "beta"

# An explicit singular reference resolves inside the active Project too, not
# against the whole Registry. The fixture is built to separate the two failure
# modes it has to keep apart: Window `ns-one` and Pane `shared` exist in alpha
# *and* in beta, so a cross-Project leak is visible, and alpha holds a second
# Pane `shared` under `ns-two`, so a real intra-Project ambiguity must survive
# the narrowing instead of being silently broken by the active Window.
binding_ns_alpha_pane="$(
  binding_inside_pmx create pane -p alpha -w ns-one --create-window --name shared -o pane-id -- sleep 600
)"
binding_ns_sibling_pane="$(
  binding_inside_pmx create pane -p alpha -w ns-two --create-window --name shared -o pane-id -- sleep 600
)"
binding_ns_beta_pane="$(
  binding_inside_pmx create pane -p beta -w ns-one --create-window --name shared -o pane-id -- sleep 600
)"
binding_ns_alpha_pane_uid="$(binding_tmux show-options -pqv -t "$binding_ns_alpha_pane" @projmux_pane_uid)"
binding_ns_sibling_pane_uid="$(binding_tmux show-options -pqv -t "$binding_ns_sibling_pane" @projmux_pane_uid)"
binding_ns_beta_pane_uid="$(binding_tmux show-options -pqv -t "$binding_ns_beta_pane" @projmux_pane_uid)"
binding_ns_alpha_window_uid="$(binding_inside_pmx describe window ns-one -p alpha -o uid | tr -d '[:space:]')"
binding_ns_beta_window_uid="$(binding_inside_pmx describe window ns-one -p beta -o uid | tr -d '[:space:]')"
if [[ -z "$binding_ns_alpha_pane_uid" ]] || [[ -z "$binding_ns_sibling_pane_uid" ]] ||
  [[ -z "$binding_ns_beta_pane_uid" ]] || [[ -z "$binding_ns_alpha_window_uid" ]] ||
  [[ -z "$binding_ns_beta_window_uid" ]]; then
  echo "singular namespace fixture left an identity empty" >&2
  exit 1
fi
if [[ "$binding_ns_alpha_window_uid" == "$binding_ns_beta_window_uid" ]]; then
  echo "singular namespace fixture reused one Window across Projects" >&2
  exit 1
fi

binding_assert_singular_uid() {
  local want="$1" label="$2"
  shift 2
  binding_inside_pmx "$@" >"$binding_root/singular-$label.out"
  if [[ "$(tr -d '[:space:]' <"$binding_root/singular-$label.out")" != "$want" ]]; then
    echo "singular $label did not resolve $want" >&2
    cat "$binding_root/singular-$label.out" >&2
    exit 1
  fi
}

# The reproduction: a name that exists in both Projects resolves to this one.
binding_assert_singular_uid "$binding_ns_alpha_window_uid" window   describe window ns-one -o uid
# Explicit --project still wins, and outside tmux the whole-Registry ambiguity
# is exactly what it was before the namespace existed.
binding_assert_singular_uid "$binding_ns_beta_window_uid" window-explicit   describe window ns-one -p beta -o uid
set +e
binding_pmx describe window ns-one -o uid \
  >"$binding_root/singular-outside.out" 2>"$binding_root/singular-outside.err"
binding_singular_outside_status=$?
set -e
if [[ "$binding_singular_outside_status" == "0" ]] || [[ -s "$binding_root/singular-outside.out" ]]; then
  echo "outside tmux a duplicated Window name resolved instead of failing" >&2
  exit 1
fi
smoke_assert_file_contains "$binding_root/singular-outside.err" "matched 2 windows"

# The Project narrows the search; it never picks the target. Two same-named
# Panes inside alpha stay a bounded exact-one ambiguity, and the candidate
# listing must not reach into beta.
set +e
binding_inside_pmx describe pane shared -o uid \
  >"$binding_root/singular-ambiguous.out" 2>"$binding_root/singular-ambiguous.err"
binding_singular_ambiguous_status=$?
set -e
if [[ "$binding_singular_ambiguous_status" == "0" ]] || [[ -s "$binding_root/singular-ambiguous.out" ]]; then
  echo "an intra-Project Pane ambiguity was silently resolved" >&2
  exit 1
fi
smoke_assert_file_contains "$binding_root/singular-ambiguous.err" "matched 2 panes"
smoke_assert_file_contains "$binding_root/singular-ambiguous.err" "window/ns-one"
smoke_assert_file_contains "$binding_root/singular-ambiguous.err" "window/ns-two"
if grep -Fq "project/beta" "$binding_root/singular-ambiguous.err"; then
  echo "intra-Project ambiguity listed a beta candidate" >&2
  cat "$binding_root/singular-ambiguous.err" >&2
  exit 1
fi
binding_assert_singular_uid "$binding_ns_alpha_pane_uid" pane-scoped \
  describe pane shared -w ns-one -o uid
binding_assert_singular_uid "$binding_ns_sibling_pane_uid" pane-sibling \
  describe pane shared -w ns-two -o uid
binding_assert_singular_uid "$binding_ns_beta_pane_uid" pane-explicit \
  describe pane shared -p beta -o uid

# A uid that belongs to another Project is a no-match, not a cross-Project hit.
set +e
binding_inside_pmx describe window "uid:$binding_ns_beta_window_uid" -o uid \
  >"$binding_root/singular-foreign-uid.out" 2>"$binding_root/singular-foreign-uid.err"
binding_singular_foreign_status=$?
set -e
if [[ "$binding_singular_foreign_status" == "0" ]] || [[ -s "$binding_root/singular-foreign-uid.out" ]]; then
  echo "an out-of-scope Window uid resolved across Projects" >&2
  exit 1
fi
smoke_assert_file_contains "$binding_root/singular-foreign-uid.err" "matched no windows"

# rename reads the same universe, so the preview and the mutation agree.
binding_inside_pmx rename window ns-one --name ns-renamed >"$binding_root/singular-rename.out"
binding_assert_singular_uid "$binding_ns_alpha_window_uid" window-renamed \
  describe window ns-renamed -o uid
binding_assert_singular_uid "$binding_ns_beta_window_uid" window-beta-intact \
  describe window ns-one -p beta -o uid

# Inside tmux an underivable namespace refuses; it never widens back to the
# whole Registry, which is the cross-Project match this rule exists to prevent.
binding_tmux set-option -wu -t "$lifecycle_window" @projmux_window_uid
set +e
binding_inside_pmx describe window ns-renamed -o uid \
  >"$binding_root/singular-refusal.out" 2>"$binding_root/singular-refusal.err"
binding_singular_refusal_status=$?
set -e
binding_tmux set-option -wq -t "$lifecycle_window" @projmux_window_uid "$lifecycle_window_uid"
if [[ "$binding_singular_refusal_status" == "0" ]] || [[ -s "$binding_root/singular-refusal.out" ]]; then
  echo "a broken owner chain fell back to the global Window search" >&2
  exit 1
fi
smoke_assert_file_contains "$binding_root/singular-refusal.err" "the active Project namespace is undecidable"

set +e
binding_inside_pmx get panes --all >"$binding_root/bare-all.out" 2>"$binding_root/bare-all.err"
binding_bare_all_status=$?
set -e
if [[ "$binding_bare_all_status" == "0" ]] || [[ -s "$binding_root/bare-all.out" ]]; then
  echo "get panes accepted destructive bare --all" >&2
  exit 1
fi
smoke_assert_file_contains "$binding_root/bare-all.err" "flag provided but not defined: -all"

# A client is still inside tmux when its Window mirror is missing, so the read
# must refuse instead of falling back to the outside-tmux global inventory.
binding_tmux set-option -wu -t "$lifecycle_window" @projmux_window_uid
set +e
binding_inside_pmx get panes -o uid >"$binding_root/missing-binding.out" 2>"$binding_root/missing-binding.err"
binding_missing_status=$?
set -e
if [[ "$binding_missing_status" == "0" ]] || [[ -s "$binding_root/missing-binding.out" ]]; then
  echo "missing active Project binding fell back to global Pane inventory" >&2
  exit 1
fi
smoke_assert_file_contains "$binding_root/missing-binding.err" "carries no @projmux_window_uid"
binding_tmux set-option -wq -t "$lifecycle_window" @projmux_window_uid "$lifecycle_window_uid"

# Re-running the exact hidden lifecycle boundary with no new object is
# byte-write-free. Unit tests additionally inspect the generated hook and the
# fake tmux call log for zero set-option/rename calls.
cp "$binding_registry" "$binding_root/registry.before-repeat-hook"
binding_registry_stat="$(stat -c '%i:%s:%y' "$binding_registry")"
binding_repeat_converged=0
: >"$binding_root/converge-repeat.attempts.err"
for _ in $(seq 1 100); do
  binding_pmx internal tmux converge --socket-path "$binding_socket_path" --session "$binding_session_id" \
    --reason runtime-created 2>"$binding_root/converge-repeat.err"
  cat "$binding_root/converge-repeat.err" >>"$binding_root/converge-repeat.attempts.err"
  if grep -Fq "converged=true" "$binding_root/converge-repeat.err"; then
    binding_repeat_converged=1
    break
  fi
  sleep 0.05
done
if [[ "$binding_repeat_converged" != "1" ]]; then
  echo "repeat lifecycle convergence never acquired the coalesced worker lease" >&2
  cat "$binding_root/converge-repeat.attempts.err" >&2
  exit 1
fi
smoke_assert_file_contains "$binding_root/converge-repeat.err" "converged=true"
if ! cmp -s "$binding_root/registry.before-repeat-hook" "$binding_registry"; then
  echo "repeat lifecycle convergence changed Registry content" >&2
  diff -u "$binding_root/registry.before-repeat-hook" "$binding_registry" >&2 || true
  exit 1
fi
if [[ "$(stat -c '%i:%s:%y' "$binding_registry")" != "$binding_registry_stat" ]]; then
  echo "repeat lifecycle convergence rewrote byte-identical registry content" >&2
  exit 1
fi

# A hook burst is the load the lifecycle triggers are actually built for: both
# pane-exit hooks fire on every pane exit in every session and both creation
# hooks fire on every window and split, so many producers can arrive while one
# convergence is mid-flight. Every one of them must exit 0, at most one may
# converge, and the registry must end byte-identical to the converged state --
# with no `acquire lock: exhausted` anywhere, which is the failure mode a fleet
# of concurrent workers contending for the one registry lock produces.
cp "$binding_registry" "$binding_root/registry.before-burst"
binding_burst_pids=()
for binding_burst_index in 1 2 3 4 5 6 7 8; do
  binding_pmx internal tmux converge --socket-path "$binding_socket_path" \
    --session "$binding_session" --reason pane-killed \
    >"$binding_root/burst-$binding_burst_index.out" 2>"$binding_root/burst-$binding_burst_index.err" &
  binding_burst_pids+=("$!")
done
binding_burst_failed=0
for binding_burst_pid in "${binding_burst_pids[@]}"; do
  if ! wait "$binding_burst_pid"; then
    binding_burst_failed=1
  fi
done
if [[ "$binding_burst_failed" != "0" ]]; then
  echo "a hook burst producer exited non-zero" >&2
  cat "$binding_root"/burst-*.err >&2 || true
  exit 1
fi
binding_burst_deferred=0
binding_burst_converged=0
for binding_burst_index in 1 2 3 4 5 6 7 8; do
  if [[ -s "$binding_root/burst-$binding_burst_index.out" ]]; then
    echo "burst producer $binding_burst_index wrote to stdout" >&2
    cat "$binding_root/burst-$binding_burst_index.out" >&2
    exit 1
  fi
  if grep -q "exhausted" "$binding_root/burst-$binding_burst_index.err"; then
    echo "a hook burst exhausted the registry lock" >&2
    cat "$binding_root/burst-$binding_burst_index.err" >&2
    exit 1
  fi
  if grep -q "another controller worker holds" "$binding_root/burst-$binding_burst_index.err"; then
    binding_burst_deferred=$((binding_burst_deferred + 1))
  fi
  if grep -q "converged=true" "$binding_root/burst-$binding_burst_index.err"; then
    binding_burst_converged=$((binding_burst_converged + 1))
  fi
done
if [[ "$binding_burst_deferred" == "0" ]]; then
  echo "a burst of 8 producers coalesced none of them onto one worker" >&2
  cat "$binding_root"/burst-*.err >&2 || true
  exit 1
fi
if [[ "$binding_burst_converged" == "0" ]]; then
  echo "a burst of 8 producers reported no convergence" >&2
  cat "$binding_root"/burst-*.err >&2 || true
  exit 1
fi
if ! cmp -s "$binding_root/registry.before-burst" "$binding_registry"; then
  echo "a hook burst over an already-converged server rewrote the registry" >&2
  exit 1
fi
echo ">> controller trigger burst: 8 producers, $binding_burst_deferred coalesced, $binding_burst_converged converged"

# A read verb must not start a controller. `get` is the widest read the UI runs,
# and after it the registry is byte-identical and no controller event or lease
# exists for this server.
rm -rf "$binding_root/state/projmux/controller"
binding_pmx get panes -A -o uid >"$binding_root/read-isolation.out"
if ! cmp -s "$binding_root/registry.before-burst" "$binding_registry"; then
  echo "a read verb mutated the registry" >&2
  exit 1
fi
if [[ -d "$binding_root/state/projmux/controller" ]]; then
  echo "a read verb started a controller worker" >&2
  ls -la "$binding_root/state/projmux/controller" >&2
  exit 1
fi

binding_second_after="$(binding_second_tmux show-options -gqv @projmux_phase3_sentinel):$(binding_second_tmux list-windows -a -F '#{session_name}:#{window_id}:#{@projmux_window_uid}')"
if [[ "$binding_second_after" != "$binding_second_before" ]]; then
  echo "primary apply/lifecycle touched the second socket" >&2
  exit 1
fi

binding_cleanup
trap smoke_cleanup_env EXIT
echo ">> managed binding e2e passed: socket=$binding_socket path=$binding_socket_path"

# ---------------------------------------------------------------------------
# Window resource + exact live-binding deletion.
#
# This block owns a fresh smoke root. The public command discovers the explicit
# run-unique `-L` route, binds its observed physical socket, then pins live
# inventory and every mutation to that exact `-S` path. TMUX_TMPDIR keeps the
# discovery name unique, and cleanup independently verifies the physical path.
# External calls strip inherited TMUX/TMUX_PANE. The self-target call runs in
# its own exact managed Window and proves its stdout/Registry result survives
# the Window disappearing.
# ---------------------------------------------------------------------------
delete_root="$PROJMUX_SMOKE_WORKDIR/delete-window-e2e"
delete_socket="projmux-delete-$RANDOM-$$"
delete_other_socket="projmux-delete-other-$RANDOM-$$"
delete_product_socket="projmux"
mkdir -p \
  "$delete_root/home" \
  "$delete_root/config" \
  "$delete_root/state" \
  "$delete_root/runtime" \
  "$delete_root/tmux" \
  "$delete_root/work/alpha" \
  "$delete_root/work/beta" \
  "$delete_root/work/gamma"
chmod 0700 "$delete_root/runtime" "$delete_root/tmux"

delete_real_tmux="$(command -v tmux)"
delete_shim="$delete_root/shim"
delete_shim_log="$delete_root/tmux-shim.calls"
mkdir -p "$delete_shim"
cat >"$delete_shim/tmux" <<DELETE_SHIM
#!/usr/bin/env bash
printf '%s\n' "\$*" >>$(printf %q "$delete_shim_log")
if [[ "\${1:-}" == "-L" && "\${2:-}" == $(printf %q "$delete_product_socket") ]]; then
  shift 2
  exec $(printf %q "$delete_real_tmux") -L $(printf %q "$delete_socket") "\$@"
fi
if [[ "\${1:-}" == "-L" || "\${1:-}" == "-S" ]]; then
  exec $(printf %q "$delete_real_tmux") "\$@"
fi
exec $(printf %q "$delete_real_tmux") -L $(printf %q "$delete_socket") "\$@"
DELETE_SHIM
chmod 0755 "$delete_shim/tmux"

delete_tmux() {
  env -u TMUX -u TMUX_PANE \
    HOME="$delete_root/home" \
    XDG_CONFIG_HOME="$delete_root/config" \
    XDG_STATE_HOME="$delete_root/state" \
    XDG_RUNTIME_DIR="$delete_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$delete_root/work" \
    TMUX_TMPDIR="$delete_root/tmux" \
    PATH="$delete_shim:$PATH" \
    SHELL=/bin/sh \
    "$delete_real_tmux" -L "$delete_socket" "$@"
}

delete_other_tmux() {
  env -u TMUX -u TMUX_PANE \
    HOME="$delete_root/home" \
    XDG_CONFIG_HOME="$delete_root/config" \
    XDG_STATE_HOME="$delete_root/state" \
    XDG_RUNTIME_DIR="$delete_root/runtime" \
    TMUX_TMPDIR="$delete_root/tmux" \
    SHELL=/bin/sh \
    "$delete_real_tmux" -L "$delete_other_socket" "$@"
}

# `delete` names the exact server it mutates. Passing the run-unique socket is
# what proves the route no longer defaults to the app's own `-L projmux`: the
# shim below would map that default onto this same server, so a fallback would
# be invisible without the explicit flag.
delete_pmx_delete() {
  local kind="$1"
  shift
  delete_pmx delete "$kind" --socket "$delete_socket" "$@"
}

delete_pmx() {
  env -u TMUX -u TMUX_PANE \
    HOME="$delete_root/home" \
    XDG_CONFIG_HOME="$delete_root/config" \
    XDG_STATE_HOME="$delete_root/state" \
    XDG_RUNTIME_DIR="$delete_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$delete_root/work" \
    TMUX_TMPDIR="$delete_root/tmux" \
    PATH="$delete_shim:$PATH" \
    SHELL=/bin/sh \
    "$bin" "$@"
}

delete_tmux new-session -d -s work-alpha -n primary -c "$delete_root/work/alpha" sleep 600
delete_tmux set-option -t work-alpha -q @projmux_project_path "$delete_root/work/alpha"
delete_tmux new-window -d -t work-alpha: -n sibling -c "$delete_root/work/alpha" sleep 600
delete_tmux new-session -d -s work-beta -n only -c "$delete_root/work/beta" sleep 600
delete_tmux set-option -t work-beta -q @projmux_project_path "$delete_root/work/beta"
delete_other_tmux new-session -d -s foreign-delete -n untouched sleep 600
delete_other_tmux set-option -gq @projmux_delete_sentinel untouched

delete_socket_path="$(delete_tmux display-message -p -t work-alpha '#{socket_path}')"
delete_other_socket_path="$(delete_other_tmux display-message -p -t foreign-delete '#{socket_path}')"
case "$delete_socket_path" in
  "$delete_root"/*) ;;
  *)
    echo "delete Window e2e socket escaped the smoke root: $delete_socket_path" >&2
    exit 1
    ;;
esac
case "$delete_other_socket_path" in
  "$delete_root"/*) ;;
  *)
    echo "delete Pane/Agent e2e second socket escaped the smoke root: $delete_other_socket_path" >&2
    exit 1
    ;;
esac
delete_other_before="$(delete_other_tmux show-options -gqv @projmux_delete_sentinel):$(delete_other_tmux list-windows -a -F '#{session_name}:#{window_id}')"
echo ">> delete resource e2e socket=$delete_socket path=$delete_socket_path other-socket=$delete_other_socket other-path=$delete_other_socket_path"

delete_cleanup() {
  local actual other_actual
  actual="$(delete_tmux display-message -p '#{socket_path}' 2>/dev/null || true)"
  if [[ -n "$actual" ]]; then
    case "$actual" in
      "$delete_root"/*)
        env -u TMUX -u TMUX_PANE tmux -S "$actual" kill-server >/dev/null 2>&1 || true
        ;;
      *)
        echo "refusing delete Window e2e cleanup outside the smoke root: $actual" >&2
        ;;
    esac
  fi
  other_actual="$(delete_other_tmux display-message -p '#{socket_path}' 2>/dev/null || true)"
  if [[ -n "$other_actual" ]]; then
    case "$other_actual" in
      "$delete_root"/*)
        env -u TMUX -u TMUX_PANE tmux -S "$other_actual" kill-server >/dev/null 2>&1 || true
        ;;
      *)
        echo "refusing delete Pane/Agent e2e second cleanup outside the smoke root: $other_actual" >&2
        ;;
    esac
  fi
}
trap 'delete_cleanup; smoke_cleanup_env' EXIT

delete_config="$delete_root/config/projmux/tmux.conf"
delete_pmx reconcile resources --socket "$delete_socket" --dry-run -o json >"$delete_root/d2.json"
smoke_assert_file_contains "$delete_root/d2.json" '"outcome": "no-op"'
if [[ -e "$delete_root/state/projmux/metadata/registry.json" ]]; then
  echo "delete D2 dry-run created a Registry" >&2
  exit 1
fi
delete_pmx create project --root "$delete_root/work/alpha" --name alpha >"$delete_root/register-alpha.out"
delete_pmx create project --root "$delete_root/work/beta" --name beta >"$delete_root/register-beta.out"
delete_pmx internal tmux apply --bin "$bin" --config "$delete_config" --socket "$delete_socket" \
  >"$delete_root/apply.out"
smoke_assert_file_contains "$delete_root/apply.out" "reloaded tmux server -L $delete_socket"
# Apply binds the explicitly registered default topology. Its explicit repeat
# is empty before the raw sibling is replaced by a canonical managed Window.
delete_pmx reconcile resources --socket "$delete_socket" -o json >"$delete_root/reconcile-1.json"
smoke_assert_file_contains "$delete_root/reconcile-1.json" '"outcome": "no-op"'
smoke_assert_file_contains "$delete_root/reconcile-1.json" '"items": []'
delete_tmux kill-window -t work-alpha:sibling
delete_pmx create window --project alpha --name sibling >"$delete_root/create-sibling.out"

delete_primary="$(delete_tmux display-message -p -t work-alpha:primary '#{window_id}')"
delete_sibling="$(delete_tmux display-message -p -t work-alpha:sibling '#{window_id}')"
delete_beta="$(delete_tmux display-message -p -t work-beta:only '#{window_id}')"
delete_primary_uid="$(delete_tmux show-options -wqv -t "$delete_primary" @projmux_window_uid)"
delete_sibling_uid="$(delete_tmux show-options -wqv -t "$delete_sibling" @projmux_window_uid)"
delete_beta_uid="$(delete_tmux show-options -wqv -t "$delete_beta" @projmux_window_uid)"
delete_alpha_project_uid="$(delete_pmx describe project alpha -o uid)"
delete_beta_project_uid="$(delete_pmx describe project beta -o uid)"
if [[ -z "$delete_primary_uid" || -z "$delete_sibling_uid" || -z "$delete_beta_uid" || -z "$delete_alpha_project_uid" || -z "$delete_beta_project_uid" ]]; then
  echo "delete Window e2e apply left an identity mirror empty" >&2
  exit 1
fi

# The active-Project namespace of a singular reference is a Registry rule, not
# a transport one. This block is the app-owned host: the shim maps the product
# `-L projmux` spelling onto this exact server, and the reads below are the
# same three shapes the standalone-host block above ran, so a host-dependent
# scope would show up as a different answer to identical argv. They are
# deliberately read-only -- no resource is created, renamed, or removed -- so
# the delete fixture this block owns is unchanged.
delete_server_pid="$(delete_tmux display-message -p -t work-alpha:primary '#{pid}')"
delete_primary_pane="$(delete_tmux display-message -p -t work-alpha:primary '#{pane_id}')"
delete_pmx_inside() {
  env \
    HOME="$delete_root/home" \
    XDG_CONFIG_HOME="$delete_root/config" \
    XDG_STATE_HOME="$delete_root/state" \
    XDG_RUNTIME_DIR="$delete_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$delete_root/work" \
    TMUX_TMPDIR="$delete_root/tmux" \
    TMUX="$delete_socket_path,$delete_server_pid,0" \
    TMUX_PANE="$delete_primary_pane" \
    PATH="$delete_shim:$PATH" \
    SHELL=/bin/sh \
    "$bin" "$@"
}

# Explicit registration allocates stable Window metadata.name independently of
# tmux window_name, so the references are read back from the Registry
# rather than assumed from the session layout above.
delete_primary_name="$(delete_pmx describe window "uid:$delete_primary_uid" -o name | tr -d '[:space:]')"
delete_beta_name="$(delete_pmx describe window "uid:$delete_beta_uid" -o name | tr -d '[:space:]')"
if [[ -z "$delete_primary_name" ]] || [[ -z "$delete_beta_name" ]]; then
  echo "app-owned host could not read back a Window name" >&2
  exit 1
fi

if [[ "$(delete_pmx_inside describe window "$delete_primary_name" -o uid | tr -d '[:space:]')" != "$delete_primary_uid" ]]; then
  echo "app-owned host did not resolve the reference inside the active Project" >&2
  exit 1
fi
if [[ "$(delete_pmx_inside describe window "$delete_beta_name" -p beta -o uid | tr -d '[:space:]')" != "$delete_beta_uid" ]]; then
  echo "app-owned host ignored an explicit --project" >&2
  exit 1
fi
# beta's Window name is resolved in beta only when beta is named. From inside
# alpha the very same word must never reach beta's uid -- whether alpha happens
# to hold that name too (a different uid) or holds nothing (a no-match).
set +e
delete_pmx_inside describe window "$delete_beta_name" -o uid \
  >"$delete_root/singular-foreign.out" 2>"$delete_root/singular-foreign.err"
delete_singular_status=$?
set -e
if [[ "$(tr -d '[:space:]' <"$delete_root/singular-foreign.out")" == "$delete_beta_uid" ]]; then
  echo "app-owned host resolved a Window owned by another Project" >&2
  exit 1
fi
if [[ "$delete_singular_status" != "0" ]]; then
  smoke_assert_file_contains "$delete_root/singular-foreign.err" "matched no windows"
fi

delete_registry="$delete_root/state/projmux/metadata/registry.json"
cp "$delete_registry" "$delete_root/registry.before-dry-run"
delete_tmux list-windows -a -F '#{session_name}:#{window_id}:#{@projmux_window_uid}' \
  >"$delete_root/windows.before-dry-run"
delete_pmx_delete window "uid:$delete_primary_uid" --dry-run >"$delete_root/external-dry-run.out"
cmp "$delete_root/registry.before-dry-run" "$delete_registry"
delete_tmux list-windows -a -F '#{session_name}:#{window_id}:#{@projmux_window_uid}' \
  >"$delete_root/windows.after-dry-run"
cmp "$delete_root/windows.before-dry-run" "$delete_root/windows.after-dry-run"
smoke_assert_file_contains "$delete_root/external-dry-run.out" "live would kill tmux window $delete_primary"
if grep -Fq "last live Window" "$delete_root/external-dry-run.out"; then
  echo "two-Window dry-run incorrectly predicted session termination" >&2
  exit 1
fi

delete_pmx_delete window "uid:$delete_primary_uid" --yes >"$delete_root/external.out"
if [[ "$(delete_tmux display-message -p -t "$delete_primary" '#{window_id}' 2>/dev/null || true)" == "$delete_primary" ]]; then
  echo "external Window delete left $delete_primary live" >&2
  exit 1
fi
if [[ "$(delete_tmux display-message -p -t "$delete_sibling" '#{window_id}')" != "$delete_sibling" ]]; then
  echo "external Window delete changed sibling $delete_sibling" >&2
  exit 1
fi
delete_pmx get windows --all-projects -o uid >"$delete_root/windows.after-external"
if grep -Fqx "$delete_primary_uid" "$delete_root/windows.after-external" || \
  ! grep -Fqx "$delete_sibling_uid" "$delete_root/windows.after-external"; then
  echo "external Window delete did not preserve exact registry sibling" >&2
  exit 1
fi
delete_pmx get projects -o uid >"$delete_root/projects.after-external"
smoke_assert_file_contains "$delete_root/projects.after-external" "$delete_alpha_project_uid"
smoke_assert_file_contains "$delete_root/external.out" "live killed tmux window $delete_primary"

# The only beta Window explicitly predicts and then causes the canonical
# Project root cascade. Explicit Window deletion allocates no replacement
# Window/shell; alpha and the foreign socket remain byte-semantically untouched.
{
  delete_pmx get windows --project "uid:$delete_alpha_project_uid" -o uid
  delete_pmx get panes --project "uid:$delete_alpha_project_uid" -o uid
  delete_pmx get agents --project "uid:$delete_alpha_project_uid" -o uid
} | sort >"$delete_root/alpha-graph.before-beta-delete"
delete_other_before_last_window="$(delete_other_tmux show-options -gqv @projmux_delete_sentinel):$(delete_other_tmux list-windows -a -F '#{session_name}:#{window_id}')"
delete_pmx_delete window "uid:$delete_beta_uid" --dry-run >"$delete_root/last-dry-run.out"
smoke_assert_file_contains "$delete_root/last-dry-run.out" "live cascade would end Project session work-beta"
delete_pmx_delete window "uid:$delete_beta_uid" --yes >"$delete_root/last.out"
if delete_tmux has-session -t work-beta 2>/dev/null; then
  echo "last-Window delete left work-beta session live" >&2
  exit 1
fi
smoke_assert_file_contains "$delete_root/last.out" "live cascade ended Project session work-beta"
if grep -Fq "retryable drift" "$delete_root/last.out"; then
  echo "last-Window delete reported retryable drift after a successful root cascade" >&2
  exit 1
fi
delete_pmx get projects -o uid >"$delete_root/projects.after-last-delete"
delete_pmx get windows --all-projects -o uid >"$delete_root/windows.after-last-delete"
if grep -Fqx "$delete_beta_project_uid" "$delete_root/projects.after-last-delete" ||
  grep -Fqx "$delete_beta_uid" "$delete_root/windows.after-last-delete"; then
  echo "last-Window canonical root cascade retained beta identity" >&2
  exit 1
fi
if [[ "$(wc -l <"$delete_root/windows.after-last-delete" | tr -d '[:space:]')" != "1" ]] ||
  ! grep -Fqx "$delete_sibling_uid" "$delete_root/windows.after-last-delete"; then
  echo "last-Window root cascade allocated a replacement Window or changed alpha: $(cat "$delete_root/windows.after-last-delete")" >&2
  exit 1
fi
{
  delete_pmx get windows --project "uid:$delete_alpha_project_uid" -o uid
  delete_pmx get panes --project "uid:$delete_alpha_project_uid" -o uid
  delete_pmx get agents --project "uid:$delete_alpha_project_uid" -o uid
} | sort >"$delete_root/alpha-graph.after-beta-delete"
cmp "$delete_root/alpha-graph.before-beta-delete" "$delete_root/alpha-graph.after-beta-delete"
delete_other_after_last_window="$(delete_other_tmux show-options -gqv @projmux_delete_sentinel):$(delete_other_tmux list-windows -a -F '#{session_name}:#{window_id}')"
if [[ "$delete_other_after_last_window" != "$delete_other_before_last_window" ]]; then
  echo "explicit last-Window root cascade touched the foreign socket" >&2
  exit 1
fi

# A managed runner Window invokes implicit delete from inside itself. There is
# intentionally no post-command marker: a correct implementation flushes the
# CLI result first and then the queued exact kill removes the shell that would
# have written such a marker.
self_script="$delete_root/self-delete.sh"
cat >"$self_script" <<SELF_DELETE
#!/usr/bin/env bash
while [[ ! -e $(printf %q "$delete_root/self-start") ]]; do sleep 0.01; done
HOME=$(printf %q "$delete_root/home") \
XDG_CONFIG_HOME=$(printf %q "$delete_root/config") \
XDG_STATE_HOME=$(printf %q "$delete_root/state") \
    XDG_RUNTIME_DIR=$(printf %q "$delete_root/runtime") \
    PROJMUX_MANAGED_ROOTS=$(printf %q "$delete_root/work") \
    TMUX_TMPDIR=$(printf %q "$delete_root/tmux") \
    PATH=$(printf %q "$delete_shim"):\$PATH \
SHELL=/bin/sh \
$(printf %q "$bin") delete window --socket $(printf %q "$delete_socket") --yes >$(printf %q "$delete_root/self.out") 2>$(printf %q "$delete_root/self.err")
SELF_DELETE
chmod 0755 "$self_script"
self_uid="$(
  delete_pmx create window --project "uid:$delete_alpha_project_uid" --name self-delete \
    -o uid -- "$self_script"
)"
self_window="$(
  delete_tmux list-windows -a -F '#{window_id}|#{@projmux_window_uid}' |
    awk -F '|' -v uid="$self_uid" '$2 == uid { print $1 }'
)"
if [[ -z "$self_uid" ]] || [[ -z "$self_window" ]]; then
  echo "canonical self-target Window create left its identity empty" >&2
  exit 1
fi
touch "$delete_root/self-start"
for _ in {1..200}; do
  if [[ -s "$delete_root/self.out" ]] && \
    [[ "$(delete_tmux display-message -p -t "$self_window" '#{window_id}' 2>/dev/null || true)" != "$self_window" ]]; then
    break
  fi
  sleep 0.05
done
if [[ ! -s "$delete_root/self.out" ]]; then
  echo "self-target delete left no durable stdout" >&2
  cat "$delete_root/self.err" >&2 || true
  exit 1
fi
if [[ "$(delete_tmux display-message -p -t "$self_window" '#{window_id}' 2>/dev/null || true)" == "$self_window" ]]; then
  echo "self-target delete left $self_window live" >&2
  exit 1
fi
delete_pmx get windows --all-projects -o uid >"$delete_root/windows.after-self"
if grep -Fqx "$self_uid" "$delete_root/windows.after-self"; then
  echo "self-target delete left Registry uid $self_uid" >&2
  exit 1
fi
if ! grep -Fqx "$delete_sibling_uid" "$delete_root/windows.after-self"; then
  echo "self-target delete changed sibling uid $delete_sibling_uid" >&2
  exit 1
fi
smoke_assert_file_contains "$delete_root/self.out" "will queue after this result is flushed to kill tmux window $self_window"

# Pane deletion removes one exact live split and Registry resource while the
# sibling Pane, owning Window, Project, and foreign socket remain unchanged.
delete_sibling_shell="$(delete_tmux display-message -p -t "$delete_sibling" '#{pane_id}')"
delete_sibling_shell_uid="$(delete_tmux show-options -pqv -t "$delete_sibling_shell" @projmux_pane_uid)"
delete_split="$(delete_pmx create pane --project "uid:$delete_alpha_project_uid" --window "uid:$delete_sibling_uid" -o pane-id -- sleep 600)"
delete_split_uid="$(delete_tmux show-options -pqv -t "$delete_split" @projmux_pane_uid)"
echo ">> delete Pane sibling target pane=$delete_split uid=$delete_split_uid"
delete_pmx_delete pane "uid:$delete_split_uid" --dry-run >"$delete_root/pane-sibling-dry-run.out"
smoke_assert_file_contains "$delete_root/pane-sibling-dry-run.out" "live would kill tmux pane $delete_split pane-uid=$delete_split_uid"
if grep -Fq "last live Pane" "$delete_root/pane-sibling-dry-run.out"; then
  echo "sibling Pane dry-run incorrectly predicted Window termination" >&2
  exit 1
fi
delete_pmx_delete pane "uid:$delete_split_uid" --yes >"$delete_root/pane-sibling.out"
if [[ "$(delete_tmux display-message -p -t "$delete_split" '#{pane_id}' 2>/dev/null || true)" == "$delete_split" ]] || \
  [[ "$(delete_tmux display-message -p -t "$delete_sibling_shell" '#{pane_id}')" != "$delete_sibling_shell" ]]; then
  echo "exact Pane delete removed the wrong sibling or left its target live" >&2
  exit 1
fi
# The generated after-kill-pane liveness sweep is intentionally backgrounded.
# Let that already-triggered inventory transaction settle before introducing a
# new Agent resource, so this test does not ask a pre-Agent snapshot to judge a
# Pane created after the snapshot was taken.
sleep 0.5

# A real provider-shaped managed Pane exercises both ownership outcomes: Pane
# delete keeps its Agent Offline, while Agent delete cascades through its exact
# managed Pane and preserves the shell sibling.
cat >"$delete_shim/codex" <<'DELETE_CODEX_STUB'
#!/usr/bin/env bash
exec sleep 600
DELETE_CODEX_STUB
chmod 0755 "$delete_shim/codex"
delete_agent_pane="$(delete_pmx create agent --provider codex --project "uid:$delete_alpha_project_uid" --window "uid:$delete_sibling_uid" -o pane-id)"
delete_agent_uid="$(delete_pmx get agents --project "uid:$delete_alpha_project_uid" --window "uid:$delete_sibling_uid" -o uid | tail -n 1)"
delete_agent_pane_uid="$(delete_tmux show-options -pqv -t "$delete_agent_pane" @projmux_pane_uid)"
echo ">> delete managed Pane target agent=$delete_agent_uid pane=$delete_agent_pane uid=$delete_agent_pane_uid"
delete_pmx_delete pane "uid:$delete_agent_pane_uid" --yes >"$delete_root/managed-pane.out"
delete_pmx describe agent "uid:$delete_agent_uid" -o json >"$delete_root/managed-agent-offline.json"
smoke_assert_file_contains "$delete_root/managed-agent-offline.json" '"phase": "Offline"'
if grep -Fq '"paneRef"' "$delete_root/managed-agent-offline.json"; then
  echo "managed Pane delete left Agent paneRef behind" >&2
  exit 1
fi
sleep 0.5

delete_agent_two_pane="$(delete_pmx create agent --provider codex --project "uid:$delete_alpha_project_uid" --window "uid:$delete_sibling_uid" -o pane-id)"
delete_agent_two_uid="$(delete_pmx get agents --project "uid:$delete_alpha_project_uid" --window "uid:$delete_sibling_uid" -o uid | tail -n 1)"
delete_agent_two_pane_uid="$(delete_tmux show-options -pqv -t "$delete_agent_two_pane" @projmux_pane_uid)"
echo ">> delete Agent target agent=$delete_agent_two_uid pane=$delete_agent_two_pane uid=$delete_agent_two_pane_uid"
delete_pmx_delete agent "uid:$delete_agent_two_uid" --dry-run >"$delete_root/agent-dry-run.out"
smoke_assert_file_contains "$delete_root/agent-dry-run.out" "cascade pane/"
smoke_assert_file_contains "$delete_root/agent-dry-run.out" "live would kill tmux pane $delete_agent_two_pane pane-uid=$delete_agent_two_pane_uid"
delete_pmx_delete agent "uid:$delete_agent_two_uid" --yes >"$delete_root/agent.out"
if [[ "$(delete_tmux display-message -p -t "$delete_agent_two_pane" '#{pane_id}' 2>/dev/null || true)" == "$delete_agent_two_pane" ]]; then
  echo "Agent delete left managed Pane $delete_agent_two_pane live" >&2
  exit 1
fi
delete_pmx get agents --project "uid:$delete_alpha_project_uid" --window "uid:$delete_sibling_uid" -o uid >"$delete_root/agents.after-delete"
if grep -Fqx "$delete_agent_two_uid" "$delete_root/agents.after-delete"; then
  echo "Agent delete left Registry uid $delete_agent_two_uid" >&2
  exit 1
fi
if [[ "$(delete_tmux display-message -p -t "$delete_sibling_shell" '#{pane_id}')" != "$delete_sibling_shell" ]]; then
  echo "Agent delete changed shell sibling $delete_sibling_shell" >&2
  exit 1
fi

# Exact runtime absence plus durable lifecycle evidence authorizes only a
# Registry-only Pane/Agent delete. Exercise the Pane while the socket is
# app-owned, then the Agent with the app marker removed (standalone), keeping a
# live Agent and the complete already-absent tmux inventory byte-identical.
delete_live_agent_pane="$(delete_pmx create agent --provider codex --project "uid:$delete_alpha_project_uid" --window "uid:$delete_sibling_uid" -o pane-id)"
delete_live_agent_uid="$(delete_pmx get agents --project "uid:$delete_alpha_project_uid" --window "uid:$delete_sibling_uid" -o uid | tail -n 1)"
delete_live_agent_pane_uid="$(delete_tmux show-options -pqv -t "$delete_live_agent_pane" @projmux_pane_uid)"
delete_offline_pane="$(delete_pmx create pane --project "uid:$delete_alpha_project_uid" --window "uid:$delete_sibling_uid" -o pane-id -- sleep 600)"
delete_offline_pane_uid="$(delete_tmux show-options -pqv -t "$delete_offline_pane" @projmux_pane_uid)"
if [[ "$(delete_tmux show-options -gqv @projmux_app)" != "1" ]]; then
  echo "offline Pane fixture is not on the app-owned host" >&2
  exit 1
fi
delete_tmux kill-pane -t "$delete_offline_pane"
delete_offline_pane_ready=0
for _ in {1..200}; do
  if delete_pmx describe pane "uid:$delete_offline_pane_uid" -o json >"$delete_root/offline-pane.json" 2>/dev/null && \
    grep -Fq '"type": "MissingRuntime"' "$delete_root/offline-pane.json" && \
    grep -Fq '"reason": "RuntimeUnbound"' "$delete_root/offline-pane.json"; then
    delete_offline_pane_ready=1
    break
  fi
  sleep 0.05
done
if [[ "$delete_offline_pane_ready" != "1" ]]; then
  echo "raw Pane loss did not converge to durable MissingRuntime evidence" >&2
  exit 1
fi
delete_offline_agent_pane="$(delete_pmx create agent --provider codex --project "uid:$delete_alpha_project_uid" --window "uid:$delete_sibling_uid" -o pane-id)"
delete_offline_agent_uid="$(delete_pmx get agents --project "uid:$delete_alpha_project_uid" --window "uid:$delete_sibling_uid" -o uid | tail -n 1)"
delete_offline_agent_pane_uid="$(delete_tmux show-options -pqv -t "$delete_offline_agent_pane" @projmux_pane_uid)"
delete_tmux kill-pane -t "$delete_offline_agent_pane"
delete_offline_agent_ready=0
for _ in {1..200}; do
  if delete_pmx describe agent "uid:$delete_offline_agent_uid" -o json >"$delete_root/offline-agent.json" 2>/dev/null && \
    delete_pmx describe pane "uid:$delete_offline_agent_pane_uid" -o json >"$delete_root/offline-agent-pane.json" 2>/dev/null && \
    grep -Fq '"phase": "Offline"' "$delete_root/offline-agent.json" && \
    grep -Fq '"type": "MissingRuntime"' "$delete_root/offline-agent-pane.json"; then
    delete_offline_agent_ready=1
    break
  fi
  sleep 0.05
done
if [[ "$delete_offline_agent_ready" != "1" ]]; then
  echo "raw Agent Pane loss did not converge to Offline/MissingRuntime evidence" >&2
  exit 1
fi
delete_tmux list-panes -a -F '#{session_id}:#{window_id}:#{pane_id}:#{@projmux_pane_uid}' | sort >"$delete_root/offline-pane.tmux-before"
{
  delete_pmx get projects -o uid
  delete_pmx get windows --all-projects -o uid
  delete_pmx get panes --all-projects -o uid
  delete_pmx get agents --all-projects -o uid
} | grep -Fvx -e "$delete_offline_pane_uid" -e "$delete_offline_agent_uid" -e "$delete_offline_agent_pane_uid" | sort >"$delete_root/offline-sibling-graph.before"
sha256sum "$delete_root/offline-sibling-graph.before" >"$delete_root/offline-sibling-graph.before.sha256"
cp "$delete_registry" "$delete_root/registry.before-offline-pane-dry-run"
delete_pmx_delete pane "uid:$delete_offline_pane_uid" --dry-run >"$delete_root/offline-pane-dry-run.out"
cmp "$delete_root/registry.before-offline-pane-dry-run" "$delete_registry"
smoke_assert_file_contains "$delete_root/offline-pane-dry-run.out" "registry-only would delete this Pane; no tmux Pane would be killed"
delete_pmx_delete pane "uid:$delete_offline_pane_uid" --yes >"$delete_root/offline-pane.out"
smoke_assert_file_contains "$delete_root/offline-pane.out" "registry-only deleted this Pane; no tmux Pane was killed"
delete_tmux list-panes -a -F '#{session_id}:#{window_id}:#{pane_id}:#{@projmux_pane_uid}' | sort >"$delete_root/offline-pane.tmux-after"
cmp "$delete_root/offline-pane.tmux-before" "$delete_root/offline-pane.tmux-after"
if delete_pmx_delete pane "uid:$delete_offline_pane_uid" --yes >"$delete_root/offline-pane-repeat.out" 2>"$delete_root/offline-pane-repeat.err"; then
  echo "repeat offline Pane delete unexpectedly succeeded" >&2
  exit 1
fi
smoke_assert_file_contains "$delete_root/offline-pane-repeat.err" "matched no panes"

delete_tmux set-option -gqu @projmux_app
if [[ -n "$(delete_tmux show-options -gqv @projmux_app)" ]]; then
  echo "offline Agent fixture did not enter standalone host mode" >&2
  exit 1
fi
delete_tmux list-panes -a -F '#{session_id}:#{window_id}:#{pane_id}:#{@projmux_pane_uid}' | sort >"$delete_root/offline-agent.tmux-before"
delete_pmx_delete agent "uid:$delete_offline_agent_uid" --dry-run >"$delete_root/offline-agent-dry-run.out"
smoke_assert_file_contains "$delete_root/offline-agent-dry-run.out" "registry-only would delete this Agent; no tmux Pane would be killed"
smoke_assert_file_contains "$delete_root/offline-agent-dry-run.out" "evidence=Offline+MissingRuntime"
delete_pmx_delete agent "uid:$delete_offline_agent_uid" --yes >"$delete_root/offline-agent.out"
smoke_assert_file_contains "$delete_root/offline-agent.out" "registry-only deleted this Agent; no tmux Pane was killed"
delete_tmux list-panes -a -F '#{session_id}:#{window_id}:#{pane_id}:#{@projmux_pane_uid}' | sort >"$delete_root/offline-agent.tmux-after"
cmp "$delete_root/offline-agent.tmux-before" "$delete_root/offline-agent.tmux-after"
delete_pmx describe agent "uid:$delete_live_agent_uid" -o json >"$delete_root/live-agent.after-offline-deletes.json"
smoke_assert_file_contains "$delete_root/live-agent.after-offline-deletes.json" '"phase": "Running"'
smoke_assert_file_contains "$delete_root/live-agent.after-offline-deletes.json" "\"paneRef\": \"$delete_live_agent_pane_uid\""
{
  delete_pmx get projects -o uid
  delete_pmx get windows --all-projects -o uid
  delete_pmx get panes --all-projects -o uid
  delete_pmx get agents --all-projects -o uid
} | sort >"$delete_root/offline-sibling-graph.after"
sha256sum "$delete_root/offline-sibling-graph.after" >"$delete_root/offline-sibling-graph.after.sha256"
cmp "$delete_root/offline-sibling-graph.before" "$delete_root/offline-sibling-graph.after"
if [[ "$(cut -d ' ' -f 1 <"$delete_root/offline-sibling-graph.before.sha256")" != \
  "$(cut -d ' ' -f 1 <"$delete_root/offline-sibling-graph.after.sha256")" ]]; then
  echo "offline Pane/Agent delete changed the sibling Project/Window/Pane/Agent graph hash" >&2
  exit 1
fi
if delete_pmx_delete agent "uid:$delete_offline_agent_uid" --yes >"$delete_root/offline-agent-repeat.out" 2>"$delete_root/offline-agent-repeat.err"; then
  echo "repeat offline Agent delete unexpectedly succeeded" >&2
  exit 1
fi
smoke_assert_file_contains "$delete_root/offline-agent-repeat.err" "matched no agents"
delete_tmux set-option -gq @projmux_app 1
for delete_offline_target in "$delete_offline_pane" "$delete_offline_agent_pane"; do
  for delete_forbidden_route in "-L $delete_socket" "-L $delete_product_socket" "-S $delete_socket_path"; do
    if grep -Fq -- "$delete_forbidden_route kill-pane -t $delete_offline_target" "$delete_shim_log"; then
      echo "offline Pane/Agent canonical delete issued raw tmux cleanup: $delete_forbidden_route target=$delete_offline_target" >&2
      exit 1
    fi
  done
done

# Raw runtime loss leaves desired topology in the Registry. Canonical Window
# delete must retire that graph without issuing a tmux kill. Include a shell
# Pane and Agent descendant, then prove dry-run, repeat, sibling, and socket
# containment around the Registry-only path. The pane-exited lifecycle may
# already retire the Agent's dead managed Pane before this command; the unit
# cascade fixture separately pins the full Agent-owned Pane case.
delete_offline_window_uid="$(
  delete_pmx create window --project "uid:$delete_alpha_project_uid" --name offline-delete -o uid -- sleep 600
)"
delete_offline_window="$(
  delete_tmux list-windows -a -F '#{window_id}|#{@projmux_window_uid}' |
    awk -F '|' -v uid="$delete_offline_window_uid" '$2 == uid { print $1 }'
)"
delete_offline_shell="$(delete_tmux display-message -p -t "$delete_offline_window" '#{pane_id}')"
delete_offline_shell_uid="$(delete_tmux show-options -pqv -t "$delete_offline_shell" @projmux_pane_uid)"
delete_offline_agent_pane="$(delete_pmx create agent --provider codex --project "uid:$delete_alpha_project_uid" --window "uid:$delete_offline_window_uid" -o pane-id)"
delete_offline_agent_uid="$(delete_pmx get agents --project "uid:$delete_alpha_project_uid" --window "uid:$delete_offline_window_uid" -o uid | tail -n 1)"
if [[ -z "$delete_offline_window_uid" || -z "$delete_offline_shell_uid" || -z "$delete_offline_agent_pane" || -z "$delete_offline_agent_uid" ]]; then
  echo "offline Window delete fixture has an empty Registry identity" >&2
  exit 1
fi
delete_tmux kill-window -t "$delete_offline_window"
sleep 0.5

cp "$delete_registry" "$delete_root/registry.before-offline-dry-run"
delete_tmux list-windows -a -F '#{session_name}:#{window_id}:#{@projmux_window_uid}' >"$delete_root/windows.before-offline-dry-run"
delete_pmx_delete window "uid:$delete_offline_window_uid" --project "uid:$delete_alpha_project_uid" --dry-run >"$delete_root/offline-dry-run.out"
cmp "$delete_root/registry.before-offline-dry-run" "$delete_registry"
delete_tmux list-windows -a -F '#{session_name}:#{window_id}:#{@projmux_window_uid}' >"$delete_root/windows.after-offline-dry-run"
cmp "$delete_root/windows.before-offline-dry-run" "$delete_root/windows.after-offline-dry-run"
for expected in \
  "cascade agent/" \
  "uid=$delete_offline_agent_uid" \
  "uid=$delete_offline_shell_uid" \
  "registry-only would delete this Window; no tmux Window would be killed"; do
  smoke_assert_file_contains "$delete_root/offline-dry-run.out" "$expected"
done
if grep -Fq "live would kill tmux window" "$delete_root/offline-dry-run.out"; then
  echo "offline Window dry-run planned a tmux kill" >&2
  exit 1
fi

delete_pmx_delete window "uid:$delete_offline_window_uid" --project "uid:$delete_alpha_project_uid" --yes >"$delete_root/offline.out"
smoke_assert_file_contains "$delete_root/offline.out" "registry-only deleted this Window; no tmux Window was killed"
delete_pmx get windows --all-projects -o uid >"$delete_root/windows.after-offline"
delete_pmx get panes --all-projects -o uid >"$delete_root/panes.after-offline"
delete_pmx get agents --all-projects -o uid >"$delete_root/agents.after-offline"
if grep -Fqx "$delete_offline_window_uid" "$delete_root/windows.after-offline"; then
  echo "offline Window cascade left Window uid $delete_offline_window_uid" >&2
  exit 1
fi
if grep -Fqx "$delete_offline_shell_uid" "$delete_root/panes.after-offline"; then
  echo "offline Window cascade left Pane uid $delete_offline_shell_uid" >&2
  exit 1
fi
if grep -Fqx "$delete_offline_agent_uid" "$delete_root/agents.after-offline"; then
  echo "offline Window cascade left Agent uid $delete_offline_agent_uid" >&2
  exit 1
fi
if ! grep -Fqx "$delete_sibling_uid" "$delete_root/windows.after-offline" || \
  [[ "$(delete_tmux display-message -p -t "$delete_sibling" '#{window_id}')" != "$delete_sibling" ]]; then
  echo "offline Window delete changed its live sibling" >&2
  exit 1
fi
for delete_forbidden_route in "-L $delete_socket" "-L $delete_product_socket" "-S $delete_socket_path"; do
  if grep -Fq -- "$delete_forbidden_route kill-window -t $delete_offline_window" "$delete_shim_log"; then
    echo "offline Window canonical delete issued a tmux kill: $delete_forbidden_route target=$delete_offline_window" >&2
    exit 1
  fi
done

cp "$delete_registry" "$delete_root/registry.before-offline-repeat"
if delete_pmx_delete window "uid:$delete_offline_window_uid" --project "uid:$delete_alpha_project_uid" --yes \
  >"$delete_root/offline-repeat.out" 2>"$delete_root/offline-repeat.err"; then
  echo "repeat offline Window delete unexpectedly succeeded" >&2
  exit 1
fi
cmp "$delete_root/registry.before-offline-repeat" "$delete_registry"
smoke_assert_file_contains "$delete_root/offline-repeat.err" "matched no windows"

# Deleting the sole Pane predicts and creates a replacement shell before the
# prior Pane is killed. The same Window identity remains live.
delete_last_window_uid="$(
  delete_pmx create window --project "uid:$delete_alpha_project_uid" --name pane-last -o uid -- sleep 600
)"
delete_last_window="$(
  delete_tmux list-windows -a -F '#{window_id}|#{@projmux_window_uid}' |
    awk -F '|' -v uid="$delete_last_window_uid" '$2 == uid { print $1 }'
)"
delete_last_pane="$(delete_tmux display-message -p -t "$delete_last_window" '#{pane_id}')"
delete_last_pane_uid="$(delete_tmux show-options -pqv -t "$delete_last_pane" @projmux_pane_uid)"
echo ">> delete last Pane Window target pane=$delete_last_pane uid=$delete_last_pane_uid window=$delete_last_window"
delete_pmx_delete pane "uid:$delete_last_pane_uid" --dry-run >"$delete_root/pane-last-dry-run.out"
smoke_assert_file_contains "$delete_root/pane-last-dry-run.out" "live lifecycle would create a replacement shell in Window $delete_last_window"
delete_pmx_delete pane "uid:$delete_last_pane_uid" --yes >"$delete_root/pane-last.out"
if [[ "$(delete_tmux display-message -p -t "$delete_last_window" '#{window_id}' 2>/dev/null || true)" != "$delete_last_window" ]]; then
  echo "last-Pane delete did not preserve Window $delete_last_window" >&2
  exit 1
fi
delete_last_pane_replacement_uid="$(delete_tmux list-panes -t "$delete_last_window" -F '#{@projmux_pane_uid}')"
if [[ -z "$delete_last_pane_replacement_uid" || "$delete_last_pane_replacement_uid" == "$delete_last_pane_uid" ]]; then
  echo "last-Pane delete did not install a distinct replacement shell" >&2
  exit 1
fi

# A freshly and explicitly registered one-Window Project makes the same
# last-Pane replacement preserve the complete session. Its raw canonical session remains D2
# until registration; the bounded repair then establishes managed identity.
mkdir -p "$delete_root/work/gamma"
delete_tmux new-session -d -s work-gamma -n only -c "$delete_root/work/gamma" sleep 600
delete_tmux set-option -t work-gamma -q @projmux_project_path "$delete_root/work/gamma"
delete_gamma_project_uid="$(
  delete_pmx create project --root "$delete_root/work/gamma" --name gamma -o uid
)"
printf '%s\n' "$delete_gamma_project_uid" >"$delete_root/register-gamma.out"
if [[ -z "$delete_gamma_project_uid" ]]; then
  echo "gamma registration returned no exact Registry Project UID" >&2
  exit 1
fi
# The preceding last-Pane deletion emits an asynchronous pane-exit controller
# pass. It can legitimately claim the freshly registered gamma Project UID
# after apply's pre-observation but before its first write. That exact guard
# refusal is expected safety, so rebuild the apply plan within a strict bound;
# another target, field, or failure remains fatal. Each attempt is retained as
# evidence, and no blind delay substitutes for a fresh observation.
mapfile -t delete_gamma_session_matches < <(
  delete_tmux list-sessions -F '#{session_id}|#{session_name}' |
    awk -F '|' '$2 == "work-gamma" { print $1 }'
)
if [[ "${#delete_gamma_session_matches[@]}" != "1" ]] || \
  [[ ! "${delete_gamma_session_matches[0]}" =~ ^\$[0-9]+$ ]]; then
  echo "gamma apply requires exactly one work-gamma session receipt" >&2
  printf '%s\n' "${delete_gamma_session_matches[@]}" >&2
  exit 1
fi
delete_gamma_session_id="${delete_gamma_session_matches[0]}"
mapfile -t delete_gamma_pane_matches < <(
  delete_tmux list-panes -s -t "$delete_gamma_session_id" -F '#{pane_id}'
)
if [[ "${#delete_gamma_pane_matches[@]}" != "1" ]] || \
  [[ ! "${delete_gamma_pane_matches[0]}" =~ ^%[0-9]+$ ]]; then
  echo "gamma apply requires exactly one Pane in $delete_gamma_session_id" >&2
  printf '%s\n' "${delete_gamma_pane_matches[@]}" >&2
  exit 1
fi
delete_gamma_receipt_pane="${delete_gamma_pane_matches[0]}"
delete_gamma_receipt="$(
  delete_tmux display-message -p -t "$delete_gamma_receipt_pane" \
    '#{socket_path}|#{pid}|#{session_id}|#{session_name}|#{@projmux_project_path}|#{@projmux_project_uid}|receipt-end'
)"
IFS='|' read -r delete_gamma_socket_path delete_gamma_server_pid delete_gamma_observed_session_id \
  delete_gamma_session_name delete_gamma_project_path delete_gamma_observed_uid delete_gamma_receipt_end \
  <<<"$delete_gamma_receipt"
if [[ "$delete_gamma_socket_path" != "$delete_socket_path" ]] || \
  [[ "$delete_gamma_server_pid" != "$delete_server_pid" ]] || \
  [[ "$delete_gamma_observed_session_id" != "$delete_gamma_session_id" ]] || \
  [[ "$delete_gamma_session_name" != "work-gamma" ]] || \
  [[ "$delete_gamma_project_path" != "$delete_root/work/gamma" ]] || \
  [[ -n "$delete_gamma_observed_uid" && "$delete_gamma_observed_uid" != "$delete_gamma_project_uid" ]] || \
  [[ "$delete_gamma_receipt_end" != "receipt-end" ]]; then
  echo "gamma apply lacks an exact session receipt: $delete_gamma_receipt" >&2
  exit 1
fi
delete_gamma_apply_succeeded=0
delete_gamma_expected_drift="runtime mutation plan: guard refused action \"write-identity\" before first write: controller target $delete_gamma_session_id guard @projmux_project_uid drifted"
for delete_gamma_apply_attempt in 1 2 3 4; do
  delete_gamma_apply_out="$delete_root/apply-gamma-$delete_gamma_apply_attempt.out"
  delete_gamma_apply_err="$delete_root/apply-gamma-$delete_gamma_apply_attempt.err"
  if delete_pmx internal tmux apply --bin "$bin" --config "$delete_config" --socket "$delete_socket" \
    >"$delete_gamma_apply_out" 2>"$delete_gamma_apply_err"; then
    cp "$delete_gamma_apply_out" "$delete_root/apply-gamma.out"
    delete_gamma_apply_succeeded=1
    break
  fi
  if [[ "$(grep -Fc "$delete_gamma_expected_drift" "$delete_gamma_apply_err")" != "1" ]] || \
    [[ "$(tail -n 1 "$delete_gamma_apply_err")" != *"$delete_gamma_expected_drift"* ]] || \
    grep -F 'runtime mutation plan:' "$delete_gamma_apply_err" |
      grep -Fv "$delete_gamma_expected_drift" >/dev/null; then
    echo "gamma apply failed for a reason other than its exact asynchronous Project UID claim" >&2
    cat "$delete_gamma_apply_err" >&2
    exit 1
  fi
  delete_gamma_retry_receipt="$(
    delete_tmux display-message -p -t "$delete_gamma_receipt_pane" \
      '#{socket_path}|#{pid}|#{session_id}|#{session_name}|#{@projmux_project_path}|#{@projmux_project_uid}|receipt-end'
  )"
  IFS='|' read -r delete_gamma_retry_socket_path delete_gamma_retry_server_pid delete_gamma_retry_session_id \
    delete_gamma_retry_session_name delete_gamma_retry_project_path delete_gamma_retry_project_uid \
    delete_gamma_retry_receipt_end <<<"$delete_gamma_retry_receipt"
  if [[ "$delete_gamma_retry_socket_path" != "$delete_gamma_socket_path" ]] || \
    [[ "$delete_gamma_retry_server_pid" != "$delete_gamma_server_pid" ]] || \
    [[ "$delete_gamma_retry_session_id" != "$delete_gamma_session_id" ]] || \
    [[ "$delete_gamma_retry_session_name" != "$delete_gamma_session_name" ]] || \
    [[ "$delete_gamma_retry_project_path" != "$delete_gamma_project_path" ]] || \
    [[ "$delete_gamma_retry_project_uid" != "$delete_gamma_project_uid" ]] || \
    [[ "$delete_gamma_retry_receipt_end" != "receipt-end" ]]; then
    echo "gamma apply drift was not followed by its exact Registry UID claim on the original route: $delete_gamma_retry_receipt" >&2
    cat "$delete_gamma_apply_err" >&2
    exit 1
  fi
done
if [[ "$delete_gamma_apply_succeeded" != "1" ]]; then
  echo "gamma apply did not converge after four exact Project UID drift replans" >&2
  for delete_gamma_apply_attempt in 1 2 3 4; do
    cat "$delete_root/apply-gamma-$delete_gamma_apply_attempt.err" >&2
  done
  exit 1
fi
smoke_assert_file_contains "$delete_root/apply-gamma.out" "reloaded tmux server -L $delete_socket"
# The same asynchronous pass can also complete before the first explicit
# reconcile, so that call is not itself a stable repeat boundary. Preserve
# every report and require the bounded sequence to finish with an empty no-op;
# the earlier initial apply/reconcile assertions remain strict where no
# lifecycle event precedes them.
delete_gamma_converged=0
for delete_gamma_pass in 1 2 3 4; do
  delete_gamma_report="$delete_root/reconcile-gamma-$delete_gamma_pass.json"
  delete_pmx reconcile resources --socket "$delete_socket" -o json >"$delete_gamma_report"
  if grep -Fq '"outcome": "changed"' "$delete_gamma_report"; then
    continue
  fi
  if grep -Fq '"outcome": "no-op"' "$delete_gamma_report"; then
    smoke_assert_file_contains "$delete_gamma_report" '"items": []'
    delete_gamma_converged=1
    break
  fi
  echo "gamma reconcile reported neither changed nor no-op: $delete_gamma_report" >&2
  cat "$delete_gamma_report" >&2
  exit 1
done
if [[ "$delete_gamma_converged" != "1" ]]; then
  echo "gamma reconcile did not converge to an empty no-op within four passes" >&2
  exit 1
fi
delete_gamma_pane="$(delete_tmux display-message -p -t work-gamma:only '#{pane_id}')"
delete_gamma_pane_uid="$(delete_tmux show-options -pqv -t "$delete_gamma_pane" @projmux_pane_uid)"
delete_gamma_window="$(delete_tmux display-message -p -t "$delete_gamma_pane" '#{window_id}')"
echo ">> delete last Pane session target pane=$delete_gamma_pane uid=$delete_gamma_pane_uid window=$delete_gamma_window session=work-gamma"
delete_pmx_delete pane "uid:$delete_gamma_pane_uid" --dry-run >"$delete_root/pane-session-last-dry-run.out"
smoke_assert_file_contains "$delete_root/pane-session-last-dry-run.out" "live lifecycle would create a replacement shell in Window $delete_gamma_window"
delete_pmx_delete pane "uid:$delete_gamma_pane_uid" --yes >"$delete_root/pane-session-last.out"
if ! delete_tmux has-session -t work-gamma 2>/dev/null; then
  echo "last-Pane delete did not preserve work-gamma session" >&2
  exit 1
fi
smoke_assert_file_contains "$delete_root/pane-session-last.out" "Window uid="

delete_pmx internal tmux apply --bin "$bin" --config "$delete_config" --socket "$delete_socket" >"$delete_root/apply-after-pane-delete.out"
delete_pmx get panes --all-projects -o uid >"$delete_root/panes.after-reconcile"
for deleted_uid in "$delete_split_uid" "$delete_agent_pane_uid" "$delete_agent_two_pane_uid" \
  "$delete_offline_pane_uid" "$delete_offline_agent_pane_uid" "$delete_last_pane_uid" "$delete_gamma_pane_uid"; do
  if grep -Fqx "$deleted_uid" "$delete_root/panes.after-reconcile"; then
    echo "deleted Pane $deleted_uid was re-imported after reconciliation" >&2
    exit 1
  fi
done
if ! grep -Fqx "$delete_sibling_shell_uid" "$delete_root/panes.after-reconcile"; then
  echo "Pane/Agent delete lost shell sibling Registry uid $delete_sibling_shell_uid" >&2
  exit 1
fi

# The entire selected tmux server can be absent, not merely one Window. The
# typed no-server inventory is authoritative empty runtime state and still
# permits the exact Registry-only Window cascade. A byte-identical dry-run and
# the live foreign socket prove the destructive exception stays narrow.
delete_tmux kill-server
# Killing the server fires both pane-exit hooks for every pane it held, and the
# generated hooks background their convergence. Those convergences are the last
# legitimate writers of this registry, so the byte-identity check below has to
# start from a settled file rather than from whatever the file held mid-flight.
# Stability is the honest signal: ten identical 50ms observations establish a
# 500ms quiet window in which no worker is still landing a pass.
delete_settled=0
delete_stable_samples=0
cp "$delete_registry" "$delete_root/registry.settle-probe"
for _ in {1..200}; do
  sleep 0.05
  if cmp -s "$delete_root/registry.settle-probe" "$delete_registry"; then
    delete_stable_samples=$((delete_stable_samples + 1))
    if [[ "$delete_stable_samples" == "10" ]]; then
      delete_settled=1
      break
    fi
    continue
  fi
  delete_stable_samples=0
  cp "$delete_registry" "$delete_root/registry.settle-probe"
done
if [[ "$delete_settled" != "1" ]]; then
  echo "hook-driven convergence never settled after kill-server" >&2
  exit 1
fi
cp "$delete_registry" "$delete_root/registry.before-no-server-dry-run"
if delete_pmx_delete pane "uid:$delete_sibling_shell_uid" --dry-run \
  >"$delete_root/no-server-pane.out" 2>"$delete_root/no-server-pane.err"; then
  echo "absent-server Pane delete incorrectly treated absence as authority" >&2
  exit 1
fi
cmp "$delete_root/registry.before-no-server-dry-run" "$delete_registry"
if ! grep -Fq "unavailable (no-server)" "$delete_root/no-server-pane.err" ||
  ! grep -Fq "absence is not Registry deletion authority" "$delete_root/no-server-pane.err"; then
  echo "absent-server Pane refusal lacked the required authority diagnostics:" >&2
  echo "--- no-server-pane.err ---" >&2
  cat "$delete_root/no-server-pane.err" >&2
  echo "--- no-server-pane.out ---" >&2
  cat "$delete_root/no-server-pane.out" >&2
  exit 1
fi
delete_pmx_delete window "uid:$delete_sibling_uid" --project "uid:$delete_alpha_project_uid" --dry-run >"$delete_root/no-server-dry-run.out"
cmp "$delete_root/registry.before-no-server-dry-run" "$delete_registry"
smoke_assert_file_contains "$delete_root/no-server-dry-run.out" "registry-only would delete this Window; no tmux Window would be killed"
if grep -Fq "live would kill tmux window" "$delete_root/no-server-dry-run.out"; then
  echo "absent-server Window dry-run planned a tmux kill" >&2
  exit 1
fi
delete_pmx_delete window "uid:$delete_sibling_uid" --project "uid:$delete_alpha_project_uid" --yes >"$delete_root/no-server.out"
smoke_assert_file_contains "$delete_root/no-server.out" "registry-only deleted this Window; no tmux Window was killed"
delete_pmx get windows --all-projects -o uid >"$delete_root/windows.after-no-server"
if grep -Fqx "$delete_sibling_uid" "$delete_root/windows.after-no-server"; then
  echo "absent-server canonical delete left Registry Window $delete_sibling_uid" >&2
  exit 1
fi
delete_other_after="$(delete_other_tmux show-options -gqv @projmux_delete_sentinel):$(delete_other_tmux list-windows -a -F '#{session_name}:#{window_id}')"
if [[ "$delete_other_after" != "$delete_other_before" ]]; then
  echo "Pane/Agent delete touched the foreign socket" >&2
  exit 1
fi

# Each invocation may use its explicit logical name only to discover the
# physical socket. Inventory and every write are then pinned to that printable
# exact `-S` authority; the queued self-delete embeds run-shell later on the
# same routed tmux argv after its durable lease write.
if ! grep -Fq -- "-L $delete_socket display-message -p -F #{socket_path}" "$delete_shim_log"; then
  echo "delete Window e2e did not observe logical route discovery for -L $delete_socket" >&2
  cat "$delete_shim_log" >&2
  exit 1
fi
for canonical_read in \
  "-S $delete_socket_path list-windows -a" \
  "-S $delete_socket_path list-panes -a"; do
  if ! grep -Fq -- "$canonical_read" "$delete_shim_log"; then
    echo "delete Window e2e did not observe physically pinned inventory: $canonical_read" >&2
    cat "$delete_shim_log" >&2
    exit 1
  fi
done
for canonical_mutation in \
  "kill-window -t $delete_primary" \
  "run-shell -b" \
  "kill-pane -t $delete_split" \
  "kill-pane -t $delete_agent_two_pane"; do
  if ! awk -v route="-S $delete_socket_path " -v mutation="$canonical_mutation" \
    'index($0, route) == 1 && index($0, mutation) > 0 { found = 1 } END { exit !found }' "$delete_shim_log"; then
    echo "delete Window e2e did not observe exact physical mutation routing: -S $delete_socket_path ... $canonical_mutation" >&2
    cat "$delete_shim_log" >&2
    exit 1
  fi
done
# The product socket may be queried only for closed read-only route discovery
# and ownership-marker observations. Any mutation or new/unknown command on
# that default logical route is still a forbidden fallback; all writes above
# must remain pinned to the exact physical socket.
if ! awk -v route="-L $delete_product_socket " '
  index($0, route) == 1 {
    command = substr($0, length(route) + 1)
    if (command != "display-message -p -F #{socket_path}" &&
        command != "show-options -gqv @projmux_app" &&
        command != "show-options -gqv @projmux_socket_name") {
      print "forbidden default-route delete call: " $0 > "/dev/stderr"
      rejected = 1
    }
  }
  END { exit rejected }
' "$delete_shim_log"; then
  echo "delete used an unclassified command on default app socket -L $delete_product_socket" >&2
  exit 1
fi

delete_cleanup
trap smoke_cleanup_env EXIT
echo ">> delete Window/Pane/Agent e2e passed: socket=$delete_socket path=$delete_socket_path other-socket=$delete_other_socket other-path=$delete_other_socket_path cleanup=validated-exact-sockets"

# Rename/rebind convergence uses its own exact two-socket environment. The app
# receives an explicit synthetic client socket path for immediate mirror lookup;
# every setup/read/repair command strips inherited client state and names -L.
rename_root="$PROJMUX_SMOKE_WORKDIR/rename-rebind-phase5"
rename_socket="projmux-rename-$RANDOM-$$"
rename_other_socket="projmux-rename-other-$RANDOM-$$"
rename_session="work-alpha"
mkdir -p \
  "$rename_root/home" \
  "$rename_root/config" \
  "$rename_root/state" \
  "$rename_root/runtime" \
  "$rename_root/tmux" \
  "$rename_root/work/alpha" \
  "$rename_root/shim"
chmod 0700 "$rename_root/runtime" "$rename_root/tmux"

rename_tmux() {
  env -u TMUX -u TMUX_PANE \
    HOME="$rename_root/home" \
    XDG_CONFIG_HOME="$rename_root/config" \
    XDG_STATE_HOME="$rename_root/state" \
    XDG_RUNTIME_DIR="$rename_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$rename_root/work" \
    TMUX_TMPDIR="$rename_root/tmux" \
    SHELL=/bin/sh \
    tmux -L "$rename_socket" "$@"
}

rename_other_tmux() {
  env -u TMUX -u TMUX_PANE \
    HOME="$rename_root/home" \
    XDG_CONFIG_HOME="$rename_root/config" \
    XDG_STATE_HOME="$rename_root/state" \
    XDG_RUNTIME_DIR="$rename_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$rename_root/work" \
    TMUX_TMPDIR="$rename_root/tmux" \
    SHELL=/bin/sh \
    tmux -L "$rename_other_socket" "$@"
}

rename_base_pmx() {
  env -u TMUX -u TMUX_PANE \
    HOME="$rename_root/home" \
    XDG_CONFIG_HOME="$rename_root/config" \
    XDG_STATE_HOME="$rename_root/state" \
    XDG_RUNTIME_DIR="$rename_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$rename_root/work" \
    TMUX_TMPDIR="$rename_root/tmux" \
    SHELL=/bin/sh \
    "$bin" "$@"
}

rename_tmux new-session -d -s "$rename_session" -n runtime-window -c "$rename_root/work/alpha" sleep 600
rename_tmux set-option -t "$rename_session" -q @projmux_project_path "$rename_root/work/alpha"
rename_pane="$(rename_tmux display-message -p -t "$rename_session:0.0" '#{pane_id}')"
rename_tmux select-pane -T runtime-pane-title -t "$rename_pane"
rename_other_tmux new-session -d -s untouched -c "$rename_root/work/alpha" sleep 600
rename_other_tmux set-option -gq @projmux_rename_sentinel unchanged

rename_socket_path="$(rename_tmux display-message -p -t "$rename_session" '#{socket_path}')"
rename_socket_pid="$(rename_tmux display-message -p -t "$rename_session" '#{pid}')"
rename_other_socket_path="$(rename_other_tmux display-message -p -t untouched '#{socket_path}')"
for actual in "$rename_socket_path" "$rename_other_socket_path"; do
  case "$actual" in
    "$rename_root"/*) ;;
    *)
      echo "rename/rebind e2e socket escaped smoke root: $actual" >&2
      exit 1
      ;;
  esac
done

rename_cleanup() {
  local socket actual hook
  for socket in "$rename_socket" "$rename_other_socket"; do
    actual="$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$rename_root/tmux" tmux -L "$socket" display-message -p '#{socket_path}' 2>/dev/null || true)"
    if [[ -z "$actual" ]]; then
      continue
    fi
    case "$actual" in
      "$rename_root"/*)
        for hook in pane-exited after-kill-pane pane-focus-out pane-focus-in after-select-pane after-select-window client-session-changed; do
          env -u TMUX -u TMUX_PANE tmux -S "$actual" set-hook -gu "$hook" >/dev/null 2>&1 || true
        done
        env -u TMUX -u TMUX_PANE tmux -S "$actual" kill-server >/dev/null 2>&1 || true
        ;;
      *) echo "refusing rename/rebind cleanup outside smoke root: $actual" >&2 ;;
    esac
  done
}
trap 'rename_cleanup; smoke_cleanup_env' EXIT

rename_config="$rename_root/config/projmux/tmux.conf"
rename_base_pmx reconcile resources --socket "$rename_socket" --dry-run -o json >"$rename_root/d2.json"
smoke_assert_file_contains "$rename_root/d2.json" '"outcome": "no-op"'
if [[ -e "$rename_root/state/projmux/metadata/registry.json" ]]; then
  echo "rename/rebind D2 dry-run created a Registry" >&2
  exit 1
fi
rename_base_pmx create project --root "$rename_root/work/alpha" --name alpha >"$rename_root/register-alpha.out"
rename_base_pmx internal tmux apply --bin "$bin" --config "$rename_config" --socket "$rename_socket" >"$rename_root/apply.out"
e2e_bounded_reconcile_to_noop --allow-initial-noop "$rename_root/initial-reconcile" \
  rename_base_pmx reconcile resources --socket "$rename_socket" -o json
rename_project_uid="$(rename_tmux show-options -qv -t "$rename_session" @projmux_project_uid)"
rename_window="$(rename_tmux display-message -p -t "$rename_session:0" '#{window_id}')"
rename_window_uid="$(rename_tmux show-options -wqv -t "$rename_window" @projmux_window_uid)"
rename_pane_uid="$(rename_tmux show-options -pqv -t "$rename_pane" @projmux_pane_uid)"
if [[ -z "$rename_project_uid" || -z "$rename_window_uid" || -z "$rename_pane_uid" ]]; then
  echo "rename/rebind e2e apply left an identity mirror empty" >&2
  exit 1
fi
rename_session_name_before="$(rename_tmux display-message -p -t "$rename_session" '#{session_name}')"
rename_window_name_before="$(rename_tmux display-message -p -t "$rename_window" '#{window_name}')"
rename_pane_title_before="$(rename_tmux display-message -p -t "$rename_pane" '#{pane_title}')"
rename_other_before="$(rename_other_tmux show-options -gqv @projmux_rename_sentinel):$(rename_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"

rename_pmx() {
  env -u TMUX_PANE \
    HOME="$rename_root/home" \
    XDG_CONFIG_HOME="$rename_root/config" \
    XDG_STATE_HOME="$rename_root/state" \
    XDG_RUNTIME_DIR="$rename_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$rename_root/work" \
    TMUX_TMPDIR="$rename_root/tmux" \
    TMUX="$rename_socket_path,$rename_socket_pid,0" \
    SHELL=/bin/sh \
    "$bin" "$@"
}

rename_pmx_at_pane() {
  local anchor_pane="$1"
  shift
  env \
    HOME="$rename_root/home" \
    XDG_CONFIG_HOME="$rename_root/config" \
    XDG_STATE_HOME="$rename_root/state" \
    XDG_RUNTIME_DIR="$rename_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$rename_root/work" \
    TMUX_TMPDIR="$rename_root/tmux" \
    TMUX="$rename_socket_path,$rename_socket_pid,0" \
    TMUX_PANE="$anchor_pane" \
    SHELL=/bin/sh \
    "$bin" "$@"
}

rename_pmx rename project "uid:$rename_project_uid" --name stable-project >"$rename_root/rename-project.out"
rename_pmx rename window "uid:$rename_window_uid" --name stable-window >"$rename_root/rename-window.out"
rename_pmx rename pane "uid:$rename_pane_uid" --name stable-pane >"$rename_root/rename-pane.out"
mkdir -p "$rename_root/work/moved"
rename_pmx rebind project "uid:$rename_project_uid" --root "$rename_root/work/moved" >"$rename_root/rebind-project.out"

# Agent stable-name parity is Registry-only. A provider-shaped managed Pane
# proves the rename does not leak into provider or Pane metadata/title.
cat >"$rename_root/shim/codex" <<'RENAME_CODEX_STUB'
#!/usr/bin/env bash
exec sleep 600
RENAME_CODEX_STUB
chmod 0755 "$rename_root/shim/codex"
rename_agent_anchor_receipt="$(
  rename_tmux display-message -p -t "$rename_pane" \
    '#{socket_path}|#{pid}|#{session_id}|#{session_name}|#{window_id}|#{pane_id}|#{@projmux_project_uid}|#{@projmux_window_uid}|#{@projmux_pane_uid}|receipt-end'
)"
IFS='|' read -r rename_agent_anchor_socket rename_agent_anchor_pid rename_agent_anchor_session_id \
  rename_agent_anchor_session_name rename_agent_anchor_window rename_agent_anchor_pane \
  rename_agent_anchor_project_uid rename_agent_anchor_window_uid rename_agent_anchor_pane_uid \
  rename_agent_anchor_end <<<"$rename_agent_anchor_receipt"
if [[ "$rename_agent_anchor_socket" != "$rename_socket_path" ]] || \
  [[ "$rename_agent_anchor_pid" != "$rename_socket_pid" ]] || \
  [[ ! "$rename_agent_anchor_session_id" =~ ^\$[0-9]+$ ]] || \
  [[ "$rename_agent_anchor_session_name" != "$rename_session" ]] || \
  [[ "$rename_agent_anchor_window" != "$rename_window" ]] || \
  [[ "$rename_agent_anchor_pane" != "$rename_pane" ]] || \
  [[ "$rename_agent_anchor_project_uid" != "$rename_project_uid" ]] || \
  [[ "$rename_agent_anchor_window_uid" != "$rename_window_uid" ]] || \
  [[ "$rename_agent_anchor_pane_uid" != "$rename_pane_uid" ]] || \
  [[ "$rename_agent_anchor_end" != "receipt-end" ]]; then
  echo "rename Agent create lacks an exact managed placement receipt: $rename_agent_anchor_receipt" >&2
  exit 1
fi
rename_agent_pane="$(
  PATH="$rename_root/shim:$PATH" rename_pmx_at_pane "$rename_pane" create agent \
    --provider codex --project "uid:$rename_project_uid" --window "uid:$rename_window_uid" -o pane-id
)"
rename_agent_uid="$(rename_base_pmx get agents --project "uid:$rename_project_uid" --window "uid:$rename_window_uid" -o uid | tail -n 1)"
rename_agent_pane_label_before="$(rename_tmux show-options -pqv -t "$rename_agent_pane" @projmux_pane_label)"
rename_agent_pane_title_before="$(rename_tmux display-message -p -t "$rename_agent_pane" '#{pane_title}')"
rename_pmx rename agent "uid:$rename_agent_uid" --name reviewer >"$rename_root/rename-agent.out"
rename_base_pmx describe agent "uid:$rename_agent_uid" -o json >"$rename_root/agent-after.json"
smoke_assert_file_contains "$rename_root/agent-after.json" '"name": "reviewer"'
smoke_assert_file_contains "$rename_root/agent-after.json" '"provider": "codex"'
if [[ "$(rename_tmux show-options -pqv -t "$rename_agent_pane" @projmux_pane_label)" != "$rename_agent_pane_label_before" ]] || \
  [[ "$(rename_tmux display-message -p -t "$rename_agent_pane" '#{pane_title}')" != "$rename_agent_pane_title_before" ]]; then
  echo "rename agent changed its managed Pane name/title" >&2
  exit 1
fi
if [[ "$(rename_tmux show-options -qv -t "$rename_session" @projmux_project_name)" != stable-project ]] || \
  [[ "$(rename_tmux show-options -wqv -t "$rename_window" @projmux_window_name)" != stable-window ]] || \
  [[ "$(rename_tmux show-options -pqv -t "$rename_pane" @projmux_pane_label)" != stable-pane ]] || \
  [[ "$(rename_tmux show-options -qv -t "$rename_session" @projmux_project_path)" != "$rename_root/work/moved" ]]; then
  echo "rename/rebind e2e immediate mirrors did not converge" >&2
  exit 1
fi
if [[ "$(rename_tmux display-message -p -t "$rename_session" '#{session_name}')" != "$rename_session_name_before" ]] || \
  [[ "$(rename_tmux display-message -p -t "$rename_window" '#{window_name}')" != "$rename_window_name_before" ]] || \
  [[ "$(rename_tmux display-message -p -t "$rename_pane" '#{pane_title}')" != "$rename_pane_title_before" ]]; then
  echo "rename/rebind changed a raw runtime session/window/pane name" >&2
  exit 1
fi

# With no live target, rename persists stable Registry state. Recreating the
# same exact UID-bound session with stale mirrors lets the public exact-socket
# route repair both name and path later.
rename_tmux kill-session -t "$rename_session"
rename_pmx rename project "uid:$rename_project_uid" --name offline-project >"$rename_root/offline-rename.out"
rename_tmux new-session -d -s "$rename_session" -n replacement -c "$rename_root/work/alpha" sleep 600
rename_tmux set-option -t "$rename_session" -q @projmux_project_uid "$rename_project_uid"
rename_tmux set-option -t "$rename_session" -q @projmux_project_name stable-project
rename_tmux set-option -t "$rename_session" -q @projmux_project_path "$rename_root/work/alpha"
rename_socket_path="$(rename_tmux display-message -p -t "$rename_session" '#{socket_path}')"
rename_socket_pid="$(rename_tmux display-message -p -t "$rename_session" '#{pid}')"
# Killing the sole session ended the app-configured server. This replacement
# is intentionally a standalone exact-socket runtime: controller writes may
# use only its operator-selected physical path, never infer authority from the
# old logical name. The Project name/path assertions below and final foreign
# snapshot retain the desired-state and sibling-containment evidence.
rename_replacement_pane="$(rename_tmux list-panes -s -t "$rename_session" -F '#{pane_id}')"
if [[ ! "$rename_replacement_pane" =~ ^%[0-9]+$ ]]; then
  echo "offline rename replacement lacks exactly one Pane: $rename_replacement_pane" >&2
  exit 1
fi
rename_replacement_receipt="$(
  rename_tmux display-message -p -t "$rename_replacement_pane" \
    '#{socket_path}|#{pid}|#{session_id}|#{session_name}|#{@projmux_project_uid}|#{@projmux_project_path}|#{@projmux_app}|#{@projmux_socket_name}|receipt-end'
)"
IFS='|' read -r rename_replacement_socket rename_replacement_pid rename_replacement_session_id \
  rename_replacement_session_name rename_replacement_project_uid rename_replacement_project_path \
  rename_replacement_app_marker rename_replacement_logical_marker rename_replacement_end \
  <<<"$rename_replacement_receipt"
if [[ "$rename_replacement_socket" != "$rename_socket_path" ]] || \
  [[ "$rename_replacement_pid" != "$rename_socket_pid" ]] || \
  [[ ! "$rename_replacement_session_id" =~ ^\$[0-9]+$ ]] || \
  [[ "$rename_replacement_session_name" != "$rename_session" ]] || \
  [[ "$rename_replacement_project_uid" != "$rename_project_uid" ]] || \
  [[ "$rename_replacement_project_path" != "$rename_root/work/alpha" ]] || \
  [[ -n "$rename_replacement_app_marker" ]] || \
  [[ -n "$rename_replacement_logical_marker" ]] || \
  [[ "$rename_replacement_end" != "receipt-end" ]]; then
  echo "offline rename replacement lacks exact standalone authority: $rename_replacement_receipt" >&2
  exit 1
fi
rename_base_pmx reconcile resources --socket-path "$rename_socket_path" >"$rename_root/offline-reconcile.out"
if [[ "$(rename_tmux show-options -qv -t "$rename_session" @projmux_project_name)" != offline-project ]] || \
  [[ "$(rename_tmux show-options -qv -t "$rename_session" @projmux_project_path)" != "$rename_root/work/moved" ]]; then
  echo "offline rename/rebind drift did not converge through public reconcile" >&2
  exit 1
fi

# A narrow tmux shim forces only the live Project name option write to fail.
# The Registry result remains durable and the unshimmed public retry repairs it.
rename_real_tmux="$(command -v tmux)"
cat >"$rename_root/shim/tmux" <<RENAME_TMUX_SHIM
#!/usr/bin/env bash
rename_tmux_args=("\$@")
if [[ "\${rename_tmux_args[0]:-}" == "-S" && \${#rename_tmux_args[@]} -ge 2 ]]; then
  rename_tmux_args=("\${rename_tmux_args[@]:2}")
fi
if [[ "\${rename_tmux_args[0]:-}" == "set-option" ]]; then
  for rename_tmux_arg in "\${rename_tmux_args[@]:1}"; do
    if [[ "\$rename_tmux_arg" == "@projmux_project_name" ]]; then
      exit 77
    fi
  done
fi
exec $(printf %q "$rename_real_tmux") "\$@"
RENAME_TMUX_SHIM
chmod 0755 "$rename_root/shim/tmux"
set +e
PATH="$rename_root/shim:$PATH" rename_pmx rename project "uid:$rename_project_uid" --name retry-project \
  >"$rename_root/failure.out" 2>"$rename_root/failure.err"
rename_failure_status=$?
set -e
if [[ "$rename_failure_status" == 0 ]]; then
  echo "injected live mirror failure unexpectedly succeeded" >&2
  exit 1
fi
if ! grep -Fq "rename project \"$rename_project_uid\" committed Registry state" "$rename_root/failure.err" || \
  ! grep -Fq "projmux reconcile resources" "$rename_root/failure.err"; then
  echo "injected live mirror failure did not expose the committed/retry contract" >&2
  cat "$rename_root/failure.err" >&2
  exit 1
fi
rename_base_pmx describe project "uid:$rename_project_uid" -o name >"$rename_root/name-after-failure"
smoke_assert_file_contains "$rename_root/name-after-failure" "retry-project"
if [[ "$(rename_tmux show-options -qv -t "$rename_session" @projmux_project_name)" != offline-project ]]; then
  echo "injected live mirror failure unexpectedly changed the option" >&2
  exit 1
fi
rename_base_pmx reconcile resources --socket-path "$rename_socket_path" >"$rename_root/failure-retry.out"
if [[ "$(rename_tmux show-options -qv -t "$rename_session" @projmux_project_name)" != retry-project ]]; then
  echo "public retry did not repair failed live Project name mirror" >&2
  exit 1
fi
rename_base_pmx reconcile resources --socket-path "$rename_socket_path" -o json >"$rename_root/repeat.json"
smoke_assert_file_contains "$rename_root/repeat.json" '"outcome": "no-op"'

rename_other_after="$(rename_other_tmux show-options -gqv @projmux_rename_sentinel):$(rename_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"
if [[ "$rename_other_after" != "$rename_other_before" ]]; then
  echo "rename/rebind touched the foreign socket" >&2
  exit 1
fi

rename_cleanup
trap smoke_cleanup_env EXIT
echo ">> rename/rebind e2e passed: socket=$rename_socket path=$rename_socket_path other-socket=$rename_other_socket other-path=$rename_other_socket_path cleanup=validated-exact-sockets"

# Explicit Registry desired-topology materialization uses its own exact
# two-socket environment. Every setup/read/repair command strips inherited
# client state and names an exact -L socket; nothing here relies on a default
# socket, an inherited $TMUX, or TMUX_TMPDIR alone.
topology_root="$PROJMUX_SMOKE_WORKDIR/registry-topology"
topology_socket="projmux-topology-$RANDOM-$$"
topology_other_socket="projmux-topology-other-$RANDOM-$$"
topology_session="work-alpha"
mkdir -p \
  "$topology_root/home" \
  "$topology_root/config" \
  "$topology_root/state" \
  "$topology_root/runtime" \
  "$topology_root/tmux" \
  "$topology_root/work/alpha" \
  "$topology_root/work/alpha/logs" \
  "$topology_root/shim"
chmod 0700 "$topology_root/runtime" "$topology_root/tmux"

# The container's default shell exits immediately in a detached pane, which
# would make every materialized Pane disappear before it could be observed.
# A persistent stub shell keeps the runtime graph inspectable without giving
# any Pane a stored command.
topology_shell="$topology_root/shim/persistent-shell"
cat >"$topology_shell" <<'TOPOLOGY_SHELL_STUB'
#!/usr/bin/env bash
exec sleep 600
TOPOLOGY_SHELL_STUB
chmod 0755 "$topology_shell"

topology_tmux() {
  env -u TMUX -u TMUX_PANE \
    HOME="$topology_root/home" \
    XDG_CONFIG_HOME="$topology_root/config" \
    XDG_STATE_HOME="$topology_root/state" \
    XDG_RUNTIME_DIR="$topology_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$topology_root/work" \
    TMUX_TMPDIR="$topology_root/tmux" \
    SHELL="$topology_shell" \
    tmux -L "$topology_socket" "$@"
}

topology_other_tmux() {
  env -u TMUX -u TMUX_PANE \
    HOME="$topology_root/home" \
    XDG_CONFIG_HOME="$topology_root/config" \
    XDG_STATE_HOME="$topology_root/state" \
    XDG_RUNTIME_DIR="$topology_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$topology_root/work" \
    TMUX_TMPDIR="$topology_root/tmux" \
    SHELL="$topology_shell" \
    tmux -L "$topology_other_socket" "$@"
}

topology_pmx() {
  env -u TMUX -u TMUX_PANE \
    HOME="$topology_root/home" \
    XDG_CONFIG_HOME="$topology_root/config" \
    XDG_STATE_HOME="$topology_root/state" \
    XDG_RUNTIME_DIR="$topology_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$topology_root/work" \
    TMUX_TMPDIR="$topology_root/tmux" \
    PATH="$topology_root/shim:$PATH" \
    SHELL="$topology_shell" \
    "$bin" "$@"
}

# Resource creation routes through the client's inherited socket rather than a
# --socket flag, so the live half of the fixture names the exact synthetic
# client socket path instead of falling back to the default app socket.
topology_live_pmx() {
  env -u TMUX_PANE \
    HOME="$topology_root/home" \
    XDG_CONFIG_HOME="$topology_root/config" \
    XDG_STATE_HOME="$topology_root/state" \
    XDG_RUNTIME_DIR="$topology_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$topology_root/work" \
    TMUX_TMPDIR="$topology_root/tmux" \
    TMUX="$topology_socket_path,$topology_socket_pid,0" \
    SHELL="$topology_shell" \
    "$bin" "$@"
}

topology_create_pmx() {
  env \
    HOME="$topology_root/home" \
    XDG_CONFIG_HOME="$topology_root/config" \
    XDG_STATE_HOME="$topology_root/state" \
    XDG_RUNTIME_DIR="$topology_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$topology_root/work" \
    TMUX_TMPDIR="$topology_root/tmux" \
    TMUX="$topology_socket_path,$topology_socket_pid,0" \
    TMUX_PANE="$topology_create_anchor_pane" \
    SHELL="$topology_shell" \
    "$bin" "$@"
}

# Every projmux tmux call in this block is explicit -L/-S and passes through
# untouched; the shim's remaining job is to route a bare `tmux` onto the exact
# smoke socket. The `-L projmux` branch is kept as a tripwire: nothing should
# reach it any more, because `delete` now names its server like every other
# exact-transport route.
topology_real_tmux="$(command -v tmux)"
mkdir -p "$topology_root/bin"
cat >"$topology_root/bin/tmux" <<TOPOLOGY_TMUX_SHIM
#!/usr/bin/env bash
if [[ "\${1:-}" == "-L" && "\${2:-}" == "projmux" ]]; then
  shift 2
  exec $(printf %q "$topology_real_tmux") -L $(printf %q "$topology_socket") "\$@"
fi
if [[ "\${1:-}" == "-L" || "\${1:-}" == "-S" ]]; then
  exec $(printf %q "$topology_real_tmux") "\$@"
fi
exec $(printf %q "$topology_real_tmux") -L $(printf %q "$topology_socket") "\$@"
TOPOLOGY_TMUX_SHIM
chmod 0755 "$topology_root/bin/tmux"

topology_app_pmx() {
  env -u TMUX -u TMUX_PANE \
    HOME="$topology_root/home" \
    XDG_CONFIG_HOME="$topology_root/config" \
    XDG_STATE_HOME="$topology_root/state" \
    XDG_RUNTIME_DIR="$topology_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$topology_root/work" \
    TMUX_TMPDIR="$topology_root/tmux" \
    PATH="$topology_root/bin:$PATH" \
    SHELL="$topology_shell" \
    "$bin" "$@"
}

topology_tmux new-session -d -s "$topology_session" -n main -c "$topology_root/work/alpha" sleep 600
topology_tmux set-option -t "$topology_session" -q @projmux_project_path "$topology_root/work/alpha"
topology_other_tmux new-session -d -s untouched -c "$topology_root/work/alpha" sleep 600
topology_other_tmux set-option -gq @projmux_topology_sentinel unchanged

topology_socket_path="$(topology_tmux display-message -p -t "$topology_session" '#{socket_path}')"
topology_socket_pid="$(topology_tmux display-message -p -t "$topology_session" '#{pid}')"
topology_other_socket_path="$(topology_other_tmux display-message -p -t untouched '#{socket_path}')"
for actual in "$topology_socket_path" "$topology_other_socket_path"; do
  case "$actual" in
    "$topology_root"/*) ;;
    *)
      echo "topology e2e socket escaped smoke root: $actual" >&2
      exit 1
      ;;
  esac
done

topology_cleanup() {
  local socket actual
  for socket in "$topology_socket" "$topology_other_socket"; do
    actual="$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$topology_root/tmux" tmux -L "$socket" display-message -p '#{socket_path}' 2>/dev/null || true)"
    if [[ -z "$actual" ]]; then
      continue
    fi
    case "$actual" in
      "$topology_root"/*) env -u TMUX -u TMUX_PANE tmux -S "$actual" kill-server >/dev/null 2>&1 || true ;;
      *) echo "refusing topology cleanup outside smoke root: $actual" >&2 ;;
    esac
  done
}
trap 'topology_cleanup; smoke_cleanup_env' EXIT

topology_pmx reconcile resources --socket "$topology_socket" --dry-run -o json >"$topology_root/d2.json"
smoke_assert_file_contains "$topology_root/d2.json" '"outcome": "no-op"'
if [[ -e "$topology_root/state/projmux/metadata/registry.json" ]]; then
  echo "topology D2 dry-run created a Registry" >&2
  exit 1
fi
topology_pmx create project --root "$topology_root/work/alpha" --name alpha >"$topology_root/register-alpha.out"
topology_pmx internal tmux apply --bin "$bin" --config "$topology_root/config/projmux/tmux.conf" --socket "$topology_socket" >"$topology_root/apply.out"
e2e_bounded_reconcile_to_noop --allow-initial-noop "$topology_root/import" \
  topology_pmx reconcile resources --socket "$topology_socket" -o json

topology_project_uid="$(topology_tmux show-options -qv -t "$topology_session" @projmux_project_uid)"
if [[ -z "$topology_project_uid" ]]; then
  echo "topology e2e explicit authority left the Project uid empty" >&2
  exit 1
fi
topology_create_anchor_pane="$(topology_tmux list-panes -s -t "$topology_session" -F '#{pane_id}')"
if [[ ! "$topology_create_anchor_pane" =~ ^%[0-9]+$ ]]; then
  echo "topology create requires exactly one initial managed Pane: $topology_create_anchor_pane" >&2
  exit 1
fi
topology_create_anchor_receipt="$(
  topology_tmux display-message -p -t "$topology_create_anchor_pane" \
    '#{socket_path}|#{pid}|#{session_id}|#{window_id}|#{pane_id}|#{@projmux_project_uid}|#{@projmux_window_uid}|#{@projmux_pane_uid}|receipt-end'
)"
IFS='|' read -r topology_anchor_socket topology_anchor_pid topology_anchor_session topology_anchor_window \
  topology_anchor_pane topology_anchor_project_uid topology_anchor_window_uid topology_anchor_pane_uid \
  topology_anchor_end <<<"$topology_create_anchor_receipt"
if [[ "$topology_anchor_socket" != "$topology_socket_path" ]] || \
  [[ "$topology_anchor_pid" != "$topology_socket_pid" ]] || \
  [[ ! "$topology_anchor_session" =~ ^\$[0-9]+$ ]] || \
  [[ ! "$topology_anchor_window" =~ ^@[0-9]+$ ]] || \
  [[ "$topology_anchor_pane" != "$topology_create_anchor_pane" ]] || \
  [[ "$topology_anchor_project_uid" != "$topology_project_uid" ]] || \
  [[ -z "$topology_anchor_window_uid" ]] || [[ -z "$topology_anchor_pane_uid" ]] || \
  [[ "$topology_anchor_end" != "receipt-end" ]]; then
  echo "topology create lacks exact managed anchor containment: $topology_create_anchor_receipt" >&2
  exit 1
fi
topology_create_pmx create window --project "uid:$topology_project_uid" --name review >"$topology_root/create-window.out"
# The stored command is recorded as a one-time name seed. Materialization must
# never execute it, which the recreated Pane's start command proves below: it
# names the managed process supervisor over the default shell, never this.
topology_stored_command=(sleep 600)
topology_create_pmx create pane --project "uid:$topology_project_uid" --window review --placement right -o pane-id -- "${topology_stored_command[@]}" >"$topology_root/create-pane.out"

# A stored Agent with a recorded conversation. The guard at the end of this
# block used to assert that materialization started no Agent; against a fixture
# with no Agent in it that was vacuously true, and it now contradicts the shipped
# contract, which is that materialization DOES replay stored Agents and DOES
# resume the conversation `status.sessionRef` names. The provider is a PATH shim
# that records its argv, so the resume is asserted without a real provider.
# The shell Pane uid set is captured before the Agent exists. A replayed Agent is
# rebound into a *new* managed Pane resource -- releasing the dead one is what
# clears the stale binding -- so its uid legitimately changes across a kill and a
# replay, while every Window-owned shell Pane must keep the uid it was created
# with. Splitting the two is what keeps the shell-Pane identity assertion exact
# instead of loosening it to accommodate the Agent.
topology_shell_pane_uids="$(topology_pmx get panes --project "uid:$topology_project_uid" -o uid | sort)"

topology_agent_argv="$topology_root/codex-argv.log"
: >"$topology_agent_argv"
cat >"$topology_root/shim/codex" <<TOPOLOGY_CODEX_STUB
#!/usr/bin/env bash
printf '%s\n' "\$*" >>$(printf %q "$topology_agent_argv")
exec sleep 600
TOPOLOGY_CODEX_STUB
chmod 0755 "$topology_root/shim/codex"
topology_agent_pane="$(PATH="$topology_root/shim:$PATH" topology_create_pmx create agent --provider codex \
  --project "uid:$topology_project_uid" --window review -o pane-id)"
# Agent reads run through the client-socket helper. A read with no inherited
# $TMUX and no -L would observe live Agent state against the *default* socket,
# which is a server this smoke never created.
topology_agent_uid="$(topology_live_pmx get agents --project "uid:$topology_project_uid" -o uid | tail -n 1)"
if [[ -z "$topology_agent_pane" || -z "$topology_agent_uid" ]]; then
  echo "topology e2e fixture did not create an Agent" >&2
  exit 1
fi
topology_hook_pmx() {
  env -u TMUX -u TMUX_PANE \
    HOME="$topology_root/home" \
    XDG_CONFIG_HOME="$topology_root/config" \
    XDG_STATE_HOME="$topology_root/state" \
    XDG_RUNTIME_DIR="$topology_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$topology_root/work" \
    TMUX_TMPDIR="$topology_root/tmux" \
    TMUX="$topology_socket_path,$topology_socket_pid,0" \
    TMUX_PANE="$topology_agent_pane" \
    PATH="$topology_root/shim:$PATH" \
    SHELL="$topology_shell" \
    "$bin" "$@"
}
printf '%s' '{"hook_event_name":"UserPromptSubmit","thread_id":"topology-thread","session_id":"topology-session","turn_id":"topology-turn","cwd":"'"$topology_root"'/work/alpha"}' |
  topology_hook_pmx internal agent-hook ingest codex-hook >"$topology_root/agent-ingest.out"
topology_live_pmx describe agent "uid:$topology_agent_uid" -o json >"$topology_root/agent-before.json"
smoke_assert_file_contains "$topology_root/agent-before.json" 'topology-thread'

topology_window_uids="$(topology_pmx get windows --project "uid:$topology_project_uid" -o uid | sort)"
topology_pane_uids="$(topology_pmx get panes --project "uid:$topology_project_uid" -o uid | sort)"
topology_extra_pane="$(tr -d '[:space:]' <"$topology_root/create-pane.out")"
topology_extra_pane_uid="$(topology_tmux show-options -pqv -t "$topology_extra_pane" @projmux_pane_uid)"
if [[ "$(printf '%s\n' "$topology_window_uids" | wc -l)" != "2" ]] || [[ -z "$topology_extra_pane_uid" ]]; then
  echo "topology e2e fixture did not build two Windows and an extra shell Pane" >&2
  printf 'windows=%s panes=%s extra-pane=%s extra=%s\n' "$topology_window_uids" "$topology_pane_uids" "$topology_extra_pane" "$topology_extra_pane_uid" >&2
  exit 1
fi
topology_review_window_uid="$(topology_pmx get windows --project "uid:$topology_project_uid" --window review -o uid | tr -d '[:space:]')"
topology_review_window="$(topology_tmux list-windows -t "$topology_session" -F '#{window_id} #{@projmux_window_uid}' | awk -v uid="$topology_review_window_uid" '$2 == uid {print $1}')"
topology_other_before="$(topology_other_tmux show-options -gqv @projmux_topology_sentinel):$(topology_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"

# 1. Raw runtime loss of a whole Window while the Project stays live is drift.
# The preview writes nothing, and execute restores the exact Registry uids.
topology_registry="$topology_root/state/projmux/metadata/registry.json"

# Killing a live pane makes its managed process supervisor record a termination
# receipt, which is an asynchronous Registry write by a process this script does
# not wait on. Every "the Registry did not change" assertion below therefore
# snapshots the file only once those writes have settled.
topology_settle_registry() {
  local previous="" current stable_samples=0
  for _ in $(seq 1 200); do
    current="$(sha256sum "$topology_registry" | cut -d' ' -f1)"
    if [[ -n "$previous" && "$current" == "$previous" ]]; then
      stable_samples=$((stable_samples + 1))
      if [[ "$stable_samples" == "10" ]]; then
        return 0
      fi
    else
      stable_samples=0
    fi
    previous="$current"
    sleep 0.05
  done
  echo "the Registry never settled after a live pane was killed" >&2
  exit 1
}

# topology_assert_panes_converged is the Pane half of "the runtime is exactly the
# Registry". It asserts two independent things rather than one loose one: the
# live Pane uid set equals the Registry Pane uid set as it stands now, and every
# shell Pane uid the fixture created is still in both. A replayed Agent's managed
# Pane is allowed to be a new resource; a shell Pane is not.
topology_assert_panes_converged() {
  local stage="$1" registry live missing
  registry="$(topology_pmx get panes --project "uid:$topology_project_uid" -o uid | sort)"
  live="$(topology_tmux list-panes -s -t "$topology_session" -F '#{@projmux_pane_uid}' | sort)"
  if [[ "$registry" != "$live" ]]; then
    echo "$stage did not restore the exact Registry Pane uids" >&2
    printf 'registry=%s\nlive=%s\n' "$registry" "$live" >&2
    exit 1
  fi
  missing="$(comm -23 <(printf '%s\n' "$topology_shell_pane_uids") <(printf '%s\n' "$live"))"
  if [[ -n "$missing" ]]; then
    echo "$stage lost shell Pane uids that must never change: $missing" >&2
    exit 1
  fi
}

topology_tmux kill-window -t "$topology_review_window"
topology_settle_registry
topology_registry_before="$(sha256sum "$topology_registry" | cut -d' ' -f1)"
topology_pmx reconcile resources --socket "$topology_socket" --materialize-project "uid:$topology_project_uid" --dry-run -o json >"$topology_root/partial-dry-run.json"
if [[ "$(sha256sum "$topology_registry" | cut -d' ' -f1)" != "$topology_registry_before" ]]; then
  echo "topology dry-run wrote to the Registry" >&2
  exit 1
fi
if [[ "$(topology_tmux list-windows -t "$topology_session" -F '#{window_id}' | wc -l)" != "1" ]]; then
  echo "topology dry-run created runtime objects" >&2
  exit 1
fi
smoke_assert_file_contains "$topology_root/partial-dry-run.json" '"action": "materialize"'
smoke_assert_file_contains "$topology_root/partial-dry-run.json" "uid:$topology_review_window_uid"

topology_pmx reconcile resources --socket "$topology_socket" --materialize-project "uid:$topology_project_uid" -o json >"$topology_root/partial.json"
topology_window_uids_after="$(topology_tmux list-windows -t "$topology_session" -F '#{@projmux_window_uid}' | sort)"
if [[ "$topology_window_uids_after" != "$topology_window_uids" ]]; then
  echo "live partial materialization did not restore the exact Registry Window uids" >&2
  printf 'want=%s got=%s\n' "$topology_window_uids" "$topology_window_uids_after" >&2
  exit 1
fi
topology_assert_panes_converged "live partial materialization"

# 2. A converged graph is a true no-op: no create and no Registry byte change.
topology_settle_registry
topology_registry_before="$(sha256sum "$topology_registry" | cut -d' ' -f1)"
topology_runtime_before="$(topology_tmux list-panes -s -t "$topology_session" -F '#{window_id}:#{pane_id}:#{@projmux_pane_uid}' | sort)"
topology_pmx reconcile resources --socket "$topology_socket" --materialize-project "uid:$topology_project_uid" -o json >"$topology_root/repeat.json"
smoke_assert_file_contains "$topology_root/repeat.json" '"outcome": "no-op"'
if [[ "$(sha256sum "$topology_registry" | cut -d' ' -f1)" != "$topology_registry_before" ]] ||
  [[ "$(topology_tmux list-panes -s -t "$topology_session" -F '#{window_id}:#{pane_id}:#{@projmux_pane_uid}' | sort)" != "$topology_runtime_before" ]]; then
  echo "repeat materialization was not a zero-write no-op" >&2
  exit 1
fi

# The recreated Window's panes run the default shell, never the stored command.
# The main Window's Pane is excluded because the fixture itself created it with
# a raw `sleep 600`, which is not a materialization input.
topology_review_window="$(topology_tmux list-windows -t "$topology_session" -F '#{window_id} #{@projmux_window_uid}' | awk -v uid="$topology_review_window_uid" '$2 == uid {print $1}')"
# Materialized panes launch through `internal supervise` over tmux's own
# default shell, so a start command is expected. What must never appear in it is
# the stored `Pane.spec.command`, which remains a one-time name seed.
topology_start_commands="$(topology_tmux list-panes -t "$topology_review_window" -F '#{pane_start_command}')"
while IFS= read -r topology_start_command; do
  [[ -n "$topology_start_command" ]] || continue
  case "$topology_start_command" in
    *"internal supervise"*) ;;
    *)
      echo "materialization launched a pane outside the supervisor: $topology_start_command" >&2
      exit 1
      ;;
  esac
  if [[ "$topology_start_command" == *"${topology_stored_command[*]}"* ]]; then
    echo "materialization replayed a stored Pane command: $topology_start_command" >&2
    exit 1
  fi
done <<<"$topology_start_commands"

# 3. Canonical delete removes the desire itself, so the Pane is not replayed --
# unlike the raw kill above, which is drift.
topology_app_pmx delete pane "uid:$topology_extra_pane_uid" --socket "$topology_socket" --yes >"$topology_root/delete-pane.out"
topology_pmx reconcile resources --socket "$topology_socket" --materialize-project "uid:$topology_project_uid" -o json >"$topology_root/after-delete.json"
if topology_tmux list-panes -s -t "$topology_session" -F '#{@projmux_pane_uid}' | grep -Fqx "$topology_extra_pane_uid"; then
  echo "canonical delete was replayed by materialization" >&2
  exit 1
fi
topology_shell_pane_uids="$(comm -12 \
  <(printf '%s\n' "$topology_shell_pane_uids") \
  <(topology_pmx get panes --project "uid:$topology_project_uid" -o uid | sort))"

# 4. An offline Project is dormant, not deleted. Explicit materialization on the
# exact socket rebuilds the whole stored topology under its original uids.
topology_tmux kill-server >/dev/null 2>&1 || true
topology_pmx reconcile resources --socket "$topology_socket" --materialize-project "uid:$topology_project_uid" -o json >"$topology_root/offline-full.json"
topology_window_uids_after="$(topology_tmux list-windows -t "$topology_session" -F '#{@projmux_window_uid}' | sort)"
if [[ "$topology_window_uids_after" != "$topology_window_uids" ]]; then
  echo "offline full materialization did not restore the exact Registry Window uids" >&2
  printf 'want=%s got=%s\n' "$topology_window_uids" "$topology_window_uids_after" >&2
  exit 1
fi
topology_assert_panes_converged "offline full materialization"
if [[ "$(topology_tmux show-options -qv -t "$topology_session" @projmux_project_uid)" != "$topology_project_uid" ]]; then
  echo "offline full materialization did not restore the exact Project uid binding" >&2
  exit 1
fi
topology_pmx reconcile resources --socket "$topology_socket" --materialize-project "uid:$topology_project_uid" -o json >"$topology_root/offline-repeat.json"
smoke_assert_file_contains "$topology_root/offline-repeat.json" '"outcome": "no-op"'

# Offline materialization started a new server generation. Resolve the durable
# primary Pane UID back to one exact live handle before any inherited-route
# read; retaining the old PID or guessing the first Pane would be false client
# authority even though these calls are observational.
mapfile -t topology_recreated_anchor_matches < <(
  topology_tmux list-panes -s -t "$topology_session" -F '#{pane_id}|#{@projmux_pane_uid}' |
    awk -F '|' -v uid="$topology_anchor_pane_uid" '$2 == uid { print $1 }'
)
if [[ "${#topology_recreated_anchor_matches[@]}" != "1" ]] || \
  [[ ! "${topology_recreated_anchor_matches[0]}" =~ ^%[0-9]+$ ]]; then
  echo "offline topology replay did not restore one durable anchor for $topology_anchor_pane_uid" >&2
  printf '%s\n' "${topology_recreated_anchor_matches[@]}" >&2
  exit 1
fi
topology_recreated_anchor="${topology_recreated_anchor_matches[0]}"
topology_recreated_receipt="$(
  topology_tmux display-message -p -t "$topology_recreated_anchor" \
    '#{socket_path}|#{pid}|#{session_id}|#{window_id}|#{pane_id}|#{@projmux_project_uid}|#{@projmux_window_uid}|#{@projmux_pane_uid}|receipt-end'
)"
IFS='|' read -r topology_recreated_socket topology_recreated_pid topology_recreated_session \
  topology_recreated_window topology_recreated_pane topology_recreated_project_uid \
  topology_recreated_window_uid topology_recreated_pane_uid topology_recreated_end \
  <<<"$topology_recreated_receipt"
if [[ "$topology_recreated_socket" != "$topology_socket_path" ]] || \
  [[ ! "$topology_recreated_pid" =~ ^[1-9][0-9]*$ ]] || \
  [[ ! "$topology_recreated_session" =~ ^\$[0-9]+$ ]] || \
  [[ ! "$topology_recreated_window" =~ ^@[0-9]+$ ]] || \
  [[ "$topology_recreated_pane" != "$topology_recreated_anchor" ]] || \
  [[ "$topology_recreated_project_uid" != "$topology_project_uid" ]] || \
  [[ "$topology_recreated_window_uid" != "$topology_anchor_window_uid" ]] || \
  [[ "$topology_recreated_pane_uid" != "$topology_anchor_pane_uid" ]] || \
  [[ "$topology_recreated_end" != "receipt-end" ]]; then
  echo "offline topology replay anchor containment drifted: $topology_recreated_receipt" >&2
  exit 1
fi
topology_socket_pid="$topology_recreated_pid"

# 5. The stored Agent is replayed, and the conversation its `status.sessionRef`
# names is resumed rather than silently replaced by a new one. The stored Pane
# command is still never executed; that is asserted in section 2 above.
topology_live_pmx describe agent "uid:$topology_agent_uid" -o json >"$topology_root/agent-after.json"
smoke_assert_file_contains "$topology_root/agent-after.json" '"phase": "Running"'
smoke_assert_file_contains "$topology_root/agent-after.json" 'topology-thread'
topology_resume_seen=0
for _ in $(seq 1 200); do
  if grep -Fq "resume topology-thread" "$topology_agent_argv"; then
    topology_resume_seen=1
    break
  fi
  sleep 0.05
done
if [[ "$topology_resume_seen" != "1" ]]; then
  echo "materialization did not resume the Agent's recorded conversation" >&2
  cat "$topology_agent_argv" >&2 || true
  exit 1
fi

# 6. The final-v2 Agent-only Window shape is now exercised against the same
# installed binary and real tmux server, not only the fake runtime. Save the
# live graph while its Agent still owns one exact managed Pane, stop the exact
# smoke socket, then use the test-only fixture utility to remove this Window's
# direct shells and make that retained Agent Pane its required anchor. No
# product lifecycle/delete route is used to manufacture the offline fixture.
topology_settle_registry
topology_agent_only_source="$topology_root/agent-only-source.json"
cp "$topology_registry" "$topology_agent_only_source"
IFS=$'\t' read -r _ _ topology_agent_anchor_uid _ _ _ < <(
  go run ./test/e2e/anchorfixture inspect "$topology_root" "agent-only-source.json" "$topology_review_window_uid" "$topology_agent_uid"
)
if [[ -z "$topology_agent_anchor_uid" ]]; then
  echo "Agent-only materialization fixture has no retained Agent Pane uid" >&2
  exit 1
fi
topology_tmux kill-server >/dev/null 2>&1 || true
topology_settle_registry
topology_rewritten_anchor_uid="$(
  go run ./test/e2e/anchorfixture rewrite \
    "$topology_root" "agent-only-source.json" "state/projmux/metadata/registry.json" \
    "$topology_review_window_uid" "$topology_agent_uid"
)"
if [[ "$topology_rewritten_anchor_uid" != "$topology_agent_anchor_uid" ]]; then
  echo "Agent-only fixture changed exact Agent anchor uid: $topology_agent_anchor_uid -> $topology_rewritten_anchor_uid" >&2
  exit 1
fi

topology_registry_before="$(sha256sum "$topology_registry" | cut -d' ' -f1)"
topology_pmx reconcile resources --socket "$topology_socket" --materialize-project "uid:$topology_project_uid" --dry-run -o json >"$topology_root/agent-only-dry-run.json"
smoke_assert_file_contains "$topology_root/agent-only-dry-run.json" '"action": "allocate default shell"'
smoke_assert_file_contains "$topology_root/agent-only-dry-run.json" '"kind": "Agent"'
if [[ "$(sha256sum "$topology_registry" | cut -d' ' -f1)" != "$topology_registry_before" ]]; then
  echo "Agent-only dry-run wrote to the Registry" >&2
  exit 1
fi
if topology_tmux list-sessions >"$topology_root/agent-only-dry-run-sessions.out" 2>&1; then
  echo "Agent-only dry-run materialized an offline tmux server/session" >&2
  exit 1
fi

topology_pmx reconcile resources --socket "$topology_socket" --materialize-project "uid:$topology_project_uid" -o json >"$topology_root/agent-only-execute.json"
IFS=$'\t' read -r topology_post_anchor_uid topology_post_default_uid topology_post_agent_pane_uid \
  topology_post_default_owner_kind topology_post_default_owner_uid topology_post_default_role < <(
    go run ./test/e2e/anchorfixture inspect "$topology_root" "state/projmux/metadata/registry.json" \
      "$topology_review_window_uid" "$topology_agent_uid"
  )
if [[ "$topology_post_anchor_uid" != "$topology_agent_anchor_uid" ]] || \
  [[ "$topology_post_agent_pane_uid" != "$topology_agent_anchor_uid" ]] || \
  [[ -z "$topology_post_default_uid" ]] || \
  [[ "$topology_post_default_uid" == "$topology_agent_anchor_uid" ]] || \
  [[ "$topology_post_default_owner_kind" != "Window" ]] || \
  [[ "$topology_post_default_owner_uid" != "$topology_review_window_uid" ]] || \
  [[ "$topology_post_default_role" != "shell" ]]; then
  echo "Agent-only execute did not produce the exact valid anchor/default graph" >&2
  printf 'anchor=%s agent-pane=%s default=%s owner=%s/%s role=%s\n' \
    "$topology_post_anchor_uid" "$topology_post_agent_pane_uid" "$topology_post_default_uid" \
    "$topology_post_default_owner_kind" "$topology_post_default_owner_uid" "$topology_post_default_role" >&2
  exit 1
fi
topology_review_window="$(topology_tmux list-windows -t "$topology_session" -F '#{window_id} #{@projmux_window_uid}' | awk -v uid="$topology_review_window_uid" '$2 == uid {print $1}')"
mapfile -t topology_live_agent_anchor_matches < <(
  topology_tmux list-panes -t "$topology_review_window" -F '#{@projmux_pane_uid}' | grep -Fx "$topology_agent_anchor_uid"
)
mapfile -t topology_live_default_matches < <(
  topology_tmux list-panes -t "$topology_review_window" -F '#{@projmux_pane_uid}' | grep -Fx "$topology_post_default_uid"
)
if [[ "${#topology_live_agent_anchor_matches[@]}" != "1" ]] || \
  [[ "${#topology_live_default_matches[@]}" != "1" ]]; then
  echo "Agent-only execute lacks exact live Agent/default Pane uid bindings" >&2
  topology_tmux list-panes -t "$topology_review_window" -F '#{pane_id}|#{@projmux_pane_uid}' >&2
  exit 1
fi
topology_agent_only_socket_path="$(topology_tmux display-message -p -t "$topology_session" '#{socket_path}')"
if [[ "$topology_agent_only_socket_path" != "$topology_socket_path" ]]; then
  echo "Agent-only execute changed socket authority: $topology_socket_path -> $topology_agent_only_socket_path" >&2
  exit 1
fi
case "$topology_agent_only_socket_path" in
  "$topology_root"/*) ;;
  *)
    echo "Agent-only execute socket escaped smoke root: $topology_agent_only_socket_path" >&2
    exit 1
    ;;
esac

topology_settle_registry
topology_registry_before="$(sha256sum "$topology_registry" | cut -d' ' -f1)"
topology_runtime_before="$(topology_tmux list-panes -s -t "$topology_session" -F '#{window_id}:#{pane_id}:#{@projmux_pane_uid}' | sort)"
topology_pmx reconcile resources --socket "$topology_socket" --materialize-project "uid:$topology_project_uid" -o json >"$topology_root/agent-only-repeat.json"
smoke_assert_file_contains "$topology_root/agent-only-repeat.json" '"outcome": "no-op"'
if [[ "$(sha256sum "$topology_registry" | cut -d' ' -f1)" != "$topology_registry_before" ]] || \
  [[ "$(topology_tmux list-panes -s -t "$topology_session" -F '#{window_id}:#{pane_id}:#{@projmux_pane_uid}' | sort)" != "$topology_runtime_before" ]]; then
  echo "Agent-only repeat was not a Registry/runtime no-op" >&2
  exit 1
fi

topology_other_after="$(topology_other_tmux show-options -gqv @projmux_topology_sentinel):$(topology_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"
if [[ "$topology_other_after" != "$topology_other_before" ]]; then
  echo "topology materialization touched the foreign socket" >&2
  exit 1
fi

topology_cleanup
trap smoke_cleanup_env EXIT
echo ">> Registry topology materialization e2e passed: socket=$topology_socket path=$topology_socket_path other-socket=$topology_other_socket other-path=$topology_other_socket_path cleanup=validated-exact-sockets"

# Closed Project managed startup parity gets its own exact two-socket
# environment and its own real attached tmux client. Opening a closed Project is
# an explicit activation, so this section drives the installed `switch open`
# route from inside a client rather than calling the reconcile CLI: the client
# move is the half that only a real client can prove.
startup_root="$PROJMUX_SMOKE_WORKDIR/topology-startup"
startup_socket="projmux-startup-$RANDOM-$$"
startup_other_socket="projmux-startup-other-$RANDOM-$$"
startup_driver="startup-driver"
# The namer derives the session name from <parent>-<base> of the Project root, so
# the fixture session must be named exactly what a later `switch open` resolves.
startup_session="work-alpha"
mkdir -p \
  "$startup_root/home" \
  "$startup_root/config" \
  "$startup_root/state" \
  "$startup_root/runtime" \
  "$startup_root/tmux" \
  "$startup_root/work/alpha" \
  "$startup_root/shim" \
  "$startup_root/bin"
chmod 0700 "$startup_root/runtime" "$startup_root/tmux"
startup_project="$startup_root/work/alpha"

# A detached pane running the container's default shell exits immediately, which
# would destroy every materialized Pane before it could be observed.
startup_shell="$startup_root/shim/persistent-shell"
cat >"$startup_shell" <<'STARTUP_SHELL_STUB'
#!/usr/bin/env bash
exec sleep 600
STARTUP_SHELL_STUB
chmod 0755 "$startup_shell"

startup_tmux() {
  env -u TMUX -u TMUX_PANE \
    HOME="$startup_root/home" \
    XDG_CONFIG_HOME="$startup_root/config" \
    XDG_STATE_HOME="$startup_root/state" \
    XDG_RUNTIME_DIR="$startup_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$startup_root/work" \
    TMUX_TMPDIR="$startup_root/tmux" \
    SHELL="$startup_shell" \
    tmux -L "$startup_socket" "$@"
}

startup_other_tmux() {
  env -u TMUX -u TMUX_PANE \
    HOME="$startup_root/home" \
    XDG_CONFIG_HOME="$startup_root/config" \
    XDG_STATE_HOME="$startup_root/state" \
    XDG_RUNTIME_DIR="$startup_root/runtime" \
    TMUX_TMPDIR="$startup_root/tmux" \
    SHELL="$startup_shell" \
    tmux -L "$startup_other_socket" "$@"
}

startup_pmx() {
  env -u TMUX -u TMUX_PANE \
    HOME="$startup_root/home" \
    XDG_CONFIG_HOME="$startup_root/config" \
    XDG_STATE_HOME="$startup_root/state" \
    XDG_RUNTIME_DIR="$startup_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$startup_root/work" \
    TMUX_TMPDIR="$startup_root/tmux" \
    SHELL="$startup_shell" \
    "$bin" "$@"
}

# `switch open` resolves the app socket by its default name, exactly as the
# installed build does. A minimal shim maps that one default `-L projmux` route
# onto this smoke socket; every explicit -L/-S call passes through untouched.
startup_real_tmux="$(command -v tmux)"
cat >"$startup_root/bin/tmux" <<STARTUP_TMUX_SHIM
#!/usr/bin/env bash
if [[ "\${1:-}" == "-L" && "\${2:-}" == "projmux" ]]; then
  shift 2
  exec $(printf %q "$startup_real_tmux") -L $(printf %q "$startup_socket") "\$@"
fi
if [[ "\${1:-}" == "-L" || "\${1:-}" == "-S" ]]; then
  exec $(printf %q "$startup_real_tmux") "\$@"
fi
if [[ "\${1:-}" == "display-message" ]]; then
  printf '%s\n' "\$*" >>$(printf %q "$startup_root/display-messages.log")
fi
exec $(printf %q "$startup_real_tmux") -L $(printf %q "$startup_socket") "\$@"
STARTUP_TMUX_SHIM
chmod 0755 "$startup_root/bin/tmux"

# The open runs from inside the attached client's pane so `$TMUX`/`$TMUX_PANE`
# are the real inherited client state: that is the only way the switch-client
# half of the contract is exercised rather than simulated.
cat >"$startup_root/open-project.sh" <<STARTUP_OPEN_SCRIPT
#!/usr/bin/env bash
export HOME="$startup_root/home"
export XDG_CONFIG_HOME="$startup_root/config"
export XDG_STATE_HOME="$startup_root/state"
export XDG_RUNTIME_DIR="$startup_root/runtime"
export PROJMUX_MANAGED_ROOTS="$startup_root/work"
export TMUX_TMPDIR="$startup_root/tmux"
export SHELL="$startup_shell"
export PATH="$startup_root/bin:$startup_root/shim:\$PATH"
$(printf %q "$bin") switch open "\$1" >"$startup_root/open-\$2.out" 2>"$startup_root/open-\$2.err"
echo \$? >"$startup_root/open-\$2.rc"
STARTUP_OPEN_SCRIPT
chmod 0755 "$startup_root/open-project.sh"

# The `Open fresh` row is a one-step neutral action. Drive the same detached
# continuation that the sidebar launches so no confirmation or destructive
# workflow can be hidden behind the picker transport.
cat >"$startup_root/open-fresh.sh" <<STARTUP_FRESH_SCRIPT
#!/usr/bin/env bash
export HOME="$startup_root/home"
export XDG_CONFIG_HOME="$startup_root/config"
export XDG_STATE_HOME="$startup_root/state"
export XDG_RUNTIME_DIR="$startup_root/runtime"
export PROJMUX_MANAGED_ROOTS="$startup_root/work"
export TMUX_TMPDIR="$startup_root/tmux"
export SHELL="$startup_shell"
export PATH="$startup_root/bin:$startup_root/shim:\$PATH"
env -u TMUX -u TMUX_PANE $(printf %q "$bin") switch sidebar-open --path "\$1" --session "\$2" --mode fresh --client "\$3" \
  >"$startup_root/open-new.out" 2>"$startup_root/open-new.err"
echo \$? >"$startup_root/open-new.rc"
STARTUP_FRESH_SCRIPT
chmod 0755 "$startup_root/open-fresh.sh"

cat >"$startup_root/open-continue.sh" <<STARTUP_CONTINUE_SCRIPT
#!/usr/bin/env bash
export HOME="$startup_root/home"
export XDG_CONFIG_HOME="$startup_root/config"
export XDG_STATE_HOME="$startup_root/state"
export XDG_RUNTIME_DIR="$startup_root/runtime"
export PROJMUX_MANAGED_ROOTS="$startup_root/work"
export TMUX_TMPDIR="$startup_root/tmux"
export SHELL="$startup_shell"
export PATH="$startup_root/bin:$startup_root/shim:\$PATH"
env -u TMUX -u TMUX_PANE $(printf %q "$bin") switch sidebar-open --path "\$1" --session "\$2" --mode continue --client "\$3" \
  >"$startup_root/open-continue.out" 2>"$startup_root/open-continue.err"
echo \$? >"$startup_root/open-continue.rc"
STARTUP_CONTINUE_SCRIPT
chmod 0755 "$startup_root/open-continue.sh"

cat >"$startup_root/restore-project.sh" <<STARTUP_RESTORE_SCRIPT
#!/usr/bin/env bash
export HOME="$startup_root/home"
export XDG_CONFIG_HOME="$startup_root/config"
export XDG_STATE_HOME="$startup_root/state"
export XDG_RUNTIME_DIR="$startup_root/runtime"
export PROJMUX_MANAGED_ROOTS="$startup_root/work"
export TMUX_TMPDIR="$startup_root/tmux"
export SHELL="$startup_shell"
export PATH="$startup_root/bin:$startup_root/shim:\$PATH"
env -u TMUX -u TMUX_PANE $(printf %q "$bin") restore snapshot --session "\$1" --project "\$2" --yes --client "\$3" \
  >"$startup_root/restore-project.out" 2>"$startup_root/restore-project.err"
echo \$? >"$startup_root/restore-project.rc"
STARTUP_RESTORE_SCRIPT
chmod 0755 "$startup_root/restore-project.sh"

startup_tmux new-session -d -s "$startup_session" -n main -c "$startup_project" sleep 600
startup_tmux set-option -t "$startup_session" -q @projmux_project_path "$startup_project"
startup_tmux new-session -d -s "$startup_driver" -c "$startup_root" bash --noprofile --norc
startup_other_tmux new-session -d -s untouched -c "$startup_root" sleep 600
startup_other_tmux set-option -gq @projmux_startup_sentinel unchanged

startup_socket_path="$(startup_tmux display-message -p -t "$startup_session" '#{socket_path}')"
startup_socket_pid="$(startup_tmux display-message -p -t "$startup_session" '#{pid}')"
startup_other_socket_path="$(startup_other_tmux display-message -p -t untouched '#{socket_path}')"
for actual in "$startup_socket_path" "$startup_other_socket_path"; do
  case "$actual" in
    "$startup_root"/*) ;;
    *)
      echo "startup e2e socket escaped smoke root: $actual" >&2
      exit 1
      ;;
  esac
done

startup_live_pmx() {
  env -u TMUX_PANE \
    HOME="$startup_root/home" \
    XDG_CONFIG_HOME="$startup_root/config" \
    XDG_STATE_HOME="$startup_root/state" \
    XDG_RUNTIME_DIR="$startup_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$startup_root/work" \
    TMUX_TMPDIR="$startup_root/tmux" \
    TMUX="$startup_socket_path,$startup_socket_pid,0" \
    SHELL="$startup_shell" \
    "$bin" "$@"
}

startup_create_pmx() {
  env \
    HOME="$startup_root/home" \
    XDG_CONFIG_HOME="$startup_root/config" \
    XDG_STATE_HOME="$startup_root/state" \
    XDG_RUNTIME_DIR="$startup_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$startup_root/work" \
    TMUX_TMPDIR="$startup_root/tmux" \
    TMUX="$startup_socket_path,$startup_socket_pid,0" \
    TMUX_PANE="$startup_create_anchor_pane" \
    SHELL="$startup_shell" \
    "$bin" "$@"
}

startup_cleanup() {
  local socket actual
  for socket in "$startup_socket" "$startup_other_socket"; do
    actual="$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$startup_root/tmux" tmux -L "$socket" display-message -p '#{socket_path}' 2>/dev/null || true)"
    if [[ -z "$actual" ]]; then
      continue
    fi
    case "$actual" in
      "$startup_root"/*) env -u TMUX -u TMUX_PANE tmux -S "$actual" kill-server >/dev/null 2>&1 || true ;;
      *) echo "refusing startup cleanup outside smoke root: $actual" >&2 ;;
    esac
  done
}
trap 'startup_cleanup; smoke_cleanup_env' EXIT

startup_pmx reconcile resources --socket "$startup_socket" --dry-run -o json >"$startup_root/d2.json"
smoke_assert_file_contains "$startup_root/d2.json" '"outcome": "no-op"'
if [[ -e "$startup_root/state/projmux/metadata/registry.json" ]]; then
  echo "startup D2 dry-run created a Registry" >&2
  exit 1
fi
startup_pmx create project --root "$startup_root/work/alpha" --name alpha >"$startup_root/register-alpha.out"
startup_registry="$startup_root/state/projmux/metadata/registry.json"
startup_pmx internal tmux apply --bin "$bin" --config "$startup_root/config/projmux/tmux.conf" --socket "$startup_socket" >"$startup_root/apply.out"
e2e_bounded_reconcile_to_noop --allow-initial-noop "$startup_root/import" \
  startup_pmx reconcile resources --socket "$startup_socket" -o json

startup_project_uid="$(startup_tmux show-options -qv -t "$startup_session" @projmux_project_uid)"
if [[ -z "$startup_project_uid" ]]; then
  echo "startup e2e explicit authority left the Project uid empty" >&2
  exit 1
fi
startup_create_anchor_pane="$(startup_tmux list-panes -s -t "$startup_session" -F '#{pane_id}')"
if [[ ! "$startup_create_anchor_pane" =~ ^%[0-9]+$ ]]; then
  echo "startup create requires exactly one initial managed Pane: $startup_create_anchor_pane" >&2
  exit 1
fi
startup_create_anchor_receipt="$(
  startup_tmux display-message -p -t "$startup_create_anchor_pane" \
    '#{socket_path}|#{pid}|#{session_id}|#{window_id}|#{pane_id}|#{@projmux_project_uid}|#{@projmux_window_uid}|#{@projmux_pane_uid}|receipt-end'
)"
IFS='|' read -r startup_anchor_socket startup_anchor_pid startup_anchor_session startup_anchor_window \
  startup_anchor_pane startup_anchor_project_uid startup_anchor_window_uid startup_anchor_pane_uid \
  startup_anchor_end <<<"$startup_create_anchor_receipt"
if [[ "$startup_anchor_socket" != "$startup_socket_path" ]] || \
  [[ "$startup_anchor_pid" != "$startup_socket_pid" ]] || \
  [[ ! "$startup_anchor_session" =~ ^\$[0-9]+$ ]] || \
  [[ ! "$startup_anchor_window" =~ ^@[0-9]+$ ]] || \
  [[ "$startup_anchor_pane" != "$startup_create_anchor_pane" ]] || \
  [[ "$startup_anchor_project_uid" != "$startup_project_uid" ]] || \
  [[ -z "$startup_anchor_window_uid" ]] || [[ -z "$startup_anchor_pane_uid" ]] || \
  [[ "$startup_anchor_end" != "receipt-end" ]]; then
  echo "startup create lacks exact managed anchor containment: $startup_create_anchor_receipt" >&2
  exit 1
fi
# The stored command is a one-time name seed. A startup that executed it would
# show up inside the recreated Pane's start command below, which must name only
# the managed process supervisor over the default shell.
startup_stored_command=(sleep 600)
startup_create_pmx create window --project "uid:$startup_project_uid" --name review >"$startup_root/create-window.out"
startup_create_pmx create pane --project "uid:$startup_project_uid" --window review --placement right -o pane-id -- "${startup_stored_command[@]}" >"$startup_root/create-pane.out"

# A stored Agent with a recorded conversation.
#
# Without one, the "no Agent was resumed" guard this section used to carry was
# vacuously true -- the fixture had no Agent at all -- and, since closed-Project
# topology materialization now replays stored Agents, it also asserted the exact
# opposite of the shipped contract. The provider is a PATH shim that appends its
# own argv to a log, so "the recorded conversation was resumed" is checked
# against real argv rather than against a real provider.
# The shell Pane uid set is captured before the Agent exists. A replayed Agent is
# rebound into a new managed Pane resource, so its uid legitimately changes across
# a close and a replay; a Window-owned shell Pane's uid never does.
startup_shell_pane_uids="$(startup_pmx get panes --project "uid:$startup_project_uid" -o uid | sort)"

startup_agent_argv="$startup_root/codex-argv.log"
: >"$startup_agent_argv"
cat >"$startup_root/shim/codex" <<STARTUP_CODEX_STUB
#!/usr/bin/env bash
printf '%s\n' "\$*" >>$(printf %q "$startup_agent_argv")
exec sleep 600
STARTUP_CODEX_STUB
chmod 0755 "$startup_root/shim/codex"
startup_agent_pane="$(PATH="$startup_root/shim:$PATH" startup_create_pmx create agent --provider codex \
  --project "uid:$startup_project_uid" --window review -o pane-id)"
# Agent reads run through the client-socket helper, so live Agent state is never
# observed against the default socket this smoke never created.
startup_agent_uid="$(startup_live_pmx get agents --project "uid:$startup_project_uid" -o uid | tail -n 1)"
if [[ -z "$startup_agent_pane" || -z "$startup_agent_uid" ]]; then
  echo "startup e2e fixture did not create an Agent" >&2
  exit 1
fi

# Seed the durable conversation pointer through the canonical hook ingress, the
# only route that writes Agent.status.sessionRef.
startup_hook_pmx() {
  env -u TMUX -u TMUX_PANE \
    HOME="$startup_root/home" \
    XDG_CONFIG_HOME="$startup_root/config" \
    XDG_STATE_HOME="$startup_root/state" \
    XDG_RUNTIME_DIR="$startup_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$startup_root/work" \
    TMUX_TMPDIR="$startup_root/tmux" \
    TMUX="$startup_socket_path,$startup_socket_pid,0" \
    TMUX_PANE="$startup_agent_pane" \
    SHELL="$startup_shell" \
    "$bin" "$@"
}
printf '%s' '{"hook_event_name":"UserPromptSubmit","thread_id":"startup-thread","session_id":"startup-session","turn_id":"startup-turn","cwd":"'"$startup_project"'"}' |
  startup_hook_pmx internal agent-hook ingest codex-hook >"$startup_root/agent-ingest.out"
startup_live_pmx describe agent "uid:$startup_agent_uid" -o json >"$startup_root/agent-before.json"
smoke_assert_file_contains "$startup_root/agent-before.json" 'startup-thread'

startup_window_uids="$(startup_pmx get windows --project "uid:$startup_project_uid" -o uid | sort)"
startup_pane_uids="$(startup_pmx get panes --project "uid:$startup_project_uid" -o uid | sort)"
if [[ "$(printf '%s\n' "$startup_window_uids" | wc -l)" != "2" ]] || [[ "$(printf '%s\n' "$startup_pane_uids" | wc -l)" != "4" ]]; then
  echo "startup e2e fixture did not build two Windows, three shell Panes, and one Agent Pane" >&2
  printf 'windows=%s panes=%s agent=%s\n' "$startup_window_uids" "$startup_pane_uids" "$startup_agent_uid" >&2
  exit 1
fi
startup_other_before="$(startup_other_tmux show-options -gqv @projmux_startup_sentinel):$(startup_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"

# Attach a real client to the driver session. `switch open` runs inside its pane,
# so the client move is a real switch-client on the same exact server.
startup_client_log="$startup_root/driver-client.log"
startup_client_input="$startup_root/driver-client.in"
mkfifo "$startup_client_input"
exec 8<>"$startup_client_input"
TERM=xterm-256color script -qefc \
  "TERM=xterm-256color env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$startup_root/tmux' tmux -L '$startup_socket' attach-session -t '$startup_driver'" \
  "$startup_client_log" <"$startup_client_input" >/dev/null 2>&1 &
startup_client_pid=$!

startup_wait_for() {
  local description="$1"
  shift
  for _ in {1..200}; do
    if "$@"; then
      return 0
    fi
    sleep 0.05
  done
  echo "timed out waiting for $description" >&2
  tail -c 8000 "$startup_client_log" >&2 || true
  return 1
}

startup_client_is_on() {
  local expected="$1"
  [[ "$(startup_tmux display-message -p -c "$startup_client" '#{session_name}' 2>/dev/null)" == "$expected" ]]
}

startup_wait_for "attached startup tmux client" sh -c \
  "test -n \"\$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$startup_root/tmux' tmux -L '$startup_socket' list-clients -F '#{client_name}' 2>/dev/null | head -n 1)\""
startup_client="$(startup_tmux list-clients -F '#{client_name}' | head -n 1)"
startup_driver_pane="$(startup_tmux display-message -p -c "$startup_client" '#{pane_id}')"

# 1. The Project is closed. Opening it must materialize the whole declared shell
# topology under the stored uids and only then move the client.
startup_tmux kill-session -t "$startup_session"
if startup_tmux has-session -t "$startup_session" 2>/dev/null; then
  echo "startup e2e could not close the Project session" >&2
  exit 1
fi
startup_tmux send-keys -t "$startup_driver_pane" "bash '$startup_root/open-project.sh' '$startup_project' topology" Enter
startup_wait_for "closed Project Continue open" test -s "$startup_root/open-topology.rc"
if [[ "$(tr -d '[:space:]' <"$startup_root/open-topology.rc")" != "0" ]]; then
  echo "closed Project Continue open failed" >&2
  cat "$startup_root/open-topology.err" >&2 || true
  exit 1
fi

startup_window_uids_after="$(startup_tmux list-windows -t "$startup_session" -F '#{@projmux_window_uid}' | sort)"
if [[ "$startup_window_uids_after" != "$startup_window_uids" ]]; then
  echo "closed Project open did not materialize the exact Registry Window uids" >&2
  printf 'want=%s got=%s\n' "$startup_window_uids" "$startup_window_uids_after" >&2
  exit 1
fi
# The live Pane set equals the Registry Pane set as it stands after the open, and
# every shell Pane uid the fixture created is still in it. The Agent's managed
# Pane is the one resource allowed to be new; nothing else is.
startup_registry_pane_uids="$(startup_pmx get panes --project "uid:$startup_project_uid" -o uid | sort)"
startup_pane_uids_after="$(startup_tmux list-panes -s -t "$startup_session" -F '#{@projmux_pane_uid}' | sort)"
if [[ "$startup_pane_uids_after" != "$startup_registry_pane_uids" ]]; then
  echo "closed Project open did not materialize the exact Registry Pane uids" >&2
  printf 'registry=%s\nlive=%s\n' "$startup_registry_pane_uids" "$startup_pane_uids_after" >&2
  exit 1
fi
startup_missing_shell_panes="$(comm -23 <(printf '%s\n' "$startup_shell_pane_uids") <(printf '%s\n' "$startup_pane_uids_after"))"
if [[ -n "$startup_missing_shell_panes" ]]; then
  echo "closed Project open lost shell Pane uids that must never change: $startup_missing_shell_panes" >&2
  exit 1
fi
if [[ "$(startup_tmux show-options -qv -t "$startup_session" @projmux_project_uid)" != "$startup_project_uid" ]]; then
  echo "closed Project open did not restore the exact Project uid binding" >&2
  exit 1
fi
startup_wait_for "client moved to the opened Project session" sh -c \
  "test \"\$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$startup_root/tmux' tmux -L '$startup_socket' display-message -p -c '$startup_client' '#{session_name}' 2>/dev/null)\" = '$startup_session'"

# Every materialized Pane runs the default shell under the managed process
# supervisor, never a stored command. A pane the session brought with it is
# adopted rather than launched, so it carries no start command at all.
startup_start_commands="$(startup_tmux list-panes -s -t "$startup_session" -F '#{pane_start_command}')"
while IFS= read -r startup_start_command; do
  [[ -n "$startup_start_command" ]] || continue
  case "$startup_start_command" in
    *"internal supervise"*) ;;
    *)
      echo "closed Project open launched a pane outside the supervisor: $startup_start_command" >&2
      exit 1
      ;;
  esac
  if [[ "$startup_start_command" == *"${startup_stored_command[*]}"* ]]; then
    echo "closed Project open replayed a stored Pane command: $startup_start_command" >&2
    exit 1
  fi
done <<<"$startup_start_commands"
# The stored Agent is replayed, and because its Registry record carries a
# `status.sessionRef` it is resumed into the conversation that pointer names
# rather than silently starting a new one. This guard used to assert that no
# Agent was running -- against a fixture with no Agent in it.
startup_live_pmx describe agent "uid:$startup_agent_uid" -o json >"$startup_root/agent-after-topology.json"
smoke_assert_file_contains "$startup_root/agent-after-topology.json" '"phase": "Running"'
smoke_assert_file_contains "$startup_root/agent-after-topology.json" 'startup-thread'
startup_wait_for "the replayed Agent to record its resume argv" \
  grep -Fq "resume startup-thread" "$startup_agent_argv"
startup_agent_pane_uid="$(sed -n 's/.*"paneRef": "\([^"]*\)".*/\1/p' "$startup_root/agent-after-topology.json" | head -n 1)"
if [[ -z "$startup_agent_pane_uid" ]] ||
  ! startup_tmux list-panes -s -t "$startup_session" -F '#{@projmux_pane_uid}' | grep -Fqx "$startup_agent_pane_uid"; then
  echo "the replayed Agent's managed Pane never reached tmux" >&2
  startup_tmux list-panes -s -t "$startup_session" -F '#{@projmux_pane_uid}' >&2 || true
  exit 1
fi

# The original anchor handle died with the closed Project session. Rebind the
# producer helper only through the durable primary Pane UID and revalidate its
# exact recreated $/@/% containment on the same physical server generation.
mapfile -t startup_recreated_anchor_matches < <(
  startup_tmux list-panes -s -t "$startup_session" -F '#{pane_id}|#{@projmux_pane_uid}' |
    awk -F '|' -v uid="$startup_anchor_pane_uid" '$2 == uid { print $1 }'
)
if [[ "${#startup_recreated_anchor_matches[@]}" != "1" ]] || \
  [[ ! "${startup_recreated_anchor_matches[0]}" =~ ^%[0-9]+$ ]]; then
  echo "startup replay did not restore one exact durable producer anchor for $startup_anchor_pane_uid" >&2
  printf '%s\n' "${startup_recreated_anchor_matches[@]}" >&2
  exit 1
fi
startup_create_anchor_pane="${startup_recreated_anchor_matches[0]}"
startup_recreated_anchor_receipt="$(
  startup_tmux display-message -p -t "$startup_create_anchor_pane" \
    '#{socket_path}|#{pid}|#{session_id}|#{window_id}|#{pane_id}|#{@projmux_project_uid}|#{@projmux_window_uid}|#{@projmux_pane_uid}|receipt-end'
)"
IFS='|' read -r startup_recreated_socket startup_recreated_pid startup_recreated_session \
  startup_recreated_window startup_recreated_pane startup_recreated_project_uid \
  startup_recreated_window_uid startup_recreated_pane_uid startup_recreated_end \
  <<<"$startup_recreated_anchor_receipt"
if [[ "$startup_recreated_socket" != "$startup_socket_path" ]] || \
  [[ "$startup_recreated_pid" != "$startup_socket_pid" ]] || \
  [[ ! "$startup_recreated_session" =~ ^\$[0-9]+$ ]] || \
  [[ ! "$startup_recreated_window" =~ ^@[0-9]+$ ]] || \
  [[ "$startup_recreated_pane" != "$startup_create_anchor_pane" ]] || \
  [[ "$startup_recreated_project_uid" != "$startup_project_uid" ]] || \
  [[ "$startup_recreated_window_uid" != "$startup_anchor_window_uid" ]] || \
  [[ "$startup_recreated_pane_uid" != "$startup_anchor_pane_uid" ]] || \
  [[ "$startup_recreated_end" != "receipt-end" ]]; then
  echo "startup replay producer anchor containment drifted: $startup_recreated_anchor_receipt" >&2
  exit 1
fi

# Seed a real latest snapshot while the Project session is current. The
# fail-closed `new` attempt below must retain these exact source bytes.
startup_create_pmx create snapshot >"$startup_root/create-latest-snapshot.out"
smoke_assert_file_contains "$startup_root/create-latest-snapshot.out" "saved session snapshot: $startup_session"
startup_latest_snapshot="$startup_root/state/projmux/sessions/$startup_session.json"
if [[ ! -s "$startup_latest_snapshot" ]]; then
  echo "startup e2e did not seed the latest snapshot" >&2
  exit 1
fi
cp "$startup_latest_snapshot" "$startup_root/latest-snapshot.saved.json"

# Mutate desired state after the save. The later projection must replace only
# this Project subtree with the saved desired state and leave the source bytes
# untouched.
startup_create_pmx create window --project "uid:$startup_project_uid" --name after-save >"$startup_root/create-after-save.out"
startup_windows_after_mutate="$(startup_pmx get windows --project "uid:$startup_project_uid" -o uid | grep -c .)"
if [[ "$startup_windows_after_mutate" != "3" ]]; then
  echo "snapshot projection fixture did not add the post-save Window" >&2
  exit 1
fi

# 2. A failed preflight must not move the client. The Project root is taken away,
# so the plan is refused before the first create and the client stays put.
startup_tmux switch-client -c "$startup_client" -t "$startup_driver"
startup_wait_for "client back on the driver session" sh -c \
  "test \"\$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$startup_root/tmux' tmux -L '$startup_socket' display-message -p -c '$startup_client' '#{session_name}' 2>/dev/null)\" = '$startup_driver'"
startup_tmux kill-session -t "$startup_session"
mv "$startup_project" "$startup_project-withdrawn"
startup_tmux send-keys -t "$startup_driver_pane" "bash '$startup_root/open-project.sh' '$startup_project' refused" Enter
startup_wait_for "refused closed Project open" test -s "$startup_root/open-refused.rc"
if [[ "$(tr -d '[:space:]' <"$startup_root/open-refused.rc")" == "0" ]]; then
  echo "a missing Project root did not fail the closed Project open" >&2
  exit 1
fi
smoke_assert_file_contains "$startup_root/open-refused.err" "materialize Registry topology"
if startup_tmux has-session -t "$startup_session" 2>/dev/null; then
  echo "refused closed Project open still created the session" >&2
  exit 1
fi
if [[ "$(startup_tmux display-message -p -c "$startup_client" '#{session_name}')" != "$startup_driver" ]]; then
  echo "refused closed Project open moved the client" >&2
  exit 1
fi
mv "$startup_project-withdrawn" "$startup_project"

# 3. Project the saved snapshot into the exact closed Project. The source
# snapshot stays byte-identical, the post-save Window disappears, ordinary
# materialization runs, and the explicit-client switch is the final handoff even
# though the continuation has no inherited TMUX.
startup_tmux send-keys -t "$startup_driver_pane" "bash '$startup_root/restore-project.sh' '$startup_session' 'uid:$startup_project_uid' '$startup_client'" Enter
startup_wait_for "snapshot Registry projection" test -s "$startup_root/restore-project.rc"
if [[ "$(tr -d '[:space:]' <"$startup_root/restore-project.rc")" != "0" ]]; then
  echo "snapshot Registry projection failed" >&2
  cat "$startup_root/restore-project.err" >&2 || true
  exit 1
fi
cmp "$startup_root/latest-snapshot.saved.json" "$startup_latest_snapshot"
startup_wait_for "projection client handoff" startup_client_is_on "$startup_session"
if [[ "$(startup_pmx get windows --project "uid:$startup_project_uid" -o uid | grep -c .)" != "2" ]]; then
  echo "snapshot projection did not remove the post-save Window" >&2
  exit 1
fi

# 4. Close the just-materialized runtime and use Continue project. This proves
# the committed desired state is consumed only through the ordinary startup
# materializer and remains convergent.
startup_tmux switch-client -c "$startup_client" -t "$startup_driver"
startup_tmux kill-session -t "$startup_session"
rm -f "$startup_root/open-continue.rc"
startup_tmux send-keys -t "$startup_driver_pane" "bash '$startup_root/open-continue.sh' '$startup_project' '$startup_session' '$startup_client'" Enter
startup_wait_for "Continue project after projection" test -s "$startup_root/open-continue.rc"
if [[ "$(tr -d '[:space:]' <"$startup_root/open-continue.rc")" != "0" ]]; then
  cat "$startup_root/open-continue.err" >&2 || true
  exit 1
fi
startup_wait_for "Continue project client handoff" startup_client_is_on "$startup_session"
cmp "$startup_root/latest-snapshot.saved.json" "$startup_latest_snapshot"

# 5. Open fresh from a detached continuation with TMUX unset and explicit
# client authority. Agent/extra descendants are removed atomically while the
# canonical Project/Window/minimum-shell UID chain is retained. A closed repeat
# retains the exact UID chain; the planner/transaction suites separately pin
# its Registry byte-level zero diff. The filesystem root and snapshot source
# bytes remain untouched.
startup_pmx describe project "uid:$startup_project_uid" -o json >"$startup_root/project.before-fresh.json"
startup_primary_window_before="$(sed -n 's/.*"primaryWindowRef": "\([^"]*\)".*/\1/p' "$startup_root/project.before-fresh.json" | head -n 1)"
startup_pmx describe window "uid:$startup_primary_window_before" --project "uid:$startup_project_uid" -o json >"$startup_root/window.before-fresh.json"
startup_primary_pane_before="$(sed -n 's/.*"defaultShellPaneRef": "\([^"]*\)".*/\1/p' "$startup_root/window.before-fresh.json" | head -n 1)"
if [[ -z "$startup_primary_window_before" || -z "$startup_primary_pane_before" ]]; then
  echo "Open fresh fixture has no canonical shell identity" >&2
  exit 1
fi
: >"$startup_agent_argv"
startup_tmux switch-client -c "$startup_client" -t "$startup_driver"
startup_tmux kill-session -t "$startup_session"
startup_tmux send-keys -t "$startup_driver_pane" "bash '$startup_root/open-fresh.sh' '$startup_project' '$startup_session' '$startup_client'" Enter
startup_wait_for "Open fresh continuation" test -s "$startup_root/open-new.rc"
if [[ "$(tr -d '[:space:]' <"$startup_root/open-new.rc")" != "0" ]]; then
  cat "$startup_root/open-new.err" >&2 || true
  exit 1
fi
startup_wait_for "Open fresh explicit client handoff" startup_client_is_on "$startup_session"
startup_project_uid_after="$(startup_pmx get projects -o uid)"
if [[ -z "$startup_project_uid_after" ]] || [[ "$startup_project_uid_after" != "$startup_project_uid" ]] ||
  [[ "$(printf '%s\n' "$startup_project_uid_after" | grep -c .)" != "1" ]]; then
  echo "Open fresh did not preserve exactly one canonical Project identity" >&2
  startup_pmx get projects -o uid >&2 || true
  exit 1
fi
startup_pmx describe project "uid:$startup_project_uid_after" -o json >"$startup_root/project.after-fresh.json"
startup_primary_window_after="$(sed -n 's/.*"primaryWindowRef": "\([^"]*\)".*/\1/p' "$startup_root/project.after-fresh.json" | head -n 1)"
startup_pmx describe window "uid:$startup_primary_window_after" --project "uid:$startup_project_uid_after" -o json >"$startup_root/window.after-fresh.json"
startup_primary_pane_after="$(sed -n 's/.*"defaultShellPaneRef": "\([^"]*\)".*/\1/p' "$startup_root/window.after-fresh.json" | head -n 1)"
if [[ -z "$startup_primary_window_after" ]] || [[ -z "$startup_primary_pane_after" ]] ||
  [[ "$startup_primary_window_after" != "$startup_primary_window_before" ]] ||
  [[ "$startup_primary_pane_after" != "$startup_primary_pane_before" ]]; then
  echo "Open fresh changed the canonical Window/shell identity" >&2
  exit 1
fi
if [[ "$(startup_pmx get windows --project "uid:$startup_project_uid_after" -o uid | grep -c .)" != "1" ]] ||
  [[ "$(startup_pmx get panes --project "uid:$startup_project_uid_after" -o uid | grep -c .)" != "1" ]] ||
  [[ "$(startup_pmx get agents --project "uid:$startup_project_uid_after" -o uid 2>/dev/null | grep -c . || true)" != "0" ]]; then
  echo "Open fresh did not create exactly one canonical shell" >&2
  exit 1
fi
startup_runtime_project_uid="$(startup_tmux show-options -qv -t "$startup_session" @projmux_project_uid)"
startup_runtime_window_uid="$(startup_tmux list-windows -t "$startup_session" -F '#{@projmux_window_uid}')"
startup_runtime_pane_uid="$(startup_tmux list-panes -s -t "$startup_session" -F '#{@projmux_pane_uid}')"
if [[ "$startup_runtime_project_uid" != "$startup_project_uid_after" ]] ||
  [[ "$startup_runtime_window_uid" != "$startup_primary_window_after" ]] ||
  [[ "$startup_runtime_pane_uid" != "$startup_primary_pane_after" ]]; then
  echo "Open fresh runtime identity does not match the new canonical Registry shell: project=$startup_runtime_project_uid/$startup_project_uid_after window=$startup_runtime_window_uid/$startup_primary_window_after pane=$startup_runtime_pane_uid/$startup_primary_pane_after" >&2
  exit 1
fi
cmp "$startup_root/latest-snapshot.saved.json" "$startup_latest_snapshot"
if [[ -s "$startup_agent_argv" ]]; then
  echo "Open fresh launched a removed Agent" >&2
  cat "$startup_agent_argv" >&2 || true
  exit 1
fi

startup_tmux switch-client -c "$startup_client" -t "$startup_driver"
startup_tmux kill-session -t "$startup_session"
rm -f "$startup_root/open-new.rc"
startup_tmux send-keys -t "$startup_driver_pane" "bash '$startup_root/open-fresh.sh' '$startup_project' '$startup_session' '$startup_client'" Enter
startup_wait_for "repeat Open fresh continuation" test -s "$startup_root/open-new.rc"
if [[ "$(tr -d '[:space:]' <"$startup_root/open-new.rc")" != "0" ]]; then
  cat "$startup_root/open-new.err" >&2 || true
  exit 1
fi
startup_wait_for "repeat Open fresh explicit client handoff" startup_client_is_on "$startup_session"
if [[ "$(startup_pmx get projects -o uid)" != "$startup_project_uid" ]] ||
  [[ "$(startup_pmx get windows --project "uid:$startup_project_uid" -o uid)" != "$startup_primary_window_before" ]] ||
  [[ "$(startup_pmx get panes --project "uid:$startup_project_uid" -o uid)" != "$startup_primary_pane_before" ]]; then
  echo "repeat Open fresh changed the canonical UID chain" >&2
  exit 1
fi
cmp "$startup_root/latest-snapshot.saved.json" "$startup_latest_snapshot"

startup_other_after="$(startup_other_tmux show-options -gqv @projmux_startup_sentinel):$(startup_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"
if [[ "$startup_other_after" != "$startup_other_before" ]]; then
  echo "closed Project startup touched the sibling socket" >&2
  exit 1
fi

exec 8>&-
kill "$startup_client_pid" >/dev/null 2>&1 || true
wait "$startup_client_pid" 2>/dev/null || true
startup_cleanup
trap smoke_cleanup_env EXIT
echo ">> Closed Project managed startup e2e passed: socket=$startup_socket path=$startup_socket_path other-socket=$startup_other_socket other-path=$startup_other_socket_path client=$startup_client cleanup=validated-exact-sockets"

# ---------------------------------------------------------------------------
# First-open Project identity mirror.
#
# Opening an unregistered directory mints a Project and then starts its session
# through the shipped `EnsureSession` path, which writes only the
# `@projmux_project_path` anchor. The session therefore carried no
# `@projmux_project_uid` / `@projmux_project_name`, so the next `create` in it
# read a blank identity and refused its own session as foreign. This block drives
# the installed `switch open` route from inside a real attached client and proves
# the open now finishes the identity mirror in the same flow, that `create` in
# that session succeeds, that repeating the open changes nothing, and that
# opening `$HOME` still mints no managed identity.
#
# Isolation follows the four mandatory conditions: inherited TMUX/TMUX_PANE are
# stripped from every no--L call, the server is a run-unique -L socket name under
# a dedicated TMUX_TMPDIR, the real #{socket_path} is queried and proven to sit
# inside the smoke root, and only that exact queried socket is killed.
# ---------------------------------------------------------------------------
fopen_root="$PROJMUX_SMOKE_WORKDIR/first-open-mirror"
fopen_socket="projmux-firstopen-$RANDOM-$$"
fopen_other_socket="projmux-firstopen-other-$RANDOM-$$"
fopen_driver="firstopen-driver"
# The namer derives the session name from <parent>-<base> of the Project root, so
# the session a first open mints is known before the open runs.
fopen_session="work-gamma"
mkdir -p \
  "$fopen_root/home" \
  "$fopen_root/config" \
  "$fopen_root/state" \
  "$fopen_root/runtime" \
  "$fopen_root/tmux" \
  "$fopen_root/work/gamma" \
  "$fopen_root/work/delta" \
  "$fopen_root/shim" \
  "$fopen_root/bin"
chmod 0700 "$fopen_root/runtime" "$fopen_root/tmux"
fopen_project="$fopen_root/work/gamma"

fopen_shell="$fopen_root/shim/persistent-shell"
cat >"$fopen_shell" <<'FOPEN_SHELL_STUB'
#!/usr/bin/env bash
exec sleep 600
FOPEN_SHELL_STUB
chmod 0755 "$fopen_shell"

fopen_tmux() {
  env -u TMUX -u TMUX_PANE \
    HOME="$fopen_root/home" \
    XDG_CONFIG_HOME="$fopen_root/config" \
    XDG_STATE_HOME="$fopen_root/state" \
    XDG_RUNTIME_DIR="$fopen_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$fopen_root/work" \
    TMUX_TMPDIR="$fopen_root/tmux" \
    SHELL="$fopen_shell" \
    tmux -L "$fopen_socket" "$@"
}

fopen_other_tmux() {
  env -u TMUX -u TMUX_PANE \
    HOME="$fopen_root/home" \
    XDG_CONFIG_HOME="$fopen_root/config" \
    XDG_STATE_HOME="$fopen_root/state" \
    XDG_RUNTIME_DIR="$fopen_root/runtime" \
    TMUX_TMPDIR="$fopen_root/tmux" \
    SHELL="$fopen_shell" \
    tmux -L "$fopen_other_socket" "$@"
}

fopen_pmx() {
  env -u TMUX -u TMUX_PANE \
    HOME="$fopen_root/home" \
    XDG_CONFIG_HOME="$fopen_root/config" \
    XDG_STATE_HOME="$fopen_root/state" \
    XDG_RUNTIME_DIR="$fopen_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$fopen_root/work" \
    TMUX_TMPDIR="$fopen_root/tmux" \
    SHELL="$fopen_shell" \
    "$bin" "$@"
}

# A first open shells out to a bare `tmux` on purpose -- the session lands on the
# app-owned transport the operator is actually in, and so must its identity
# mirror. The paired markers below preserve the logical name needed by
# PROJMUX_SOCKET hook re-entry; the shim maps the default `-L projmux` metadata
# route onto this smoke socket while every other explicit -L/-S call passes
# through untouched.
fopen_real_tmux="$(command -v tmux)"
cat >"$fopen_root/bin/tmux" <<FOPEN_TMUX_SHIM
#!/usr/bin/env bash
if [[ "\${1:-}" == "-L" && "\${2:-}" == "projmux" ]]; then
  shift 2
  exec $(printf %q "$fopen_real_tmux") -L $(printf %q "$fopen_socket") "\$@"
fi
if [[ "\${1:-}" == "-L" || "\${1:-}" == "-S" ]]; then
  exec $(printf %q "$fopen_real_tmux") "\$@"
fi
exec $(printf %q "$fopen_real_tmux") -L $(printf %q "$fopen_socket") "\$@"
FOPEN_TMUX_SHIM
chmod 0755 "$fopen_root/bin/tmux"

# The open runs from inside the attached client's pane so `$TMUX`/`$TMUX_PANE` are
# the real inherited client state rather than a simulation.
cat >"$fopen_root/open-project.sh" <<FOPEN_OPEN_SCRIPT
#!/usr/bin/env bash
export HOME="$fopen_root/home"
export XDG_CONFIG_HOME="$fopen_root/config"
export XDG_STATE_HOME="$fopen_root/state"
export XDG_RUNTIME_DIR="$fopen_root/runtime"
export PROJMUX_MANAGED_ROOTS="$fopen_root/work"
export TMUX_TMPDIR="$fopen_root/tmux"
export SHELL="$fopen_shell"
export PATH="$fopen_root/bin:\$PATH"
$(printf %q "$bin") switch open "\$1" >"$fopen_root/open-\$2.out" 2>"$fopen_root/open-\$2.err"
echo \$? >"$fopen_root/open-\$2.rc"
FOPEN_OPEN_SCRIPT
chmod 0755 "$fopen_root/open-project.sh"

fopen_tmux new-session -d -s "$fopen_driver" -c "$fopen_root" bash --noprofile --norc
fopen_tmux set-option -gq @projmux_app 1
fopen_tmux set-option -gq @projmux_socket_name "$fopen_socket"
if [[ "$(fopen_tmux show-options -gqv @projmux_app)" != "1" ]] || \
  [[ "$(fopen_tmux show-options -gqv @projmux_socket_name)" != "$fopen_socket" ]]; then
  echo "first-open fixture lacks a complete app-owned logical route" >&2
  exit 1
fi
fopen_other_tmux new-session -d -s untouched -c "$fopen_root" sleep 600
fopen_other_tmux set-option -gq @projmux_firstopen_sentinel unchanged
fopen_other_before="$(fopen_other_tmux show-options -gqv @projmux_firstopen_sentinel):$(fopen_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"

fopen_socket_path="$(fopen_tmux display-message -p -t "$fopen_driver" '#{socket_path}')"
fopen_socket_pid="$(fopen_tmux display-message -p -t "$fopen_driver" '#{pid}')"
fopen_other_socket_path="$(fopen_other_tmux display-message -p -t untouched '#{socket_path}')"
for actual in "$fopen_socket_path" "$fopen_other_socket_path"; do
  case "$actual" in
    "$fopen_root"/*) ;;
    *)
      echo "first-open e2e socket escaped smoke root: $actual" >&2
      exit 1
      ;;
  esac
done

fopen_cleanup() {
  local socket actual
  for socket in "$fopen_socket" "$fopen_other_socket"; do
    actual="$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$fopen_root/tmux" tmux -L "$socket" display-message -p '#{socket_path}' 2>/dev/null || true)"
    if [[ -z "$actual" ]]; then
      continue
    fi
    case "$actual" in
      "$fopen_root"/*)
        echo ">> first-open e2e cleanup target=$actual"
        env -u TMUX -u TMUX_PANE tmux -S "$actual" kill-server >/dev/null 2>&1 || true
        ;;
      *) echo "refusing first-open cleanup outside smoke root: $actual" >&2 ;;
    esac
  done
}
trap 'fopen_cleanup; smoke_cleanup_env' EXIT

fopen_registry="$fopen_root/state/projmux/metadata/registry.json"
if [[ -e "$fopen_registry" ]]; then
  echo "first-open e2e did not start from an unregistered world" >&2
  exit 1
fi

# Attach a real client to the driver session so the client move is a real
# switch-client on this exact server.
fopen_client_log="$fopen_root/driver-client.log"
fopen_client_input="$fopen_root/driver-client.in"
mkfifo "$fopen_client_input"
exec 9<>"$fopen_client_input"
TERM=xterm-256color script -qefc \
  "TERM=xterm-256color env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$fopen_root/tmux' tmux -L '$fopen_socket' attach-session -t '$fopen_driver'" \
  "$fopen_client_log" <"$fopen_client_input" >/dev/null 2>&1 &
fopen_client_pid=$!

fopen_wait_for() {
  local description="$1"
  shift
  for _ in {1..200}; do
    if "$@"; then
      return 0
    fi
    sleep 0.05
  done
  echo "timed out waiting for $description" >&2
  tail -c 8000 "$fopen_client_log" >&2 || true
  return 1
}

fopen_wait_for "attached first-open tmux client" sh -c \
  "test -n \"\$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$fopen_root/tmux' tmux -L '$fopen_socket' list-clients -F '#{client_name}' 2>/dev/null | head -n 1)\""
fopen_client="$(fopen_tmux list-clients -F '#{client_name}' | head -n 1)"
fopen_driver_pane="$(fopen_tmux display-message -p -c "$fopen_client" '#{pane_id}')"
fopen_driver_receipt="$(
  fopen_tmux display-message -p -t "$fopen_driver_pane" \
    '#{socket_path}|#{pid}|#{session_id}|#{session_name}|#{window_id}|#{pane_id}|#{@projmux_app}|#{@projmux_socket_name}|receipt-end'
)"
IFS='|' read -r fopen_driver_socket fopen_driver_pid fopen_driver_session_id fopen_driver_session_name \
  fopen_driver_window fopen_driver_observed_pane fopen_driver_app_marker fopen_driver_logical_marker \
  fopen_driver_receipt_end <<<"$fopen_driver_receipt"
if [[ "$fopen_driver_socket" != "$fopen_socket_path" ]] || \
  [[ "$fopen_driver_pid" != "$fopen_socket_pid" ]] || \
  [[ ! "$fopen_driver_session_id" =~ ^\$[0-9]+$ ]] || \
  [[ "$fopen_driver_session_name" != "$fopen_driver" ]] || \
  [[ ! "$fopen_driver_window" =~ ^@[0-9]+$ ]] || \
  [[ "$fopen_driver_observed_pane" != "$fopen_driver_pane" ]] || \
  [[ "$fopen_driver_app_marker" != "1" ]] || \
  [[ "$fopen_driver_logical_marker" != "$fopen_socket" ]] || \
  [[ "$fopen_driver_receipt_end" != "receipt-end" ]]; then
  echo "first-open fixture lacks exact app-owned client authority: $fopen_driver_receipt" >&2
  exit 1
fi

# 1. The first open of an unregistered directory.
fopen_tmux send-keys -t "$fopen_driver_pane" "bash '$fopen_root/open-project.sh' '$fopen_project' first" Enter
fopen_wait_for "first open of an unregistered directory" test -s "$fopen_root/open-first.rc"
if [[ "$(tr -d '[:space:]' <"$fopen_root/open-first.rc")" != "0" ]]; then
  echo "the first open of an unregistered directory failed" >&2
  cat "$fopen_root/open-first.err" >&2 || true
  exit 1
fi
if [[ ! -f "$fopen_registry" ]]; then
  echo "the first open minted no Project" >&2
  exit 1
fi
fopen_project_uid="$(fopen_pmx get projects -o uid)"
if [[ "$(printf '%s\n' "$fopen_project_uid" | wc -l)" != "1" ]] || [[ -z "$fopen_project_uid" ]]; then
  echo "the first open registered something other than exactly the opened path: $fopen_project_uid" >&2
  exit 1
fi
# The sibling candidate under the same discovery root stays unregistered.
if fopen_pmx get projects -o json | grep -Fq "$fopen_root/work/delta"; then
  echo "the first open also registered the sibling candidate delta" >&2
  exit 1
fi

# 2. The session exists and carries the whole identity mirror, not just the
# project-path anchor `EnsureSession` writes for itself.
if ! fopen_tmux has-session -t "$fopen_session" 2>/dev/null; then
  echo "the first open minted no session named $fopen_session" >&2
  fopen_tmux list-sessions -F '#{session_name}' >&2 || true
  exit 1
fi
fopen_mirrored_uid="$(fopen_tmux show-options -qv -t "$fopen_session" @projmux_project_uid)"
fopen_mirrored_name="$(fopen_tmux show-options -qv -t "$fopen_session" @projmux_project_name)"
fopen_mirrored_path="$(fopen_tmux show-options -qv -t "$fopen_session" @projmux_project_path)"
if [[ "$fopen_mirrored_uid" != "$fopen_project_uid" ]]; then
  echo "first open session project uid = '$fopen_mirrored_uid', want '$fopen_project_uid'" >&2
  exit 1
fi
if [[ "$fopen_mirrored_name" != "gamma" ]]; then
  echo "first open session project name = '$fopen_mirrored_name', want 'gamma'" >&2
  exit 1
fi
if [[ "$fopen_mirrored_path" != "$fopen_project" ]]; then
  echo "first open session project path = '$fopen_mirrored_path', want '$fopen_project'" >&2
  exit 1
fi
fopen_wait_for "client moved to the freshly opened session" sh -c \
  "test \"\$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$fopen_root/tmux' tmux -L '$fopen_socket' display-message -p -c '$fopen_client' '#{session_name}' 2>/dev/null)\" = '$fopen_session'"

# 3. `create` in that session owns it now. This is the exact route that used to
# refuse: `preflightSessionOwnership` reads the session identity the open wrote.
fopen_pane="$(fopen_tmux display-message -p -t "$fopen_session" '#{pane_id}')"
fopen_live_pmx() {
  env \
    HOME="$fopen_root/home" \
    XDG_CONFIG_HOME="$fopen_root/config" \
    XDG_STATE_HOME="$fopen_root/state" \
    XDG_RUNTIME_DIR="$fopen_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$fopen_root/work" \
    TMUX_TMPDIR="$fopen_root/tmux" \
    PATH="$fopen_root/bin:$PATH" \
    TMUX="$fopen_socket_path,$fopen_socket_pid,0" \
    TMUX_PANE="$fopen_pane" \
    SHELL="$fopen_shell" \
    "$bin" "$@"
}
fopen_live_pmx create window --project "uid:$fopen_project_uid" --name smoke >"$fopen_root/create-window.out"
smoke_assert_file_contains "$fopen_root/create-window.out" "window/smoke created"
fopen_live_pmx create pane --project "uid:$fopen_project_uid" --window smoke --placement right -o pane-id >"$fopen_root/create-pane.out"
fopen_created_pane="$(tr -d '[:space:]' <"$fopen_root/create-pane.out")"
if [[ -z "$fopen_created_pane" ]]; then
  echo "create pane in the freshly opened session returned no pane id" >&2
  exit 1
fi
# The pane is managed: the Registry lists it and the live pane mirrors its uid.
fopen_pane_uids="$(fopen_pmx get panes --project "uid:$fopen_project_uid" -o uid)"
if [[ -z "$fopen_pane_uids" ]]; then
  echo "get panes listed nothing for the freshly opened Project" >&2
  exit 1
fi
fopen_live_pane_uid="$(fopen_tmux display-message -p -t "$fopen_created_pane" '#{@projmux_pane_uid}')"
if ! printf '%s\n' "$fopen_pane_uids" | grep -Fqx "$fopen_live_pane_uid"; then
  echo "the created pane uid '$fopen_live_pane_uid' is not in get panes: $fopen_pane_uids" >&2
  exit 1
fi

# 4. Repeating the open converges. Every session option and the Registry itself
# stay byte-identical, which is the observable form of "no second mirror write".
fopen_options_before="$(fopen_tmux show-options -t "$fopen_session" | sort)"
cp "$fopen_registry" "$fopen_root/registry.before-repeat"
fopen_tmux send-keys -t "$fopen_driver_pane" "bash '$fopen_root/open-project.sh' '$fopen_project' repeat" Enter
fopen_wait_for "repeat open of the now-registered Project" test -s "$fopen_root/open-repeat.rc"
if [[ "$(tr -d '[:space:]' <"$fopen_root/open-repeat.rc")" != "0" ]]; then
  echo "the repeat open failed" >&2
  cat "$fopen_root/open-repeat.err" >&2 || true
  exit 1
fi
if [[ "$(fopen_tmux show-options -t "$fopen_session" | sort)" != "$fopen_options_before" ]]; then
  echo "the repeat open rewrote a session option" >&2
  diff <(printf '%s\n' "$fopen_options_before") <(fopen_tmux show-options -t "$fopen_session" | sort) >&2 || true
  exit 1
fi
if ! cmp -s "$fopen_registry" "$fopen_root/registry.before-repeat"; then
  echo "the repeat open rewrote the Registry" >&2
  exit 1
fi

# 5. Home is chrome, not a path-declared ControlSession. This fixture has no
# exact ControlSession declaration, so `switch open` must refuse rather than
# fall back to a UID-less raw session. The dedicated `projmux shell` surface
# owns ControlSession bootstrap; this Project-path surface stays write-free.
fopen_projects_before="$(fopen_pmx get projects -o uid | sort)"
cp "$fopen_registry" "$fopen_root/registry.before-home"
fopen_runtime_before="$(
  fopen_tmux list-sessions -F '#{session_id}|#{session_name}|#{@projmux_project_uid}|#{@projmux_session_role}'
  fopen_tmux list-windows -a -F '#{session_id}|#{window_id}|#{@projmux_window_uid}'
  fopen_tmux list-panes -a -F '#{session_id}|#{window_id}|#{pane_id}|#{@projmux_pane_uid}'
)"
fopen_tmux send-keys -t "$fopen_driver_pane" "bash '$fopen_root/open-project.sh' '$fopen_root/home' home" Enter
fopen_wait_for "open of Home" test -s "$fopen_root/open-home.rc"
if [[ "$(tr -d '[:space:]' <"$fopen_root/open-home.rc")" == "0" ]]; then
  echo "opening undeclared Home unexpectedly succeeded" >&2
  exit 1
fi
if ! grep -Fq "exact Registry Project UID is unavailable; no runtime was created" "$fopen_root/open-home.err"; then
  echo "opening undeclared Home lacked the exact fail-closed diagnostic" >&2
  cat "$fopen_root/open-home.err" >&2 || true
  exit 1
fi
cmp "$fopen_root/registry.before-home" "$fopen_registry"
if [[ "$(fopen_pmx get projects -o uid | sort)" != "$fopen_projects_before" ]]; then
  echo "opening Home minted a Project" >&2
  exit 1
fi
fopen_runtime_after="$(
  fopen_tmux list-sessions -F '#{session_id}|#{session_name}|#{@projmux_project_uid}|#{@projmux_session_role}'
  fopen_tmux list-windows -a -F '#{session_id}|#{window_id}|#{@projmux_window_uid}'
  fopen_tmux list-panes -a -F '#{session_id}|#{window_id}|#{pane_id}|#{@projmux_pane_uid}'
)"
if [[ "$fopen_runtime_after" != "$fopen_runtime_before" ]]; then
  echo "opening undeclared Home changed runtime inventory" >&2
  diff <(printf '%s\n' "$fopen_runtime_before") <(printf '%s\n' "$fopen_runtime_after") >&2 || true
  exit 1
fi
fopen_managed_sessions="$(fopen_tmux list-sessions -F '#{session_name} #{@projmux_project_uid}' | awk 'NF == 2 && $2 != "" {print $1}')"
if [[ "$fopen_managed_sessions" != "$fopen_session" ]]; then
  echo "sessions carrying a managed Project uid = '$fopen_managed_sessions', want only '$fopen_session'" >&2
  fopen_tmux list-sessions -F '#{session_name} #{@projmux_project_uid} #{@projmux_project_name}' >&2 || true
  exit 1
fi

# 6. The sibling socket never heard about any of it.
fopen_other_after="$(fopen_other_tmux show-options -gqv @projmux_firstopen_sentinel):$(fopen_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"
if [[ "$fopen_other_after" != "$fopen_other_before" ]]; then
  echo "the first-open flow touched the sibling socket: $fopen_other_after" >&2
  exit 1
fi

exec 9>&-
kill "$fopen_client_pid" >/dev/null 2>&1 || true
wait "$fopen_client_pid" 2>/dev/null || true
fopen_cleanup
trap smoke_cleanup_env EXIT
echo ">> First-open Project identity mirror e2e passed: socket=$fopen_socket path=$fopen_socket_path other-socket=$fopen_other_socket other-path=$fopen_other_socket_path client=$fopen_client project=$fopen_project_uid session=$fopen_session cleanup=validated-exact-sockets"

# ---------------------------------------------------------------------------
# Runtime diagnostics escape hatch.
#
# The Registry-first surfaces list managed resources, so an operator's own
# window, the Home control session, and a scratch session are correctly absent
# from them -- and "correctly absent" is indistinguishable from "lost" without a
# surface that shows the server as it is. This block proves that surface against
# a real tmux server and a real attached client: every live object is named with
# its attribution and its exact handle, the picker shows the same rows through a
# real popup, and neither the read nor the UI writes a byte anywhere.
#
# Isolation follows the four mandatory conditions: inherited TMUX/TMUX_PANE are
# stripped from every call, the server lives under a run-unique TMUX_TMPDIR with
# its own -L name, the real #{socket_path} is queried and proven to sit inside
# the smoke root, and only that exact socket is killed.
# ---------------------------------------------------------------------------
rtd_root="$PROJMUX_SMOKE_WORKDIR/runtime-diagnostics-e2e"
rtd_socket="projmux-runtime-diag-$$-$RANDOM"
rtd_other_socket="projmux-runtime-diag-other-$$-$RANDOM"
rtd_session="work-alpha"
rtd_driver="runtime-driver"
mkdir -p "$rtd_root/tmux" "$rtd_root/state" "$rtd_root/config" "$rtd_root/home" \
  "$rtd_root/runtime" "$rtd_root/work/alpha"
chmod 0700 "$rtd_root/runtime"
rtd_real_tmux="$(command -v tmux)"

rtd_tmux() { env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$rtd_root/tmux" "$rtd_real_tmux" -L "$rtd_socket" "$@"; }
rtd_other_tmux() { env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$rtd_root/tmux" "$rtd_real_tmux" -L "$rtd_other_socket" "$@"; }
rtd_pmx() {
  env -u TMUX -u TMUX_PANE \
    HOME="$rtd_root/home" \
    XDG_CONFIG_HOME="$rtd_root/config" \
    XDG_STATE_HOME="$rtd_root/state" \
    XDG_RUNTIME_DIR="$rtd_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$rtd_root/work" \
    TMUX_TMPDIR="$rtd_root/tmux" \
    SHELL=/bin/sh \
    "$bin" "$@"
}

rtd_tmux new-session -d -s "$rtd_session" -c "$rtd_root/work/alpha" sleep 600
rtd_tmux set-option -t "$rtd_session" -q @projmux_project_path "$rtd_root/work/alpha"
rtd_tmux new-session -d -s "$rtd_driver" -c "$rtd_root" bash --noprofile --norc
rtd_other_tmux new-session -d -s untouched -c "$rtd_root" sleep 600
rtd_other_tmux set-option -gq @projmux_runtime_sentinel unchanged

rtd_socket_path="$(rtd_tmux display-message -p -t "$rtd_session" '#{socket_path}')"
rtd_socket_pid="$(rtd_tmux display-message -p -t "$rtd_session" '#{pid}')"
rtd_other_socket_path="$(rtd_other_tmux display-message -p -t untouched '#{socket_path}')"
for actual in "$rtd_socket_path" "$rtd_other_socket_path"; do
  case "$actual" in
    "$rtd_root"/*) ;;
    *)
      echo "runtime diagnostics e2e socket escaped smoke root: $actual" >&2
      exit 1
      ;;
  esac
done

rtd_cleanup() {
  local socket actual
  for socket in "$rtd_socket" "$rtd_other_socket"; do
    actual="$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$rtd_root/tmux" tmux -L "$socket" display-message -p '#{socket_path}' 2>/dev/null || true)"
    if [[ -z "$actual" ]]; then
      continue
    fi
    case "$actual" in
      "$rtd_root"/*) env -u TMUX -u TMUX_PANE tmux -S "$actual" kill-server >/dev/null 2>&1 || true ;;
      *) echo "refusing runtime diagnostics cleanup outside smoke root: $actual" >&2 ;;
    esac
  done
}
trap 'rtd_cleanup; smoke_cleanup_env' EXIT

# This is a server projmux started, so unmarked objects on it are projmux's own
# world rather than the operator's.
rtd_tmux set-option -g -q @projmux_app 1
rtd_tmux set-option -g -q @projmux_socket_name "$rtd_socket"
if [[ "$(rtd_tmux show-options -gqv @projmux_app)" != "1" ]] || \
  [[ "$(rtd_tmux show-options -gqv @projmux_socket_name)" != "$rtd_socket" ]]; then
  echo "runtime diagnostics fixture lacks a complete app-owned route marker pair" >&2
  exit 1
fi
# tmux renames a window to its foreground command on its own. That is tmux
# writing, not projmux, and leaving it on would make the zero-write comparison
# below fail the moment a client attaches and a shell starts.
rtd_tmux set-option -g -q automatic-rename off
rtd_pmx reconcile resources --socket "$rtd_socket" --dry-run -o json >"$rtd_root/d2.json"
smoke_assert_file_contains "$rtd_root/d2.json" '"outcome": "no-op"'
if [[ -e "$rtd_root/state/projmux/metadata/registry.json" ]]; then
  echo "runtime diagnostics D2 dry-run created a Registry" >&2
  exit 1
fi
rtd_pmx create project --root "$rtd_root/work/alpha" --name alpha >"$rtd_root/register-alpha.out"
e2e_bounded_reconcile_to_noop "$rtd_root/import" \
  rtd_pmx reconcile resources --socket "$rtd_socket" -o json
rtd_project_uid="$(rtd_tmux show-options -t "$rtd_session" -qv @projmux_project_uid)"
if [[ -z "$rtd_project_uid" ]]; then
  echo "runtime diagnostics e2e explicit authority left the Project uid empty" >&2
  exit 1
fi

# Everything the managed UI cannot show, on one server: the control session, a
# scratch session, an unmarked window, and a window mirroring a uid no Registry
# contains.
rtd_tmux new-session -d -s runtime-home -c "$rtd_root" sleep 600
rtd_tmux set-option -t runtime-home -q @projmux_session_role control
rtd_tmux new-session -d -s runtime-scratch -c "$rtd_root" sleep 600
rtd_tmux set-option -t runtime-scratch -q @projmux_ephemeral 1
rtd_tmux new-window -d -t "=$rtd_session" -n plain -c "$rtd_root" sleep 600
rtd_tmux new-window -d -t "=$rtd_session" -n ghost -c "$rtd_root" sleep 600
rtd_ghost_window="$(rtd_tmux list-windows -t "=$rtd_session" -F '#{window_id} #{window_name}' | awk '$2 == "ghost" {print $1}')"
rtd_tmux set-option -w -t "$rtd_ghost_window" -q @projmux_window_uid win-not-in-this-registry

rtd_snapshot() {
  rtd_tmux list-sessions -F '#{session_id} #{session_name} #{@projmux_project_uid} #{@projmux_session_role} #{@projmux_ephemeral}'
  rtd_tmux list-windows -a -F '#{window_id} #{session_id} #{window_name} #{@projmux_window_uid}'
  rtd_tmux list-panes -a -F '#{pane_id} #{window_id} #{@projmux_pane_uid}'
}
rtd_registry="$rtd_root/state/projmux/metadata/registry.json"
rtd_snapshot >"$rtd_root/server.before"
rtd_other_before="$(rtd_other_tmux show-options -gqv @projmux_runtime_sentinel):$(rtd_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"
cp "$rtd_registry" "$rtd_root/registry.before"

# The read, taken through the inherited client socket exactly as an in-tmux
# invocation would. Every live object of every kind has to be in it.
rtd_live_pmx() {
  env -u TMUX_PANE \
    HOME="$rtd_root/home" \
    XDG_CONFIG_HOME="$rtd_root/config" \
    XDG_STATE_HOME="$rtd_root/state" \
    XDG_RUNTIME_DIR="$rtd_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$rtd_root/work" \
    TMUX_TMPDIR="$rtd_root/tmux" \
    TMUX="$rtd_socket_path,$rtd_socket_pid,0" \
    SHELL=/bin/sh \
    "$bin" "$@"
}
for rtd_kind in sessions windows panes; do
  rtd_live_pmx get runtime "$rtd_kind" -o json >"$rtd_root/runtime-$rtd_kind.json"
  smoke_assert_file_contains "$rtd_root/runtime-$rtd_kind.json" '"hostMode": "app-owned"'
  smoke_assert_file_contains "$rtd_root/runtime-$rtd_kind.json" '"source": "inherited-tmux-env"'
  smoke_assert_file_contains "$rtd_root/runtime-$rtd_kind.json" "\"value\": \"$rtd_socket_path\""
done
for rtd_id in $(rtd_tmux list-sessions -F '#{session_id}'); do
  smoke_assert_file_contains "$rtd_root/runtime-sessions.json" "\"id\": \"$rtd_id\""
done
for rtd_id in $(rtd_tmux list-windows -a -F '#{window_id}'); do
  smoke_assert_file_contains "$rtd_root/runtime-windows.json" "\"id\": \"$rtd_id\""
done
for rtd_id in $(rtd_tmux list-panes -a -F '#{pane_id}'); do
  smoke_assert_file_contains "$rtd_root/runtime-panes.json" "\"id\": \"$rtd_id\""
done
smoke_assert_file_contains "$rtd_root/runtime-sessions.json" '"class": "managed"'
smoke_assert_file_contains "$rtd_root/runtime-sessions.json" '"class": "control"'
smoke_assert_file_contains "$rtd_root/runtime-sessions.json" '"class": "ephemeral"'
smoke_assert_file_contains "$rtd_root/runtime-windows.json" '"class": "unattributed"'
smoke_assert_file_contains "$rtd_root/runtime-windows.json" '"class": "recoverable"'
smoke_assert_file_contains "$rtd_root/runtime-windows.json" '"uid": "win-not-in-this-registry"'

# The picker through a real attached client and a real display-popup. The rows
# an operator sees are the rows the read reported.
rtd_client_log="$rtd_root/driver-client.log"
rtd_client_input="$rtd_root/driver-client.in"
mkfifo "$rtd_client_input"
exec 7<>"$rtd_client_input"
TERM=xterm-256color script -qefc \
  "TERM=xterm-256color env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$rtd_root/tmux' tmux -L '$rtd_socket' attach-session -t '$rtd_driver'" \
  "$rtd_client_log" <"$rtd_client_input" >/dev/null 2>&1 &
rtd_client_pid=$!

rtd_wait_for() {
  local description="$1"
  shift
  for _ in {1..200}; do
    if "$@"; then
      return 0
    fi
    sleep 0.05
  done
  echo "timed out waiting for $description" >&2
  tail -c 12000 "$rtd_client_log" >&2 || true
  return 1
}

rtd_wait_for "attached runtime diagnostics client" sh -c \
  "test -n \"\$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$rtd_root/tmux' tmux -L '$rtd_socket' list-clients -F '#{client_name}' 2>/dev/null | head -n 1)\""
rtd_client="$(rtd_tmux list-clients -F '#{client_name}' | head -n 1)"

rtd_popup_offset="$(stat -c %s "$rtd_client_log")"
rtd_tmux display-popup -c "$rtd_client" -T "Runtime E2E" -w 72 -h 24 -E \
  "env HOME='$rtd_root/home' XDG_CONFIG_HOME='$rtd_root/config' XDG_STATE_HOME='$rtd_root/state' XDG_RUNTIME_DIR='$rtd_root/runtime' TMUX_TMPDIR='$rtd_root/tmux' '$bin' runtime diagnostics --socket '$rtd_socket'" &
rtd_popup_pid=$!

rtd_screen_has() {
  tail -c +$((rtd_popup_offset + 1)) "$rtd_client_log" | grep -aFq "$1"
}
rtd_wait_for "Runtime diagnostics picker" rtd_screen_has "Runtime diagnostics"
rtd_wait_for "runtime diagnostics host header" rtd_screen_has "host app-owned"
rtd_wait_for "runtime diagnostics footer" rtd_screen_has "Enter: actions"
# Every attribution class the managed UI cannot show is on the first screen,
# beside the managed rows a drift-only report would have omitted.
for rtd_class in managed control ephemeral unattributed recoverable; do
  rtd_wait_for "runtime diagnostics class $rtd_class" rtd_screen_has "$rtd_class"
done

# The popup is shorter than the list, so walking to the last row is what proves
# the model holds every observed object rather than the visible prefix. The
# three leading rows are the host header, the attribution tally, and the column
# header.
rtd_object_total=$(( $(rtd_tmux list-sessions -F '#{session_id}' | wc -l) \
  + $(rtd_tmux list-windows -a -F '#{window_id}' | wc -l) \
  + $(rtd_tmux list-panes -a -F '#{pane_id}' | wc -l) ))
for _ in $(seq 1 $((rtd_object_total + 2))); do
  printf '\016' >&7
done
for rtd_id in $(rtd_tmux list-sessions -F '#{session_id}') $(rtd_tmux list-windows -a -F '#{window_id}') $(rtd_tmux list-panes -a -F '#{pane_id}'); do
  rtd_wait_for "runtime diagnostics row $rtd_id" rtd_screen_has "$rtd_id"
done

# The action menu of the last row -- a Pane -- offers exactly the existing safe
# routes, and states why the ones that do not apply do not.
rtd_menu_offset="$(stat -c %s "$rtd_client_log")"
printf '\r' >&7
rtd_menu_has() {
  tail -c +$((rtd_menu_offset + 1)) "$rtd_client_log" | grep -aFq "$1"
}
rtd_wait_for "runtime diagnostics action menu" rtd_menu_has "Runtime object"
rtd_wait_for "runtime diagnostics focus action" rtd_menu_has "Focus"
rtd_wait_for "runtime diagnostics inspect action" rtd_menu_has "Open Resource Inspector"
# The popup truncates the refusal at its width, so the needle is the part that
# always fits.
rtd_wait_for "runtime diagnostics attach refusal" rtd_menu_has "unavailable - only a session"
if tail -c +$((rtd_menu_offset + 1)) "$rtd_client_log" | grep -aEq 'Kill|Delete|Adopt|Import|Rename'; then
  echo "runtime diagnostics action menu offered a destructive or adopting action" >&2
  exit 1
fi

printf '\003' >&7
printf '\003' >&7
rtd_wait_for "runtime diagnostics popup exit" sh -c "! kill -0 '$rtd_popup_pid' 2>/dev/null"
wait "$rtd_popup_pid" || true

# Zero writes: the whole surface -- the reads and the picker session above --
# left the Registry, the exact server, and the sibling socket untouched.
cmp "$rtd_root/registry.before" "$rtd_registry"
rtd_snapshot >"$rtd_root/server.after"
if ! diff -u "$rtd_root/server.before" "$rtd_root/server.after" >"$rtd_root/server.diff"; then
  echo "runtime diagnostics mutated the exact server" >&2
  cat "$rtd_root/server.diff" >&2
  exit 1
fi
rtd_other_after="$(rtd_other_tmux show-options -gqv @projmux_runtime_sentinel):$(rtd_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"
if [[ "$rtd_other_after" != "$rtd_other_before" ]]; then
  echo "runtime diagnostics touched the sibling socket" >&2
  exit 1
fi
if [[ "$(rtd_tmux display-message -p -c "$rtd_client" '#{session_name}')" != "$rtd_driver" ]]; then
  echo "runtime diagnostics moved the attached client" >&2
  exit 1
fi

exec 7>&-
kill "$rtd_client_pid" >/dev/null 2>&1 || true
wait "$rtd_client_pid" 2>/dev/null || true
rtd_cleanup
trap smoke_cleanup_env EXIT
echo ">> Runtime diagnostics e2e passed: socket=$rtd_socket path=$rtd_socket_path other-socket=$rtd_other_socket other-path=$rtd_other_socket_path client=$rtd_client writes=0 cleanup=validated-exact-sockets"

# ---------------------------------------------------------------------------
# Registry-first primary navigation.
#
# The Projects surface used to enumerate the machine, so a Project whose session
# was closed disappeared from the list whose purpose is reopening it. This block
# proves the inverted authority against a real tmux server, a real attached
# client, and a real 80x24 popup: the Registry Project is a row while its session
# is live and still a row after the session is killed, with the same identity and
# only its status changed; the operator's own window, the Home control session,
# and a scratch session are not rows and are reachable through the Runtime link;
# and the whole surface writes nothing.
#
# Isolation follows the four mandatory conditions: inherited TMUX/TMUX_PANE are
# stripped from every call, the server lives under a run-unique TMUX_TMPDIR with
# its own -L name, the real #{socket_path} is queried and proven to sit inside the
# smoke root, and only that exact socket is killed.
# ---------------------------------------------------------------------------
nav_root="$PROJMUX_SMOKE_WORKDIR/n"
nav_socket="n-$$"
nav_other_socket="o-$$"
nav_session="nav-alpha"
nav_beta_session="nav-beta"
nav_driver="home"
mkdir -p "$nav_root/t" "$nav_root/state" "$nav_root/config" "$nav_root/home" \
  "$nav_root/runtime" "$nav_root/nav/alpha" "$nav_root/nav/beta"
chmod 0700 "$nav_root/runtime"
nav_real_tmux="$(command -v tmux)"

nav_tmux() { env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$nav_root/t" "$nav_real_tmux" -L "$nav_socket" "$@"; }
nav_other_tmux() { env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$nav_root/t" "$nav_real_tmux" -L "$nav_other_socket" "$@"; }
nav_pmx() {
  env -u TMUX -u TMUX_PANE \
    HOME="$nav_root/home" \
    XDG_CONFIG_HOME="$nav_root/config" \
    XDG_STATE_HOME="$nav_root/state" \
    XDG_RUNTIME_DIR="$nav_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$nav_root/nav" \
    TMUX_TMPDIR="$nav_root/t" \
    SHELL=/bin/sh \
    "$bin" "$@"
}

nav_tmux new-session -d -s "$nav_session" -c "$nav_root/nav/alpha" sleep 600
nav_tmux set-option -t "$nav_session" -q @projmux_project_path "$nav_root/nav/alpha"
# A second managed Project, imported in the same reconcile so the Registry holds
# both in a known slice order. Only one of them is live at a time later, which is
# what makes the presentation tiers observable at all.
nav_tmux new-session -d -s "$nav_beta_session" -c "$nav_root/nav/beta" sleep 600
nav_tmux set-option -t "$nav_beta_session" -q @projmux_project_path "$nav_root/nav/beta"
# The attached origin is the canonical Home target. Clearing a Project filter
# therefore exercises the real Home preview return without a fixture-only tmux
# switch; `nav-home` below remains the separate control-role runtime object.
nav_tmux new-session -d -s "$nav_driver" -c "$nav_root/home" bash --noprofile --norc
nav_other_tmux new-session -d -s untouched -c "$nav_root" sleep 600
nav_other_tmux set-option -gq @projmux_nav_sentinel unchanged

nav_socket_path="$(nav_tmux display-message -p -t "$nav_session" '#{socket_path}')"
nav_socket_pid="$(nav_tmux display-message -p -t "$nav_session" '#{pid}')"
nav_other_socket_path="$(nav_other_tmux display-message -p -t untouched '#{socket_path}')"
for actual in "$nav_socket_path" "$nav_other_socket_path"; do
  case "$actual" in
    "$nav_root"/*) ;;
    *)
      echo "registry navigation e2e socket escaped smoke root: $actual" >&2
      exit 1
      ;;
  esac
done

nav_cleanup() {
  local socket actual
  for socket in "$nav_socket" "$nav_other_socket"; do
    actual="$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$nav_root/t" tmux -L "$socket" display-message -p '#{socket_path}' 2>/dev/null || true)"
    if [[ -z "$actual" ]]; then
      continue
    fi
    case "$actual" in
      "$nav_root"/*) env -u TMUX -u TMUX_PANE tmux -S "$actual" kill-server >/dev/null 2>&1 || true ;;
      *) echo "refusing registry navigation cleanup outside smoke root: $actual" >&2 ;;
    esac
  done
}
trap 'nav_cleanup; smoke_cleanup_env' EXIT

nav_tmux set-option -g -q @projmux_app 1
nav_tmux set-option -g -q @projmux_socket_name "$nav_socket"
if [[ "$(nav_tmux show-options -gqv @projmux_app)" != "1" ]] || \
  [[ "$(nav_tmux show-options -gqv @projmux_socket_name)" != "$nav_socket" ]]; then
  echo "registry navigation fixture lacks a complete app-owned route marker pair" >&2
  exit 1
fi
# tmux renames a window to its foreground command on its own. That is tmux
# writing, not projmux, and it would break the zero-write comparison below.
nav_tmux set-option -g -q automatic-rename off
nav_pmx reconcile resources --socket "$nav_socket" --dry-run -o json >"$nav_root/d2.json"
smoke_assert_file_contains "$nav_root/d2.json" '"outcome": "no-op"'
if [[ -e "$nav_root/state/projmux/metadata/registry.json" ]]; then
  echo "registry navigation D2 dry-run created a Registry" >&2
  exit 1
fi
nav_pmx create project --root "$nav_root/nav/alpha" --name alpha >"$nav_root/register-alpha.out"
nav_pmx create project --root "$nav_root/nav/beta" --name beta >"$nav_root/register-beta.out"
e2e_bounded_reconcile_to_noop "$nav_root/import" \
  nav_pmx reconcile resources --socket "$nav_socket" -o json
nav_project_uid="$(nav_tmux show-options -t "$nav_session" -qv @projmux_project_uid)"
nav_beta_uid="$(nav_tmux show-options -t "$nav_beta_session" -qv @projmux_project_uid)"
for nav_uid in "$nav_project_uid" "$nav_beta_uid"; do
  if [[ -z "$nav_uid" ]]; then
    echo "registry navigation e2e explicit authority left a Project uid empty" >&2
    exit 1
  fi
done
if [[ "$nav_project_uid" == "$nav_beta_uid" ]]; then
  echo "registry navigation e2e imported both Projects under one uid: $nav_project_uid" >&2
  exit 1
fi

# Everything the managed list must not contain, on the same server: the control
# session, a scratch session, an operator's own window, and a window mirroring a
# uid the Registry does not carry.
nav_tmux new-session -d -s nav-home -c "$nav_root" sleep 600
nav_tmux set-option -t nav-home -q @projmux_session_role control
nav_tmux new-session -d -s nav-scratch -c "$nav_root" sleep 600
nav_tmux set-option -t nav-scratch -q @projmux_ephemeral 1
nav_tmux new-window -d -t "=$nav_session" -n handmade -c "$nav_root" sleep 600
nav_tmux new-window -d -t "=$nav_session" -n phantom -c "$nav_root" sleep 600
nav_phantom_window="$(nav_tmux list-windows -t "=$nav_session" -F '#{window_id} #{window_name}' | awk '$2 == "phantom" {print $1}')"
nav_tmux set-option -w -t "$nav_phantom_window" -q @projmux_window_uid win-not-in-this-registry

nav_snapshot() {
  nav_tmux list-sessions -F '#{session_id} #{session_name} #{@projmux_project_uid} #{@projmux_session_role} #{@projmux_ephemeral}'
  nav_tmux list-windows -a -F '#{window_id} #{session_id} #{window_name} #{@projmux_window_uid}'
  nav_tmux list-panes -a -F '#{pane_id} #{window_id} #{@projmux_pane_uid}'
}
nav_registry="$nav_root/state/projmux/metadata/registry.json"
cp "$nav_registry" "$nav_root/registry.before"
nav_other_before="$(nav_other_tmux show-options -gqv @projmux_nav_sentinel):$(nav_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"

# The attached client the popups are rendered into.
nav_client_log="$nav_root/driver-client.log"
nav_client_input="$nav_root/driver-client.in"
mkfifo "$nav_client_input"
exec 8<>"$nav_client_input"
TERM=xterm-256color script -qefc \
  "TERM=xterm-256color env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$nav_root/t' tmux -L '$nav_socket' attach-session -t '$nav_driver'" \
  "$nav_client_log" <"$nav_client_input" >/dev/null 2>&1 &
nav_client_pid=$!

nav_wait_for() {
  local description="$1"
  shift
  for _ in {1..200}; do
    if "$@"; then
      return 0
    fi
    sleep 0.05
  done
  echo "timed out waiting for $description" >&2
  tail -c 12000 "$nav_client_log" >&2 || true
  return 1
}

nav_wait_for "attached registry navigation client" sh -c \
  "test -n \"\$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$nav_root/t' tmux -L '$nav_socket' list-clients -F '#{client_name}' 2>/dev/null | head -n 1)\""
nav_client="$(nav_tmux list-clients -F '#{client_name}' | head -n 1)"
nav_client_is_on_driver() {
  [[ "$(nav_tmux display-message -p -c "$nav_client" '#{session_name}' 2>/dev/null || true)" == "$nav_driver" ]]
}
# One 80x24 popup run of the Projects sidebar, rendered through the exact
# inherited socket the client is attached to. This isolated case uses a short
# root and socket name so the complete path remains observable in the nested
# Runtime header without changing the viewport assumptions below.
nav_open_projects() {
  local offset_var="$1"
  printf -v "$offset_var" '%s' "$(stat -c %s "$nav_client_log")"
  nav_tmux display-popup -c "$nav_client" -T "Projects E2E" -w 80 -h 24 -E \
    "env -u TMUX_PANE HOME='$nav_root/home' XDG_CONFIG_HOME='$nav_root/config' XDG_STATE_HOME='$nav_root/state' XDG_RUNTIME_DIR='$nav_root/runtime' PROJMUX_MANAGED_ROOTS='$nav_root/nav' TMUX_TMPDIR='$nav_root/t' TMUX='$nav_socket_path,$nav_socket_pid,0' SHELL=/bin/sh '$bin' switch --ui=sidebar" &
  nav_popup_pid=$!
}

nav_open_projects nav_popup_offset
nav_screen_has() {
  tail -c +$((nav_popup_offset + 1)) "$nav_client_log" | grep -aFq "$1"
}
nav_wait_for "Projects sidebar" nav_screen_has "Projects"
# The Runtime link is a row of the Projects list, and it says what it leads to.
# The tally is the observation that the runtime-only objects on this server are
# present and reachable while being absent from the managed rows. Filter to the
# row so this evidence is independent of how many Home preview lines fit in the
# fixed 80x24 viewport.
nav_runtime_filter_offset="$(stat -c %s "$nav_client_log")"
printf 'Runtime' >&8
nav_runtime_filter_has() {
  tail -c +$((nav_runtime_filter_offset + 1)) "$nav_client_log" | grep -aF "$1" >/dev/null
}
nav_wait_for "Runtime link row" nav_runtime_filter_has "Runtime -"
nav_wait_for "Runtime link control tally" nav_runtime_filter_has "control 1"
nav_wait_for "Runtime link ephemeral tally" nav_runtime_filter_has "ephemeral 1"
nav_wait_for "Runtime link recoverable tally" nav_runtime_filter_has "recoverable 1"

# Entering Runtime is an in-process native-surface transition: switch owns the
# package-global theme section and diagnostics inherits it. This is the exact
# path that used to erase the outer popup and deadlock while trying to reacquire
# the same non-reentrant mutex.
nav_snapshot >"$nav_root/runtime-transition.before"
cp "$nav_registry" "$nav_root/runtime-transition-registry.before"
nav_runtime_enter_offset="$(stat -c %s "$nav_client_log")"
printf '\r' >&8
nav_runtime_enter_has() {
  tail -c +$((nav_runtime_enter_offset + 1)) "$nav_client_log" | grep -aF "$1" >/dev/null
}
nav_wait_for "nested Runtime diagnostics title" nav_runtime_enter_has "Runtime diagnostics"
nav_wait_for "nested Runtime diagnostics exact host" nav_runtime_enter_has "host app-owned"
nav_wait_for "nested Runtime diagnostics exact transport" \
  nav_runtime_enter_has "transport tmux -S $nav_socket_path"
nav_wait_for "nested Runtime diagnostics footer" nav_runtime_enter_has "Enter: actions"

# Escape closes diagnostics and therefore the outer switch invocation. Prove
# completion with the exact popup process and the attached client's unchanged
# target, then compare both the exact server and sibling before reopening the
# Projects surface for the remaining navigation assertions.
printf '\033' >&8
nav_wait_for "nested Runtime diagnostics popup exit" sh -c "! kill -0 '$nav_popup_pid' 2>/dev/null"
wait "$nav_popup_pid" || true
nav_wait_for "nested Runtime diagnostics return to driver" nav_client_is_on_driver
nav_snapshot >"$nav_root/runtime-transition.after"
cmp "$nav_root/runtime-transition.before" "$nav_root/runtime-transition.after"
cmp "$nav_root/runtime-transition-registry.before" "$nav_registry"
if [[ "$(nav_other_tmux show-options -gqv @projmux_nav_sentinel):$(nav_other_tmux list-windows -a -F '#{session_name}:#{window_name}')" != "$nav_other_before" ]]; then
  echo "switch -> Runtime touched the sibling socket" >&2
  exit 1
fi

nav_open_projects nav_popup_offset
nav_wait_for "Projects sidebar after nested Runtime exit" nav_screen_has "Projects"

# The managed row is reached through the list's own filter rather than by
# assuming a viewport. An 80x24 popup shows three cards at a time, so a row's
# presence is a property of the model, and the filter is how an operator asks the
# model for it.
nav_filter_offset="$(stat -c %s "$nav_client_log")"
printf 'alpha' >&8
nav_filter_has() {
  tail -c +$((nav_filter_offset + 1)) "$nav_client_log" | grep -aFq "$1"
}
# The needle is the row's own path line rather than the name, because the search
# field echoes whatever was typed: only the card can put the Project root on the
# screen.
nav_wait_for "managed Project row" nav_filter_has "nav/alpha"

# The dedicated key opens the read-only Registry hierarchy of the selected
# Project: its Window, its shell Pane, and their live status. This surface is the
# one that has to contain the Registry rows and nothing else, so it is where the
# separation is observed.
nav_hier_offset="$(stat -c %s "$nav_client_log")"
printf '\022' >&8
nav_hier_has() {
  tail -c +$((nav_hier_offset + 1)) "$nav_client_log" | grep -aFq "$1"
}
nav_wait_for "Projects resources hierarchy" nav_hier_has "Projects > Resources"
nav_wait_for "hierarchy host header" nav_hier_has "host app-owned"
nav_wait_for "hierarchy project row" nav_hier_has "project"
nav_wait_for "hierarchy window row" nav_hier_has "window"
nav_wait_for "hierarchy pane row" nav_hier_has "pane"
nav_wait_for "hierarchy live status" nav_hier_has "live"
for nav_absent in nav-home nav-scratch handmade phantom; do
  if nav_hier_has "$nav_absent"; then
    echo "the Registry hierarchy named the runtime-only object $nav_absent" >&2
    tail -c 12000 "$nav_client_log" >&2 || true
    exit 1
  fi
done
nav_root_return_offset="$(stat -c %s "$nav_client_log")"
printf '\003' >&8
nav_root_return_has() {
  tail -c +$((nav_root_return_offset + 1)) "$nav_client_log" | grep -aF "$1" >/dev/null
}
nav_wait_for "Projects root after closing the resources hierarchy" \
  nav_root_return_has "Alt-P: pin project"
# The root picker intentionally retains its `alpha` filter. Clear it through
# the picker input so Home becomes the selected root row and its preview
# restores the attached origin before the outer cancel is delivered.
nav_home_return_offset="$(stat -c %s "$nav_client_log")"
printf '\025' >&8
nav_home_return_has() {
  tail -c +$((nav_home_return_offset + 1)) "$nav_client_log" | grep -aF "home" >/dev/null &&
    tail -c +$((nav_home_return_offset + 1)) "$nav_client_log" | grep -aF "~" >/dev/null
}
nav_wait_for "Home root selection after clearing the Projects filter" nav_home_return_has
nav_wait_for "Home preview return to the driver session" nav_client_is_on_driver
printf '\003' >&8
nav_wait_for "Projects popup exit" sh -c "! kill -0 '$nav_popup_pid' 2>/dev/null"
wait "$nav_popup_pid" || true
nav_wait_for "Projects sidebar return to the driver session" nav_client_is_on_driver
# The presentation order of the managed rows.
#
# Order is a property of the list, so it is read out of a real pane instead of a
# popup: `capture-pane` returns one frame with the rows in the order they are
# drawn, and the sidebar's preview is placed below the list, so the first frame
# line matching a row's path is always that row rather than its preview echo.
#
# Registry order is read from the Registry itself rather than assumed, because
# what has to be proven is that the *live overlay* -- not the import order --
# decides which managed Project comes first.
#
# The list is opened in a detached session with an explicit size rather than in
# the attached driver session: a detached session's -x/-y are fixed, so the whole
# list fits the viewport instead of depending on whatever size the harness's
# terminal happens to have. It starts in `$HOME` so the cursor lands on the Home
# row and the viewport is at the top of the list.
nav_order_session="nav-order"
nav_order_pane=""
nav_open_order_pane() {
  nav_order_pane="$(nav_tmux new-session -d -s "$nav_order_session" -x 100 -y 40 -P -F '#{pane_id}' -c "$nav_root/home" \
    "env HOME='$nav_root/home' XDG_CONFIG_HOME='$nav_root/config' XDG_STATE_HOME='$nav_root/state' XDG_RUNTIME_DIR='$nav_root/runtime' PROJMUX_MANAGED_ROOTS='$nav_root/nav' TMUX_TMPDIR='$nav_root/tmux' SHELL=/bin/sh '$bin' switch --ui=sidebar")"
}
nav_close_order_pane() {
  nav_tmux kill-session -t "=$nav_order_session" >/dev/null 2>&1 || true
  nav_order_pane=""
}
nav_order_sequence() {
  nav_tmux capture-pane -p -t "$nav_order_pane" 2>/dev/null \
    | grep -aoE 'nav/(alpha|beta)' \
    | sed -e 's|nav/||' \
    | awk '!seen[$0]++' \
    | tr '\n' ' ' \
    | sed -e 's/ *$//'
}
nav_order_ready() {
  local sequence
  sequence="$(nav_order_sequence)"
  [[ "$sequence" == "alpha beta" || "$sequence" == "beta alpha" ]]
}
# Home leads the whole list. Its own card carries `~` as its path line, so an
# exact match on the first such line is the Home row and nothing else.
nav_home_row_leads() {
  # `capture-pane` returns the framed cells, so the row is matched by content
  # rather than by an exact line: the Home card is the only one whose path cell is
  # `~`, and it is the only line on the screen that carries a `~` and no `/`.
  nav_tmux capture-pane -p -t "$nav_order_pane" 2>/dev/null | awk '
    { line = $0
      if (!home && index(line, "~") > 0 && index(line, "/") == 0) { home = NR }
      if (!project && (index(line, "nav/alpha") > 0 || index(line, "nav/beta") > 0)) { project = NR } }
    END { exit !(home && project && home < project) }'
}
nav_registry_project_order() {
  grep -a '"root":' "$nav_registry" \
    | sed -e 's|.*/nav/||' -e 's|".*||' \
    | tr '\n' ' ' \
    | sed -e 's/ *$//'
}

nav_registry_order="$(nav_registry_project_order)"
case "$nav_registry_order" in
  "alpha beta" | "beta alpha") ;;
  *)
    echo "registry navigation e2e expected two managed Project roots, got: $nav_registry_order" >&2
    exit 1
    ;;
esac

nav_open_order_pane
nav_wait_for "sidebar order pane with both managed rows" nav_order_ready
nav_wait_for "Home chrome row leading the managed rows" nav_home_row_leads
nav_live_order="$(nav_order_sequence)"
if [[ "$nav_live_order" != "$nav_registry_order" ]]; then
  echo "both-live sidebar order = $nav_live_order, want Registry order $nav_registry_order" >&2
  nav_tmux capture-pane -p -t "$nav_order_pane" >&2 || true
  exit 1
fi
nav_close_order_pane

# The runtime goes away. A Registry row is a logical resource, so the Project has
# to still be a row -- with the same identity and only its status changed.
nav_tmux kill-session -t "=$nav_session"
nav_open_projects nav_popup_offset
nav_wait_for "Projects sidebar after the session was killed" nav_screen_has "Projects"
nav_offline_filter_offset="$(stat -c %s "$nav_client_log")"
printf 'alpha' >&8
nav_offline_filter_has() {
  tail -c +$((nav_offline_filter_offset + 1)) "$nav_client_log" | grep -aFq "$1"
}
nav_wait_for "offline Project row" nav_offline_filter_has "nav/alpha"
nav_offline_offset="$(stat -c %s "$nav_client_log")"
printf '\022' >&8
nav_offline_has() {
  tail -c +$((nav_offline_offset + 1)) "$nav_client_log" | grep -aFq "$1"
}
nav_wait_for "offline hierarchy" nav_offline_has "Projects > Resources"
nav_wait_for "offline hierarchy row" nav_offline_has "offline"
nav_wait_for "offline start action" nav_offline_has "start"
nav_offline_root_return_offset="$(stat -c %s "$nav_client_log")"
printf '\003' >&8
nav_offline_root_return_has() {
  tail -c +$((nav_offline_root_return_offset + 1)) "$nav_client_log" | grep -aF "$1" >/dev/null
}
nav_wait_for "Projects root after closing the offline resources hierarchy" \
  nav_offline_root_return_has "Alt-P: pin project"
# fzf may retain the already-rendered Home cells when `alpha` is cleared, so a
# fresh log slice is not guaranteed to contain their bytes. First transition
# through an acknowledged no-match query; clearing that query must repopulate
# and newly render the Home row/path before the outer picker is cancelled.
nav_offline_no_match_offset="$(stat -c %s "$nav_client_log")"
printf '__phase8_no_match__' >&8
nav_offline_no_match_has() {
  tail -c +$((nav_offline_no_match_offset + 1)) "$nav_client_log" |
    grep -aF "__phase8_no_match__" >/dev/null
}
nav_wait_for "offline Projects no-match query" nav_offline_no_match_has
nav_offline_home_return_offset="$(stat -c %s "$nav_client_log")"
printf '\025' >&8
nav_offline_home_return_has() {
  tail -c +$((nav_offline_home_return_offset + 1)) "$nav_client_log" | grep -aF "home" >/dev/null &&
    tail -c +$((nav_offline_home_return_offset + 1)) "$nav_client_log" | grep -aF "~" >/dev/null
}
nav_wait_for "Home root selection after clearing the offline Projects filter" \
  nav_offline_home_return_has
nav_wait_for "offline Home preview return to the driver session" nav_client_is_on_driver
printf '\003' >&8
nav_wait_for "offline Projects popup exit" sh -c "! kill -0 '$nav_popup_pid' 2>/dev/null"
wait "$nav_popup_pid" || true
nav_wait_for "offline Projects sidebar return to the driver session" nav_client_is_on_driver

# The closed Project is still a row and it is now behind the one that is still
# live. Nothing about the Registry changed -- its slice order is the same file --
# so the only thing that moved the row is the exact host's live overlay, which is
# the whole point of a presentation tier.
nav_open_order_pane
nav_wait_for "sidebar order pane after the close" nav_order_ready
nav_wait_for "Home chrome row still leading" nav_home_row_leads
nav_closed_order="$(nav_order_sequence)"
if [[ "$nav_closed_order" != "beta alpha" ]]; then
  echo "post-close sidebar order = $nav_closed_order, want the live Project first: beta alpha" >&2
  nav_tmux capture-pane -p -t "$nav_order_pane" >&2 || true
  exit 1
fi
if [[ "$(nav_registry_project_order)" != "$nav_registry_order" ]]; then
  echo "the sidebar reordering changed Registry order: $(nav_registry_project_order) != $nav_registry_order" >&2
  exit 1
fi
if [[ "$nav_registry_order" == "alpha beta" && "$nav_closed_order" == "$nav_live_order" ]]; then
  echo "closing the live Project moved no row, so the presentation tiers are untested" >&2
  exit 1
fi
nav_close_order_pane

# Zero writes: the Registry the whole surface read is byte-identical, the sibling
# socket never moved, and the attached client is where it started.
cmp "$nav_root/registry.before" "$nav_registry"
nav_other_after="$(nav_other_tmux show-options -gqv @projmux_nav_sentinel):$(nav_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"
if [[ "$nav_other_after" != "$nav_other_before" ]]; then
  echo "registry navigation touched the sibling socket" >&2
  exit 1
fi
if [[ "$(nav_tmux display-message -p -c "$nav_client" '#{session_name}')" != "$nav_driver" ]]; then
  echo "registry navigation moved the attached client" >&2
  exit 1
fi

exec 8>&-
kill "$nav_client_pid" >/dev/null 2>&1 || true
wait "$nav_client_pid" 2>/dev/null || true
nav_cleanup
trap smoke_cleanup_env EXIT
echo ">> Registry-first navigation e2e passed: socket=$nav_socket path=$nav_socket_path other-socket=$nav_other_socket other-path=$nav_other_socket_path client=$nav_client project=$nav_project_uid beta=$nav_beta_uid offline-row=preserved registry-order=\"$nav_registry_order\" live-order=\"$nav_live_order\" closed-order=\"$nav_closed_order\" writes=0 cleanup=validated-exact-sockets"

# ---------------------------------------------------------------------------
# Alt-1 contextual Runtime row.
#
# The Projects sidebar is a Project-switching surface, and its Runtime link is a
# diagnostics escape hatch. `Settings > Projects > Project Sidebar > Runtime
# diagnostics` decides whether the row is always there or only when it is needed,
# with `When needed` the read-time default. The Registry-first navigation block
# above already proves the needed half against a deliberately anomalous host --
# control, ephemeral and recoverable objects are on that server, so the default
# keeps the row and the nested Runtime entry/Esc return works there.
#
# This block proves the other half against a host that is exactly what the
# Registry desires: every observed session, window and pane is managed, so the
# default withholds the row and `Always` brings it back with its entry and Esc
# return intact. The direct `get runtime` routes are compared across both modes
# on the same real server, because a presentation preference must never reach a
# diagnostics route.
#
# Isolation follows the four mandatory conditions: inherited TMUX/TMUX_PANE are
# stripped from every call, the server lives under a run-unique TMUX_TMPDIR with
# its own -L name, the real #{socket_path} is queried and proven to sit inside the
# smoke root, and only those exact sockets are killed.
# ---------------------------------------------------------------------------
rtv_root="$PROJMUX_SMOKE_WORKDIR/rtv"
rtv_socket="rtv-$$"
rtv_other_socket="rtvo-$$"
rtv_session="rtv-alpha"
rtv_driver="rtv-driver"
rtv_list_session="rtv-list"
mkdir -p "$rtv_root/t" "$rtv_root/state" "$rtv_root/config/projmux" "$rtv_root/home" \
  "$rtv_root/runtime" "$rtv_root/p/alpha" "$rtv_root/p/driver" "$rtv_root/p/list"
chmod 0700 "$rtv_root/runtime"
rtv_real_tmux="$(command -v tmux)"
rtv_visibility_file="$rtv_root/config/projmux/runtime-diagnostics-visibility"

rtv_tmux() { env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$rtv_root/t" "$rtv_real_tmux" -L "$rtv_socket" "$@"; }
rtv_other_tmux() { env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$rtv_root/t" "$rtv_real_tmux" -L "$rtv_other_socket" "$@"; }
rtv_pmx() {
  env -u TMUX -u TMUX_PANE \
    HOME="$rtv_root/home" \
    XDG_CONFIG_HOME="$rtv_root/config" \
    XDG_STATE_HOME="$rtv_root/state" \
    XDG_RUNTIME_DIR="$rtv_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$rtv_root/p" \
    TMUX_TMPDIR="$rtv_root/t" \
    SHELL=/bin/sh \
    "$bin" "$@"
}
# The sidebar's own environment, shared by the popup and the list pane so the two
# observations differ in viewport and in nothing else.
rtv_sidebar_command="env HOME='$rtv_root/home' XDG_CONFIG_HOME='$rtv_root/config' XDG_STATE_HOME='$rtv_root/state' XDG_RUNTIME_DIR='$rtv_root/runtime' PROJMUX_MANAGED_ROOTS='$rtv_root/p' TMUX_TMPDIR='$rtv_root/t' SHELL=/bin/sh '$bin' switch --ui=sidebar"

# Three managed Projects and nothing else on the server. Two of them exist so the
# list has real rows; the third is the surface the list is rendered into, and it
# is a Project precisely so that observing the sidebar does not create the
# unattributed object the sidebar would then report.
rtv_tmux new-session -d -s "$rtv_session" -c "$rtv_root/p/alpha" sleep 600
rtv_tmux set-option -t "$rtv_session" -q @projmux_project_path "$rtv_root/p/alpha"
rtv_tmux new-session -d -s "$rtv_driver" -c "$rtv_root/p/driver" bash --noprofile --norc
rtv_tmux set-option -t "$rtv_driver" -q @projmux_project_path "$rtv_root/p/driver"
rtv_tmux new-session -d -s "$rtv_list_session" -x 80 -y 40 -c "$rtv_root/p/list" sleep 600
rtv_tmux set-option -t "$rtv_list_session" -q @projmux_project_path "$rtv_root/p/list"
rtv_other_tmux new-session -d -s untouched -c "$rtv_root" sleep 600
rtv_other_tmux set-option -gq @projmux_rtv_sentinel unchanged

rtv_socket_path="$(rtv_tmux display-message -p -t "$rtv_session" '#{socket_path}')"
rtv_socket_pid="$(rtv_tmux display-message -p -t "$rtv_session" '#{pid}')"
rtv_other_socket_path="$(rtv_other_tmux display-message -p -t untouched '#{socket_path}')"
for actual in "$rtv_socket_path" "$rtv_other_socket_path"; do
  case "$actual" in
    "$rtv_root"/*) ;;
    *)
      echo "Alt-1 Runtime visibility e2e socket escaped smoke root: $actual" >&2
      exit 1
      ;;
  esac
done

rtv_cleanup() {
  local socket actual
  for socket in "$rtv_socket" "$rtv_other_socket"; do
    actual="$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$rtv_root/t" tmux -L "$socket" display-message -p '#{socket_path}' 2>/dev/null || true)"
    if [[ -z "$actual" ]]; then
      continue
    fi
    case "$actual" in
      "$rtv_root"/*) env -u TMUX -u TMUX_PANE tmux -S "$actual" kill-server >/dev/null 2>&1 || true ;;
      *) echo "refusing Alt-1 Runtime visibility cleanup outside smoke root: $actual" >&2 ;;
    esac
  done
}
trap 'rtv_cleanup; smoke_cleanup_env' EXIT

rtv_tmux set-option -g -q @projmux_app 1
rtv_tmux set-option -g -q @projmux_socket_name "$rtv_socket"
if [[ "$(rtv_tmux show-options -gqv @projmux_app)" != "1" ]] || \
  [[ "$(rtv_tmux show-options -gqv @projmux_socket_name)" != "$rtv_socket" ]]; then
  echo "Alt-1 Runtime visibility fixture lacks a complete app-owned route marker pair" >&2
  exit 1
fi
rtv_tmux set-option -g -q automatic-rename off
for rtv_project in alpha driver list; do
  rtv_pmx create project --root "$rtv_root/p/$rtv_project" --name "rtv-$rtv_project" >"$rtv_root/register-$rtv_project.out"
done
e2e_bounded_reconcile_to_noop "$rtv_root/import" \
  rtv_pmx reconcile resources --socket "$rtv_socket" -o json

# The fixture is only meaningful if the host really is what the Registry desires.
# A withheld row over an anomalous host would prove nothing, so the class column
# of every scope is checked to be `managed` before the sidebar is ever opened.
for rtv_scope in sessions windows panes; do
  rtv_pmx get runtime "$rtv_scope" --socket "$rtv_socket" >"$rtv_root/classes-$rtv_scope.txt"
  rtv_classes="$(tail -n +3 "$rtv_root/classes-$rtv_scope.txt" |
    awk '{for (i = 1; i <= NF; i++) if ($i ~ /^(managed|control|ephemeral|unattributed|foreign|recoverable|conflict)$/) { print $i; break }}' |
    sort -u | tr '\n' ' ')"
  if [[ "$rtv_classes" != "managed " ]]; then
    echo "Alt-1 Runtime visibility e2e fixture is not healthy: $rtv_scope classes = $rtv_classes" >&2
    cat "$rtv_root/classes-$rtv_scope.txt" >&2
    exit 1
  fi
done
if [[ -e "$rtv_visibility_file" ]]; then
  echo "Alt-1 Runtime visibility e2e started with a saved preference: $rtv_visibility_file" >&2
  exit 1
fi

rtv_registry="$rtv_root/state/projmux/metadata/registry.json"
cp "$rtv_registry" "$rtv_root/registry.before"
rtv_other_before="$(rtv_other_tmux show-options -gqv @projmux_rtv_sentinel):$(rtv_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"

# The list pane is the managed Pane of the third Project. Rendering the sidebar
# into it with `respawn-pane` keeps that exact Pane and its mirrored uid, so the
# observation the sidebar takes of its own host stays complete and managed.
rtv_list_pane="$(rtv_tmux list-panes -t "=$rtv_list_session" -F '#{pane_id}' | head -n 1)"
rtv_render_list() {
  rtv_tmux respawn-pane -k -t "$rtv_list_pane" "$rtv_sidebar_command"
}
rtv_stop_list() {
  rtv_tmux respawn-pane -k -t "$rtv_list_pane" sleep 600
}
rtv_list_screen() {
  rtv_tmux capture-pane -p -t "$rtv_list_pane" 2>/dev/null || true
}
rtv_list_ready() {
  local screen
  screen="$(rtv_list_screen)"
  [[ "$screen" == *"p/alpha"* && "$screen" == *"p/driver"* && "$screen" == *"p/list"* ]]
}
rtv_list_has_runtime() {
  [[ "$(rtv_list_screen)" == *"Runtime"* ]]
}

rtv_wait_for() {
  local description="$1"
  shift
  for _ in {1..200}; do
    if "$@"; then
      return 0
    fi
    sleep 0.05
  done
  echo "timed out waiting for $description" >&2
  return 1
}

# Default `When needed` on a healthy host: the Project rows are there and the
# diagnostics row is not.
#
# Absence is asserted from a whole captured screen 40 rows tall rather than from a
# scrolling 80x24 transcript, because the row is last in the list and a viewport
# that could not have shown it would prove nothing. The `Always` half below uses
# the same pane and the same geometry, so the difference observed is the
# preference and not the viewport.
rtv_render_list
rtv_wait_for "default sidebar list pane with every managed row" rtv_list_ready
if rtv_list_has_runtime; then
  echo "the default Projects sidebar offered a Runtime row on a healthy host" >&2
  rtv_list_screen >&2
  exit 1
fi
rtv_list_screen >"$rtv_root/sidebar-default.txt"
rtv_stop_list

# The same host, the same pane, the same geometry, `Always` saved: the row is
# back. Without this half the withheld row above would only prove the list was
# short.
printf 'always\n' >"$rtv_visibility_file"
rtv_render_list
rtv_wait_for "Always sidebar list pane with every managed row" rtv_list_ready
rtv_wait_for "Always Runtime row in the list pane" rtv_list_has_runtime
rtv_list_screen >"$rtv_root/sidebar-always.txt"
if ! grep -aFq "nothing here that projmux does not manage" "$rtv_root/sidebar-always.txt"; then
  echo "the Always Runtime row lost its shipped empty-state label" >&2
  cat "$rtv_root/sidebar-always.txt" >&2
  exit 1
fi
rtv_stop_list

# The real attached 80x24 client. The default leads with the Project rows the
# surface exists for; `Always` restores the row and it still enters the
# diagnostics surface in-process and returns on Esc.
rtv_client_log="$rtv_root/driver-client.log"
rtv_client_input="$rtv_root/driver-client.in"
mkfifo "$rtv_client_input"
exec 9<>"$rtv_client_input"
TERM=xterm-256color script -qefc \
  "TERM=xterm-256color env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$rtv_root/t' tmux -L '$rtv_socket' attach-session -t '$rtv_driver'" \
  "$rtv_client_log" <"$rtv_client_input" >/dev/null 2>&1 &
rtv_client_pid=$!

rtv_wait_for_client() {
  local description="$1"
  shift
  for _ in {1..200}; do
    if "$@"; then
      return 0
    fi
    sleep 0.05
  done
  echo "timed out waiting for $description" >&2
  tail -c 12000 "$rtv_client_log" >&2 || true
  return 1
}

rtv_wait_for_client "attached Alt-1 Runtime visibility client" sh -c \
  "test -n \"\$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$rtv_root/t' tmux -L '$rtv_socket' list-clients -F '#{client_name}' 2>/dev/null | head -n 1)\""
rtv_client="$(rtv_tmux list-clients -F '#{client_name}' | head -n 1)"
rtv_client_is_on_driver() {
  [[ "$(rtv_tmux display-message -p -c "$rtv_client" '#{session_name}' 2>/dev/null || true)" == "$rtv_driver" ]]
}
rtv_open_projects() {
  local offset_var="$1"
  printf -v "$offset_var" '%s' "$(stat -c %s "$rtv_client_log")"
  rtv_tmux display-popup -c "$rtv_client" -T "Alt-1 Runtime E2E" -w 80 -h 24 -E \
    "env -u TMUX_PANE HOME='$rtv_root/home' XDG_CONFIG_HOME='$rtv_root/config' XDG_STATE_HOME='$rtv_root/state' XDG_RUNTIME_DIR='$rtv_root/runtime' PROJMUX_MANAGED_ROOTS='$rtv_root/p' TMUX_TMPDIR='$rtv_root/t' TMUX='$rtv_socket_path,$rtv_socket_pid,0' SHELL=/bin/sh '$bin' switch --ui=sidebar" &
  rtv_popup_pid=$!
}
rtv_screen_has() {
  tail -c +$((rtv_popup_offset + 1)) "$rtv_client_log" | grep -aFq "$1"
}

: >"$rtv_visibility_file"
rm -f "$rtv_visibility_file"
rtv_open_projects rtv_popup_offset
rtv_wait_for_client "default 80x24 Projects sidebar" rtv_screen_has "Projects"
rtv_wait_for_client "default 80x24 managed Project row" rtv_screen_has "$rtv_root/p/alpha"
printf '\033' >&9
rtv_wait_for_client "default 80x24 Projects popup exit" sh -c "! kill -0 '$rtv_popup_pid' 2>/dev/null"
wait "$rtv_popup_pid" || true
rtv_wait_for_client "default 80x24 Projects return to driver" rtv_client_is_on_driver

printf 'always\n' >"$rtv_visibility_file"
rtv_open_projects rtv_popup_offset
rtv_wait_for_client "Projects sidebar under Always" rtv_screen_has "Projects"
rtv_always_filter_offset="$(stat -c %s "$rtv_client_log")"
printf 'Runtime' >&9
rtv_always_filter_has() {
  tail -c +$((rtv_always_filter_offset + 1)) "$rtv_client_log" | grep -aF "$1" >/dev/null
}
rtv_wait_for_client "Always Runtime row in the 80x24 client" rtv_always_filter_has "Runtime -"
rtv_enter_offset="$(stat -c %s "$rtv_client_log")"
printf '\r' >&9
rtv_enter_has() {
  tail -c +$((rtv_enter_offset + 1)) "$rtv_client_log" | grep -aF "$1" >/dev/null
}
rtv_wait_for_client "nested Runtime diagnostics title under Always" rtv_enter_has "Runtime diagnostics"
rtv_wait_for_client "nested Runtime diagnostics exact host under Always" rtv_enter_has "host app-owned"
printf '\033' >&9
rtv_wait_for_client "Always Runtime popup exit" sh -c "! kill -0 '$rtv_popup_pid' 2>/dev/null"
wait "$rtv_popup_pid" || true
rtv_wait_for_client "Always Runtime return to driver" rtv_client_is_on_driver

# The preference is presentation only: the direct runtime routes answer
# byte-identically in both modes, on the same real server.
for rtv_mode in when-needed always; do
  printf '%s\n' "$rtv_mode" >"$rtv_visibility_file"
  : >"$rtv_root/direct-$rtv_mode.txt"
  for rtv_scope in sessions windows panes; do
    rtv_pmx get runtime "$rtv_scope" --socket "$rtv_socket" >>"$rtv_root/direct-$rtv_mode.txt"
  done
done
cmp "$rtv_root/direct-when-needed.txt" "$rtv_root/direct-always.txt"

# Zero writes: the Registry the whole surface read is byte-identical, the sibling
# socket never moved, and the attached client is where it started.
cmp "$rtv_root/registry.before" "$rtv_registry"
rtv_other_after="$(rtv_other_tmux show-options -gqv @projmux_rtv_sentinel):$(rtv_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"
if [[ "$rtv_other_after" != "$rtv_other_before" ]]; then
  echo "Alt-1 Runtime visibility touched the sibling socket" >&2
  exit 1
fi
if [[ "$(rtv_tmux display-message -p -c "$rtv_client" '#{session_name}')" != "$rtv_driver" ]]; then
  echo "Alt-1 Runtime visibility moved the attached client" >&2
  exit 1
fi

exec 9>&-
kill "$rtv_client_pid" >/dev/null 2>&1 || true
wait "$rtv_client_pid" 2>/dev/null || true
rtv_cleanup
trap smoke_cleanup_env EXIT
echo ">> Alt-1 Runtime visibility e2e passed: socket=$rtv_socket path=$rtv_socket_path other-socket=$rtv_other_socket other-path=$rtv_other_socket_path client=$rtv_client list-pane=$rtv_list_pane default=withheld always=offered direct-routes=identical writes=0 cleanup=validated-exact-sockets"

# ---------------------------------------------------------------------------
# Project discovery and pin authority split.
#
# A discovery root full of directories used to be a Registry full of Projects:
# any mutation registered everything the scan found, so "unregistered" was a
# state a directory passed through rather than one it stayed in. This block
# proves the three collections against a real server, a real attached client and
# a real `switch open`: scanning registers nothing, opening one candidate
# registers that one path, its siblings stay candidates, and the pin file states
# which authority each preference points at.
#
# The Windows half of the same contract is frozen by the cross-platform Settings
# golden and the MatchKeyFor compatibility table in the unit suite; this block is
# the Linux runtime half.
#
# Isolation follows the four mandatory conditions: inherited TMUX/TMUX_PANE are
# stripped from every call, the server lives under a run-unique TMUX_TMPDIR with
# its own -L name, the real #{socket_path} is queried and proven to sit inside the
# smoke root, and only those exact sockets are killed.
# ---------------------------------------------------------------------------
disc_root="$PROJMUX_SMOKE_WORKDIR/discovery-authority-e2e"
disc_socket="projmux-discovery-$$-$RANDOM"
disc_other_socket="projmux-discovery-other-$$-$RANDOM"
disc_driver="disc-driver"
mkdir -p "$disc_root/home" "$disc_root/config" "$disc_root/state" "$disc_root/runtime" \
  "$disc_root/tmux" "$disc_root/bin" "$disc_root/shim" \
  "$disc_root/work/app" "$disc_root/work/scratch" "$disc_root/work/sibling"
chmod 0700 "$disc_root/runtime" "$disc_root/tmux"
disc_registry="$disc_root/state/projmux/metadata/registry.json"
disc_pins="$disc_root/config/projmux/pins"
disc_real_tmux="$(command -v tmux)"

disc_shell="$disc_root/shim/persistent-shell"
cat >"$disc_shell" <<'DISC_SHELL_STUB'
#!/usr/bin/env bash
exec sleep 600
DISC_SHELL_STUB
chmod 0755 "$disc_shell"

disc_tmux() {
  env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$disc_root/tmux" SHELL="$disc_shell" \
    "$disc_real_tmux" -L "$disc_socket" "$@"
}
disc_other_tmux() {
  env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$disc_root/tmux" SHELL="$disc_shell" \
    "$disc_real_tmux" -L "$disc_other_socket" "$@"
}
disc_pmx() {
  env -u TMUX -u TMUX_PANE \
    HOME="$disc_root/home" \
    XDG_CONFIG_HOME="$disc_root/config" \
    XDG_STATE_HOME="$disc_root/state" \
    XDG_RUNTIME_DIR="$disc_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$disc_root/work" \
    TMUX_TMPDIR="$disc_root/tmux" \
    SHELL="$disc_shell" \
    "$bin" "$@"
}

# `switch open` resolves the app socket by its default name. The same shim the
# startup block uses maps that one default route onto this smoke socket.
cat >"$disc_root/bin/tmux" <<DISC_TMUX_SHIM
#!/usr/bin/env bash
if [[ "\${1:-}" == "-L" && "\${2:-}" == "projmux" ]]; then
  shift 2
  exec $(printf %q "$disc_real_tmux") -L $(printf %q "$disc_socket") "\$@"
fi
if [[ "\${1:-}" == "-L" || "\${1:-}" == "-S" ]]; then
  exec $(printf %q "$disc_real_tmux") "\$@"
fi
exec $(printf %q "$disc_real_tmux") -L $(printf %q "$disc_socket") "\$@"
DISC_TMUX_SHIM
chmod 0755 "$disc_root/bin/tmux"

cat >"$disc_root/open-candidate.sh" <<DISC_OPEN_SCRIPT
#!/usr/bin/env bash
export HOME="$disc_root/home"
export XDG_CONFIG_HOME="$disc_root/config"
export XDG_STATE_HOME="$disc_root/state"
export XDG_RUNTIME_DIR="$disc_root/runtime"
export PROJMUX_MANAGED_ROOTS="$disc_root/work"
export TMUX_TMPDIR="$disc_root/tmux"
export SHELL="$disc_shell"
export PATH="$disc_root/bin:\$PATH"
$(printf %q "$bin") switch open "\$1" >"$disc_root/open-\$2.out" 2>"$disc_root/open-\$2.err"
echo \$? >"$disc_root/open-\$2.rc"
DISC_OPEN_SCRIPT
chmod 0755 "$disc_root/open-candidate.sh"

disc_tmux new-session -d -s "$disc_driver" -c "$disc_root" bash --noprofile --norc
disc_tmux set-option -g -q @projmux_app 1
disc_tmux set-option -g -q @projmux_socket_name "$disc_socket"
if [[ "$(disc_tmux show-options -gqv @projmux_app)" != "1" ]] || \
  [[ "$(disc_tmux show-options -gqv @projmux_socket_name)" != "$disc_socket" ]]; then
  echo "discovery authority fixture lacks a complete app-owned route marker pair" >&2
  exit 1
fi
disc_tmux set-option -g -q automatic-rename off
disc_other_tmux new-session -d -s untouched -c "$disc_root" sleep 600
disc_other_tmux set-option -gq @projmux_disc_sentinel unchanged

disc_socket_path="$(disc_tmux display-message -p -t "$disc_driver" '#{socket_path}')"
disc_other_socket_path="$(disc_other_tmux display-message -p -t untouched '#{socket_path}')"
for actual in "$disc_socket_path" "$disc_other_socket_path"; do
  case "$actual" in
    "$disc_root"/*) ;;
    *)
      echo "discovery authority e2e socket escaped smoke root: $actual" >&2
      exit 1
      ;;
  esac
done

disc_cleanup() {
  local socket actual
  for socket in "$disc_socket" "$disc_other_socket"; do
    actual="$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$disc_root/tmux" tmux -L "$socket" display-message -p '#{socket_path}' 2>/dev/null || true)"
    if [[ -z "$actual" ]]; then
      continue
    fi
    case "$actual" in
      "$disc_root"/*) env -u TMUX -u TMUX_PANE tmux -S "$actual" kill-server >/dev/null 2>&1 || true ;;
      *) echo "refusing discovery authority cleanup outside smoke root: $actual" >&2 ;;
    esac
  done
}
trap 'disc_cleanup; smoke_cleanup_env' EXIT

disc_other_before="$(disc_other_tmux show-options -gqv @projmux_disc_sentinel):$(disc_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"

# 1. Reconciling a server whose only sessions are the driver and nothing
# attributable registers no Project, with three directories under the scan root.
disc_pmx reconcile resources --socket "$disc_socket" >"$disc_root/import.out"
disc_pmx get projects -o json >"$disc_root/projects-after-reconcile.json"
smoke_assert_file_contains "$disc_root/projects-after-reconcile.json" '"items": []'

# 2. Attach a real client and open exactly one candidate from inside its pane.
disc_client_log="$disc_root/driver-client.log"
disc_client_input="$disc_root/driver-client.in"
mkfifo "$disc_client_input"
exec 9<>"$disc_client_input"
TERM=xterm-256color script -qefc \
  "TERM=xterm-256color env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$disc_root/tmux' tmux -L '$disc_socket' attach-session -t '$disc_driver'" \
  "$disc_client_log" <"$disc_client_input" >/dev/null 2>&1 &
disc_client_pid=$!

disc_wait_for() {
  local description="$1"
  shift
  for _ in {1..200}; do
    if "$@"; then
      return 0
    fi
    sleep 0.05
  done
  echo "timed out waiting for $description" >&2
  tail -c 8000 "$disc_client_log" >&2 || true
  return 1
}

disc_wait_for "attached discovery tmux client" sh -c \
  "test -n \"\$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$disc_root/tmux' tmux -L '$disc_socket' list-clients -F '#{client_name}' 2>/dev/null | head -n 1)\""
disc_client="$(disc_tmux list-clients -F '#{client_name}' | head -n 1)"
disc_driver_pane="$(disc_tmux display-message -p -c "$disc_client" '#{pane_id}')"

disc_tmux send-keys -t "$disc_driver_pane" "bash '$disc_root/open-candidate.sh' '$disc_root/work/app' bootstrap" Enter
disc_wait_for "candidate bootstrap open" test -s "$disc_root/open-bootstrap.rc"
if [[ "$(tr -d '[:space:]' <"$disc_root/open-bootstrap.rc")" != "0" ]]; then
  echo "opening an unregistered candidate failed" >&2
  cat "$disc_root/open-bootstrap.err" >&2 || true
  exit 1
fi

disc_project_uids="$(disc_pmx get projects -o uid)"
if [[ "$(printf '%s\n' "$disc_project_uids" | wc -l)" != "1" ]]; then
  echo "one candidate open registered more than one Project: $disc_project_uids" >&2
  disc_pmx get projects -o json >&2
  exit 1
fi
disc_project_uid="$disc_project_uids"
disc_pmx describe project "uid:$disc_project_uid" -o json >"$disc_root/project.json"
smoke_assert_file_contains "$disc_root/project.json" "\"root\": \"$disc_root/work/app\""
for disc_unregistered in scratch sibling; do
  if grep -Fq "$disc_root/work/$disc_unregistered" "$disc_root/project.json"; then
    echo "the bootstrap claimed a sibling candidate ($disc_unregistered)" >&2
    exit 1
  fi
done
disc_pmx get projects -o json >"$disc_root/projects-after-bootstrap.json"
for disc_unregistered in scratch sibling; do
  if grep -Fq "$disc_root/work/$disc_unregistered" "$disc_root/projects-after-bootstrap.json"; then
    echo "a sibling candidate ($disc_unregistered) was registered by the bootstrap" >&2
    exit 1
  fi
done

# 3. Reopening the now-registered Project is write-free.
cp "$disc_registry" "$disc_root/registry.after-bootstrap"
disc_tmux send-keys -t "$disc_driver_pane" "bash '$disc_root/open-candidate.sh' '$disc_root/work/app' repeat" Enter
disc_wait_for "candidate reopen" test -s "$disc_root/open-repeat.rc"
if [[ "$(tr -d '[:space:]' <"$disc_root/open-repeat.rc")" != "0" ]]; then
  echo "reopening the registered Project failed" >&2
  cat "$disc_root/open-repeat.err" >&2 || true
  exit 1
fi
cmp "$disc_root/registry.after-bootstrap" "$disc_registry"

# 3b. Home is chrome. This Project-path surface has neither a Registry Project
# UID nor a declared ControlSession, so it records the exact refusal and writes
# nothing. ControlSession creation belongs to the dedicated shell bootstrap.
cp "$disc_registry" "$disc_root/registry.before-home"
disc_runtime_before="$(
  disc_tmux list-sessions -F '#{session_id}|#{session_name}|#{@projmux_project_uid}|#{@projmux_session_role}'
  disc_tmux list-windows -a -F '#{session_id}|#{window_id}|#{@projmux_window_uid}'
  disc_tmux list-panes -a -F '#{session_id}|#{window_id}|#{pane_id}|#{@projmux_pane_uid}'
)"
disc_tmux send-keys -t "$disc_driver_pane" "bash '$disc_root/open-candidate.sh' '$disc_root/home' home" Enter
disc_wait_for "Home open" test -s "$disc_root/open-home.rc"
if [[ "$(tr -d '[:space:]' <"$disc_root/open-home.rc")" == "0" ]]; then
  echo "opening undeclared Home unexpectedly succeeded" >&2
  exit 1
fi
if ! grep -Fq "exact Registry Project UID is unavailable; no runtime was created" "$disc_root/open-home.err"; then
  echo "opening undeclared Home lacked the exact fail-closed diagnostic" >&2
  cat "$disc_root/open-home.err" >&2 || true
  exit 1
fi
cmp "$disc_root/registry.before-home" "$disc_registry"
disc_pmx get projects -o uid >"$disc_root/projects-after-home.uid"
if [[ "$(wc -l <"$disc_root/projects-after-home.uid")" != "1" ]]; then
  echo "opening Home registered a Project:" >&2
  cat "$disc_root/projects-after-home.uid" >&2
  exit 1
fi
if grep -Fq "$disc_root/home" "$disc_root/projects-after-home.uid"; then
  echo "the home directory became a Project" >&2
  exit 1
fi
disc_runtime_after="$(
  disc_tmux list-sessions -F '#{session_id}|#{session_name}|#{@projmux_project_uid}|#{@projmux_session_role}'
  disc_tmux list-windows -a -F '#{session_id}|#{window_id}|#{@projmux_window_uid}'
  disc_tmux list-panes -a -F '#{session_id}|#{window_id}|#{pane_id}|#{@projmux_pane_uid}'
)"
if [[ "$disc_runtime_after" != "$disc_runtime_before" ]]; then
  echo "opening undeclared Home changed discovery runtime inventory" >&2
  diff <(printf '%s\n' "$disc_runtime_before") <(printf '%s\n' "$disc_runtime_after") >&2 || true
  exit 1
fi

# 4. The two pin collections. A registered root types itself as the Project uid;
# an unregistered sibling stays a path.
disc_pmx pin project add "$disc_root/work/app" >"$disc_root/pin-managed.out"
smoke_assert_file_contains "$disc_root/pin-managed.out" "pinned: project $disc_project_uid"
disc_pmx pin project add "$disc_root/work/scratch" >"$disc_root/pin-candidate.out"
smoke_assert_file_contains "$disc_root/pin-candidate.out" "pinned: candidate $disc_root/work/scratch"
if [[ "$(cat "$disc_pins")" != "$(printf 'projmux-pins v2\nproject %s\ncandidate %s' "$disc_project_uid" "$disc_root/work/scratch")" ]]; then
  echo "the pin file is not the typed envelope:" >&2
  cat "$disc_pins" >&2
  exit 1
fi
disc_pmx pin project list >"$disc_root/pin-list.out" 2>"$disc_root/pin-list.err"
smoke_assert_file_contains "$disc_root/pin-list.out" "project	uid:$disc_project_uid	$disc_root/work/app"
smoke_assert_file_contains "$disc_root/pin-list.out" "candidate	$disc_root/work/scratch"
disc_pmx pin project list --kind project >"$disc_root/pin-list-managed.out" 2>/dev/null
if grep -Fq "$disc_root/work/scratch" "$disc_root/pin-list-managed.out"; then
  echo "the managed pin listing leaked a candidate pin" >&2
  exit 1
fi

# 5. The managed pin follows the Project through a rebind: same uid, new root,
# candidate pin untouched.
mkdir -p "$disc_root/moved"
disc_pmx rebind project "uid:$disc_project_uid" --root "$disc_root/moved" >"$disc_root/rebind.out"
disc_pmx pin project list >"$disc_root/pin-list-after-rebind.out" 2>/dev/null
smoke_assert_file_contains "$disc_root/pin-list-after-rebind.out" "project	uid:$disc_project_uid	$disc_root/moved"
smoke_assert_file_contains "$disc_root/pin-list-after-rebind.out" "candidate	$disc_root/work/scratch"

# The sibling socket never moved.
disc_other_after="$(disc_other_tmux show-options -gqv @projmux_disc_sentinel):$(disc_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"
if [[ "$disc_other_after" != "$disc_other_before" ]]; then
  echo "discovery authority e2e touched the sibling socket" >&2
  exit 1
fi

exec 9>&-
kill "$disc_client_pid" >/dev/null 2>&1 || true
wait "$disc_client_pid" 2>/dev/null || true
disc_cleanup
trap smoke_cleanup_env EXIT
echo ">> discovery/pin authority e2e passed: socket=$disc_socket path=$disc_socket_path other-socket=$disc_other_socket other-path=$disc_other_socket_path client=$disc_client project=$disc_project_uid siblings=unregistered cleanup=validated-exact-sockets"

# --- exit reconciliation end to end -----------------------------------------
#
# The integration smoke drives the reconciliation through an explicit route. This
# block proves the user-visible property instead: with the generated config
# installed on an exact socket, a managed Agent whose process ends converges to
# the right phase **without any projmux command being run to make it happen**, and
# a whole-server loss followed by a restart converges without starting anything.
exitrec_root="$PROJMUX_SMOKE_WORKDIR/exit-reconciliation"
exitrec_socket="projmux-exitrec-$RANDOM-$$"
exitrec_other_socket="projmux-exitrec-other-$RANDOM-$$"
exitrec_session="work-alpha"
mkdir -p \
  "$exitrec_root/home" \
  "$exitrec_root/config" \
  "$exitrec_root/state" \
  "$exitrec_root/runtime" \
  "$exitrec_root/tmux" \
  "$exitrec_root/bin" \
  "$exitrec_root/shim" \
  "$exitrec_root/work/alpha"
chmod 0700 "$exitrec_root/runtime" "$exitrec_root/tmux"

# The container's default shell exits immediately in a detached pane, which would
# make every Pane disappear before the fixture could name it. A persistent stub
# keeps the graph inspectable without giving any Pane a stored command.
exitrec_shell="$exitrec_root/shim/persistent-shell"
cat >"$exitrec_shell" <<'EXITREC_SHELL_STUB'
#!/usr/bin/env bash
exec sleep 600
EXITREC_SHELL_STUB
chmod 0755 "$exitrec_shell"

# The provider stub is a real process that ends the way each case asks for.
# Nothing about a provider's own protocol participates: the classification comes
# from the wait status.
for exitrec_provider in claude codex agy; do
  cat >"$exitrec_root/bin/$exitrec_provider" <<PROVIDER_STUB
#!/usr/bin/env bash
exec sh -c "\$(cat $(printf %q "$exitrec_root/stub-script"))"
PROVIDER_STUB
  chmod 0755 "$exitrec_root/bin/$exitrec_provider"
done
printf '%s\n' 'sleep 600' >"$exitrec_root/stub-script"

exitrec_tmux() {
  env -u TMUX -u TMUX_PANE \
    HOME="$exitrec_root/home" \
    XDG_CONFIG_HOME="$exitrec_root/config" \
    XDG_STATE_HOME="$exitrec_root/state" \
    XDG_RUNTIME_DIR="$exitrec_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$exitrec_root/work" \
    TMUX_TMPDIR="$exitrec_root/tmux" \
    PATH="$exitrec_root/bin:$PATH" \
    SHELL="$exitrec_shell" \
    tmux -L "$exitrec_socket" "$@"
}

exitrec_other_tmux() {
  env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$exitrec_root/tmux" \
    tmux -L "$exitrec_other_socket" "$@"
}

exitrec_pmx() {
  env -u TMUX -u TMUX_PANE \
    HOME="$exitrec_root/home" \
    XDG_CONFIG_HOME="$exitrec_root/config" \
    XDG_STATE_HOME="$exitrec_root/state" \
    XDG_RUNTIME_DIR="$exitrec_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$exitrec_root/work" \
    TMUX_TMPDIR="$exitrec_root/tmux" \
    PATH="$exitrec_root/bin:$PATH" \
    SHELL="$exitrec_shell" \
    "$bin" "$@"
}

# Creation routes through the client's inherited socket rather than a --socket
# flag, so the live half names the exact synthetic client socket instead of
# falling back to the default app socket.
exitrec_live_pmx() {
  env \
    HOME="$exitrec_root/home" \
    XDG_CONFIG_HOME="$exitrec_root/config" \
    XDG_STATE_HOME="$exitrec_root/state" \
    XDG_RUNTIME_DIR="$exitrec_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$exitrec_root/work" \
    TMUX_TMPDIR="$exitrec_root/tmux" \
    TMUX="$exitrec_socket_path,$exitrec_socket_pid,0" \
    TMUX_PANE="$exitrec_create_anchor_pane" \
    PATH="$exitrec_root/bin:$PATH" \
    SHELL="$exitrec_shell" \
    "$bin" "$@"
}

exitrec_tmux new-session -d -s "$exitrec_session" -n main -c "$exitrec_root/work/alpha" sleep 600
exitrec_tmux set-option -t "$exitrec_session" -q @projmux_project_path "$exitrec_root/work/alpha"
exitrec_other_tmux new-session -d -s untouched sleep 600
exitrec_other_tmux set-option -gq @projmux_exitrec_sentinel unchanged

exitrec_other_before="$(exitrec_other_tmux show-options -gqv @projmux_exitrec_sentinel):$(exitrec_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"
exitrec_socket_path="$(exitrec_tmux display-message -p -t "$exitrec_session" '#{socket_path}')"
exitrec_socket_pid="$(exitrec_tmux display-message -p -t "$exitrec_session" '#{pid}')"
exitrec_other_socket_path="$(exitrec_other_tmux display-message -p -t untouched '#{socket_path}')"
for exitrec_candidate in "$exitrec_socket_path" "$exitrec_other_socket_path"; do
  case "$exitrec_candidate" in
    "$exitrec_root"/*) ;;
    *)
      echo "exit reconciliation e2e socket escaped the smoke root: $exitrec_candidate" >&2
      exit 1
      ;;
  esac
done

exitrec_cleanup() {
  local actual
  for actual in "$exitrec_socket_path" "$exitrec_other_socket_path"; do
    [[ -n "$actual" ]] || continue
    case "$actual" in
      "$exitrec_root"/*)
        env -u TMUX -u TMUX_PANE tmux -S "$actual" kill-server >/dev/null 2>&1 || true
        ;;
      *)
        echo "refusing exit reconciliation e2e cleanup outside the smoke root: $actual" >&2
        ;;
    esac
  done
}
trap 'exitrec_cleanup; smoke_cleanup_env' EXIT

# Install the generated config on the exact socket. This is what puts the
# `pane-exited`, `after-kill-pane`, and `window-unlinked` hooks in place; every
# hook-only convergence below is driven by them and by nothing this script runs.
exitrec_pmx reconcile resources --socket "$exitrec_socket" --dry-run -o json >"$exitrec_root/d2.json"
smoke_assert_file_contains "$exitrec_root/d2.json" '"outcome": "no-op"'
if [[ -e "$exitrec_root/state/projmux/metadata/registry.json" ]]; then
  echo "exit reconciliation D2 dry-run created a Registry" >&2
  exit 1
fi
exitrec_pmx create project --root "$exitrec_root/work/alpha" --name alpha >"$exitrec_root/register-alpha.out"
exitrec_pmx internal tmux apply --bin "$bin" \
  --config "$exitrec_root/config/projmux/tmux.conf" --socket "$exitrec_socket" \
  >"$exitrec_root/apply.out"
e2e_bounded_reconcile_to_noop --allow-initial-noop "$exitrec_root/import" \
  exitrec_pmx reconcile resources --socket "$exitrec_socket" -o json
exitrec_project_uid="$(exitrec_tmux show-options -qv -t "$exitrec_session" @projmux_project_uid)"
if [[ -z "$exitrec_project_uid" ]]; then
  echo "exit reconciliation e2e explicit authority left the Project uid empty" >&2
  exit 1
fi
exitrec_window_uid="$(exitrec_tmux show-options -wqv -t "$exitrec_session" @projmux_window_uid)"
exitrec_anchor_rows="$(exitrec_tmux list-panes -s -t "$exitrec_session" -F '#{pane_id}|#{@projmux_pane_uid}')"
if [[ "$(printf '%s\n' "$exitrec_anchor_rows" | grep -c .)" != "1" ]]; then
  echo "exit reconciliation requires exactly one initial managed anchor: $exitrec_anchor_rows" >&2
  exit 1
fi
IFS='|' read -r exitrec_create_anchor_pane exitrec_shell_pane <<<"$exitrec_anchor_rows"
if [[ ! "$exitrec_create_anchor_pane" =~ ^%[0-9]+$ ]] || \
  [[ -z "$exitrec_window_uid" ]] || [[ -z "$exitrec_shell_pane" ]]; then
  echo "exit reconciliation e2e canonical Window/shell has no exact Registry identity" >&2
  exit 1
fi
exitrec_anchor_receipt="$(
  exitrec_tmux display-message -p -t "$exitrec_create_anchor_pane" \
    '#{socket_path}|#{pid}|#{session_id}|#{window_id}|#{pane_id}|#{@projmux_project_uid}|#{@projmux_window_uid}|#{@projmux_pane_uid}|receipt-end'
)"
IFS='|' read -r exitrec_anchor_socket exitrec_anchor_pid exitrec_anchor_session exitrec_anchor_window \
  exitrec_anchor_pane exitrec_anchor_project_uid exitrec_anchor_window_uid exitrec_anchor_pane_uid \
  exitrec_anchor_end <<<"$exitrec_anchor_receipt"
if [[ "$exitrec_anchor_socket" != "$exitrec_socket_path" ]] || \
  [[ "$exitrec_anchor_pid" != "$exitrec_socket_pid" ]] || \
  [[ ! "$exitrec_anchor_session" =~ ^\$[0-9]+$ ]] || \
  [[ ! "$exitrec_anchor_window" =~ ^@[0-9]+$ ]] || \
  [[ "$exitrec_anchor_pane" != "$exitrec_create_anchor_pane" ]] || \
  [[ "$exitrec_anchor_project_uid" != "$exitrec_project_uid" ]] || \
  [[ "$exitrec_anchor_window_uid" != "$exitrec_window_uid" ]] || \
  [[ "$exitrec_anchor_pane_uid" != "$exitrec_shell_pane" ]] || \
  [[ "$exitrec_anchor_end" != "receipt-end" ]]; then
  echo "exit reconciliation create lacks exact managed anchor containment: $exitrec_anchor_receipt" >&2
  exit 1
fi
if ! exitrec_tmux show-hooks -g | grep -q "internal tmux converge --socket-path"; then
  echo "the generated config installed no controller trigger, so hook-driven convergence cannot be observed" >&2
  exitrec_tmux show-hooks -g >&2
  exit 1
fi
# All five lifecycle hooks reach the one entrypoint, and none of them retains a
# route of its own. This is the trigger inventory as the live server holds it,
# which is a stronger statement than the same assertion over rendered text: it
# also proves `apply` sourced them.
# `pane-exited` and `window-unlinked` are window-scoped hooks in current tmux, so
# `show-hooks -g` may omit them while listing `after-kill-pane`. Reading both
# tables makes this the whole live trigger inventory rather than the half that
# happens to be server-global.
exitrec_hooks="$(exitrec_tmux show-hooks -g; exitrec_tmux show-hooks -gw)"
for exitrec_hook in pane-exited pane-died after-kill-pane window-unlinked after-new-window after-split-window; do
  if ! printf '%s\n' "$exitrec_hooks" | grep -q "^$exitrec_hook.*internal tmux converge --socket-path"; then
    echo "hook $exitrec_hook does not reach the controller entrypoint" >&2
    printf '%s\n' "$exitrec_hooks" >&2
    exit 1
  fi
done
for exitrec_retired in release-dead-agent-panes reconcile-bindings; do
  if printf '%s\n' "$exitrec_hooks" | grep -q "$exitrec_retired"; then
    echo "a live hook still invokes the retired $exitrec_retired route" >&2
    printf '%s\n' "$exitrec_hooks" >&2
    exit 1
  fi
done

# Read helpers. One document per observation, parsed out of a file, so a failed
# read is a visible empty document rather than a swallowed pipeline status.
exitrec_doc() {
  exitrec_pmx describe "$1" "uid:$2" -o json >"$exitrec_root/doc.json" 2>"$exitrec_root/doc.err" || true
}

exitrec_field() {
  sed -n "s/^[[:space:]]*\"$1\": \(.*\)$/\1/p" "$exitrec_root/doc.json" \
    | head -n 1 | sed 's/,$//; s/^"//; s/"$//'
}

exitrec_termination_field() {
  sed -n '/"lastTermination"/,$p' "$exitrec_root/doc.json" \
    | sed -n "s/^[[:space:]]*\"$1\": \(.*\)$/\1/p" \
    | head -n 1 | sed 's/,$//; s/^"//; s/"$//'
}

# The hook is backgrounded by the generated config, so the convergence it drives
# is asynchronous. Polling `describe` is what makes this an end-to-end assertion:
# `describe` is a read verb and reconciles nothing, so a phase that appears here
# was written by the hook.
exitrec_await_phase() {
  local kind="$1" uid="$2" want="$3"
  for _ in $(seq 1 150); do
    exitrec_doc "$kind" "$uid"
    if [[ "$(exitrec_field phase)" == "$want" ]]; then
      return 0
    fi
    sleep 0.2
  done
  echo "$kind $uid never reached $want; the pane-exit hook did not converge" >&2
  cat "$exitrec_root/doc.json" >&2
  exit 1
}

exitrec_await_absent() {
  local kind="$1" uid="$2"
  for _ in $(seq 1 150); do
    exitrec_doc "$kind" "$uid"
    if [[ ! -s "$exitrec_root/doc.json" ]]; then
      return 0
    fi
    sleep 0.2
  done
  echo "$kind $uid survived the exact clean pane-exit cascade" >&2
  cat "$exitrec_root/doc.json" >&2
  exit 1
}

# 1. A provider that exits non-zero converges to Failed on the hook alone.
printf 'sleep 0.5\n%s\n' 'exit 42' >"$exitrec_root/stub-script"
exitrec_failed_agent="$(exitrec_live_pmx create agent --provider codex \
  --project "uid:$exitrec_project_uid" -o uid)"
if [[ -z "$exitrec_failed_agent" ]]; then
  echo "exit reconciliation e2e created no Agent" >&2
  exit 1
fi
exitrec_await_phase agent "$exitrec_failed_agent" Failed
if [[ "$(exitrec_termination_field classification)" != "abnormal" ]]; then
  echo "the hook classified an exit 42 as $(exitrec_termination_field classification), want abnormal" >&2
  cat "$exitrec_root/doc.json" >&2
  exit 1
fi
if [[ -n "$(exitrec_field paneRef)" ]]; then
  echo "the hook left the failed Agent bound to its dead pane" >&2
  exit 1
fi
echo ">> exit reconciliation e2e hook-driven failure agent=$exitrec_failed_agent phase=Failed class=abnormal"

# 2. A provider that exits 0 is a qualifying exact pane-exited teardown. Its
# Pane row is released while the Agent identity remains Offline; the durable
# supervisor row remains in the termination journal. The Window, its shell,
# and the failed sibling Agent remain untouched.
printf 'sleep 1\n%s\n' 'exit 0' >"$exitrec_root/stub-script"
exitrec_clean_agent="$(exitrec_live_pmx create agent --provider claude \
  --project "uid:$exitrec_project_uid" -o uid)"
exitrec_doc agent "$exitrec_clean_agent"
exitrec_clean_pane="$(exitrec_field paneRef)"
if [[ -z "$exitrec_clean_pane" ]]; then
  echo "clean provider Agent $exitrec_clean_agent exposed no current Pane before exit" >&2
  exit 1
fi
exitrec_await_phase agent "$exitrec_clean_agent" Offline
exitrec_await_absent pane "$exitrec_clean_pane"
exitrec_doc agent "$exitrec_clean_agent"
if [[ -n "$(exitrec_field paneRef)" ]]; then
  echo "clean provider Agent $exitrec_clean_agent remained bound to its released Pane" >&2
  exit 1
fi
if ! awk -v pane="\"paneUID\":\"$exitrec_clean_pane\"" -v classification='"classification":"normal"' \
  -v source='"source":"supervisor"' \
  'index($0, pane) && index($0, classification) && index($0, source) { found = 1 } END { exit !found }' \
  "$exitrec_root/state/projmux/termination-receipts.jsonl"; then
  echo "clean provider $exitrec_clean_agent lost its pre-delete normal/supervisor journal evidence" >&2
  exit 1
fi
exitrec_doc agent "$exitrec_failed_agent"
if [[ "$(exitrec_field phase)" != "Failed" ]]; then
  echo "one-of-many clean exit changed failed sibling Agent $exitrec_failed_agent" >&2
  cat "$exitrec_root/doc.json" >&2 || true
  cat "$exitrec_root/doc.err" >&2 || true
  exit 1
fi
if ! exitrec_pmx describe window "uid:$exitrec_window_uid" -o json >"$exitrec_root/window-after-clean.json"; then
  echo "one-of-many clean exit deleted the owning Window" >&2
  exit 1
fi
if ! exitrec_pmx describe pane "uid:$exitrec_shell_pane" -o json >"$exitrec_root/shell-after-clean.json" ||
  ! exitrec_tmux list-panes -a -F '#{@projmux_pane_uid}' | grep -qx "$exitrec_shell_pane"; then
  echo "one-of-many clean exit deleted the sibling shell Pane/runtime" >&2
  exit 1
fi
echo ">> exit reconciliation e2e hook-driven clean exit agent=$exitrec_clean_agent phase=Offline pane=$exitrec_clean_pane registry=released journal=preserved siblings=preserved"

# 3. A second Project on the same exact server reaches its last-descendant
# boundary. Canonical Pane delete first leaves one Agent as the Window anchor.
# When that owner exits cleanly, pane-exited creates and mirrors a replacement
# shell before committing the Pane release. The Project and exact Window
# uid/name survive, and a new worker can be created in that same Window.
mkdir -p "$exitrec_root/work/beta"
exitrec_tmux new-session -d -s work-beta -n main -c "$exitrec_root/work/beta" sleep 600
exitrec_tmux set-option -t work-beta -q @projmux_project_path "$exitrec_root/work/beta"
exitrec_beta_project_uid="$(exitrec_pmx create project --root "$exitrec_root/work/beta" --name beta -o uid)"
e2e_bounded_reconcile_to_noop "$exitrec_root/import-beta" \
  exitrec_pmx reconcile resources --socket "$exitrec_socket" -o json
exitrec_beta_window_uid="$(exitrec_tmux show-options -wqv -t work-beta @projmux_window_uid)"
exitrec_beta_window_name="$(exitrec_tmux display-message -p -t work-beta '#{window_name}')"
exitrec_beta_initial_runtime="$(exitrec_tmux display-message -p -t work-beta '#{pane_id}')"
exitrec_beta_initial_pane_uid="$(exitrec_tmux show-options -pqv -t "$exitrec_beta_initial_runtime" @projmux_pane_uid)"
printf 'sleep 3\n%s\n' 'exit 0' >"$exitrec_root/stub-script"
exitrec_beta_agent_uid="$(exitrec_live_pmx create agent --provider antigravity \
  --project "uid:$exitrec_beta_project_uid" --window "uid:$exitrec_beta_window_uid" -o uid)"
exitrec_doc agent "$exitrec_beta_agent_uid"
exitrec_beta_agent_pane_uid="$(exitrec_field paneRef)"
for _ in $(seq 1 100); do
  if [[ "$(exitrec_tmux list-panes -t work-beta -F '#{pane_id}' | grep -c . || true)" == "2" ]]; then
    break
  fi
  sleep 0.05
done
if [[ "$(exitrec_tmux list-panes -t work-beta -F '#{pane_id}' | grep -c . || true)" != "2" ]]; then
  echo "Phase 3 e2e managed last Pane never materialized" >&2
  exit 1
fi
exitrec_live_pmx delete pane "uid:$exitrec_beta_initial_pane_uid" --yes >"$exitrec_root/delete-beta-shell.out"
if [[ "$(exitrec_tmux list-panes -t work-beta -F '#{@projmux_pane_uid}' | grep -c . || true)" != "1" ]] ||
  ! exitrec_tmux list-panes -t work-beta -F '#{@projmux_pane_uid}' | grep -Fxq "$exitrec_beta_agent_pane_uid"; then
  echo "Phase 3 e2e canonical shell delete did not leave the Agent as sole Window descendant" >&2
  exit 1
fi
exitrec_await_phase agent "$exitrec_beta_agent_uid" Offline
exitrec_await_absent pane "$exitrec_beta_agent_pane_uid"
for _ in $(seq 1 150); do
  exitrec_beta_live_rows="$(exitrec_tmux list-panes -t work-beta -F '#{@projmux_window_uid}|#{window_name}|#{@projmux_pane_uid}' 2>/dev/null || true)"
  if [[ "$(printf '%s\n' "$exitrec_beta_live_rows" | grep -c . || true)" == "1" ]] &&
    [[ "$exitrec_beta_live_rows" == "$exitrec_beta_window_uid|$exitrec_beta_window_name|"* ]]; then
    break
  fi
  sleep 0.2
done
exitrec_beta_replacement_uid="${exitrec_beta_live_rows##*|}"
if [[ -z "$exitrec_beta_replacement_uid" ]] || [[ "$exitrec_beta_replacement_uid" == "$exitrec_beta_agent_pane_uid" ]]; then
  echo "Phase 3 e2e produced no distinct live replacement shell: $exitrec_beta_live_rows" >&2
  exit 1
fi
if ! exitrec_pmx describe project "uid:$exitrec_beta_project_uid" -o json >"$exitrec_root/beta-after-owner-exit.json" ||
  ! exitrec_pmx describe window "uid:$exitrec_beta_window_uid" -o json >"$exitrec_root/beta-window-after-owner-exit.json" ||
  ! exitrec_pmx describe pane "uid:$exitrec_beta_replacement_uid" -o json >"$exitrec_root/beta-replacement.json"; then
  echo "Phase 3 e2e Registry did not retain the beta Project/Window/replacement shell" >&2
  exit 1
fi
printf '%s\n' 'sleep 600' >"$exitrec_root/stub-script"
exitrec_beta_new_worker="$(exitrec_live_pmx create agent --provider claude \
  --project "uid:$exitrec_beta_project_uid" --window "uid:$exitrec_beta_window_uid" -o uid)"
exitrec_doc agent "$exitrec_beta_new_worker"
exitrec_beta_new_worker_pane="$(exitrec_field paneRef)"
if [[ -z "$exitrec_beta_new_worker_pane" ]] ||
  ! exitrec_tmux list-panes -t work-beta -F '#{@projmux_pane_uid}' | grep -Fxq "$exitrec_beta_new_worker_pane"; then
  echo "Phase 3 e2e could not create a new worker in retained Window $exitrec_beta_window_uid" >&2
  exit 1
fi
if ! exitrec_pmx describe project "uid:$exitrec_project_uid" -o json >"$exitrec_root/alpha-after-beta.json" ||
  ! exitrec_pmx describe window "uid:$exitrec_window_uid" -o json >"$exitrec_root/alpha-window-after-beta.json" ||
  ! exitrec_tmux list-panes -a -F '#{@projmux_pane_uid}' | grep -qx "$exitrec_shell_pane"; then
  echo "Phase 3 e2e owner-exit retention changed its same-socket sibling Project" >&2
  exit 1
fi
exitrec_other_after="$(exitrec_other_tmux show-options -gqv @projmux_exitrec_sentinel):$(exitrec_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"
if [[ "$exitrec_other_after" != "$exitrec_other_before" ]]; then
  echo "Phase 3 e2e owner-exit retention touched its sibling socket" >&2
  exit 1
fi
echo ">> exit reconciliation e2e owner-exit retained project=$exitrec_beta_project_uid window=$exitrec_beta_window_uid name=$exitrec_beta_window_name old-agent=$exitrec_beta_agent_uid replacement=$exitrec_beta_replacement_uid new-worker=$exitrec_beta_new_worker pane=$exitrec_beta_new_worker_pane siblings=preserved"

# 4. The read surfaces project what the hook stored, and write nothing.
exitrec_registry="$exitrec_root/state/projmux/metadata/registry.json"
exitrec_settle_registry() {
  local previous="" current stable_samples=0
  for _ in $(seq 1 300); do
    current="$(sha256sum "$exitrec_registry" | cut -d' ' -f1)"
    if [[ -n "$previous" && "$current" == "$previous" ]]; then
      stable_samples=$((stable_samples + 1))
      if [[ "$stable_samples" == "10" ]]; then
        return 0
      fi
    else
      stable_samples=0
    fi
    previous="$current"
    sleep 0.05
  done
  echo "the Registry never settled after the pane-exit hooks fired" >&2
  exit 1
}
exitrec_settle_registry
exitrec_read_before="$(sha256sum "$exitrec_registry" | cut -d' ' -f1)"
exitrec_pmx get agents --project "uid:$exitrec_project_uid" >"$exitrec_root/get-agents.txt"
exitrec_pmx get panes --project "uid:$exitrec_project_uid" >"$exitrec_root/get-panes.txt"
exitrec_pmx describe agent "uid:$exitrec_failed_agent" >"$exitrec_root/describe-agent.txt"
smoke_assert_file_contains "$exitrec_root/get-agents.txt" "TERMINATION"
smoke_assert_file_contains "$exitrec_root/get-agents.txt" "abnormal/supervisor exit=42"
smoke_assert_file_contains "$exitrec_root/get-panes.txt" "TERMINATION"
smoke_assert_file_contains "$exitrec_root/describe-agent.txt" "TerminationSource:"
if [[ "$(sha256sum "$exitrec_registry" | cut -d' ' -f1)" != "$exitrec_read_before" ]]; then
  echo "a read surface wrote to the Registry" >&2
  exit 1
fi
echo ">> exit reconciliation e2e read projection write-free"

# 5. Whole-server loss, then restart on the same socket. Receipt-free recovery
# ordering is explicitly outside this slice; the stable contract here is that no
# evidence invents normal/intentional, the Pane row survives, and reconciliation
# starts nothing because observation is not activation authority.
exitrec_shell_pane="$(exitrec_live_pmx create pane --project "uid:$exitrec_project_uid" -o uid -- sleep 600)"
exitrec_settle_registry
env -u TMUX -u TMUX_PANE tmux -S "$exitrec_socket_path" kill-server >/dev/null 2>&1 || true
exitrec_settle_registry
exitrec_doc pane "$exitrec_shell_pane"
if [[ "$(exitrec_termination_field source)" == "reconcile" ]]; then
  echo "reconciler evidence was filed while the server was unreadable" >&2
  cat "$exitrec_root/doc.json" >&2
  exit 1
fi
exitrec_tmux new-session -d -s exitrec-restarted -n main sleep 600
exitrec_restarted_socket_path="$(exitrec_tmux display-message -p -t exitrec-restarted '#{socket_path}')"
exitrec_restarted_socket_pid="$(exitrec_tmux display-message -p -t exitrec-restarted '#{pid}')"
exitrec_restarted_pane="$(exitrec_tmux list-panes -s -t exitrec-restarted -F '#{pane_id}')"
if [[ ! "$exitrec_restarted_pane" =~ ^%[0-9]+$ ]]; then
  echo "exit reconciliation restart lacks exactly one raw Pane: $exitrec_restarted_pane" >&2
  exit 1
fi
exitrec_restarted_receipt="$(
  exitrec_tmux display-message -p -t "$exitrec_restarted_pane" \
    '#{socket_path}|#{pid}|#{session_id}|#{session_name}|#{window_id}|#{pane_id}|#{@projmux_app}|#{@projmux_socket_name}|receipt-end'
)"
IFS='|' read -r exitrec_restarted_socket exitrec_restarted_pid exitrec_restarted_session \
  exitrec_restarted_session_name exitrec_restarted_window exitrec_restarted_observed_pane \
  exitrec_restarted_app_marker exitrec_restarted_logical_marker exitrec_restarted_end \
  <<<"$exitrec_restarted_receipt"
if [[ "$exitrec_restarted_socket_path" != "$exitrec_socket_path" ]] || \
  [[ "$exitrec_restarted_socket" != "$exitrec_restarted_socket_path" ]] || \
  [[ "$exitrec_restarted_pid" != "$exitrec_restarted_socket_pid" ]] || \
  [[ ! "$exitrec_restarted_session" =~ ^\$[0-9]+$ ]] || \
  [[ "$exitrec_restarted_session_name" != "exitrec-restarted" ]] || \
  [[ ! "$exitrec_restarted_window" =~ ^@[0-9]+$ ]] || \
  [[ "$exitrec_restarted_observed_pane" != "$exitrec_restarted_pane" ]] || \
  [[ -n "$exitrec_restarted_app_marker" ]] || \
  [[ -n "$exitrec_restarted_logical_marker" ]] || \
  [[ "$exitrec_restarted_end" != "receipt-end" ]]; then
  echo "exit reconciliation restart lacks exact standalone authority: $exitrec_restarted_receipt" >&2
  exit 1
fi
exitrec_pmx reconcile resources --socket-path "$exitrec_restarted_socket_path" -o json >"$exitrec_root/after-restart.json"
exitrec_doc pane "$exitrec_shell_pane"
if [[ -z "$(cat "$exitrec_root/doc.json")" ]]; then
  echo "the restart deleted the logical shell Pane $exitrec_shell_pane" >&2
  exit 1
fi
case "$(exitrec_termination_field classification)" in
  ""|killed|unknown) ;;
  *)
    echo "the restarted host classified the lost pane as '$(exitrec_termination_field classification)', want empty, killed, or unknown" >&2
    cat "$exitrec_root/doc.json" >&2
    exit 1
    ;;
esac
if [[ "$(exitrec_tmux list-panes -a -F '#{@projmux_pane_uid}' | grep -c . || true)" != "0" ]]; then
  echo "the reconciliation after a restart materialized managed panes:" >&2
  exitrec_tmux list-panes -a -F '#{pane_id} #{@projmux_pane_uid}' >&2
  exit 1
fi
echo ">> exit reconciliation e2e restart converged pane=$exitrec_shell_pane class=$(exitrec_termination_field classification) no-autostart"

# 6. The sibling socket was never read or written.
exitrec_other_after="$(exitrec_other_tmux show-options -gqv @projmux_exitrec_sentinel):$(exitrec_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"
if [[ "$exitrec_other_after" != "$exitrec_other_before" ]]; then
  echo "exit reconciliation e2e touched the sibling socket: $exitrec_other_after" >&2
  exit 1
fi

exitrec_cleanup
trap smoke_cleanup_env EXIT
echo ">> exit reconciliation e2e passed: socket=$exitrec_socket path=$exitrec_socket_path other-path=$exitrec_other_socket_path project=$exitrec_project_uid cleanup=validated-exact-sockets"

# Declarative contract stabilization Phase 5: audit the generated
# MouseDown3Pane binding, then select the same managed actions through a real
# tmux display-menu on an attached client. Every command in this block strips
# the caller's tmux identity and names a run-local TMUX_TMPDIR plus one unique
# socket. Cleanup uses only the queried exact socket path, after proving that
# path is inside this smoke root.
menu_root="$PROJMUX_SMOKE_WORKDIR/pane-menu"
menu_socket="pane-menu-e2e-$$-$RANDOM"
menu_session="work-alpha"
menu_config="$menu_root/config/projmux/tmux.conf"
menu_client_log="$menu_root/client.log"
menu_client_input="$menu_root/client.in"
mkdir -p "$menu_root"/{home,cache,config,runtime,state,tmux,work/alpha}
chmod 0700 "$menu_root/runtime"

menu_env=(
  env -u TMUX -u TMUX_PANE -u PROJMUX_SMOKE_TMUX_SOCKET
  HOME="$menu_root/home"
  XDG_CACHE_HOME="$menu_root/cache"
  XDG_CONFIG_HOME="$menu_root/config"
  XDG_RUNTIME_DIR="$menu_root/runtime"
  XDG_STATE_HOME="$menu_root/state"
  TMUX_TMPDIR="$menu_root/tmux"
  PROJMUX_MANAGED_ROOTS="$menu_root/work"
  SHELL=/bin/bash
  PATH="$PATH"
)
menu_tmux() { "${menu_env[@]}" tmux -L "$menu_socket" "$@"; }
menu_pmx() { "${menu_env[@]}" "$bin" "$@"; }

menu_client_pid=""
menu_socket_path=""
menu_cleanup_done=0
menu_cleanup() {
  if [[ "$menu_cleanup_done" == "1" ]]; then
    return
  fi
  menu_cleanup_done=1
  if [[ -n "$menu_socket_path" ]]; then
    case "$menu_socket_path" in
      "$menu_root"/tmux/*) ;;
      *)
        echo "refusing pane-menu cleanup outside smoke root: $menu_socket_path" >&2
        return 1
        ;;
    esac
    "${menu_env[@]}" tmux -S "$menu_socket_path" kill-server >/dev/null 2>&1 || true
  fi
  if [[ -n "$menu_client_pid" ]]; then
    wait "$menu_client_pid" 2>/dev/null || true
  fi
}
trap 'menu_cleanup; smoke_cleanup_env' EXIT

menu_tmux new-session -d -s "$menu_session" -c "$menu_root/work/alpha" sleep 600
menu_origin_pane="$(menu_tmux display-message -p -t "$menu_session:0.0" '#{pane_id}')"
menu_socket_path="$(menu_tmux display-message -p -t "$menu_origin_pane" '#{socket_path}')"
case "$menu_socket_path" in
  "$menu_root"/tmux/*) ;;
  *)
    echo "pane-menu socket escaped smoke root: $menu_socket_path" >&2
    exit 1
    ;;
esac

menu_pmx reconcile resources --socket "$menu_socket" --dry-run -o json >"$menu_root/d2.json"
smoke_assert_file_contains "$menu_root/d2.json" '"outcome": "no-op"'
if [[ -e "$menu_root/state/projmux/metadata/registry.json" ]]; then
  echo "pane-menu D2 dry-run created a Registry" >&2
  exit 1
fi
menu_project_uid="$(menu_pmx create project --root "$menu_root/work/alpha" -o uid)"
menu_pmx config apply --bin "$bin" --config "$menu_config" --socket "$menu_socket" >"$menu_root/apply.out"
e2e_bounded_reconcile_to_noop --allow-initial-noop "$menu_root/reconcile" \
  menu_pmx reconcile resources --socket "$menu_socket" -o json
if [[ -z "$(menu_tmux show-options -pqv -t "$menu_origin_pane" @projmux_pane_uid)" ]]; then
  echo "pane-menu fixture origin was not reconciled as a managed Pane" >&2
  exit 1
fi
menu_binding="$(menu_tmux list-keys -T root MouseDown3Pane)"
smoke_assert_output_contains "$menu_binding" "internal tmux pane-menu --client"
for menu_raw_verb in split-window kill-pane respawn-pane; do
  if [[ "$menu_binding" == *"$menu_raw_verb"* ]]; then
    echo "generated MouseDown3Pane binding retained raw $menu_raw_verb" >&2
    exit 1
  fi
done
if [[ "$menu_binding" == *Respawn* ]]; then
  echo "generated MouseDown3Pane binding retained Respawn menu text" >&2
  exit 1
fi
# The generated client-attached welcome popup is asynchronous and would race
# this fixture for the first mouse event. It is outside this menu-route slice;
# keep the generated MouseDown3Pane binding exact and suppress only that popup.
menu_tmux set-hook -gu client-attached

mkfifo "$menu_client_input"
exec 6<>"$menu_client_input"
TERM=xterm-256color script -qefc \
  "TERM=xterm-256color env -u TMUX -u TMUX_PANE -u PROJMUX_SMOKE_TMUX_SOCKET HOME='$menu_root/home' XDG_CACHE_HOME='$menu_root/cache' XDG_CONFIG_HOME='$menu_root/config' XDG_RUNTIME_DIR='$menu_root/runtime' XDG_STATE_HOME='$menu_root/state' TMUX_TMPDIR='$menu_root/tmux' PROJMUX_MANAGED_ROOTS='$menu_root/work' SHELL=/bin/bash PATH='$PATH' tmux -S '$menu_socket_path' attach-session -t '$menu_session'" \
  "$menu_client_log" <"$menu_client_input" >/dev/null 2>&1 &
menu_client_pid=$!
smoke_wait_for "attached pane-menu tmux client" sh -c \
  "test -n \"\$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$menu_root/tmux' tmux -L '$menu_socket' list-clients -F '#{client_name}' 2>/dev/null | head -n 1)\""
menu_client="$(menu_tmux list-clients -F '#{client_name}' | head -n 1)"
menu_tmux switch-client -c "$menu_client" -t "$menu_origin_pane"
if [[ "$(menu_tmux display-message -p -c "$menu_client" '#{pane_id}')" != "$menu_origin_pane" ]] ||
  [[ "$(menu_tmux display-message -p -c "$menu_client" '#{socket_path}')" != "$menu_socket_path" ]]; then
  echo "pane-menu client did not attach to the exact origin pane and socket" >&2
  exit 1
fi

menu_select_item() {
  local pane="$1"
  local key="$2"
  local menu_display_pid menu_log_offset
  menu_log_offset="$(stat -c %s "$menu_client_log")"
  menu_tmux display-menu -c "$menu_client" -T '#[align=centre]managed pane menu e2e' -t "$pane" -x 1 -y 1 \
    "Horizontal Split" h "run-shell \"'$bin' internal tmux pane-menu --client '$menu_client' split-right '$pane'\"" \
    "Vertical Split" v "run-shell \"'$bin' internal tmux pane-menu --client '$menu_client' split-down '$pane'\"" \
    "Kill" X "run-shell \"'$bin' internal tmux pane-menu --client '$menu_client' kill '$pane'\"" &
  menu_display_pid=$!
  smoke_wait_for "MouseDown3Pane menu for $pane" sh -c \
    "tail -c +$((menu_log_offset + 1)) '$menu_client_log' | grep -aFq 'managed pane menu e2e'"
  printf '%s' "$key" >&6
  wait "$menu_display_pid"
}

menu_wait_for_pane_count() {
  [[ "$(menu_tmux list-panes -t "$menu_session:0" -F '#{pane_id}' | wc -l)" == "$1" ]]
}
menu_new_pane_except() {
  local excluded="$1"
  menu_tmux list-panes -t "$menu_session:0" -F '#{pane_id}' | grep -Fvx "$excluded" | tail -n 1
}
menu_pane_is_managed() {
  local pane="$1"
  local uid
  uid="$(menu_tmux show-options -pqv -t "$pane" @projmux_pane_uid 2>/dev/null || true)"
  [[ -n "$uid" ]] && menu_pmx get panes -o uid | grep -Fxq "$uid"
}
menu_delete_converged() {
  local uid="$1"
  local log_offset="$2"
  local expected_pane_count="$3"
  local registry_uids pane_count
  tail -c "+$((log_offset + 1))" "$menu_client_log" | grep -aFq 'delete pane: deleting 1 pane' || return 1
  registry_uids="$(menu_pmx get panes -o uid)" || return 1
  if printf '%s\n' "$registry_uids" | grep -Fxq "$uid"; then
    return 1
  fi
  pane_count="$(menu_tmux list-panes -t "$menu_session:0" -F '#{pane_id}' | wc -l)" || return 1
  [[ "$pane_count" == "$expected_pane_count" ]]
}

# Horizontal Split: a real right-click menu selection reaches managed create.
menu_select_item "$menu_origin_pane" h
smoke_wait_for "managed Horizontal Split" menu_wait_for_pane_count 2
menu_horizontal_pane="$(menu_new_pane_except "$menu_origin_pane")"
smoke_wait_for "Horizontal Split Registry identity" menu_pane_is_managed "$menu_horizontal_pane"
menu_horizontal_uid="$(menu_tmux show-options -pqv -t "$menu_horizontal_pane" @projmux_pane_uid)"
if [[ -z "$menu_horizontal_uid" ]] || ! menu_pmx get panes -o uid | grep -Fxq "$menu_horizontal_uid"; then
  echo "Horizontal Split did not create a Registry-backed Pane: pane=$menu_horizontal_pane uid=$menu_horizontal_uid" >&2
  exit 1
fi

# Kill: target the new pane and select the stock X shortcut.
menu_kill_log_offset="$(stat -c %s "$menu_client_log")"
menu_select_item "$menu_horizontal_pane" X
smoke_wait_for "canonical Horizontal Kill completion and Registry convergence" \
  menu_delete_converged "$menu_horizontal_uid" "$menu_kill_log_offset" 1

# Vertical Split reaches the other placement through the same exact anchor.
menu_select_item "$menu_origin_pane" v
smoke_wait_for "managed Vertical Split" menu_wait_for_pane_count 2
menu_vertical_pane="$(menu_new_pane_except "$menu_origin_pane")"
smoke_wait_for "Vertical Split Registry identity" menu_pane_is_managed "$menu_vertical_pane"
menu_vertical_uid="$(menu_tmux show-options -pqv -t "$menu_vertical_pane" @projmux_pane_uid)"
if [[ -z "$menu_vertical_uid" ]] || ! menu_pmx get panes -o uid | grep -Fxq "$menu_vertical_uid"; then
  echo "Vertical Split did not create a Registry-backed Pane: pane=$menu_vertical_pane uid=$menu_vertical_uid" >&2
  exit 1
fi

# Remove the Vertical Split through the same canonical Kill so cleanup starts
# from the original managed pane only.
menu_kill_log_offset="$(stat -c %s "$menu_client_log")"
menu_select_item "$menu_vertical_pane" X
smoke_wait_for "canonical Vertical Kill completion and Registry convergence" \
  menu_delete_converged "$menu_vertical_uid" "$menu_kill_log_offset" 1

menu_cleanup_target="$menu_socket_path"
menu_cleanup
if "${menu_env[@]}" tmux -S "$menu_cleanup_target" list-sessions >/dev/null 2>&1; then
  echo "pane-menu exact socket cleanup left a live server at $menu_cleanup_target" >&2
  exit 1
fi
trap smoke_cleanup_env EXIT
echo ">> managed pane-menu e2e passed: pane=$menu_origin_pane project=$menu_project_uid socket=$menu_socket path=$menu_socket_path cleanup=$menu_cleanup_target inherited=unset"

# Declarative contract stabilization Phase 12: drive the four Home popup
# terminal paths through the built binary on an exact, config-apply-declared
# ControlSession. The scripted native picker is still the production picker;
# line mode only replaces terminal key decoding so this smoke can select rows
# deterministically. Every invocation strips inherited TMUX/TMUX_PANE and then
# supplies the popup's exact socket and origin Pane explicitly.
p12_root="$PROJMUX_SMOKE_WORKDIR/control-root-create"
p12_socket="control-root-create-e2e-$$-$RANDOM"
p12_session="home"
p12_config="$p12_root/config/projmux/tmux.conf"
p12_registry="$p12_root/state/projmux/metadata/registry.json"
p12_agent_argv="$p12_root/agent-argv.log"
mkdir -p "$p12_root"/{home,bin,cache,config,runtime,state,tmux,workspace} \
  "$p12_root/home/.codex/sessions/2026/08/21"
chmod 0700 "$p12_root/runtime"

for p12_provider in claude codex antigravity; do
  printf '%s\n' '#!/bin/sh' \
    "printf '%s\\n' \"\$0 \$*\" >> \"\$PROJMUX_PHASE12_AGENT_ARGV\"" \
    'exec sleep 600' >"$p12_root/bin/$p12_provider"
  chmod 0755 "$p12_root/bin/$p12_provider"
done

p12_env=(
  env -u TMUX -u TMUX_PANE -u PROJMUX_SMOKE_TMUX_SOCKET
  HOME="$p12_root/home"
  XDG_CACHE_HOME="$p12_root/cache"
  XDG_CONFIG_HOME="$p12_root/config"
  XDG_RUNTIME_DIR="$p12_root/runtime"
  XDG_STATE_HOME="$p12_root/state"
  TMUX_TMPDIR="$p12_root/tmux"
  PROJMUX_PHASE12_AGENT_ARGV="$p12_agent_argv"
  SHELL=/bin/bash
  PATH="$p12_root/bin:$PATH"
)
p12_tmux() { "${p12_env[@]}" tmux -L "$p12_socket" "$@"; }
p12_pmx() { "${p12_env[@]}" "$bin" "$@"; }

p12_socket_path=""
p12_cleanup_target=""
p12_cleanup_done=0
p12_cleanup() {
  if [[ "$p12_cleanup_done" == "1" ]]; then
    return
  fi
  p12_cleanup_done=1
  if [[ -n "$p12_cleanup_target" ]]; then
    case "$p12_cleanup_target" in
      "$p12_root"/tmux/*) ;;
      *)
        echo "refusing Phase 12 ControlSession cleanup outside smoke root: $p12_cleanup_target" >&2
        return 1
        ;;
    esac
    "${p12_env[@]}" tmux -S "$p12_cleanup_target" kill-server >/dev/null 2>&1 || true
  fi
}
trap 'p12_cleanup; smoke_cleanup_env' EXIT

p12_tmux new-session -d -s "$p12_session" -c "$p12_root/workspace" sleep 600
p12_origin_pane="$(p12_tmux display-message -p -t "$p12_session:0.0" '#{pane_id}')"
p12_socket_path="$(p12_tmux display-message -p -t "$p12_origin_pane" '#{socket_path}')"
p12_server_pid="$(p12_tmux display-message -p -t "$p12_origin_pane" '#{pid}')"
case "$p12_socket_path" in
  "$p12_root"/tmux/*) p12_cleanup_target="$p12_socket_path" ;;
  *)
    echo "Phase 12 ControlSession socket escaped smoke root: $p12_socket_path" >&2
    exit 1
    ;;
esac

p12_pmx config apply --bin "$bin" --config "$p12_config" --socket "$p12_socket" >"$p12_root/apply.out"
p12_control_uid="$(sed -n '/"controlSessions"/,/"windows"/ s/.*"uid": "\([^"]*\)".*/\1/p' "$p12_registry" | head -n 1)"
p12_window_uid="$(p12_tmux show-options -wqv -t "$p12_origin_pane" @projmux_window_uid)"
p12_origin_uid="$(p12_tmux show-options -pqv -t "$p12_origin_pane" @projmux_pane_uid)"
if [[ -z "$p12_control_uid" || -z "$p12_window_uid" || -z "$p12_origin_uid" ]] ||
  [[ "$(p12_tmux show-options -qv -t "$p12_session" @projmux_session_role)" != "control" ]]; then
  echo "config apply did not declare the exact Home ControlSession/Window/Pane chain" >&2
  exit 1
fi

p12_popup() {
  "${p12_env[@]}" \
    TMUX="$p12_socket_path,$p12_server_pid,0" \
    TMUX_SPLIT_TARGET_PANE="$p12_origin_pane" \
    PROJMUX_NATIVE_TTY_FALLBACK=0 \
    PROJMUX_NATIVE_LINE_MODE=1 \
    "$bin" "$@"
}
p12_pane_count() { p12_tmux list-panes -t "$p12_session:0" -F '#{pane_id}' | wc -l; }
p12_agent_count() { p12_popup get agents --all-projects -o uid 2>/dev/null | grep -c . || true; }
p12_new_pane() {
  p12_tmux list-panes -t "$p12_session:0" -F '#{pane_id}' | grep -Fvx "$p12_origin_pane" | sort -t% -k2,2n | tail -n 1
}
p12_assert_managed_create() {
  local label="$1"
  local before_panes="$2"
  local before_agents="$3"
  local want_agent="$4"
  local pane pane_uid pane_json agents_json
  if [[ "$(p12_pane_count)" != "$((before_panes + 1))" ]]; then
    echo "$label did not add exactly one Home Pane" >&2
    exit 1
  fi
  pane="$(p12_new_pane)"
  pane_uid="$(p12_tmux show-options -pqv -t "$pane" @projmux_pane_uid)"
  if [[ -z "$pane_uid" ]]; then
    echo "$label created a Pane without a managed uid" >&2
    exit 1
  fi
  pane_json="$(p12_popup get pane --pane "uid:$pane_uid" -o json)"
  smoke_assert_output_contains "$pane_json" "$pane_uid"
  agents_json="$(p12_popup get agents --all-projects -o json)"
  if [[ "$want_agent" == "1" ]]; then
    if [[ "$(p12_agent_count)" != "$((before_agents + 1))" ]]; then
      echo "$label did not add exactly one managed Agent" >&2
      exit 1
    fi
    smoke_assert_output_contains "$agents_json" "$p12_window_uid"
  elif [[ "$(p12_agent_count)" != "$before_agents" ]]; then
    echo "$label shell split unexpectedly added an Agent" >&2
    exit 1
  fi
  if [[ "$(p12_tmux display-message -p -t "$pane" '#{@projmux_window_uid}')" != "$p12_window_uid" ]]; then
    echo "$label Pane was not mirrored below the exact Home Window" >&2
    exit 1
  fi
  p12_last_pane_uid="$pane_uid"
}

# Provider picker: filter to codex, then select the one remaining production
# row. The resulting process is a harmless run-local provider shim.
p12_before_panes="$(p12_pane_count)"
p12_before_agents="$(p12_agent_count)"
printf 'codex\n1\n' | p12_popup internal agent-pane picker --inside right >"$p12_root/provider-picker.out"
p12_assert_managed_create "Home provider picker" "$p12_before_panes" "$p12_before_agents" 1
p12_provider_uid="$p12_last_pane_uid"

# Resume picker: one real Codex rollout row for the Home Pane cwd, selected via
# the same production picker. The provider shim records the exact resume argv.
p12_resume_id="019f0000-0000-7000-8000-000000001212"
printf '%s\n' \
  "{\"type\":\"session_meta\",\"payload\":{\"id\":\"$p12_resume_id\",\"cwd\":\"$p12_root/workspace\",\"git_branch\":\"fix/control-root-canonical-create\"}}" \
  '{"type":"event_msg","payload":{"message":"Phase 12 Home resume"}}' \
  >"$p12_root/home/.codex/sessions/2026/08/21/rollout-phase12.jsonl"
p12_before_panes="$(p12_pane_count)"
p12_before_agents="$(p12_agent_count)"
{ sleep 1; printf '2\n'; } | p12_popup internal agent-pane picker --inside --resume down >"$p12_root/resume-picker.out"
p12_assert_managed_create "Home resume picker" "$p12_before_panes" "$p12_before_agents" 1
p12_resume_uid="$p12_last_pane_uid"
smoke_assert_file_contains "$p12_agent_argv" "resume $p12_resume_id"

# Saved default and direct shell use the same exact popup origin but exercise
# the two non-picker producers.
mkdir -p "$p12_root/config/projmux"
printf 'codex\n' >"$p12_root/config/projmux/tmux-ai-split-mode"
p12_before_panes="$(p12_pane_count)"
p12_before_agents="$(p12_agent_count)"
p12_popup internal agent-pane launch-default right >"$p12_root/default.out"
p12_assert_managed_create "Home saved default" "$p12_before_panes" "$p12_before_agents" 1
p12_default_uid="$p12_last_pane_uid"

p12_before_panes="$(p12_pane_count)"
p12_before_agents="$(p12_agent_count)"
p12_popup internal agent-pane launch-shell down >"$p12_root/shell.out"
p12_assert_managed_create "Home shell split" "$p12_before_panes" "$p12_before_agents" 0
p12_shell_uid="$p12_last_pane_uid"

# Declarative contract stabilization Phase 13: add a sibling Project and one
# foreign tmux Pane to the same isolated server, then close the plural-read
# context x selector matrix through the built binary. The ControlSession rows
# are the real Home chain declared above; no fixture-only Registry edit is used.
mkdir -p "$p12_root/project"
p12_inside() {
  local pane="$1"
  shift
  "${p12_env[@]}" \
    TMUX="$p12_socket_path,$p12_server_pid,0" \
    TMUX_PANE="$pane" \
    "$bin" "$@"
}

p12_project_uid="$(p12_pmx create project --root "$p12_root/project" --name phase13-project -o uid)"
p12_project_window_uid="$(
  p12_inside "$p12_origin_pane" create window \
    --project "uid:$p12_project_uid" \
    --name phase13-project-window \
    -o uid -- sleep 600
)"
p12_project_origin_pane="$(
  p12_tmux list-panes -a -F '#{pane_id} #{@projmux_window_uid}' |
    awk -v uid="$p12_project_window_uid" '$2 == uid { print $1 }'
)"
p12_project_origin_uid="$(p12_tmux show-options -pqv -t "$p12_project_origin_pane" @projmux_pane_uid)"
if [[ -z "$p12_project_uid" || -z "$p12_project_window_uid" || -z "$p12_project_origin_pane" ||
  -z "$p12_project_origin_uid" ]] ||
  [[ "$(printf '%s\n' "$p12_project_origin_pane" | grep -c .)" != "1" ]]; then
  echo "Phase 13 canonical Project fixture did not resolve one exact Project/Window/Pane chain" >&2
  exit 1
fi

p12_project_agent_pane="$(
  p12_inside "$p12_project_origin_pane" create codex \
    --project "uid:$p12_project_uid" \
    --window "uid:$p12_project_window_uid" \
    -o pane-id
)"
p12_project_agent_pane_uid="$(p12_tmux show-options -pqv -t "$p12_project_agent_pane" @projmux_pane_uid)"
p12_project_agent_uid="$(p12_pmx get agents --project "uid:$p12_project_uid" -o uid)"
if [[ -z "$p12_project_agent_pane_uid" || -z "$p12_project_agent_uid" ]] ||
  [[ "$(printf '%s\n' "$p12_project_agent_uid" | grep -c .)" != "1" ]]; then
  echo "Phase 13 Project fixture did not create exactly one managed Agent" >&2
  exit 1
fi

p12_tmux new-session -d -s phase13-foreign -c "$p12_root" sleep 600
p12_foreign_pane="$(p12_tmux display-message -p -t phase13-foreign:0.0 '#{pane_id}')"
if [[ -n "$(p12_tmux show-options -wqv -t "$p12_foreign_pane" @projmux_window_uid)" ]]; then
  echo "Phase 13 foreign fixture unexpectedly carries a managed Window uid" >&2
  exit 1
fi

p13_context_run() {
  local context="$1"
  shift
  case "$context" in
    project) p12_inside "$p12_project_origin_pane" "$@" ;;
    control) p12_inside "$p12_origin_pane" "$@" ;;
    foreign) p12_inside "$p12_foreign_pane" "$@" ;;
    outside) p12_pmx "$@" ;;
    *) echo "unknown Phase 13 read context: $context" >&2; return 1 ;;
  esac
}

p13_case=0
p13_assert_success() {
  local label="$1"
  local want="$2"
  local context="$3"
  shift 3
  local raw actual status err
  p13_case=$((p13_case + 1))
  err="$p12_root/phase13-matrix-$p13_case.err"
  set +e
  raw="$(p13_context_run "$context" "$@" 2>"$err")"
  status=$?
  set -e
  actual="$(printf '%s\n' "$raw" | sort)"
  if [[ "$status" != "0" || -s "$err" || "$actual" != "$want" ]]; then
    echo "Phase 13 $label: status=$status actual=[$actual] want=[$want]" >&2
    cat "$err" >&2 || true
    exit 1
  fi
}

p13_assert_refusal() {
  local label="$1"
  local context="$2"
  shift 2
  local raw status err
  p13_case=$((p13_case + 1))
  err="$p12_root/phase13-matrix-$p13_case.err"
  set +e
  raw="$(p13_context_run "$context" "$@" 2>"$err")"
  status=$?
  set -e
  if [[ "$status" != "2" || -n "$raw" ]]; then
    echo "Phase 13 $label did not refuse with status 2 and zero stdout: status=$status stdout=[$raw]" >&2
    cat "$err" >&2 || true
    exit 1
  fi
  smoke_assert_file_contains "$err" "active managed-root scope is undecidable"
  smoke_assert_file_contains "$err" "all-projects"
}

for p13_kind in windows panes agents; do
  case "$p13_kind" in
    windows)
      p13_control_want="$(p12_pmx get windows --all-projects --window "uid:$p12_window_uid" -o uid | sort)"
      p13_project_want="$(p12_pmx get windows --project "uid:$p12_project_uid" -o uid | sort)"
      p13_uid_args=(--window "uid:$p12_project_window_uid")
      p13_uid_want="$p12_project_window_uid"
      p13_control_count=1
      p13_project_count=2
      ;;
    panes)
      p13_control_want="$(p12_pmx get panes --all-projects --window "uid:$p12_window_uid" -o uid | sort)"
      p13_project_want="$(p12_pmx get panes --project "uid:$p12_project_uid" -o uid | sort)"
      p13_uid_args=(--pane "uid:$p12_project_origin_uid")
      p13_uid_want="$p12_project_origin_uid"
      p13_control_count=5
      p13_project_count=3
      ;;
    agents)
      p13_control_want="$(p12_pmx get agents --all-projects --window "uid:$p12_window_uid" -o uid | sort)"
      p13_project_want="$(p12_pmx get agents --project "uid:$p12_project_uid" -o uid | sort)"
      p13_uid_args=(--window "uid:$p12_project_window_uid")
      p13_uid_want="$p12_project_agent_uid"
      p13_control_count=3
      p13_project_count=1
      ;;
  esac
  if [[ "$(printf '%s\n' "$p13_control_want" | grep -c .)" != "$p13_control_count" ]] ||
    [[ "$(printf '%s\n' "$p13_project_want" | grep -c .)" != "$p13_project_count" ]]; then
    echo "Phase 13 $p13_kind root fixtures do not have the expected exact cardinalities" >&2
    exit 1
  fi
  p13_global_want="$(printf '%s\n%s\n' "$p13_control_want" "$p13_project_want" | sort)"

  for p13_context in project control foreign outside; do
    case "$p13_context" in
      project) p13_omitted_want="$p13_project_want" ;;
      control) p13_omitted_want="$p13_control_want" ;;
      foreign)
        p13_assert_refusal "$p13_kind foreign omitted" "$p13_context" \
          get "$p13_kind" -o uid
        ;;
      outside) p13_omitted_want="$p13_global_want" ;;
    esac
    if [[ "$p13_context" != "foreign" ]]; then
      p13_assert_success "$p13_kind $p13_context omitted" "$p13_omitted_want" "$p13_context" \
        get "$p13_kind" -o uid
    fi

    p13_assert_success "$p13_kind $p13_context explicit Project" "$p13_project_want" "$p13_context" \
      get "$p13_kind" --project "uid:$p12_project_uid" -o uid
    p13_assert_success "$p13_kind $p13_context --all-projects" "$p13_global_want" "$p13_context" \
      get "$p13_kind" --all-projects -o uid
    p13_assert_success "$p13_kind $p13_context -A" "$p13_global_want" "$p13_context" \
      get "$p13_kind" -A -o uid

    p13_assert_success "$p13_kind $p13_context global uid selector" "$p13_uid_want" "$p13_context" \
      get "$p13_kind" "${p13_uid_args[@]}" -o uid
  done
done

if [[ ! -s "$p12_registry" ]]; then
  echo "Phase 12 Home producers left no Registry" >&2
  exit 1
fi
p12_cleanup
if "${p12_env[@]}" tmux -S "$p12_cleanup_target" list-sessions >/dev/null 2>&1; then
  echo "Phase 12 exact socket cleanup left a live server at $p12_cleanup_target" >&2
  exit 1
fi
trap smoke_cleanup_env EXIT
echo ">> ControlSession create/read-scope e2e passed: control=$p12_control_uid window=$p12_window_uid origin=$p12_origin_uid provider=$p12_provider_uid resume=$p12_resume_uid default=$p12_default_uid shell=$p12_shell_uid project=$p12_project_uid project-window=$p12_project_window_uid foreign=$p12_foreign_pane matrix=$p13_case socket=$p12_socket path=$p12_socket_path cleanup=$p12_cleanup_target inherited=unset"
