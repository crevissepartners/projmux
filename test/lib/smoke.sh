#!/usr/bin/env bash

smoke_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

smoke_setup_env() {
  PROJMUX_SMOKE_WORKDIR="$(mktemp -d)"
  export PROJMUX_SMOKE_WORKDIR

  # A smoke run must never inherit the caller's tmux client identity.  Keep
  # every bare tmux command, as well as every named -L socket, below a
  # run-unique directory that cleanup can validate before touching a server.
  unset TMUX TMUX_PANE
  PROJMUX_SMOKE_TMUX_ROOT="$PROJMUX_SMOKE_WORKDIR/tmux"
  export PROJMUX_SMOKE_TMUX_ROOT
  export TMUX_TMPDIR="$PROJMUX_SMOKE_TMUX_ROOT"

  export HOME="$PROJMUX_SMOKE_WORKDIR/home"
  export XDG_CACHE_HOME="$PROJMUX_SMOKE_WORKDIR/cache"
  export XDG_CONFIG_HOME="$PROJMUX_SMOKE_WORKDIR/config"
  export XDG_RUNTIME_DIR="$PROJMUX_SMOKE_WORKDIR/runtime"
  export XDG_STATE_HOME="$PROJMUX_SMOKE_WORKDIR/state"
  export TMPDIR="$PROJMUX_SMOKE_WORKDIR/tmp"
  export GOCACHE="$PROJMUX_SMOKE_WORKDIR/go-cache"
  export PROJMUX_USAGE_STATE_DIR="$PROJMUX_SMOKE_WORKDIR/usage"
  # Suites build without network access, so the module cache must already hold
  # the checked-in module graph. An inherited GOMODCACHE (the harness mounts a
  # prefetched one) wins; otherwise fall back to a run-local cache, which is
  # only sufficient when the build needs no external modules.
  export GOMODCACHE="${GOMODCACHE:-$PROJMUX_SMOKE_WORKDIR/go-mod-cache}"
  export GOTOOLCHAIN=local

  mkdir -p \
    "$HOME" \
    "$XDG_CACHE_HOME" \
    "$XDG_CONFIG_HOME" \
    "$XDG_RUNTIME_DIR" \
    "$XDG_STATE_HOME" \
    "$TMPDIR" \
    "$TMUX_TMPDIR" \
    "$GOCACHE" \
    "$GOMODCACHE" \
    "$PROJMUX_USAGE_STATE_DIR"
  chmod 0700 "$XDG_RUNTIME_DIR" "$TMUX_TMPDIR"
}

smoke_cleanup_tmux_server() {
  local socket_name="${1:-}"
  local actual=""
  local -a tmux_command=(env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$PROJMUX_SMOKE_TMUX_ROOT" tmux)

  if [[ -n "$socket_name" ]]; then
    actual="$("${tmux_command[@]}" -L "$socket_name" display-message -p '#{socket_path}' 2>/dev/null || true)"
  else
    actual="$("${tmux_command[@]}" display-message -p '#{socket_path}' 2>/dev/null || true)"
  fi
  if [[ -z "$actual" ]]; then
    return
  fi

  case "$actual" in
    "$PROJMUX_SMOKE_TMUX_ROOT"/*) ;;
    *)
      echo "refusing smoke cleanup outside run-local tmux root: $actual" >&2
      return 1
      ;;
  esac

  # Keep an exact audit record before killing through -S.  A socket name is
  # never itself accepted as cleanup authority.
  printf '%s\n' "$actual" >>"$PROJMUX_SMOKE_WORKDIR/tmux-cleanup-targets"
  "${tmux_command[@]}" -S "$actual" kill-server >/dev/null 2>&1 || true
}

smoke_cleanup_env() {
  local cleanup_status=0
  if [[ -n "${PROJMUX_SMOKE_TMUX_SOCKET:-}" ]]; then
    smoke_cleanup_tmux_server "$PROJMUX_SMOKE_TMUX_SOCKET" || cleanup_status=$?
  fi
  smoke_cleanup_tmux_server || cleanup_status=$?
  if [[ "$cleanup_status" != "0" ]]; then
    echo "preserving smoke workdir after refused tmux cleanup: $PROJMUX_SMOKE_WORKDIR" >&2
    return "$cleanup_status"
  fi
  if [[ -n "${PROJMUX_SMOKE_WORKDIR:-}" ]]; then
    # Go module downloads are intentionally read-only. Make only this validated
    # run-local tree writable again so direct/local smoke cleanup can remove it.
    chmod -R u+w "$PROJMUX_SMOKE_WORKDIR" 2>/dev/null || true
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

# smoke_assert_file_lacks fails when the file contains needle. It is the
# negative half used by the CLI internal-namespace audit: proving a generated
# config no longer emits a relocated route needs an absence assertion, not
# another presence assertion.
smoke_assert_file_lacks() {
  local path="$1"
  local needle="$2"
  if grep -Fq "$needle" "$path"; then
    echo "expected $path to NOT contain: $needle" >&2
    grep -Fn "$needle" "$path" >&2 || true
    exit 1
  fi
}
