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
export PROJMUX_TEST_SKIP_PREFETCH=1

# Required network-isolated suites share the same immutable binary. Linux owns
# four independent mutable fixtures. Local acceptance runs them serially by
# default; explicit parallel mode remains available as the stress profile.
manifest="$root/test/e2e/linux-shards.tsv"
default_order="fixture-1 fixture-2 fixture-3 fixture-4"
expected="L01,L02,L03,L04,L05,L06,L07,L08,L09,L10,L11,L12,L13,L14,L15,L16,L17,L18,L19,C01,N01"
codex_expected="C01"
npm_expected="N01"
run_codex=1
run_npm=1

# The manifest owns which stable IDs live in a shard, so a single-shard run
# derives its exact terminal inventory from the same row the router uses.
shard_expected() {
  awk -F'\t' -v want="$1" '
    $1 == want {
      count = split($2, ids, " ")
      for (index_ = 1; index_ <= count; index_++) {
        printf "%s%s", (index_ > 1 ? "," : ""), ids[index_]
      }
      found = 1
    }
    END { exit found ? 0 : 1 }
  ' "$manifest"
}

# One suite per invocation is what a per-runner CI job needs. The selectors are
# mutually exclusive so a job never silently widens into the full matrix.
suite="${PROJMUX_E2E_SUITE:-}"
if [[ -n "${PROJMUX_E2E_LINUX_SHARD:-}" ]]; then
  if [[ -n "$suite" ]]; then
    echo "E2E selection is exclusive: set PROJMUX_E2E_LINUX_SHARD or PROJMUX_E2E_SUITE, not both" >&2
    exit 2
  fi
  suite="linux-${PROJMUX_E2E_LINUX_SHARD}"
fi
if [[ -n "${E2E_SCENARIO:-}" && -n "$suite" ]]; then
  echo "E2E selection is exclusive: E2E_SCENARIO cannot combine with a suite selector" >&2
  exit 2
fi

if [[ -n "$suite" ]]; then
  run_codex=0
  run_npm=0
  case "$suite" in
    linux-*)
      shard="${suite#linux-}"
      if ! expected="$(shard_expected "$shard")"; then
        echo "unknown Linux shard: $shard" >&2
        exit 2
      fi
      linux_shards=("$shard")
      ;;
    codex-lifecycle) linux_shards=(); run_codex=1; expected="$codex_expected" ;;
    npm-staging) linux_shards=(); run_npm=1; expected="$npm_expected" ;;
    *) echo "unknown E2E suite: $suite" >&2; exit 2 ;;
  esac
  echo ">> E2E suite=$suite expected=$expected"
elif [[ -n "${E2E_SCENARIO:-}" ]]; then
  route="$(python3 "$root/scripts/e2e-evidence.py" route --manifest "$manifest" "$E2E_SCENARIO")"
  route_name="${route%%:*}"
  expected="$E2E_SCENARIO"
  run_codex=0
  run_npm=0
  case "$route_name" in
    linux-*) linux_shards=("${route_name#linux-}") ;;
    codex-lifecycle) linux_shards=(); run_codex=1 ;;
    npm-staging) linux_shards=(); run_npm=1 ;;
    *) echo "unknown E2E replay route: $route_name" >&2; exit 2 ;;
  esac
  echo ">> E2E replay scenario=$E2E_SCENARIO route=$route_name expected=$expected"
else
  read -r -a linux_shards <<<"${PROJMUX_E2E_SHARD_ORDER:-$default_order}"
  mapfile -t ordered_shards < <(printf '%s\n' "${linux_shards[@]}" | sort)
  read -r -a canonical_shards <<<"$default_order"
  mapfile -t canonical_shards < <(printf '%s\n' "${canonical_shards[@]}" | sort)
  if [[ "${#linux_shards[@]}" != "4" || "${ordered_shards[*]}" != "${canonical_shards[*]}" ]]; then
    echo "E2E shard order must contain each known Linux shard exactly once" >&2
    exit 2
  fi
fi
run_linux_shard() {
  local shard="$1"
  PROJMUX_E2E_LINUX_SHARD="$shard" \
    PROJMUX_E2E_ARTIFACTS="$attempt_root/linux-$shard" \
    "$root/scripts/test-docker-run.sh" test/e2e/linux-smoke.sh "$@"
}

linux_mode="${PROJMUX_E2E_LINUX_MODE:-serial}"
if [[ "$linux_mode" == "serial" || "${#linux_shards[@]}" -lt 2 ]]; then
  for shard in "${linux_shards[@]}"; do
    run_linux_shard "$shard" "$@"
  done
else
  shard_pids=()
  for shard in "${linux_shards[@]}"; do
    run_linux_shard "$shard" "$@" >"$attempt_root/linux-$shard.log" 2>&1 &
    shard_pids+=("$!")
  done
  shard_failed=0
  for index in "${!shard_pids[@]}"; do
    if ! wait "${shard_pids[$index]}"; then
      shard_failed=1
      cat "$attempt_root/linux-${linux_shards[$index]}.log" >&2
    fi
  done
  [[ "$shard_failed" == "0" ]] || exit 1
  for shard in "${linux_shards[@]}"; do
    cat "$attempt_root/linux-$shard.log"
  done
fi

if [[ "$run_codex" == "1" ]]; then
  PROJMUX_E2E_ARTIFACTS="$attempt_root/codex-lifecycle" "$root/scripts/test-docker-run.sh" test/e2e/codex-lifecycle.sh "$@"
fi
if [[ "$run_npm" == "1" ]]; then
  PROJMUX_E2E_ARTIFACTS="$attempt_root/npm-staging" "$root/scripts/test-docker-run.sh" test/e2e/npm-staging-path.sh "$@"
fi

result_hash="$(python3 "$root/scripts/e2e-evidence.py" result-hash --expected "$expected" "$attempt_root")"
printf '%s\n' "$result_hash" >"$attempt_root/result.sha256"
echo ">> E2E contract-result-sha256=$result_hash"
