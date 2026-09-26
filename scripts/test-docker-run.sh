#!/usr/bin/env bash
# Runs a test suite (or builds the binary with --build-binary) inside the
# pinned test image, with the checkout mounted read-only at /workspace.
#
# Bind-mount sources are resolved on the docker daemon's host. On a
# Docker-outside-of-Docker runner (the job container talks to the VM's
# docker.sock) the job's checkout path does not exist there, and docker would
# mount an empty directory. A probe container therefore checks once whether
# the daemon sees this checkout with the same content. If it does, the
# checkout is bind-mounted as before; if not, it is copied into a temporary
# docker volume that is removed on exit.
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

# Decide once where /workspace comes from. The probe uses --mount, not -v:
# --mount refuses a missing source instead of creating an empty root-owned
# directory on the daemon host. Hashing a few tracked files, not just testing
# existence, also catches a daemon-side path that exists with other content.
probe_files=(go.mod go.sum scripts/test-docker-run.sh)
local_digest="$(cd "$root" && sha256sum "${probe_files[@]}")"
daemon_digest="$(docker run --rm \
  --network none \
  --user "$(id -u):$(id -g)" \
  --mount "type=bind,source=$root,target=/projmux-probe,readonly" \
  -w /projmux-probe \
  "$image" \
  sha256sum "${probe_files[@]}" 2>/dev/null)" || daemon_digest=""
if [[ -n "$daemon_digest" && "$daemon_digest" == "$local_digest" ]]; then
  workspace_mount=(-v "$root:/workspace:ro")
else
  echo ">> docker daemon cannot see $root; staging the checkout into a docker volume"
  workspace_volume="$(docker volume create --label projmux.test-workspace=1)"
  # The trap only cleans up; bash keeps the script's own exit status.
  trap 'docker volume rm -f "$workspace_volume" >/dev/null 2>&1 || true' EXIT
  # Copy the whole tree, .git included, for parity with the bind mount.
  # Numeric owners keep the files owned by the uid the suite runs as.
  tar -C "$root" --numeric-owner -cf - . |
    docker run --rm -i \
      --network none \
      --user 0:0 \
      -v "$workspace_volume:/workspace" \
      "$image" \
      tar -C /workspace --numeric-owner -xf -
  workspace_mount=(--mount "type=volume,source=$workspace_volume,target=/workspace,readonly")
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
      "${workspace_mount[@]}" \
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
    "${workspace_mount[@]}" \
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

# Suites share the harness-owned compiler cache, so a later suite reuses the
# compile actions an earlier suite already ran instead of rebuilding the binary
# and test binaries from an empty cache. The Go build cache is safe for
# concurrent go invocations. The mount is the one dedicated cache directory,
# outside HOME/XDG and /workspace, so network isolation, the read-only
# workspace, and HOME/XDG isolation are unchanged.
docker run --rm \
  --network "$docker_network" \
  --user "$(id -u):$(id -g)" \
  -e HOME=/tmp/projmux-home \
  -e XDG_CACHE_HOME=/tmp/projmux-cache \
  -e XDG_CONFIG_HOME=/tmp/projmux-config \
  -e XDG_RUNTIME_DIR=/tmp/projmux-runtime \
  -e XDG_STATE_HOME=/tmp/projmux-state \
  -e GOCACHE=/gocache \
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
  "${workspace_mount[@]}" \
  -v "$modcache:/gomodcache:ro" \
  -v "$buildcache:/gocache:rw" \
  -v "$evidence:/evidence:rw" \
  "${prebuilt_docker_args[@]}" \
  -w /workspace \
  "$image" \
  "${suite_shell[@]}" "$suite" "$@"
