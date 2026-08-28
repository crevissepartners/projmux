#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
"$root/scripts/test-docker-run.sh" test/integration/linux-smoke.sh "$@"
"$root/scripts/test-docker-run.sh" test/integration/agent-control-binding-frame.sh "$@"
exec "$root/scripts/test-docker-run.sh" test/integration/codex-appserver-topology.sh "$@"
