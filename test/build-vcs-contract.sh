#!/usr/bin/env bash
# Contract for scripts/build-vcs-flag.sh, the flag `make build` passes to
# `go build`. Go treats only a `.git` directory as a repository root, so a
# linked worktree (whose `.git` is a file) is stamped with the enclosing
# checkout's revision, or with none. The script must leave the primary
# checkout unchanged and make every linked worktree build stamp no revision.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
script="$root/scripts/build-vcs-flag.sh"
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
failures=0

export GOFLAGS=
export GOTOOLCHAIN=local
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR

pass() { echo "PASS $1"; }
fail() {
  echo "FAIL $1" >&2
  failures=1
}

g() {
  git -c user.name=t -c user.email=t@example.invalid -c commit.gpgsign=false "$@"
}

# revision_of prints the vcs.revision a binary carries, or nothing. The
# `go version -m` line is `<tab>build<tab>vcs.revision=<sha>`: one field.
revision_of() {
  awk '$1=="build" && index($2,"vcs.revision=")==1 {print substr($2,14)}' "$1"
}

# build_in DIR LABEL FLAG builds DIR's module with FLAG (may be empty) and
# writes `go version -m` of the result to $workdir/LABEL.m.
build_in() {
  local dir="$1" label="$2" flag="$3" bin="$workdir/bin/$2"
  local -a args=()
  if [[ -n "$flag" ]]; then
    args=("$flag")
  fi
  if ! (cd "$dir" && go build ${args[@]+"${args[@]}"} -o "$bin" .) >"$workdir/$label.build" 2>&1; then
    fail "$label: go build failed"
    cat "$workdir/$label.build" >&2
    return 1
  fi
  go version -m "$bin" >"$workdir/$label.m"
}

# check_layout DIR LABEL EXPECT: EXPECT is `primary` (empty flag, silent,
# revision equals DIR's HEAD) or `worktree` (-buildvcs=false, one reason
# line, no revision).
check_layout() {
  local dir="$1" label="$2" expect="$3" status=0 flag rev head lines
  (cd "$dir" && "$script") >"$workdir/$label.out" 2>"$workdir/$label.err" || status=$?
  if [[ "$status" != 0 ]]; then
    fail "$label: script exit $status"
    cat "$workdir/$label.err" >&2
    return
  fi
  flag="$(cat "$workdir/$label.out")"
  lines="$(wc -l <"$workdir/$label.err")"

  if [[ "$expect" == primary ]]; then
    if [[ -n "$flag" || -s "$workdir/$label.err" ]]; then
      fail "$label: expected empty flag and stderr, got flag '$flag'"
      cat "$workdir/$label.err" >&2
    else
      pass "$label: flag empty, stderr empty"
    fi
    build_in "$dir" "$label" "$flag" || return
    rev="$(revision_of "$workdir/$label.m")"
    head="$(git -C "$dir" rev-parse HEAD)"
    if [[ "$rev" =~ ^[0-9a-f]{40}$ && "$rev" == "$head" ]]; then
      pass "$label: vcs.revision equals the checkout's HEAD"
    else
      fail "$label: vcs.revision '$rev', expected HEAD '$head'"
    fi
    return
  fi

  if [[ "$flag" == "-buildvcs=false" ]]; then
    pass "$label: flag -buildvcs=false"
  else
    fail "$label: flag '$flag', expected -buildvcs=false"
  fi
  if [[ "$lines" == 1 ]] && grep -q -F '>> linked worktree:' "$workdir/$label.err"; then
    pass "$label: one reason line on stderr"
  else
    fail "$label: expected one reason line on stderr, got $lines"
    cat "$workdir/$label.err" >&2
  fi
  build_in "$dir" "$label" "$flag" || return
  if grep -q -F 'vcs.revision' "$workdir/$label.m"; then
    fail "$label: binary still carries vcs.revision"
    cat "$workdir/$label.m" >&2
  else
    pass "$label: binary carries no vcs.revision"
  fi
}

mkdir -p "$workdir/bin"
primary="$workdir/primary"
nested="$primary/.wt/b"
sibling="$workdir/sibling"

mkdir -p "$primary"
g -C "$primary" init -q
printf 'module x\n\ngo 1.21\n' >"$primary/go.mod"
printf 'package main\n\nfunc main() {}\n' >"$primary/main.go"
printf '.wt/\n' >"$primary/.gitignore"
g -C "$primary" add go.mod main.go .gitignore
g -C "$primary" commit -q -m A
primary_head="$(git -C "$primary" rev-parse HEAD)"

g -C "$primary" worktree add -q -b b "$nested"
printf 'b\n' >"$nested/b.txt"
g -C "$nested" add b.txt
g -C "$nested" commit -q -m B
nested_head="$(git -C "$nested" rev-parse HEAD)"

g -C "$primary" worktree add -q -b s "$sibling"
printf 's\n' >"$sibling/s.txt"
g -C "$sibling" add s.txt
g -C "$sibling" commit -q -m S

check_layout "$primary" primary primary

# Control: the premise. Without the flag, a nested worktree build is stamped
# with the primary checkout's HEAD instead of its own. If this fails, Go's
# repository-root detection changed and the script needs re-evaluation.
if build_in "$nested" nested-unflagged-control ""; then
  control_rev="$(revision_of "$workdir/nested-unflagged-control.m")"
  if [[ "$control_rev" == "$primary_head" && "$control_rev" != "$nested_head" ]]; then
    pass "control (premise): unflagged nested worktree build stamps the primary's HEAD, not its own"
  else
    fail "control (premise): unflagged nested build stamped '$control_rev' (primary $primary_head, nested $nested_head); Go's root detection changed, re-evaluate scripts/build-vcs-flag.sh"
  fi
fi

check_layout "$nested" nested-worktree worktree
check_layout "$sibling" sibling-worktree worktree

# Outside any git checkout the script must print nothing and succeed.
mkdir -p "$workdir/nogit"
status=0
(cd "$workdir/nogit" && GIT_CEILING_DIRECTORIES="$workdir" "$script") >"$workdir/nogit.out" 2>"$workdir/nogit.err" || status=$?
if [[ "$status" == 0 && ! -s "$workdir/nogit.out" && ! -s "$workdir/nogit.err" ]]; then
  pass "outside git: exit 0, stdout and stderr empty"
else
  fail "outside git: exit $status or unexpected output"
  cat "$workdir/nogit.out" "$workdir/nogit.err" >&2
fi

# Bind the contract to `make build`: its recipe must use the script.
recipe="$(awk '/^build:/ {f=1; next} f && !/^\t/ {f=0} f' "$root/Makefile")"
if [[ "$recipe" == *'scripts/build-vcs-flag.sh'* && "$recipe" == *"\$(GO) build \$\$vcs_flag"* ]]; then
  pass "Makefile build recipe passes scripts/build-vcs-flag.sh output to go build"
else
  fail "Makefile build recipe does not pass scripts/build-vcs-flag.sh output to go build"
fi

exit "$failures"
