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

installed_report="$PROJMUX_SMOKE_WORKDIR/installed-support/report.tar.gz"
if [[ -e "$(dirname "$installed_report")" || -e "$installed_report" ]]; then
  echo "installed support report existed before explicit request" >&2
  exit 1
fi
"$installed" diagnostics report --output "$installed_report" >"$PROJMUX_SMOKE_WORKDIR/installed-report-preview.txt"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/installed-report-preview.txt" "projmux diagnostics report preview"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/installed-report-preview.txt" "archive write follows this complete preview"
if [[ ! -f "$installed_report" ]] || [[ "$(stat -c '%a' "$installed_report")" != "600" ]]; then
  echo "expected installed binary to create a private support archive" >&2
  exit 1
fi
tar -xOzf "$installed_report" manifest.json >"$PROJMUX_SMOKE_WORKDIR/installed-report-manifest.json"
tar -xOzf "$installed_report" doctor.json >"$PROJMUX_SMOKE_WORKDIR/installed-report-doctor.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/installed-report-manifest.json" '"report_schema_version": 2'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/installed-report-manifest.json" '"redaction_mode": "default-hash-v1"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/installed-report-doctor.json" '"schema_version": 2'

smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/make-install.out" "atomically replaced $installed"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/make-install.out" "reloaded tmux server -L projmux: 1 sessions"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/make-install.out" "reconcile:"
smoke_assert_file_contains "$HOME/.config/projmux/tmux.conf" 'internal status notify --max-width #{?#{e|<:#{client_width},160},#{e|/:#{client_width},2},80}'
smoke_assert_file_contains "$HOME/.config/projmux/tmux.conf" 'internal status usage --max-width #{e|-:#{client_width},#{?#{e|<:#{client_width},160},#{e|/:#{client_width},2},80}}'
# The installed config is what a live tmux server sources, so it is the one
# place the internal-namespace relocation has to hold end to end.
smoke_assert_file_contains "$HOME/.config/projmux/tmux.conf" "internal statusbar click"
smoke_assert_file_lacks "$HOME/.config/projmux/tmux.conf" "'$installed' status"
smoke_assert_file_lacks "$HOME/.config/projmux/tmux.conf" "'$installed' statusbar"
smoke_assert_file_lacks "$HOME/.config/projmux/tmux.conf" "'$installed' tmux"

app_flag="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv @projmux_app)"
if [[ "$app_flag" != "1" ]]; then
  echo "expected installed tmux apply to set @projmux_app=1, got: $app_flag" >&2
  exit 1
fi
