#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

docker run --rm -it \
  -v "$root":/work \
  -w /work \
  -e TERM=xterm-256color \
  -e SHELL=/bin/bash \
  node:24-trixie \
  bash -lc 'apt-get update &&
    apt-get install -y --no-install-recommends tmux git golang-go make ncurses-bin procps ca-certificates &&
    ! command -v fzf &&
    go test ./internal/ui/picker ./internal/app -run "Native|TestSettingsNativeBackendDoesNotCallFZF|TestSwitchCommandUsesNativePickerWhenRequested" &&
    go build -o /tmp/projmux ./cmd/projmux &&
    printf "1\n4\n" | env HOME=/tmp/projmux-home XDG_CONFIG_HOME=/tmp/projmux-home/.config PROJMUX_PICKER_BACKEND=native /tmp/projmux settings &&
    test "$(cat /tmp/projmux-home/.config/projmux/tmux-ai-split-mode)" = codex'
