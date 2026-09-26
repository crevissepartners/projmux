#!/usr/bin/env bash
# Contract for how scripts/test-docker-run.sh provides /workspace. On a host
# docker daemon the checkout is bind-mounted read-only as before. On a
# Docker-outside-of-Docker runner the daemon cannot see the job's checkout
# path, so the script must stage the checkout into a docker volume, mount that
# read-only, and remove it on exit without changing the exit status. A fake
# `docker` on PATH stands in for the daemon, so no real docker is needed.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
failures=0

pass() { echo "PASS $1"; }
fail() {
  echo "FAIL $1" >&2
  failures=1
}

mkdir -p "$workdir/bin"
cat >"$workdir/bin/docker" <<'FAKE'
#!/usr/bin/env bash
# Fake docker: logs argv (one line per call) and emulates the daemon's view.
set -euo pipefail
printf '%s\n' "$*" >>"$FAKE_DOCKER_LOG"
sub="$1"
shift
case "$sub" in
  build) exit 0 ;;
  volume)
    case "$1" in
      create) echo fake-workspace-vol ;;
      rm) ;;
      *) exit 1 ;;
    esac
    exit 0
    ;;
  run) ;;
  *) exit 1 ;;
esac

args=("$@")
bind_source="" bind_target="" workdir="" artifact="" interactive=0 suite=0 cmd_at=-1
for ((i = 0; i < ${#args[@]}; i++)); do
  a="${args[i]}"
  case "$a" in
    --mount)
      m="${args[i + 1]}"
      if [[ "$m" == type=bind,* ]]; then
        bind_source="${m#*source=}"
        bind_source="${bind_source%%,*}"
        bind_target="${m#*target=}"
        bind_target="${bind_target%%,*}"
      fi
      ;;
    -w) workdir="${args[i + 1]}" ;;
    -i) interactive=1 ;;
    -v)
      case "${args[i + 1]}" in
        *:/artifact:rw) artifact="${args[i + 1]%:/artifact:rw}" ;;
        *:/evidence:rw) suite=1 ;;
      esac
      ;;
    sha256sum) cmd_at=$i ;;
  esac
done

if [[ -n "$bind_source" ]]; then
  visible=0
  IFS=: read -r -a paths <<<"${FAKE_DAEMON_VISIBLE:-}"
  for p in "${paths[@]}"; do
    [[ -n "$p" && "$p" == "$bind_source" ]] && visible=1
  done
  if [[ "$visible" != 1 ]]; then
    echo "docker: Error response from daemon: invalid mount config for type \"bind\": bind source path does not exist: $bind_source" >&2
    exit 125
  fi
  dir="$bind_source${workdir#"$bind_target"}"
  out="$(cd "$dir" && sha256sum "${args[@]:cmd_at+1}")"
  if [[ "${FAKE_DAEMON_STALE:-}" == 1 ]]; then
    out="$(sed 's/^[0-9a-f]*/0000/' <<<"$out")"
  fi
  printf '%s\n' "$out"
  exit 0
fi
if [[ "$interactive" == 1 ]]; then
  cat >/dev/null
  exit 0
fi
if [[ -n "$artifact" ]]; then
  : >"$artifact/projmux"
  exit 0
fi
if [[ "$suite" == 1 ]]; then
  exit "${FAKE_SUITE_EXIT:-0}"
fi
exit 0
FAKE
chmod +x "$workdir/bin/docker"

# make_checkout DIR creates the files the script and its probe read.
make_checkout() {
  mkdir -p "$1/scripts"
  cp "$root/scripts/test-docker-run.sh" "$1/scripts/"
  cp "$root/go.mod" "$root/go.sum" "$1/"
}

# run_case LABEL CHECKOUT VISIBLE [ARGS...] runs the script from CHECKOUT with
# fresh caches; VISIBLE is the daemon-visible path list. It sets $status and
# leaves the call log in $workdir/LABEL.log.
run_case() {
  local label="$1" checkout="$2" visible="$3"
  shift 3
  local cases="$workdir/cases/$label"
  mkdir -p "$cases"
  : >"$workdir/$label.log"
  status=0
  env PATH="$workdir/bin:$PATH" \
    FAKE_DOCKER_LOG="$workdir/$label.log" \
    FAKE_DAEMON_VISIBLE="$visible" \
    PROJMUX_TEST_SKIP_IMAGE_BUILD=1 \
    PROJMUX_TEST_GOMODCACHE="$cases/gomodcache" \
    PROJMUX_TEST_GOCACHE="$cases/gocache" \
    PROJMUX_E2E_ARTIFACTS="$cases/evidence" \
    bash "$checkout/scripts/test-docker-run.sh" "$@" \
    >"$workdir/$label.out" 2>"$workdir/$label.err" || status=$?
}

# line LABEL PATTERN prints the logged calls containing PATTERN.
line() { grep -F -- "$2" "$workdir/$1.log" || true; }
count() { grep -c -F -- "$2" "$workdir/$1.log" || true; }

vol_mount='--mount type=volume,source=fake-workspace-vol,target=/workspace,readonly'

# expect_bind LABEL CHECKOUT: no staging; prefetch and suite bind the checkout.
expect_bind() {
  local label="$1" checkout="$2" ok=1 kind
  if [[ "$status" != 0 ]]; then
    fail "$label: exit $status"
    cat "$workdir/$label.err" >&2
    return
  fi
  [[ "$(count "$label" 'volume create')" == 0 ]] || ok=0
  [[ "$(count "$label" 'tar -C /workspace')" == 0 ]] || ok=0
  if [[ "$ok" == 1 ]]; then
    pass "$label: no volume created, no copy"
  else
    fail "$label: staged although the daemon sees the checkout"
  fi
  for kind in 'go mod download' '/evidence:rw'; do
    if [[ "$(line "$label" "$kind")" == *"-v $checkout:/workspace:ro "* ]]; then
      pass "$label: '$kind' run mounts -v $checkout:/workspace:ro"
    else
      fail "$label: '$kind' run does not bind the checkout: $(line "$label" "$kind")"
    fi
  done
}

# expect_staged LABEL CHECKOUT RUNKIND...: one volume, one copy, every RUNKIND
# run mounts the volume read-only, the checkout is never bound at /workspace,
# the volume is removed last, and the reason line is printed.
expect_staged() {
  local label="$1" checkout="$2" kind
  shift 2
  if [[ "$(count "$label" 'volume create')" == 1 && "$(count "$label" 'tar -C /workspace --numeric-owner -xf -')" == 1 ]]; then
    pass "$label: one volume created, one tar copy"
  else
    fail "$label: expected one volume create and one copy"
    cat "$workdir/$label.log" >&2
  fi
  for kind in "$@"; do
    if [[ "$(line "$label" "$kind")" == *"$vol_mount"* ]]; then
      pass "$label: '$kind' run mounts the volume read-only at /workspace"
    else
      fail "$label: '$kind' run does not mount the volume: $(line "$label" "$kind")"
    fi
  done
  if [[ "$(count "$label" "$checkout:/workspace")" == 0 ]]; then
    pass "$label: checkout never bind-mounted at /workspace"
  else
    fail "$label: checkout bind-mounted at /workspace"
  fi
  if [[ "$(tail -n 1 "$workdir/$label.log")" == 'volume rm -f fake-workspace-vol' ]]; then
    pass "$label: volume removed at exit"
  else
    fail "$label: volume not removed last"
    cat "$workdir/$label.log" >&2
  fi
  if grep -q -F ">> docker daemon cannot see $checkout; staging the checkout into a docker volume" "$workdir/$label.out"; then
    pass "$label: reason line printed"
  else
    fail "$label: reason line missing"
  fi
}

# a. Host docker: the daemon sees the checkout path with the same content.
host="$workdir/host"
make_checkout "$host"
run_case host "$host" "$host" test/suite.sh
expect_bind host "$host"

# b. DooD: the daemon cannot see the job's checkout path.
dood="$workdir/dood"
make_checkout "$dood"
run_case dood "$dood" "" test/suite.sh
if [[ "$status" == 0 ]]; then
  pass "dood: exit 0"
else
  fail "dood: exit $status"
  cat "$workdir/dood.err" >&2
fi
expect_staged dood "$dood" 'go mod download' '/evidence:rw'

# c. A wrapper already copied the checkout to a path the daemon sees with the
# same content, and runs the script from there: no second staging.
staged="$workdir/shared/staged-checkout"
mkdir -p "$workdir/shared"
cp -a "$dood" "$staged"
run_case already-staged "$staged" "$staged" test/suite.sh
expect_bind already-staged "$staged"

# d. The daemon has the same path but different content: stage.
stale="$workdir/stale"
make_checkout "$stale"
FAKE_DAEMON_STALE=1 run_case stale "$stale" "$stale" test/suite.sh
expect_staged stale "$stale" 'go mod download' '/evidence:rw'

# e. --build-binary in DooD builds from the volume.
build="$workdir/build"
make_checkout "$build"
run_case build "$build" "" --build-binary "$workdir/cases/build/artifact"
if [[ "$status" == 0 && -x "$workdir/cases/build/artifact/projmux" ]]; then
  pass "build: exit 0, artifact made executable"
else
  fail "build: exit $status"
  cat "$workdir/build.err" >&2
fi
expect_staged build "$build" 'go mod download' '/artifact/projmux'

# f. A failing suite keeps its exit status through the cleanup trap.
failing="$workdir/failing"
make_checkout "$failing"
FAKE_SUITE_EXIT=7 run_case failing "$failing" "" test/suite.sh
if [[ "$status" == 7 ]]; then
  pass "failing: suite exit status 7 preserved"
else
  fail "failing: exit $status, expected 7"
fi
expect_staged failing "$failing" '/evidence:rw'

exit "$failures"
