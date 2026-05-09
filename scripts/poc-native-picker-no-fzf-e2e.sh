#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
base_image="${PROJMUX_POC_NO_FZF_BASE_IMAGE:-golang:1.24-trixie}"
image="${PROJMUX_POC_NO_FZF_IMAGE:-projmux:poc-no-fzf-go124-trixie}"
dockerfile="$root/test/docker/no-fzf-poc.Dockerfile"

echo "[poc/no-fzf] starting Docker e2e from $root"
echo "[poc/no-fzf] building dependency image $image from $base_image"
docker build --pull=false --build-arg "BASE_IMAGE=$base_image" -f "$dockerfile" -t "$image" "$root"

echo "[poc/no-fzf] running isolated no-fzf test container"

docker run --rm \
  --network none \
  -v "$root":/work \
  -w /work \
  -e HOME=/tmp/projmux-home \
  -e XDG_CACHE_HOME=/tmp/projmux-cache \
  -e XDG_CONFIG_HOME=/tmp/projmux-config \
  -e XDG_RUNTIME_DIR=/tmp/projmux-runtime \
  -e XDG_STATE_HOME=/tmp/projmux-state \
  -e GOCACHE=/tmp/projmux-gocache \
  -e GOTOOLCHAIN=local \
  -e TERM=xterm-256color \
  -e SHELL=/bin/bash \
  "$image" \
  bash -lc 'set -euo pipefail
    echo "[poc/no-fzf] assert fzf is absent"
    ! command -v fzf
    echo "[poc/no-fzf] run focused tests"
    go test ./internal/ui/picker ./internal/app -run "Native|TestSettingsNativeBackendDoesNotCallFZF|TestSwitchCommandUsesNativePickerWhenRequested"
    echo "[poc/no-fzf] build projmux"
    go build -o /tmp/projmux ./cmd/projmux
    echo "[poc/no-fzf] store native picker through Settings > Labs"
    rm -f "$XDG_CONFIG_HOME/projmux/picker-backend"
    labs_log=/tmp/projmux-settings-labs.log
    labs_stderr=/tmp/projmux-settings-labs.stderr
    labs_status=0
    { printf "\033[B\033[B\033[B\r\033[B\033[B\033[B\r"; sleep 0.3; printf "\0335"; } | timeout 8s script -q -e -E never -c "env PROJMUX_PICKER_BACKEND=native /tmp/projmux settings 2>$labs_stderr" "$labs_log" || labs_status=$?
    if [[ "$labs_status" != 0 ]]; then
      cat "$labs_log"
      cat "$labs_stderr"
      exit "$labs_status"
    fi
    if [[ -s "$labs_stderr" ]]; then
      if grep -Ev "^(error connecting to /tmp/tmux-[0-9]+/default \\(No such file or directory\\)|no server running on /tmp/tmux-[0-9]+/default)$" "$labs_stderr"; then
        exit 1
      fi
      echo "[poc/no-fzf] ignored expected tmux display-message miss in no-server container"
    fi
    test "$(cat "$XDG_CONFIG_HOME/projmux/picker-backend")" = native
    echo "[poc/no-fzf] Settings > Labs persisted native picker backend"
    echo "[poc/no-fzf] exercise native AI settings simple picker with smart-case query"
    rm -f "$XDG_CONFIG_HOME/projmux/tmux-ai-split-mode"
    ai_settings_log=/tmp/projmux-ai-settings.log
    ai_settings_status=0
    printf "Codex\r" | timeout 8s script -q -e -E never -c "/tmp/projmux ai settings" "$ai_settings_log" || ai_settings_status=$?
    if [[ "$ai_settings_status" != 0 ]]; then
      cat "$ai_settings_log"
      exit "$ai_settings_status"
    fi
    test "$(cat "$XDG_CONFIG_HOME/projmux/tmux-ai-split-mode")" = codex
    echo "[poc/no-fzf] native AI settings simple picker selected codex via smart-case query"
    echo "[poc/no-fzf] exercise native switch picker under a PTY"
    demo_root=/tmp/projmux-projects
    mkdir -p "$demo_root/alpha-api" "$demo_root/bravo-web" "$demo_root/charlie-tools"
    for project in alpha-api bravo-web charlie-tools; do
      if [[ ! -d "$demo_root/$project/.git" ]]; then
        git -C "$demo_root/$project" init -q
      fi
      printf "# %s\n" "$project" > "$demo_root/$project/README.md"
    done
    switch_log=/tmp/projmux-switch.log
    switch_status=0
    printf "bravo\r" | timeout 8s script -q -e -E never -c "env PROJMUX_PICKER_BACKEND=native PROJMUX_NATIVE_LAUNCH_KEY=alt-1 PROJMUX_PROJDIR=$demo_root PROJMUX_MANAGED_ROOTS=$demo_root /tmp/projmux switch --ui=sidebar" "$switch_log" || switch_status=$?
    if [[ "$switch_status" != 0 && "$switch_status" != 124 ]]; then
      cat "$switch_log"
      exit "$switch_status"
    fi
    if ! grep -q "/tmp/projmux-projects/bravo-web" "$switch_log"; then
      cat "$switch_log"
      echo "native switch did not open bravo-web" >&2
      exit 1
    fi
    tmux kill-server 2>/dev/null || true
    echo "[poc/no-fzf] native switch selected bravo-web"
    echo "[poc/no-fzf] exercise native switch popup preview cycle against existing sessions"
    preview_state="$XDG_STATE_HOME/projmux/preview-state"
    rm -f "$preview_state"
    tmux new-session -d -s projmux-projects-alpha-api -c "$demo_root/alpha-api"
    tmux new-session -d -s projmux-projects-bravo-web -c "$demo_root/bravo-web"
    tmux new-window -t projmux-projects-bravo-web -n inspect -c "$demo_root/bravo-web"
    tmux split-window -t projmux-projects-bravo-web:1 -h -c "$demo_root/bravo-web"
    tmux select-window -t projmux-projects-bravo-web:0
    popup_log=/tmp/projmux-switch-popup.log
    popup_status=0
    printf "bravo\033[C\033[1;3B\r" | timeout 8s script -q -e -E never -c "env PROJMUX_PICKER_BACKEND=native PROJMUX_PROJDIR=$demo_root PROJMUX_MANAGED_ROOTS=$demo_root /tmp/projmux switch --ui=popup" "$popup_log" || popup_status=$?
    if [[ "$popup_status" != 0 && "$popup_status" != 124 ]]; then
      cat "$popup_log"
      exit "$popup_status"
    fi
    if ! grep -q "/tmp/projmux-projects/bravo-web" "$popup_log"; then
      cat "$popup_log"
      echo "native switch popup did not open bravo-web" >&2
      exit 1
    fi
    if ! grep -Eq "^projmux-projects-bravo-web[[:space:]]+[0-9]+[[:space:]]+[0-9]+" "$preview_state"; then
      cat "$popup_log"
      cat "$preview_state" 2>/dev/null || true
      echo "native switch popup did not persist preview cycle window/pane state" >&2
      exit 1
    fi
    tmux kill-server 2>/dev/null || true
    echo "[poc/no-fzf] native switch popup cycled window/pane preview and selected existing bravo-web"
    echo "[poc/no-fzf] exercise native sessions picker preview cycle against existing sessions"
    rm -f "$preview_state"
    tmux new-session -d -s projmux-projects-alpha-api -c "$demo_root/alpha-api"
    tmux new-session -d -s projmux-projects-bravo-web -c "$demo_root/bravo-web"
    tmux new-window -t projmux-projects-bravo-web -n inspect -c "$demo_root/bravo-web"
    tmux split-window -t projmux-projects-bravo-web:1 -h -c "$demo_root/bravo-web"
    tmux select-window -t projmux-projects-bravo-web:0
    sessions_log=/tmp/projmux-sessions.log
    sessions_status=0
    printf "bravo\033[C\033[1;3B\r" | timeout 8s script -q -e -E never -c "env PROJMUX_PICKER_BACKEND=native /tmp/projmux sessions --ui=popup" "$sessions_log" || sessions_status=$?
    if [[ "$sessions_status" != 0 && "$sessions_status" != 124 ]]; then
      cat "$sessions_log"
      exit "$sessions_status"
    fi
    if ! grep -q "/tmp/projmux-projects/bravo-web" "$sessions_log"; then
      cat "$sessions_log"
      echo "native sessions picker did not open bravo-web" >&2
      exit 1
    fi
    if ! grep -Eq "^projmux-projects-bravo-web[[:space:]]+[0-9]+[[:space:]]+[0-9]+" "$preview_state"; then
      cat "$sessions_log"
      cat "$preview_state" 2>/dev/null || true
      echo "native sessions picker did not persist preview cycle window/pane state" >&2
      exit 1
    fi
    tmux kill-server 2>/dev/null || true
    echo "[poc/no-fzf] native sessions picker cycled window/pane preview and selected bravo-web"
    echo "[poc/no-fzf] launch projmux shell under a PTY"
    shell_config=/tmp/projmux-native-tmux.conf
    /tmp/projmux tmux print-app-config --bin /tmp/projmux > "$shell_config"
    {
      printf "set-environment -g PROJMUX_PICKER_BACKEND native\n"
      printf "set-environment -g PROJMUX_PROJDIR %q\n" "$demo_root"
      printf "set-environment -g PROJMUX_MANAGED_ROOTS %q\n" "$demo_root"
    } >> "$shell_config"
    printf -v popup_env "env PROJMUX_PICKER_BACKEND=native PROJMUX_PROJDIR=%q PROJMUX_MANAGED_ROOTS=%q" "$demo_root" "$demo_root"
    {
      printf "bind-key -n M-1 run-shell \"%s /tmp/projmux tmux popup-toggle --client #{client_tty} sessionizer-sidebar\"\n" "$popup_env"
      printf "bind-key -n User4 run-shell \"%s /tmp/projmux tmux popup-toggle --client #{client_tty} sessionizer-sidebar\"\n" "$popup_env"
    } >> "$shell_config"
    grep -q "PROJMUX_PICKER_BACKEND=native" "$shell_config"
    shell_log=/tmp/projmux-shell.log
    shell_status=0
    timeout 8s script -q -e -E never -c "env PROJMUX_PICKER_BACKEND=native /tmp/projmux shell --socket poc-no-fzf --session main --config $shell_config --no-install" "$shell_log" || shell_status=$?
    if [[ "$shell_status" != 0 && "$shell_status" != 124 ]]; then
      cat "$shell_log"
      exit "$shell_status"
    fi
    if ! tmux -L poc-no-fzf has-session -t main 2>/tmp/projmux-shell-tmux.err; then
      cat "$shell_log"
      cat /tmp/projmux-shell-tmux.err
      exit 1
    fi
    if ! tmux -L poc-no-fzf show-environment -g PROJMUX_PICKER_BACKEND | grep -qx "PROJMUX_PICKER_BACKEND=native"; then
      tmux -L poc-no-fzf show-environment -g
      exit 1
    fi
    tmux -L poc-no-fzf kill-server
    echo "[poc/no-fzf] projmux shell launched tmux session with native picker env"
    echo "[poc/no-fzf] exercise projmux shell Alt-1 popup binding"
    shell_alt_log=/tmp/projmux-shell-alt.log
    shell_alt_status=0
    { printf "\0331"; sleep 0.5; printf "bravo\r"; sleep 0.5; } | timeout 10s script -q -e -E never -c "env PROJMUX_PICKER_BACKEND=native /tmp/projmux shell --socket poc-no-fzf-alt --session main --config $shell_config --no-install" "$shell_alt_log" || shell_alt_status=$?
    if [[ "$shell_alt_status" != 0 && "$shell_alt_status" != 124 ]]; then
      cat "$shell_alt_log"
      exit "$shell_alt_status"
    fi
    if ! grep -q "bravo-web" "$shell_alt_log"; then
      cat "$shell_alt_log"
      echo "projmux shell Alt-1 popup did not render bravo-web" >&2
      exit 1
    fi
    if ! grep -q "╭" "$shell_alt_log" || ! grep -q "╰" "$shell_alt_log"; then
      cat "$shell_alt_log"
      echo "projmux shell Alt-1 popup did not render native frame borders" >&2
      exit 1
    fi
    tmux -L poc-no-fzf-alt kill-server 2>/dev/null || true
    echo "[poc/no-fzf] projmux shell Alt-1 popup rendered native switch"
    echo "[poc/no-fzf] exercise native launch key closes immediately"
    launch_close_log=/tmp/projmux-launch-close.log
    launch_close_debug=/tmp/projmux-launch-close.debug
    launch_close_status=0
    printf "\0331" | timeout 8s script -q -e -E never -c "env PROJMUX_PICKER_BACKEND=native PROJMUX_NATIVE_DEBUG_LOG=$launch_close_debug PROJMUX_NATIVE_LAUNCH_KEY=alt-1 PROJMUX_PROJDIR=$demo_root PROJMUX_MANAGED_ROOTS=$demo_root /tmp/projmux switch --ui=sidebar" "$launch_close_log" || launch_close_status=$?
    if [[ "$launch_close_status" != 0 ]]; then
      cat "$launch_close_log"
      cat "$launch_close_debug"
      exit "$launch_close_status"
    fi
    if ! grep -q "╭" "$launch_close_log" || ! grep -q "╰" "$launch_close_log"; then
      cat "$launch_close_log"
      echo "native switch did not render full frame while checking launch-key close" >&2
      exit 1
    fi
    if ! grep -q "action key=\"alt-1\" intent=\"close\"" "$launch_close_debug"; then
      cat "$launch_close_debug"
      echo "native switch did not close on immediate Alt-1 toggle" >&2
      exit 1
    fi
    echo "[poc/no-fzf] native launch key closes immediately"
    echo "[poc/no-fzf] exercise native notify sidebar printable expect key"
    /tmp/projmux notify push --text "deploy ok" --target main --source ai --id poc-notify
    notify_log=/tmp/projmux-notify.log
    notify_status=0
    printf "x" | timeout 8s script -q -e -E never -c "env PROJMUX_PICKER_BACKEND=native PROJMUX_NATIVE_LAUNCH_KEY=alt-2 /tmp/projmux notify list --ui=sidebar" "$notify_log" || notify_status=$?
    if [[ "$notify_status" != 0 ]]; then
      cat "$notify_log"
      exit "$notify_status"
    fi
    if ! grep -q "ack poc-notify" "$notify_log"; then
      cat "$notify_log"
      echo "native notify sidebar did not ack with printable expect key" >&2
      exit 1
    fi
    if grep -q "Notify >" "$notify_log"; then
      cat "$notify_log"
      echo "native notify sidebar should hide the input prompt when search is disabled" >&2
      exit 1
    fi
    echo "[poc/no-fzf] native notify sidebar acked selected row"
    echo "[poc/no-fzf] exercise native settings picker with arrow keys under a PTY"
    rm -f "$XDG_CONFIG_HOME/projmux/tmux-ai-split-mode"
    settings_log=/tmp/projmux-settings.log
    settings_stderr=/tmp/projmux-settings.stderr
    settings_status=0
    { printf "\r\033[B\033[B\033[B\r"; sleep 0.3; printf "\0335"; } | timeout 8s script -q -e -E never -c "env PROJMUX_PICKER_BACKEND=native /tmp/projmux settings 2>$settings_stderr" "$settings_log" || settings_status=$?
    if [[ "$settings_status" != 0 ]]; then
      cat "$settings_log"
      cat "$settings_stderr"
      exit "$settings_status"
    fi
    if [[ -s "$settings_stderr" ]]; then
      if grep -Ev "^(error connecting to /tmp/tmux-[0-9]+/default \\(No such file or directory\\)|no server running on /tmp/tmux-[0-9]+/default)$" "$settings_stderr"; then
        exit 1
      fi
      echo "[poc/no-fzf] ignored expected tmux display-message miss in no-server container"
    fi
    test "$(cat "$XDG_CONFIG_HOME/projmux/tmux-ai-split-mode")" = codex
    echo "[poc/no-fzf] passed"'
