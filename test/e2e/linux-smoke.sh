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
    "$bin" create pane -o pane-id --placement right
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

# 13. The compatibility bridge is untouched: with no --project the invocation is
#     still the compatibility-free split bridge, and its identity projections name the flag
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
resumed_pane_uid="$(pmx_agent_live describe agent "uid:$agent_uid" -o json | sed -n 's/.*"paneRef": "\([^"]*\)".*/\1/p' | head -n 1)"
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
binding_pmx internal tmux reconcile-bindings --socket-path "$binding_socket_path" --session "$binding_session"
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

delete_registry="$delete_root/state/projmux/metadata/registry.json"
cp "$delete_registry" "$delete_root/registry.before-dry-run"
delete_tmux list-windows -a -F '#{session_name}:#{window_id}:#{@projmux_window_uid}' \
  >"$delete_root/windows.before-dry-run"
delete_pmx delete window "uid:$delete_primary_uid" --dry-run >"$delete_root/external-dry-run.out"
cmp "$delete_root/registry.before-dry-run" "$delete_registry"
delete_tmux list-windows -a -F '#{session_name}:#{window_id}:#{@projmux_window_uid}' \
  >"$delete_root/windows.after-dry-run"
cmp "$delete_root/windows.before-dry-run" "$delete_root/windows.after-dry-run"
smoke_assert_file_contains "$delete_root/external-dry-run.out" "live would kill tmux window $delete_primary"
if grep -Fq "last live Window" "$delete_root/external-dry-run.out"; then
  echo "two-Window dry-run incorrectly predicted session termination" >&2
  exit 1
fi

delete_pmx delete window "uid:$delete_primary_uid" --yes >"$delete_root/external.out"
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
delete_pmx delete window "uid:$delete_beta_uid" --dry-run >"$delete_root/last-dry-run.out"
smoke_assert_file_contains "$delete_root/last-dry-run.out" "live cascade would end Project session delete-beta"
delete_pmx delete window "uid:$delete_beta_uid" --yes >"$delete_root/last.out"
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
$(printf %q "$bin") delete window --yes >$(printf %q "$delete_root/self.out") 2>$(printf %q "$delete_root/self.err")
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
delete_pmx delete pane "uid:$delete_split_uid" --dry-run >"$delete_root/pane-sibling-dry-run.out"
smoke_assert_file_contains "$delete_root/pane-sibling-dry-run.out" "live would kill tmux pane $delete_split pane-uid=$delete_split_uid"
if grep -Fq "last live Pane" "$delete_root/pane-sibling-dry-run.out"; then
  echo "sibling Pane dry-run incorrectly predicted Window termination" >&2
  exit 1
fi
delete_pmx delete pane "uid:$delete_split_uid" --yes >"$delete_root/pane-sibling.out"
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
delete_pmx delete pane "uid:$delete_agent_pane_uid" --yes >"$delete_root/managed-pane.out"
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
delete_pmx delete agent "uid:$delete_agent_two_uid" --dry-run >"$delete_root/agent-dry-run.out"
smoke_assert_file_contains "$delete_root/agent-dry-run.out" "cascade pane/"
smoke_assert_file_contains "$delete_root/agent-dry-run.out" "live would kill tmux pane $delete_agent_two_pane pane-uid=$delete_agent_two_pane_uid"
delete_pmx delete agent "uid:$delete_agent_two_uid" --yes >"$delete_root/agent.out"
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

# Deleting the sole Pane predicts and causes both implicit Window and session
# teardown. A following reconciliation must not mint the deleted Pane back.
delete_last_window="$(delete_tmux new-window -d -t delete-alpha: -n pane-last -c "$delete_root/work/alpha" -P -F '#{window_id}' sleep 600)"
delete_last_pane="$(delete_tmux display-message -p -t "$delete_last_window" '#{pane_id}')"
delete_last_pane_uid="$(delete_tmux show-options -pqv -t "$delete_last_pane" @projmux_pane_uid)"
echo ">> delete last Pane Window target pane=$delete_last_pane uid=$delete_last_pane_uid window=$delete_last_window"
delete_pmx delete pane "uid:$delete_last_pane_uid" --dry-run >"$delete_root/pane-last-dry-run.out"
smoke_assert_file_contains "$delete_root/pane-last-dry-run.out" "live cascade would end Window $delete_last_window because its last live Pane is deleted"
delete_pmx delete pane "uid:$delete_last_pane_uid" --yes >"$delete_root/pane-last.out"
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
delete_pmx delete pane "uid:$delete_gamma_pane_uid" --dry-run >"$delete_root/pane-session-last-dry-run.out"
smoke_assert_file_contains "$delete_root/pane-session-last-dry-run.out" "live cascade would end Window $delete_gamma_window because its last live Pane is deleted"
smoke_assert_file_contains "$delete_root/pane-session-last-dry-run.out" "live cascade would end Project session delete-gamma because its last live Window is deleted"
delete_pmx delete pane "uid:$delete_gamma_pane_uid" --yes >"$delete_root/pane-session-last.out"
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
delete_other_after="$(delete_other_tmux show-options -gqv @projmux_delete_sentinel):$(delete_other_tmux list-windows -a -F '#{session_name}:#{window_id}')"
if [[ "$delete_other_after" != "$delete_other_before" ]]; then
  echo "Pane/Agent delete touched the foreign socket" >&2
  exit 1
fi

for canonical_call in \
  "-L $delete_product_socket list-windows -a" \
  "-L $delete_product_socket kill-window -t $delete_primary" \
  "-L $delete_product_socket run-shell -b" \
  "-L $delete_product_socket list-panes -a" \
  "-L $delete_product_socket kill-pane -t $delete_split" \
  "-L $delete_product_socket kill-pane -t $delete_agent_two_pane"; do
  if ! grep -Fq -- "$canonical_call" "$delete_shim_log"; then
    echo "delete Window e2e did not observe canonical exact routing: $canonical_call" >&2
    cat "$delete_shim_log" >&2
    exit 1
  fi
done

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
