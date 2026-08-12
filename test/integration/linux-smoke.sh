#!/usr/bin/env bash
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/test/lib/smoke.sh"

smoke_setup_env
trap smoke_cleanup_env EXIT
cd "$smoke_root"

smoke_build_binary
bin="$PROJMUX_SMOKE_BIN"

# The CLI contract must propagate the pane id returned by tmux without scraping
# a second command. Keep this fake-backed check at the built-binary boundary.
fake_mux_dir="$PROJMUX_SMOKE_WORKDIR/fake-mux"
mkdir -p "$fake_mux_dir"
cat >"$fake_mux_dir/tmux" <<'FAKE_TMUX'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$PROJMUX_FAKE_MUX_LOG"
if [[ "${1:-}" == "split-window" ]]; then
  printf '%%81\n'
fi
FAKE_TMUX
chmod 0755 "$fake_mux_dir/tmux"

fake_tmux_log="$PROJMUX_SMOKE_WORKDIR/fake-tmux.log"
fake_tmux_output="$(
  PROJMUX_FAKE_MUX_LOG="$fake_tmux_log" \
    PATH="$fake_mux_dir:$PATH" \
    TMUX="fake" \
    TMUX_SPLIT_TARGET_PANE="%7" \
    TMUX_SPLIT_CONTEXT_DIR="$smoke_root" \
    SHELL="/bin/sh" \
    "$bin" ai split --agent shell --print-pane-id right
)"
if [[ "$fake_tmux_output" != "%81" ]]; then
  echo "expected fake tmux pane id %81, got: $fake_tmux_output" >&2
  exit 1
fi
smoke_assert_file_contains "$fake_tmux_log" "split-window -P -F #{pane_id} -h -t %7"

"$bin" doctor --json >"$PROJMUX_SMOKE_WORKDIR/doctor.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor.json" '"schema_version": 1'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor.json" '"name": "tmux"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor.json" '"status": "ok"'

"$bin" doctor --json --section deps --verbose >"$PROJMUX_SMOKE_WORKDIR/doctor-deps.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor-deps.json" '"dependencies"'
if grep -Eq '"(ai_notify_integrations|session_state_resume|runtime|logs)"' "$PROJMUX_SMOKE_WORKDIR/doctor-deps.json"; then
  echo "doctor deps projection leaked another section" >&2
  exit 1
fi

"$bin" doctor --section runtime >"$PROJMUX_SMOKE_WORKDIR/doctor-runtime.txt"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor-runtime.txt" "No runtime checks in schema version 1"

removed_flag_stderr="$PROJMUX_SMOKE_WORKDIR/doctor-removed-flag.err"
operations_log="$XDG_STATE_HOME/projmux/logs/operations.jsonl"
operations_before="$PROJMUX_SMOKE_WORKDIR/operations-before.jsonl"
if [[ -f "$operations_log" ]]; then
  cp "$operations_log" "$operations_before"
fi
legacy_hook="$XDG_CONFIG_HOME/projmux/hooks/post-create"
mkdir -p "$(dirname "$legacy_hook")"
printf 'printf legacy-hook-must-stay\n' >"$legacy_hook"
if "$bin" doctor --install-missing >"$PROJMUX_SMOKE_WORKDIR/doctor-removed-flag.out" 2>"$removed_flag_stderr"; then
  echo "removed doctor --install-missing unexpectedly succeeded" >&2
  exit 1
fi
smoke_assert_file_contains "$removed_flag_stderr" "flag provided but not defined: -install-missing"
smoke_assert_file_contains "$removed_flag_stderr" "projmux doctor is read-only; remove --install-missing and run displayed remediation guidance explicitly outside doctor"
smoke_assert_file_contains "$legacy_hook" "legacy-hook-must-stay"
if [[ -e "$legacy_hook.bak" || -e "$XDG_CONFIG_HOME/projmux/config.toml" ]]; then
  echo "removed doctor flag invocation mutated legacy hook state" >&2
  exit 1
fi
if [[ -f "$operations_before" ]]; then
  cmp "$operations_before" "$operations_log"
elif [[ -e "$operations_log" ]]; then
  echo "removed doctor flag invocation wrote an operational outcome" >&2
  exit 1
fi

"$bin" tmux print-config --bin "$bin" >"$PROJMUX_SMOKE_WORKDIR/projmux.conf"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/projmux.conf" "status notify --max-width 80"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/projmux.conf" "tmux popup-toggle"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/projmux.conf" "notify-sidebar"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/projmux.conf" "set -g @projmux_live_resources off"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/projmux.conf" "status resources"

mkdir -p "$XDG_CONFIG_HOME/projmux"
printf 'on\n' >"$XDG_CONFIG_HOME/projmux/live-resources"
"$bin" tmux print-config --bin "$bin" >"$PROJMUX_SMOKE_WORKDIR/projmux-resources.conf"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/projmux-resources.conf" "set -g @projmux_live_resources on"

resources_first="$("$bin" status resources)"
if [[ ! "$resources_first" =~ CPU[[:space:]]{2}--%.*MEM[[:space:]]{1,3}[0-9]{1,3}% ]] ||
  [[ "$resources_first" =~ (normal|warning|critical|unknown) ]]; then
  echo "expected first resource sample to show unavailable CPU and numeric memory, got: $resources_first" >&2
  exit 1
fi
sleep 0.1
resources_second="$("$bin" status resources)"
if [[ ! "$resources_second" =~ CPU[[:space:]]{1,3}[0-9]{1,3}%.*MEM[[:space:]]{1,3}[0-9]{1,3}% ]] ||
  [[ "$resources_second" =~ (normal|warning|critical|unknown) ]]; then
  echo "expected second resource sample to show numeric CPU and memory, got: $resources_second" >&2
  exit 1
fi

"$bin" tmux install \
  --bin "$bin" \
  --config "$HOME/.tmux.conf" \
  --include "$XDG_CONFIG_HOME/tmux/projmux.conf" \
  >"$PROJMUX_SMOKE_WORKDIR/install.out"
smoke_assert_file_contains "$HOME/.tmux.conf" "source-file"
smoke_assert_file_contains "$XDG_CONFIG_HOME/tmux/projmux.conf" "unbind-key -q F"

PROJMUX_SMOKE_TMUX_SOCKET="projmux-it"
export PROJMUX_SMOKE_TMUX_SOCKET
tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" new-session -d -s integration-smoke -c "$smoke_root" sleep 300

# A pre-rename-pane-label config could leave C-t installed in the live root
# table even after the retired keymap action disappeared. Seed that exact stale
# shape plus an unrelated live binding before apply.
keymap_path="$XDG_CONFIG_HOME/projmux/keymap.toml"
install -m 0644 "$smoke_root/test/fixtures/keymaps/stale-pane-label-current.toml" "$keymap_path"
cp "$keymap_path" "$PROJMUX_SMOKE_WORKDIR/keymap-mp.before"
tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" bind-key -n C-t command-prompt -p "pane label:" "set-option -p @projmux_pane_label '%%'"
tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" bind-key -n M-F12 display-message "unrelated-live-binding"

apply_out="$("$bin" tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET")"
smoke_assert_output_contains "$apply_out" "reloaded tmux server -L $PROJMUX_SMOKE_TMUX_SOCKET: 1 sessions"

if stale_ct="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root C-t 2>/dev/null)"; then
  echo "expected stale C-t pane-label binding to be absent after apply, got: $stale_ct" >&2
  exit 1
fi
mp_binding="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root M-p)"
if [[ "$mp_binding" != *"command-prompt"* || "$mp_binding" != *"pane label:"* || "$mp_binding" != *"@projmux_pane_label"* ]]; then
  echo "expected current M-p pane-label binding after apply, got: $mp_binding" >&2
  exit 1
fi
unrelated_binding="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root M-F12)"
if [[ "$unrelated_binding" != *"unrelated-live-binding"* ]]; then
  echo "expected unrelated live binding to survive apply, got: $unrelated_binding" >&2
  exit 1
fi
ma_binding="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root M-a)"
if [[ "$ma_binding" != *"sessionizer-sidebar"* ]]; then
  echo "expected unrelated current M-a binding to survive apply, got: $ma_binding" >&2
  exit 1
fi
cmp "$PROJMUX_SMOKE_WORKDIR/keymap-mp.before" "$keymap_path"
cp "$XDG_CONFIG_HOME/projmux/tmux.conf" "$PROJMUX_SMOKE_WORKDIR/tmux-mp.first"

repeat_apply_out="$("$bin" tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET")"
smoke_assert_output_contains "$repeat_apply_out" "reloaded tmux server -L $PROJMUX_SMOKE_TMUX_SOCKET: 1 sessions"
cmp "$PROJMUX_SMOKE_WORKDIR/tmux-mp.first" "$XDG_CONFIG_HOME/projmux/tmux.conf"
cmp "$PROJMUX_SMOKE_WORKDIR/keymap-mp.before" "$keymap_path"
if tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root C-t >/dev/null 2>&1; then
  echo "repeated apply restored retired C-t binding" >&2
  exit 1
fi

# A current action may explicitly reclaim C-t. The retired cleanup must render
# and execute first so the current new-window binding wins without retaining
# the stale pane-label body.
install -m 0644 "$smoke_root/test/fixtures/keymaps/stale-pane-label-ct-reassigned.toml" "$keymap_path"
cp "$keymap_path" "$PROJMUX_SMOKE_WORKDIR/keymap-ct-reassigned.before"
reassign_apply_out="$("$bin" tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET")"
smoke_assert_output_contains "$reassign_apply_out" "reloaded tmux server -L $PROJMUX_SMOKE_TMUX_SOCKET: 1 sessions"
ct_binding="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root C-t)"
if [[ "$ct_binding" != *"new-window"* || "$ct_binding" == *"pane label:"* || "$ct_binding" == *"@projmux_pane_label"* ]]; then
  echo "expected current C-t new-window binding without stale pane-label body, got: $ct_binding" >&2
  exit 1
fi
if [[ "$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root M-p)" != *"pane label:"* ]]; then
  echo "expected M-p pane-label binding to remain after C-t reassignment" >&2
  exit 1
fi
if [[ "$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root M-F12)" != *"unrelated-live-binding"* ]]; then
  echo "expected unrelated live binding to survive C-t reassignment apply" >&2
  exit 1
fi
cmp "$PROJMUX_SMOKE_WORKDIR/keymap-ct-reassigned.before" "$keymap_path"
cp "$XDG_CONFIG_HOME/projmux/tmux.conf" "$PROJMUX_SMOKE_WORKDIR/tmux-ct-reassigned.first"
"$bin" tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET" \
  >"$PROJMUX_SMOKE_WORKDIR/reassign-repeat-apply.out"
cmp "$PROJMUX_SMOKE_WORKDIR/tmux-ct-reassigned.first" "$XDG_CONFIG_HOME/projmux/tmux.conf"
cmp "$PROJMUX_SMOKE_WORKDIR/keymap-ct-reassigned.before" "$keymap_path"
ct_binding="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root C-t)"
if [[ "$ct_binding" != *"new-window"* || "$ct_binding" == *"pane label:"* ]]; then
  echo "repeated reassignment apply changed C-t ownership: $ct_binding" >&2
  exit 1
fi

app_flag="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv @projmux_app)"
if [[ "$app_flag" != "1" ]]; then
  echo "expected tmux apply to set @projmux_app=1, got: $app_flag" >&2
  exit 1
fi
resources_flag="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv @projmux_live_resources)"
if [[ "$resources_flag" != "on" ]]; then
  echo "expected tmux apply to set @projmux_live_resources=on, got: $resources_flag" >&2
  exit 1
fi

"$bin" notify push --id integration-smoke --text "integration smoke" --target "integration-smoke" >"$PROJMUX_SMOKE_WORKDIR/notify-push.out"
"$bin" notify list --json >"$PROJMUX_SMOKE_WORKDIR/notify-list.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/notify-list.json" "integration smoke"
"$bin" notify ack integration-smoke >"$PROJMUX_SMOKE_WORKDIR/notify-ack.out"

# Operational outcomes persist in the isolated XDG state tree. Read-only hot
# paths and the viewer itself must not append, while an injected journal path
# failure must not change a successful state-changing command.
operations_log="$XDG_STATE_HOME/projmux/logs/operations.jsonl"
if [[ ! -f "$operations_log" ]]; then
  echo "expected operational diagnostics log: $operations_log" >&2
  exit 1
fi
if [[ "$(stat -c '%a' "$XDG_STATE_HOME/projmux")" != "700" ]] ||
  [[ "$(stat -c '%a' "$XDG_STATE_HOME/projmux/logs")" != "700" ]] ||
  [[ "$(stat -c '%a' "$operations_log")" != "600" ]]; then
  echo "expected private 0700/0700/0600 operational diagnostics modes" >&2
  exit 1
fi

before_read_only="$(wc -l <"$operations_log")"
"$bin" status resources >"$PROJMUX_SMOKE_WORKDIR/resources-read-only.out"
"$bin" diagnostics log --tail 1 --json --level info --component cli \
  >"$PROJMUX_SMOKE_WORKDIR/operations-tail.jsonl"
after_read_only="$(wc -l <"$operations_log")"
if [[ "$before_read_only" != "$after_read_only" ]]; then
  echo "read-only status/viewer success unexpectedly appended an operational event" >&2
  exit 1
fi
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/operations-tail.jsonl" '"component":"cli"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/operations-tail.jsonl" '"result":"success"'

# Generated hooks and status formats call these six automatic paths. Exercise
# each against the isolated tmux server and prove its successful top-level
# outcome does not grow the operational journal.
automatic_pane="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p -t integration-smoke '#{pane_id}')"
automatic_window="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p -t integration-smoke '#{window_id}')"
automatic_socket="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p -t integration-smoke '#{socket_path}')"
automatic_server_pid="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p -t integration-smoke '#{pid}')"
automatic_tmux_env="$automatic_socket,$automatic_server_pid,0"

assert_automatic_success_no_record() {
  local label="$1"
  shift
  local before after
  before="$(wc -l <"$operations_log")"
  "$@" >"$PROJMUX_SMOKE_WORKDIR/automatic-$label.out"
  after="$(wc -l <"$operations_log")"
  if [[ "$before" != "$after" ]]; then
    echo "$label success unexpectedly appended an operational event" >&2
    exit 1
  fi
}

assert_automatic_success_no_record ai-ingest \
  env TMUX="$automatic_tmux_env" "$bin" ai ingest bell --pane "$automatic_pane"
assert_automatic_success_no_record attention-arm \
  env TMUX="$automatic_tmux_env" "$bin" attention arm "$automatic_pane"
assert_automatic_success_no_record attention-clear \
  env TMUX="$automatic_tmux_env" "$bin" attention clear "$automatic_pane"
assert_automatic_success_no_record attention-window \
  env TMUX="$automatic_tmux_env" "$bin" attention window "$automatic_window"
assert_automatic_success_no_record session-state-autosave \
  env TMUX="$automatic_tmux_env" "$bin" tmux autosave-session-state --quiet
assert_automatic_success_no_record recent-window-record \
  env TMUX="$automatic_tmux_env" "$bin" window record

set +e
"$bin" unknown-fixture-command >"$PROJMUX_SMOKE_WORKDIR/unknown.out" 2>"$PROJMUX_SMOKE_WORKDIR/unknown.err"
unknown_status=$?
set -e
if [[ "$unknown_status" != "1" ]]; then
  echo "expected unknown command exit 1, got: $unknown_status" >&2
  exit 1
fi
"$bin" diagnostics log --tail 1 --json --level error \
  >"$PROJMUX_SMOKE_WORKDIR/operations-error.jsonl"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/operations-error.jsonl" '"result":"error"'
if grep -Fq 'unknown-fixture-command' "$PROJMUX_SMOKE_WORKDIR/operations-error.jsonl"; then
  echo "operational error record leaked raw argv" >&2
  exit 1
fi

chmod 0755 "$XDG_STATE_HOME/projmux" "$XDG_STATE_HOME/projmux/logs"
chmod 0644 "$operations_log"
"$bin" pin add "$smoke_root" >"$PROJMUX_SMOKE_WORKDIR/pin-add.out"
if [[ "$(stat -c '%a' "$XDG_STATE_HOME/projmux")" != "700" ]] ||
  [[ "$(stat -c '%a' "$XDG_STATE_HOME/projmux/logs")" != "700" ]] ||
  [[ "$(stat -c '%a' "$operations_log")" != "600" ]]; then
  echo "operational diagnostics did not repair private modes" >&2
  exit 1
fi

blocked_state="$PROJMUX_SMOKE_WORKDIR/blocked-state"
printf 'not-a-directory\n' >"$blocked_state"
set +e
XDG_STATE_HOME="$blocked_state" "$bin" pin add "$smoke_root" \
  >"$PROJMUX_SMOKE_WORKDIR/pin-add-blocked-log.out" \
  2>"$PROJMUX_SMOKE_WORKDIR/pin-add-blocked-log.err"
blocked_status=$?
set -e
if [[ "$blocked_status" != "0" ]]; then
  echo "best-effort operational writer failure changed exit code: $blocked_status" >&2
  exit 1
fi
if [[ -s "$PROJMUX_SMOKE_WORKDIR/pin-add-blocked-log.err" ]]; then
  echo "best-effort operational writer failure leaked to command stderr" >&2
  exit 1
fi
if [[ "$(cat "$PROJMUX_SMOKE_WORKDIR/pin-add-blocked-log.out")" != "pinned: $smoke_root" ]]; then
  echo "best-effort operational writer failure changed command stdout" >&2
  exit 1
fi
