#!/usr/bin/env bash
set -euo pipefail

if (($# == 0)); then
  echo "usage: test-e2e-admission.sh <command> [args...]" >&2
  exit 2
fi

# CI already gives every selected shard/suite its own runner. A single-scenario
# replay is similarly below the full-suite capacity boundary. Admitting either
# here would silently turn independent CI jobs into a machine-global queue.
if [[ -n "${PROJMUX_E2E_LINUX_SHARD:-}" ||
  -n "${PROJMUX_E2E_SUITE:-}" ||
  -n "${E2E_SCENARIO:-}" ]]; then
  exec "$@"
fi

case "${PROJMUX_E2E_ADMISSION_BYPASS:-0}" in
  0 | "") ;;
  1)
    echo ">> E2E_ADMISSION state=bypass reason=explicit-stress owner_pid=$$ owner_cwd=$PWD" >&2
    exec "$@"
    ;;
  *)
    echo "invalid PROJMUX_E2E_ADMISSION_BYPASS=${PROJMUX_E2E_ADMISSION_BYPASS}; expected 0 or 1" >&2
    exit 2
    ;;
esac

poll_seconds="${PROJMUX_E2E_ADMISSION_POLL_SECONDS:-0.2}"
if [[ ! "$poll_seconds" =~ ^[0-9]+([.][0-9]+)?$ || "$poll_seconds" == "0" ]]; then
  echo "invalid PROJMUX_E2E_ADMISSION_POLL_SECONDS=$poll_seconds; expected a positive number" >&2
  exit 2
fi

if [[ -n "${PROJMUX_E2E_ADMISSION_STATE_DIR:-}" ]]; then
  state_dir="$PROJMUX_E2E_ADMISSION_STATE_DIR"
elif [[ -n "${XDG_RUNTIME_DIR:-}" ]]; then
  state_dir="$XDG_RUNTIME_DIR/projmux/e2e-admission"
elif [[ -n "${XDG_STATE_HOME:-}" ]]; then
  state_dir="$XDG_STATE_HOME/projmux/e2e-admission"
elif [[ -n "${HOME:-}" ]]; then
  state_dir="$HOME/.local/state/projmux/e2e-admission"
else
  state_dir="${TMPDIR:-/tmp}/projmux-e2e-admission-$(id -u)"
fi
active_state="$state_dir/active"
started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
owner_record="$state_dir/owner-$$-$(date +%s)-${RANDOM}"
acquired=0

umask 077
mkdir -p "$state_dir"
chmod 0700 "$state_dir"
{
  printf 'owner_pid=%s\n' "$$"
  printf 'owner_started=%s\n' "$started_at"
  printf 'owner_cwd=%s\n' "$PWD"
  printf 'owner_record=%s\n' "$owner_record"
  printf 'reason=local-full-suite-capacity\n'
  printf 'capacity=1\n'
} >"$owner_record"

# shellcheck disable=SC2317 # Invoked indirectly by the EXIT trap.
release_admission() {
  if [[ "$acquired" == "1" && -e "$active_state" && "$active_state" -ef "$owner_record" ]]; then
    rm -f "$active_state"
  fi
  rm -f "$owner_record"
}
trap release_admission EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

record_value() {
  local key="$1"
  awk -F= -v key="$key" '
    $1 == key {
      sub(/^[^=]*=/, "")
      print
      exit
    }
  '
}

last_wait_owner=""
while true; do
  if ln "$owner_record" "$active_state" 2>/dev/null; then
    acquired=1
    echo ">> E2E_ADMISSION state=acquired reason=local-full-suite-capacity capacity=1 owner_pid=$$ owner_started=$started_at owner_cwd=$PWD active_state=$active_state" >&2
    break
  fi

  active_record="$(cat "$active_state" 2>/dev/null || true)"
  [[ -n "$active_record" ]] || continue
  active_pid="$(record_value owner_pid <<<"$active_record")"
  active_started="$(record_value owner_started <<<"$active_record")"
  active_cwd="$(record_value owner_cwd <<<"$active_record")"
  active_owner_record="$(record_value owner_record <<<"$active_record")"

  if [[ "$active_pid" =~ ^[1-9][0-9]*$ ]] && ! kill -0 "$active_pid" 2>/dev/null; then
    echo ">> E2E_ADMISSION state=reclaiming reason=stale-owner owner_pid=$active_pid owner_started=${active_started:-unknown} owner_cwd=${active_cwd:-unknown} active_state=$active_state" >&2
    if [[ -n "$active_owner_record" && -e "$active_owner_record" &&
      -e "$active_state" && "$active_state" -ef "$active_owner_record" ]]; then
      rm -f "$active_state" "$active_owner_record"
    fi
    continue
  fi

  wait_owner="${active_pid:-unknown}|${active_started:-unknown}|${active_cwd:-unknown}"
  if [[ "$wait_owner" != "$last_wait_owner" ]]; then
    echo ">> E2E_ADMISSION state=waiting reason=local-full-suite-capacity capacity=1 owner_pid=${active_pid:-unknown} owner_started=${active_started:-unknown} owner_cwd=${active_cwd:-unknown} active_state=$active_state" >&2
    last_wait_owner="$wait_owner"
  fi
  sleep "$poll_seconds"
done

status=0
"$@" || status=$?
exit "$status"
