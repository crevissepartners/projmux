#!/usr/bin/env bash
set -euo pipefail

source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/test/lib/smoke.sh"

smoke_setup_env
trap smoke_cleanup_env EXIT
cd "$smoke_root"

install_dir="$PROJMUX_SMOKE_WORKDIR/bin"
build_dir="$PROJMUX_SMOKE_WORKDIR/install-build"
mkdir -p "$install_dir"
export PATH="$install_dir:$PATH"

PROJMUX_SMOKE_TMUX_SOCKET="projmux"
export PROJMUX_SMOKE_TMUX_SOCKET
tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" new-session -d -s install-smoke -c "$smoke_root" sleep 300

make install \
  BUILD_DIR="$build_dir" \
  PROJMUX_BIN="$build_dir/projmux" \
  INSTALL_DIR="$install_dir" \
  >"$PROJMUX_SMOKE_WORKDIR/make-install.out"

installed="$install_dir/projmux"
if [[ ! -x "$installed" ]]; then
  echo "expected installed binary at $installed" >&2
  exit 1
fi

"$installed" version >"$PROJMUX_SMOKE_WORKDIR/version.out"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/version.out" "projmux"

smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/make-install.out" "atomically replaced $installed"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/make-install.out" "reloaded tmux server -L projmux: 1 sessions"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/make-install.out" "reconcile:"
smoke_assert_file_contains "$HOME/.config/projmux/tmux.conf" "status notify --max-width 80"

app_flag="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv @projmux_app)"
if [[ "$app_flag" != "1" ]]; then
  echo "expected installed tmux apply to set @projmux_app=1, got: $app_flag" >&2
  exit 1
fi
