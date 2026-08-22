#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Required, network-isolated e2e suites. Each runs in the same offline Linux
# image (Go + tmux, no registry). Add new offline suites here.
"$root/scripts/test-docker-run.sh" test/e2e/linux-smoke.sh "$@"
"$root/scripts/test-docker-run.sh" test/e2e/codex-lifecycle.sh "$@"
"$root/scripts/test-docker-run.sh" test/e2e/npm-staging-path.sh "$@"
