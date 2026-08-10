#!/usr/bin/env bash
set -euo pipefail

# npm update-flow e2e. Unlike the network-isolated smoke suite this one must
# reach the public npm registry, so it uses a Node.js-capable image and a
# bridged network. It depends on the published `projmux` npm package, so it is
# opt-in / local rather than a required CI gate.
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

export PROJMUX_TEST_IMAGE="${PROJMUX_TEST_IMAGE:-projmux:test-node}"
export PROJMUX_TEST_DOCKERFILE="${PROJMUX_TEST_DOCKERFILE:-$root/test/docker/Dockerfile.node}"
export PROJMUX_TEST_DOCKER_NETWORK="${PROJMUX_TEST_DOCKER_NETWORK:-bridge}"

exec "$root/scripts/test-docker-run.sh" test/e2e/update-flow.sh "$@"
