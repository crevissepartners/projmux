#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
coordinator="$(mktemp -d /tmp/projmux-shard-isolation.XXXXXX)"
worker_pids=()

wait_for_file() {
  local path="$1"
  local attempts="${2:-100}"
  local attempt
  for ((attempt = 0; attempt < attempts; attempt++)); do
    [[ -s "$path" ]] && return 0
    sleep 0.05
  done
  echo "bounded shard isolation wait missed $path" >&2
  for worker_log in "$coordinator"/worker-*.log; do
    [[ -f "$worker_log" ]] || continue
    sed "s#^#${worker_log##*/}: #" "$worker_log" >&2
  done
  return 1
}

release_workers() {
  local fixture
  for fixture in 1 2 3 4; do
    printf 'release\n' >"$coordinator/release-$fixture"
  done
  for worker_pid in "${worker_pids[@]}"; do
    wait "$worker_pid" 2>/dev/null || true
  done
}

cleanup() {
  release_workers
  rm -rf "$coordinator"
}
trap cleanup EXIT

run_worker() (
  set -euo pipefail
  local fixture="$1"
  # shellcheck disable=SC1091
  # Repository root is resolved at runtime.
  source "$root/test/lib/smoke.sh"
  smoke_setup_env
  PROJMUX_SMOKE_TMUX_SOCKET="isolation-${fixture}-$$-${RANDOM}"
  export PROJMUX_SMOKE_TMUX_SOCKET
  env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$PROJMUX_SMOKE_TMUX_ROOT" \
    tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" new-session -d -s isolation "sleep 30"
  local socket_path
  socket_path="$(env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$PROJMUX_SMOKE_TMUX_ROOT" \
    tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p '#{socket_path}')"
  case "$socket_path" in
    "$PROJMUX_SMOKE_WORKDIR"/*) ;;
    *) echo "fixture $fixture socket escaped owned root: $socket_path" >&2; exit 1 ;;
  esac
  env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$PROJMUX_SMOKE_TMUX_ROOT" \
    tmux -S "$socket_path" set-option -gq @projmux_isolation_sentinel "fixture-$fixture"
  printf '%s\t%s\t%s\t%s\n' \
    "$PROJMUX_SMOKE_WORKDIR" "$PROJMUX_SMOKE_TMUX_SOCKET" "$socket_path" "$PROJMUX_SMOKE_OWNER" \
    >"$coordinator/ready-$fixture"
  echo "fixture-$fixture ready root=$PROJMUX_SMOKE_WORKDIR socket=$socket_path"
  # A worker may become ready well before the fourth server and intentionally
  # remains live while earlier owners are cleaned. Keep this coordination
  # barrier bounded independently of product/E2E assertion timeouts.
  wait_for_file "$coordinator/release-$fixture"
  echo "fixture-$fixture released"
  local owned_root="$PROJMUX_SMOKE_WORKDIR"
  smoke_cleanup_env
  echo "fixture-$fixture cleanup-complete"
  [[ ! -e "$owned_root" ]] || { echo "fixture $fixture owned root survived cleanup" >&2; exit 1; }
  printf 'done\n' >"$coordinator/done-$fixture"
)

for fixture in 1 2 3 4; do
  run_worker "$fixture" >"$coordinator/worker-$fixture.log" 2>&1 &
  worker_pids+=("$!")
done

declare -A roots=() sockets=() paths=()
for fixture in 1 2 3 4; do
  wait_for_file "$coordinator/ready-$fixture"
  IFS=$'\t' read -r owned_root socket_name socket_path owner \
    <"$coordinator/ready-$fixture"
  [[ -n "$owner" && -f "$owned_root/.projmux-smoke-owner" ]] || exit 1
  [[ "$(cat "$owned_root/.projmux-smoke-owner")" == "$owner" ]] || exit 1
  roots["$fixture"]="$owned_root"
  sockets["$fixture"]="$socket_name"
  paths["$fixture"]="$socket_path"
done

for field in roots sockets paths; do
  mapfile -t unique_values < <(
    for fixture in 1 2 3 4; do
      case "$field" in
        roots) printf '%s\n' "${roots[$fixture]}" ;;
        sockets) printf '%s\n' "${sockets[$fixture]}" ;;
        paths) printf '%s\n' "${paths[$fixture]}" ;;
      esac
    done | sort -u
  )
  [[ "${#unique_values[@]}" == "4" ]] || {
    echo "concurrent fixture $field collided" >&2
    exit 1
  }
done

released=()
for fixture in 3 1 4 2; do
  printf 'release\n' >"$coordinator/release-$fixture"
  wait_for_file "$coordinator/done-$fixture"
  wait "${worker_pids[$((fixture - 1))]}"
  released+=("$fixture")
  [[ ! -e "${roots[$fixture]}" && ! -e "${paths[$fixture]}" ]] || {
    echo "fixture $fixture left an owned root or socket orphan" >&2
    exit 1
  }
  for sibling in 1 2 3 4; do
    if printf '%s\n' "${released[@]}" | grep -qx "$sibling"; then
      continue
    fi
    [[ -d "${roots[$sibling]}" && -S "${paths[$sibling]}" ]] || {
      echo "fixture $fixture cleanup removed sibling $sibling" >&2
      exit 1
    }
    [[ "$(env -u TMUX -u TMUX_PANE tmux -S "${paths[$sibling]}" show-options -gqv @projmux_isolation_sentinel)" == \
      "fixture-$sibling" ]] || {
      echo "fixture $sibling sentinel was not preserved" >&2
      exit 1
    }
  done
done

for fixture in 1 2 3 4; do
  [[ ! -e "${roots[$fixture]}" && ! -e "${paths[$fixture]}" ]] || exit 1
done
trap - EXIT
rm -rf "$coordinator"
echo ">> four-owner collision/cleanup stress passed: unique roots/sockets=4 sibling sentinels=preserved orphans=0"
