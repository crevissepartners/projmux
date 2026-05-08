#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="${PROJMUX_POC_NO_FZF_IMAGE:-projmux:poc-no-fzf}"
dockerfile="$root/test/docker/no-fzf-poc.Dockerfile"

echo "[poc/no-fzf] building dependency image $image"
docker build --pull=false -f "$dockerfile" -t "$image" "$root"

echo "[poc/no-fzf] entering interactive no-fzf sandbox"
docker run --rm -it \
  -v "$root":/work \
  -w /work \
  -e HOME=/tmp/projmux-home \
  -e XDG_CACHE_HOME=/tmp/projmux-cache \
  -e XDG_CONFIG_HOME=/tmp/projmux-config \
  -e XDG_RUNTIME_DIR=/tmp/projmux-runtime \
  -e XDG_STATE_HOME=/tmp/projmux-state \
  -e GOCACHE=/tmp/projmux-gocache \
  -e GOTOOLCHAIN=local \
  -e PROJMUX_PICKER_BACKEND=native \
  -e TERM=xterm-256color \
  -e SHELL=/bin/bash \
  "$image" \
  bash -lc 'set -euo pipefail
    go build -o /usr/local/bin/projmux ./cmd/projmux
    if command -v fzf >/dev/null 2>&1; then
      echo "unexpected fzf on PATH" >&2
      exit 1
    fi
    cat <<'"'"'EOF'"'"'

[poc/no-fzf] interactive sandbox ready

fzf is absent. projmux was built from the mounted POC worktree:
  /usr/local/bin/projmux

Try:
  projmux shell --socket poc-no-fzf --session main
  projmux settings
  projmux switch
  projmux doctor --json

Environment:
  PROJMUX_PICKER_BACKEND=native

EOF
    exec bash -l'
