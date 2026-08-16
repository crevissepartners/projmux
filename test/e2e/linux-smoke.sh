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

# The pre-namespace spelling is what a tmux server configured by a previously
# installed binary still invokes, so it is exercised here verbatim; the
# relocated spelling must render the same segment against the same live state.
status_notify="$("$bin" status notify --max-width 80)"
smoke_assert_output_contains "$status_notify" "docker e2e"
internal_status_notify="$("$bin" internal status notify --max-width 80)"
if [[ "$internal_status_notify" != "$status_notify" ]]; then
  echo "internal status notify diverged from the compatibility spelling status notify" >&2
  echo "compatibility: $status_notify" >&2
  echo "internal:      $internal_status_notify" >&2
  exit 1
fi

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

tmux -L "$recorder_socket" display-popup -c "$recorder_client" \
	-T "Recorder E2E" -w 72 -h 20 -E \
	"env PROJMUX_PICKER_BACKEND=retired-value '$bin' settings" &
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
# The returned Action detail carries the cancellation as an observable
# feedback row, which is also the Phase 2 zero-silent-no-op contract. The row
# is asserted rather than "+ Add key" because the detail is taller than the
# 20-row popup and the Add row can sit below the visible window.
smoke_wait_for "Action detail after Esc cancellation" sh -c \
  "tail -c +$((recorder_cancel_log_offset + 1)) '$recorder_log' | grep -aFq 'Keybinding cancelled'"
if grep -Fq '"M-s"' "$XDG_CONFIG_HOME/projmux/keymap.toml"; then
  echo "recorder persisted M-s after Esc cancellation" >&2
  exit 1
fi

# Phase 2 action-detail correctness on the same real client. The detail
# projects the action's semantics and the shipped handler, the retired
# Advanced/Troubleshooting containers are gone, and the key detail's Test
# delivery row reports a result instead of returning to the same loop.
recorder_detail_offset="$(stat -c %s "$recorder_log")"
for marker in 'Target kind' 'Result kind' 'Handler'; do
  smoke_wait_for "action detail $marker" sh -c \
    "tail -c +$((recorder_detail_offset + 1)) '$recorder_log' | grep -aFq '$marker'"
done
if tail -c +$((recorder_detail_offset + 1)) "$recorder_log" | grep -aEq 'Troubleshooting|Advanced\.\.\.'; then
  echo "action detail still renders a retired Advanced/Troubleshooting container" >&2
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
keymap_before_test="$(cat "$XDG_CONFIG_HOME/projmux/keymap.toml")"
recorder_testdelivery_offset="$(stat -c %s "$recorder_log")"
printf 'Test delivery\r' >&9
smoke_wait_for "Test delivery reader" sh -c \
  "tail -c +$((recorder_testdelivery_offset + 1)) '$recorder_log' | grep -aFq 'Press a key combination'"
recorder_teststage_offset="$(stat -c %s "$recorder_log")"
printf '\022' >&9
smoke_wait_for "Test delivery observed C-r" sh -c \
  "tail -c +$((recorder_teststage_offset + 1)) '$recorder_log' | grep -aFq 'Staged: C-r'"
recorder_testresult_offset="$(stat -c %s "$recorder_log")"
printf '\r' >&9
smoke_wait_for "Test delivery observable result" sh -c \
  "tail -c +$((recorder_testresult_offset + 1)) '$recorder_log' | grep -aFq 'Test delivery complete'"
if [[ "$keymap_before_test" != "$(cat "$XDG_CONFIG_HOME/projmux/keymap.toml")" ]]; then
  echo "Test delivery mutated keymap.toml" >&2
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
  "$bin" notify push \
    --id "polish-$severity" \
    --text "polish $severity" \
    --target "$recorder_session:0.0" \
    --socket "$recorder_socket" \
    --severity "$severity" \
    --source external >/dev/null
done
notify_before="$PROJMUX_SMOKE_WORKDIR/notify-polish-before.json"
"$bin" notify list --json >"$notify_before"
for severity in info warn critical; do
  smoke_assert_file_contains "$notify_before" "polish-$severity"
done

notify_focus_before="$(tmux -L "$recorder_socket" display-message -p -c "$recorder_client" '#{pane_id}')"
notify_cancel_marker="$PROJMUX_SMOKE_WORKDIR/notify-clear-cancel.done"
notify_cancel_offset="$(stat -c %s "$recorder_log")"
tmux -L "$recorder_socket" display-popup -c "$recorder_client" \
  -T "Notify E2E" -w 80 -h 24 -E \
  "'$bin' notify list --ui=sidebar --client '$recorder_client'; printf done >'$notify_cancel_marker'" &
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
"$bin" notify list --json >"$notify_after_cancel"
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
  "'$bin' notify list --ui=sidebar --client '$recorder_client'; printf done >'$notify_confirm_marker'" &
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
"$bin" notify list --json >"$notify_after_confirm"
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

tmux -L "$recorder_socket" kill-server
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
# The registry file, the legacy naming migration, and the detached split have
# never run against a real tmux server in production, so this block does all
# three end to end on a dedicated exact socket.
#
# Isolation follows the four mandatory conditions: inherited TMUX/TMUX_PANE are
# stripped from every call, the server lives under a run-unique TMUX_TMPDIR with
# its own -L name, the real #{socket_path} is queried and proven to sit inside
# the smoke root, and only that exact socket is killed.
# ---------------------------------------------------------------------------
create_root="$PROJMUX_SMOKE_WORKDIR/create-e2e"
create_socket="projmux-create-e2e-$$-$RANDOM"
mkdir -p "$create_root/tt" "$create_root/state" "$create_root/config" "$create_root/work/alpha" "$create_root/work/beta"
create_real_tmux="$(command -v tmux)"

ctx() { env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$create_root/tt" "$create_real_tmux" -L "$create_socket" "$@"; }

# projmux shells out to a bare `tmux`, so route it onto this exact socket.
create_shim="$create_root/shim"
mkdir -p "$create_shim"
cat >"$create_shim/tmux" <<CREATE_SHIM
#!/usr/bin/env bash
if [[ "\${PROJMUX_CREATE_FAIL_SPLIT:-}" == "1" && "\${1:-}" == "split-window" ]]; then
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
    PROJMUX_MANAGED_ROOTS="$create_root/work" \
    SHELL=/bin/sh \
    "$bin" "$@"
}

create_registry="$create_root/state/projmux/metadata/registry.json"
if [[ -e "$create_registry" ]]; then
  echo "create e2e did not start from an empty registry" >&2
  exit 1
fi

# A pre-v2 session for the legacy naming migration: window_name, automatic
# rename, a user pane label, and a raw pane title must never become the stable
# Window metadata.name. window_name is retained as displayName.
ctx new-session -d -s legacy-alpha -c "$create_root/work/alpha" sleep 600
ctx set-option -t legacy-alpha -q @projmux_project_path "$create_root/work/alpha"
ctx set-option -w -t legacy-alpha:0 automatic-rename on
legacy_pane="$(ctx display-message -p -t legacy-alpha '#{pane_id}')"
legacy_window_id="$(ctx display-message -p -t legacy-alpha '#{window_id}')"
ctx set-option -p -t "$legacy_pane" @projmux_pane_label "buildlog"
ctx select-pane -T "raw title must not win" -t "$legacy_pane"
legacy_window_name_before="$(ctx display-message -p -t "$legacy_window_id" '#{window_name}')"

create_socket_path="$(ctx display-message -p -t legacy-alpha '#{socket_path}')"
case "$create_socket_path" in
  "$create_root"/*) ;;
  *)
    echo "create e2e socket escaped the smoke root: $create_socket_path" >&2
    exit 1
    ;;
esac
echo ">> create e2e socket=$create_socket path=$create_socket_path"

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
}
trap 'create_cleanup; smoke_cleanup_env' EXIT

# 1. A pre-create hook refusal aborts with zero mutations.
mkdir -p "$create_root/config/projmux"
cat >"$create_root/config/projmux/config.toml" <<'PRECREATE'
[hooks.pre-create]
run = "exit 7"
PRECREATE
create_sessions_before="$(ctx list-sessions -F '#{session_name}' | wc -l)"
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
if [[ -e "$create_registry" ]]; then
  echo "a pre-create hook refusal created the registry" >&2
  exit 1
fi
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
if [[ ! -f "$create_registry" ]]; then
  echo "the first successful create did not write the registry" >&2
  exit 1
fi
if [[ "$(stat -c '%a' "$create_registry")" != "600" ]]; then
  echo "registry mode = $(stat -c '%a' "$create_registry"), want 600" >&2
  exit 1
fi

# 3. The legacy migration allocated a stable Window name independently of every
#    runtime attribute, retained window_name as displayName, turned
#    automatic-rename off, and mirrored the allocated uids back.
legacy_window_name="$(ctx display-message -p -t legacy-alpha:0 '#{window_name}')"
if [[ "$legacy_window_name" != "$legacy_window_name_before" ]]; then
  echo "legacy migration runtime display = $legacy_window_name, want preserved $legacy_window_name_before" >&2
  exit 1
fi
if [[ "$(ctx show-options -wqv -t legacy-alpha:0 automatic-rename)" != "off" ]]; then
  echo "legacy migration left automatic-rename on for a managed Window" >&2
  exit 1
fi
if [[ -z "$(ctx display-message -p -t legacy-alpha:0 '#{@projmux_window_uid}')" ]]; then
  echo "legacy migration did not mirror the Window uid" >&2
  exit 1
fi
if [[ -z "$(ctx display-message -p -t "$legacy_pane" '#{@projmux_pane_uid}')" ]]; then
  echo "legacy migration did not mirror the Pane uid" >&2
  exit 1
fi
pmx get windows --project alpha -o name >"$create_root/alpha-windows.out"
smoke_assert_file_contains "$create_root/alpha-windows.out" "window"
pmx describe window window --project alpha >"$create_root/alpha-window.describe"
smoke_assert_file_contains "$create_root/alpha-window.describe" "DisplayName:"
smoke_assert_file_contains "$create_root/alpha-window.describe" "$legacy_window_name_before"

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

run_in_pane right create pane --project alpha --window window --placement right -o pane-id
run_in_pane down create pane --project alpha --window window --placement down -o pane-id
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

# 6. A stale primaryPaneRef is exit 2 with zero mutations and zero stdout.
cp "$create_registry" "$create_root/registry.intact"
# The registry is written with MarshalIndent, so every primaryPaneRef sits alone
# on its own line and the substitution cannot reach any other field.
sed -i 's/"primaryPaneRef": "pane-[a-z0-9]*"/"primaryPaneRef": "pane-doesnotexist"/' "$create_registry"
if ! grep -Fq '"primaryPaneRef": "pane-doesnotexist"' "$create_registry"; then
  echo "the stale primaryPaneRef fixture did not apply" >&2
  exit 1
fi
stale_before="$(md5sum "$create_registry" | cut -d' ' -f1)"
stale_panes_before="$(ctx list-panes -s -t legacy-alpha -F '#{pane_id}' | wc -l)"
set +e
pmx create pane --project alpha --window review >"$create_root/stale.out" 2>"$create_root/stale.err"
stale_status=$?
set -e
if [[ "$stale_status" != "2" ]]; then
  echo "stale primaryPaneRef exit = $stale_status, want 2" >&2
  cat "$create_root/stale.err" >&2 || true
  exit 1
fi
if [[ -s "$create_root/stale.out" ]]; then
  echo "stale primaryPaneRef wrote to stdout" >&2
  exit 1
fi
if [[ "$(md5sum "$create_registry" | cut -d' ' -f1)" != "$stale_before" ]]; then
  echo "stale primaryPaneRef mutated the registry" >&2
  exit 1
fi
if [[ "$(ctx list-panes -s -t legacy-alpha -F '#{pane_id}' | wc -l)" != "$stale_panes_before" ]]; then
  echo "stale primaryPaneRef fell back to a live pane" >&2
  exit 1
fi
smoke_assert_file_contains "$create_root/stale.err" "primaryPaneRef"
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
  PROJMUX_MANAGED_ROOTS="$create_root/work" \
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
    PROJMUX_MANAGED_ROOTS="$create_root/work" \
    SHELL=/bin/bash \
    "$bin" "$@"
}

agent_window_before="$(ctx display-message -p -t legacy-alpha '#{window_id}')"
agent_pane_before="$(ctx display-message -p -t legacy-alpha '#{pane_id}')"

pmx_agent create agent --provider codex --project alpha --window window -o pane-id \
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
if ! grep -Fq "cwd=$create_root/work/alpha" "$create_root/agent-launch.log"; then
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
pmx_agent create codex --project alpha --window window -o name >"$create_root/agent-second.out"
if [[ "$(tr -d '[:space:]' <"$create_root/agent-second.out")" != "codex-1" ]]; then
  echo "the second Agent name = $(cat "$create_root/agent-second.out"), want codex-1" >&2
  exit 1
fi
pmx_agent get agents --project alpha -o name >"$create_root/agent-list.out"
for want in codex codex-1; do
  if ! grep -qx "$want" "$create_root/agent-list.out"; then
    echo "get agents is missing $want:" >&2
    cat "$create_root/agent-list.out" >&2
    exit 1
  fi
done

# 10. The payload after -- reaches the provider and never the naming.
: >"$create_root/agent-launch.log"
pmx_agent create agent --provider codex --project alpha --window window -o name \
  -- --topic "release triage" >"$create_root/agent-payload.out"
if [[ "$(tr -d '[:space:]' <"$create_root/agent-payload.out")" != "codex-2" ]]; then
  echo "a payload changed the Agent name: $(cat "$create_root/agent-payload.out")" >&2
  exit 1
fi
for _ in {1..200}; do
  [[ -s "$create_root/agent-launch.log" ]] && break
  sleep 0.05
done
if ! grep -Fq -- "args=--topic release triage" "$create_root/agent-launch.log"; then
  echo "the payload did not reach the provider:" >&2
  cat "$create_root/agent-launch.log" >&2 || true
  exit 1
fi

# 11. A missing provider is exit 2 with zero mutations and zero stdout.
agent_registry_before="$(md5sum "$create_registry" | cut -d' ' -f1)"
set +e
pmx_agent create agent --project alpha --window window \
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
pmx_agent create agent --provider codex --project alpha --window window --name codex \
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

# 13. The compatibility bridge is untouched: with no --project the invocation is
#     still the legacy `ai split`, and its identity projections name the flag
#     that would make them work.
set +e
pmx_agent create agent --provider codex -o uid \
  >"$create_root/agent-bridge.out" 2>"$create_root/agent-bridge.err"
agent_bridge_status=$?
set -e
if [[ "$agent_bridge_status" != "2" ]]; then
  echo "the bridge identity projection exit = $agent_bridge_status, want 2" >&2
  exit 1
fi
if [[ -s "$create_root/agent-bridge.out" ]]; then
  echo "the bridge identity projection wrote to stdout" >&2
  exit 1
fi
# The needle deliberately starts with a word: the helper passes it to grep as a
# pattern, so a leading `--` would be read as an option.
smoke_assert_file_contains "$create_root/agent-bridge.err" "add --project <ref>"

create_cleanup
trap smoke_cleanup_env EXIT
echo ">> create e2e passed: socket=$create_socket path=$create_socket_path"

# Managed runtime binding convergence runs on its own two exact sockets. Every
# client call strips inherited TMUX/TMUX_PANE; only the implicit reads below
# receive a synthetic client identity after the binding has already converged.
binding_root="$PROJMUX_SMOKE_WORKDIR/managed-binding-phase3"
binding_socket="projmux-binding-$RANDOM-$$"
binding_second_socket="projmux-binding-second-$RANDOM-$$"
binding_session="managed-binding"
binding_beta_session="managed-binding-beta"
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

binding_cleanup() {
  local socket actual
  for socket in "$binding_socket" "$binding_second_socket"; do
    actual="$(
      env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$binding_root/tmux" \
        tmux -L "$socket" display-message -p '#{socket_path}' 2>/dev/null || true
    )"
    if [[ -z "$actual" ]]; then
      continue
    fi
    case "$actual" in
      "$binding_root"/*)
        # The app config owns background pane-exit/focus hooks. Disable them on
        # this exact server before kill-server so no detached helper races the
        # smoke root cleanup by recreating state after the server is gone.
        local hook
        for hook in pane-exited after-kill-pane pane-focus-out pane-focus-in after-select-pane after-select-window client-session-changed; do
          env -u TMUX -u TMUX_PANE tmux -S "$actual" set-hook -gu "$hook" >/dev/null 2>&1 || true
        done
        env -u TMUX -u TMUX_PANE tmux -S "$actual" kill-server >/dev/null 2>&1 || true
        ;;
      *)
        echo "refusing managed binding e2e cleanup outside the smoke root: $actual" >&2
        ;;
    esac
  done
}
trap 'binding_cleanup; smoke_cleanup_env' EXIT

binding_second_before="$(binding_second_tmux show-options -gqv @projmux_phase3_sentinel):$(binding_second_tmux list-windows -a -F '#{session_name}:#{window_id}:#{@projmux_window_uid}')"
binding_config="$binding_root/config/projmux/tmux.conf"
binding_pmx tmux apply --bin "$bin" --config "$binding_config" --socket "$binding_socket" \
  >"$binding_root/first-apply.out"
smoke_assert_file_contains "$binding_root/first-apply.out" "reloaded tmux server -L $binding_socket"

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
binding_pmx tmux apply --bin "$bin" --config "$binding_config" --socket "$binding_socket" \
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
binding_pmx tmux apply --bin "$bin" --config "$binding_config" --socket "$binding_socket" \
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

# The generated hooks are synchronous. Therefore these options must be visible
# immediately after new-window/split-window returns, without a polling loop.
lifecycle_window="$(
  binding_tmux new-window -d -t "$binding_session:" -n lifecycle -c "$binding_root/work/alpha" \
    -P -F '#{window_id}' sleep 600
)"
lifecycle_window_uid="$(binding_tmux show-options -wqv -t "$lifecycle_window" @projmux_window_uid)"
lifecycle_initial_pane="$(binding_tmux display-message -p -t "$lifecycle_window" '#{pane_id}')"
lifecycle_initial_pane_uid="$(binding_tmux show-options -pqv -t "$lifecycle_initial_pane" @projmux_pane_uid)"
if [[ -z "$lifecycle_window_uid" ]] || [[ -z "$lifecycle_initial_pane_uid" ]]; then
  echo "after-new-window returned before its Window/Pane binding converged" >&2
  exit 1
fi

lifecycle_split_pane="$(
  binding_tmux split-window -d -t "$lifecycle_initial_pane" -c "$binding_root/work/alpha" \
    -P -F '#{pane_id}' sleep 600
)"
lifecycle_split_uid="$(binding_tmux show-options -pqv -t "$lifecycle_split_pane" @projmux_pane_uid)"
if [[ -z "$lifecycle_split_uid" ]]; then
  echo "after-split-window returned before its Pane binding converged" >&2
  exit 1
fi

# The immediately following selector-omitted read resolves that exact Pane.
binding_server_pid="$(binding_tmux display-message -p -t "$lifecycle_split_pane" '#{pid}')"
env \
  HOME="$binding_root/home" \
  XDG_CONFIG_HOME="$binding_root/config" \
  XDG_STATE_HOME="$binding_root/state" \
  XDG_RUNTIME_DIR="$binding_root/runtime" \
  PROJMUX_MANAGED_ROOTS="$binding_root/work" \
  TMUX_TMPDIR="$binding_root/tmux" \
  TMUX="$binding_socket_path,$binding_server_pid,0" \
  TMUX_PANE="$lifecycle_split_pane" \
  SHELL=/bin/sh \
  "$bin" describe pane -o uid >"$binding_root/implicit-read.out"
if [[ "$(tr -d '[:space:]' <"$binding_root/implicit-read.out")" != "$lifecycle_split_uid" ]]; then
  echo "implicit read did not resolve the synchronously bound Pane" >&2
  cat "$binding_root/implicit-read.out" >&2
  exit 1
fi

# The plural registry reads use Project as a namespace-like default. The
# synthetic client sits in alpha: its default Pane and Window inventories must
# exclude beta, while --all-projects and the outside-tmux compatibility path
# must include both. Project inventory itself remains global.
binding_inside_pmx() {
  env \
    HOME="$binding_root/home" \
    XDG_CONFIG_HOME="$binding_root/config" \
    XDG_STATE_HOME="$binding_root/state" \
    XDG_RUNTIME_DIR="$binding_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$binding_root/work" \
    TMUX_TMPDIR="$binding_root/tmux" \
    TMUX="$binding_socket_path,$binding_server_pid,0" \
    TMUX_PANE="$lifecycle_split_pane" \
    SHELL=/bin/sh \
    "$bin" "$@"
}

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

binding_inside_pmx get panes --all-projects -o uid >"$binding_root/all-project-panes.out"
smoke_assert_file_contains "$binding_root/all-project-panes.out" "$lifecycle_split_uid"
smoke_assert_file_contains "$binding_root/all-project-panes.out" "$binding_beta_pane_uid"
binding_pmx get panes -o uid >"$binding_root/outside-panes.out"
sort "$binding_root/all-project-panes.out" >"$binding_root/all-project-panes.sorted"
sort "$binding_root/outside-panes.out" >"$binding_root/outside-panes.sorted"
cmp "$binding_root/all-project-panes.sorted" "$binding_root/outside-panes.sorted"

binding_inside_pmx get panes --project beta -o uid >"$binding_root/explicit-beta-panes.out"
smoke_assert_file_contains "$binding_root/explicit-beta-panes.out" "$binding_beta_pane_uid"
if grep -Fqx "$lifecycle_split_uid" "$binding_root/explicit-beta-panes.out"; then
  echo "explicit --project beta included an alpha Pane" >&2
  exit 1
fi

binding_inside_pmx get projects -o name >"$binding_root/projects.out"
smoke_assert_file_contains "$binding_root/projects.out" "alpha"
smoke_assert_file_contains "$binding_root/projects.out" "beta"

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
binding_pmx internal tmux reconcile-bindings --socket-path "$binding_socket_path"
cmp "$binding_root/registry.before-repeat-hook" "$binding_registry"
if [[ "$(stat -c '%i:%s:%y' "$binding_registry")" != "$binding_registry_stat" ]]; then
  echo "repeat lifecycle convergence rewrote byte-identical registry content" >&2
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
