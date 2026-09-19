#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
"$root/scripts/test-docker-run.sh" test/integration/linux-smoke.sh "$@"
"$root/scripts/test-docker-run.sh" test/integration/agent-control-binding-frame.sh "$@"
"$root/scripts/test-docker-run.sh" test/integration/claude-endpoint-binding.sh "$@"
"$root/scripts/test-docker-run.sh" test/integration/install-replacement-drain.sh "$@"
"$root/scripts/test-docker-run.sh" test/integration/flag-shaped-window-names.sh "$@"
"$root/scripts/test-docker-run.sh" test/integration/resumed-agent-pane-names.sh "$@"
"$root/scripts/test-docker-run.sh" test/integration/split-cwd-from-pane.sh "$@"
"$root/scripts/test-docker-run.sh" test/integration/cli-window-rename-display.sh "$@"
"$root/scripts/test-docker-run.sh" test/integration/route-guard-identity-cache.sh "$@"
"$root/scripts/test-docker-run.sh" test/integration/lifecycle-outside-tmux.sh "$@"
exec "$root/scripts/test-docker-run.sh" test/integration/codex-appserver-topology.sh "$@"
