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
