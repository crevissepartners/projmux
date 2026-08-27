#!/usr/bin/env bash
set -euo pipefail

unset TMUX TMUX_PANE
source "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/test/lib/smoke.sh"

smoke_setup_env
trap smoke_cleanup_env EXIT
cd "$smoke_root"
smoke_build_binary
bin="$PROJMUX_SMOKE_BIN"

topology_root="$PROJMUX_SMOKE_WORKDIR/codex-appserver-topology"
topology_path="$topology_root/path"
topology_empty_path="$topology_root/empty-path"
topology_invocations="$topology_root/codex-invocations"
topology_stderr_sentinel='managed standalone Codex install not found at /private/topology-stderr token=topology-secret prompt=topology-secret'
mkdir -p "$topology_path" "$topology_empty_path"

# The fake CLI reports a missing endpoint by exiting before the websocket
# upgrade. Its output is deliberately hostile; Doctor must not retain it.
cat >"$topology_path/codex" <<'FAKE_CODEX'
#!/bin/bash
set -euo pipefail
printf '%s\n' "$*" >>"${PROJMUX_FAKE_CODEX_INVOCATIONS:?}"
printf '%s\n' 'managed standalone Codex install not found at /private/topology-stderr token=topology-secret prompt=topology-secret' >&2
exit 0
FAKE_CODEX
chmod 0755 "$topology_path/codex"

topology_snapshot() {
  local root="$1" path relative metadata digest
  while IFS= read -r path; do
    relative="${path#"$root"}"
    metadata="$(stat -c '%F|%a|%s' "$path")"
    digest=directory
    if [[ -f "$path" ]]; then
      digest="$(sha256sum "$path" | awk '{print $1}')"
    fi
    printf '%s|%s|%s\n' "$relative" "$metadata" "$digest"
  done < <(find "$root" -print | LC_ALL=C sort)
}

topology_run_doctor() {
  local name="$1" capability="$2" probe_reason="$3" path="$4"
  local codex_home="$topology_root/$name-codex-home"
  local before="$topology_root/$name.before" after="$topology_root/$name.after"
  local output="$topology_root/$name.json" stderr="$topology_root/$name.err"
  mkdir -p "$codex_home"
  printf '%s\n' 'user-state-must-remain-byte-identical' >"$codex_home/user-state-sentinel"
  chmod 0600 "$codex_home/user-state-sentinel"
  if [[ "$capability" == "managed-ready" ]]; then
    mkdir -p "$codex_home/packages/standalone/current/bin"
    printf '%s\n' managed >"$codex_home/packages/standalone/current/bin/codex"
    chmod 0755 "$codex_home/packages/standalone/current/bin/codex"
  fi
  topology_snapshot "$codex_home" >"$before"
  env -u TMUX -u TMUX_PANE \
    CODEX_HOME="$codex_home" \
    PATH="$path" \
    PROJMUX_FAKE_CODEX_INVOCATIONS="$topology_invocations" \
    "$bin" doctor --section integrations --json >"$output" 2>"$stderr"
  topology_snapshot "$codex_home" >"$after"
  if ! cmp -s "$before" "$after"; then
    echo "Doctor changed fake CODEX_HOME for $name" >&2
    diff -u "$before" "$after" >&2 || true
    exit 1
  fi
  smoke_assert_file_contains "$output" '"schema_version": 2'
  smoke_assert_file_contains "$output" '"reason": "hook-unavailable"'
  smoke_assert_file_contains "$output" '"probe_reason": "'"$probe_reason"'"'
  smoke_assert_file_contains "$output" '"install_capability": "'"$capability"'"'
  smoke_assert_file_contains "$output" '"lifecycle_outcome": "not-attempted"'
  smoke_assert_file_contains "$output" '"lifecycle_reason": "read-only"'
  if grep -Fq "$topology_stderr_sentinel" "$output" "$stderr" ||
    grep -Fq '/private/topology-stderr' "$output" "$stderr" ||
    grep -Fq 'token=topology-secret' "$output" "$stderr" ||
    grep -Fq 'prompt=topology-secret' "$output" "$stderr"; then
    echo "Doctor exposed raw fake Codex process output for $name" >&2
    exit 1
  fi
}

: >"$topology_invocations"
topology_run_doctor external-only external-cli-only daemon-not-running "$topology_path"
topology_run_doctor managed managed-ready daemon-not-running "$topology_path"
topology_run_doctor cli-missing cli-missing executable-missing "$topology_empty_path"

if grep -Fq 'daemon start' "$topology_invocations"; then
  echo "read-only topology integration started the daemon" >&2
  cat "$topology_invocations" >&2
  exit 1
fi
if [[ "$(grep -Fxc 'app-server proxy' "$topology_invocations" || true)" != "2" ]]; then
  echo "unexpected read-only proxy invocation count" >&2
  cat "$topology_invocations" >&2
  exit 1
fi

# Settings is interactive, so exercise its production control-plane row through
# the focused package seam that drives the real read-only lifecycle policy.
go test ./internal/app -run '^TestDoctorAndSettingsReadOnlyTopologyNeverStartOrWrite$' -count=1

echo ">> Codex app-server topology integration passed"
