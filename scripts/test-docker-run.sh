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
# Suites are network-isolated by default. Suites that must reach a real
# registry (e.g. the npm update-flow e2e) override this to "bridge".
docker_network="${PROJMUX_TEST_DOCKER_NETWORK:-none}"
# Keep suite builds bounded. The default intentionally contains only the
# build-safe package limit; smoke suites call go build as well as test binaries.
suite_gomaxprocs="${GOMAXPROCS:-2}"
suite_goflags="${GOFLAGS:--p=1}"

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required for this test target" >&2
  exit 127
fi

docker build \
  --pull=false \
  -f "$dockerfile" \
  -t "$image" \
  "$docker_context"

# Suite containers stay network-isolated, so the Go module cache they build
# against must be populated beforehand. The prefetch runs in the same pinned
# image with the network enabled and writes into a host-side cache directory
# that the isolated run then mounts. The go.sum stamp keeps the prefetch a no-op
# once the cache already matches the checked-in module graph.
# Keep the cache outside the repository so repository-wide file scans (gofmt,
# gitleaks working tree) never walk third-party module sources.
modcache="${PROJMUX_TEST_GOMODCACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/projmux/test-gomodcache}"
mkdir -p "$modcache"
stamp="$modcache/.projmux-go-sum"
if ! cmp -s "$root/go.sum" "$stamp"; then
  echo ">> prefetching Go modules into $modcache"
  docker run --rm \
    --network bridge \
    --user "$(id -u):$(id -g)" \
    -e HOME=/tmp/projmux-home \
    -e GOCACHE=/tmp/projmux-gocache \
    -e GOMODCACHE=/gomodcache \
    -e GOTOOLCHAIN=local \
    -v "$root:/workspace:ro" \
    -v "$modcache:/gomodcache:rw" \
    -w /workspace \
    "$image" \
    go mod download
  cp "$root/go.sum" "$stamp"
fi

docker run --rm \
  --network "$docker_network" \
  --user "$(id -u):$(id -g)" \
  -e HOME=/tmp/projmux-home \
  -e XDG_CACHE_HOME=/tmp/projmux-cache \
  -e XDG_CONFIG_HOME=/tmp/projmux-config \
  -e XDG_RUNTIME_DIR=/tmp/projmux-runtime \
  -e XDG_STATE_HOME=/tmp/projmux-state \
  -e GOCACHE=/tmp/projmux-gocache \
  -e GOMODCACHE=/gomodcache \
  -e GOTOOLCHAIN=local \
  -e GOMAXPROCS="$suite_gomaxprocs" \
  -e GOFLAGS="$suite_goflags" \
  -v "$root:/workspace:rw" \
  -v "$modcache:/gomodcache:rw" \
  -w /workspace \
  "$image" \
  bash "$suite" "$@"
