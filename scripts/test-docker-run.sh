#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "usage: scripts/test-docker-run.sh <suite-script> [args...] | --build-binary <directory>" >&2
  exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
mode="suite"
suite="$1"
shift
build_output=""
if [[ "$suite" == "--build-binary" ]]; then
  if [[ $# != 1 ]]; then
    echo "usage: scripts/test-docker-run.sh --build-binary <directory>" >&2
    exit 2
  fi
  mode="build"
  build_output="$1"
  shift
fi

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

if [[ "${PROJMUX_TEST_SKIP_IMAGE_BUILD:-}" != "1" ]]; then
  docker build \
    --pull=false \
    -f "$dockerfile" \
    -t "$image" \
    "$docker_context"
fi

# Suite containers stay network-isolated, so the Go module cache they build
# against must be populated beforehand. The prefetch runs in the same pinned
# image with the network enabled and writes into a host-side cache directory
# that the isolated run then mounts. The go.sum stamp keeps the prefetch a no-op
# once the cache already matches the checked-in module graph.
# Keep the cache outside the repository so repository-wide file scans (gofmt,
# gitleaks working tree) never walk third-party module sources.
modcache="${PROJMUX_TEST_GOMODCACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/projmux/test-gomodcache}"
buildcache="${PROJMUX_TEST_GOCACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/projmux/test-gocache}"
mkdir -p "$modcache" "$buildcache"
stamp="$modcache/.projmux-go-sum"
if [[ "${PROJMUX_TEST_SKIP_PREFETCH:-}" == "1" ]]; then
  if ! cmp -s "$root/go.sum" "$stamp"; then
    echo "suite consumer module cache is not prepared for the checked-in graph" >&2
    exit 2
  fi
else
  exec 7>"$modcache/.prefetch.lock"
  flock 7
  if ! cmp -s "$root/go.sum" "$stamp"; then
    echo ">> prefetching Go modules into $modcache"
    docker run --rm \
      --network bridge \
      --user "$(id -u):$(id -g)" \
      -e HOME=/tmp/projmux-home \
      -e GOCACHE=/gocache \
      -e GOMODCACHE=/gomodcache \
      -e GOTOOLCHAIN=local \
      -v "$root:/workspace:ro" \
      -v "$modcache:/gomodcache:rw" \
      -v "$buildcache:/gocache:rw" \
      -w /workspace \
      "$image" \
      go mod download
    cp "$root/go.sum" "$stamp"
  fi
  flock -u 7
fi

if [[ "$mode" == "build" ]]; then
  mkdir -p "$build_output"
  docker run --rm \
    --network "$docker_network" \
    --user "$(id -u):$(id -g)" \
    -e HOME=/tmp/projmux-home \
    -e GOCACHE=/gocache \
    -e GOMODCACHE=/gomodcache \
    -e GOTOOLCHAIN=local \
    -e GOMAXPROCS="$suite_gomaxprocs" \
    -e GOFLAGS="$suite_goflags" \
    -v "$root:/workspace:ro" \
    -v "$modcache:/gomodcache:ro" \
    -v "$buildcache:/gocache:rw" \
    -v "$build_output:/artifact:rw" \
    -w /workspace \
    "$image" \
    bash -ceu 'go build -trimpath -o /artifact/projmux ./cmd/projmux; printf "1\n" > /artifact/build-count'
  chmod 0555 "$build_output/projmux"
  exit
fi

prebuilt="${PROJMUX_TEST_PREBUILT_BIN:-}"
expected_sha="${PROJMUX_TEST_PREBUILT_SHA256:-}"
prebuilt_docker_args=()
if [[ -n "$prebuilt" || -n "$expected_sha" ]]; then
  if [[ -z "$prebuilt" || ! -f "$prebuilt" || -L "$prebuilt" || ! -x "$prebuilt" || -z "$expected_sha" ]]; then
    echo "prebuilt suite run requires PROJMUX_TEST_PREBUILT_BIN regular executable and expected SHA" >&2
    exit 2
  fi
  if [[ "$(sha256sum "$prebuilt" | awk '{print $1}')" != "$expected_sha" ]]; then
    echo "host prebuilt binary hash mismatch" >&2
    exit 2
  fi
  prebuilt_docker_args+=(
    -e PROJMUX_SMOKE_PREBUILT_BIN=/projmux-artifact/projmux
    -e PROJMUX_SMOKE_EXPECTED_BIN_SHA256="$expected_sha"
    -v "$(dirname "$prebuilt"):/projmux-artifact:ro"
  )
fi
evidence="${PROJMUX_E2E_ARTIFACTS:-$root/.bin/e2e-evidence}"
mkdir -p "$evidence"
suite_shell=(bash)
if [[ "${PROJMUX_TEST_BASH_TRACE:-}" == "1" ]]; then
  suite_shell+=(-x)
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
  -e PROJMUX_E2E_ARTIFACTS=/evidence \
  -e PROJMUX_E2E_ATTEMPT="${PROJMUX_E2E_ATTEMPT:-${GITHUB_RUN_ATTEMPT:-1}}" \
  -e PROJMUX_E2E_LINUX_SHARD="${PROJMUX_E2E_LINUX_SHARD:-}" \
  -e PROJMUX_E2E_REGISTRY_STRESS="${PROJMUX_E2E_REGISTRY_STRESS:-}" \
  -e E2E_SCENARIO="${E2E_SCENARIO:-}" \
  -e E2E_WAIT_SCALE="${E2E_WAIT_SCALE:-}" \
  -v "$root:/workspace:ro" \
  -v "$modcache:/gomodcache:ro" \
  -v "$evidence:/evidence:rw" \
  "${prebuilt_docker_args[@]}" \
  -w /workspace \
  "$image" \
  "${suite_shell[@]}" "$suite" "$@"
