#!/usr/bin/env bash

smoke_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

smoke_setup_env() {
  PROJMUX_SMOKE_WORKDIR="$(mktemp -d)"
  export PROJMUX_SMOKE_WORKDIR

  export HOME="$PROJMUX_SMOKE_WORKDIR/home"
  export XDG_CACHE_HOME="$PROJMUX_SMOKE_WORKDIR/cache"
  export XDG_CONFIG_HOME="$PROJMUX_SMOKE_WORKDIR/config"
  export XDG_RUNTIME_DIR="$PROJMUX_SMOKE_WORKDIR/runtime"
  export XDG_STATE_HOME="$PROJMUX_SMOKE_WORKDIR/state"
  export GOCACHE="$PROJMUX_SMOKE_WORKDIR/go-cache"
  export GOMODCACHE="$PROJMUX_SMOKE_WORKDIR/go-mod-cache"
  export GOTOOLCHAIN=local

  mkdir -p \
    "$HOME" \
    "$XDG_CACHE_HOME" \
    "$XDG_CONFIG_HOME" \
    "$XDG_RUNTIME_DIR" \
    "$XDG_STATE_HOME" \
    "$GOCACHE" \
    "$GOMODCACHE"
  chmod 0700 "$XDG_RUNTIME_DIR"
}

smoke_cleanup_env() {
  if [[ -n "${PROJMUX_SMOKE_TMUX_SOCKET:-}" ]]; then
    tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" kill-server >/dev/null 2>&1 || true
  fi
  tmux kill-server >/dev/null 2>&1 || true
  if [[ -n "${PROJMUX_SMOKE_WORKDIR:-}" ]]; then
    rm -rf "$PROJMUX_SMOKE_WORKDIR"
  fi
}

smoke_build_binary() {
  make build BUILD_DIR="$PROJMUX_SMOKE_WORKDIR/build" PROJMUX_BIN="$PROJMUX_SMOKE_WORKDIR/build/projmux"
  PROJMUX_SMOKE_BIN="$PROJMUX_SMOKE_WORKDIR/build/projmux"
  export PROJMUX_SMOKE_BIN
}

smoke_assert_file_contains() {
  local path="$1"
  local needle="$2"
  if ! grep -Fq "$needle" "$path"; then
    echo "expected $path to contain: $needle" >&2
    exit 1
  fi
}

smoke_assert_output_contains() {
  local output="$1"
  local needle="$2"
  if [[ "$output" != *"$needle"* ]]; then
    echo "expected output to contain: $needle" >&2
    echo "$output" >&2
    exit 1
  fi
}
