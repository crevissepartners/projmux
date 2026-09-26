#!/usr/bin/env bash
# Contract for how scripts/test-docker-run.sh provides /workspace. On a host
# docker daemon the checkout is bind-mounted read-only as before. On a
# Docker-outside-of-Docker runner the daemon cannot see the job's checkout
# path, so the script must stage the checkout into a docker volume, mount that
# read-only, and remove it on exit without changing the exit status. There the
# build output, the prebuilt binary, and the evidence also travel through
# volumes, and outputs are copied back to the job. A staging run first removes
# labelled volumes a killed run left behind once they pass the age threshold,
# never one still in use or carrying another label. A fake `docker` on PATH
# stands in for the daemon, so no real docker is needed; a failing `go` stub
# pins that scripts/test-e2e-docker.sh needs no host Go toolchain.
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

# running PID succeeds while PID has not ended. An exited but unreaped process
# (state Z in /proc/PID/stat) has ended; without /proc, kill -0 decides.
# Kept in step with running() in the fake docker below.
running() {
  local stat
  kill -0 "$1" 2>/dev/null || return 1
  stat="$(cat "/proc/$1/stat" 2>/dev/null)" || return 0
  stat="${stat##*) }"
  [[ "${stat%% *}" != Z ]]
}

mkdir -p "$workdir/bin"
cat >"$workdir/bin/docker" <<'FAKE'
#!/usr/bin/env bash
# Fake docker: logs argv (one line per call) and emulates the daemon's view.
# The daemon's disk is $FAKE_DAEMON_ROOT: volumes live under volumes/, and a
# bind source the daemon cannot see becomes an empty directory under phantom/,
# as real docker creates one on its own host. A bind source the daemon sees
# (equal to or under a FAKE_DAEMON_VISIBLE path) is the job's own directory.
set -euo pipefail
printf '%s\n' "$*" >>"$FAKE_DOCKER_LOG"
sub="$1"
shift
# Volume metadata lives under meta/: NAME.label (k=v), NAME.created (the
# CreatedAt docker prints), NAME.inuse (a container id a test pins the volume
# with), and NAME.run.PID while a fake `docker run` with PID mounts it.
volumes="$FAKE_DAEMON_ROOT/volumes"
meta="$FAKE_DAEMON_ROOT/meta"
mkdir -p "$volumes" "$meta"
# running PID succeeds while PID has not ended. An exited but unreaped process
# (state Z in /proc/PID/stat) has ended; without /proc, kill -0 decides.
# Kept in step with running() in the contract script below.
running() {
  local stat
  kill -0 "$1" 2>/dev/null || return 1
  stat="$(cat "/proc/$1/stat" 2>/dev/null)" || return 0
  stat="${stat##*) }"
  [[ "${stat%% *}" != Z ]]
}
# in_use NAME prints the container holding NAME and succeeds when one does.
in_use() {
  local f
  if [[ -f "$meta/$1.inuse" ]]; then
    cat "$meta/$1.inuse"
    return 0
  fi
  for f in "$meta/$1".run.*; do
    [[ -e "$f" ]] || continue
    if running "${f##*.run.}"; then
      printf 'fake-run-%s\n' "${f##*.run.}"
      return 0
    fi
  done
  return 1
}
case "$sub" in
  build) exit 0 ;;
  volume)
    case "$1" in
      create)
        shift
        n=$(($(find "$FAKE_DAEMON_ROOT" -maxdepth 1 -name 'created-*' | wc -l) + 1))
        : >"$FAKE_DAEMON_ROOT/created-$n"
        mkdir "$volumes/fake-vol-$n"
        [[ "${1:-}" == --label ]] && printf '%s\n' "$2" >"$meta/fake-vol-$n.label"
        date -u +%Y-%m-%dT%H:%M:%SZ >"$meta/fake-vol-$n.created"
        echo "fake-vol-$n"
        ;;
      ls)
        [[ "$2" == -q && "$3" == --filter && "$4" == label=* ]] || exit 1
        [[ -z "${FAKE_VOLUME_LS_EXIT:-}" ]] || exit "$FAKE_VOLUME_LS_EXIT"
        for d in "$volumes"/*; do
          [[ -d "$d" ]] || continue
          v="${d##*/}"
          if [[ -f "$meta/$v.label" && "$(cat "$meta/$v.label")" == "${4#label=}" ]]; then
            echo "$v"
          fi
        done
        ;;
      inspect)
        [[ "$2" == --format && "$3" == '{{.CreatedAt}}' && -d "$volumes/$4" ]] || {
          echo "Error response from daemon: get $4: no such volume" >&2
          exit 1
        }
        cat "$meta/$4.created"
        ;;
      rm)
        shift
        if [[ "${1:-}" == -f ]]; then
          shift
          for v in "$@"; do rm -rf "${volumes:?}/$v" "$meta/$v".*; done
          exit 0
        fi
        # Without -f docker refuses a volume a container holds.
        rc=0
        for v in "$@"; do
          if [[ ! -d "$volumes/$v" ]]; then
            echo "Error response from daemon: get $v: no such volume" >&2
            rc=1
          elif holder="$(in_use "$v")"; then
            echo "Error response from daemon: remove $v: volume is in use - [$holder]" >&2
            rc=1
          else
            rm -rf "${volumes:?}/$v" "$meta/$v".*
            echo "$v"
          fi
        done
        exit "$rc"
        ;;
      *) exit 1 ;;
    esac
    exit 0
    ;;
  run) ;;
  *) exit 1 ;;
esac

visible() {
  local p
  IFS=: read -r -a paths <<<"${FAKE_DAEMON_VISIBLE:-}"
  for p in "${paths[@]}"; do
    [[ -n "$p" && ("$1" == "$p" || "$1" == "$p"/*) ]] && return 0
  done
  return 1
}

# Mount targets and the fake daemon directories behind them, in order.
targets=() dirs=() envs=() workdir="" bind_probe=0
add_mount() { # KIND SOURCE TARGET
  local dir
  if [[ "$1" == volume ]]; then
    dir="$volumes/$2"
    [[ -d "$dir" ]] || { echo "docker: no such volume: $2" >&2; exit 125; }
    : >"$meta/$2.run.$$"
    trap 'rm -f "$meta"/*.run.$$' EXIT
  elif visible "$2"; then
    dir="$2"
  elif [[ "$1" == mount-bind ]]; then
    echo "docker: Error response from daemon: invalid mount config for type \"bind\": bind source path does not exist: $2" >&2
    exit 125
  else
    dir="$FAKE_DAEMON_ROOT/phantom$2"
    mkdir -p "$dir"
  fi
  targets+=("$3")
  dirs+=("$dir")
}
args=("$@")
i=0
while ((i < ${#args[@]})); do
  a="${args[i]}"
  case "$a" in
    --rm | -i) i=$((i + 1)) ;;
    --network | --user) i=$((i + 2)) ;;
    -e) envs+=("${args[i + 1]}"); i=$((i + 2)) ;;
    -w) workdir="${args[i + 1]}"; i=$((i + 2)) ;;
    -v)
      IFS=: read -r src tgt _ <<<"${args[i + 1]}"
      if [[ "$src" == /* ]]; then add_mount bind "$src" "$tgt"; else add_mount volume "$src" "$tgt"; fi
      i=$((i + 2))
      ;;
    --mount)
      m="${args[i + 1]}"
      src="${m#*source=}" && src="${src%%,*}"
      tgt="${m#*target=}" && tgt="${tgt%%,*}"
      case "$m" in
        type=bind,*) add_mount mount-bind "$src" "$tgt"; bind_probe=1 ;;
        type=volume,*) add_mount volume "$src" "$tgt" ;;
      esac
      i=$((i + 2))
      ;;
    -*) echo "fake docker: unknown option $a" >&2; exit 1 ;;
    *) break ;;
  esac
done
cmd=("${args[@]:i+1}")

# map PATH prints the fake daemon directory behind a container path.
map() {
  local k
  for k in "${!targets[@]}"; do
    if [[ "$1" == "${targets[k]}" || "$1" == "${targets[k]}"/* ]]; then
      printf '%s\n' "${dirs[k]}${1#"${targets[k]}"}"
      return 0
    fi
  done
  echo "fake docker: $1 is not mounted" >&2
  exit 1
}

case "${cmd[0]}" in
  sha256sum)
    out="$(cd "$(map "$workdir")" && sha256sum "${cmd[@]:1}")"
    if [[ "$bind_probe" == 1 && "${FAKE_DAEMON_STALE:-}" == 1 ]]; then
      out="$(sed 's/^[0-9a-f]*/0000/' <<<"$out")"
    fi
    printf '%s\n' "$out"
    ;;
  tar)
    # tar -C PATH ...: run the real tar against the mapped directory.
    [[ "${cmd[1]}" == -C ]] || exit 1
    if [[ " ${cmd[*]} " == *" -cf "* && -n "${FAKE_COPY_BACK_EXIT:-}" ]]; then
      exit "$FAKE_COPY_BACK_EXIT"
    fi
    tar -C "$(map "${cmd[2]}")" "${cmd[@]:3}"
    ;;
  go) ;;
  bash)
    if [[ "${cmd[*]}" == *"/artifact/projmux"* ]]; then
      artifact="$(map /artifact)"
      printf '#!/bin/sh\n' >"$artifact/projmux"
      chmod +x "$artifact/projmux"
      printf '1\n' >"$artifact/build-count"
      exit 0
    fi
    # A suite: record evidence, require the prebuilt binary when passed one.
    printf 'suite ran\n' >"$(map /evidence)/fake-suite.evidence"
    # FAKE_SUITE_BLOCK=DIR: publish this pid in DIR/pid and block, bounded,
    # until DIR/release exists, like a suite container still running.
    if [[ -n "${FAKE_SUITE_BLOCK:-}" ]]; then
      echo "$$" >"$FAKE_SUITE_BLOCK/pid"
      for ((t = 0; t < 300; t++)); do
        [[ -e "$FAKE_SUITE_BLOCK/release" ]] && break
        sleep 0.1
      done
    fi
    for e in "${envs[@]}"; do
      if [[ "$e" == PROJMUX_SMOKE_PREBUILT_BIN=* && ! -x "$(map "${e#*=}")" ]]; then
        echo "fake suite: ${e#*=} missing" >&2
        exit 3
      fi
    done
    exit "${FAKE_SUITE_EXIT:-0}"
    ;;
  *) exit 1 ;;
esac
FAKE
chmod +x "$workdir/bin/docker"
# A host Go toolchain must never be needed: the stub logs any call and fails.
cat >"$workdir/bin/go" <<'FAKE'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FAKE_GO_LOG"
exit 127
FAKE
chmod +x "$workdir/bin/go"

# make_checkout DIR creates the files the script and its probe read, plus what
# scripts/test-e2e-docker.sh hashes, as a git checkout.
make_checkout() {
  mkdir -p "$1/scripts" "$1/test/docker"
  cp "$root/scripts/test-docker-run.sh" "$root/scripts/test-e2e-docker.sh" "$1/scripts/"
  cp "$root/test/docker/Dockerfile" "$1/test/docker/"
  cp "$root/go.mod" "$root/go.sum" "$1/"
  git -C "$1" init -q
  git -C "$1" add -A
}

# run_case LABEL CHECKOUT VISIBLE [ARGS...] runs the script from CHECKOUT with
# fresh caches and a fresh fake daemon; VISIBLE is the daemon-visible path
# list. SCRIPT (default scripts/test-docker-run.sh) picks the entrypoint;
# DAEMON_CASE reuses another case's fake daemon and caches. It sets $status and
# leaves the call log in $workdir/LABEL.log.
run_case() {
  local label="$1" checkout="$2" visible="$3"
  shift 3
  local cases="$workdir/cases/${DAEMON_CASE:-$label}"
  mkdir -p "$cases/daemon"
  : >"$workdir/$label.log"
  : >"$workdir/$label.go.log"
  status=0
  env PATH="$workdir/bin:$PATH" \
    FAKE_DOCKER_LOG="$workdir/$label.log" \
    FAKE_GO_LOG="$workdir/$label.go.log" \
    FAKE_DAEMON_ROOT="$cases/daemon" \
    FAKE_DAEMON_VISIBLE="$visible" \
    PROJMUX_TEST_SKIP_IMAGE_BUILD=1 \
    PROJMUX_TEST_GOMODCACHE="$cases/gomodcache" \
    PROJMUX_TEST_GOCACHE="$cases/gocache" \
    PROJMUX_E2E_ARTIFACTS="$cases/evidence" \
    PROJMUX_E2E_BUILD_CACHE="$cases/e2e-build-cache" \
    bash "$checkout/${SCRIPT:-scripts/test-docker-run.sh}" "$@" \
    >"$workdir/$label.out" 2>"$workdir/$label.err" || status=$?
}

# line LABEL PATTERN prints the logged calls containing PATTERN.
line() { grep -F -- "$2" "$workdir/$1.log" || true; }
count() { grep -c -F -- "$2" "$workdir/$1.log" || true; }

# check LABEL MESSAGE COMMAND... passes when COMMAND succeeds.
check() {
  local label="$1" message="$2"
  shift 2
  if "$@"; then
    pass "$label: $message"
  else
    fail "$label: $message"
  fi
}

# check_has LABEL MESSAGE PATTERN NEEDLE: a logged call containing PATTERN
# contains NEEDLE.
check_has() {
  if [[ "$(line "$1" "$3")" == *"$4"* ]]; then
    pass "$1: $2"
  else
    fail "$1: $2: $(line "$1" "$3")"
  fi
}

# check_never LABEL MESSAGE NEEDLE: no logged call contains NEEDLE.
check_never() {
  if [[ "$(count "$1" "$3")" == 0 ]]; then
    pass "$1: $2"
  else
    fail "$1: $2"
  fi
}

# expect_status LABEL WANT
expect_status() {
  if [[ "$status" == "$2" ]]; then
    pass "$1: exit $2"
  else
    fail "$1: exit $status, expected $2"
    cat "$workdir/$1.err" >&2
  fi
}

# The workspace is always the first volume a staged run creates.
vol_mount='--mount type=volume,source=fake-vol-1,target=/workspace,readonly'

# expect_bind LABEL CHECKOUT: no staging; prefetch and suite bind the checkout.
expect_bind() {
  local label="$1" checkout="$2" ok=1 kind
  if [[ "$status" != 0 ]]; then
    fail "$label: exit $status"
    cat "$workdir/$label.err" >&2
    return
  fi
  [[ "$(count "$label" 'volume ')" == 0 ]] || ok=0
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

# expect_staged LABEL CHECKOUT VOLUMES RUNKIND...: VOLUMES labelled volumes,
# one workspace copy, every RUNKIND run mounts the workspace volume read-only,
# the checkout is never bound at /workspace, every volume is removed last and
# none is left behind, and the reason line is printed.
expect_staged() {
  local label="$1" checkout="$2" volumes="$3" kind n names=""
  shift 3
  if [[ "$(count "$label" 'volume create --label projmux.test-workspace=1')" == "$volumes" && "$(count "$label" 'tar -C /workspace --numeric-owner -xf -')" == 1 ]]; then
    pass "$label: $volumes labelled volumes created, one workspace tar copy"
  else
    fail "$label: expected $volumes volume creates and one workspace copy"
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
  for ((n = 1; n <= volumes; n++)); do names+=" fake-vol-$n"; done
  if [[ "$(tail -n 1 "$workdir/$label.log")" == "volume rm -f$names" && -z "$(ls -A "$workdir/cases/$label/daemon/volumes")" ]]; then
    pass "$label: every volume removed at exit"
  else
    fail "$label: volumes not removed last"
    cat "$workdir/$label.log" >&2
  fi
  if grep -q -F ">> docker daemon cannot see $checkout; staging the checkout into a docker volume" "$workdir/$label.out"; then
    pass "$label: reason line printed"
  else
    fail "$label: reason line missing"
  fi
}

# expect_evidence LABEL: the suite's evidence reached the job's directory.
expect_evidence() {
  check "$1" "suite evidence visible job-side" \
    test -f "$workdir/cases/$1/evidence/fake-suite.evidence"
}

# A prebuilt binary for suite runs, passed as scripts/test-e2e-docker.sh does.
prebuilt_dir="$workdir/prebuilt"
mkdir -p "$prebuilt_dir"
printf '#!/bin/sh\n' >"$prebuilt_dir/projmux"
chmod 0555 "$prebuilt_dir/projmux"
prebuilt_sha="$(sha256sum "$prebuilt_dir/projmux" | awk '{print $1}')"
with_prebuilt() {
  PROJMUX_TEST_PREBUILT_BIN="$prebuilt_dir/projmux" PROJMUX_TEST_PREBUILT_SHA256="$prebuilt_sha" "$@"
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
expect_status dood 0
expect_staged dood "$dood" 2 'go mod download' 'test/suite.sh'
expect_evidence dood

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
expect_staged stale "$stale" 2 'go mod download' 'test/suite.sh'

# e. --build-binary in DooD builds from the workspace volume into an artifact
# volume and copies the result back into the job's directory.
build="$workdir/build"
make_checkout "$build"
artifact="$workdir/cases/build/artifact"
run_case build "$build" "" --build-binary "$artifact"
expect_status build 0
expect_staged build "$build" 2 'go mod download' '/artifact/projmux'
check_has build "artifact volume mounted at /artifact" \
  /artifact/projmux "--mount type=volume,source=fake-vol-2,target=/artifact "
check_never build "output never bind-mounted" "$artifact:"
check build "job-side build-count is 1" grep -qx 1 "$artifact/build-count"
check build "job-side binary is executable" test -x "$artifact/projmux"

# f. A failing suite keeps its exit status through the cleanup trap.
failing="$workdir/failing"
make_checkout "$failing"
FAKE_SUITE_EXIT=7 run_case failing "$failing" "" test/suite.sh
expect_status failing 7
expect_staged failing "$failing" 2 'test/suite.sh'

# g. Host docker keeps binding every input and output: no volumes at all.
host_out="$workdir/host-outputs"
make_checkout "$host_out"
run_case host-build "$host_out" "$workdir" --build-binary "$workdir/cases/host-build/artifact"
expect_status host-build 0
check_has host-build "output bound at /artifact" \
  /artifact/projmux "-v $workdir/cases/host-build/artifact:/artifact:rw "
check_never host-build "no volume calls" "volume "
check host-build "job-side binary is executable" test -x "$workdir/cases/host-build/artifact/projmux"
with_prebuilt run_case host-suite "$host_out" "$workdir" test/suite.sh
expect_status host-suite 0
check_has host-suite "evidence bound at /evidence" \
  test/suite.sh "-v $workdir/cases/host-suite/evidence:/evidence:rw "
check_has host-suite "prebuilt bound at /projmux-artifact" \
  test/suite.sh "-v $prebuilt_dir:/projmux-artifact:ro "
check_never host-suite "no volume calls" "volume "
expect_evidence host-suite

# h. DooD suite with a prebuilt binary: the binary travels in a volume, and
# the evidence comes back to the job.
dood_pre="$workdir/dood-prebuilt"
make_checkout "$dood_pre"
with_prebuilt run_case dood-prebuilt "$dood_pre" "" test/suite.sh
expect_status dood-prebuilt 0
expect_staged dood-prebuilt "$dood_pre" 3 'test/suite.sh'
check_has dood-prebuilt "prebuilt mounted read-only from a volume" \
  test/suite.sh "--mount type=volume,source=fake-vol-2,target=/projmux-artifact,readonly "
check_never dood-prebuilt "prebuilt never bind-mounted" "$prebuilt_dir:"
check_has dood-prebuilt "evidence mounted from a volume" \
  test/suite.sh "--mount type=volume,source=fake-vol-3,target=/evidence "
expect_evidence dood-prebuilt

# i. A failing DooD suite keeps status 7 and still returns its evidence.
FAKE_SUITE_EXIT=7 with_prebuilt run_case dood-prebuilt-failing "$dood_pre" "" test/suite.sh
expect_status dood-prebuilt-failing 7
expect_staged dood-prebuilt-failing "$dood_pre" 3 'test/suite.sh'
expect_evidence dood-prebuilt-failing

# j. A failed evidence copy-back fails an otherwise green suite run.
FAKE_COPY_BACK_EXIT=5 run_case copy-back-failing "$dood" "" test/suite.sh
check copy-back-failing "exit status is non-zero" test "$status" != 0

# k. scripts/test-e2e-docker.sh prepares one attempt without a host Go
# toolchain, on a host daemon and on DooD.
check_build_json() {
  python3 -c '
import json, re, sys
build = json.load(open(sys.argv[1]))
assert build["build_count"] == 1, build
assert re.fullmatch(r"[0-9a-f]{64}", build["source_digest"]), build
assert build["binary_sha256"] == sys.argv[2], build
' "$1/build.json" "$(sha256sum "$1/binary/projmux" | awk '{print $1}')"
}
for mode in host dood; do
  label="e2e-prepare-$mode"
  checkout="$workdir/$label"
  make_checkout "$checkout"
  visible="$workdir"
  [[ "$mode" == dood ]] && visible=""
  SCRIPT=scripts/test-e2e-docker.sh PROJMUX_E2E_PREPARE_ONLY=1 run_case "$label" "$checkout" "$visible"
  expect_status "$label" 0
  check "$label" "host go never called" test ! -s "$workdir/$label.go.log"
  if check_build_json "$workdir/cases/$label/evidence"; then
    pass "$label: build.json has build_count 1, a 64-hex source_digest, the job-side binary sha"
  else
    fail "$label: build.json missing or wrong"
  fi
done

# Leftover volumes. seed_volume CASE NAME LABEL CREATED puts a volume an
# earlier run left behind on CASE's fake daemon; an empty LABEL means none.
seed_volume() {
  local daemon="$workdir/cases/$1/daemon"
  mkdir -p "$daemon/volumes/$2" "$daemon/meta"
  [[ -z "$3" ]] || printf '%s\n' "$3" >"$daemon/meta/$2.label"
  printf '%s\n' "$4" >"$daemon/meta/$2.created"
}
# ago SECONDS prints an RFC3339 CreatedAt that many seconds in the past.
ago() { date -u -d "@$(($(date +%s) - $1))" +%Y-%m-%dT%H:%M:%SZ; }
removed_line() { printf '>> removed test workspace volume %s left behind by an earlier run' "$1"; }
# expect_removed / expect_kept LABEL NAME: NAME is gone and its removal
# printed, or NAME is still on the daemon and no removal printed.
expect_removed() {
  if [[ ! -d "$workdir/cases/${DAEMON_CASE:-$1}/daemon/volumes/$2" ]] &&
    grep -q -x -F "$(removed_line "$2")" "$workdir/$1.out"; then
    pass "$1: leftover $2 removed and reported"
  else
    fail "$1: leftover $2 not removed or not reported"
    cat "$workdir/$1.out" "$workdir/$1.err" >&2
  fi
}
expect_kept() {
  if [[ -d "$workdir/cases/${DAEMON_CASE:-$1}/daemon/volumes/$2" ]] &&
    ! grep -q -F "volume $2 left behind" "$workdir/$1.out"; then
    pass "$1: $2 kept"
  else
    fail "$1: $2 removed"
  fi
}

# l. Kill injection. A run SIGKILLed while its suite container runs never
# reaches its trap, so its volumes leak; the next staging run removes them once
# they are older than the threshold.
kill_case="staging: a SIGKILLed run's workspace volume is removed by the next staging run"
killed="$workdir/killed"
make_checkout "$killed"
block="$workdir/cases/killed/block"
mkdir -p "$block" "$workdir/cases/killed/daemon"
: >"$workdir/killed.log"
env PATH="$workdir/bin:$PATH" \
  FAKE_DOCKER_LOG="$workdir/killed.log" \
  FAKE_GO_LOG="$workdir/killed.go.log" \
  FAKE_DAEMON_ROOT="$workdir/cases/killed/daemon" \
  FAKE_DAEMON_VISIBLE="" \
  FAKE_SUITE_BLOCK="$block" \
  PROJMUX_TEST_WORKSPACE_LABEL=contract-killed \
  PROJMUX_TEST_SKIP_IMAGE_BUILD=1 \
  PROJMUX_TEST_GOMODCACHE="$workdir/cases/killed/gomodcache" \
  PROJMUX_TEST_GOCACHE="$workdir/cases/killed/gocache" \
  PROJMUX_E2E_ARTIFACTS="$workdir/cases/killed/evidence" \
  bash "$killed/scripts/test-docker-run.sh" test/suite.sh \
  >"$workdir/killed.out" 2>"$workdir/killed.err" &
script_pid=$!
for ((t = 0; t < 300; t++)); do
  [[ -s "$block/pid" ]] && break
  sleep 0.1
done
suite_pid="$(cat "$block/pid" 2>/dev/null || true)"
kill -KILL "$script_pid" 2>/dev/null || true
wait "$script_pid" 2>/dev/null || true
leaked="$(find "$workdir/cases/killed/daemon/volumes" -mindepth 1 -maxdepth 1 -printf '%f\n' 2>/dev/null | sort | tr '\n' ' ')"
if [[ -n "$suite_pid" && "$leaked" == "fake-vol-1 fake-vol-2 " && "$(count killed 'volume rm')" == 0 ]]; then
  pass "$kill_case: leak reproduced ($leaked)"
else
  fail "$kill_case: leak not reproduced (suite pid '${suite_pid}', volumes '$leaked')"
fi
# While the killed run's suite container still holds them, an old volume stays.
for v in fake-vol-1 fake-vol-2; do
  ago 3600 >"$workdir/cases/killed/daemon/meta/$v.created"
done
DAEMON_CASE=killed PROJMUX_TEST_WORKSPACE_LABEL=contract-killed PROJMUX_TEST_WORKSPACE_STALE_SECONDS=60 \
  run_case killed-held "$killed" "" test/suite.sh
expect_status killed-held 0
DAEMON_CASE=killed expect_kept killed-held fake-vol-1
check killed-held "refusal noted on stderr" grep -q -F 'kept test workspace volume fake-vol-1' "$workdir/killed-held.err"
# The container ends; the next staging run removes the leftovers.
if [[ -n "$suite_pid" ]]; then
  : >"$block/release"
  kill -KILL "$suite_pid" 2>/dev/null || true
  for ((t = 0; t < 100; t++)); do
    running "$suite_pid" || break
    sleep 0.1
  done
fi
DAEMON_CASE=killed PROJMUX_TEST_WORKSPACE_LABEL=contract-killed PROJMUX_TEST_WORKSPACE_STALE_SECONDS=60 \
  run_case killed-next "$killed" "" test/suite.sh
expect_status killed-next 0
for v in fake-vol-1 fake-vol-2; do
  if [[ ! -d "$workdir/cases/killed/daemon/volumes/$v" ]] &&
    grep -q -x -F "$(removed_line "$v")" "$workdir/killed-next.out"; then
    pass "$kill_case: $v removed and reported"
  else
    fail "$kill_case: $v left behind"
  fi
done
check killed-next "cleanup runs before the first new volume" \
  test "$(grep -n -m1 -F 'volume ls -q --filter label=projmux.test-workspace=contract-killed' "$workdir/killed-next.log" | cut -d: -f1)" -lt \
  "$(grep -n -m1 -F 'volume create' "$workdir/killed-next.log" | cut -d: -f1)"
check killed-next "no volume left after the clean run" \
  test -z "$(ls -A "$workdir/cases/killed/daemon/volumes")"

# m. An old labelled volume a container holds is kept; the run keeps its status.
seed_volume in-use leftover-held projmux.test-workspace=contract-in-use "$(ago 3600)"
echo 0123456789ab >"$workdir/cases/in-use/daemon/meta/leftover-held.inuse"
seed_volume in-use leftover-free projmux.test-workspace=contract-in-use "2026-01-01T09:00:00+09:00"
FAKE_SUITE_EXIT=7 PROJMUX_TEST_WORKSPACE_LABEL=contract-in-use PROJMUX_TEST_WORKSPACE_STALE_SECONDS=60 \
  run_case in-use "$dood" "" test/suite.sh
expect_status in-use 7
expect_kept in-use leftover-held
expect_removed in-use leftover-free
check_has in-use "rm without -f for a leftover" "volume rm leftover-held" "volume rm leftover-held"

# n. A labelled volume within the threshold is kept (default threshold too).
seed_volume fresh leftover-fresh projmux.test-workspace=contract-fresh "$(ago 30)"
seed_volume fresh leftover-hours projmux.test-workspace=contract-fresh "$(ago 3600)"
PROJMUX_TEST_WORKSPACE_LABEL=contract-fresh run_case fresh "$dood" "" test/suite.sh
expect_status fresh 0
expect_kept fresh leftover-fresh
expect_kept fresh leftover-hours
check_never fresh "no rm of a fresh volume" "volume rm leftover"

# o. Old volumes with another label value, or no label, are kept.
seed_volume other-label leftover-default projmux.test-workspace=1 "$(ago 99999)"
seed_volume other-label leftover-unlabelled "" "$(ago 99999)"
PROJMUX_TEST_WORKSPACE_LABEL=contract-other PROJMUX_TEST_WORKSPACE_STALE_SECONDS=60 \
  run_case other-label "$dood" "" test/suite.sh
expect_status other-label 0
expect_kept other-label leftover-default
expect_kept other-label leftover-unlabelled
check_has other-label "own volumes carry the isolated label" \
  "volume create" "volume create --label projmux.test-workspace=contract-other"

# p. The bind-mount path never lists volumes, even with old leftovers around.
seed_volume host-leftover leftover-old projmux.test-workspace=1 "$(ago 99999)"
PROJMUX_TEST_WORKSPACE_STALE_SECONDS=60 run_case host-leftover "$host" "$host" test/suite.sh
expect_status host-leftover 0
check_never host-leftover "no volume ls" "volume ls"
expect_kept host-leftover leftover-old

# q. A failed listing or an unreadable CreatedAt never fails the run.
FAKE_VOLUME_LS_EXIT=1 run_case ls-failing "$dood" "" test/suite.sh
expect_status ls-failing 0
seed_volume bad-created leftover-garbled projmux.test-workspace=1 "not a date"
PROJMUX_TEST_WORKSPACE_STALE_SECONDS=0 run_case bad-created "$dood" "" test/suite.sh
expect_status bad-created 0
expect_kept bad-created leftover-garbled

# r. A threshold that is not a non-negative integer is a usage error.
PROJMUX_TEST_WORKSPACE_STALE_SECONDS=6h run_case bad-threshold "$dood" "" test/suite.sh
expect_status bad-threshold 2
check_never bad-threshold "no docker call" "run "

exit "$failures"
