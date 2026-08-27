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
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" new-session -d -s install-smoke -c "$smoke_root" sleep 300
# Model the exact pre-0.13 live-server partial state: the server is app-owned,
# its legacy session is live, and the logical route marker does not exist yet.
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" set-option -gq @projmux_app 1
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" set-option -gu @projmux_socket_name 2>/dev/null || true
legacy_socket_path="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p '#{socket_path}')"
legacy_server_pid="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p '#{pid}')"
legacy_sessions="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-sessions -F '#{session_id}|#{session_name}')"
case "$legacy_socket_path" in
  "$PROJMUX_SMOKE_TMUX_ROOT"/*) ;;
  *)
    echo "pre-0.13 smoke socket escaped the run-local root: $legacy_socket_path" >&2
    exit 1
    ;;
esac

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
smoke_assert_file_contains "$HOME/.config/projmux/tmux.conf" 'internal status notify --max-width #{?#{e|<:#{client_width},40},#{e|/:#{client_width},2},#{?#{e|<:#{client_width},160},20,#{?#{e|<:#{client_width},220},#{e|-:#{client_width},140},80}}}'
smoke_assert_file_contains "$HOME/.config/projmux/tmux.conf" 'internal status usage --max-width #{e|-:#{client_width},#{?#{e|<:#{client_width},40},#{e|/:#{client_width},2},#{?#{e|<:#{client_width},160},20,#{?#{e|<:#{client_width},220},#{e|-:#{client_width},140},80}}}}'
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
logical_marker="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv @projmux_socket_name)"
recovered_socket_path="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p '#{socket_path}')"
recovered_server_pid="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p '#{pid}')"
recovered_sessions="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-sessions -F '#{session_id}|#{session_name}')"
if [[ "$logical_marker" != "$PROJMUX_SMOKE_TMUX_SOCKET" ]] ||
  [[ "$recovered_socket_path" != "$legacy_socket_path" ]] ||
  [[ "$recovered_server_pid" != "$legacy_server_pid" ]] ||
  [[ "$recovered_sessions" != "$legacy_sessions" ]]; then
  echo "installed pre-0.13 marker recovery changed the exact live server generation or sessions" >&2
  exit 1
fi

# Exercise the installed ordinary-mutation boundary, not just the explicit
# writer. A missing marker must fail non-zero with the exact recovery command
# and leave both Registry/runtime unchanged; after explicit apply, the same
# public create succeeds on the preserved server generation.
migration_project_uid="$(env -u TMUX -u TMUX_PANE "$installed" create project --root "$smoke_root" --name marker-migration -o uid)"
registry_path="$XDG_STATE_HOME/projmux/metadata/registry.json"
cp "$registry_path" "$PROJMUX_SMOKE_WORKDIR/marker-migration.registry.before"
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" set-option -gu @projmux_socket_name
ordinary_pid_before="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p '#{pid}')"
ordinary_runtime_before="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-sessions -F '#{session_id}|#{session_name}|#{session_windows}')"
set +e
env -u TMUX -u TMUX_PANE "$installed" create window \
  --project "uid:$migration_project_uid" --name recovery-check -o pane-id \
  >"$PROJMUX_SMOKE_WORKDIR/marker-migration-create-before.out" \
  2>"$PROJMUX_SMOKE_WORKDIR/marker-migration-create-before.err"
ordinary_status=$?
set -e
if [[ "$ordinary_status" == "0" ]]; then
  echo "installed ordinary mutation succeeded with a missing logical marker" >&2
  exit 1
fi
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/marker-migration-create-before.err" \
  "projmux config apply --socket $PROJMUX_SMOKE_TMUX_SOCKET"
cmp "$PROJMUX_SMOKE_WORKDIR/marker-migration.registry.before" "$registry_path"
if [[ "$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p '#{pid}')" != "$ordinary_pid_before" ]] ||
  [[ "$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-sessions -F '#{session_id}|#{session_name}|#{session_windows}')" != "$ordinary_runtime_before" ]] ||
  [[ -n "$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv @projmux_socket_name)" ]]; then
  echo "installed missing-marker refusal changed runtime state" >&2
  exit 1
fi

recovery_apply_out="$(env -u TMUX -u TMUX_PANE "$installed" config apply --socket "$PROJMUX_SMOKE_TMUX_SOCKET")"
smoke_assert_output_contains "$recovery_apply_out" "reloaded tmux server -L $PROJMUX_SMOKE_TMUX_SOCKET: 1 sessions"
if [[ "$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p '#{pid}')" != "$ordinary_pid_before" ]] ||
  [[ "$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-sessions -F '#{session_id}|#{session_name}|#{session_windows}')" != "$ordinary_runtime_before" ]]; then
  echo "installed explicit recovery replaced the live server or legacy sessions" >&2
  exit 1
fi
recovered_pane="$(env -u TMUX -u TMUX_PANE "$installed" create window \
  --project "uid:$migration_project_uid" --name recovery-check -o pane-id)"
if [[ "$recovered_pane" != %* ]]; then
  echo "installed ordinary mutation did not return an exact pane id after recovery: $recovered_pane" >&2
  exit 1
fi
