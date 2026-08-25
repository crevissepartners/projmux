#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

cache_root="${PROJMUX_E2E_BUILD_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/projmux/e2e-build-cache}"
mkdir -p "$cache_root"
source_digest="$({
  while IFS= read -r -d '' tracked; do
    sha256sum "$root/$tracked"
  done < <(git -C "$root" ls-files -z -- '*.go' go.mod go.sum test/docker/Dockerfile | sort -z)
  go version
} | sha256sum | awk '{print $1}')"
attempt_root="${PROJMUX_E2E_ARTIFACTS:-$root/.bin/e2e-evidence/attempt-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}-$$}"
binary_dir="$attempt_root/binary"
mkdir -p "$binary_dir"

# The product binary is built exactly once in every attempt. Docker layers,
# modules, and compiler objects may be warm, but no product executable crosses
# the attempt boundary. Every suite/shard receives this one artifact read-only.
PROJMUX_TEST_GOCACHE="$cache_root/go-build" \
  PROJMUX_TEST_GOMODCACHE="$cache_root/go-mod" \
  "$root/scripts/test-docker-run.sh" --build-binary "$binary_dir"
if [[ "$(cat "$binary_dir/build-count")" != "1" ]]; then
  echo "E2E product build count was not exactly one" >&2
  exit 1
fi
binary_sha="$(sha256sum "$binary_dir/projmux" | awk '{print $1}')"
printf '{"binary_sha256":"%s","build_count":1,"cache_scope":"go-module-and-object-only","source_digest":"%s"}\n' \
  "$binary_sha" "$source_digest" >"$attempt_root/build.json"
echo ">> E2E attempt product-build-count=1 sha256=$binary_sha artifacts=$attempt_root"

if [[ "${PROJMUX_E2E_PREPARE_ONLY:-}" == "1" ]]; then
  exit
fi

export PROJMUX_TEST_SKIP_IMAGE_BUILD=1
export PROJMUX_TEST_PREBUILT_BIN="$binary_dir/projmux"
export PROJMUX_TEST_PREBUILT_SHA256="$binary_sha"
export PROJMUX_TEST_GOCACHE="$cache_root/go-build"
export PROJMUX_TEST_GOMODCACHE="$cache_root/go-mod"

# Required, network-isolated e2e suites. Each runs in the same offline Linux
# image (Go + tmux, no registry). Add new offline suites here.
PROJMUX_E2E_ARTIFACTS="$attempt_root/linux" "$root/scripts/test-docker-run.sh" test/e2e/linux-smoke.sh "$@"
PROJMUX_E2E_ARTIFACTS="$attempt_root/codex-lifecycle" "$root/scripts/test-docker-run.sh" test/e2e/codex-lifecycle.sh "$@"
PROJMUX_E2E_ARTIFACTS="$attempt_root/npm-staging" "$root/scripts/test-docker-run.sh" test/e2e/npm-staging-path.sh "$@"
