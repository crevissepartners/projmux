#!/usr/bin/env bash
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/test/lib/smoke.sh"

smoke_setup_env
trap smoke_cleanup_env EXIT
cd "$smoke_root"

smoke_build_binary
bin="$PROJMUX_SMOKE_BIN"

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
    --target "$recorder_session:0.0" \
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
sequence_window="$(tmux -L "$recorder_socket" new-window -d -t "$recorder_session:" -n sequence-input \
  -P -F '#{window_id}' "/bin/sh -c 'stty raw -echo; cat >\"$sequence_capture\"'")"
tmux -L "$recorder_socket" select-window -t "$sequence_window"
smoke_wait_for "sequence capture pane" test -e "$sequence_capture"
install -m 0644 "$smoke_root/test/fixtures/keymaps/sequences-v2.toml" "$XDG_CONFIG_HOME/projmux/keymap.toml"
"$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$recorder_socket" \
  >"$PROJMUX_SMOKE_WORKDIR/sequence-e2e-apply.out"

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
# Canonical resource deletion intentionally inventories the app-owned `projmux`
# socket. TMUX_TMPDIR keeps this exact name isolated below the smoke root.
create_socket="projmux"
create_foreign_socket="projmux-create-foreign-$$-$RANDOM"
mkdir -p "$create_root/tt" "$create_root/state" "$create_root/config" "$create_root/work/alpha" "$create_root/work/beta"
create_real_tmux="$(command -v tmux)"

ctx() { env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$create_root/tt" "$create_real_tmux" -L "$create_socket" "$@"; }
cfx() { env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$create_root/tt" "$create_real_tmux" -L "$create_foreign_socket" "$@"; }

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
cfx new-session -d -s foreign-agent -c "$create_root/work/beta" sleep 600
foreign_agent_pane="$(cfx display-message -p -t foreign-agent '#{pane_id}')"
cfx select-pane -T codex -t "$foreign_agent_pane"
cfx set-option -p -t "$foreign_agent_pane" @projmux_ai_topic foreign-sentinel
cfx set-option -p -t "$foreign_agent_pane" @projmux_ai_state foreign-state
foreign_agent_before="$(cfx display-message -p -t "$foreign_agent_pane" '#{pane_title}|#{@projmux_ai_topic}|#{@projmux_ai_state}')"

create_socket_path="$(ctx display-message -p -t legacy-alpha '#{socket_path}')"
case "$create_socket_path" in
  "$create_root"/*) ;;
  *)
    echo "create e2e socket escaped the smoke root: $create_socket_path" >&2
    exit 1
    ;;
esac
echo ">> create e2e socket=$create_socket path=$create_socket_path"
create_foreign_socket_path="$(cfx display-message -p -t foreign-agent '#{socket_path}')"
case "$create_foreign_socket_path" in
  "$create_root"/*) ;;
  *)
    echo "foreign create e2e socket escaped the smoke root: $create_foreign_socket_path" >&2
    exit 1
    ;;
esac

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

# 0. The explicit Project bootstrap. A directory under the discovery root is a
# candidate until something registers it, so `beta` is registered by name here
# rather than appearing as a side effect of the first create below.
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
# Its siblings under the same discovery root stay unregistered.
pmx get projects -o json >"$create_root/projects-after-bootstrap.json"
if grep -Fq "$create_root/work/alpha" "$create_root/projects-after-bootstrap.json"; then
  echo "the explicit bootstrap also registered the sibling candidate alpha" >&2
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
    ctx set-option -t "$phase0_session" -q @projmux_project_path "$create_root/work/alpha"
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
if [[ "$legacy_window_name_before" == "window" ]]; then
  echo "legacy Window display fixture did not differ from its stable name" >&2
  exit 1
fi
pmx get windows --project alpha >"$create_root/alpha-windows-table.out"
smoke_assert_file_contains "$create_root/alpha-windows-table.out" "DISPLAY NAME  NAME"
if ! awk -v display="$legacy_window_name_before" '
  NR == 1 {
    name_column = index($0, "  NAME  ") + 2
    next
  }
  NR == 2 {
    display_cell = substr($0, 1, name_column - 3)
    sub(/[[:space:]]+$/, "", display_cell)
    name_cell = substr($0, name_column)
    sub(/[[:space:]].*$/, "", name_cell)
    found = display_cell == display && name_cell == "window"
  }
  END { exit !found }
' "$create_root/alpha-windows-table.out"; then
  echo "legacy Window table did not show displayName first and stable name second:" >&2
  cat "$create_root/alpha-windows-table.out" >&2
  exit 1
fi
pmx describe window window -p alpha >"$create_root/alpha-window.describe"
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

# Lifecycle create deliberately does not auto-claim an unrelated blank legacy
# session. The explicit repair route establishes its exact Project ownership
# before subsequent creates select it.
pmx reconcile resources --socket "$create_socket" -o json >"$create_root/alpha-reconcile.json"
if [[ -z "$(ctx show-options -qv -t legacy-alpha @projmux_project_uid)" ]]; then
  echo "explicit resource reconcile did not establish legacy-alpha Project ownership" >&2
  cat "$create_root/alpha-reconcile.json" >&2
  exit 1
fi

# tmux 3.4 reports one @N/%N row for every session that links the Window. A
# pre-existing linked Window must remain one handle with an owner set, while a
# newly returned @N/%N is still uniquely attributable to legacy-alpha.
ctx new-session -d -s linked-observer -c "$create_root/work/alpha" sleep 600
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
# A payload-bearing create now requires bounded provider/hook acknowledgement.
# Model the public hook path itself so acknowledgement is committed through the
# Agent Registry authority, without reading or synthesizing pane content.
if [[ " \$* " == *" --topic release triage "* ]]; then
  # The create route binds its initial idle projection immediately after the
  # process starts; emit the provider acknowledgement after that binding.
  sleep 0.2
  printf '{"hook_event_name":"UserPromptSubmit","thread_id":"phase6-activation-thread","session_id":"phase6-activation-session","cwd":"%s"}' "\$PWD" |
    HOME=$(printf %q "$agent_home") \
    XDG_STATE_HOME=$(printf %q "$create_root/state") \
    XDG_CONFIG_HOME=$(printf %q "$create_root/config") \
    PROJMUX_MANAGED_ROOTS=$(printf %q "$create_root/work") \
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
    PROJMUX_MANAGED_ROOTS="$create_root/work" \
    SHELL=/bin/bash \
    "$bin" "$@"
}

pmx_agent_live() {
  PATH="$agent_home/.local/bin:$PATH" \
    HOME="$agent_home" \
    TMUX="$create_socket_path,1,0" \
    TMUX_PANE="$agent_pane" \
    TMUX_TMPDIR="$create_root/tt" \
    XDG_STATE_HOME="$create_root/state" \
    XDG_CONFIG_HOME="$create_root/config" \
    PROJMUX_MANAGED_ROOTS="$create_root/work" \
    SHELL=/bin/bash \
    "$bin" "$@"
}

pmx_agent_hook_at() {
  local hook_pane="$1"
  shift
  PATH="$agent_home/.local/bin:$PATH" \
    HOME="$agent_home" \
    TMUX="$create_socket_path,1,0" \
    TMUX_PANE="$hook_pane" \
    TMUX_TMPDIR="$create_root/tt" \
    XDG_STATE_HOME="$create_root/state" \
    XDG_CONFIG_HOME="$create_root/config" \
    PROJMUX_MANAGED_ROOTS="$create_root/work" \
    SHELL=/bin/bash \
    "$bin" "$@"
}

agent_window_before="$(ctx display-message -p -t legacy-alpha '#{window_id}')"
agent_pane_before="$(ctx display-message -p -t legacy-alpha '#{pane_id}')"

pmx_agent create agent --provider codex -p alpha -w window -o pane-id \
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
pmx_agent create codex -p alpha -w window -o name >"$create_root/agent-second.out"
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
pmx_agent create agent --provider codex -p alpha -w window -o name \
  -- -p payload-project -w payload-window --topic "release triage" >"$create_root/agent-payload.out"
if [[ "$(tr -d '[:space:]' <"$create_root/agent-payload.out")" != "codex-2" ]]; then
  echo "a payload changed the Agent name: $(cat "$create_root/agent-payload.out")" >&2
  exit 1
fi
for _ in {1..200}; do
  [[ -s "$create_root/agent-launch.log" ]] && break
  sleep 0.05
done
if ! grep -Fq -- "args=-C $create_root/work/alpha -p payload-project -w payload-window --topic release triage" "$create_root/agent-launch.log"; then
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

# Drive each provider through the sole canonical ingress on the exact inherited
# pane. The live projections prove the provider-specific event paths still
# share their pre-retirement payload/state behavior.
claude_hook_pane="$(ctx split-window -d -P -F '#{pane_id}' -t legacy-alpha -c "$create_root/work/alpha" sleep 600)"
printf '%s' '{"hook_event_name":"UserPromptSubmit","session_id":"phase6-claude","cwd":"'"$create_root"'/work/alpha"}' |
  pmx_agent_hook_at "$claude_hook_pane" internal agent-hook ingest claude-hook >"$create_root/agent-claude-ingest.out"
if [[ -s "$create_root/agent-claude-ingest.out" ]] ||
  [[ "$(ctx show-options -pqv -t "$claude_hook_pane" @projmux_ai_agent)" != claude ]] ||
  [[ "$(ctx show-options -pqv -t "$claude_hook_pane" @projmux_ai_state)" != thinking ]]; then
  echo "canonical Claude hook ingest lost its state projection parity" >&2
  exit 1
fi
antigravity_hook_pane="$(ctx split-window -d -P -F '#{pane_id}' -t legacy-alpha -c "$create_root/work/alpha" sleep 600)"
printf '%s' '{"conversationId":"phase6-antigravity","workspacePaths":["'"$create_root"'/work/alpha"]}' |
  pmx_agent_hook_at "$antigravity_hook_pane" internal agent-hook ingest antigravity-hook --event PreInvocation \
    >"$create_root/agent-antigravity-ingest.out"
smoke_assert_file_contains "$create_root/agent-antigravity-ingest.out" '{}'
if [[ "$(ctx show-options -pqv -t "$antigravity_hook_pane" @projmux_ai_agent)" != antigravity ]] ||
  [[ "$(ctx show-options -pqv -t "$antigravity_hook_pane" @projmux_ai_state)" != thinking ]]; then
  echo "canonical Antigravity hook ingest lost its state projection parity" >&2
  exit 1
fi

# Seed the durable Codex conversation through the same canonical ingress, then
# take the Agent offline, change its Registry-only topic, and resume it.
printf '%s' '{"hook_event_name":"UserPromptSubmit","thread_id":"phase6-thread","session_id":"phase6-session","turn_id":"phase6-turn","cwd":"'"$create_root"'/work/alpha"}' |
  pmx_agent_live internal agent-hook ingest codex-hook
pmx_agent_live delete pane "uid:$agent_pane_uid" --yes
pmx_agent_live agent topic set "offline resume topic" "uid:$agent_uid"
pmx_agent_live agent resume "uid:$agent_uid" >"$create_root/agent-resume.out"
smoke_assert_file_contains "$create_root/agent-resume.out" "resumed"
# This read runs from the pane the delete above just killed, so its Projmux
# identity mirror is gone. An explicit --project is what keeps it resolvable:
# a singular reference derives its Project namespace from the active Window,
# and naming the Project skips that observation entirely.
resumed_pane_uid="$(pmx_agent_live describe agent "uid:$agent_uid" -p alpha -o json | sed -n 's/.*"paneRef": "\([^"]*\)".*/\1/p' | head -n 1)"
resumed_pane="$(ctx list-panes -a -F '#{@projmux_pane_uid} #{pane_id}' | awk -v uid="$resumed_pane_uid" '$1 == uid { print $2; exit }')"
if [[ -z "$resumed_pane" ]] ||
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
cross_agent_name="$(pmx_agent create agent --provider codex --project alpha --window window \
  --cwd "$create_root/work/beta" --add-dir "$create_root/work/alpha" -o name)"
smoke_assert_file_contains "$create_root/agent-launch.log" "args=-C $create_root/work/beta --add-dir $create_root/work/alpha"
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
if ! grep -Fq "cwd=$create_root/work/alpha" "$create_root/agent-launch.log"; then
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
unattributed_pane="$(ctx new-window -d -P -F '#{pane_id}' -t legacy-alpha -c "$create_root/work/alpha" sleep 600)"
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
  PROJMUX_MANAGED_ROOTS="$create_root/work" \
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
binding_pmx internal tmux apply --bin "$bin" --config "$binding_config" --socket "$binding_socket" \
  >"$binding_root/first-apply.out"
smoke_assert_file_contains "$binding_root/first-apply.out" "reloaded tmux server -L $binding_socket"
# The lifecycle apply imports legacy Window/Pane topology but deliberately does
# not turn blank Project session identity into create ownership. Establish that
# ownership through the explicit exact-socket repair route before create tests.
binding_pmx reconcile resources --socket "$binding_socket" -o json >"$binding_root/first-reconcile.json"

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
binding_pmx internal tmux converge --socket-path "$binding_socket_path" --session "$binding_session" \
  --reason runtime-created 2>"$binding_root/converge-repeat.err"
smoke_assert_file_contains "$binding_root/converge-repeat.err" "converged=true"
cmp "$binding_root/registry.before-repeat-hook" "$binding_registry"
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
    --session "$binding_session" --reason runtime-exited \
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
# This block owns a fresh smoke root. The public command routes every live read
# and mutation through `-L projmux`; TMUX_TMPDIR makes that named socket unique
# to this run, and cleanup verifies the actual socket path before using -S.
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

delete_tmux new-session -d -s delete-alpha -n primary -c "$delete_root/work/alpha" sleep 600
delete_tmux set-option -t delete-alpha -q @projmux_project_path "$delete_root/work/alpha"
delete_tmux new-window -d -t delete-alpha: -n sibling -c "$delete_root/work/alpha" sleep 600
delete_tmux new-session -d -s delete-beta -n only -c "$delete_root/work/beta" sleep 600
delete_tmux set-option -t delete-beta -q @projmux_project_path "$delete_root/work/beta"
delete_other_tmux new-session -d -s foreign-delete -n untouched sleep 600
delete_other_tmux set-option -gq @projmux_delete_sentinel untouched

delete_socket_path="$(delete_tmux display-message -p -t delete-alpha '#{socket_path}')"
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
delete_pmx internal tmux apply --bin "$bin" --config "$delete_config" --socket "$delete_socket" \
  >"$delete_root/apply.out"
smoke_assert_file_contains "$delete_root/apply.out" "reloaded tmux server -L $delete_socket"
# Creation must not auto-import a blank legacy Project session. Exercise the
# canonical explicit repair route before the later create/delete parity cases.
delete_pmx reconcile resources --socket "$delete_socket" -o json >"$delete_root/reconcile.out"

delete_primary="$(delete_tmux display-message -p -t delete-alpha:primary '#{window_id}')"
delete_sibling="$(delete_tmux display-message -p -t delete-alpha:sibling '#{window_id}')"
delete_beta="$(delete_tmux display-message -p -t delete-beta:only '#{window_id}')"
delete_primary_uid="$(delete_tmux show-options -wqv -t "$delete_primary" @projmux_window_uid)"
delete_sibling_uid="$(delete_tmux show-options -wqv -t "$delete_sibling" @projmux_window_uid)"
delete_beta_uid="$(delete_tmux show-options -wqv -t "$delete_beta" @projmux_window_uid)"
delete_alpha_project_uid="$(delete_pmx describe project alpha -o uid)"
if [[ -z "$delete_primary_uid" || -z "$delete_sibling_uid" || -z "$delete_beta_uid" || -z "$delete_alpha_project_uid" ]]; then
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
delete_server_pid="$(delete_tmux display-message -p -t delete-alpha:primary '#{pid}')"
delete_primary_pane="$(delete_tmux display-message -p -t delete-alpha:primary '#{pane_id}')"
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

# The legacy import allocates a stable Window metadata.name independently of
# the tmux window_name, so the references are read back from the Registry
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

# The only beta Window explicitly predicts and then causes the Project session
# to end; alpha remains live and untouched.
delete_pmx_delete window "uid:$delete_beta_uid" --dry-run >"$delete_root/last-dry-run.out"
smoke_assert_file_contains "$delete_root/last-dry-run.out" "live cascade would end Project session delete-beta"
delete_pmx_delete window "uid:$delete_beta_uid" --yes >"$delete_root/last.out"
if delete_tmux has-session -t delete-beta 2>/dev/null; then
  echo "last-Window delete left delete-beta session live" >&2
  exit 1
fi
smoke_assert_file_contains "$delete_root/last.out" "live cascade ended Project session delete-beta"

# A managed runner Window invokes implicit delete from inside itself. There is
# intentionally no post-command marker: a correct implementation flushes the
# CLI result first and then the queued exact kill removes the shell that would
# have written such a marker.
self_script="$delete_root/self-delete.sh"
cat >"$self_script" <<SELF_DELETE
#!/usr/bin/env bash
sleep 0.25
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
self_window="$(delete_tmux new-window -d -t delete-alpha: -n self-delete -c "$delete_root/work/alpha" -P -F '#{window_id}' "$self_script")"
self_uid="$(delete_tmux show-options -wqv -t "$self_window" @projmux_window_uid)"
if [[ -z "$self_uid" ]]; then
  echo "self-target Window was not synchronously bound" >&2
  exit 1
fi
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

# Raw runtime loss leaves desired topology in the Registry. Canonical Window
# delete must retire that graph without issuing a tmux kill. Include a shell
# Pane and Agent descendant, then prove dry-run, repeat, sibling, and socket
# containment around the Registry-only path. The pane-exited lifecycle may
# already retire the Agent's dead managed Pane before this command; the unit
# cascade fixture separately pins the full Agent-owned Pane case.
delete_offline_window="$(delete_tmux new-window -d -t delete-alpha: -n offline-delete -c "$delete_root/work/alpha" -P -F '#{window_id}' sleep 600)"
delete_offline_window_uid="$(delete_tmux show-options -wqv -t "$delete_offline_window" @projmux_window_uid)"
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
if grep -Fq -- "-L $delete_product_socket kill-window -t $delete_offline_window" "$delete_shim_log"; then
  echo "offline Window canonical delete issued a tmux kill" >&2
  exit 1
fi

cp "$delete_registry" "$delete_root/registry.before-offline-repeat"
if delete_pmx_delete window "uid:$delete_offline_window_uid" --project "uid:$delete_alpha_project_uid" --yes \
  >"$delete_root/offline-repeat.out" 2>"$delete_root/offline-repeat.err"; then
  echo "repeat offline Window delete unexpectedly succeeded" >&2
  exit 1
fi
cmp "$delete_root/registry.before-offline-repeat" "$delete_registry"
smoke_assert_file_contains "$delete_root/offline-repeat.err" "matched no windows"

# Deleting the sole Pane predicts and causes both implicit Window and session
# teardown. A following reconciliation must not mint the deleted Pane back.
delete_last_window="$(delete_tmux new-window -d -t delete-alpha: -n pane-last -c "$delete_root/work/alpha" -P -F '#{window_id}' sleep 600)"
delete_last_pane="$(delete_tmux display-message -p -t "$delete_last_window" '#{pane_id}')"
delete_last_pane_uid="$(delete_tmux show-options -pqv -t "$delete_last_pane" @projmux_pane_uid)"
echo ">> delete last Pane Window target pane=$delete_last_pane uid=$delete_last_pane_uid window=$delete_last_window"
delete_pmx_delete pane "uid:$delete_last_pane_uid" --dry-run >"$delete_root/pane-last-dry-run.out"
smoke_assert_file_contains "$delete_root/pane-last-dry-run.out" "live cascade would end Window $delete_last_window because its last live Pane is deleted"
delete_pmx_delete pane "uid:$delete_last_pane_uid" --yes >"$delete_root/pane-last.out"
if [[ "$(delete_tmux display-message -p -t "$delete_last_window" '#{window_id}' 2>/dev/null || true)" == "$delete_last_window" ]]; then
  echo "last-Pane delete left Window $delete_last_window live" >&2
  exit 1
fi

# A fresh one-Window Project makes the same last-Pane cascade end the complete
# session, which must be visible before mutation and in the durable result.
delete_tmux new-session -d -s delete-gamma -n only -c "$delete_root/work/gamma" sleep 600
delete_tmux set-option -t delete-gamma -q @projmux_project_path "$delete_root/work/gamma"
delete_pmx internal tmux apply --bin "$bin" --config "$delete_config" --socket "$delete_socket" >"$delete_root/apply-gamma.out"
delete_gamma_pane="$(delete_tmux display-message -p -t delete-gamma:only '#{pane_id}')"
delete_gamma_pane_uid="$(delete_tmux show-options -pqv -t "$delete_gamma_pane" @projmux_pane_uid)"
delete_gamma_window="$(delete_tmux display-message -p -t "$delete_gamma_pane" '#{window_id}')"
echo ">> delete last Pane session target pane=$delete_gamma_pane uid=$delete_gamma_pane_uid window=$delete_gamma_window session=delete-gamma"
delete_pmx_delete pane "uid:$delete_gamma_pane_uid" --dry-run >"$delete_root/pane-session-last-dry-run.out"
smoke_assert_file_contains "$delete_root/pane-session-last-dry-run.out" "live cascade would end Window $delete_gamma_window because its last live Pane is deleted"
smoke_assert_file_contains "$delete_root/pane-session-last-dry-run.out" "live cascade would end Project session delete-gamma because its last live Window is deleted"
delete_pmx_delete pane "uid:$delete_gamma_pane_uid" --yes >"$delete_root/pane-session-last.out"
if delete_tmux has-session -t delete-gamma 2>/dev/null; then
  echo "last-Pane delete left delete-gamma session live" >&2
  exit 1
fi
smoke_assert_file_contains "$delete_root/pane-session-last.out" "live cascade ended Project session delete-gamma"

delete_pmx internal tmux apply --bin "$bin" --config "$delete_config" --socket "$delete_socket" >"$delete_root/apply-after-pane-delete.out"
delete_pmx get panes --all-projects -o uid >"$delete_root/panes.after-reconcile"
for deleted_uid in "$delete_split_uid" "$delete_agent_pane_uid" "$delete_agent_two_pane_uid" "$delete_last_pane_uid" "$delete_gamma_pane_uid"; do
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
# Stability is the honest signal: two identical reads a beat apart mean no worker
# is still landing a pass.
delete_settled=0
cp "$delete_registry" "$delete_root/registry.settle-probe"
for _ in {1..100}; do
  sleep 0.05
  if cmp -s "$delete_root/registry.settle-probe" "$delete_registry"; then
    delete_settled=1
    break
  fi
  cp "$delete_registry" "$delete_root/registry.settle-probe"
done
if [[ "$delete_settled" != "1" ]]; then
  echo "hook-driven convergence never settled after kill-server" >&2
  exit 1
fi
cp "$delete_registry" "$delete_root/registry.before-no-server-dry-run"
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

# Every live half of every delete above ran on the socket the invocation named.
for canonical_call in \
  "-L $delete_socket list-windows -a" \
  "-L $delete_socket kill-window -t $delete_primary" \
  "-L $delete_socket run-shell -b" \
  "-L $delete_socket list-panes -a" \
  "-L $delete_socket kill-pane -t $delete_split" \
  "-L $delete_socket kill-pane -t $delete_agent_two_pane"; do
  if ! grep -Fq -- "$canonical_call" "$delete_shim_log"; then
    echo "delete Window e2e did not observe canonical exact routing: $canonical_call" >&2
    cat "$delete_shim_log" >&2
    exit 1
  fi
done
# And nothing reached the app socket by its default name. The shim would have
# mapped `-L projmux` onto this very server, so the fallback the route used to
# perform is only detectable here, in the calls it made rather than in what they
# did.
if grep -Fq -- "-L $delete_product_socket " "$delete_shim_log"; then
  echo "a delete fell back to the default app socket -L $delete_product_socket" >&2
  grep -F -- "-L $delete_product_socket " "$delete_shim_log" >&2
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
rename_session="rename-alpha"
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
rename_base_pmx internal tmux apply --bin "$bin" --config "$rename_config" --socket "$rename_socket" >"$rename_root/apply.out"
rename_base_pmx reconcile resources --socket "$rename_socket" >"$rename_root/initial-reconcile.out"
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
rename_agent_pane="$(PATH="$rename_root/shim:$PATH" rename_pmx create agent --provider codex --project "uid:$rename_project_uid" --window "uid:$rename_window_uid" -o pane-id)"
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
rename_base_pmx reconcile resources --socket "$rename_socket" >"$rename_root/offline-reconcile.out"
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
rename_base_pmx reconcile resources --socket "$rename_socket" >"$rename_root/failure-retry.out"
if [[ "$(rename_tmux show-options -qv -t "$rename_session" @projmux_project_name)" != retry-project ]]; then
  echo "public retry did not repair failed live Project name mirror" >&2
  exit 1
fi
rename_base_pmx reconcile resources --socket "$rename_socket" -o json >"$rename_root/repeat.json"
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
topology_session="topology-alpha"
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

topology_pmx internal tmux apply --bin "$bin" --config "$topology_root/config/projmux/tmux.conf" --socket "$topology_socket" >"$topology_root/apply.out"
topology_pmx reconcile resources --socket "$topology_socket" >"$topology_root/import.out"

topology_project_uid="$(topology_tmux show-options -qv -t "$topology_session" @projmux_project_uid)"
if [[ -z "$topology_project_uid" ]]; then
  echo "topology e2e import left the Project uid empty" >&2
  exit 1
fi
topology_live_pmx create window --project "uid:$topology_project_uid" --name review >"$topology_root/create-window.out"
# The stored command is recorded as a one-time name seed. Materialization must
# never execute it, which the recreated Pane's start command proves below: it
# names the managed process supervisor over the default shell, never this.
topology_stored_command=(sleep 600)
topology_live_pmx create pane --project "uid:$topology_project_uid" --window review --placement right -o pane-id -- "${topology_stored_command[@]}" >"$topology_root/create-pane.out"

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
  local previous="" current
  for _ in $(seq 1 100); do
    current="$(sha256sum "$topology_registry" | cut -d' ' -f1)"
    if [[ -n "$previous" && "$current" == "$previous" ]]; then
      return 0
    fi
    previous="$current"
    sleep 0.1
  done
  echo "the Registry never settled after a live pane was killed" >&2
  exit 1
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
topology_pane_uids_after="$(topology_tmux list-panes -s -t "$topology_session" -F '#{@projmux_pane_uid}' | sort)"
if [[ "$topology_pane_uids_after" != "$topology_pane_uids" ]]; then
  echo "live partial materialization did not restore the exact Registry Pane uids" >&2
  printf 'want=%s got=%s\n' "$topology_pane_uids" "$topology_pane_uids_after" >&2
  exit 1
fi

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
topology_pane_uids="$(topology_pmx get panes --project "uid:$topology_project_uid" -o uid | sort)"

# 4. An offline Project is dormant, not deleted. Explicit materialization on the
# exact socket rebuilds the whole stored topology under its original uids.
topology_tmux kill-server >/dev/null 2>&1 || true
topology_pmx reconcile resources --socket "$topology_socket" --materialize-project "uid:$topology_project_uid" -o json >"$topology_root/offline-full.json"
topology_window_uids_after="$(topology_tmux list-windows -t "$topology_session" -F '#{@projmux_window_uid}' | sort)"
topology_pane_uids_after="$(topology_tmux list-panes -s -t "$topology_session" -F '#{@projmux_pane_uid}' | sort)"
if [[ "$topology_window_uids_after" != "$topology_window_uids" ]] || [[ "$topology_pane_uids_after" != "$topology_pane_uids" ]]; then
  echo "offline full materialization did not restore the exact Registry topology" >&2
  printf 'windows want=%s got=%s\npanes want=%s got=%s\n' \
    "$topology_window_uids" "$topology_window_uids_after" "$topology_pane_uids" "$topology_pane_uids_after" >&2
  exit 1
fi
if [[ "$(topology_tmux show-options -qv -t "$topology_session" @projmux_project_uid)" != "$topology_project_uid" ]]; then
  echo "offline full materialization did not restore the exact Project uid binding" >&2
  exit 1
fi
topology_pmx reconcile resources --socket "$topology_socket" --materialize-project "uid:$topology_project_uid" -o json >"$topology_root/offline-repeat.json"
smoke_assert_file_contains "$topology_root/offline-repeat.json" '"outcome": "no-op"'

# 5. No Agent was resumed and no stored command was executed anywhere.
if topology_pmx get agents --project "uid:$topology_project_uid" -o json 2>/dev/null | grep -Fq '"phase": "Running"'; then
  echo "materialization started an Agent" >&2
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

# Closed Project topology startup parity gets its own exact two-socket
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
export PATH="$startup_root/bin:\$PATH"
$(printf %q "$bin") switch open "\$1" >"$startup_root/open-\$2.out" 2>"$startup_root/open-\$2.err"
echo \$? >"$startup_root/open-\$2.rc"
STARTUP_OPEN_SCRIPT
chmod 0755 "$startup_root/open-project.sh"

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

startup_pmx internal tmux apply --bin "$bin" --config "$startup_root/config/projmux/tmux.conf" --socket "$startup_socket" >"$startup_root/apply.out"
startup_pmx reconcile resources --socket "$startup_socket" >"$startup_root/import.out"

startup_project_uid="$(startup_tmux show-options -qv -t "$startup_session" @projmux_project_uid)"
if [[ -z "$startup_project_uid" ]]; then
  echo "startup e2e import left the Project uid empty" >&2
  exit 1
fi
# The stored command is a one-time name seed. A startup that executed it would
# show up inside the recreated Pane's start command below, which must name only
# the managed process supervisor over the default shell.
startup_stored_command=(sleep 600)
startup_live_pmx create window --project "uid:$startup_project_uid" --name review >"$startup_root/create-window.out"
startup_live_pmx create pane --project "uid:$startup_project_uid" --window review --placement right -o pane-id -- "${startup_stored_command[@]}" >"$startup_root/create-pane.out"

startup_window_uids="$(startup_pmx get windows --project "uid:$startup_project_uid" -o uid | sort)"
startup_pane_uids="$(startup_pmx get panes --project "uid:$startup_project_uid" -o uid | sort)"
if [[ "$(printf '%s\n' "$startup_window_uids" | wc -l)" != "2" ]] || [[ "$(printf '%s\n' "$startup_pane_uids" | wc -l)" != "3" ]]; then
  echo "startup e2e fixture did not build two Windows and three shell Panes" >&2
  printf 'windows=%s panes=%s\n' "$startup_window_uids" "$startup_pane_uids" >&2
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
startup_wait_for "closed Project topology open" test -s "$startup_root/open-topology.rc"
if [[ "$(tr -d '[:space:]' <"$startup_root/open-topology.rc")" != "0" ]]; then
  echo "closed Project topology open failed" >&2
  cat "$startup_root/open-topology.err" >&2 || true
  exit 1
fi

startup_window_uids_after="$(startup_tmux list-windows -t "$startup_session" -F '#{@projmux_window_uid}' | sort)"
startup_pane_uids_after="$(startup_tmux list-panes -s -t "$startup_session" -F '#{@projmux_pane_uid}' | sort)"
if [[ "$startup_window_uids_after" != "$startup_window_uids" ]] || [[ "$startup_pane_uids_after" != "$startup_pane_uids" ]]; then
  echo "closed Project open did not materialize the exact Registry topology" >&2
  printf 'windows want=%s got=%s\npanes want=%s got=%s\n' \
    "$startup_window_uids" "$startup_window_uids_after" "$startup_pane_uids" "$startup_pane_uids_after" >&2
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
if startup_pmx get agents --project "uid:$startup_project_uid" -o json 2>/dev/null | grep -Fq '"phase": "Running"'; then
  echo "closed Project open started an Agent" >&2
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

startup_other_after="$(startup_other_tmux show-options -gqv @projmux_startup_sentinel):$(startup_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"
if [[ "$startup_other_after" != "$startup_other_before" ]]; then
  echo "closed Project topology startup touched the sibling socket" >&2
  exit 1
fi

exec 8>&-
kill "$startup_client_pid" >/dev/null 2>&1 || true
wait "$startup_client_pid" 2>/dev/null || true
startup_cleanup
trap smoke_cleanup_env EXIT
echo ">> Closed Project topology startup e2e passed: socket=$startup_socket path=$startup_socket_path other-socket=$startup_other_socket other-path=$startup_other_socket_path client=$startup_client cleanup=validated-exact-sockets"

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
rtd_session="runtime-alpha"
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
# tmux renames a window to its foreground command on its own. That is tmux
# writing, not projmux, and leaving it on would make the zero-write comparison
# below fail the moment a client attaches and a shell starts.
rtd_tmux set-option -g -q automatic-rename off
rtd_pmx reconcile resources --socket "$rtd_socket" >"$rtd_root/import.out"
rtd_project_uid="$(rtd_tmux show-options -t "$rtd_session" -qv @projmux_project_uid)"
if [[ -z "$rtd_project_uid" ]]; then
  echo "runtime diagnostics e2e import left the Project uid empty" >&2
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
nav_root="$PROJMUX_SMOKE_WORKDIR/registry-navigation-e2e"
nav_socket="projmux-registry-nav-$$-$RANDOM"
nav_other_socket="projmux-registry-nav-other-$$-$RANDOM"
nav_session="nav-alpha"
nav_beta_session="nav-beta"
nav_driver="nav-driver"
mkdir -p "$nav_root/tmux" "$nav_root/state" "$nav_root/config" "$nav_root/home" \
  "$nav_root/runtime" "$nav_root/work/alpha" "$nav_root/work/beta"
chmod 0700 "$nav_root/runtime"
nav_real_tmux="$(command -v tmux)"

nav_tmux() { env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$nav_root/tmux" "$nav_real_tmux" -L "$nav_socket" "$@"; }
nav_other_tmux() { env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$nav_root/tmux" "$nav_real_tmux" -L "$nav_other_socket" "$@"; }
nav_pmx() {
  env -u TMUX -u TMUX_PANE \
    HOME="$nav_root/home" \
    XDG_CONFIG_HOME="$nav_root/config" \
    XDG_STATE_HOME="$nav_root/state" \
    XDG_RUNTIME_DIR="$nav_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$nav_root/work" \
    TMUX_TMPDIR="$nav_root/tmux" \
    SHELL=/bin/sh \
    "$bin" "$@"
}

nav_tmux new-session -d -s "$nav_session" -c "$nav_root/work/alpha" sleep 600
nav_tmux set-option -t "$nav_session" -q @projmux_project_path "$nav_root/work/alpha"
# A second managed Project, imported in the same reconcile so the Registry holds
# both in a known slice order. Only one of them is live at a time later, which is
# what makes the presentation tiers observable at all.
nav_tmux new-session -d -s "$nav_beta_session" -c "$nav_root/work/beta" sleep 600
nav_tmux set-option -t "$nav_beta_session" -q @projmux_project_path "$nav_root/work/beta"
nav_tmux new-session -d -s "$nav_driver" -c "$nav_root" bash --noprofile --norc
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
    actual="$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$nav_root/tmux" tmux -L "$socket" display-message -p '#{socket_path}' 2>/dev/null || true)"
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
# tmux renames a window to its foreground command on its own. That is tmux
# writing, not projmux, and it would break the zero-write comparison below.
nav_tmux set-option -g -q automatic-rename off
nav_pmx reconcile resources --socket "$nav_socket" >"$nav_root/import.out"
nav_project_uid="$(nav_tmux show-options -t "$nav_session" -qv @projmux_project_uid)"
nav_beta_uid="$(nav_tmux show-options -t "$nav_beta_session" -qv @projmux_project_uid)"
for nav_uid in "$nav_project_uid" "$nav_beta_uid"; do
  if [[ -z "$nav_uid" ]]; then
    echo "registry navigation e2e import left a Project uid empty" >&2
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
  "TERM=xterm-256color env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$nav_root/tmux' tmux -L '$nav_socket' attach-session -t '$nav_driver'" \
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
  "test -n \"\$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR='$nav_root/tmux' tmux -L '$nav_socket' list-clients -F '#{client_name}' 2>/dev/null | head -n 1)\""
nav_client="$(nav_tmux list-clients -F '#{client_name}' | head -n 1)"

# One 80x24 popup run of the Projects sidebar, rendered through the exact
# inherited socket the client is attached to.
nav_open_projects() {
  local offset_var="$1"
  printf -v "$offset_var" '%s' "$(stat -c %s "$nav_client_log")"
  nav_tmux display-popup -c "$nav_client" -T "Projects E2E" -w 80 -h 24 -E \
    "env -u TMUX_PANE HOME='$nav_root/home' XDG_CONFIG_HOME='$nav_root/config' XDG_STATE_HOME='$nav_root/state' XDG_RUNTIME_DIR='$nav_root/runtime' PROJMUX_MANAGED_ROOTS='$nav_root/work' TMUX_TMPDIR='$nav_root/tmux' TMUX='$nav_socket_path,$nav_socket_pid,0' SHELL=/bin/sh '$bin' switch --ui=sidebar" &
  nav_popup_pid=$!
}

nav_open_projects nav_popup_offset
nav_screen_has() {
  tail -c +$((nav_popup_offset + 1)) "$nav_client_log" | grep -aFq "$1"
}
nav_wait_for "Projects sidebar" nav_screen_has "Projects"
# The Runtime link is a row of the Projects list, and it says what it leads to.
# The tally is the observation that the runtime-only objects on this server are
# present and reachable while being absent from the managed rows.
nav_wait_for "Runtime link row" nav_screen_has "Runtime -"
nav_wait_for "Runtime link control tally" nav_screen_has "control 1"
nav_wait_for "Runtime link ephemeral tally" nav_screen_has "ephemeral 1"
nav_wait_for "Runtime link recoverable tally" nav_screen_has "recoverable 1"

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
nav_wait_for "managed Project row" nav_filter_has "work/alpha"

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
printf '\003' >&8
printf '\003' >&8
nav_wait_for "Projects popup exit" sh -c "! kill -0 '$nav_popup_pid' 2>/dev/null"
wait "$nav_popup_pid" || true

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
    "env HOME='$nav_root/home' XDG_CONFIG_HOME='$nav_root/config' XDG_STATE_HOME='$nav_root/state' XDG_RUNTIME_DIR='$nav_root/runtime' PROJMUX_MANAGED_ROOTS='$nav_root/work' TMUX_TMPDIR='$nav_root/tmux' SHELL=/bin/sh '$bin' switch --ui=sidebar")"
}
nav_close_order_pane() {
  nav_tmux kill-session -t "=$nav_order_session" >/dev/null 2>&1 || true
  nav_order_pane=""
}
nav_order_sequence() {
  nav_tmux capture-pane -p -t "$nav_order_pane" 2>/dev/null \
    | grep -aoE 'work/(alpha|beta)' \
    | sed -e 's|work/||' \
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
      if (!project && (index(line, "work/alpha") > 0 || index(line, "work/beta") > 0)) { project = NR } }
    END { exit !(home && project && home < project) }'
}
nav_registry_project_order() {
  grep -a '"root":' "$nav_registry" \
    | sed -e 's|.*/work/||' -e 's|".*||' \
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
nav_wait_for "offline Project row" nav_offline_filter_has "work/alpha"
nav_offline_offset="$(stat -c %s "$nav_client_log")"
printf '\022' >&8
nav_offline_has() {
  tail -c +$((nav_offline_offset + 1)) "$nav_client_log" | grep -aFq "$1"
}
nav_wait_for "offline hierarchy" nav_offline_has "Projects > Resources"
nav_wait_for "offline hierarchy row" nav_offline_has "offline"
nav_wait_for "offline start action" nav_offline_has "start"
printf '\003' >&8
printf '\003' >&8
nav_wait_for "offline Projects popup exit" sh -c "! kill -0 '$nav_popup_pid' 2>/dev/null"
wait "$nav_popup_pid" || true

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

# 3b. Home is chrome. Opening it opens a session and registers nothing, because
# `$HOME` alone has never been evidence of a Project.
disc_tmux send-keys -t "$disc_driver_pane" "bash '$disc_root/open-candidate.sh' '$disc_root/home' home" Enter
disc_wait_for "Home open" test -s "$disc_root/open-home.rc"
if [[ "$(tr -d '[:space:]' <"$disc_root/open-home.rc")" != "0" ]]; then
  echo "opening Home failed" >&2
  cat "$disc_root/open-home.err" >&2 || true
  exit 1
fi
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
exitrec_session="exitrec-alpha"
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
  env -u TMUX_PANE \
    HOME="$exitrec_root/home" \
    XDG_CONFIG_HOME="$exitrec_root/config" \
    XDG_STATE_HOME="$exitrec_root/state" \
    XDG_RUNTIME_DIR="$exitrec_root/runtime" \
    PROJMUX_MANAGED_ROOTS="$exitrec_root/work" \
    TMUX_TMPDIR="$exitrec_root/tmux" \
    TMUX="$exitrec_socket_path,$exitrec_socket_pid,0" \
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
# `pane-exited` and `after-kill-pane` hooks in place; every convergence below is
# driven by them and by nothing this script runs.
exitrec_pmx internal tmux apply --bin "$bin" \
  --config "$exitrec_root/config/projmux/tmux.conf" --socket "$exitrec_socket" \
  >"$exitrec_root/apply.out"
exitrec_pmx reconcile resources --socket "$exitrec_socket" >"$exitrec_root/import.out"
exitrec_project_uid="$(exitrec_tmux show-options -qv -t "$exitrec_session" @projmux_project_uid)"
if [[ -z "$exitrec_project_uid" ]]; then
  echo "exit reconciliation e2e import left the Project uid empty" >&2
  exit 1
fi
if ! exitrec_tmux show-hooks -g | grep -q "internal tmux converge --socket-path"; then
  echo "the generated config installed no controller trigger, so hook-driven convergence cannot be observed" >&2
  exitrec_tmux show-hooks -g >&2
  exit 1
fi
# All four lifecycle hooks reach the one entrypoint, and none of them retains a
# route of its own. This is the trigger inventory as the live server holds it,
# which is a stronger statement than the same assertion over rendered text: it
# also proves `apply` sourced them.
# `pane-exited` is a window-scoped hook in current tmux, so `show-hooks -g` does
# not list it while it lists `after-kill-pane`. Reading both tables is what makes
# this the whole live trigger inventory rather than the half of it that happens to
# be server-global.
exitrec_hooks="$(exitrec_tmux show-hooks -g; exitrec_tmux show-hooks -gw)"
for exitrec_hook in pane-exited after-kill-pane after-new-window after-split-window; do
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

# 1. A provider that exits non-zero converges to Failed on the hook alone.
printf '%s\n' 'exit 42' >"$exitrec_root/stub-script"
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

# 2. A provider that exits 0 converges to Offline, not Failed, and keeps its
# conversation pointer if one was ever reported. Exit 0 is `normal`, never
# `intentional`.
printf '%s\n' 'exit 0' >"$exitrec_root/stub-script"
exitrec_clean_agent="$(exitrec_live_pmx create agent --provider claude \
  --project "uid:$exitrec_project_uid" -o uid)"
exitrec_await_phase agent "$exitrec_clean_agent" Offline
if [[ "$(exitrec_termination_field classification)" != "normal" ]]; then
  echo "the hook classified an exit 0 as $(exitrec_termination_field classification), want normal" >&2
  cat "$exitrec_root/doc.json" >&2
  exit 1
fi
echo ">> exit reconciliation e2e hook-driven clean exit agent=$exitrec_clean_agent phase=Offline class=normal"

# 3. The read surfaces project what the hook stored, and write nothing.
exitrec_registry="$exitrec_root/state/projmux/metadata/registry.json"
exitrec_settle_registry() {
  local previous="" current
  for _ in $(seq 1 150); do
    current="$(sha256sum "$exitrec_registry" | cut -d' ' -f1)"
    if [[ -n "$previous" && "$current" == "$previous" ]]; then
      return 0
    fi
    previous="$current"
    sleep 0.2
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

# 4. Whole-server loss, then restart on the same socket. The reconciliation after
# the restart converges the logical graph and starts nothing: an observation is
# not an activation authority.
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
exitrec_pmx reconcile resources --socket "$exitrec_socket" -o json >"$exitrec_root/after-restart.json"
exitrec_doc pane "$exitrec_shell_pane"
if [[ -z "$(cat "$exitrec_root/doc.json")" ]]; then
  echo "the restart deleted the logical shell Pane $exitrec_shell_pane" >&2
  exit 1
fi
case "$(exitrec_termination_field classification)" in
  abnormal|unknown) ;;
  *)
    echo "the restarted host classified the lost pane as '$(exitrec_termination_field classification)', want abnormal or unknown" >&2
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

# 5. The sibling socket was never read or written.
exitrec_other_after="$(exitrec_other_tmux show-options -gqv @projmux_exitrec_sentinel):$(exitrec_other_tmux list-windows -a -F '#{session_name}:#{window_name}')"
if [[ "$exitrec_other_after" != "$exitrec_other_before" ]]; then
  echo "exit reconciliation e2e touched the sibling socket: $exitrec_other_after" >&2
  exit 1
fi

exitrec_cleanup
trap smoke_cleanup_env EXIT
echo ">> exit reconciliation e2e passed: socket=$exitrec_socket path=$exitrec_socket_path other-path=$exitrec_other_socket_path project=$exitrec_project_uid cleanup=validated-exact-sockets"
