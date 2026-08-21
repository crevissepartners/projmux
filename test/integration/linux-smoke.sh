#!/usr/bin/env bash
set -euo pipefail

# Never inherit a caller's live tmux client context into this process. Every
# tmux mutation below is routed to a run-unique -L socket.
unset TMUX TMUX_PANE

source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/test/lib/smoke.sh"

smoke_setup_env
PROJMUX_SMOKE_TMUX_ACTUAL=""
PROJMUX_SMOKE_TMUX_STARTED=0
PROJMUX_RECONCILE_PRIMARY_STARTED=0
PROJMUX_RECONCILE_SECONDARY_STARTED=0
PROJMUX_RUNTIME_APP_STARTED=0
PROJMUX_RUNTIME_GUEST_STARTED=0
PROJMUX_RUNTIME_SIBLING_STARTED=0
PROJMUX_DISCOVERY_STARTED=0
integration_await_server_gone() {
  local actual="$1" label="$2"
  for _ in $(seq 1 200); do
    if ! env -u TMUX -u TMUX_PANE tmux -S "$actual" list-sessions >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.05
  done
  echo "integration cleanup left $label server live at $actual" >&2
  return 1
}

integration_background_routes() {
  ps -eo pid=,args= | PROJMUX_ROUTE_BIN="${PROJMUX_SMOKE_BIN:-}" awk '
    ENVIRON["PROJMUX_ROUTE_BIN"] != "" && index($0, ENVIRON["PROJMUX_ROUTE_BIN"]) > 0 { print }'
}

integration_await_background_routes() {
  if [[ -z "${PROJMUX_SMOKE_BIN:-}" ]]; then
    return 0
  fi
  local routes quiet_samples=0
  for _ in $(seq 1 200); do
    routes="$(integration_background_routes)"
    if [[ -z "$routes" ]]; then
      quiet_samples=$((quiet_samples + 1))
      if [[ "$quiet_samples" == "10" ]]; then
        return 0
      fi
    else
      quiet_samples=0
    fi
    sleep 0.05
  done
  echo "integration cleanup still has isolated background routes:" >&2
  integration_background_routes >&2
  return 1
}

cleanup_reconcile_socket() {
  local started="$1"
  local socket="$2"
  local actual="$3"
  local session="$4"
  if [[ "$started" != "1" ]]; then
    return 0
  fi
  case "$actual" in
    "$PROJMUX_SMOKE_WORKDIR"/tmux/*) ;;
    *)
      echo "refusing reconcile cleanup outside smoke root: $actual" >&2
      return 1
      ;;
  esac
  local current
  current="$(env -u TMUX -u TMUX_PANE tmux -L "$socket" display-message -p -t "=$session" '#{socket_path}' 2>/dev/null || true)"
  if [[ -z "$current" || "$current" != "$actual" ]]; then
    echo "refusing reconcile cleanup after socket identity changed: expected=$actual current=$current" >&2
    return 1
  fi
  env -u TMUX -u TMUX_PANE tmux -S "$actual" kill-server >/dev/null 2>&1 || true
  integration_await_server_gone "$actual" "$socket"
}
integration_cleanup() {
	cleanup_reconcile_socket "${PROJMUX_RECONCILE_PRIMARY_STARTED:-0}" "${PROJMUX_RECONCILE_PRIMARY_SOCKET:-}" "${PROJMUX_RECONCILE_PRIMARY_ACTUAL:-}" "${PROJMUX_RECONCILE_SESSION:-}"
	cleanup_reconcile_socket "${PROJMUX_RECONCILE_SECONDARY_STARTED:-0}" "${PROJMUX_RECONCILE_SECONDARY_SOCKET:-}" "${PROJMUX_RECONCILE_SECONDARY_ACTUAL:-}" "${PROJMUX_RECONCILE_SESSION:-}"
	cleanup_reconcile_socket "${PROJMUX_RUNTIME_APP_STARTED:-0}" "${PROJMUX_RUNTIME_APP_SOCKET:-}" "${PROJMUX_RUNTIME_APP_ACTUAL:-}" "${PROJMUX_RUNTIME_SESSION:-}"
	cleanup_reconcile_socket "${PROJMUX_RUNTIME_GUEST_STARTED:-0}" "${PROJMUX_RUNTIME_GUEST_SOCKET:-}" "${PROJMUX_RUNTIME_GUEST_ACTUAL:-}" "${PROJMUX_RUNTIME_SESSION:-}"
	cleanup_reconcile_socket "${PROJMUX_RUNTIME_SIBLING_STARTED:-0}" "${PROJMUX_RUNTIME_SIBLING_SOCKET:-}" "${PROJMUX_RUNTIME_SIBLING_ACTUAL:-}" "${PROJMUX_RUNTIME_SESSION:-}"
	cleanup_reconcile_socket "${PROJMUX_DISCOVERY_STARTED:-0}" "${PROJMUX_DISCOVERY_SOCKET:-}" "${PROJMUX_DISCOVERY_ACTUAL:-}" "${PROJMUX_DISCOVERY_SESSION:-}"
	if [[ "${PROJMUX_SMOKE_TMUX_STARTED:-0}" == "1" ]]; then
		if [[ -z "${PROJMUX_SMOKE_TMUX_ACTUAL:-}" || -z "${PROJMUX_SMOKE_TMUX_SOCKET:-}" ]]; then
			echo "refusing integration cleanup without expected socket identity" >&2
			return 1
		fi
    case "$PROJMUX_SMOKE_TMUX_ACTUAL" in
      "$PROJMUX_SMOKE_WORKDIR"/tmux/*) ;;
      *)
        echo "refusing integration cleanup outside smoke root: $PROJMUX_SMOKE_TMUX_ACTUAL" >&2
        return 1
        ;;
    esac
    current_socket="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p -t integration-smoke '#{socket_path}' 2>/dev/null || true)"
		if [[ -z "$current_socket" ]]; then
			echo "refusing integration cleanup: expected server socket is unqueryable" >&2
			return 1
		fi
		if [[ "$current_socket" != "$PROJMUX_SMOKE_TMUX_ACTUAL" ]]; then
      echo "refusing integration cleanup after socket identity changed: $current_socket" >&2
      return 1
    fi
		env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" kill-server >/dev/null 2>&1 || true
		integration_await_server_gone "$PROJMUX_SMOKE_TMUX_ACTUAL" "$PROJMUX_SMOKE_TMUX_SOCKET"
		PROJMUX_SMOKE_TMUX_STARTED=0
  fi
  if [[ -n "${PROJMUX_SMOKE_WORKDIR:-}" ]]; then
    # Generated tmux hooks are background routes. Killing every exact isolated
    # server above closes their producer side; require a full quiet window with
    # no fixture-binary process before deleting the root those routes write.
    integration_await_background_routes
    rm -rf -- "$PROJMUX_SMOKE_WORKDIR"
  fi
}
trap integration_cleanup EXIT
cd "$smoke_root"

smoke_build_binary
bin="$PROJMUX_SMOKE_BIN"

# `create` is resource-backed on every spelling, so an invocation that resolves
# no managed Project creates nothing at all. Pin that at the built-binary
# boundary: a `$TMUX` with no `$TMUX_PANE` is "inside tmux, no active target",
# which is exactly what a `run-shell` child would see if the generated binding
# ever stopped carrying `#{pane_id}`. The command must exit 2 naming --project
# and must issue no tmux command at all -- not even a probe of the server it
# inherited. `$TMUX_SPLIT_TARGET_PANE` is set here on purpose: a popup's origin
# pane is an anchor the split UI states on its create intent and never an
# ambient scope, so a *typed* create inside that same runtime keeps refusing and
# keeps probing nothing. The positive pane-id propagation runs against a real tmux server in
# the e2e smoke, which is the only place a resource-backed create can be
# observed end to end.
fake_mux_dir="$PROJMUX_SMOKE_WORKDIR/fake-mux"
mkdir -p "$fake_mux_dir"
cat >"$fake_mux_dir/tmux" <<'FAKE_TMUX'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$PROJMUX_FAKE_MUX_LOG"
if [[ "${1:-}" == "split-window" ]]; then
  printf '%%81\n'
fi
FAKE_TMUX
chmod 0755 "$fake_mux_dir/tmux"

fake_tmux_log="$PROJMUX_SMOKE_WORKDIR/fake-tmux.log"
: >"$fake_tmux_log"
# A failing command journals one outcome event, which is correct and unrelated
# to what this check measures. Give it its own state root so the shared
# diagnostics journal keeps the empty-source invariant the report assertions
# below depend on.
set +e
fake_tmux_output="$(
  PROJMUX_FAKE_MUX_LOG="$fake_tmux_log" \
    PATH="$fake_mux_dir:$PATH" \
    TMUX="fake" \
    TMUX_SPLIT_TARGET_PANE="%7" \
    XDG_STATE_HOME="$PROJMUX_SMOKE_WORKDIR/create-no-scope-state" \
    SHELL="/bin/sh" \
    "$bin" create pane -o pane-id --placement right 2>"$PROJMUX_SMOKE_WORKDIR/create-no-scope.err"
)"
fake_tmux_status=$?
set -e
if [[ "$fake_tmux_status" != "2" ]]; then
  echo "a create with no resolvable scope exited $fake_tmux_status, want 2" >&2
  cat "$PROJMUX_SMOKE_WORKDIR/create-no-scope.err" >&2 || true
  exit 1
fi
if [[ -n "$fake_tmux_output" ]]; then
  echo "a refused create wrote to stdout: $fake_tmux_output" >&2
  exit 1
fi
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/create-no-scope.err" "pass --project <ref>"
if [[ -s "$fake_tmux_log" ]]; then
  echo "a refused create issued tmux commands:" >&2
  cat "$fake_tmux_log" >&2
  exit 1
fi

# Generated keybindings enter the saved-default split through the hidden
# post-`ai` retirement bridge, and that bridge now produces a canonical create
# intent rather than a raw split. Exercise the built-binary boundary so a
# route/catalog refactor cannot leave the popup functional while launch is a
# no-op -- and so the bridge cannot quietly regain a runtime-only split.
#
# `$TMUX` with `$TMUX_SPLIT_TARGET_PANE` and no `$TMUX_PANE` is the popup
# runtime: `display-popup -E` exports the first two and never the third, so the
# origin pane the keypress carried is the only target this invocation has. The
# bridge must therefore reach create with that pane as an explicit anchor, which
# is observable here as the read-only identity probe of `%7`.
#
# The fixture's fake pane mirrors no Projmux identity, which is what keeps the
# assertion sharp on both sides: the anchor is used, and the canonical intent
# requires its exact Pane mirror before it will inspect the Window/root chain.
# The missing Pane uid therefore refuses with the exact origin-loss reason,
# exit 2, and no mutation. A bridge that called `split-window` would exit 0 and
# log one; a bridge that lost the anchor would log no probe at all. Shell mode
# keeps the fixture independent of provider binaries.
mkdir -p "$XDG_CONFIG_HOME/projmux"
printf 'shell\n' >"$XDG_CONFIG_HOME/projmux/tmux-ai-split-mode"
: >"$fake_tmux_log"
set +e
launch_default_output="$(
  PROJMUX_FAKE_MUX_LOG="$fake_tmux_log" \
    PATH="$fake_mux_dir:$PATH" \
    TMUX="fake" \
    TMUX_SPLIT_TARGET_PANE="%7" \
    TMUX_SPLIT_CONTEXT_DIR="$smoke_root" \
    XDG_STATE_HOME="$PROJMUX_SMOKE_WORKDIR/launch-default-state" \
    SHELL="/bin/sh" \
    "$bin" internal agent-pane launch-default down 2>"$PROJMUX_SMOKE_WORKDIR/launch-default.err"
)"
launch_default_status=$?
set -e
if [[ "$launch_default_status" != "2" ]]; then
  echo "the saved-default split bridge exited $launch_default_status, want the canonical create refusal 2" >&2
  cat "$PROJMUX_SMOKE_WORKDIR/launch-default.err" >&2 || true
  exit 1
fi
if [[ -n "$launch_default_output" ]]; then
  echo "the saved-default split bridge wrote to stdout: $launch_default_output" >&2
  exit 1
fi
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/launch-default.err" "carries no @projmux_pane_uid; the exact UI origin was lost"
# The anchor reached create: canonical scope resolution reads the exact Pane uid
# off `%7` and stops there when that first owner-chain link is absent.
smoke_assert_file_contains "$fake_tmux_log" "display-message -p -t %7 -F #{@projmux_pane_uid}"
if grep -Fq "@projmux_window_uid" "$fake_tmux_log"; then
  echo "the saved-default split bridge probed a Window after the exact Pane origin was lost:" >&2
  cat "$fake_tmux_log" >&2
  exit 1
fi
if grep -qE '(^| )(split-window|new-window|new-session|kill-pane|kill-window|set-option|send-keys)( |$)' "$fake_tmux_log"; then
  echo "the saved-default split bridge mutated tmux instead of refusing:" >&2
  cat "$fake_tmux_log" >&2
  exit 1
fi
if grep -qvE '^display-message ' "$fake_tmux_log"; then
  echo "the saved-default split bridge issued something other than the read-only anchor probes:" >&2
  cat "$fake_tmux_log" >&2
  exit 1
fi
printf 'selective\n' >"$XDG_CONFIG_HOME/projmux/tmux-ai-split-mode"

"$bin" doctor --json >"$PROJMUX_SMOKE_WORKDIR/doctor.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor.json" '"schema_version": 2'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor.json" '"name": "tmux"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor.json" '"status": "ok"'

"$bin" doctor --json --section deps --verbose >"$PROJMUX_SMOKE_WORKDIR/doctor-deps.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor-deps.json" '"dependencies"'
if grep -Eq '"(ai_notify_integrations|session_state_resume|runtime|logs)"' "$PROJMUX_SMOKE_WORKDIR/doctor-deps.json"; then
  echo "doctor deps projection leaked another section" >&2
  exit 1
fi

"$bin" doctor --section runtime >"$PROJMUX_SMOKE_WORKDIR/doctor-runtime.txt"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor-runtime.txt" "runtime.socket.unreachable"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor-runtime.txt" "runtime.config.generated-missing"

removed_flag_stderr="$PROJMUX_SMOKE_WORKDIR/doctor-removed-flag.err"
operations_log="$XDG_STATE_HOME/projmux/logs/operations.jsonl"
operations_before="$PROJMUX_SMOKE_WORKDIR/operations-before.jsonl"
if [[ -f "$operations_log" ]]; then
  cp "$operations_log" "$operations_before"
fi
legacy_hook="$XDG_CONFIG_HOME/projmux/hooks/post-create"
mkdir -p "$(dirname "$legacy_hook")"
printf 'printf legacy-hook-must-stay\n' >"$legacy_hook"
if "$bin" doctor --install-missing >"$PROJMUX_SMOKE_WORKDIR/doctor-removed-flag.out" 2>"$removed_flag_stderr"; then
  echo "removed doctor --install-missing unexpectedly succeeded" >&2
  exit 1
fi
smoke_assert_file_contains "$removed_flag_stderr" "flag provided but not defined: -install-missing"
smoke_assert_file_contains "$removed_flag_stderr" "projmux doctor is read-only; remove --install-missing and run displayed remediation guidance explicitly outside doctor"
smoke_assert_file_contains "$legacy_hook" "legacy-hook-must-stay"
if [[ -e "$legacy_hook.bak" || -e "$XDG_CONFIG_HOME/projmux/config.toml" ]]; then
  echo "removed doctor flag invocation mutated legacy hook state" >&2
  exit 1
fi
if [[ -f "$operations_before" ]]; then
  cmp "$operations_before" "$operations_log"
elif [[ -e "$operations_log" ]]; then
  echo "removed doctor flag invocation wrote an operational outcome" >&2
  exit 1
fi

# An explicit report invocation prints the complete preview before writing one
# private local archive. It reuses Doctor schema v2 and does not migrate the
# seeded legacy hook or journal its own success.
support_report="$PROJMUX_SMOKE_WORKDIR/support/report.tar.gz"
if [[ -e "$(dirname "$support_report")" || -e "$support_report" ]]; then
  echo "support report existed before explicit request" >&2
  exit 1
fi
operations_count_before_report=0
if [[ -f "$operations_log" ]]; then
  operations_count_before_report="$(wc -l <"$operations_log")"
fi
"$bin" diagnostics report --output "$support_report" >"$PROJMUX_SMOKE_WORKDIR/report-preview.txt"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/report-preview.txt" "projmux diagnostics report preview"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/report-preview.txt" "manifest.json: included"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/report-preview.txt" "doctor.json: included"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/report-preview.txt" "archive write follows this complete preview"
if [[ ! -f "$support_report" ]] || [[ "$(stat -c '%a' "$support_report")" != "600" ]]; then
  echo "expected explicit private support archive" >&2
  exit 1
fi
tar -xOzf "$support_report" manifest.json >"$PROJMUX_SMOKE_WORKDIR/report-manifest.json"
tar -xOzf "$support_report" doctor.json >"$PROJMUX_SMOKE_WORKDIR/report-doctor.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/report-manifest.json" '"report_schema_version": 2'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/report-manifest.json" '"redaction_mode": "default-hash-v1"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/report-doctor.json" '"schema_version": 2'
smoke_assert_file_contains "$legacy_hook" "legacy-hook-must-stay"
operations_count_after_report=0
if [[ -f "$operations_log" ]]; then
  operations_count_after_report="$(wc -l <"$operations_log")"
fi
if [[ "$operations_count_before_report" != "$operations_count_after_report" ]]; then
  echo "support report unexpectedly appended an operational event" >&2
  exit 1
fi

assert_report_manifest_entry() {
  local manifest="$1"
  local name="$2"
  local status="$3"
  local reason="$4"
  awk -v want_name="$name" -v want_status="$status" -v want_reason="$reason" '
    index($0, "\"name\": \"" want_name "\"") { active=1; got_status=0; got_reason=0 }
    active && index($0, "\"status\": \"" want_status "\"") { got_status=1 }
    active && index($0, "\"reason\": \"" want_reason "\"") { got_reason=1 }
    active && index($0, "}") { if (got_status && got_reason) found=1; active=0 }
    END { exit(found ? 0 : 1) }
  ' "$manifest" || {
    echo "missing report manifest contract: $name $status $reason" >&2
    cat "$manifest" >&2
    exit 1
  }
}

assert_report_manifest_entry "$PROJMUX_SMOKE_WORKDIR/report-manifest.json" \
  "operational-errors.json" "omitted" "source-missing"
assert_report_manifest_entry "$PROJMUX_SMOKE_WORKDIR/report-manifest.json" \
  "ai-ingest-summary.json" "omitted" "source-missing"

# Dedicated empty state proves both bounded sources remain missing and report
# reads do not create them. The output parent is separate from the state root.
missing_state="$PROJMUX_SMOKE_WORKDIR/report-missing-state"
missing_report="$PROJMUX_SMOKE_WORKDIR/report-missing-output/report.tar.gz"
if [[ -e "$missing_state" || -e "$(dirname "$missing_report")" ]]; then
  echo "missing-source fixture existed before explicit report request" >&2
  exit 1
fi
XDG_STATE_HOME="$missing_state" "$bin" diagnostics report --output "$missing_report" \
  >"$PROJMUX_SMOKE_WORKDIR/report-missing-preview.txt"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/report-missing-preview.txt" "archive write follows this complete preview"
tar -xOzf "$missing_report" manifest.json >"$PROJMUX_SMOKE_WORKDIR/report-missing-manifest.json"
assert_report_manifest_entry "$PROJMUX_SMOKE_WORKDIR/report-missing-manifest.json" \
  "operational-errors.json" "omitted" "source-missing"
assert_report_manifest_entry "$PROJMUX_SMOKE_WORKDIR/report-missing-manifest.json" \
  "ai-ingest-summary.json" "omitted" "source-missing"
if [[ -e "$missing_state" ]]; then
  echo "missing-source report created state input paths" >&2
  exit 1
fi
XDG_STATE_HOME="$missing_state" "$bin" doctor --json --section logs \
  >"$PROJMUX_SMOKE_WORKDIR/doctor-missing-logs.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor-missing-logs.json" '"code": "logs.state.missing"'
if [[ -e "$missing_state" ]]; then
  echo "missing-source Doctor created state input paths" >&2
  exit 1
fi

# Corrupt bounded sources degrade to stable omissions without mode repair.
corrupt_state="$PROJMUX_SMOKE_WORKDIR/report-corrupt-state"
corrupt_operations="$corrupt_state/projmux/logs/operations.jsonl"
corrupt_ingest="$corrupt_state/projmux/ai-ingest.log"
mkdir -p "$(dirname "$corrupt_operations")"
printf 'corrupt\n{"truncated"' >"$corrupt_operations"
printf 'not-json\n' >"$corrupt_ingest"
chmod 0644 "$corrupt_operations" "$corrupt_ingest"
corrupt_report="$PROJMUX_SMOKE_WORKDIR/report-corrupt/report.tar.gz"
XDG_STATE_HOME="$corrupt_state" "$bin" diagnostics report --output "$corrupt_report" \
  >"$PROJMUX_SMOKE_WORKDIR/report-corrupt-preview.txt"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/report-corrupt-preview.txt" "archive write follows this complete preview"
tar -xOzf "$corrupt_report" manifest.json >"$PROJMUX_SMOKE_WORKDIR/report-corrupt-manifest.json"
assert_report_manifest_entry "$PROJMUX_SMOKE_WORKDIR/report-corrupt-manifest.json" \
  "operational-errors.json" "omitted" "source-corrupt-no-valid-errors"
assert_report_manifest_entry "$PROJMUX_SMOKE_WORKDIR/report-corrupt-manifest.json" \
  "ai-ingest-summary.json" "omitted" "source-corrupt-no-safe-records"
XDG_STATE_HOME="$corrupt_state" "$bin" doctor --json --section logs \
  >"$PROJMUX_SMOKE_WORKDIR/doctor-corrupt-logs.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor-corrupt-logs.json" '"code": "logs.journal.malformed"'
if [[ "$(stat -c '%a' "$corrupt_operations")" != "644" ]] ||
  [[ "$(stat -c '%a' "$corrupt_ingest")" != "644" ]]; then
  echo "support report repaired corrupt source modes" >&2
  exit 1
fi

# A failed preview writer proves the built CLI creates neither output parent
# nor archive before the complete preview is accepted.
preview_failure_parent="$PROJMUX_SMOKE_WORKDIR/report-preview-failure"
set +e
XDG_STATE_HOME="$corrupt_state" "$bin" diagnostics report \
  --output "$preview_failure_parent/report.tar.gz" >/dev/full 2>"$PROJMUX_SMOKE_WORKDIR/report-preview-failure.err"
preview_failure_status=$?
set -e
if [[ "$preview_failure_status" == "0" ]] || [[ -e "$preview_failure_parent" ]]; then
  echo "failed report preview wrote output state" >&2
  exit 1
fi
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/report-preview-failure.err" "write support report preview failed"

# An output collision preserves exact bytes and mode and leaves no temp file.
collision_parent="$PROJMUX_SMOKE_WORKDIR/report-collision"
collision_report="$collision_parent/report.tar.gz"
mkdir -p "$collision_parent"
printf 'existing-report-bytes\n' >"$collision_report"
chmod 0640 "$collision_report"
cp "$collision_report" "$PROJMUX_SMOKE_WORKDIR/report-collision-before"
set +e
XDG_STATE_HOME="$corrupt_state" "$bin" diagnostics report --output "$collision_report" \
  >"$PROJMUX_SMOKE_WORKDIR/report-collision.out" 2>"$PROJMUX_SMOKE_WORKDIR/report-collision.err"
collision_status=$?
set -e
if [[ "$collision_status" == "0" ]]; then
  echo "support report collision unexpectedly succeeded" >&2
  exit 1
fi
cmp "$PROJMUX_SMOKE_WORKDIR/report-collision-before" "$collision_report"
if [[ "$(stat -c '%a' "$collision_report")" != "640" ]] ||
  compgen -G "$collision_parent/.projmux-support-*.tmp" >/dev/null; then
  echo "support report collision changed target mode or left temp state" >&2
  exit 1
fi
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/report-collision.err" "support report destination already exists"

# Permission denial uses a non-root execution when the container itself is
# root. If that adapter is unavailable, record and assert the explicit skip so
# root never turns chmod into a flaky false pass.
permission_case=""
permission_root="$PROJMUX_SMOKE_WORKDIR/report-permission"
permission_state="$permission_root/state"
permission_operations="$permission_state/projmux/logs/operations.jsonl"
permission_ingest="$permission_state/projmux/ai-ingest.log"
permission_output="$permission_root/output/report.tar.gz"
permission_operations_mode_before=""
permission_ingest_mode_before=""
mkdir -p "$(dirname "$permission_operations")" "$permission_root/home" "$permission_root/config" "$permission_root/output"
printf '{}\n' >"$permission_operations"
printf '{}\n' >"$permission_ingest"
if [[ "$(id -u)" == "0" ]]; then
  if command -v runuser >/dev/null 2>&1 && id nobody >/dev/null 2>&1; then
    cp "$bin" "$permission_root/projmux"
    chmod 0755 "$permission_root/projmux"
    chown -R nobody:nogroup "$permission_root/home" "$permission_root/config" "$permission_root/output"
    chown nobody:nogroup "$permission_state/projmux" "$permission_state/projmux/logs"
    chmod 0500 "$permission_state/projmux" "$permission_state/projmux/logs"
    chown root:root "$permission_operations" "$permission_ingest"
    chmod 0600 "$permission_operations" "$permission_ingest"
    permission_operations_mode_before="$(stat -c '%a' "$permission_operations")"
    permission_ingest_mode_before="$(stat -c '%a' "$permission_ingest")"
    chmod 0711 "$PROJMUX_SMOKE_WORKDIR"
    runuser -u nobody -- env \
      HOME="$permission_root/home" \
      XDG_CONFIG_HOME="$permission_root/config" \
      XDG_STATE_HOME="$permission_state" \
      "$permission_root/projmux" diagnostics report --output "$permission_output" \
      >"$PROJMUX_SMOKE_WORKDIR/report-permission-preview.txt"
    runuser -u nobody -- env \
      HOME="$permission_root/home" \
      XDG_CONFIG_HOME="$permission_root/config" \
      XDG_STATE_HOME="$permission_state" \
      "$permission_root/projmux" doctor --json --section logs \
      >"$PROJMUX_SMOKE_WORKDIR/doctor-permission.json"
    chmod 0700 "$PROJMUX_SMOKE_WORKDIR"
    permission_case="passed-non-root-adapter"
  else
    echo "SKIP: report permission-denied fixture requires runuser+nobody when integration runs as root" >&2
    permission_case="skipped-root-no-adapter"
  fi
else
  chmod 0500 "$permission_state/projmux" "$permission_state/projmux/logs"
  chmod 0000 "$permission_operations" "$permission_ingest"
  permission_operations_mode_before="$(stat -c '%a' "$permission_operations")"
  permission_ingest_mode_before="$(stat -c '%a' "$permission_ingest")"
  XDG_STATE_HOME="$permission_state" "$bin" diagnostics report --output "$permission_output" \
    >"$PROJMUX_SMOKE_WORKDIR/report-permission-preview.txt"
  XDG_STATE_HOME="$permission_state" "$bin" doctor --json --section logs \
    >"$PROJMUX_SMOKE_WORKDIR/doctor-permission.json"
  permission_case="passed-direct-non-root"
fi
case "$permission_case" in
passed-*)
  tar -xOzf "$permission_output" manifest.json >"$PROJMUX_SMOKE_WORKDIR/report-permission-manifest.json"
  assert_report_manifest_entry "$PROJMUX_SMOKE_WORKDIR/report-permission-manifest.json" \
    "operational-errors.json" "omitted" "source-permission-denied"
  assert_report_manifest_entry "$PROJMUX_SMOKE_WORKDIR/report-permission-manifest.json" \
    "ai-ingest-summary.json" "omitted" "source-permission-denied"
  smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor-permission.json" '"code": "logs.recent-errors.unavailable"'
  smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor-permission.json" '"code": "logs.state.not-writable"'
  smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor-permission.json" '"code": "logs.directory.not-writable"'
  if ! grep -Eq '"code": "logs.journal.(not-writable|insecure-permissions)"' "$PROJMUX_SMOKE_WORKDIR/doctor-permission.json"; then
    echo "permission-denied Doctor fixture did not classify journal writability" >&2
    cat "$PROJMUX_SMOKE_WORKDIR/doctor-permission.json" >&2
    exit 1
  fi
  if [[ "$(stat -c '%a' "$permission_operations")" != "$permission_operations_mode_before" ]] ||
    [[ "$(stat -c '%a' "$permission_ingest")" != "$permission_ingest_mode_before" ]] ||
    compgen -G "$permission_root/output/.projmux-support-*.tmp" >/dev/null; then
    echo "permission-denied report repaired sources or left temp state" >&2
    exit 1
  fi
  chmod 0700 "$permission_state/projmux" "$permission_state/projmux/logs"
  chmod 0600 "$permission_operations" "$permission_ingest"
  ;;
skipped-root-no-adapter) ;;
*)
  echo "permission-denied report fixture did not choose an asserted branch" >&2
  exit 1
  ;;
esac

"$bin" internal tmux print-config --bin "$bin" >"$PROJMUX_SMOKE_WORKDIR/projmux.conf"
# Both row-0 HUD budgets are tmux formats derived from the attached client,
# not literal cell counts; the generated config is where that derivation has
# to survive quoting end to end.
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/projmux.conf" 'internal status notify --max-width #{?#{e|<:#{client_width},40},#{e|/:#{client_width},2},#{?#{e|<:#{client_width},160},20,#{?#{e|<:#{client_width},220},#{e|-:#{client_width},140},80}}}'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/projmux.conf" 'internal status usage --max-width #{e|-:#{client_width},#{?#{e|<:#{client_width},40},#{e|/:#{client_width},2},#{?#{e|<:#{client_width},160},20,#{?#{e|<:#{client_width},220},#{e|-:#{client_width},140},80}}}}'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/projmux.conf" "internal tmux popup-toggle"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/projmux.conf" "internal statusbar click"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/projmux.conf" "notify-sidebar"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/projmux.conf" "set -g @projmux_live_resources off"
smoke_assert_file_lacks "$PROJMUX_SMOKE_WORKDIR/projmux.conf" "internal status resources"
# A newly installed binary must not emit a pre-namespace plumbing route into
# generated config. Every occurrence is prefixed by the quoted binary path, so
# anchoring on "'$bin' <route>" is what separates an emitted invocation from an
# unrelated substring such as a popup mode name.
for relocated in status statusbar preview session-popup tmux key-broker popup-wait-key; do
  smoke_assert_file_lacks "$PROJMUX_SMOKE_WORKDIR/projmux.conf" "'$bin' $relocated"
done

mkdir -p "$XDG_CONFIG_HOME/projmux"
printf 'on\n' >"$XDG_CONFIG_HOME/projmux/live-resources"
"$bin" internal tmux print-config --bin "$bin" >"$PROJMUX_SMOKE_WORKDIR/projmux-resources.conf"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/projmux-resources.conf" "set -g @projmux_live_resources on"

resources_first="$("$bin" internal status resources)"
if [[ ! "$resources_first" =~ CPU[[:space:]]{2}--%.*MEM[[:space:]]{1,3}[0-9]{1,3}% ]] ||
  [[ "$resources_first" =~ (normal|warning|critical|unknown) ]]; then
  echo "expected first resource sample to show unavailable CPU and numeric memory, got: $resources_first" >&2
  exit 1
fi
sleep 0.1
resources_second="$("$bin" internal status resources)"
if [[ ! "$resources_second" =~ CPU[[:space:]]{1,3}[0-9]{1,3}%.*MEM[[:space:]]{1,3}[0-9]{1,3}% ]] ||
  [[ "$resources_second" =~ (normal|warning|critical|unknown) ]]; then
  echo "expected second resource sample to show numeric CPU and memory, got: $resources_second" >&2
  exit 1
fi

"$bin" internal tmux install \
  --bin "$bin" \
  --config "$HOME/.tmux.conf" \
  --include "$XDG_CONFIG_HOME/tmux/projmux.conf" \
  >"$PROJMUX_SMOKE_WORKDIR/install.out"
smoke_assert_file_contains "$HOME/.tmux.conf" "source-file"
smoke_assert_file_contains "$XDG_CONFIG_HOME/tmux/projmux.conf" "unbind-key -q F"

export TMUX_TMPDIR="$PROJMUX_SMOKE_WORKDIR/tmux"
mkdir -p "$TMUX_TMPDIR"
chmod 0700 "$TMUX_TMPDIR"
PROJMUX_SMOKE_TMUX_SOCKET="projmux-it-$$-$RANDOM"
export PROJMUX_SMOKE_TMUX_SOCKET
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" new-session -d -s integration-smoke -c "$smoke_root" sleep 300
PROJMUX_SMOKE_TMUX_STARTED=1
PROJMUX_SMOKE_TMUX_ACTUAL="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p -t integration-smoke '#{socket_path}')"
case "$PROJMUX_SMOKE_TMUX_ACTUAL" in
  "$PROJMUX_SMOKE_WORKDIR"/tmux/*) ;;
  *)
    echo "integration socket escaped smoke root: $PROJMUX_SMOKE_TMUX_ACTUAL" >&2
    exit 1
    ;;
esac

# A pre-rename-pane-label config could leave C-t installed in the live root
# table even after the retired keymap action disappeared. Seed that exact stale
# shape plus an unrelated live binding before apply.
keymap_path="$XDG_CONFIG_HOME/projmux/keymap.toml"
install -m 0644 "$smoke_root/test/fixtures/keymaps/stale-pane-label-current.toml" "$keymap_path"
cp "$keymap_path" "$PROJMUX_SMOKE_WORKDIR/keymap-mp.before"
tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" bind-key -n C-t command-prompt -p "pane label:" "set-option -p @projmux_pane_label '%%'"
tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" bind-key -n M-F12 display-message "unrelated-live-binding"

apply_out="$("$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET")"
smoke_assert_output_contains "$apply_out" "reloaded tmux server -L $PROJMUX_SMOKE_TMUX_SOCKET: 1 sessions"

if stale_ct="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root C-t 2>/dev/null)"; then
  echo "expected stale C-t pane-label binding to be absent after apply, got: $stale_ct" >&2
  exit 1
fi
mp_binding="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root M-p)"
if [[ "$mp_binding" != *"command-prompt"* || "$mp_binding" != *"pane label:"* || "$mp_binding" != *"@projmux_pane_label"* ]]; then
  echo "expected current M-p pane-label binding after apply, got: $mp_binding" >&2
  exit 1
fi
unrelated_binding="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root M-F12)"
if [[ "$unrelated_binding" != *"unrelated-live-binding"* ]]; then
  echo "expected unrelated live binding to survive apply, got: $unrelated_binding" >&2
  exit 1
fi
ma_binding="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root M-a)"
if [[ "$ma_binding" != *"sessionizer-sidebar"* ]]; then
  echo "expected unrelated current M-a binding to survive apply, got: $ma_binding" >&2
  exit 1
fi

# The first apply against an unversioned (v0) keymap migrates it to
# schema_version = 2 and leaves one digest-named backup holding the untouched
# original. The rewrite happens before any generated config is written, and the
# effective bindings asserted above are what prove it preserved behaviour.
smoke_assert_keymap_backup_count() {
  local want="$1"
  local got
  got="$(find "$XDG_CONFIG_HOME/projmux" -maxdepth 1 -name 'keymap.toml.pre-v1-*.bak' | wc -l)"
  if [[ "$got" != "$want" ]]; then
    echo "expected $want keymap pre-v1 backup(s), got $got" >&2
    find "$XDG_CONFIG_HOME/projmux" -maxdepth 1 -name 'keymap.toml*' >&2
    exit 1
  fi
}
smoke_assert_file_contains "$keymap_path" "schema_version = 2"
smoke_assert_file_contains "$keymap_path" '[bindings."pane.rename"]'
smoke_assert_file_contains "$keymap_path" '[bindings."project-sidebar.toggle"]'
smoke_assert_keymap_backup_count 1
cmp "$PROJMUX_SMOKE_WORKDIR/keymap-mp.before" \
  "$(find "$XDG_CONFIG_HOME/projmux" -maxdepth 1 -name 'keymap.toml.pre-v1-*.bak')"
cp "$keymap_path" "$PROJMUX_SMOKE_WORKDIR/keymap-mp.migrated"
cp "$XDG_CONFIG_HOME/projmux/tmux.conf" "$PROJMUX_SMOKE_WORKDIR/tmux-mp.first"

# Rendering is a read: it reports the pending migration on stderr when there is
# one and never touches the keymap.
render_err="$PROJMUX_SMOKE_WORKDIR/config-render-app.err"
"$bin" config render app --bin "$bin" >"$PROJMUX_SMOKE_WORKDIR/config-render-app.out" 2>"$render_err"
cmp "$PROJMUX_SMOKE_WORKDIR/keymap-mp.migrated" "$keymap_path"
smoke_assert_keymap_backup_count 1
if [[ -s "$render_err" ]] && ! grep -q "keymap migration" "$render_err"; then
  echo "unexpected config render stderr: $(cat "$render_err")" >&2
  exit 1
fi

repeat_apply_out="$("$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET")"
smoke_assert_output_contains "$repeat_apply_out" "reloaded tmux server -L $PROJMUX_SMOKE_TMUX_SOCKET: 1 sessions"
cmp "$PROJMUX_SMOKE_WORKDIR/tmux-mp.first" "$XDG_CONFIG_HOME/projmux/tmux.conf"
# Repeat apply on an already-current keymap writes no bytes and adds no backup.
cmp "$PROJMUX_SMOKE_WORKDIR/keymap-mp.migrated" "$keymap_path"
smoke_assert_keymap_backup_count 1
if tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root C-t >/dev/null 2>&1; then
  echo "repeated apply restored retired C-t binding" >&2
  exit 1
fi

# A current action may explicitly reclaim C-t. The retired cleanup must render
# and execute first so the current new-window binding wins without retaining
# the stale pane-label body.
install -m 0644 "$smoke_root/test/fixtures/keymaps/stale-pane-label-ct-reassigned.toml" "$keymap_path"
cp "$keymap_path" "$PROJMUX_SMOKE_WORKDIR/keymap-ct-reassigned.before"
# Re-seeding a v0 file means the next apply migrates again, against a different
# original, so this one earns its own backup.
reassign_apply_out="$("$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET")"
smoke_assert_output_contains "$reassign_apply_out" "reloaded tmux server -L $PROJMUX_SMOKE_TMUX_SOCKET: 1 sessions"
ct_binding="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root C-t)"
if [[ "$ct_binding" != *"new-window"* || "$ct_binding" == *"pane label:"* || "$ct_binding" == *"@projmux_pane_label"* ]]; then
  echo "expected current C-t new-window binding without stale pane-label body, got: $ct_binding" >&2
  exit 1
fi
if [[ "$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root M-p)" != *"pane label:"* ]]; then
  echo "expected M-p pane-label binding to remain after C-t reassignment" >&2
  exit 1
fi
if [[ "$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root M-F12)" != *"unrelated-live-binding"* ]]; then
  echo "expected unrelated live binding to survive C-t reassignment apply" >&2
  exit 1
fi
smoke_assert_file_contains "$keymap_path" "schema_version = 2"
smoke_assert_file_contains "$keymap_path" '[bindings."window.create"]'
smoke_assert_keymap_backup_count 2
cp "$keymap_path" "$PROJMUX_SMOKE_WORKDIR/keymap-ct-reassigned.migrated"
cp "$XDG_CONFIG_HOME/projmux/tmux.conf" "$PROJMUX_SMOKE_WORKDIR/tmux-ct-reassigned.first"
"$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET" \
  >"$PROJMUX_SMOKE_WORKDIR/reassign-repeat-apply.out"
cmp "$PROJMUX_SMOKE_WORKDIR/tmux-ct-reassigned.first" "$XDG_CONFIG_HOME/projmux/tmux.conf"
cmp "$PROJMUX_SMOKE_WORKDIR/keymap-ct-reassigned.migrated" "$keymap_path"
smoke_assert_keymap_backup_count 2
ct_binding="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root C-t)"
if [[ "$ct_binding" != *"new-window"* || "$ct_binding" == *"pane label:"* ]]; then
  echo "repeated reassignment apply changed C-t ownership: $ct_binding" >&2
  exit 1
fi

app_flag="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv @projmux_app)"
if [[ "$app_flag" != "1" ]]; then
  echo "expected tmux apply to set @projmux_app=1, got: $app_flag" >&2
  exit 1
fi

# Schema v2 sequences compile shared prefixes into one generated trie. Apply
# records the exact generated roots/tables so a repeat source is idempotent and
# a later removal can retire stale state without touching unrelated bindings.
install -m 0644 "$smoke_root/test/fixtures/keymaps/sequences-v2.toml" "$keymap_path"
sequence_apply_out="$("$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET")"
smoke_assert_output_contains "$sequence_apply_out" "reloaded tmux server -L $PROJMUX_SMOKE_TMUX_SOCKET: 1 sessions"
sequence_roots="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv @projmux_sequence_roots)"
sequence_tables="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv @projmux_sequence_tables)"
if [[ "$sequence_roots" != "C-k" || -z "$sequence_tables" || "$sequence_tables" == *" "* ]]; then
  echo "expected one C-k sequence root/table, got roots=$sequence_roots tables=$sequence_tables" >&2
  exit 1
fi
for contract in \
  "root C-k switch-client -T $sequence_tables" \
  "$sequence_tables C-w new-window" \
  "$sequence_tables Enter set -g mouse" \
  "$sequence_tables Escape switch-client -T root" \
  "$sequence_tables Any switch-client -T root"; do
  read -r table key fragment <<<"$contract"
  binding="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T "$table" "$key")"
  if [[ "$binding" != *"$fragment"* ]]; then
    echo "sequence binding $table/$key missing $fragment: $binding" >&2
    exit 1
  fi
done
if [[ "$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root M-S-Right)" != *"next-window"* ]]; then
  echo "sequence apply changed existing single-chord behavior" >&2
  exit 1
fi
cp "$keymap_path" "$PROJMUX_SMOKE_WORKDIR/keymap-sequences.first"
cp "$XDG_CONFIG_HOME/projmux/tmux.conf" "$PROJMUX_SMOKE_WORKDIR/tmux-sequences.first"
"$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET" \
  >"$PROJMUX_SMOKE_WORKDIR/sequences-repeat-apply.out"
cmp "$PROJMUX_SMOKE_WORKDIR/keymap-sequences.first" "$keymap_path"
cmp "$PROJMUX_SMOKE_WORKDIR/tmux-sequences.first" "$XDG_CONFIG_HOME/projmux/tmux.conf"

# A duplicate sequence is rejected before keymap/config/live writes. Compare
# all three surfaces byte-for-byte, then install a valid no-sequence file to
# prove the previous generated root and table are removed.
install -m 0644 "$smoke_root/test/fixtures/keymaps/sequences-v2-conflict.toml" "$keymap_path"
cp "$keymap_path" "$PROJMUX_SMOKE_WORKDIR/keymap-sequences-conflict.before"
cp "$XDG_CONFIG_HOME/projmux/tmux.conf" "$PROJMUX_SMOKE_WORKDIR/tmux-sequences-conflict.before"
tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -a >"$PROJMUX_SMOKE_WORKDIR/live-sequences-conflict.before"
if "$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET" \
  >"$PROJMUX_SMOKE_WORKDIR/sequences-conflict.out" 2>"$PROJMUX_SMOKE_WORKDIR/sequences-conflict.err"; then
  echo "duplicate sequence apply unexpectedly succeeded" >&2
  exit 1
fi
cmp "$PROJMUX_SMOKE_WORKDIR/keymap-sequences-conflict.before" "$keymap_path"
cmp "$PROJMUX_SMOKE_WORKDIR/tmux-sequences-conflict.before" "$XDG_CONFIG_HOME/projmux/tmux.conf"
tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -a >"$PROJMUX_SMOKE_WORKDIR/live-sequences-conflict.after"
cmp "$PROJMUX_SMOKE_WORKDIR/live-sequences-conflict.before" "$PROJMUX_SMOKE_WORKDIR/live-sequences-conflict.after"

install -m 0644 "$smoke_root/test/fixtures/keymaps/sequences-v2-removed.toml" "$keymap_path"
"$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET" \
  >"$PROJMUX_SMOKE_WORKDIR/sequences-remove-apply.out"
stale_sequence_root="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root C-k 2>/dev/null || true)"
if [[ -n "$stale_sequence_root" ]]; then
  echo "removed sequence left stale C-k root binding: $stale_sequence_root" >&2
  exit 1
fi
if tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T "$sequence_tables" >/dev/null 2>&1; then
  echo "removed sequence left stale generated table $sequence_tables" >&2
  exit 1
fi
if [[ -n "$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv @projmux_sequence_roots)" ]] ||
  [[ -n "$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv @projmux_sequence_tables)" ]]; then
  echo "removed sequence left generated state options" >&2
  exit 1
fi
resources_flag="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv @projmux_live_resources)"
if [[ "$resources_flag" != "on" ]]; then
  echo "expected tmux apply to set @projmux_live_resources=on, got: $resources_flag" >&2
  exit 1
fi

# Row-0 HUD visibility is global presentation only. Drive all four persisted
# combinations through the production exact-socket apply path and inspect the
# live server after each source. One-off rows must own the whole client budget;
# all-off must collapse to one structural row while retaining quiet autosave.
notifications_visibility="$XDG_CONFIG_HOME/projmux/statusbar-visibility-notifications-hud"
usage_visibility="$XDG_CONFIG_HOME/projmux/statusbar-visibility-agent-usage-hud"
assert_live_hud_row() {
  local want_status="$1"
  local want_notify="$2"
  local want_usage="$3"
  local row0
  local status_count
  status_count="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv status)"
  row0="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv 'status-format[0]')"
  if [[ "$status_count" != "$want_status" ]]; then
    echo "HUD visibility live status count = $status_count, want $want_status" >&2
    exit 1
  fi
  if [[ "$want_notify" == "on" ]]; then
    [[ "$row0" == *"range=user|notify"* ]] || { echo "live row 0 lost notify range: $row0" >&2; exit 1; }
  elif [[ "$row0" == *"range=user|notify"* ]]; then
    echo "live row 0 retained notify range while hidden: $row0" >&2
    exit 1
  fi
  if [[ "$want_usage" == "on" ]]; then
    [[ "$row0" == *"range=user|usage"* ]] || { echo "live row 0 lost usage range: $row0" >&2; exit 1; }
  elif [[ "$row0" == *"range=user|usage"* ]]; then
    echo "live row 0 retained usage range while hidden: $row0" >&2
    exit 1
  fi
  if [[ "$want_notify" == "off" && "$want_usage" == "off" ]]; then
    [[ "$row0" == *"autosave-session-state --quiet"* ]] || { echo "all-off live row lost autosave: $row0" >&2; exit 1; }
    if [[ -n "$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv 'status-format[1]')" ]]; then
      echo "all-off live status retained row 1 residue" >&2
      exit 1
    fi
  fi
}

printf 'on\n' >"$notifications_visibility"
printf 'on\n' >"$usage_visibility"
"$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET" >/dev/null
assert_live_hud_row 2 on on

printf 'off\n' >"$usage_visibility"
"$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET" >/dev/null
assert_live_hud_row 2 on off
smoke_assert_file_contains "$XDG_CONFIG_HOME/projmux/tmux.conf" 'internal status notify --max-width #{client_width}'

printf 'off\n' >"$notifications_visibility"
printf 'on\n' >"$usage_visibility"
"$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET" >/dev/null
assert_live_hud_row 2 off on
smoke_assert_file_contains "$XDG_CONFIG_HOME/projmux/tmux.conf" 'internal status usage --max-width #{client_width}'

printf 'off\n' >"$usage_visibility"
"$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET" >/dev/null
assert_live_hud_row on off off

# Restore compatibility defaults for the remaining integration assertions.
printf 'on\n' >"$notifications_visibility"
printf 'on\n' >"$usage_visibility"
"$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET" >/dev/null
assert_live_hud_row 2 on on

# Agent Usage HUD provider/window leaves are consumed by the status command at
# render time, after the enabled-provider collection/cache path. Seed a
# backoff-guarded cache so this isolated real-tmux smoke is deterministic, then
# exercise all-on parity, provider-off, weekly-only official shedding, and
# all-provider-off empty text without changing the live row structure.
usage_leaf_state="$PROJMUX_SMOKE_WORKDIR/usage-visibility-cache"
mkdir -p "$usage_leaf_state"
cat >"$usage_leaf_state/snapshots.json" <<'USAGE_VISIBILITY_JSON'
{
  "version": 2,
  "last_collect": {"claude": "2026-08-16T12:00:00Z"},
  "backoff": {"claude": {"until": "2099-08-16T13:00:00Z", "consecutive": 2}},
  "snapshots": [
    {"model":"claude","window":"5h","pct":42,"resets_at":"2026-08-16T17:00:00Z","updated_at":"2026-08-16T12:00:00Z"},
    {"model":"claude","window":"weekly","pct":18,"resets_at":"2026-08-23T12:00:00Z","updated_at":"2026-08-16T12:00:00Z"}
  ]
}
USAGE_VISIBILITY_JSON
printf 'claude\n' >"$XDG_CONFIG_HOME/projmux/ai-enabled-agents"
claude_provider_visibility="$XDG_CONFIG_HOME/projmux/statusbar-visibility-agent-usage-provider-claude"
claude_5h_visibility="$XDG_CONFIG_HOME/projmux/statusbar-visibility-agent-usage-window-claude-5h"
claude_weekly_visibility="$XDG_CONFIG_HOME/projmux/statusbar-visibility-agent-usage-window-claude-weekly"

usage_missing_wide="$(PROJMUX_USAGE_STATE_DIR="$usage_leaf_state" "$bin" internal status usage --max-width 200)"
usage_missing_narrow="$(PROJMUX_USAGE_STATE_DIR="$usage_leaf_state" "$bin" internal status usage --max-width 40)"
printf 'on\n' >"$claude_provider_visibility"
printf 'on\n' >"$claude_5h_visibility"
printf 'on\n' >"$claude_weekly_visibility"
usage_on_wide="$(PROJMUX_USAGE_STATE_DIR="$usage_leaf_state" "$bin" internal status usage --max-width 200)"
usage_on_narrow="$(PROJMUX_USAGE_STATE_DIR="$usage_leaf_state" "$bin" internal status usage --max-width 40)"
[[ "$usage_missing_wide" == "$usage_on_wide" ]] || { echo "all-on wide usage bytes drifted" >&2; exit 1; }
[[ "$usage_missing_narrow" == "$usage_on_narrow" ]] || { echo "all-on narrow usage bytes drifted" >&2; exit 1; }

printf 'off\n' >"$claude_provider_visibility"
[[ -z "$(PROJMUX_USAGE_STATE_DIR="$usage_leaf_state" "$bin" internal status usage --max-width 200)" ]] || { echo "provider-off usage text was not empty" >&2; exit 1; }
printf 'on\n' >"$claude_provider_visibility"
printf 'off\n' >"$claude_weekly_visibility"
five_only="$(PROJMUX_USAGE_STATE_DIR="$usage_leaf_state" "$bin" internal status usage --max-width 200)"
[[ "$five_only" == *"5h"* && "$five_only" != *"weekly"* ]] || { echo "5h-only official projection drifted: $five_only" >&2; exit 1; }
printf 'on\n' >"$claude_weekly_visibility"
printf 'off\n' >"$claude_5h_visibility"
weekly_only="$(PROJMUX_USAGE_STATE_DIR="$usage_leaf_state" "$bin" internal status usage --max-width 24)"
[[ "$weekly_only" == *"weekly"* && "$weekly_only" != *"5h"* ]] || { echo "weekly-only official projection drifted: $weekly_only" >&2; exit 1; }
printf 'off\n' >"$claude_weekly_visibility"
[[ -z "$(PROJMUX_USAGE_STATE_DIR="$usage_leaf_state" "$bin" internal status usage --max-width 200)" ]] || { echo "all-window-off usage text was not empty" >&2; exit 1; }

explicit_usage="$(PROJMUX_USAGE_STATE_DIR="$usage_leaf_state" "$bin" agent usage --model claude --json)"
[[ "$explicit_usage" == *'"window": "5h"'* && "$explicit_usage" == *'"window": "weekly"'* ]] || { echo "explicit usage lost hidden windows: $explicit_usage" >&2; exit 1; }
printf 'claude,codex,antigravity\n' >"$XDG_CONFIG_HOME/projmux/ai-enabled-agents"
printf 'on\n' >"$claude_provider_visibility"
printf 'on\n' >"$claude_5h_visibility"
printf 'on\n' >"$claude_weekly_visibility"

# Row-1 component visibility shares the same production exact-socket apply
# path. Exercise a representative mixed layout and an empty row, proving
# ranges/jobs and owned spacing disappear together while the default Settings
# keybinding remains. The retired Kube range/job must never be generated.
project_visibility="$XDG_CONFIG_HOME/projmux/statusbar-visibility-project"
cwd_visibility="$XDG_CONFIG_HOME/projmux/statusbar-visibility-working-directory"
git_visibility="$XDG_CONFIG_HOME/projmux/statusbar-visibility-git"
clock_visibility="$XDG_CONFIG_HOME/projmux/statusbar-visibility-clock"
settings_visibility="$XDG_CONFIG_HOME/projmux/statusbar-visibility-settings-launcher"
live_resources="$XDG_CONFIG_HOME/projmux/live-resources"

printf 'off\n' >"$project_visibility"
printf 'on\n' >"$cwd_visibility"
printf 'off\n' >"$git_visibility"
printf 'off\n' >"$clock_visibility"
printf 'on\n' >"$settings_visibility"
printf 'on\n' >"$live_resources"
"$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET" >/dev/null
row1_left="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv status-left)"
row1_right="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv status-right)"
if [[ -n "$row1_left" || "$row1_right" != *"range=user|pwd"* || "$row1_right" != *"range=user|resources"* || "$row1_right" != *"range=user|settings"* ]]; then
  echo "mixed row-1 live layout missing retained components: left=$row1_left right=$row1_right" >&2
  exit 1
fi
if [[ "$row1_right" == *"range=user|git"* || "$row1_right" == *"internal status git"* || "$row1_right" == *"range=user|kube"* || "$row1_right" == *"status kube"* || "$row1_right" == *"projmux-status k"* || "$row1_right" == *" %Y-%m-%d %H:%M"* ]]; then
  echo "mixed row-1 live layout retained hidden component residue: $row1_right" >&2
  exit 1
fi

printf 'off\n' >"$cwd_visibility"
printf 'off\n' >"$settings_visibility"
printf 'off\n' >"$live_resources"
"$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET" >/dev/null
row1_right="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -gqv status-right)"
if [[ -n "$row1_right" ]]; then
  echo "empty row-1 live layout retained product residue: $row1_right" >&2
  exit 1
fi
for hidden_range in pwd git resources settings kube; do
  if [[ "$row1_right" == *"range=user|$hidden_range"* ]]; then
    echo "minimal row-1 live layout retained $hidden_range range: $row1_right" >&2
    exit 1
  fi
done
if [[ "$row1_right" == *"internal status resources"* || "$row1_right" == *" %Y-%m-%d %H:%M"* ]]; then
  echo "minimal row-1 live layout retained sampler/clock residue: $row1_right" >&2
  exit 1
fi
if [[ "$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-keys -T root M-5)" != *"ai-split-settings"* ]]; then
  echo "Settings launcher off removed the Settings keybinding" >&2
  exit 1
fi

# Restore compatibility defaults for the remaining integration assertions.
for visibility_path in "$project_visibility" "$cwd_visibility" "$git_visibility" "$clock_visibility" "$settings_visibility"; do
  printf 'on\n' >"$visibility_path"
done
printf 'on\n' >"$live_resources"
"$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$PROJMUX_SMOKE_TMUX_SOCKET" >/dev/null

# Doctor probes a fixed closed `tmux -L projmux show-options` argv. This test
# wrapper routes only that fixed name to the run-unique real socket so the
# binary boundary still exercises the actual server without accepting a raw
# socket/path from Doctor input.
doctor_mux_dir="$PROJMUX_SMOKE_WORKDIR/doctor-mux"
mkdir -p "$doctor_mux_dir"
real_doctor_tmux="$(command -v tmux)"
cat >"$doctor_mux_dir/tmux" <<'DOCTOR_TMUX'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" != "-L" || "${2:-}" != "projmux" ]]; then
  echo "unexpected Doctor tmux argv" >&2
  exit 64
fi
shift 2
exec "$PROJMUX_REAL_TMUX" -L "$PROJMUX_SMOKE_TMUX_SOCKET" "$@"
DOCTOR_TMUX
chmod 0755 "$doctor_mux_dir/tmux"

doctor_config="$XDG_CONFIG_HOME/projmux/tmux.conf"
cp "$doctor_config" "$PROJMUX_SMOKE_WORKDIR/doctor-config.before"
cp "$operations_log" "$PROJMUX_SMOKE_WORKDIR/doctor-operations.before"
find "$XDG_STATE_HOME/projmux" "$XDG_CONFIG_HOME/projmux" -printf '%p|%m|%s|%T@\n' | sort >"$PROJMUX_SMOKE_WORKDIR/doctor-inventory.before"
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -g >"$PROJMUX_SMOKE_WORKDIR/doctor-tmux-options.before"
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-sessions -F '#{session_id}|#{session_name}|#{session_windows}|#{session_attached}' >"$PROJMUX_SMOKE_WORKDIR/doctor-tmux-sessions.before"
pgrep -x tmux | sort >"$PROJMUX_SMOKE_WORKDIR/doctor-process-state.before" || true
for package_state in /var/lib/dpkg/status /lib/apk/db/installed /var/lib/rpm/rpmdb.sqlite; do
  if [[ -e "$package_state" ]]; then
    stat -c '%n|%m|%s|%Y' "$package_state"
  fi
done >"$PROJMUX_SMOKE_WORKDIR/doctor-package-state.before"
env -u TMUX -u TMUX_PANE \
  PATH="$doctor_mux_dir:$PATH" \
  PROJMUX_REAL_TMUX="$real_doctor_tmux" \
  "$bin" doctor --json >"$PROJMUX_SMOKE_WORKDIR/doctor-runtime-current.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor-runtime-current.json" '"code": "runtime.socket.reachable"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor-runtime-current.json" '"code": "runtime.config.generated-current"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor-runtime-current.json" '"code": "runtime.config.applied-current"'
cmp "$PROJMUX_SMOKE_WORKDIR/doctor-config.before" "$doctor_config"
cmp "$PROJMUX_SMOKE_WORKDIR/doctor-operations.before" "$operations_log"
find "$XDG_STATE_HOME/projmux" "$XDG_CONFIG_HOME/projmux" -printf '%p|%m|%s|%T@\n' | sort >"$PROJMUX_SMOKE_WORKDIR/doctor-inventory.after"
cmp "$PROJMUX_SMOKE_WORKDIR/doctor-inventory.before" "$PROJMUX_SMOKE_WORKDIR/doctor-inventory.after"
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" show-options -g >"$PROJMUX_SMOKE_WORKDIR/doctor-tmux-options.after"
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-sessions -F '#{session_id}|#{session_name}|#{session_windows}|#{session_attached}' >"$PROJMUX_SMOKE_WORKDIR/doctor-tmux-sessions.after"
cmp "$PROJMUX_SMOKE_WORKDIR/doctor-tmux-options.before" "$PROJMUX_SMOKE_WORKDIR/doctor-tmux-options.after"
cmp "$PROJMUX_SMOKE_WORKDIR/doctor-tmux-sessions.before" "$PROJMUX_SMOKE_WORKDIR/doctor-tmux-sessions.after"
pgrep -x tmux | sort >"$PROJMUX_SMOKE_WORKDIR/doctor-process-state.after" || true
cmp "$PROJMUX_SMOKE_WORKDIR/doctor-process-state.before" "$PROJMUX_SMOKE_WORKDIR/doctor-process-state.after"
for package_state in /var/lib/dpkg/status /lib/apk/db/installed /var/lib/rpm/rpmdb.sqlite; do
  if [[ -e "$package_state" ]]; then
    stat -c '%n|%m|%s|%Y' "$package_state"
  fi
done >"$PROJMUX_SMOKE_WORKDIR/doctor-package-state.after"
cmp "$PROJMUX_SMOKE_WORKDIR/doctor-package-state.before" "$PROJMUX_SMOKE_WORKDIR/doctor-package-state.after"

env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" set-option -g @projmux_config_digest "$(printf '0%.0s' {1..64})"
env -u TMUX -u TMUX_PANE \
  PATH="$doctor_mux_dir:$PATH" \
  PROJMUX_REAL_TMUX="$real_doctor_tmux" \
  "$bin" doctor --json --section runtime >"$PROJMUX_SMOKE_WORKDIR/doctor-runtime-stale.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/doctor-runtime-stale.json" '"code": "runtime.config.applied-stale"'
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" source-file "$doctor_config"

# Real lifecycle fixture. A tiny routing wrapper adds the run-unique -L socket
# for projmux subprocesses that intentionally do not accept a socket flag. The
# mutation itself is still performed by the host tmux executable.
real_tmux="$(command -v tmux)"
lifecycle_mux_dir="$PROJMUX_SMOKE_WORKDIR/lifecycle-mux"
mkdir -p "$lifecycle_mux_dir"
cat >"$lifecycle_mux_dir/tmux" <<'LIFECYCLE_TMUX'
#!/usr/bin/env bash
set -euo pipefail
if [[ -n "${PROJMUX_KILL_RACE_TARGET:-}" && "${1:-}" == "list-sessions" && "$*" == *"@projmux_ephemeral"* ]]; then
  output="$("$PROJMUX_REAL_TMUX" -L "$PROJMUX_SMOKE_TMUX_SOCKET" "$@")"
  "$PROJMUX_REAL_TMUX" -L "$PROJMUX_SMOKE_TMUX_SOCKET" kill-session -t "=$PROJMUX_KILL_RACE_TARGET"
  printf '%s\n' "$output"
  exit 0
fi
exec "$PROJMUX_REAL_TMUX" -L "$PROJMUX_SMOKE_TMUX_SOCKET" "$@"
LIFECYCLE_TMUX
chmod 0755 "$lifecycle_mux_dir/tmux"
lifecycle_path="$lifecycle_mux_dir:$PATH"
server_pid="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p -t integration-smoke '#{pid}')"
lifecycle_pane="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p -t integration-smoke '#{pane_id}')"
lifecycle_tmux_env="$PROJMUX_SMOKE_TMUX_ACTUAL,$server_pid,0"

# Keep one real control-mode client attached so explicit switch operations can
# select it without relying on pane contents, ordering, or send-keys.
control_fifo="$PROJMUX_SMOKE_WORKDIR/lifecycle-control.in"
control_out="$PROJMUX_SMOKE_WORKDIR/lifecycle-control.out"
mkfifo "$control_fifo"
exec 9<>"$control_fifo"
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" -C attach-session -t integration-smoke <&9 >"$control_out" 2>&1 &
control_pid=$!
control_client=""
for _ in $(seq 1 500); do
  control_client="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-clients -F '#{client_name}' 2>/dev/null | head -n 1 || true)"
  [[ -n "$control_client" ]] && break
  sleep 0.02
done
if [[ -z "$control_client" ]]; then
  echo "real lifecycle fixture could not establish control client" >&2
  exit 1
fi

run_inside_lifecycle() {
  env \
    PATH="$lifecycle_path" \
    PROJMUX_REAL_TMUX="$real_tmux" \
    TMUX="$lifecycle_tmux_env" \
    TMUX_PANE="$lifecycle_pane" \
    PROJMUX_SWITCH_TARGET_CLIENT="$control_client" \
    "$bin" "$@"
}

# Session State diagnostics uses the same run-unique socket and isolated XDG
# root. Capture a real snapshot containing seeded private cwd/command/session
# metadata, prove successful autosave stays silent, inject one quiet autosave
# failure, replay the actual latest snapshot, then delete it canonically.
session_state_root="$PROJMUX_SMOKE_WORKDIR/raw-session-state-project-$$"
session_state_name="raw-session-state-$$"
mkdir -p "$session_state_root"
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" new-session -d -s "$session_state_name" -c "$session_state_root" 'sleep 300'
session_state_pane="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p -t "=$session_state_name" '#{pane_id}')"
session_state_tmux_env="$PROJMUX_SMOKE_TMUX_ACTUAL,$server_pid,0"

env PATH="$lifecycle_path" PROJMUX_REAL_TMUX="$real_tmux" \
  TMUX="$session_state_tmux_env" TMUX_PANE="$session_state_pane" \
  "$bin" create snapshot >"$PROJMUX_SMOKE_WORKDIR/session-state-save.out"

session_state_log="$XDG_STATE_HOME/projmux/logs/operations.jsonl"
session_state_before_autosave="$(wc -l <"$session_state_log")"
env PATH="$lifecycle_path" PROJMUX_REAL_TMUX="$real_tmux" \
  PROJMUX_SESSIONSTATE_AUTOSAVE=on TMUX="$session_state_tmux_env" TMUX_PANE="$session_state_pane" \
  "$bin" internal tmux autosave-session-state --force >"$PROJMUX_SMOKE_WORKDIR/session-state-autosave.out"
session_state_after_autosave="$(wc -l <"$session_state_log")"
if [[ "$session_state_before_autosave" != "$session_state_after_autosave" ]]; then
  echo "successful autosave appended an operational event" >&2
  exit 1
fi

session_state_fail_mux="$PROJMUX_SMOKE_WORKDIR/session-state-fail-mux"
mkdir -p "$session_state_fail_mux"
cat >"$session_state_fail_mux/tmux" <<'SESSION_STATE_FAIL_TMUX'
#!/usr/bin/env bash
echo "raw session-state command /seed/private/path" >&2
exit 23
SESSION_STATE_FAIL_TMUX
chmod 0755 "$session_state_fail_mux/tmux"
env PATH="$session_state_fail_mux:$PATH" \
  PROJMUX_SESSIONSTATE_AUTOSAVE=on TMUX="$session_state_tmux_env" TMUX_PANE="$session_state_pane" \
  "$bin" internal tmux autosave-session-state --quiet >"$PROJMUX_SMOKE_WORKDIR/session-state-autosave-fail.out"

env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" kill-session -t "=$session_state_name"
run_inside_lifecycle switch sidebar-open --path "$session_state_root" --session "$session_state_name" --mode latest \
  >"$PROJMUX_SMOKE_WORKDIR/session-state-restore.out"
if ! env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" has-session -t "=$session_state_name" 2>/dev/null; then
  echo "actual latest snapshot restore did not recreate $session_state_name" >&2
  exit 1
fi
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" switch-client -c "$control_client" -t integration-smoke

"$bin" delete snapshot --session "$session_state_name" \
  >"$PROJMUX_SMOKE_WORKDIR/session-state-delete.out"
if find "$XDG_STATE_HOME/projmux/sessions" -maxdepth 1 -type f -name '*raw-session-state*' -print -quit | grep -q .; then
  echo "canonical snapshot delete left its target" >&2
  exit 1
fi
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" kill-session -t "=$session_state_name"

if [[ "$(grep -c '"event":"session-state.outcome".*"operation":"session-state.save".*"source":"manual"' "$session_state_log")" != "1" ]]; then
  echo "manual save did not emit exactly one closed session-state outcome" >&2
  exit 1
fi
if [[ "$(grep -c '"event":"session-state.outcome".*"operation":"session-state.autosave".*"code":"session-state.autosave.failed"' "$session_state_log")" != "1" ]]; then
  echo "quiet autosave failure did not emit exactly one closed error" >&2
  exit 1
fi
if [[ "$(grep -c '"event":"session-state.outcome".*"operation":"session-state.restore".*"source":"startup-latest"' "$session_state_log")" != "1" ]]; then
  echo "actual latest restore did not emit exactly one closed outcome" >&2
  exit 1
fi
if ! grep -q '"event":"session-state.outcome".*"operation":"session-state.delete".*"source":"manual".*"item_count":1' "$session_state_log"; then
  echo "canonical delete did not project its exact item count" >&2
  exit 1
fi
if grep -q '"event":"command.outcome".*"command":"session-state"' "$session_state_log" ||
  grep -q '"event":"command.outcome".*"command":"prune","subcommand":"session-state"' "$session_state_log" ||
  grep -q '"event":"command.outcome".*"command":"switch","subcommand":"sidebar-open"' "$session_state_log"; then
  echo "owned Session State mutation emitted a duplicate generic outcome" >&2
  exit 1
fi
for raw in "$session_state_root" "$session_state_name" 'sleep 300' 'raw session-state command' '/seed/private/path'; do
  if grep -Fq "$raw" "$session_state_log"; then
    echo "session-state operational journal leaked raw metadata: $raw" >&2
    exit 1
  fi
done

# Create: change the real origin pane cwd to a run-unique project, then let
# `switch open` ensures it and switches the attached control client to it.
create_root="$PROJMUX_SMOKE_WORKDIR/raw-create-project-$$"
mkdir -p "$create_root"
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" respawn-pane -k -t integration-smoke -c "$create_root" sleep 300
run_inside_lifecycle switch open "$create_root" >"$PROJMUX_SMOKE_WORKDIR/lifecycle-create.out"

# Deterministic real create failure: the production global pre-create hook
# aborts before tmux new-session and the target remains absent.
mkdir -p "$XDG_CONFIG_HOME/projmux"
cat >"$XDG_CONFIG_HOME/projmux/config.toml" <<'CREATE_FAILURE_HOOK'
[hooks.pre-create]
run = "exit 17"
CREATE_FAILURE_HOOK
create_fail_root="$PROJMUX_SMOKE_WORKDIR/raw-create-failure-$$"
mkdir -p "$create_fail_root"
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" respawn-pane -k -t integration-smoke -c "$create_fail_root" sleep 300
if run_inside_lifecycle switch open "$create_fail_root" >"$PROJMUX_SMOKE_WORKDIR/lifecycle-create-fail.out" 2>"$PROJMUX_SMOKE_WORKDIR/lifecycle-create-fail.err"; then
  echo "real pre-create failure unexpectedly succeeded" >&2
  exit 1
fi
rm -f "$XDG_CONFIG_HOME/projmux/config.toml"

# Switch success and failure through the formerly bypassing session-popup
# surface. The missing target exercises a real tmux switch-client failure.
run_inside_lifecycle internal session-popup open integration-smoke >"$PROJMUX_SMOKE_WORKDIR/lifecycle-switch.out"
if run_inside_lifecycle internal session-popup open raw-missing-session >"$PROJMUX_SMOKE_WORKDIR/lifecycle-switch-fail.out" 2>"$PROJMUX_SMOKE_WORKDIR/lifecycle-switch-fail.err"; then
  echo "real missing-session switch unexpectedly succeeded" >&2
  exit 1
fi

# Kill through the formerly bypassing prune surface.
for session in raw-ephemeral-one raw-ephemeral-two; do
  env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" new-session -d -s "$session" sleep 300
  env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" set-option -t "$session" -q @projmux_ephemeral 1
  env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" switch-client -c "$control_client" -t "=$session"
  env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" switch-client -c "$control_client" -t integration-smoke
done
run_inside_lifecycle runtime prune --keep=0 >"$PROJMUX_SMOKE_WORKDIR/lifecycle-kill.out"
for session in raw-ephemeral-one raw-ephemeral-two; do
  if env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" has-session -t "=$session" 2>/dev/null; then
    echo "real prune did not kill $session" >&2
    exit 1
  fi
done

# Deterministic real kill failure: after prune reads the real inventory, the
# routing wrapper removes the selected session with the real tmux executable;
# projmux's ensuing real kill-session sees the missing target and fails.
kill_race_target="raw-ephemeral-race"
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" new-session -d -s "$kill_race_target" sleep 300
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" set-option -t "$kill_race_target" -q @projmux_ephemeral 1
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" switch-client -c "$control_client" -t "=$kill_race_target"
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" switch-client -c "$control_client" -t integration-smoke
if PROJMUX_KILL_RACE_TARGET="$kill_race_target" run_inside_lifecycle runtime prune --keep=0 >"$PROJMUX_SMOKE_WORKDIR/lifecycle-kill-fail.out" 2>"$PROJMUX_SMOKE_WORKDIR/lifecycle-kill-fail.err"; then
  echo "real kill race failure unexpectedly succeeded" >&2
  exit 1
fi

# Attach success/failure use a real pseudo-terminal and the same unique socket
# routing wrapper. The successful client is detached by a server query, not by
# pane keystrokes or screen scraping.
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" new-session -d -s raw-attach-success sleep 300
attach_command="env -u TMUX -u TMUX_PANE TERM=xterm PATH=$lifecycle_path PROJMUX_REAL_TMUX=$real_tmux PROJMUX_SMOKE_TMUX_SOCKET=$PROJMUX_SMOKE_TMUX_SOCKET $bin internal session-popup open raw-attach-success"
timeout 10 script -qec "$attach_command" /dev/null >"$PROJMUX_SMOKE_WORKDIR/lifecycle-attach.out" 2>"$PROJMUX_SMOKE_WORKDIR/lifecycle-attach.err" &
attach_pid=$!
attach_seen=0
for _ in $(seq 1 500); do
  if env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" list-clients -F '#{client_session}' 2>/dev/null | grep -Fxq raw-attach-success; then
    attach_seen=1
    break
  fi
  sleep 0.02
done
if [[ "$attach_seen" != "1" ]]; then
  echo "real attach client did not appear" >&2
	cat "$PROJMUX_SMOKE_WORKDIR/lifecycle-attach.out" "$PROJMUX_SMOKE_WORKDIR/lifecycle-attach.err" >&2 || true
  exit 1
fi
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" detach-client -s raw-attach-success
wait "$attach_pid"
if timeout 10 script -qec "env -u TMUX -u TMUX_PANE TERM=xterm PATH=$lifecycle_path PROJMUX_REAL_TMUX=$real_tmux PROJMUX_SMOKE_TMUX_SOCKET=$PROJMUX_SMOKE_TMUX_SOCKET $bin internal session-popup open raw-attach-missing" /dev/null >"$PROJMUX_SMOKE_WORKDIR/lifecycle-attach-fail.out" 2>"$PROJMUX_SMOKE_WORKDIR/lifecycle-attach-fail.err"; then
  echo "real missing-session attach unexpectedly succeeded" >&2
  exit 1
fi

# Apply classification: a real absent unique socket is unreachable, while an
# executable/runner-style list failure remains the generic closed apply code.
missing_apply_socket="missing-$PROJMUX_SMOKE_TMUX_SOCKET"
"$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket "$missing_apply_socket" >"$PROJMUX_SMOKE_WORKDIR/apply-unreachable.out"
generic_mux_dir="$PROJMUX_SMOKE_WORKDIR/generic-apply-mux"
mkdir -p "$generic_mux_dir"
cat >"$generic_mux_dir/tmux" <<'GENERIC_TMUX'
#!/usr/bin/env bash
echo "permission denied by deterministic integration runner" >&2
exit 13
GENERIC_TMUX
chmod 0755 "$generic_mux_dir/tmux"
PATH="$generic_mux_dir:$PATH" "$bin" internal tmux apply --bin "$bin" --config "$XDG_CONFIG_HOME/projmux/tmux.conf" --socket raw-generic-socket >"$PROJMUX_SMOKE_WORKDIR/apply-generic.out"

# Stop only the control client process; the guarded trap owns the exact server.
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" detach-client -s integration-smoke >/dev/null 2>&1 || true
exec 9>&-
wait "$control_pid" || true

# Each explicit apply owns one correlated lifecycle pair and suppresses the
# older generic top-level outcome. Eighteen apply invocations ran above: the
# existing six, five row-0 visibility convergence applies, three row-1
# mixed/minimal/default convergence applies, and four sequence applies
# (install, repeat, rejected duplicate, removal). The rejected duplicate still
# owns a correlated pair because it fails inside the apply operation.
operations_log="$XDG_STATE_HOME/projmux/logs/operations.jsonl"
expected_apply_count=18
apply_starts="$(grep -c '"event":"lifecycle.start".*"operation":"tmux.apply"' "$operations_log")"
apply_outcomes="$(grep -c '"event":"lifecycle.outcome".*"operation":"tmux.apply"' "$operations_log")"
if [[ "$apply_starts" != "$expected_apply_count" || "$apply_outcomes" != "$expected_apply_count" ]]; then
  echo "expected $expected_apply_count correlated tmux apply lifecycle pairs, got starts=$apply_starts outcomes=$apply_outcomes" >&2
  exit 1
fi
if grep -q '"event":"command.outcome".*"command":"tmux","subcommand":"apply"' "$operations_log"; then
  echo "tmux apply emitted a duplicate generic top-level outcome" >&2
  exit 1
fi
apply_run_counts="$PROJMUX_SMOKE_WORKDIR/apply-run-counts.txt"
grep '"operation":"tmux.apply"' "$operations_log" |
  sed -n 's/.*"run_id":"\([^"]*\)".*/\1/p' |
  sort | uniq -c >"$apply_run_counts"
if [[ "$(wc -l <"$apply_run_counts")" != "$expected_apply_count" ]] || ! awk '$1 != 2 { bad=1 } END { exit bad }' "$apply_run_counts"; then
  echo "tmux apply start/outcome run_id correlation failed" >&2
  cat "$apply_run_counts" >&2
  exit 1
fi
for code in tmux.apply.socket-unreachable tmux.apply.failed; do
  if ! grep -q '"event":"lifecycle.outcome".*"code":"'"$code"'"' "$operations_log"; then
    echo "missing apply classification: $code" >&2
    exit 1
  fi
done

# Every lifecycle run is exactly one start/outcome pair with no generic
# duplicate. Real fixture failures remain closed and raw routing never enters
# the journal.
lifecycle_run_counts="$PROJMUX_SMOKE_WORKDIR/lifecycle-run-counts.txt"
grep '"event":"lifecycle.\(start\|outcome\)"' "$operations_log" |
  sed -n 's/.*"run_id":"\([^"]*\)".*/\1/p' |
  sort | uniq -c >"$lifecycle_run_counts"
if ! awk '$1 != 2 { bad=1 } END { exit bad }' "$lifecycle_run_counts"; then
  echo "lifecycle start/outcome run_id correlation failed" >&2
  cat "$lifecycle_run_counts" >&2
  exit 1
fi
for operation in session.create session.attach session.switch session.kill; do
  if ! grep -q '"event":"lifecycle.start".*"operation":"'"$operation"'"' "$operations_log" ||
    ! grep -q '"event":"lifecycle.outcome".*"operation":"'"$operation"'"' "$operations_log"; then
    echo "missing real lifecycle pair for $operation" >&2
    exit 1
  fi
done
for code in session.create.failed session.attach.failed session.switch.failed session.kill.failed; do
  if ! grep -q '"event":"lifecycle.outcome".*"code":"'"$code"'"' "$operations_log"; then
    echo "missing real lifecycle failure classification: $code" >&2
    exit 1
  fi
done
if grep -q '"event":"command.outcome".*"command":"\(current\|session-popup\|prune\)"' "$operations_log"; then
  echo "real lifecycle fixture emitted a duplicate generic top-level outcome" >&2
  exit 1
fi
for raw in "$PROJMUX_SMOKE_TMUX_ACTUAL" "$PROJMUX_SMOKE_WORKDIR" raw-create-project raw-missing-session raw-ephemeral raw-attach raw-generic-socket "$lifecycle_pane"; do
  if grep -Fq "$raw" "$operations_log"; then
    echo "operational lifecycle journal leaked raw routing: $raw" >&2
    exit 1
  fi
done

"$bin" create notification --id integration-smoke --text "integration smoke" --target "integration-smoke" >"$PROJMUX_SMOKE_WORKDIR/notify-push.out"
"$bin" get notifications --json >"$PROJMUX_SMOKE_WORKDIR/notify-list.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/notify-list.json" "integration smoke"
"$bin" notification ack integration-smoke >"$PROJMUX_SMOKE_WORKDIR/notify-ack.out"

# Operational outcomes persist in the isolated XDG state tree. Read-only hot
# paths and the viewer itself must not append, while an injected journal path
# failure must not change a successful state-changing command.
if [[ ! -f "$operations_log" ]]; then
  echo "expected operational diagnostics log: $operations_log" >&2
  exit 1
fi
if [[ "$(stat -c '%a' "$XDG_STATE_HOME/projmux")" != "700" ]] ||
  [[ "$(stat -c '%a' "$XDG_STATE_HOME/projmux/logs")" != "700" ]] ||
  [[ "$(stat -c '%a' "$operations_log")" != "600" ]]; then
  echo "expected private 0700/0700/0600 operational diagnostics modes" >&2
  exit 1
fi

before_read_only="$(wc -l <"$operations_log")"
"$bin" internal status resources >"$PROJMUX_SMOKE_WORKDIR/resources-read-only.out"
"$bin" diagnostics log --tail 1 --json --level info --component cli \
  >"$PROJMUX_SMOKE_WORKDIR/operations-tail.jsonl"
after_read_only="$(wc -l <"$operations_log")"
if [[ "$before_read_only" != "$after_read_only" ]]; then
  echo "read-only status/viewer success unexpectedly appended an operational event" >&2
  exit 1
fi
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/operations-tail.jsonl" '"component":"cli"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/operations-tail.jsonl" '"result":"success"'

# Generated hooks and status formats call these six automatic paths. Exercise
# each against the isolated tmux server and prove its successful top-level
# outcome does not grow the operational journal.
automatic_pane="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p -t integration-smoke '#{pane_id}')"
automatic_window="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p -t integration-smoke '#{window_id}')"
automatic_socket="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p -t integration-smoke '#{socket_path}')"
automatic_server_pid="$(tmux -L "$PROJMUX_SMOKE_TMUX_SOCKET" display-message -p -t integration-smoke '#{pid}')"
automatic_tmux_env="$automatic_socket,$automatic_server_pid,0"

assert_automatic_success_no_record() {
  local label="$1"
  shift
  local before after
  before="$(wc -l <"$operations_log")"
  "$@" >"$PROJMUX_SMOKE_WORKDIR/automatic-$label.out"
  after="$(wc -l <"$operations_log")"
  if [[ "$before" != "$after" ]]; then
    echo "$label success unexpectedly appended an operational event" >&2
    exit 1
  fi
}

assert_automatic_success_no_record agent-hook-ingest \
  env TMUX="$automatic_tmux_env" "$bin" internal agent-hook ingest bell --pane "$automatic_pane"
assert_automatic_success_no_record attention-arm \
  env TMUX="$automatic_tmux_env" "$bin" attention arm "$automatic_pane"
assert_automatic_success_no_record attention-clear \
  env TMUX="$automatic_tmux_env" "$bin" attention clear "$automatic_pane"
assert_automatic_success_no_record attention-window \
  env TMUX="$automatic_tmux_env" "$bin" attention window "$automatic_window"
assert_automatic_success_no_record session-state-autosave \
  env TMUX="$automatic_tmux_env" "$bin" internal tmux autosave-session-state --quiet
assert_automatic_success_no_record recent-window-record \
  env TMUX="$automatic_tmux_env" "$bin" window record

set +e
"$bin" unknown-fixture-command >"$PROJMUX_SMOKE_WORKDIR/unknown.out" 2>"$PROJMUX_SMOKE_WORKDIR/unknown.err"
unknown_status=$?
set -e
if [[ "$unknown_status" != "1" ]]; then
  echo "expected unknown command exit 1, got: $unknown_status" >&2
  exit 1
fi
"$bin" diagnostics log --tail 1 --json --level error \
  >"$PROJMUX_SMOKE_WORKDIR/operations-error.jsonl"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/operations-error.jsonl" '"result":"error"'
if grep -Fq 'unknown-fixture-command' "$PROJMUX_SMOKE_WORKDIR/operations-error.jsonl"; then
  echo "operational error record leaked raw argv" >&2
  exit 1
fi

chmod 0755 "$XDG_STATE_HOME/projmux" "$XDG_STATE_HOME/projmux/logs"
chmod 0644 "$operations_log"
"$bin" pin project add "$smoke_root" >"$PROJMUX_SMOKE_WORKDIR/pin-add.out"
if [[ "$(stat -c '%a' "$XDG_STATE_HOME/projmux")" != "700" ]] ||
  [[ "$(stat -c '%a' "$XDG_STATE_HOME/projmux/logs")" != "700" ]] ||
  [[ "$(stat -c '%a' "$operations_log")" != "600" ]]; then
  echo "operational diagnostics did not repair private modes" >&2
  exit 1
fi

# The operational diagnostics writer is best effort: only its own log path is
# broken here. The Registry lives under the same state home and stays readable,
# because typing a pin is a Registry question and a pin mutation that could not
# read the Registry refuses instead of guessing the pin's kind.
blocked_state="$PROJMUX_SMOKE_WORKDIR/blocked-state"
mkdir -p "$blocked_state/projmux/metadata"
printf 'not-a-directory\n' >"$blocked_state/projmux/logs"
set +e
XDG_STATE_HOME="$blocked_state" "$bin" pin project add "$smoke_root" \
  >"$PROJMUX_SMOKE_WORKDIR/pin-add-blocked-log.out" \
  2>"$PROJMUX_SMOKE_WORKDIR/pin-add-blocked-log.err"
blocked_status=$?
set -e
if [[ "$blocked_status" != "0" ]]; then
  echo "best-effort operational writer failure changed exit code: $blocked_status" >&2
  exit 1
fi
if [[ -s "$PROJMUX_SMOKE_WORKDIR/pin-add-blocked-log.err" ]]; then
  echo "best-effort operational writer failure leaked to command stderr" >&2
  exit 1
fi
if [[ "$(cat "$PROJMUX_SMOKE_WORKDIR/pin-add-blocked-log.out")" != "pinned: candidate $smoke_root" ]]; then
  echo "best-effort operational writer failure changed command stdout" >&2
  exit 1
fi

# Public resource reconciliation uses one explicit server and a fresh Registry.
# The second real server has the same live shape and proves the first plan never
# escapes its exact -L/-S target.
reconcile_root="$PROJMUX_SMOKE_WORKDIR/reconcile-root"
reconcile_state="$PROJMUX_SMOKE_WORKDIR/reconcile-state"
mkdir -p "$reconcile_root"
PROJMUX_RECONCILE_SESSION="$(basename "$reconcile_root")"
PROJMUX_RECONCILE_PRIMARY_SOCKET="projmux-reconcile-primary-$$-$RANDOM"
PROJMUX_RECONCILE_SECONDARY_SOCKET="projmux-reconcile-secondary-$$-$RANDOM"
export PROJMUX_RECONCILE_SESSION PROJMUX_RECONCILE_PRIMARY_SOCKET PROJMUX_RECONCILE_SECONDARY_SOCKET
for reconcile_socket in "$PROJMUX_RECONCILE_PRIMARY_SOCKET" "$PROJMUX_RECONCILE_SECONDARY_SOCKET"; do
  env -u TMUX -u TMUX_PANE tmux -L "$reconcile_socket" new-session -d -s "$PROJMUX_RECONCILE_SESSION" -c "$reconcile_root" sleep 300
  env -u TMUX -u TMUX_PANE tmux -L "$reconcile_socket" set-option -t "$PROJMUX_RECONCILE_SESSION" -q @projmux_project_path "$reconcile_root"
  if [[ "$(env -u TMUX -u TMUX_PANE tmux -L "$reconcile_socket" show-options -t "$PROJMUX_RECONCILE_SESSION" -qv @projmux_project_path)" != "$reconcile_root" ]]; then
    echo "failed to seed reconcile session project path" >&2
    exit 1
  fi
done
PROJMUX_RECONCILE_PRIMARY_STARTED=1
PROJMUX_RECONCILE_SECONDARY_STARTED=1
PROJMUX_RECONCILE_PRIMARY_ACTUAL="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_RECONCILE_PRIMARY_SOCKET" display-message -p -t "=$PROJMUX_RECONCILE_SESSION" '#{socket_path}')"
PROJMUX_RECONCILE_SECONDARY_ACTUAL="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_RECONCILE_SECONDARY_SOCKET" display-message -p -t "=$PROJMUX_RECONCILE_SESSION" '#{socket_path}')"
export PROJMUX_RECONCILE_PRIMARY_ACTUAL PROJMUX_RECONCILE_SECONDARY_ACTUAL

resource_tmux_snapshot() {
  local socket="$1"
  env -u TMUX -u TMUX_PANE tmux -L "$socket" list-sessions -F '#{session_id}\037#{session_name}\037#{@projmux_project_uid}\037#{@projmux_project_name}\037#{@projmux_project_path}'
  env -u TMUX -u TMUX_PANE tmux -L "$socket" list-windows -a -F '#{window_id}\037#{window_name}\037#{@projmux_window_uid}\037#{@projmux_window_name}'
  env -u TMUX -u TMUX_PANE tmux -L "$socket" list-panes -a -F '#{pane_id}\037#{@projmux_pane_uid}\037#{@projmux_pane_label}'
}

# Registry authority converges parent before child: Project identity makes the
# Window authoritative on the next observation, and Window identity does the
# same for Pane. Keep every public report, require the first pass to change, and
# cap Project -> Window -> Pane -> no-op at four passes.
bounded_resource_reconcile_to_noop() {
  local report_prefix="$1"
  shift
  local pass report
  for pass in 1 2 3 4; do
    if [[ "$pass" == "1" ]]; then
      report="$report_prefix.json"
    else
      report="$report_prefix-pass-$pass.json"
    fi
    "$@" >"$report"
    if grep -Fq '"outcome": "changed"' "$report"; then
      continue
    fi
    if grep -Fq '"outcome": "no-op"' "$report"; then
      if [[ "$pass" == "1" ]]; then
        echo "bounded resource reconcile made no initial progress: $report" >&2
        cat "$report" >&2
        return 1
      fi
      smoke_assert_file_contains "$report" '"items": []'
      return 0
    fi
    echo "bounded resource reconcile reported neither changed nor no-op: $report" >&2
    cat "$report" >&2
    return 1
  done
  echo "resource reconciliation did not converge within four public passes: $report_prefix" >&2
  cat "$report" >&2
  return 1
}

resource_tmux_snapshot "$PROJMUX_RECONCILE_PRIMARY_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/reconcile-primary.before"
resource_tmux_snapshot "$PROJMUX_RECONCILE_SECONDARY_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/reconcile-secondary.before"
# An anchored live topology with no Registry authority is D2. Phase 8 closes all
# three automatic triggers at L0 for D2, so even a dry-run reports no recovery
# work and writes neither the Registry nor either exact tmux server.
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" reconcile resources --dry-run --socket "$PROJMUX_RECONCILE_PRIMARY_SOCKET" -o json \
  >"$PROJMUX_SMOKE_WORKDIR/reconcile-d2-dry-run.json"
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" reconcile resources --dry-run --socket "$PROJMUX_RECONCILE_PRIMARY_SOCKET" -o json \
  >"$PROJMUX_SMOKE_WORKDIR/reconcile-d2-dry-run-repeat.json"
cmp "$PROJMUX_SMOKE_WORKDIR/reconcile-d2-dry-run.json" "$PROJMUX_SMOKE_WORKDIR/reconcile-d2-dry-run-repeat.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/reconcile-d2-dry-run.json" '"outcome": "no-op"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/reconcile-d2-dry-run.json" '"items": []'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/reconcile-d2-dry-run.json" '"tmuxFlag": "-L"'
if [[ -e "$reconcile_state/projmux/metadata/registry.json" ]]; then
  echo "D2 resource reconcile dry-run created a Registry" >&2
  exit 1
fi
resource_tmux_snapshot "$PROJMUX_RECONCILE_PRIMARY_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/reconcile-primary.after-d2-dry-run"
resource_tmux_snapshot "$PROJMUX_RECONCILE_SECONDARY_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/reconcile-secondary.after-d2-dry-run"
cmp "$PROJMUX_SMOKE_WORKDIR/reconcile-primary.before" "$PROJMUX_SMOKE_WORKDIR/reconcile-primary.after-d2-dry-run"
cmp "$PROJMUX_SMOKE_WORKDIR/reconcile-secondary.before" "$PROJMUX_SMOKE_WORKDIR/reconcile-secondary.after-d2-dry-run"

# Seed managed identity through the explicit Registry-first route. The existing
# missing-mirror flow below now repairs only this authoritative Project.
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" create project --root "$reconcile_root" --name "$PROJMUX_RECONCILE_SESSION" \
  >"$PROJMUX_SMOKE_WORKDIR/reconcile-register.out"
registry_path="$reconcile_state/projmux/metadata/registry.json"
if [[ ! -s "$registry_path" ]]; then
  echo "explicit reconcile fixture registration did not create a Registry" >&2
  exit 1
fi
cp "$registry_path" "$PROJMUX_SMOKE_WORKDIR/reconcile-registry.after-register"

PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" reconcile resources --dry-run --socket "$PROJMUX_RECONCILE_PRIMARY_SOCKET" -o json \
  >"$PROJMUX_SMOKE_WORKDIR/reconcile-dry-run.json"
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" reconcile resources --dry-run --socket "$PROJMUX_RECONCILE_PRIMARY_SOCKET" -o json \
  >"$PROJMUX_SMOKE_WORKDIR/reconcile-dry-run-repeat.json"
cmp "$PROJMUX_SMOKE_WORKDIR/reconcile-dry-run.json" "$PROJMUX_SMOKE_WORKDIR/reconcile-dry-run-repeat.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/reconcile-dry-run.json" '"drift": "missing"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/reconcile-dry-run.json" '"tmuxFlag": "-L"'
cmp "$PROJMUX_SMOKE_WORKDIR/reconcile-registry.after-register" "$registry_path"
resource_tmux_snapshot "$PROJMUX_RECONCILE_PRIMARY_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/reconcile-primary.after-dry-run"
cmp "$PROJMUX_SMOKE_WORKDIR/reconcile-primary.before" "$PROJMUX_SMOKE_WORKDIR/reconcile-primary.after-dry-run"

bounded_resource_reconcile_to_noop "$PROJMUX_SMOKE_WORKDIR/reconcile-execute" \
  env PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" reconcile resources --socket "$PROJMUX_RECONCILE_PRIMARY_SOCKET" -o json
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/reconcile-execute.json" '"outcome": "changed"'
if [[ ! -s "$registry_path" ]]; then
  echo "resource reconcile execute did not commit the Registry" >&2
  exit 1
fi
primary_project_uid="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_RECONCILE_PRIMARY_SOCKET" show-options -t "$PROJMUX_RECONCILE_SESSION" -qv @projmux_project_uid)"
primary_window_uid="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_RECONCILE_PRIMARY_SOCKET" show-options -w -t "=$PROJMUX_RECONCILE_SESSION:0" -qv @projmux_window_uid)"
primary_pane_uid="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_RECONCILE_PRIMARY_SOCKET" show-options -p -t "=$PROJMUX_RECONCILE_SESSION:0.0" -qv @projmux_pane_uid)"
if [[ -z "$primary_project_uid" || -z "$primary_window_uid" || -z "$primary_pane_uid" ]]; then
  echo "resource reconcile did not mirror Project/Window/Pane identity: project=$primary_project_uid window=$primary_window_uid pane=$primary_pane_uid" >&2
  cat "$PROJMUX_SMOKE_WORKDIR/reconcile-execute.json" >&2
  resource_tmux_snapshot "$PROJMUX_RECONCILE_PRIMARY_SOCKET" >&2
  exit 1
fi

# Canonical rename/rebind consumes the exact UID-bound live server inherited
# through its absolute socket path. Each verb writes only its transport field;
# raw session/window/pane names and the second socket remain unchanged.
mkdir -p "$PROJMUX_SMOKE_WORKDIR/reconcile-root-moved"
primary_pid="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_RECONCILE_PRIMARY_SOCKET" display-message -p -t "=$PROJMUX_RECONCILE_SESSION" '#{pid}')"
primary_tmux_env="$PROJMUX_RECONCILE_PRIMARY_ACTUAL,$primary_pid,0"
primary_session_name_before="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_RECONCILE_PRIMARY_SOCKET" display-message -p -t "=$PROJMUX_RECONCILE_SESSION" '#{session_name}')"
primary_window_name_before="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_RECONCILE_PRIMARY_SOCKET" display-message -p -t "=$PROJMUX_RECONCILE_SESSION:0" '#{window_name}')"
primary_pane_title_before="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_RECONCILE_PRIMARY_SOCKET" display-message -p -t "=$PROJMUX_RECONCILE_SESSION:0.0" '#{pane_title}')"
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" TMUX="$primary_tmux_env" \
  "$bin" rename project "uid:$primary_project_uid" --name stable-project >"$PROJMUX_SMOKE_WORKDIR/reconcile-rename-project.out"
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" TMUX="$primary_tmux_env" \
  "$bin" rename window "uid:$primary_window_uid" --name stable-window >"$PROJMUX_SMOKE_WORKDIR/reconcile-rename-window.out"
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" TMUX="$primary_tmux_env" \
  "$bin" rename pane "uid:$primary_pane_uid" --name stable-pane >"$PROJMUX_SMOKE_WORKDIR/reconcile-rename-pane.out"
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" TMUX="$primary_tmux_env" \
  "$bin" rebind project "uid:$primary_project_uid" --root "$PROJMUX_SMOKE_WORKDIR/reconcile-root-moved" >"$PROJMUX_SMOKE_WORKDIR/reconcile-rebind-project.out"
if [[ "$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_RECONCILE_PRIMARY_SOCKET" show-options -qv -t "$PROJMUX_RECONCILE_SESSION" @projmux_project_name)" != stable-project ]] || \
  [[ "$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_RECONCILE_PRIMARY_SOCKET" show-options -wqv -t "=$PROJMUX_RECONCILE_SESSION:0" @projmux_window_name)" != stable-window ]] || \
  [[ "$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_RECONCILE_PRIMARY_SOCKET" show-options -pqv -t "=$PROJMUX_RECONCILE_SESSION:0.0" @projmux_pane_label)" != stable-pane ]] || \
  [[ "$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_RECONCILE_PRIMARY_SOCKET" show-options -qv -t "$PROJMUX_RECONCILE_SESSION" @projmux_project_path)" != "$PROJMUX_SMOKE_WORKDIR/reconcile-root-moved" ]]; then
  echo "rename/rebind integration mirrors did not converge" >&2
  exit 1
fi
if [[ "$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_RECONCILE_PRIMARY_SOCKET" display-message -p -t "=$PROJMUX_RECONCILE_SESSION" '#{session_name}')" != "$primary_session_name_before" ]] || \
  [[ "$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_RECONCILE_PRIMARY_SOCKET" display-message -p -t "=$PROJMUX_RECONCILE_SESSION:0" '#{window_name}')" != "$primary_window_name_before" ]] || \
  [[ "$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_RECONCILE_PRIMARY_SOCKET" display-message -p -t "=$PROJMUX_RECONCILE_SESSION:0.0" '#{pane_title}')" != "$primary_pane_title_before" ]]; then
  echo "rename/rebind integration changed a raw runtime name" >&2
  exit 1
fi
resource_tmux_snapshot "$PROJMUX_RECONCILE_SECONDARY_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/reconcile-secondary.after"
cmp "$PROJMUX_SMOKE_WORKDIR/reconcile-secondary.before" "$PROJMUX_SMOKE_WORKDIR/reconcile-secondary.after"

cp "$registry_path" "$PROJMUX_SMOKE_WORKDIR/reconcile-registry.before-repeat"
resource_tmux_snapshot "$PROJMUX_RECONCILE_PRIMARY_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/reconcile-primary.before-repeat"
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" reconcile resources --socket "$PROJMUX_RECONCILE_PRIMARY_SOCKET" -o json \
  >"$PROJMUX_SMOKE_WORKDIR/reconcile-repeat.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/reconcile-repeat.json" '"outcome": "no-op"'
cmp "$PROJMUX_SMOKE_WORKDIR/reconcile-registry.before-repeat" "$registry_path"
resource_tmux_snapshot "$PROJMUX_RECONCILE_PRIMARY_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/reconcile-primary.after-repeat"
cmp "$PROJMUX_SMOKE_WORKDIR/reconcile-primary.before-repeat" "$PROJMUX_SMOKE_WORKDIR/reconcile-primary.after-repeat"

PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  TMUX="$PROJMUX_RECONCILE_PRIMARY_ACTUAL,$primary_pid,0" \
  "$bin" reconcile resources --dry-run -o json >"$PROJMUX_SMOKE_WORKDIR/reconcile-inherited.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/reconcile-inherited.json" '"tmuxFlag": "-S"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/reconcile-inherited.json" "$PROJMUX_RECONCILE_PRIMARY_ACTUAL"
cmp "$PROJMUX_SMOKE_WORKDIR/reconcile-registry.before-repeat" "$registry_path"

outside_state="$PROJMUX_SMOKE_WORKDIR/reconcile-outside-state"
set +e
env -u TMUX -u TMUX_PANE PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$outside_state" \
  "$bin" reconcile resources >"$PROJMUX_SMOKE_WORKDIR/reconcile-outside.out" 2>"$PROJMUX_SMOKE_WORKDIR/reconcile-outside.err"
outside_status=$?
set -e
if [[ "$outside_status" == "0" ]] || [[ -e "$outside_state/projmux/metadata/registry.json" ]]; then
  echo "resource reconcile without an explicit outside-tmux socket did not fail before Registry mutation" >&2
  exit 1
fi
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/reconcile-outside.err" 'requires --socket <name> or --socket-path <absolute> outside tmux'

PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" "$bin" doctor --json >"$PROJMUX_SMOKE_WORKDIR/reconcile-doctor.json"
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" "$bin" get projects -o json >"$PROJMUX_SMOKE_WORKDIR/reconcile-get.json"
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" "$bin" describe project "uid:$primary_project_uid" -o json >"$PROJMUX_SMOKE_WORKDIR/reconcile-describe.json"
cmp "$PROJMUX_SMOKE_WORKDIR/reconcile-registry.before-repeat" "$registry_path"
resource_tmux_snapshot "$PROJMUX_RECONCILE_PRIMARY_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/reconcile-primary.after-reads"
cmp "$PROJMUX_SMOKE_WORKDIR/reconcile-primary.before-repeat" "$PROJMUX_SMOKE_WORKDIR/reconcile-primary.after-reads"

# Runtime diagnostics escape hatch. The Registry-first surfaces are, correctly,
# not an inventory: an operator's own shell, the Home control session, and a
# scratch session are not managed resources and do not appear where managed rows
# are listed. `get runtime` is the surface that shows the machine as it is, and
# what has to hold on a real server is that it names everything, explains every
# classification, reads exactly one socket, and writes nothing anywhere.
runtime_root="$PROJMUX_SMOKE_WORKDIR/runtime-root"
runtime_state="$PROJMUX_SMOKE_WORKDIR/runtime-state"
mkdir -p "$runtime_root"
PROJMUX_RUNTIME_SESSION="$(basename "$runtime_root")"
PROJMUX_RUNTIME_APP_SOCKET="projmux-runtime-app-$$-$RANDOM"
PROJMUX_RUNTIME_GUEST_SOCKET="projmux-runtime-guest-$$-$RANDOM"
PROJMUX_RUNTIME_SIBLING_SOCKET="projmux-runtime-sibling-$$-$RANDOM"
export PROJMUX_RUNTIME_SESSION PROJMUX_RUNTIME_APP_SOCKET PROJMUX_RUNTIME_GUEST_SOCKET PROJMUX_RUNTIME_SIBLING_SOCKET

runtime_tmux() {
  local socket="$1"
  shift
  env -u TMUX -u TMUX_PANE tmux -L "$socket" "$@"
}

runtime_snapshot() {
  local socket="$1"
  runtime_tmux "$socket" list-sessions -F '#{session_id}\037#{session_name}\037#{@projmux_project_uid}\037#{@projmux_session_role}\037#{@projmux_ephemeral}'
  runtime_tmux "$socket" list-windows -a -F '#{window_id}\037#{session_id}\037#{window_name}\037#{@projmux_window_uid}'
  runtime_tmux "$socket" list-panes -a -F '#{pane_id}\037#{window_id}\037#{@projmux_pane_uid}'
}

for runtime_socket in "$PROJMUX_RUNTIME_APP_SOCKET" "$PROJMUX_RUNTIME_GUEST_SOCKET" "$PROJMUX_RUNTIME_SIBLING_SOCKET"; do
  runtime_tmux "$runtime_socket" new-session -d -s "$PROJMUX_RUNTIME_SESSION" -c "$runtime_root" sleep 300
  runtime_tmux "$runtime_socket" set-option -t "$PROJMUX_RUNTIME_SESSION" -q @projmux_project_path "$runtime_root"
done
PROJMUX_RUNTIME_APP_STARTED=1
PROJMUX_RUNTIME_GUEST_STARTED=1
PROJMUX_RUNTIME_SIBLING_STARTED=1
PROJMUX_RUNTIME_APP_ACTUAL="$(runtime_tmux "$PROJMUX_RUNTIME_APP_SOCKET" display-message -p -t "=$PROJMUX_RUNTIME_SESSION" '#{socket_path}')"
PROJMUX_RUNTIME_GUEST_ACTUAL="$(runtime_tmux "$PROJMUX_RUNTIME_GUEST_SOCKET" display-message -p -t "=$PROJMUX_RUNTIME_SESSION" '#{socket_path}')"
PROJMUX_RUNTIME_SIBLING_ACTUAL="$(runtime_tmux "$PROJMUX_RUNTIME_SIBLING_SOCKET" display-message -p -t "=$PROJMUX_RUNTIME_SESSION" '#{socket_path}')"
export PROJMUX_RUNTIME_APP_ACTUAL PROJMUX_RUNTIME_GUEST_ACTUAL PROJMUX_RUNTIME_SIBLING_ACTUAL
# The socket every later cleanup targets is the one tmux itself reports, and it
# has to be under this run's TMUX_TMPDIR before anything kills it.
for runtime_actual in "$PROJMUX_RUNTIME_APP_ACTUAL" "$PROJMUX_RUNTIME_GUEST_ACTUAL" "$PROJMUX_RUNTIME_SIBLING_ACTUAL"; do
  case "$runtime_actual" in
    "$PROJMUX_SMOKE_WORKDIR"/tmux/*) ;;
    *)
      echo "runtime diagnostics socket escaped smoke root: $runtime_actual" >&2
      exit 1
      ;;
  esac
done

# Only the app socket carries the ownership marker. The guest socket is the
# operator's own tmux that projmux is a guest on, and the third is a sibling
# that must never be read.
runtime_tmux "$PROJMUX_RUNTIME_APP_SOCKET" set-option -g -q @projmux_app 1

# Register the Project explicitly; the anchored live D2 topology is not an
# automatic import source under the Phase 8 authority table.
PROJMUX_PROJDIR="$runtime_root" XDG_STATE_HOME="$runtime_state" \
  "$bin" create project --root "$runtime_root" --name "$PROJMUX_RUNTIME_SESSION" \
  >"$PROJMUX_SMOKE_WORKDIR/runtime-seed-register.out"
bounded_resource_reconcile_to_noop "$PROJMUX_SMOKE_WORKDIR/runtime-seed-reconcile" \
  env PROJMUX_PROJDIR="$runtime_root" XDG_STATE_HOME="$runtime_state" \
  "$bin" reconcile resources --socket "$PROJMUX_RUNTIME_APP_SOCKET" -o json
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-seed-reconcile.json" '"outcome": "changed"'
runtime_registry="$runtime_state/projmux/metadata/registry.json"
runtime_project_uid="$(runtime_tmux "$PROJMUX_RUNTIME_APP_SOCKET" show-options -t "$PROJMUX_RUNTIME_SESSION" -qv @projmux_project_uid)"
runtime_window_uid="$(runtime_tmux "$PROJMUX_RUNTIME_APP_SOCKET" show-options -wqv -t "=$PROJMUX_RUNTIME_SESSION:0" @projmux_window_uid)"
runtime_pane_uid="$(runtime_tmux "$PROJMUX_RUNTIME_APP_SOCKET" show-options -pqv -t "=$PROJMUX_RUNTIME_SESSION:0.0" @projmux_pane_uid)"
if [[ -z "$runtime_project_uid" || -z "$runtime_window_uid" || -z "$runtime_pane_uid" ]]; then
  echo "runtime diagnostics seed did not mirror identity: project=$runtime_project_uid window=$runtime_window_uid pane=$runtime_pane_uid" >&2
  cat "$PROJMUX_SMOKE_WORKDIR/runtime-seed-reconcile.json" >&2
  runtime_snapshot "$PROJMUX_RUNTIME_APP_SOCKET" >&2 || true
  exit 1
fi

# Everything the managed UI does not show: the Home control session, a scratch
# session, an unmarked window inside the managed session, and a window mirroring
# a uid no Registry contains.
runtime_tmux "$PROJMUX_RUNTIME_APP_SOCKET" new-session -d -s runtime-home -c "$runtime_root" sleep 300
runtime_tmux "$PROJMUX_RUNTIME_APP_SOCKET" set-option -t runtime-home -q @projmux_session_role control
runtime_tmux "$PROJMUX_RUNTIME_APP_SOCKET" new-session -d -s runtime-scratch -c "$runtime_root" sleep 300
runtime_tmux "$PROJMUX_RUNTIME_APP_SOCKET" set-option -t runtime-scratch -q @projmux_ephemeral 1
runtime_tmux "$PROJMUX_RUNTIME_APP_SOCKET" new-window -d -t "=$PROJMUX_RUNTIME_SESSION" -n plain -c "$runtime_root" sleep 300
runtime_tmux "$PROJMUX_RUNTIME_APP_SOCKET" new-window -d -t "=$PROJMUX_RUNTIME_SESSION" -n ghost -c "$runtime_root" sleep 300
runtime_ghost_window="$(runtime_tmux "$PROJMUX_RUNTIME_APP_SOCKET" list-windows -t "=$PROJMUX_RUNTIME_SESSION" -F '#{window_id}\037#{window_name}' | awk -F'\037' '$2 == "ghost" {print $1}')"
runtime_tmux "$PROJMUX_RUNTIME_APP_SOCKET" set-option -w -t "$runtime_ghost_window" -q @projmux_window_uid win-not-in-this-registry

runtime_snapshot "$PROJMUX_RUNTIME_APP_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/runtime-app.before"
runtime_snapshot "$PROJMUX_RUNTIME_SIBLING_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/runtime-sibling.before"
cp "$runtime_registry" "$PROJMUX_SMOKE_WORKDIR/runtime-registry.before"

for runtime_kind in sessions windows panes; do
  PROJMUX_PROJDIR="$runtime_root" XDG_STATE_HOME="$runtime_state" \
    "$bin" get runtime "$runtime_kind" --socket "$PROJMUX_RUNTIME_APP_SOCKET" -o json \
    >"$PROJMUX_SMOKE_WORKDIR/runtime-app-$runtime_kind.json"
  PROJMUX_PROJDIR="$runtime_root" XDG_STATE_HOME="$runtime_state" \
    "$bin" get runtime "$runtime_kind" --socket "$PROJMUX_RUNTIME_APP_SOCKET" -o json \
    >"$PROJMUX_SMOKE_WORKDIR/runtime-app-$runtime_kind-repeat.json"
  cmp "$PROJMUX_SMOKE_WORKDIR/runtime-app-$runtime_kind.json" "$PROJMUX_SMOKE_WORKDIR/runtime-app-$runtime_kind-repeat.json"
  smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-$runtime_kind.json" '"hostMode": "app-owned"'
  smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-$runtime_kind.json" '"kind": "socket-name"'
  smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-$runtime_kind.json" "\"value\": \"$PROJMUX_RUNTIME_APP_SOCKET\""
  smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-$runtime_kind.json" '"class": "managed"'
  smoke_assert_file_lacks "$PROJMUX_SMOKE_WORKDIR/runtime-app-$runtime_kind.json" '"unavailable"'
done
# Every class the managed UI cannot show is present with its exact handle and a
# stated reason, and the managed no-op row is there beside them.
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-sessions.json" '"class": "control"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-sessions.json" '"class": "ephemeral"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-sessions.json" "\"uid\": \"$runtime_project_uid\""
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-windows.json" '"class": "unattributed"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-windows.json" '"class": "recoverable"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-windows.json" '"uid": "win-not-in-this-registry"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-panes.json" '"class": "unattributed"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-panes.json" "\"uid\": \"$runtime_pane_uid\""
# Nothing an operator can go look at may be missing from a report that claims to
# name the whole server.
for runtime_live_session in $(runtime_tmux "$PROJMUX_RUNTIME_APP_SOCKET" list-sessions -F '#{session_id}'); do
  smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-sessions.json" "\"id\": \"$runtime_live_session\""
done
for runtime_live_window in $(runtime_tmux "$PROJMUX_RUNTIME_APP_SOCKET" list-windows -a -F '#{window_id}'); do
  smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-windows.json" "\"id\": \"$runtime_live_window\""
done
for runtime_live_pane in $(runtime_tmux "$PROJMUX_RUNTIME_APP_SOCKET" list-panes -a -F '#{pane_id}'); do
  smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-panes.json" "\"id\": \"$runtime_live_pane\""
done

# The human projection states which server answered before it states anything
# about that server.
PROJMUX_PROJDIR="$runtime_root" XDG_STATE_HOME="$runtime_state" \
  "$bin" get runtime sessions --socket "$PROJMUX_RUNTIME_APP_SOCKET" \
  >"$PROJMUX_SMOKE_WORKDIR/runtime-app-human.txt"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-human.txt" "host app-owned  transport tmux -L $PROJMUX_RUNTIME_APP_SOCKET"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-human.txt" "SESSION"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-human.txt" "control"

# The standalone host is the same Registry and the same managed identity; only
# the classification of what projmux did not mark differs.
runtime_tmux "$PROJMUX_RUNTIME_GUEST_SOCKET" set-option -t "$PROJMUX_RUNTIME_SESSION" -q @projmux_project_uid "$runtime_project_uid"
runtime_tmux "$PROJMUX_RUNTIME_GUEST_SOCKET" set-option -w -t "=$PROJMUX_RUNTIME_SESSION:0" -q @projmux_window_uid "$runtime_window_uid"
runtime_tmux "$PROJMUX_RUNTIME_GUEST_SOCKET" set-option -p -t "=$PROJMUX_RUNTIME_SESSION:0.0" -q @projmux_pane_uid "$runtime_pane_uid"
runtime_tmux "$PROJMUX_RUNTIME_GUEST_SOCKET" new-session -d -s runtime-operator -c "$runtime_root" sleep 300
runtime_tmux "$PROJMUX_RUNTIME_GUEST_SOCKET" set-option -t runtime-operator -q @projmux_session_role control
if [[ "$(runtime_tmux "$PROJMUX_RUNTIME_GUEST_SOCKET" show-options -t "$PROJMUX_RUNTIME_SESSION" -qv @projmux_project_uid)" != "$runtime_project_uid" ]]; then
  echo "runtime diagnostics guest fixture did not mirror the Project uid" >&2
  exit 1
fi
runtime_snapshot "$PROJMUX_RUNTIME_GUEST_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/runtime-guest.before"
PROJMUX_PROJDIR="$runtime_root" XDG_STATE_HOME="$runtime_state" \
  "$bin" get runtime sessions --socket "$PROJMUX_RUNTIME_GUEST_SOCKET" -o json \
  >"$PROJMUX_SMOKE_WORKDIR/runtime-guest-sessions.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-guest-sessions.json" '"hostMode": "standalone"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-guest-sessions.json" '"class": "managed"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-guest-sessions.json" "\"uid\": \"$runtime_project_uid\""
# A control marker on a server projmux does not own proves nothing: that session
# is the operator's, and it is reported as foreign rather than as a projmux role.
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-guest-sessions.json" '"class": "foreign"'
smoke_assert_file_lacks "$PROJMUX_SMOKE_WORKDIR/runtime-guest-sessions.json" '"class": "control"'
# The two hosts stay two servers. The guest report names the operator's own
# session; the app report, taken against the same Registry, never does.
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-guest-sessions.json" '"name": "runtime-operator"'
smoke_assert_file_lacks "$PROJMUX_SMOKE_WORKDIR/runtime-app-sessions.json" '"name": "runtime-operator"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-app-sessions.json" '"name": "runtime-home"'
smoke_assert_file_lacks "$PROJMUX_SMOKE_WORKDIR/runtime-guest-sessions.json" '"name": "runtime-home"' 

# Outside tmux with no socket flag the read succeeds and says why it is empty,
# and it probes no default server.
runtime_outside_state="$PROJMUX_SMOKE_WORKDIR/runtime-outside-state"
env -u TMUX -u TMUX_PANE PROJMUX_PROJDIR="$runtime_root" XDG_STATE_HOME="$runtime_outside_state" \
  "$bin" get runtime panes -o json >"$PROJMUX_SMOKE_WORKDIR/runtime-outside.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-outside.json" '"kind": "none"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-outside.json" '"hostMode": "unknown"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-outside.json" '"items": []'
for runtime_scope in host-mode sessions windows panes; do
  smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-outside.json" "\"scope\": \"$runtime_scope\""
done
if [[ -e "$runtime_outside_state/projmux/metadata/registry.json" ]]; then
  echo "a no-transport runtime read created a Registry" >&2
  exit 1
fi

# The whole surface is read-only: no Registry byte, no tmux object, and no
# sibling socket changed across every read above.
cmp "$PROJMUX_SMOKE_WORKDIR/runtime-registry.before" "$runtime_registry"
runtime_snapshot "$PROJMUX_RUNTIME_APP_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/runtime-app.after"
cmp "$PROJMUX_SMOKE_WORKDIR/runtime-app.before" "$PROJMUX_SMOKE_WORKDIR/runtime-app.after"
runtime_snapshot "$PROJMUX_RUNTIME_GUEST_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/runtime-guest.after"
cmp "$PROJMUX_SMOKE_WORKDIR/runtime-guest.before" "$PROJMUX_SMOKE_WORKDIR/runtime-guest.after"
runtime_snapshot "$PROJMUX_RUNTIME_SIBLING_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/runtime-sibling.after"
cmp "$PROJMUX_SMOKE_WORKDIR/runtime-sibling.before" "$PROJMUX_SMOKE_WORKDIR/runtime-sibling.after"

# The inherited $TMUX socket path reaches the same exact server as the flag.
runtime_app_pid="$(runtime_tmux "$PROJMUX_RUNTIME_APP_SOCKET" display-message -p -t "=$PROJMUX_RUNTIME_SESSION" '#{pid}')"
PROJMUX_PROJDIR="$runtime_root" XDG_STATE_HOME="$runtime_state" \
  TMUX="$PROJMUX_RUNTIME_APP_ACTUAL,$runtime_app_pid,0" \
  "$bin" get runtime sessions -o json >"$PROJMUX_SMOKE_WORKDIR/runtime-inherited.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-inherited.json" '"source": "inherited-tmux-env"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-inherited.json" "\"value\": \"$PROJMUX_RUNTIME_APP_ACTUAL\""
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-inherited.json" "\"uid\": \"$runtime_project_uid\""

# The refusals are operator input errors, and they cost no observation.
set +e
PROJMUX_PROJDIR="$runtime_root" XDG_STATE_HOME="$runtime_state" \
  "$bin" get runtime agents --socket "$PROJMUX_RUNTIME_APP_SOCKET" \
  >"$PROJMUX_SMOKE_WORKDIR/runtime-bad-kind.out" 2>"$PROJMUX_SMOKE_WORKDIR/runtime-bad-kind.err"
runtime_bad_kind_status=$?
set -e
if [[ "$runtime_bad_kind_status" != "2" ]] || [[ -s "$PROJMUX_SMOKE_WORKDIR/runtime-bad-kind.out" ]]; then
  echo "get runtime agents did not refuse as usage: status=$runtime_bad_kind_status" >&2
  exit 1
fi
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/runtime-bad-kind.err" "this release implements: sessions, windows, panes"

echo ">> runtime diagnostics root=$PROJMUX_SMOKE_WORKDIR app_socket=$PROJMUX_RUNTIME_APP_ACTUAL guest_socket=$PROJMUX_RUNTIME_GUEST_ACTUAL sibling_unchanged=yes writes=0"

# Registry-first primary navigation. The Projects surface used to enumerate the
# machine: a filesystem scan produced the rows and `list-sessions` decided which
# of them existed, so a Project whose session was closed vanished from the list
# whose purpose is reopening it. The rows come from the Registry now, and the
# runtime is an overlay.
#
# `switch preview` is the non-interactive projection of that view model, so this
# block can assert the row set, the order, the status overlay, the host header,
# and the zero-write property without driving a picker. It reuses the servers and
# the Registry the runtime block above already built: the same app-owned socket,
# the same guest socket, the same untouched sibling.
nav_expect_rows() {
  # The fixture Project owns exactly three managed rows -- itself, its Window,
  # and that Window's shell Pane -- and every one of them has to carry the status
  # the argument names. Counting them is what proves the projection enumerates the
  # Registry rather than whatever tmux happened to answer with.
  local file="$1"
  local status="$2"
  local rows
  smoke_assert_file_contains "$file" "project "
  smoke_assert_file_contains "$file" "window "
  smoke_assert_file_contains "$file" "pane "
  rows="$(tail -n +2 "$file" | awk -v want="$status" 'NF > 2 && $(NF - 1) == want' | wc -l | tr -d ' ')"
  if [[ "$rows" != "3" ]]; then
    echo "expected 3 registry rows with status $status in $file, got $rows" >&2
    cat "$file" >&2
    exit 1
  fi
}

PROJMUX_PROJDIR="$runtime_root" XDG_STATE_HOME="$runtime_state" \
  TMUX="$PROJMUX_RUNTIME_APP_ACTUAL,$runtime_app_pid,0" \
  "$bin" switch preview "uid:$runtime_project_uid" >"$PROJMUX_SMOKE_WORKDIR/nav-app.txt"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/nav-app.txt" "host app-owned  transport tmux -S $PROJMUX_RUNTIME_APP_ACTUAL"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/nav-app.txt" "source inherited-tmux-env"
nav_expect_rows "$PROJMUX_SMOKE_WORKDIR/nav-app.txt" "live"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/nav-app.txt" "open,delete"
# The runtime-only objects on this very server are not rows. That is the whole
# separation: they are reachable, and they are reachable somewhere else.
for nav_absent in runtime-home runtime-scratch ghost plain; do
  smoke_assert_file_lacks "$PROJMUX_SMOKE_WORKDIR/nav-app.txt" "$nav_absent"
done

# Acceptance: the same Registry renders the same rows in the same order with no
# tmux server at all, with the status downgraded rather than the row dropped, and
# with the action that would bring it back.
env -u TMUX -u TMUX_PANE PROJMUX_PROJDIR="$runtime_root" XDG_STATE_HOME="$runtime_state" \
  "$bin" switch preview "uid:$runtime_project_uid" >"$PROJMUX_SMOKE_WORKDIR/nav-dark.txt"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/nav-dark.txt" "host unknown  transport no tmux transport"
nav_expect_rows "$PROJMUX_SMOKE_WORKDIR/nav-dark.txt" "unknown"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/nav-dark.txt" "start,delete"
smoke_assert_file_lacks "$PROJMUX_SMOKE_WORKDIR/nav-dark.txt" "open,delete"
# Identity and order are the same list; only the overlay differs. nav_shape
# drops the header line and the two trailing columns -- status and actions --
# leaving the kind and the name of every row in view order. Comparing that is
# what "a refresh cannot re-identify or reorder a row" means.
nav_shape() {
  tail -n +2 "$1" | awk 'NF > 2 { NF -= 2; print }'
}
nav_shape "$PROJMUX_SMOKE_WORKDIR/nav-app.txt" >"$PROJMUX_SMOKE_WORKDIR/nav-app.shape"
nav_shape "$PROJMUX_SMOKE_WORKDIR/nav-dark.txt" >"$PROJMUX_SMOKE_WORKDIR/nav-dark.shape"
if [[ ! -s "$PROJMUX_SMOKE_WORKDIR/nav-app.shape" ]]; then
  echo "registry-first navigation projected no rows for the fixture Project" >&2
  cat "$PROJMUX_SMOKE_WORKDIR/nav-app.txt" >&2
  exit 1
fi
if ! diff -u "$PROJMUX_SMOKE_WORKDIR/nav-app.shape" "$PROJMUX_SMOKE_WORKDIR/nav-dark.shape" \
  >"$PROJMUX_SMOKE_WORKDIR/nav.rowdiff"; then
  echo "registry-first rows changed identity or order when tmux went away" >&2
  cat "$PROJMUX_SMOKE_WORKDIR/nav.rowdiff" >&2
  exit 1
fi

# The standalone host is the same Registry, the same rows, and the same
# eligibility; only the host header differs.
runtime_guest_pid="$(runtime_tmux "$PROJMUX_RUNTIME_GUEST_SOCKET" display-message -p -t "=$PROJMUX_RUNTIME_SESSION" '#{pid}')"
PROJMUX_PROJDIR="$runtime_root" XDG_STATE_HOME="$runtime_state" \
  TMUX="$PROJMUX_RUNTIME_GUEST_ACTUAL,$runtime_guest_pid,0" \
  "$bin" switch preview "uid:$runtime_project_uid" >"$PROJMUX_SMOKE_WORKDIR/nav-guest.txt"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/nav-guest.txt" "host standalone  transport tmux -S $PROJMUX_RUNTIME_GUEST_ACTUAL"
nav_expect_rows "$PROJMUX_SMOKE_WORKDIR/nav-guest.txt" "live"
smoke_assert_file_lacks "$PROJMUX_SMOKE_WORKDIR/nav-guest.txt" "runtime-operator"
nav_shape "$PROJMUX_SMOKE_WORKDIR/nav-guest.txt" >"$PROJMUX_SMOKE_WORKDIR/nav-guest.shape"
if ! diff -u "$PROJMUX_SMOKE_WORKDIR/nav-app.shape" "$PROJMUX_SMOKE_WORKDIR/nav-guest.shape" \
  >"$PROJMUX_SMOKE_WORKDIR/nav.hostdiff"; then
  echo "registry-first rows differ between the app-owned and standalone hosts" >&2
  cat "$PROJMUX_SMOKE_WORKDIR/nav.hostdiff" >&2
  exit 1
fi

# The Runtime link states what it leads to, per exact host.
PROJMUX_PROJDIR="$runtime_root" XDG_STATE_HOME="$runtime_state" \
  TMUX="$PROJMUX_RUNTIME_APP_ACTUAL,$runtime_app_pid,0" \
  "$bin" switch preview __projmux_runtime__ >"$PROJMUX_SMOKE_WORKDIR/nav-link-app.txt"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/nav-link-app.txt" "control 1"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/nav-link-app.txt" "ephemeral 1"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/nav-link-app.txt" "unattributed"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/nav-link-app.txt" "recoverable 1"
env -u TMUX -u TMUX_PANE PROJMUX_PROJDIR="$runtime_root" XDG_STATE_HOME="$runtime_state" \
  "$bin" switch preview __projmux_runtime__ >"$PROJMUX_SMOKE_WORKDIR/nav-link-dark.txt"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/nav-link-dark.txt" "Runtime - no tmux transport"

# Outside tmux with no socket flag the navigation read probes no default server
# and creates no state.
nav_outside_state="$PROJMUX_SMOKE_WORKDIR/nav-outside-state"
env -u TMUX -u TMUX_PANE PROJMUX_PROJDIR="$runtime_root" XDG_STATE_HOME="$nav_outside_state" \
  "$bin" switch preview __projmux_runtime__ >"$PROJMUX_SMOKE_WORKDIR/nav-outside.txt"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/nav-outside.txt" "transport no tmux transport"
if [[ -e "$nav_outside_state/projmux/metadata/registry.json" ]]; then
  echo "a no-transport navigation read created a Registry" >&2
  exit 1
fi

# Zero writes across every navigation read above: no Registry byte, no tmux
# object on either host, and no sibling socket.
cmp "$PROJMUX_SMOKE_WORKDIR/runtime-registry.before" "$runtime_registry"
runtime_snapshot "$PROJMUX_RUNTIME_APP_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/nav-app.after"
cmp "$PROJMUX_SMOKE_WORKDIR/runtime-app.before" "$PROJMUX_SMOKE_WORKDIR/nav-app.after"
runtime_snapshot "$PROJMUX_RUNTIME_GUEST_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/nav-guest.after"
cmp "$PROJMUX_SMOKE_WORKDIR/runtime-guest.before" "$PROJMUX_SMOKE_WORKDIR/nav-guest.after"
runtime_snapshot "$PROJMUX_RUNTIME_SIBLING_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/nav-sibling.after"
cmp "$PROJMUX_SMOKE_WORKDIR/runtime-sibling.before" "$PROJMUX_SMOKE_WORKDIR/nav-sibling.after"

echo ">> registry-first navigation root=$PROJMUX_SMOKE_WORKDIR app_host=app-owned guest_host=standalone rows=identical sibling_unchanged=yes writes=0"

# Durable Registry envelope. registry.json is the source of truth for managed
# identity and desired topology, so the installed shape has to tell a legitimate
# first use apart from a lost Registry, keep the bytes every semantic write
# replaces, and spend nothing on a convergent no-op. Every state root below lives
# under the smoke workdir, so the operator's own state is never read or written.
envelope_fresh_state="$PROJMUX_SMOKE_WORKDIR/envelope-fresh-state"
mkdir -p "$envelope_fresh_state"
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$envelope_fresh_state" \
  "$bin" get projects -o json >"$PROJMUX_SMOKE_WORKDIR/envelope-fresh-get.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/envelope-fresh-get.json" '"items": []'
if [[ -e "$envelope_fresh_state/projmux/metadata" ]]; then
  echo "a first-use Registry read materialized $envelope_fresh_state/projmux/metadata" >&2
  find "$envelope_fresh_state" >&2
  exit 1
fi

envelope_metadata="$reconcile_state/projmux/metadata"
envelope_marker="$envelope_metadata/registry.initialized"
envelope_recovery="$envelope_metadata/recovery"
if [[ ! -s "$envelope_marker" ]]; then
  echo "the committed Registry has no initialized marker at $envelope_marker" >&2
  find "$envelope_metadata" >&2
  exit 1
fi
if [[ "$(stat -c '%a' "$envelope_metadata")" != "700" ]] ||
  [[ "$(stat -c '%a' "$registry_path")" != "600" ]] ||
  [[ "$(stat -c '%a' "$envelope_marker")" != "600" ]]; then
  echo "durable envelope permissions: dir=$(stat -c '%a' "$envelope_metadata") registry=$(stat -c '%a' "$registry_path") marker=$(stat -c '%a' "$envelope_marker")" >&2
  exit 1
fi

# Seven semantic writes take one recovery copy each, bounded at five retained.
for envelope_write in 1 2 3 4 5 6 7; do
  cp "$registry_path" "$PROJMUX_SMOKE_WORKDIR/envelope-before-$envelope_write"
  PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" TMUX="$primary_tmux_env" \
    "$bin" rename project "uid:$primary_project_uid" --name "envelope-$envelope_write" \
    >"$PROJMUX_SMOKE_WORKDIR/envelope-rename-$envelope_write.out"
done
mapfile -t envelope_copies < <(find "$envelope_recovery" -maxdepth 1 -type f -name 'registry-*.json' -printf '%f\n' | sort)
if [[ "${#envelope_copies[@]}" != 5 ]]; then
  echo "recovery copies = ${#envelope_copies[@]} (${envelope_copies[*]}), want the bounded 5" >&2
  exit 1
fi
# Copy names sort chronologically, so the retained window is the newest five and
# the ends of it are the bytes the third and the seventh write replaced.
cmp "$PROJMUX_SMOKE_WORKDIR/envelope-before-3" "$envelope_recovery/${envelope_copies[0]}"
cmp "$PROJMUX_SMOKE_WORKDIR/envelope-before-7" "$envelope_recovery/${envelope_copies[4]}"
if [[ "$(stat -c '%a' "$envelope_recovery")" != "700" ]]; then
  echo "recovery dir mode = $(stat -c '%a' "$envelope_recovery"), want 700" >&2
  exit 1
fi
for envelope_copy in "${envelope_copies[@]}"; do
  if [[ "$(stat -c '%a' "$envelope_recovery/$envelope_copy")" != "600" ]]; then
    echo "recovery copy $envelope_copy mode = $(stat -c '%a' "$envelope_recovery/$envelope_copy"), want 600" >&2
    exit 1
  fi
done
if find "$envelope_metadata" "$envelope_recovery" -maxdepth 1 -name '*.tmp-*' -print -quit | grep -q .; then
  echo "the durable envelope leaked a staged temp file" >&2
  find "$envelope_metadata" >&2
  exit 1
fi

# A convergent no-op replaces neither the Registry nor a recovery slot.
envelope_registry_before="$(stat -c '%i %s %y' "$registry_path")"
envelope_marker_before="$(stat -c '%i %s %y' "$envelope_marker")"
envelope_copies_before="$(find "$envelope_recovery" -maxdepth 1 -type f -name 'registry-*.json' -printf '%f\n' | sort)"
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" reconcile resources --socket "$PROJMUX_RECONCILE_PRIMARY_SOCKET" -o json \
  >"$PROJMUX_SMOKE_WORKDIR/envelope-noop.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/envelope-noop.json" '"outcome": "no-op"'
if [[ "$(stat -c '%i %s %y' "$registry_path")" != "$envelope_registry_before" ]] ||
  [[ "$(stat -c '%i %s %y' "$envelope_marker")" != "$envelope_marker_before" ]] ||
  [[ "$(find "$envelope_recovery" -maxdepth 1 -type f -name 'registry-*.json' -printf '%f\n' | sort)" != "$envelope_copies_before" ]]; then
  echo "a convergent no-op touched the durable envelope" >&2
  exit 1
fi

# Losing only registry.json is a typed diagnostic, not an empty first use, and
# neither reads nor mutations recreate state on top of the loss.
rm "$registry_path"
set +e
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" get projects -o json >"$PROJMUX_SMOKE_WORKDIR/envelope-loss.out" 2>"$PROJMUX_SMOKE_WORKDIR/envelope-loss.err"
envelope_loss_status=$?
set -e
if [[ "$envelope_loss_status" == "0" ]] || [[ -s "$PROJMUX_SMOKE_WORKDIR/envelope-loss.out" ]]; then
  echo "a lost Registry read as a first use: status=$envelope_loss_status" >&2
  cat "$PROJMUX_SMOKE_WORKDIR/envelope-loss.out" >&2
  exit 1
fi
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/envelope-loss.err" 'resource registry is missing after initialization'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/envelope-loss.err" "$envelope_recovery"
set +e
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" TMUX="$primary_tmux_env" \
  "$bin" rename project "uid:$primary_project_uid" --name envelope-after-loss \
  >"$PROJMUX_SMOKE_WORKDIR/envelope-loss-mutation.out" 2>"$PROJMUX_SMOKE_WORKDIR/envelope-loss-mutation.err"
envelope_mutation_status=$?
set -e
if [[ "$envelope_mutation_status" == "0" ]] || [[ -e "$registry_path" ]]; then
  echo "a mutation after Registry loss minted state instead of refusing: status=$envelope_mutation_status" >&2
  exit 1
fi
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/envelope-loss-mutation.err" 'resource registry is missing after initialization'
if [[ "$(find "$envelope_recovery" -maxdepth 1 -type f -name 'registry-*.json' -printf '%f\n' | sort)" != "$envelope_copies_before" ]]; then
  echo "a refused route changed the recovery copies" >&2
  exit 1
fi

# Phase 1 recovery boundary, against the same live loss. The preview must be
# zero-write, the restore must publish only the source it was handed, the reads
# that were refused above must work again, and an unverifiable source must be
# refused with the Registry untouched.
envelope_preview_before="$(find "$envelope_metadata" -printf '%p %s %i %T@\n' | sort)"
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" reconcile registry --dry-run -o json \
  >"$PROJMUX_SMOKE_WORKDIR/recovery-preview.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/recovery-preview.json" '"outcome": "planned"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/recovery-preview.json" '"state": "missing"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/recovery-preview.json" '"eligible": true'
if [[ "$(find "$envelope_metadata" -printf '%p %s %i %T@\n' | sort)" != "$envelope_preview_before" ]]; then
  echo "the recovery preview wrote to the state dir" >&2
  exit 1
fi
# A second preview over the same state is byte-identical output.
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" reconcile registry --dry-run -o json \
  >"$PROJMUX_SMOKE_WORKDIR/recovery-preview-2.json"
cmp "$PROJMUX_SMOKE_WORKDIR/recovery-preview.json" "$PROJMUX_SMOKE_WORKDIR/recovery-preview-2.json"

# The preview's own suggestion is the guarded command an operator runs.
recovery_source="$(sed -n 's/.*"next": "projmux reconcile registry --source \x27\([^\x27]*\)\x27.*/\1/p' "$PROJMUX_SMOKE_WORKDIR/recovery-preview.json")"
recovery_expect="$(sed -n 's/.*--expect-source-checksum \(sha256:[0-9a-f]*\).*/\1/p' "$PROJMUX_SMOKE_WORKDIR/recovery-preview.json")"
if [[ -z "$recovery_source" ]] || [[ -z "$recovery_expect" ]]; then
  echo "the recovery preview printed no guarded restore command" >&2
  cat "$PROJMUX_SMOKE_WORKDIR/recovery-preview.json" >&2
  exit 1
fi
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" reconcile registry --source "$recovery_source" --expect-source-checksum "$recovery_expect" -o json \
  >"$PROJMUX_SMOKE_WORKDIR/recovery-restore.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/recovery-restore.json" '"outcome": "restored"'
cmp "$registry_path" "$envelope_recovery/$recovery_source"
if [[ "$(stat -c '%a' "$registry_path")" != "600" ]]; then
  echo "restored registry mode = $(stat -c '%a' "$registry_path"), want 600" >&2
  exit 1
fi
# The reads that failed under state loss work again on the restored identity.
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" get projects -o json >"$PROJMUX_SMOKE_WORKDIR/recovery-projects.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/recovery-projects.json" "$primary_project_uid"
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" describe project "uid:$primary_project_uid" >"$PROJMUX_SMOKE_WORKDIR/recovery-describe.out"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/recovery-describe.out" "$primary_project_uid"

# A repeat restore of the same source changes no byte.
recovery_registry_fingerprint="$(stat -c '%i %s %y' "$registry_path")"
recovery_copies_before="$(find "$envelope_recovery" -maxdepth 1 -type f -printf '%f\n' | sort)"
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" reconcile registry --source "$recovery_source" -o json \
  >"$PROJMUX_SMOKE_WORKDIR/recovery-repeat.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/recovery-repeat.json" '"outcome": "no-op"'
if [[ "$(stat -c '%i %s %y' "$registry_path")" != "$recovery_registry_fingerprint" ]] ||
  [[ "$(find "$envelope_recovery" -maxdepth 1 -type f -printf '%f\n' | sort)" != "$recovery_copies_before" ]]; then
  echo "a repeat restore wrote to the durable envelope" >&2
  exit 1
fi

# Unverifiable and raced sources are refused with the Registry untouched.
printf 'not a registry at all' >"$envelope_recovery/registry-20260101T000000Z-00.json"
chmod 600 "$envelope_recovery/registry-20260101T000000Z-00.json"
smoke_recovery_refusal() {
  local label="$1"
  shift
  set +e
  PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
    "$bin" reconcile registry "$@" -o json \
    >"$PROJMUX_SMOKE_WORKDIR/recovery-refuse-$label.json" 2>"$PROJMUX_SMOKE_WORKDIR/recovery-refuse-$label.err"
  local status=$?
  set -e
  if [[ "$status" == "0" ]]; then
    echo "recovery refusal $label exited 0" >&2
    exit 1
  fi
  smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/recovery-refuse-$label.json" '"outcome": "refused"'
  if [[ "$(stat -c '%i %s %y' "$registry_path")" != "$recovery_registry_fingerprint" ]]; then
    echo "recovery refusal $label mutated the Registry" >&2
    exit 1
  fi
}
smoke_recovery_refusal corrupt --source registry-20260101T000000Z-00.json
smoke_recovery_refusal ambiguous --source registry-
smoke_recovery_refusal raced --source "$recovery_source" \
  --expect-current-checksum sha256:0000000000000000000000000000000000000000000000000000000000000000
rm "$envelope_recovery/registry-20260101T000000Z-00.json"

echo ">> registry recovery boundary root=$reconcile_state restored_from=$recovery_source guard=passed"

# Restore the state-loss precondition the marker-removal remedy below asserts
# against: the recovery boundary deliberately repaired it.
rm "$registry_path"

# The remedy the diagnostic names: without the marker the same state dir is a
# first use again, so an operator who accepts the loss is not stuck.
rm "$envelope_marker"
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" get projects -o json >"$PROJMUX_SMOKE_WORKDIR/envelope-after-marker-removal.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/envelope-after-marker-removal.json" '"items": []'
if [[ -e "$registry_path" ]]; then
  echo "a first-use read after marker removal recreated the Registry" >&2
  exit 1
fi

echo ">> durable registry envelope root=$reconcile_state retained_copies=${#envelope_copies[@]} guard=passed"

# ---------------------------------------------------------------------------
# Project discovery and pin authority split
#
# Three collections, three authorities, asserted against real files: the
# workdirs scan root, the Registry, and the typed pin file. Every leg below is
# about the boundary between them -- a scan result is not a Project, a managed
# pin is a uid, and an unregistered path is a candidate.
# ---------------------------------------------------------------------------

discovery_home="$PROJMUX_SMOKE_WORKDIR/discovery-home"
discovery_scan="$PROJMUX_SMOKE_WORKDIR/discovery-scan"
discovery_pins="$discovery_home/.config/projmux/pins"
discovery_registry="$discovery_home/.local/state/projmux/metadata/registry.json"
mkdir -p "$discovery_home/.config/projmux" "$discovery_scan/app" "$discovery_scan/scratch" "$discovery_scan/sibling"
printf '%s\n' "$discovery_scan" >"$discovery_home/.config/projmux/workdirs"
chmod 600 "$discovery_home/.config/projmux/workdirs"

# An isolated HOME with no XDG overrides: the workdirs file, the pin file and the
# Registry all live under it, so nothing here can reach the caller's real config.
pmx_discovery() {
  env -u TMUX -u TMUX_PANE -u XDG_CONFIG_HOME -u XDG_STATE_HOME -u PROJMUX_PROJDIR -u PROJMUX_MANAGED_ROOTS \
    HOME="$discovery_home" "$bin" "$@"
}

# Reading with a scan root full of children creates nothing at all.
pmx_discovery get projects -o json >"$PROJMUX_SMOKE_WORKDIR/discovery-projects-initial.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/discovery-projects-initial.json" '"items": []'
if [[ -e "$discovery_registry" ]]; then
  echo "a read over a populated scan root created a Registry" >&2
  exit 1
fi

# One explicit bootstrap registers one exact path.
pmx_discovery create project --root "$discovery_scan/app" >"$PROJMUX_SMOKE_WORKDIR/discovery-register.out"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/discovery-register.out" 'project/app created'
discovery_project_uid="$(pmx_discovery get projects -o uid)"
if [[ "$(printf '%s\n' "$discovery_project_uid" | wc -l)" != "1" ]]; then
  echo "explicit registration produced more than one Project: $discovery_project_uid" >&2
  exit 1
fi
pmx_discovery get projects -o json >"$PROJMUX_SMOKE_WORKDIR/discovery-projects-registered.json"
for unregistered in scratch sibling; do
  if grep -q "$discovery_scan/$unregistered" "$PROJMUX_SMOKE_WORKDIR/discovery-projects-registered.json"; then
    echo "a sibling candidate ($unregistered) was registered by the explicit bootstrap" >&2
    exit 1
  fi
done

# A repeated registration is a write-free no-op.
discovery_registry_fingerprint="$(stat -c '%i %s %y' "$discovery_registry")"
pmx_discovery create project --root "$discovery_scan/app" >"$PROJMUX_SMOKE_WORKDIR/discovery-register-repeat.out"
cmp "$PROJMUX_SMOKE_WORKDIR/discovery-register.out" "$PROJMUX_SMOKE_WORKDIR/discovery-register-repeat.out"
if [[ "$(stat -c '%i %s %y' "$discovery_registry")" != "$discovery_registry_fingerprint" ]]; then
  echo "a repeated registration rewrote the Registry" >&2
  exit 1
fi

# A generic mutation route runs the full reconcile prelude against a real server
# and still adds no Project for the two adjacent candidates.
PROJMUX_DISCOVERY_SESSION="app"
PROJMUX_DISCOVERY_SOCKET="projmux-discovery-$$-$RANDOM"
export PROJMUX_DISCOVERY_SESSION PROJMUX_DISCOVERY_SOCKET
env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_DISCOVERY_SOCKET" new-session -d -s "$PROJMUX_DISCOVERY_SESSION" -c "$discovery_scan/app" sleep 300
PROJMUX_DISCOVERY_STARTED=1
PROJMUX_DISCOVERY_ACTUAL="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_DISCOVERY_SOCKET" display-message -p -t "=$PROJMUX_DISCOVERY_SESSION" '#{socket_path}')"
export PROJMUX_DISCOVERY_ACTUAL
discovery_pid="$(env -u TMUX -u TMUX_PANE tmux -L "$PROJMUX_DISCOVERY_SOCKET" display-message -p -t "=$PROJMUX_DISCOVERY_SESSION" '#{pid}')"
discovery_tmux_env="$PROJMUX_DISCOVERY_ACTUAL,$discovery_pid,0"
env -u TMUX -u TMUX_PANE -u XDG_CONFIG_HOME -u XDG_STATE_HOME -u PROJMUX_PROJDIR -u PROJMUX_MANAGED_ROOTS \
  HOME="$discovery_home" TMUX="$discovery_tmux_env" \
  "$bin" create window --project app >"$PROJMUX_SMOKE_WORKDIR/discovery-create-window.out"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/discovery-create-window.out" 'window/'
pmx_discovery get projects -o uid >"$PROJMUX_SMOKE_WORKDIR/discovery-projects-after-mutation.uid"
if [[ "$(wc -l <"$PROJMUX_SMOKE_WORKDIR/discovery-projects-after-mutation.uid")" != "1" ]]; then
  echo "a generic mutation registered extra Projects from the scan root:" >&2
  cat "$PROJMUX_SMOKE_WORKDIR/discovery-projects-after-mutation.uid" >&2
  exit 1
fi

# An unregistered candidate is refused by name, with the bootstrap route named.
set +e
pmx_discovery create window --project scratch \
  >"$PROJMUX_SMOKE_WORKDIR/discovery-refusal.out" 2>"$PROJMUX_SMOKE_WORKDIR/discovery-refusal.err"
discovery_refusal_status=$?
set -e
if [[ "$discovery_refusal_status" == "0" ]]; then
  echo "create resolved a Project that nothing registered" >&2
  exit 1
fi
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/discovery-refusal.err" 'projmux create project --root'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/discovery-refusal.err" "$discovery_scan/scratch"

# Legacy pin migration: one line resolves to the registered uid, the other has no
# Project and stays a candidate.
printf '%s\n%s\n' "$discovery_scan/app" "$discovery_scan/scratch" >"$discovery_pins"
chmod 600 "$discovery_pins"
pmx_discovery pin project migrate --dry-run >"$PROJMUX_SMOKE_WORKDIR/discovery-pin-dry-run.out"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/discovery-pin-dry-run.out" "would migrate: $discovery_scan/app -> uid:$discovery_project_uid"
if [[ "$(head -n1 "$discovery_pins")" != "$discovery_scan/app" ]]; then
  echo "a pin migration dry run rewrote the pin file" >&2
  exit 1
fi
pmx_discovery pin project migrate >"$PROJMUX_SMOKE_WORKDIR/discovery-pin-migrate.out"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/discovery-pin-migrate.out" "migrated: $discovery_scan/app -> uid:$discovery_project_uid"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/discovery-pin-migrate.out" "candidate: $discovery_scan/scratch"
if [[ "$(cat "$discovery_pins")" != "$(printf 'projmux-pins v2\nproject %s\ncandidate %s' "$discovery_project_uid" "$discovery_scan/scratch")" ]]; then
  echo "the migrated pin file is not the typed envelope:" >&2
  cat "$discovery_pins" >&2
  exit 1
fi

# A repeat migration writes nothing.
discovery_pins_fingerprint="$(stat -c '%i %s %y' "$discovery_pins")"
pmx_discovery pin project migrate >"$PROJMUX_SMOKE_WORKDIR/discovery-pin-migrate-repeat.out"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/discovery-pin-migrate-repeat.out" 'already typed'
if [[ "$(stat -c '%i %s %y' "$discovery_pins")" != "$discovery_pins_fingerprint" ]]; then
  echo "a repeat pin migration rewrote the pin file" >&2
  exit 1
fi

# The managed pin follows the Project through a rename, a rebind, and a root that
# disappears. Its uid never changes and the candidate pin is never touched.
mkdir -p "$PROJMUX_SMOKE_WORKDIR/discovery-moved"
pmx_discovery rename project "uid:$discovery_project_uid" --name renamed-app >/dev/null
pmx_discovery rebind project "uid:$discovery_project_uid" --root "$PROJMUX_SMOKE_WORKDIR/discovery-moved" >/dev/null
pmx_discovery pin project list >"$PROJMUX_SMOKE_WORKDIR/discovery-pin-list-after-rebind.out" 2>/dev/null
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/discovery-pin-list-after-rebind.out" "uid:$discovery_project_uid"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/discovery-pin-list-after-rebind.out" "$PROJMUX_SMOKE_WORKDIR/discovery-moved"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/discovery-pin-list-after-rebind.out" "renamed-app"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/discovery-pin-list-after-rebind.out" "candidate	$discovery_scan/scratch"

rm -rf "$PROJMUX_SMOKE_WORKDIR/discovery-moved"
pmx_discovery get projects -o json >"$PROJMUX_SMOKE_WORKDIR/discovery-missing-root.json"
pmx_discovery pin project list --kind project >"$PROJMUX_SMOKE_WORKDIR/discovery-pin-list-missing-root.out" 2>/dev/null
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/discovery-pin-list-missing-root.out" "uid:$discovery_project_uid"

# An ambiguous legacy pin refuses the whole migration with the pin file and the
# Registry byte-identical, and a concurrent pair of migrations converges on one
# valid typed file rather than a torn one.
discovery_ambiguous_home="$PROJMUX_SMOKE_WORKDIR/discovery-ambiguous-home"
discovery_ambiguous_dup="$PROJMUX_SMOKE_WORKDIR/discovery-ambiguous-dup"
discovery_ambiguous_pins="$discovery_ambiguous_home/.config/projmux/pins"
discovery_ambiguous_registry="$discovery_ambiguous_home/.local/state/projmux/metadata/registry.json"
mkdir -p "$discovery_ambiguous_home/.config/projmux" "$discovery_ambiguous_dup"
pmx_ambiguous() {
  env -u TMUX -u TMUX_PANE -u XDG_CONFIG_HOME -u XDG_STATE_HOME -u PROJMUX_PROJDIR -u PROJMUX_MANAGED_ROOTS \
    HOME="$discovery_ambiguous_home" "$bin" "$@"
}
pmx_ambiguous create project --root "$discovery_ambiguous_dup" --name dup-a >/dev/null
# A second Project on the same canonical root, reached the only way it can be:
# registered elsewhere and rebound onto it. This is the state a path pin cannot
# be migrated through.
mkdir -p "$PROJMUX_SMOKE_WORKDIR/discovery-ambiguous-other"
pmx_ambiguous create project --root "$PROJMUX_SMOKE_WORKDIR/discovery-ambiguous-other" --name dup-b >/dev/null
ln -sfn "$discovery_ambiguous_dup" "$PROJMUX_SMOKE_WORKDIR/discovery-ambiguous-link"
pmx_ambiguous rebind project dup-b --root "$PROJMUX_SMOKE_WORKDIR/discovery-ambiguous-link" >/dev/null
printf '%s\n' "$discovery_ambiguous_dup" >"$discovery_ambiguous_pins"
chmod 600 "$discovery_ambiguous_pins"
discovery_ambiguous_pins_before="$(cat "$discovery_ambiguous_pins")"
discovery_ambiguous_registry_fingerprint="$(stat -c '%i %s %y' "$discovery_ambiguous_registry")"
set +e
pmx_ambiguous pin project migrate \
  >"$PROJMUX_SMOKE_WORKDIR/discovery-ambiguous.out" 2>"$PROJMUX_SMOKE_WORKDIR/discovery-ambiguous.err"
discovery_ambiguous_status=$?
set -e
if [[ "$discovery_ambiguous_status" == "0" ]]; then
  echo "an ambiguous legacy pin migrated instead of refusing" >&2
  exit 1
fi
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/discovery-ambiguous.err" 'match more than one Project'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/discovery-ambiguous.err" 'the pin file is unchanged'
if [[ "$(cat "$discovery_ambiguous_pins")" != "$discovery_ambiguous_pins_before" ]]; then
  echo "a refused migration rewrote the pin file" >&2
  exit 1
fi
if [[ "$(stat -c '%i %s %y' "$discovery_ambiguous_registry")" != "$discovery_ambiguous_registry_fingerprint" ]]; then
  echo "a refused migration mutated the Registry" >&2
  exit 1
fi
# The refusal names a repair; performing it lets the same migration through.
pmx_ambiguous rebind project dup-b --root "$PROJMUX_SMOKE_WORKDIR/discovery-ambiguous-other" >/dev/null
pmx_ambiguous pin project migrate >"$PROJMUX_SMOKE_WORKDIR/discovery-ambiguous-repaired.out"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/discovery-ambiguous-repaired.out" 'migrated:'
smoke_assert_file_contains "$discovery_ambiguous_pins" 'projmux-pins v2'

# Concurrent migrations of one legacy file converge on one valid typed envelope.
discovery_race_home="$PROJMUX_SMOKE_WORKDIR/discovery-race-home"
discovery_race_pins="$discovery_race_home/.config/projmux/pins"
mkdir -p "$discovery_race_home/.config/projmux"
printf '%s\n%s\n' "$discovery_scan/scratch" "$discovery_scan/sibling" >"$discovery_race_pins"
chmod 600 "$discovery_race_pins"
for _ in 1 2 3 4; do
  env -u TMUX -u TMUX_PANE -u XDG_CONFIG_HOME -u XDG_STATE_HOME -u PROJMUX_PROJDIR -u PROJMUX_MANAGED_ROOTS \
    HOME="$discovery_race_home" "$bin" pin project migrate >/dev/null 2>&1 &
done
wait
if [[ "$(head -n1 "$discovery_race_pins")" != "projmux-pins v2" ]]; then
  echo "concurrent migrations left a pin file that is not a typed envelope:" >&2
  cat "$discovery_race_pins" >&2
  exit 1
fi
env -u TMUX -u TMUX_PANE -u XDG_CONFIG_HOME -u XDG_STATE_HOME -u PROJMUX_PROJDIR -u PROJMUX_MANAGED_ROOTS \
  HOME="$discovery_race_home" "$bin" pin project list >"$PROJMUX_SMOKE_WORKDIR/discovery-race-list.out" 2>/dev/null
if [[ "$(wc -l <"$PROJMUX_SMOKE_WORKDIR/discovery-race-list.out")" != "2" ]]; then
  echo "concurrent migrations dropped or duplicated pins:" >&2
  cat "$PROJMUX_SMOKE_WORKDIR/discovery-race-list.out" >&2
  exit 1
fi

echo ">> discovery/pin authority split home=$discovery_home scan_root=$discovery_scan project=$discovery_project_uid guard=passed"

# ---------------------------------------------------------------------------
# Termination evidence transport.
#
# Managed panes launch through `internal supervise`, so the Registry records the
# child's real wait status bound to the exact activation generation the launch
# was issued with. This block proves all three observable outcomes -- clean
# exit, non-zero exit, and death by signal -- against a real tmux server, and
# proves `delete` names one exact server or refuses instead of reaching for the
# app's own socket.
#
# Isolation: inherited TMUX/TMUX_PANE are stripped from every setup call, both
# servers live under a run-unique -L name inside a TMUX_TMPDIR below the smoke
# root, the real #{socket_path} is queried and proven to sit inside that root,
# and cleanup kills only those exact sockets.
# ---------------------------------------------------------------------------
termination_root="$PROJMUX_SMOKE_WORKDIR/termination"
termination_socket="projmux-termination-$$-$RANDOM"
termination_sibling_socket="projmux-termination-sibling-$$-$RANDOM"
termination_session="work-evidence"
mkdir -p "$termination_root/state" "$termination_root/config" "$termination_root/tmux" "$termination_root/work/evidence"
chmod 0700 "$termination_root/tmux"
termination_real_tmux="$(command -v tmux)"

# The supervisor a managed pane execs resolves its state paths from the pane's
# own inherited environment, which is the tmux *server's* environment. In
# production that is the operator's; here the server has to be started with the
# same isolated state root the CLI calls use, or the receipts would land in the
# real one.
termination_tmux() {
  env -u TMUX -u TMUX_PANE \
    TMUX_TMPDIR="$termination_root/tmux" \
    XDG_STATE_HOME="$termination_root/state" \
    XDG_CONFIG_HOME="$termination_root/config" \
    PROJMUX_MANAGED_ROOTS="$termination_root/work" \
    "$termination_real_tmux" -L "$termination_socket" "$@"
}
termination_sibling_tmux() {
  env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$termination_root/tmux" \
    "$termination_real_tmux" -L "$termination_sibling_socket" "$@"
}

termination_tmux new-session -d -s "$termination_session" -n main -c "$termination_root/work/evidence" sleep 600
termination_tmux set-option -t "$termination_session" -q @projmux_project_path "$termination_root/work/evidence"
# Phase 6 transports supervisor receipts through the generated lifecycle hook:
# the supervisor appends without taking the Registry lock, then pane-exited runs
# controller convergence to absorb that journal row. Source the same generated
# config the installed server uses onto this exact isolated server; without it
# this fixture would exercise only the old direct-Registry transport.
termination_tmux source-file "$PROJMUX_SMOKE_WORKDIR/projmux.conf"
(termination_tmux show-hooks -g; termination_tmux show-hooks -gw) >"$termination_root/pane-exited-hook.txt"
smoke_assert_file_contains "$termination_root/pane-exited-hook.txt" "reason pane-exited"
termination_sibling_tmux new-session -d -s sibling -n main sleep 600
termination_sibling_tmux set-option -gq @projmux_termination_sentinel untouched

termination_socket_path="$(termination_tmux display-message -p -t "$termination_session" '#{socket_path}')"
termination_sibling_path="$(termination_sibling_tmux display-message -p -t sibling '#{socket_path}')"
for termination_candidate in "$termination_socket_path" "$termination_sibling_path"; do
  case "$termination_candidate" in
    "$termination_root"/*) ;;
    *)
      echo "termination smoke socket escaped the smoke root: $termination_candidate" >&2
      exit 1
      ;;
  esac
done
termination_server_pid="$(termination_tmux display-message -p -t "$termination_session" '#{pid}')"
termination_session_id="$(termination_tmux display-message -p -t "$termination_session" '#{session_id}')"
if [[ "$termination_session_id" != \$* ]]; then
  echo "termination fixture has invalid exact tmux session id: $termination_session_id" >&2
  exit 1
fi

termination_cleanup() {
  local actual
  for actual in "$termination_socket_path" "$termination_sibling_path"; do
    [[ -n "$actual" ]] || continue
    case "$actual" in
      "$termination_root"/*)
        env -u TMUX -u TMUX_PANE tmux -S "$actual" kill-server >/dev/null 2>&1 || true
        ;;
      *)
        echo "refusing termination smoke cleanup outside the smoke root: $actual" >&2
        ;;
    esac
  done
}

termination_pmx() {
  env -u TMUX -u TMUX_PANE \
    XDG_STATE_HOME="$termination_root/state" \
    XDG_CONFIG_HOME="$termination_root/config" \
    TMUX_TMPDIR="$termination_root/tmux" \
    PROJMUX_MANAGED_ROOTS="$termination_root/work" \
    SHELL=/bin/sh \
    "$bin" "$@"
}

# Inside a client the create routes address the inherited exact server, which is
# the same server the explicit --socket calls above name.
termination_pmx_inside() {
  env -u TMUX_PANE \
    TMUX="$termination_socket_path,$termination_server_pid,0" \
    XDG_STATE_HOME="$termination_root/state" \
    XDG_CONFIG_HOME="$termination_root/config" \
    TMUX_TMPDIR="$termination_root/tmux" \
    PROJMUX_MANAGED_ROOTS="$termination_root/work" \
    SHELL=/bin/sh \
    "$bin" "$@"
}

termination_pmx create project --root "$termination_root/work/evidence" --name evidence \
  >"$termination_root/register-project.out"
bounded_resource_reconcile_to_noop "$termination_root/reconcile" \
  termination_pmx reconcile resources --socket "$termination_socket" -o json
smoke_assert_file_contains "$termination_root/reconcile.json" '"outcome": "changed"'

# The receipt is read back through the shipped route rather than out of the
# registry file, so the smoke exercises the same projection an operator sees.
termination_receipt_field() {
  termination_pmx describe pane "uid:$1" -o json \
    | sed -n '/"lastTermination"/,$p' \
    | sed -n "s/^[[:space:]]*\"$2\": \(.*\)$/\1/p" \
    | head -n 1 \
    | sed 's/,$//; s/^"//; s/"$//'
}

termination_activation_generation() {
  termination_pmx describe pane "uid:$1" -o json \
    | sed -n '/"activation"/,/^[[:space:]]*}/p' \
    | sed -n 's/^[[:space:]]*"generation": "\([^"]*\)".*/\1/p' \
    | head -n 1
}

termination_activation_runtime_id() {
  termination_pmx describe pane "uid:$1" -o json \
    | sed -n '/"activation"/,/^[[:space:]]*}/p' \
    | sed -n 's/^[[:space:]]*"runtimeID": "\([^"]*\)".*/\1/p' \
    | head -n 1
}

# A pane-exited hook and the supervisor prewrite begin from the same process
# death. The Phase 8 recovery prelude makes it possible for that hook pass to
# observe absence before the lock-free receipt append is durable. Wait for the
# exact supervisor row, then replay the shipped hidden pane-exited route with the
# same exact socket/session/runtime handles. The existing assertions below still
# require the exact supervisor classification, wait status, source, and
# activation generation.
termination_await_journal_receipt() {
  local pane_uid="$1" want_class="$2"
  local journal="$termination_root/state/projmux/termination-receipts.jsonl"
  for _ in $(seq 1 200); do
    if [[ -s "$journal" ]] && awk \
      -v pane="\"paneUID\":\"$pane_uid\"" \
      -v classification="\"classification\":\"$want_class\"" \
      -v source='"source":"supervisor"' \
      'index($0, pane) && index($0, classification) && index($0, source) { found = 1 } END { exit !found }' \
      "$journal"; then
      return 0
    fi
    sleep 0.05
  done
  echo "no durable supervisor journal receipt was recorded for $pane_uid" >&2
  [[ ! -e "$journal" ]] || tail -n 20 "$journal" >&2
  exit 1
}

termination_replay_pane_exited_hook() {
  local pane_uid="$1" want_class="$2" runtime_id="$3" report_label="$4"
  if [[ "$runtime_id" != %* ]]; then
    echo "Pane $pane_uid has invalid activation runtimeID '$runtime_id'" >&2
    exit 1
  fi
  termination_await_journal_receipt "$pane_uid" "$want_class"
  termination_pmx internal tmux converge \
    --socket-path "$termination_socket_path" \
    --session "$termination_session_id" \
    --reason pane-exited \
    --hook-pane "$runtime_id" \
    >"$termination_root/receipt-converge-$report_label.out" \
    2>"$termination_root/receipt-converge-$report_label.err"
}

termination_await_receipt() {
  local pane_uid="$1" want_class="$2"
  for _ in $(seq 1 200); do
    # A runtime-created pass can briefly record reconcile/unknown before the
    # supervisor's append becomes visible. Phase 6 explicitly refines that
    # same-generation evidence, so wait for the expected settled class rather
    # than accepting the first non-empty observation.
    if [[ "$(termination_receipt_field "$pane_uid" classification)" == "$want_class" ]]; then
      return 0
    fi
    sleep 0.05
  done
  echo "no termination receipt was recorded for $pane_uid" >&2
  termination_pmx describe pane "uid:$pane_uid" -o json >&2 || true
  exit 1
}

# Each case launches a managed shell Pane whose child ends a different way. The
# `--` payload words are the child's own argv; the supervisor prefixes them, and
# `-o uid` is read from the Registry so the assertion never races the pane's own
# disappearance.
termination_case() {
  local label="$1" want_class="$2" want_code="$3" want_signal="$4"
  shift 4
  local pane_uid runtime_id
  pane_uid="$(termination_pmx_inside create pane --project evidence -o uid -- "$@")"
  if [[ -z "$pane_uid" ]]; then
    echo "termination case $label created no Pane" >&2
    exit 1
  fi
  runtime_id="$(termination_activation_runtime_id "$pane_uid")"
  termination_replay_pane_exited_hook "$pane_uid" "$want_class" "$runtime_id" "$label"
  termination_await_receipt "$pane_uid" "$want_class"
  local got_class got_code got_signal got_source got_generation activation
  got_class="$(termination_receipt_field "$pane_uid" classification)"
  got_code="$(termination_receipt_field "$pane_uid" exitCode)"
  got_signal="$(termination_receipt_field "$pane_uid" signal)"
  got_source="$(termination_receipt_field "$pane_uid" source)"
  got_generation="$(termination_receipt_field "$pane_uid" generation)"
  activation="$(termination_activation_generation "$pane_uid")"
  if [[ "$got_class" != "$want_class" || "$got_code" != "$want_code" || "$got_signal" != "$want_signal" ]]; then
    echo "termination case $label recorded class=$got_class code=$got_code signal=$got_signal, want class=$want_class code=$want_code signal=$want_signal" >&2
    exit 1
  fi
  if [[ "$got_source" != "supervisor" ]]; then
    echo "termination case $label recorded source=$got_source, want supervisor" >&2
    exit 1
  fi
  if [[ -z "$activation" || "$got_generation" != "$activation" ]]; then
    echo "termination case $label receipt generation '$got_generation' is not the Pane's activation generation '$activation'" >&2
    exit 1
  fi
  echo ">> termination case $label uid=$pane_uid class=$got_class generation=$activation"
}

termination_case clean normal 0 "" sh -c 'exit 0'
termination_case failure abnormal 7 "" sh -c 'exit 7'
termination_case signal abnormal "" TERM sh -c 'kill -TERM $$; sleep 30'

# The same evidence for the three managed providers. Each stub is a real
# process that ends the way the case asks for; nothing about the provider's own
# protocol participates, which is the point -- the receipt comes from the wait
# status, never from what the provider said before it stopped.
mkdir -p "$termination_root/bin"
# The stub names are the provider *binaries* projmux resolves, which is not
# always the provider id: Antigravity's CLI is `agy`.
for termination_provider in claude codex agy; do
  cat >"$termination_root/bin/$termination_provider" <<PROVIDER_STUB
#!/usr/bin/env bash
exec sh -c "\$(cat $(printf %q "$termination_root/stub-script"))"
PROVIDER_STUB
  chmod 0755 "$termination_root/bin/$termination_provider"
done

termination_pmx_provider() {
  env -u TMUX_PANE \
    TMUX="$termination_socket_path,$termination_server_pid,0" \
    PATH="$termination_root/bin:$PATH" \
    XDG_STATE_HOME="$termination_root/state" \
    XDG_CONFIG_HOME="$termination_root/config" \
    TMUX_TMPDIR="$termination_root/tmux" \
    PROJMUX_MANAGED_ROOTS="$termination_root/work" \
    SHELL=/bin/bash \
    "$bin" "$@"
}

# The Agent document is fetched once per observation and parsed out of a file,
# so a failed read is a visible empty document rather than a pipeline whose exit
# status the last stage swallowed.
termination_agent_json() {
  termination_pmx describe agent "uid:$1" -o json >"$termination_root/agent.json"
}

termination_agent_field() {
  sed -n '/"lastTermination"/,$p' "$termination_root/agent.json" \
    | sed -n "s/^[[:space:]]*\"$1\": \(.*\)$/\1/p" \
    | head -n 1 \
    | sed 's/,$//; s/^"//; s/"$//'
}

termination_agent_pane_ref() {
  sed -n 's/^[[:space:]]*"paneRef": "\([^"]*\)".*/\1/p' "$termination_root/agent.json" \
    | head -n 1
}

termination_provider_case() {
  local provider="$1" want_class="$2" want_code="$3" want_signal="$4" script="$5"
  # The stub outlives the create transaction on purpose. A provider that ends
  # before its own create commits is a different case -- the shipped
  # dead-managed-Pane sweep can retire the Pane inside that same transaction --
  # and this block is about the receipt, not about that race.
  printf 'sleep 0.5\n%s\n' "$script" >"$termination_root/stub-script"
  local agent_uid pane_ref got_class got_code got_signal got_source got_generation activation runtime_id
  agent_uid="$(termination_pmx_provider create agent --project evidence --provider "$provider" -o uid)"
  if [[ -z "$agent_uid" ]]; then
    echo "termination provider case $provider created no Agent" >&2
    exit 1
  fi
  # The managed Pane's generation is read while the provider is still running,
  # so the comparison below is against the value the launch was issued with.
  termination_agent_json "$agent_uid"
  pane_ref="$(termination_agent_pane_ref)"
  if [[ -z "$pane_ref" ]]; then
    echo "termination provider case $provider Agent carries no managed Pane binding" >&2
    cat "$termination_root/agent.json" >&2
    exit 1
  fi
  activation="$(termination_activation_generation "$pane_ref")"
  if [[ -z "$activation" ]]; then
    echo "termination provider case $provider managed Pane $pane_ref carries no activation generation" >&2
    termination_pmx describe pane "uid:$pane_ref" -o json >&2 || true
    exit 1
  fi
  runtime_id="$(termination_activation_runtime_id "$pane_ref")"
  termination_replay_pane_exited_hook "$pane_ref" "$want_class" "$runtime_id" "provider-$provider"
  for _ in $(seq 1 200); do
    termination_agent_json "$agent_uid"
    if [[ -n "$(termination_agent_field classification)" ]]; then
      break
    fi
    sleep 0.05
  done
  got_class="$(termination_agent_field classification)"
  got_code="$(termination_agent_field exitCode)"
  got_signal="$(termination_agent_field signal)"
  got_source="$(termination_agent_field source)"
  got_generation="$(termination_agent_field generation)"
  if [[ "$got_class" != "$want_class" || "$got_code" != "$want_code" || "$got_signal" != "$want_signal" ]]; then
    echo "termination provider case $provider recorded class=$got_class code=$got_code signal=$got_signal, want class=$want_class code=$want_code signal=$want_signal" >&2
    cat "$termination_root/agent.json" >&2
    exit 1
  fi
  if [[ "$got_source" != "supervisor" ]]; then
    echo "termination provider case $provider recorded source=$got_source, want supervisor" >&2
    cat "$termination_root/agent.json" >&2
    exit 1
  fi
  if [[ "$got_generation" != "$activation" ]]; then
    echo "termination provider case $provider receipt generation '$got_generation' is not the managed Pane's activation generation '$activation'" >&2
    cat "$termination_root/agent.json" >&2
    exit 1
  fi
  # The Agent keeps the evidence even though nothing here consumed it: turning a
  # receipt into a phase belongs to a later Phase.
  echo ">> termination provider case $provider agent=$agent_uid pane=$pane_ref class=$got_class generation=$activation"
}

termination_provider_case claude normal 0 "" 'exit 0'
termination_provider_case codex abnormal 5 "" 'exit 5'
termination_provider_case antigravity abnormal "" TERM 'kill -TERM $$; sleep 30'

# A long-lived Pane proves the supervisor really is the pane's own process and
# that the Registry recorded the exact live handle it landed on.
termination_pane_uid="$(termination_pmx_inside create pane --project evidence -o uid -- sleep 600)"
termination_pane_id="$(termination_tmux list-panes -a -F '#{@projmux_pane_uid} #{pane_id}' \
  | awk -v uid="$termination_pane_uid" '$1 == uid { print $2; exit }')"
if [[ -z "$termination_pane_id" ]]; then
  echo "the long-lived termination Pane has no exact live binding" >&2
  exit 1
fi
termination_start_command="$(termination_tmux display-message -p -t "$termination_pane_id" '#{pane_start_command}')"
case "$termination_start_command" in
  *"internal supervise"*"-- sleep 600") ;;
  *)
    echo "the managed Pane did not launch through the supervisor: $termination_start_command" >&2
    exit 1
    ;;
esac
if [[ -n "$(termination_receipt_field "$termination_pane_uid" classification)" ]]; then
  echo "a running Pane already carries a termination receipt" >&2
  exit 1
fi

# A delete names its server or refuses. It never reaches for `-L projmux`.
set +e
termination_pmx delete pane "uid:$termination_pane_uid" --yes \
  >"$termination_root/delete-no-target.out" 2>"$termination_root/delete-no-target.err"
termination_status=$?
set -e
if [[ "$termination_status" != "2" ]]; then
  echo "delete outside tmux with no socket flag exited $termination_status, want the usage refusal" >&2
  cat "$termination_root/delete-no-target.err" >&2
  exit 1
fi
smoke_assert_file_contains "$termination_root/delete-no-target.err" "requires --socket"
if [[ -s "$termination_root/delete-no-target.out" ]]; then
  echo "a refused delete wrote stdout" >&2
  exit 1
fi
if [[ "$(termination_tmux display-message -p -t "$termination_pane_id" '#{pane_id}' 2>/dev/null || true)" != "$termination_pane_id" ]]; then
  echo "a refused delete killed the live Pane anyway" >&2
  exit 1
fi

termination_sibling_before="$(termination_sibling_tmux show-options -gqv @projmux_termination_sentinel):$(termination_sibling_tmux list-panes -a -F '#{pane_id}')"
termination_pmx delete pane "uid:$termination_pane_uid" --socket "$termination_socket" --dry-run \
  >"$termination_root/delete-dry-run.out"
smoke_assert_file_contains "$termination_root/delete-dry-run.out" "live would kill tmux pane $termination_pane_id"
# The reported socket is the one the invocation named. The run-unique name is
# the whole assertion: a fallback to the app default would print `-L/projmux`,
# which is not this string. A separate "must not contain -L/projmux" check would
# be a prefix collision, since the isolated name starts with `projmux` too.
smoke_assert_file_contains "$termination_root/delete-dry-run.out" "socket=-L/$termination_socket"
if [[ "$(termination_tmux display-message -p -t "$termination_pane_id" '#{pane_id}' 2>/dev/null || true)" != "$termination_pane_id" ]]; then
  echo "a dry-run delete killed the live Pane" >&2
  exit 1
fi
termination_pmx delete pane "uid:$termination_pane_uid" --socket "$termination_socket" --yes \
  >"$termination_root/delete-apply.out"
if [[ "$(termination_tmux display-message -p -t "$termination_pane_id" '#{pane_id}' 2>/dev/null || true)" == "$termination_pane_id" ]]; then
  echo "an exact-socket delete left the Pane live" >&2
  exit 1
fi
if [[ "$(termination_sibling_tmux show-options -gqv @projmux_termination_sentinel):$(termination_sibling_tmux list-panes -a -F '#{pane_id}')" != "$termination_sibling_before" ]]; then
  echo "an exact-socket delete mutated the sibling socket" >&2
  exit 1
fi

termination_cleanup
echo ">> termination evidence transport root=$termination_root socket=$termination_socket_path guard=passed"
echo ">> lifecycle smoke root=$PROJMUX_SMOKE_WORKDIR actual_socket=$PROJMUX_SMOKE_TMUX_ACTUAL guard=passed"

# --- exit reconciliation and lifecycle projection ---------------------------
#
# The Phase 6 block above proves hook-driven recording. This older explicit
# reconciliation surface proves the same receipt is consumed without granting
# observation deletion authority: a receipt plus runtime absence become Agent
# status and a released binding while every logical Pane row is preserved.
#
# Everything runs on run-unique sockets under a run-unique root, with a sibling
# server carrying the same mirrored Pane uid so containment is an assertion
# rather than a claim. Nothing here touches the user's server or the default
# socket.
exitrec_root="$PROJMUX_SMOKE_WORKDIR/exit-reconcile"
exitrec_socket="projmux-exitrec-$$-$RANDOM"
exitrec_standalone_socket="projmux-exitrec-standalone-$$-$RANDOM"
exitrec_sibling_socket="projmux-exitrec-sibling-$$-$RANDOM"
exitrec_session="work-evidence"
exitrec_standalone_session="work-standalone"
mkdir -p "$exitrec_root/state" "$exitrec_root/config" "$exitrec_root/tmux" \
  "$exitrec_root/bin" "$exitrec_root/work/evidence" "$exitrec_root/work/standalone"
chmod 0700 "$exitrec_root/tmux"
exitrec_real_tmux="$(command -v tmux)"

# Every server in this block is started with the same isolated state root the
# receipts are read back from. That is not tidiness: the supervisor resolves its
# state paths from the pane's inherited environment, which is the tmux server's
# environment, so a server started with a different root would write its receipts
# somewhere the assertions never look.
exitrec_tmux() {
  local socket="$1"
  shift
  env -u TMUX -u TMUX_PANE \
    TMUX_TMPDIR="$exitrec_root/tmux" \
    XDG_STATE_HOME="$exitrec_root/state" \
    XDG_CONFIG_HOME="$exitrec_root/config" \
    PROJMUX_MANAGED_ROOTS="$exitrec_root/work" \
    PATH="$exitrec_root/bin:$PATH" \
    SHELL=/bin/bash \
    "$exitrec_real_tmux" -L "$socket" "$@"
}

exitrec_sibling_tmux() {
  env -u TMUX -u TMUX_PANE TMUX_TMPDIR="$exitrec_root/tmux" \
    "$exitrec_real_tmux" -L "$exitrec_sibling_socket" "$@"
}

# The three provider stubs are real processes that end the way each case asks
# for. Nothing about a provider's own protocol participates: the classification
# comes from the wait status, never from what the provider said before it
# stopped.
for exitrec_provider in claude codex agy; do
  cat >"$exitrec_root/bin/$exitrec_provider" <<PROVIDER_STUB
#!/usr/bin/env bash
exec sh -c "\$(cat $(printf %q "$exitrec_root/stub-script"))"
PROVIDER_STUB
  chmod 0755 "$exitrec_root/bin/$exitrec_provider"
done

exitrec_tmux "$exitrec_socket" new-session -d -s "$exitrec_session" -n main \
  -c "$exitrec_root/work/evidence" sleep 600
exitrec_tmux "$exitrec_socket" set-option -t "$exitrec_session" -q @projmux_project_path \
  "$exitrec_root/work/evidence"
exitrec_tmux "$exitrec_standalone_socket" new-session -d -s "$exitrec_standalone_session" -n main \
  -c "$exitrec_root/work/standalone" sleep 600
exitrec_tmux "$exitrec_standalone_socket" set-option -t "$exitrec_standalone_session" -q @projmux_project_path \
  "$exitrec_root/work/standalone"
exitrec_sibling_tmux new-session -d -s sibling -n main sleep 600
exitrec_sibling_tmux set-option -gq @projmux_exitrec_sentinel untouched

exitrec_socket_path="$(exitrec_tmux "$exitrec_socket" display-message -p -t "$exitrec_session" '#{socket_path}')"
exitrec_standalone_path="$(exitrec_tmux "$exitrec_standalone_socket" display-message -p -t "$exitrec_standalone_session" '#{socket_path}')"
exitrec_sibling_path="$(exitrec_sibling_tmux display-message -p -t sibling '#{socket_path}')"
for exitrec_candidate in "$exitrec_socket_path" "$exitrec_standalone_path" "$exitrec_sibling_path"; do
  case "$exitrec_candidate" in
    "$exitrec_root"/*) ;;
    *)
      echo "exit reconciliation smoke socket escaped the smoke root: $exitrec_candidate" >&2
      exit 1
      ;;
  esac
done
exitrec_server_pid="$(exitrec_tmux "$exitrec_socket" display-message -p -t "$exitrec_session" '#{pid}')"
exitrec_standalone_pid="$(exitrec_tmux "$exitrec_standalone_socket" display-message -p -t "$exitrec_standalone_session" '#{pid}')"

# Cleanup kills the exact `#{socket_path}` each server reported, and only when it
# is under this run's root. A bare `kill-server` or a cleanup that trusted
# TMUX_TMPDIR alone could reach the operator's own session.
exitrec_cleanup() {
  local actual
  for actual in "$exitrec_socket_path" "$exitrec_standalone_path" "$exitrec_sibling_path"; do
    [[ -n "$actual" ]] || continue
    case "$actual" in
      "$exitrec_root"/*)
        env -u TMUX -u TMUX_PANE "$exitrec_real_tmux" -S "$actual" kill-server >/dev/null 2>&1 || true
        ;;
      *)
        echo "refusing exit reconciliation cleanup outside the smoke root: $actual" >&2
        ;;
    esac
  done
}
# The outer guarded cleanup stays installed: this block chains in front of it and
# restores it afterwards, so a failure anywhere below still tears down both this
# block's servers and the smoke run's own.
trap 'exitrec_cleanup; integration_cleanup' EXIT

exitrec_pmx() {
  env -u TMUX -u TMUX_PANE \
    XDG_STATE_HOME="$exitrec_root/state" \
    XDG_CONFIG_HOME="$exitrec_root/config" \
    TMUX_TMPDIR="$exitrec_root/tmux" \
    PROJMUX_MANAGED_ROOTS="$exitrec_root/work" \
    PATH="$exitrec_root/bin:$PATH" \
    SHELL=/bin/bash \
    "$bin" "$@"
}

# The in-tmux caller. $TMUX is the inherited absolute socket path, which is the
# exact-host rule both host modes share -- an app-owned server and a standalone
# one are addressed identically, because the address is the inherited socket and
# not a name projmux knows.
exitrec_pmx_inside() {
  local socket_path="$1" server_pid="$2"
  shift 2
  env -u TMUX_PANE \
    TMUX="$socket_path,$server_pid,0" \
    XDG_STATE_HOME="$exitrec_root/state" \
    XDG_CONFIG_HOME="$exitrec_root/config" \
    TMUX_TMPDIR="$exitrec_root/tmux" \
    PROJMUX_MANAGED_ROOTS="$exitrec_root/work" \
    PATH="$exitrec_root/bin:$PATH" \
    SHELL=/bin/bash \
    "$bin" "$@"
}

exitrec_pmx create project --root "$exitrec_root/work/evidence" --name evidence \
  >"$exitrec_root/register-evidence.out"
bounded_resource_reconcile_to_noop "$exitrec_root/reconcile" \
  exitrec_pmx reconcile resources --socket "$exitrec_socket" -o json
smoke_assert_file_contains "$exitrec_root/reconcile.json" '"outcome": "changed"'
exitrec_pmx create project --root "$exitrec_root/work/standalone" --name standalone \
  >"$exitrec_root/register-standalone.out"
bounded_resource_reconcile_to_noop "$exitrec_root/reconcile-standalone" \
  exitrec_pmx reconcile resources --socket "$exitrec_standalone_socket" -o json
smoke_assert_file_contains "$exitrec_root/reconcile-standalone.json" '"outcome": "changed"'

# One document per observation, parsed out of a file, so a failed read is a
# visible empty document rather than a pipeline whose exit status the last stage
# swallowed.
exitrec_doc() {
  exitrec_pmx describe "$1" "uid:$2" -o json >"$exitrec_root/doc.json" 2>"$exitrec_root/doc.err" || true
}

exitrec_field() {
  sed -n "s/^[[:space:]]*\"$1\": \(.*\)$/\1/p" "$exitrec_root/doc.json" \
    | head -n 1 \
    | sed 's/,$//; s/^"//; s/"$//'
}

exitrec_termination_field() {
  sed -n '/"lastTermination"/,$p' "$exitrec_root/doc.json" \
    | sed -n "s/^[[:space:]]*\"$1\": \(.*\)$/\1/p" \
    | head -n 1 \
    | sed 's/,$//; s/^"//; s/"$//'
}

exitrec_doc_exists() {
  exitrec_doc "$1" "$2"
  [[ -s "$exitrec_root/doc.json" ]]
}

# The reconciliation is driven through the installed public mutation route on the
# exact socket. Reading is a separate call so the read/write split stays visible:
# nothing below reconciles as a side effect of asking a question.
exitrec_reconcile() {
  exitrec_pmx reconcile resources --socket "$1" -o json >"$exitrec_root/reconcile-run.json"
}

# A managed Agent whose child ends a different way each time. The short delay
# deliberately lets create commit the Agent binding before the provider exits;
# this block tests termination consumption, not rollback of a create whose child
# vanished inside its own transaction.
exitrec_agent_case() {
  local label="$1" provider="$2" want_phase="$3" want_class="$4" script="$5"
  local agent_uid pane_ref got_phase got_class got_source got_reason
  printf 'sleep 0.5\n%s\n' "$script" >"$exitrec_root/stub-script"
  agent_uid="$(exitrec_pmx_inside "$exitrec_socket_path" "$exitrec_server_pid" \
    create agent --provider "$provider" --project evidence -o uid)"
  if [[ -z "$agent_uid" ]]; then
    echo "exit reconciliation case $label created no Agent" >&2
    exit 1
  fi
  exitrec_doc agent "$agent_uid"
  pane_ref="$(exitrec_field paneRef)"
  if [[ -z "$pane_ref" ]]; then
    echo "exit reconciliation case $label bound no managed Pane" >&2
    cat "$exitrec_root/doc.json" >&2
    exit 1
  fi

  # Wait for the runtime object to actually be gone, which is the input the
  # reconciliation consumes. Polling the live server rather than sleeping is what
  # keeps this from being timing-dependent.
  for _ in $(seq 1 100); do
    if ! exitrec_tmux "$exitrec_socket" list-panes -a -F '#{@projmux_pane_uid}' 2>/dev/null \
      | grep -qx "$pane_ref"; then
      break
    fi
    sleep 0.1
  done
  if exitrec_tmux "$exitrec_socket" list-panes -a -F '#{@projmux_pane_uid}' 2>/dev/null \
    | grep -qx "$pane_ref"; then
    echo "exit reconciliation case $label still has a live pane for $pane_ref" >&2
    exit 1
  fi

  exitrec_reconcile "$exitrec_socket"
  exitrec_doc agent "$agent_uid"
  got_phase="$(exitrec_field phase)"
  got_reason="$(exitrec_field reason)"
  got_class="$(exitrec_termination_field classification)"
  got_source="$(exitrec_termination_field source)"
  if [[ "$got_phase" != "$want_phase" ]]; then
    echo "exit reconciliation case $label phase=$got_phase, want $want_phase" >&2
    cat "$exitrec_root/doc.json" >&2
    exit 1
  fi
  if [[ "$got_class" != "$want_class" || "$got_source" != "supervisor" ]]; then
    echo "exit reconciliation case $label evidence=$got_class/$got_source, want $want_class/supervisor" >&2
    cat "$exitrec_root/doc.json" >&2
    exit 1
  fi
  if [[ -n "$(exitrec_field paneRef)" ]]; then
    echo "exit reconciliation case $label left paneRef=$(exitrec_field paneRef) bound to a dead pane" >&2
    exit 1
  fi
  if ! exitrec_doc_exists pane "$pane_ref"; then
    echo "exit reconciliation case $label deleted the managed Pane resource $pane_ref" >&2
    exit 1
  fi
  if [[ "$(exitrec_termination_field classification)" != "$want_class" ||
        "$(exitrec_termination_field source)" != "supervisor" ]]; then
    echo "exit reconciliation case $label Pane evidence did not preserve $want_class/supervisor" >&2
    cat "$exitrec_root/doc.json" >&2
    exit 1
  fi
  if [[ -z "$got_reason" ]]; then
    echo "exit reconciliation case $label recorded no status.reason" >&2
    exit 1
  fi

  # A repeat reconciliation of the same disappearance must change nothing.
  local before after
  before="$(cksum "$exitrec_root/state/projmux/metadata/registry.json" | awk '{print $1, $2}')"
  exitrec_reconcile "$exitrec_socket"
  after="$(cksum "$exitrec_root/state/projmux/metadata/registry.json" | awk '{print $1, $2}')"
  if [[ "$before" != "$after" ]]; then
    echo "exit reconciliation case $label rewrote the registry on a repeat pass" >&2
    exit 1
  fi
  echo ">> exit reconciliation case $label agent=$agent_uid phase=$got_phase class=$got_class source=$got_source"
}

exitrec_agent_case clean-exit claude Offline normal 'exit 0'
exitrec_agent_case failed-exit codex Failed abnormal 'exit 42'
exitrec_agent_case signal-death antigravity Failed abnormal 'kill -TERM $$; sleep 30'

# Direct `tmux kill-pane` is external kill evidence, never control intent. The
# supervisor reaps SIGHUP and the append journal converges to killed/supervisor;
# the Agent releases its binding while the logical Pane row remains queryable.
printf '%s\n' 'sleep 600' >"$exitrec_root/stub-script"
exitrec_external_agent="$(exitrec_pmx_inside "$exitrec_socket_path" "$exitrec_server_pid" \
  create agent --provider claude --project evidence -o uid)"
exitrec_doc agent "$exitrec_external_agent"
exitrec_external_pane="$(exitrec_field paneRef)"
exitrec_external_pane_id="$(exitrec_tmux "$exitrec_socket" list-panes -a -F '#{@projmux_pane_uid} #{pane_id}' \
  | awk -v uid="$exitrec_external_pane" '$1 == uid { print $2; exit }')"
if [[ -z "$exitrec_external_pane_id" ]]; then
  echo "the externally killed Agent Pane has no exact live binding" >&2
  exit 1
fi
exitrec_tmux "$exitrec_socket" kill-pane -t "$exitrec_external_pane_id"
for _ in $(seq 1 100); do
  exitrec_reconcile "$exitrec_socket"
  exitrec_doc agent "$exitrec_external_agent"
  if [[ "$(exitrec_termination_field classification)" == "killed" &&
        "$(exitrec_termination_field source)" == "supervisor" ]]; then
    break
  fi
  sleep 0.05
done
if [[ "$(exitrec_field phase)" != "Offline" ||
      "$(exitrec_termination_field classification)" != "killed" ||
      "$(exitrec_termination_field source)" != "supervisor" ]]; then
  echo "an external kill recorded phase=$(exitrec_field phase) evidence=$(exitrec_termination_field classification)/$(exitrec_termination_field source), want Offline killed/supervisor" >&2
  cat "$exitrec_root/doc.json" >&2
  exit 1
fi
if [[ -n "$(exitrec_field paneRef)" ]]; then
  echo "an external kill left the Agent bound to its dead pane" >&2
  exit 1
fi
if ! exitrec_doc_exists pane "$exitrec_external_pane"; then
  echo "an external kill deleted the logical Pane $exitrec_external_pane" >&2
  exit 1
fi
if [[ "$(exitrec_termination_field classification)" != "killed" ||
      "$(exitrec_termination_field source)" != "supervisor" ]]; then
  echo "an external kill Pane evidence is not killed/supervisor" >&2
  cat "$exitrec_root/doc.json" >&2
  exit 1
fi
exitrec_external_before="$(cksum "$exitrec_root/state/projmux/metadata/registry.json" | awk '{print $1, $2}')"
exitrec_reconcile "$exitrec_socket"
exitrec_external_after="$(cksum "$exitrec_root/state/projmux/metadata/registry.json" | awk '{print $1, $2}')"
if [[ "$exitrec_external_before" != "$exitrec_external_after" ]]; then
  echo "an external kill rewrote the registry on a repeat pass" >&2
  exit 1
fi
exitrec_doc agent "$exitrec_external_agent"
echo ">> exit reconciliation external kill agent=$exitrec_external_agent phase=$(exitrec_field phase) class=$(exitrec_termination_field classification)"

# A supervisor killed with SIGKILL writes no receipt at all. The absence must
# converge on `unknown` -- never on `normal`, which would claim a clean exit
# nobody observed -- and the Agent must stay resumable.
printf '%s\n' 'sleep 600' >"$exitrec_root/stub-script"
exitrec_sigkill_agent="$(exitrec_pmx_inside "$exitrec_socket_path" "$exitrec_server_pid" \
  create agent --provider claude --project evidence -o uid)"
exitrec_doc agent "$exitrec_sigkill_agent"
exitrec_sigkill_pane="$(exitrec_field paneRef)"
exitrec_sigkill_pid="$(exitrec_tmux "$exitrec_socket" list-panes -a -F '#{@projmux_pane_uid} #{pane_pid}' \
  | awk -v uid="$exitrec_sigkill_pane" '$1 == uid { print $2; exit }')"
if [[ -z "$exitrec_sigkill_pid" ]]; then
  echo "the supervised Agent Pane reported no pane pid" >&2
  exit 1
fi
kill -KILL "$exitrec_sigkill_pid" 2>/dev/null || true
for _ in $(seq 1 100); do
  exitrec_tmux "$exitrec_socket" list-panes -a -F '#{@projmux_pane_uid}' 2>/dev/null \
    | grep -qx "$exitrec_sigkill_pane" || break
  sleep 0.1
done
exitrec_reconcile "$exitrec_socket"
exitrec_doc agent "$exitrec_sigkill_agent"
if [[ "$(exitrec_field phase)" != "Offline" ]]; then
  echo "a SIGKILLed supervisor left phase=$(exitrec_field phase), want Offline" >&2
  cat "$exitrec_root/doc.json" >&2
  exit 1
fi
if [[ "$(exitrec_termination_field classification)" != "unknown" ]]; then
  echo "a SIGKILLed supervisor was classified $(exitrec_termination_field classification), want unknown" >&2
  cat "$exitrec_root/doc.json" >&2
  exit 1
fi
if [[ "$(exitrec_termination_field source)" != "reconcile" ]]; then
  echo "an unknown receipt came from $(exitrec_termination_field source), want reconcile" >&2
  exit 1
fi
echo ">> exit reconciliation supervisor SIGKILL agent=$exitrec_sigkill_agent class=unknown"

# A shell Pane's runtime loss keeps the logical Pane. Runtime loss is not a
# statement about desired topology, so the resource survives with a
# MissingRuntime condition and the evidence -- offline for a stated reason
# instead of silently absent.
exitrec_shell_pane="$(exitrec_pmx_inside "$exitrec_socket_path" "$exitrec_server_pid" \
  create pane --project evidence -o uid -- sh -c 'sleep 0.5; exit 0')"
for _ in $(seq 1 100); do
  exitrec_tmux "$exitrec_socket" list-panes -a -F '#{@projmux_pane_uid}' 2>/dev/null \
    | grep -qx "$exitrec_shell_pane" || break
  sleep 0.1
done
exitrec_reconcile "$exitrec_socket"
if ! exitrec_doc_exists pane "$exitrec_shell_pane"; then
  echo "a shell Pane's runtime loss deleted the logical Pane $exitrec_shell_pane" >&2
  exit 1
fi
exitrec_doc pane "$exitrec_shell_pane"
if [[ "$(exitrec_termination_field classification)" != "normal" ]]; then
  echo "the shell Pane recorded $(exitrec_termination_field classification), want the supervised normal exit" >&2
  cat "$exitrec_root/doc.json" >&2
  exit 1
fi
exitrec_pmx describe pane "uid:$exitrec_shell_pane" >"$exitrec_root/shell-pane.txt"
smoke_assert_file_contains "$exitrec_root/shell-pane.txt" "MissingRuntime"
smoke_assert_file_contains "$exitrec_root/shell-pane.txt" "Termination:"
echo ">> exit reconciliation shell pane preserved uid=$exitrec_shell_pane"

# The read surfaces project the stored evidence and write nothing. A read that
# advanced a lifecycle would make asking about the state change it.
exitrec_read_before="$(cksum "$exitrec_root/state/projmux/metadata/registry.json" | awk '{print $1, $2}')"
exitrec_pmx get panes --project evidence >"$exitrec_root/get-panes.txt"
exitrec_pmx get agents --project evidence >"$exitrec_root/get-agents.txt"
exitrec_pmx describe agent "uid:$exitrec_sigkill_agent" >"$exitrec_root/describe-agent.txt"
smoke_assert_file_contains "$exitrec_root/get-panes.txt" "TERMINATION"
smoke_assert_file_contains "$exitrec_root/get-agents.txt" "TERMINATION"
smoke_assert_file_contains "$exitrec_root/get-agents.txt" "unknown/reconcile"
smoke_assert_file_contains "$exitrec_root/describe-agent.txt" "Termination:"
smoke_assert_file_contains "$exitrec_root/describe-agent.txt" "TerminationSource:"
smoke_assert_file_contains "$exitrec_root/describe-agent.txt" "TerminationObservedAt:"
exitrec_read_after="$(cksum "$exitrec_root/state/projmux/metadata/registry.json" | awk '{print $1, $2}')"
if [[ "$exitrec_read_before" != "$exitrec_read_after" ]]; then
  echo "a read surface wrote to the registry" >&2
  exit 1
fi
echo ">> exit reconciliation read projection write-free"

# Server loss, then restart, on both host modes.
#
# An unreadable observation must reconcile nothing: it is indistinguishable from
# an empty one, and reading it as empty would file an unknown termination against
# every managed Pane on a machine whose server simply is not up. The restarted
# server then answers honestly-empty, every managed Pane converges on `unknown`,
# and nothing is started to fill the gap.
exitrec_restart_case() {
  local label="$1" socket="$2" socket_path="$3" server_pid="$4" project="$5"
  local pane_uid
  pane_uid="$(exitrec_pmx_inside "$socket_path" "$server_pid" \
    create pane --project "$project" -o uid -- sleep 600)"
  if [[ -z "$pane_uid" ]]; then
    echo "restart case $label created no Pane" >&2
    exit 1
  fi
  exitrec_reconcile "$socket"

  env -u TMUX -u TMUX_PANE "$exitrec_real_tmux" -S "$socket_path" kill-server >/dev/null 2>&1 || true
  # The whole server is gone, so the mirrored-uid observation *fails* rather than
  # returning an empty set, and the lifecycle projection must file nothing.
  #
  # Two things are deliberately not asserted here. The whole registry file does
  # change, because the same pass honestly projects the Project session as no
  # longer live. And evidence may well appear, because `kill-server` SIGHUPs the
  # panes and a supervisor that survives long enough reaps its child and records a
  # real `killed signal=HUP` receipt -- which is the transport working, not the
  # reconciliation guessing. The precise fail-closed property is that nothing with
  # `source: reconcile` was filed against an unreadable host.
  exitrec_pmx reconcile resources --socket "$socket" -o json \
    >"$exitrec_root/reconcile-lost-$label.json" 2>"$exitrec_root/reconcile-lost-$label.err" || true
  if ! exitrec_doc_exists pane "$pane_uid"; then
    echo "restart case $label deleted $pane_uid against an unreadable server" >&2
    exit 1
  fi
  exitrec_doc pane "$pane_uid"
  if [[ "$(exitrec_termination_field source)" == "reconcile" ]]; then
    echo "restart case $label filed reconciler evidence against an unreadable server" >&2
    cat "$exitrec_root/doc.json" >&2
    exit 1
  fi

  # Restart the same socket. The observation is now readable and honestly empty.
  exitrec_tmux "$socket" new-session -d -s "restarted-$label" -n main sleep 600
  exitrec_reconcile "$socket"
  if ! exitrec_doc_exists pane "$pane_uid"; then
    echo "restart case $label deleted the logical Pane $pane_uid" >&2
    exit 1
  fi
  exitrec_doc pane "$pane_uid"
  # A supervisor that got its SIGHUP written down leaves `killed`; one that went
  # with the whole server can leave no receipt at all. A separately completed
  # honestly-empty observation may record `unknown`. Whole-server receipt-free
  # recovery is explicitly outside this slice (kill-window is asserted on its
  # own path), so all three are honest here; normal/intentional are not.
  case "$(exitrec_termination_field classification)" in
    ""|killed|unknown) ;;
    *)
      echo "restart case $label classified the lost server as '$(exitrec_termination_field classification)', want empty, killed, or unknown" >&2
      cat "$exitrec_root/doc.json" >&2
      exit 1
      ;;
  esac
  # No autostart: the restarted server holds only the bare session this smoke
  # made, whose pane carries no mirrored uid. The reconciliation observed a
  # registry full of offline Panes and materialized not one of them -- an
  # observation is not an activation authority.
  if [[ "$(exitrec_tmux "$socket" list-panes -a -F '#{@projmux_pane_uid}' | grep -c . || true)" != "0" ]]; then
    echo "restart case $label materialized panes on the restarted server:" >&2
    exitrec_tmux "$socket" list-panes -a -F '#{pane_id} #{@projmux_pane_uid}' >&2
    exit 1
  fi
  echo ">> exit reconciliation restart $label pane=$pane_uid class=$(exitrec_termination_field classification) no-autostart"
}

# Final arguments remain Registry Project selectors, not tmux session names.
exitrec_restart_case app-owned "$exitrec_socket" "$exitrec_socket_path" \
  "$exitrec_server_pid" evidence
exitrec_restart_case standalone "$exitrec_standalone_socket" "$exitrec_standalone_path" \
  "$exitrec_standalone_pid" standalone

# Sibling containment. The sibling server carries a pane whose mirrored uid is one
# this Registry owns, and reuses the same `%N` handle space. It must be neither
# read nor written, and its identical metadata must not have decided any outcome
# above.
exitrec_sibling_tmux set-option -t sibling -q @projmux_project_path "$exitrec_root/work/evidence"
exitrec_sibling_pane_id="$(exitrec_sibling_tmux list-panes -a -F '#{pane_id}' | head -n 1)"
exitrec_sibling_tmux set-option -p -t "$exitrec_sibling_pane_id" -q @projmux_pane_uid "$exitrec_shell_pane"
exitrec_sibling_before="$(exitrec_sibling_tmux show-options -gqv @projmux_exitrec_sentinel):$(exitrec_sibling_tmux list-panes -a -F '#{pane_id} #{@projmux_pane_uid}')"
exitrec_reconcile "$exitrec_socket"
if [[ "$(exitrec_sibling_tmux show-options -gqv @projmux_exitrec_sentinel):$(exitrec_sibling_tmux list-panes -a -F '#{pane_id} #{@projmux_pane_uid}')" != "$exitrec_sibling_before" ]]; then
  echo "the sibling server changed during exit reconciliation" >&2
  exit 1
fi
exitrec_doc pane "$exitrec_shell_pane"
if [[ "$(exitrec_termination_field classification)" != "normal" ]]; then
  echo "the sibling's identical pane uid changed the shell Pane's evidence to $(exitrec_termination_field classification)" >&2
  exit 1
fi
echo ">> exit reconciliation sibling containment socket=$exitrec_sibling_path unchanged"

exitrec_cleanup
trap integration_cleanup EXIT
echo ">> exit reconciliation root=$exitrec_root sockets=$exitrec_socket_path,$exitrec_standalone_path guard=passed"
