#!/usr/bin/env bash
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/test/lib/smoke.sh"

smoke_setup_env
trap smoke_cleanup_env EXIT
cd "$smoke_root"

smoke_build_binary
bin="$PROJMUX_SMOKE_BIN"

# The CLI contract must propagate the pane id returned by the selected mux
# backend without scraping a second command. Keep this fake-backed check here
# so both tmux and psmux argv/output boundaries run through the built binary.
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
cat >"$fake_mux_dir/psmux" <<'FAKE_PSMUX'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$PROJMUX_FAKE_MUX_LOG"
if [[ "${3:-}" == "split-window" ]]; then
  printf '%%82\n'
fi
FAKE_PSMUX
chmod 0755 "$fake_mux_dir/tmux" "$fake_mux_dir/psmux"

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

fake_psmux_log="$PROJMUX_SMOKE_WORKDIR/fake-psmux.log"
fake_psmux_output="$(
  PROJMUX_FAKE_MUX_LOG="$fake_psmux_log" \
    PROJMUX_MUX_BACKEND="psmux" \
    PATH="$fake_mux_dir:$PATH" \
    TMUX="fake" \
    TMUX_SPLIT_TARGET_PANE="%7" \
    TMUX_SPLIT_CONTEXT_DIR="$smoke_root" \
    SHELL="/bin/sh" \
    "$bin" ai split --agent shell --print-pane-id down
)"
if [[ "$fake_psmux_output" != "%82" ]]; then
  echo "expected fake psmux pane id %82, got: $fake_psmux_output" >&2
  exit 1
fi
smoke_assert_file_contains "$fake_psmux_log" "projmux split-window -v -P -F #{pane_id} -t %7"

"$bin" doctor --json >"$PROJMUX_SMOKE_WORKDIR/doctor.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor.json" '"name": "tmux"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor.json" '"status": "ok"'

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
if [[ ! "$resources_first" =~ CPU\ --%.*MEM\ [0-9]+% ]]; then
  echo "expected first resource sample to show unavailable CPU and numeric memory, got: $resources_first" >&2
  exit 1
fi
sleep 0.1
resources_second="$("$bin" status resources)"
if [[ ! "$resources_second" =~ CPU\ [0-9]+%.*MEM\ [0-9]+% ]]; then
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

apply_out="$("$bin" tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET")"
smoke_assert_output_contains "$apply_out" "reloaded tmux server -L $PROJMUX_SMOKE_TMUX_SOCKET: 1 sessions"

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
