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
installed="$install_dir/projmux"

PROJMUX_SMOKE_TMUX_SOCKET="projmux"
export PROJMUX_SMOKE_TMUX_SOCKET
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" new-session -d -s home -c "$smoke_root" sleep 300
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

# Model the published v0.12.2 consumer independently from its live-server
# fixture. It can reach both supported entrypoints without requiring the marker
# introduced in v0.13, while the exact server/path/PID/session observations
# above remain real tmux state.
cat >"$installed" <<'LEGACY_CONSUMER'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  shell)
    env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" has-session -t home
    ;;
  runtime)
    [[ "${2:-}" == "attach" ]]
    env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" has-session -t home
    ;;
  version)
    echo "projmux v0.12.2 fixture"
    ;;
  *)
    exit 2
    ;;
esac
LEGACY_CONSUMER
chmod 0755 "$installed"
"$installed" shell --session home
"$installed" runtime attach

# A candidate source-file failure is a failed install, not a successful binary
# publication. The old executable, missing marker, server generation, and
# sessions remain exact, and the operator gets the Phase 0 recovery command.
failure_install_dir="$PROJMUX_SMOKE_WORKDIR/failure-bin"
failure_build_dir="$PROJMUX_SMOKE_WORKDIR/failure-build"
failure_shim_dir="$PROJMUX_SMOKE_WORKDIR/failure-shim"
mkdir -p "$failure_install_dir" "$failure_shim_dir"
cp "$installed" "$failure_install_dir/projmux"
failure_target_hash="$(sha256sum "$failure_install_dir/projmux" | awk '{print $1}')"
real_tmux="$(command -v tmux)"
export PROJMUX_SMOKE_REAL_TMUX="$real_tmux"
cat >"$failure_shim_dir/tmux" <<'FAIL_SOURCE_TMUX'
#!/usr/bin/env bash
set -euo pipefail
for arg in "$@"; do
  if [[ "$arg" == "source-file" ]]; then
    echo "injected pre-publication source-file failure" >&2
    exit 1
  fi
done
exec "$PROJMUX_SMOKE_REAL_TMUX" "$@"
FAIL_SOURCE_TMUX
chmod 0755 "$failure_shim_dir/tmux"
set +e
PATH="$failure_shim_dir:$PATH" make install \
  BUILD_DIR="$failure_build_dir" \
  PROJMUX_BIN="$failure_build_dir/projmux" \
  INSTALL_DIR="$failure_install_dir" \
  >"$PROJMUX_SMOKE_WORKDIR/make-install-source-failure.out" \
  2>"$PROJMUX_SMOKE_WORKDIR/make-install-source-failure.err"
failure_install_status=$?
set -e
if [[ "$failure_install_status" == "0" ]]; then
  echo "make install reported source-file failure as success" >&2
  exit 1
fi
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/make-install-source-failure.err" \
  "binary publication not started; recovery: run \`projmux config apply --socket $PROJMUX_SMOKE_TMUX_SOCKET\`"
if [[ "$(sha256sum "$failure_install_dir/projmux" | awk '{print $1}')" != "$failure_target_hash" ]] ||
  [[ -n "$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv @projmux_socket_name)" ]] ||
  [[ "$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p '#{socket_path}')" != "$legacy_socket_path" ]] ||
  [[ "$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p '#{pid}')" != "$legacy_server_pid" ]] ||
  [[ "$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-sessions -F '#{session_id}|#{session_name}')" != "$legacy_sessions" ]]; then
  echo "failed source apply published a binary/marker or changed the exact legacy generation" >&2
  exit 1
fi

# Pause the atomic publication after candidate config apply succeeds. The
# consumer probes below therefore observe all three states deterministically:
# legacy/before, candidate+during, and installed/after. The install target is
# not replaced until the test releases this exact seam.
install_seam_ready="$PROJMUX_SMOKE_WORKDIR/install-seam.ready"
install_seam_release="$PROJMUX_SMOKE_WORKDIR/install-seam.release"
install_mv="$PROJMUX_SMOKE_WORKDIR/install-mv"
export PROJMUX_INSTALL_SEAM_READY="$install_seam_ready"
export PROJMUX_INSTALL_SEAM_RELEASE="$install_seam_release"
cat >"$install_mv" <<'INSTALL_MV'
#!/usr/bin/env bash
set -euo pipefail
: >"$PROJMUX_INSTALL_SEAM_READY"
for _ in {1..500}; do
  [[ -f "$PROJMUX_INSTALL_SEAM_RELEASE" ]] && exec /bin/mv "$@"
  sleep 0.01
done
echo "timed out waiting for install publication seam release" >&2
exit 1
INSTALL_MV
chmod 0755 "$install_mv"

make install \
  BUILD_DIR="$build_dir" \
  PROJMUX_BIN="$build_dir/projmux" \
  INSTALL_DIR="$install_dir" \
	INSTALL_MV="$install_mv" \
	>"$PROJMUX_SMOKE_WORKDIR/make-install.out" \
	2>"$PROJMUX_SMOKE_WORKDIR/make-install.err" &
install_make_pid=$!
for _ in {1..500}; do
  [[ -f "$install_seam_ready" ]] && break
  if ! kill -0 "$install_make_pid" 2>/dev/null; then
    wait "$install_make_pid" || true
    echo "make install exited before the publication seam" >&2
    cat "$PROJMUX_SMOKE_WORKDIR/make-install.err" >&2
    exit 1
  fi
  sleep 0.01
done
if [[ ! -f "$install_seam_ready" ]]; then
  echo "make install did not reach the publication seam" >&2
  exit 1
fi

prepublished_marker="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv @projmux_socket_name)"
if [[ "$prepublished_marker" != "$PROJMUX_SMOKE_TMUX_SOCKET" ]]; then
  echo "candidate publication became reachable before exact marker convergence: $prepublished_marker" >&2
  exit 1
fi

probe_consumer_without_raw_marker_error() {
  local label="$1"
  shift
  set +e
  timeout 5 "$@" \
    >"$PROJMUX_SMOKE_WORKDIR/$label.out" \
    2>"$PROJMUX_SMOKE_WORKDIR/$label.err"
  local status=$?
  set -e
  if grep -Fq "has no logical socket marker" "$PROJMUX_SMOKE_WORKDIR/$label.err" ||
    grep -Fq "missing-logical-marker" "$PROJMUX_SMOKE_WORKDIR/$label.err"; then
    echo "$label exposed the raw missing-marker state (status=$status)" >&2
    cat "$PROJMUX_SMOKE_WORKDIR/$label.err" >&2
    exit 1
  fi
}

# The built candidate is the new consumer that will be published. Running it
# while publication is paused proves both shell and attach see the migrated
# exact generation, never the old raw missing-marker error.
probe_consumer_without_raw_marker_error during-shell \
  env -u TMUX -u TMUX_PANE "$build_dir/projmux" shell --session home --no-install
probe_consumer_without_raw_marker_error during-attach \
  env -u TMUX -u TMUX_PANE "$build_dir/projmux" runtime attach --fallback=home

: >"$install_seam_release"
set +e
wait "$install_make_pid"
install_make_status=$?
set -e
if [[ "$install_make_status" != "0" ]]; then
  echo "make install failed after publication seam (status=$install_make_status)" >&2
  cat "$PROJMUX_SMOKE_WORKDIR/make-install.err" >&2
  exit 1
fi

if [[ ! -x "$installed" ]]; then
  echo "expected installed binary at $installed" >&2
  exit 1
fi

probe_consumer_without_raw_marker_error after-shell \
  env -u TMUX -u TMUX_PANE "$installed" shell --session home --no-install
probe_consumer_without_raw_marker_error after-attach \
  env -u TMUX -u TMUX_PANE "$installed" runtime attach --fallback=home

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
