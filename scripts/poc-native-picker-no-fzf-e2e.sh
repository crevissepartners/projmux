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
    echo "[poc/no-fzf] exercise native settings picker"
    printf "1\n4\n" | env PROJMUX_PICKER_BACKEND=native /tmp/projmux settings
    test "$(cat "$XDG_CONFIG_HOME/projmux/tmux-ai-split-mode")" = codex
    echo "[poc/no-fzf] passed"'
