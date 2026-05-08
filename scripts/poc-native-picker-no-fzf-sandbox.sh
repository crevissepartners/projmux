#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="${PROJMUX_POC_NO_FZF_IMAGE:-projmux:poc-no-fzf}"
dockerfile="$root/test/docker/no-fzf-poc.Dockerfile"

echo "[poc/no-fzf] building dependency image $image"
docker build --pull=false -f "$dockerfile" -t "$image" "$root"

echo "[poc/no-fzf] entering interactive no-fzf sandbox"
docker run --rm -it \
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
  -e LANG=C.UTF-8 \
  -e LC_ALL=C.UTF-8 \
  -e PROJMUX_PICKER_BACKEND=native \
  -e PROJMUX_NATIVE_TTY_FALLBACK=1 \
  -e TERM=xterm-256color \
  -e SHELL=/bin/bash \
  "$image" \
  bash -lc 'set -euo pipefail
    go build -o /usr/local/bin/projmux ./cmd/projmux
    if command -v fzf >/dev/null 2>&1; then
      echo "unexpected fzf on PATH" >&2
      exit 1
    fi
    demo_root=/workspace/projects
    mkdir -p "$demo_root/alpha-api" "$demo_root/bravo-web" "$demo_root/charlie-tools"
    for project in alpha-api bravo-web charlie-tools; do
      if [[ ! -d "$demo_root/$project/.git" ]]; then
        git -C "$demo_root/$project" init -q
      fi
      printf "# %s\n\nno-fzf projmux sandbox project\n" "$project" > "$demo_root/$project/README.md"
    done
    export PROJMUX_PROJDIR="$demo_root"
    export PROJMUX_MANAGED_ROOTS="$demo_root"
    cd "$demo_root/alpha-api"
    cat <<'"'"'EOF'"'"'

[poc/no-fzf] launching interactive projmux shell

fzf is absent. projmux was built from the mounted POC worktree:
  /usr/local/bin/projmux

Demo project root:
  /workspace/projects

Inside tmux, try:
  projmux switch
  projmux settings
  projmux doctor --json

Environment:
  PROJMUX_PICKER_BACKEND=native
  PROJMUX_PROJDIR=/workspace/projects

Detach from tmux with Ctrl-b d. Exit the container shell to remove the sandbox.
EOF
    exec projmux shell --socket poc-no-fzf --session main --bin /usr/local/bin/projmux'
