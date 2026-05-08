#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "[poc/no-fzf] starting Docker e2e from $root"
echo "[poc/no-fzf] using image node:24-trixie"

docker run --rm \
  -v "$root":/work \
  -w /work \
  -e TERM=xterm-256color \
  -e SHELL=/bin/bash \
  node:24-trixie \
  bash -lc 'set -euo pipefail
    echo "[poc/no-fzf] apt-get update"
    apt-get update
    echo "[poc/no-fzf] install test dependencies"
    apt-get install -y --no-install-recommends tmux git golang-go make ncurses-bin procps ca-certificates
    echo "[poc/no-fzf] assert fzf is absent"
    ! command -v fzf
    echo "[poc/no-fzf] run focused tests"
    go test ./internal/ui/picker ./internal/app -run "Native|TestSettingsNativeBackendDoesNotCallFZF|TestSwitchCommandUsesNativePickerWhenRequested"
    echo "[poc/no-fzf] build projmux"
    go build -o /tmp/projmux ./cmd/projmux
    echo "[poc/no-fzf] exercise native settings picker"
    printf "1\n4\n" | env HOME=/tmp/projmux-home XDG_CONFIG_HOME=/tmp/projmux-home/.config PROJMUX_PICKER_BACKEND=native /tmp/projmux settings
    test "$(cat /tmp/projmux-home/.config/projmux/tmux-ai-split-mode)" = codex
    echo "[poc/no-fzf] passed"'
