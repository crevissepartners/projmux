#!/usr/bin/env bash
set -euo pipefail

# npm update-flow e2e. Unlike the network-isolated smoke suite this one must
# reach the public npm registry, so it uses a Node.js-capable image and a
# bridged network. It depends on the published `projmux` npm package, so it is
# not a required gate; it runs in its own `Update Flow E2E` workflow.
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

export PROJMUX_TEST_IMAGE="${PROJMUX_TEST_IMAGE:-projmux:test-node}"
export PROJMUX_TEST_DOCKERFILE="${PROJMUX_TEST_DOCKERFILE:-$root/test/docker/Dockerfile.node}"
export PROJMUX_TEST_DOCKER_NETWORK="${PROJMUX_TEST_DOCKER_NETWORK:-bridge}"

# A CI run exists to prove the update path, so a skip there must fail rather
# than read as a pass. Local opt-in runs keep skipping cleanly when the
# registry is unreachable; PROJMUX_UPDATE_FLOW_STRICT=1 opts a local run in.
if [[ "${CI:-}" == "true" || "${PROJMUX_UPDATE_FLOW_STRICT:-}" == "1" ]]; then
  set -- --strict "$@"
fi

exec "$root/scripts/test-docker-run.sh" test/e2e/update-flow.sh "$@"
