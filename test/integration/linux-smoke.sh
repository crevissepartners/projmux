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
}
integration_cleanup() {
	cleanup_reconcile_socket "${PROJMUX_RECONCILE_PRIMARY_STARTED:-0}" "${PROJMUX_RECONCILE_PRIMARY_SOCKET:-}" "${PROJMUX_RECONCILE_PRIMARY_ACTUAL:-}" "${PROJMUX_RECONCILE_SESSION:-}"
	cleanup_reconcile_socket "${PROJMUX_RECONCILE_SECONDARY_STARTED:-0}" "${PROJMUX_RECONCILE_SECONDARY_SOCKET:-}" "${PROJMUX_RECONCILE_SECONDARY_ACTUAL:-}" "${PROJMUX_RECONCILE_SESSION:-}"
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
		PROJMUX_SMOKE_TMUX_STARTED=0
  fi
  if [[ -n "${PROJMUX_SMOKE_WORKDIR:-}" ]]; then
    rm -rf -- "$PROJMUX_SMOKE_WORKDIR"
  fi
}
trap integration_cleanup EXIT
cd "$smoke_root"

smoke_build_binary
bin="$PROJMUX_SMOKE_BIN"

# The CLI contract must propagate the pane id returned by tmux without scraping
# a second command. Keep this fake-backed check at the built-binary boundary.
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
fake_tmux_output="$(
  PROJMUX_FAKE_MUX_LOG="$fake_tmux_log" \
    PATH="$fake_mux_dir:$PATH" \
    TMUX="fake" \
    TMUX_SPLIT_TARGET_PANE="%7" \
    TMUX_SPLIT_CONTEXT_DIR="$smoke_root" \
    SHELL="/bin/sh" \
    "$bin" create pane -o pane-id --placement right
)"
if [[ "$fake_tmux_output" != "%81" ]]; then
  echo "expected fake tmux pane id %81, got: $fake_tmux_output" >&2
  exit 1
fi
smoke_assert_file_contains "$fake_tmux_log" "split-window -P -F #{pane_id} -h -t %7"

# Generated keybindings enter the saved-default split handler through the
# hidden post-`ai` retirement bridge. Exercise that built-binary boundary so a
# route/catalog refactor cannot leave the popup functional while launch is a
# no-op. Shell mode keeps the fixture independent of provider binaries.
mkdir -p "$XDG_CONFIG_HOME/projmux"
printf 'shell\n' >"$XDG_CONFIG_HOME/projmux/tmux-ai-split-mode"
PROJMUX_FAKE_MUX_LOG="$fake_tmux_log" \
  PATH="$fake_mux_dir:$PATH" \
  TMUX="fake" \
  TMUX_SPLIT_TARGET_PANE="%7" \
  TMUX_SPLIT_CONTEXT_DIR="$smoke_root" \
  SHELL="/bin/sh" \
  "$bin" internal agent-pane launch-default down
smoke_assert_file_contains "$fake_tmux_log" "split-window -v -t %7"
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

blocked_state="$PROJMUX_SMOKE_WORKDIR/blocked-state"
printf 'not-a-directory\n' >"$blocked_state"
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
if [[ "$(cat "$PROJMUX_SMOKE_WORKDIR/pin-add-blocked-log.out")" != "pinned: $smoke_root" ]]; then
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

resource_tmux_snapshot "$PROJMUX_RECONCILE_PRIMARY_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/reconcile-primary.before"
resource_tmux_snapshot "$PROJMUX_RECONCILE_SECONDARY_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/reconcile-secondary.before"
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" reconcile resources --dry-run --socket "$PROJMUX_RECONCILE_PRIMARY_SOCKET" -o json \
  >"$PROJMUX_SMOKE_WORKDIR/reconcile-dry-run.json"
PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" reconcile resources --dry-run --socket "$PROJMUX_RECONCILE_PRIMARY_SOCKET" -o json \
  >"$PROJMUX_SMOKE_WORKDIR/reconcile-dry-run-repeat.json"
cmp "$PROJMUX_SMOKE_WORKDIR/reconcile-dry-run.json" "$PROJMUX_SMOKE_WORKDIR/reconcile-dry-run-repeat.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/reconcile-dry-run.json" '"drift": "missing"'
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/reconcile-dry-run.json" '"tmuxFlag": "-L"'
if [[ -e "$reconcile_state/projmux/metadata/registry.json" ]]; then
  echo "resource reconcile dry-run created a Registry" >&2
  exit 1
fi
resource_tmux_snapshot "$PROJMUX_RECONCILE_PRIMARY_SOCKET" >"$PROJMUX_SMOKE_WORKDIR/reconcile-primary.after-dry-run"
cmp "$PROJMUX_SMOKE_WORKDIR/reconcile-primary.before" "$PROJMUX_SMOKE_WORKDIR/reconcile-primary.after-dry-run"

PROJMUX_PROJDIR="$reconcile_root" XDG_STATE_HOME="$reconcile_state" \
  "$bin" reconcile resources --socket "$PROJMUX_RECONCILE_PRIMARY_SOCKET" -o json \
  >"$PROJMUX_SMOKE_WORKDIR/reconcile-execute.json"
smoke_assert_file_contains "$PROJMUX_SMOKE_WORKDIR/reconcile-execute.json" '"outcome": "changed"'
registry_path="$reconcile_state/projmux/metadata/registry.json"
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

echo ">> lifecycle smoke root=$PROJMUX_SMOKE_WORKDIR actual_socket=$PROJMUX_SMOKE_TMUX_ACTUAL guard=passed"
