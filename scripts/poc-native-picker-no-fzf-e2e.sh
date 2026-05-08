#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="${PROJMUX_POC_NO_FZF_IMAGE:-projmux:poc-no-fzf}"
dockerfile="$root/test/docker/no-fzf-poc.Dockerfile"

echo "[poc/no-fzf] starting Docker e2e from $root"
echo "[poc/no-fzf] building dependency image $image"
docker build --pull=false -f "$dockerfile" -t "$image" "$root"

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
    echo "[poc/no-fzf] launch projmux shell under a PTY"
    shell_log=/tmp/projmux-shell.log
    shell_status=0
    timeout 8s script -q -e -c "env PROJMUX_PICKER_BACKEND=native /tmp/projmux shell --socket poc-no-fzf --session main" "$shell_log" || shell_status=$?
    if [[ "$shell_status" != 0 && "$shell_status" != 124 ]]; then
      cat "$shell_log"
      exit "$shell_status"
    fi
    if ! tmux -L poc-no-fzf has-session -t main 2>/tmp/projmux-shell-tmux.err; then
      cat "$shell_log"
      cat /tmp/projmux-shell-tmux.err
      exit 1
    fi
    tmux -L poc-no-fzf kill-server
    echo "[poc/no-fzf] projmux shell launched tmux session"
    echo "[poc/no-fzf] exercise native settings picker"
    settings_stderr=/tmp/projmux-settings.stderr
    printf "1\n4\n" | env PROJMUX_PICKER_BACKEND=native /tmp/projmux settings 2>"$settings_stderr"
    if [[ -s "$settings_stderr" ]]; then
      if grep -Ev "^error connecting to /tmp/tmux-[0-9]+/default \\(No such file or directory\\)$" "$settings_stderr"; then
        exit 1
      fi
      echo "[poc/no-fzf] ignored expected tmux display-message miss in no-server container"
    fi
    test "$(cat "$XDG_CONFIG_HOME/projmux/tmux-ai-split-mode")" = codex
    echo "[poc/no-fzf] passed"'
