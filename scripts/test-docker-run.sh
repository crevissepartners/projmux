#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: scripts/test-docker-run.sh <suite-script> [args...]" >&2
  exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
suite="$1"
shift

image="${PROJMUX_TEST_IMAGE:-projmux:test-linux}"
dockerfile="${PROJMUX_TEST_DOCKERFILE:-$root/test/docker/Dockerfile}"
docker_context="${PROJMUX_TEST_DOCKER_CONTEXT:-$root/test/docker}"

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required for this test target" >&2
  exit 127
fi

docker build \
  --pull=false \
  -f "$dockerfile" \
  -t "$image" \
  "$docker_context"

docker run --rm \
  --network none \
  --user "$(id -u):$(id -g)" \
  -e HOME=/tmp/projmux-home \
  -e XDG_CACHE_HOME=/tmp/projmux-cache \
  -e XDG_CONFIG_HOME=/tmp/projmux-config \
  -e XDG_RUNTIME_DIR=/tmp/projmux-runtime \
  -e XDG_STATE_HOME=/tmp/projmux-state \
  -e GOCACHE=/tmp/projmux-gocache \
  -e GOMODCACHE=/tmp/projmux-gomodcache \
  -e GOTOOLCHAIN=local \
  -v "$root:/workspace:rw" \
  -w /workspace \
  "$image" \
  bash "$suite" "$@"
