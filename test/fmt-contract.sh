#!/usr/bin/env bash
# Contract for the Makefile `fmt` and `fmt-check` targets. They must stream
# the Go path list into gofmt instead of expanding it into one shell argument:
# Linux caps a single argument at MAX_ARG_STRLEN (131072 bytes), so a recipe
# that inlines every path breaks once the tree grows past it. The targets keep
# the `.git/`, `.wt/`, and `node_modules/` exclusions, never run gofmt with no
# files (it would read stdin), and fail when gofmt itself fails.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
makefile="$root/Makefile"
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
failures=0

# A parent make must not leak jobs, silence, or dry-run flags into ours.
unset MAKEFLAGS MAKELEVEL MFLAGS MAKEOVERRIDES GNUMAKEFLAGS
export GOFLAGS=
export GOTOOLCHAIN=local

pass() { echo "PASS $1"; }
fail() {
  echo "FAIL $1" >&2
  failures=1
}

# mk DIR TARGET runs the repo Makefile's TARGET with DIR as the tree root,
# writing stdout/stderr to $workdir/last.out and $workdir/last.err. It prints
# the exit status.
mk() {
  local status=0
  make -s -C "$1" -f "$makefile" "$2" >"$workdir/last.out" 2>"$workdir/last.err" || status=$?
  echo "$status"
}

# recipe_bytes DIR TARGET prints the byte size of TARGET's dry-run recipe.
recipe_bytes() {
  make -n --no-print-directory -C "$1" -f "$makefile" "$2" 2>/dev/null | wc -c | tr -d ' '
}

# go_list_bytes DIR sums the bytes of the path list the targets must cover.
go_list_bytes() {
  (cd "$1" && find . -type f -name '*.go' -not -path './.git/*' -not -path './.wt/*' \
    -not -path '*/node_modules/*' -print0 | tr -d '\0' | wc -c | tr -d ' ')
}

formatted='package p\n'
unformatted='package p\nfunc  f( ) {  }\n'

# Big tree: 3 nested 200-char components, 6 leaf directories, 40 files each.
big="$workdir/big"
seg_a="$(printf 'a%.0s' $(seq 200))"
seg_b="$(printf 'b%.0s' $(seq 200))"
for leaf in 1 2 3 4 5 6; do
  dir="$big/$seg_a/$seg_b/${leaf}$(printf 'c%.0s' $(seq 199))"
  mkdir -p "$dir"
  for i in $(seq 40); do
    printf '%b' "$formatted" >"$dir/file_$i.go"
  done
done
big_bytes="$(go_list_bytes "$big")"
if ((big_bytes > 131072)); then
  pass "fixture: big tree path list is $big_bytes bytes (> 131072)"
else
  fail "fixture: big tree path list is only $big_bytes bytes; it must exceed 131072"
fi

# a. Big formatted tree.
status="$(mk "$big" fmt-check)"
if [[ "$status" == 0 && ! -s "$workdir/last.out" ]]; then
  pass "a: fmt-check on the big formatted tree exits 0 with no paths"
else
  fail "a: fmt-check on the big formatted tree exited $status"
  cat "$workdir/last.out" "$workdir/last.err" >&2
fi
status="$(mk "$big" fmt)"
if [[ "$status" == 0 ]]; then
  pass "a: fmt on the big formatted tree exits 0"
else
  fail "a: fmt on the big formatted tree exited $status"
  cat "$workdir/last.err" >&2
fi

# b. One unformatted file in the big tree.
bad_rel="./$seg_a/$seg_b/zz_unformatted.go"
printf '%b' "$unformatted" >"$big/$bad_rel"
status="$(mk "$big" fmt-check)"
if [[ "$status" != 0 ]] && [[ "$(cat "$workdir/last.out")" == "$bad_rel" ]]; then
  pass "b: fmt-check prints the one unformatted path and fails"
else
  fail "b: fmt-check exited $status; expected nonzero and exactly '$bad_rel'"
  cat "$workdir/last.out" "$workdir/last.err" >&2
fi
status="$(mk "$big" fmt)"
status2="$(mk "$big" fmt-check)"
if [[ "$status" == 0 && "$status2" == 0 ]]; then
  pass "b: after fmt, fmt-check exits 0"
else
  fail "b: fmt exited $status, then fmt-check exited $status2"
  cat "$workdir/last.out" "$workdir/last.err" >&2
fi

# c. Excluded directories are neither checked nor rewritten.
excl="$workdir/excluded"
mkdir -p "$excl/.wt/x" "$excl/node_modules/y" "$excl/sub/node_modules/z" "$excl/.git/w"
printf '%b' "$formatted" >"$excl/ok.go"
for f in .wt/x/a.go node_modules/y/b.go sub/node_modules/z/c.go .git/w/d.go; do
  printf '%b' "$unformatted" >"$excl/$f"
done
status="$(mk "$excl" fmt-check)"
if [[ "$status" == 0 && ! -s "$workdir/last.out" ]]; then
  pass "c: fmt-check ignores .wt/, node_modules/, and .git/"
else
  fail "c: fmt-check exited $status on a tree whose only unformatted files are excluded"
  cat "$workdir/last.out" "$workdir/last.err" >&2
fi
status="$(mk "$excl" fmt)"
untouched=1
for f in .wt/x/a.go node_modules/y/b.go sub/node_modules/z/c.go .git/w/d.go; do
  if [[ "$(cat "$excl/$f")" != "$(printf '%b' "$unformatted")" ]]; then
    untouched=0
    fail "c: fmt rewrote excluded file $f"
  fi
done
if [[ "$status" == 0 && "$untouched" == 1 ]]; then
  pass "c: fmt exits 0 and leaves excluded files unchanged"
elif [[ "$status" != 0 ]]; then
  fail "c: fmt exited $status"
fi

# d. A gofmt failure (parse error) fails both targets.
broken="$workdir/broken"
mkdir -p "$broken"
printf '%b' "$formatted" >"$broken/ok.go"
printf 'package p\nfunc {\n' >"$broken/bad.go"
status="$(mk "$broken" fmt-check)"
if [[ "$status" != 0 ]]; then
  pass "d: fmt-check fails when gofmt fails on a parse error (exit $status)"
else
  fail "d: fmt-check exited 0 on a Go file with a parse error"
fi
status="$(mk "$broken" fmt)"
if [[ "$status" != 0 ]]; then
  pass "d: fmt fails when gofmt fails on a parse error (exit $status)"
else
  fail "d: fmt exited 0 on a Go file with a parse error"
fi

# Empty tree: gofmt must never start without files, because it would then
# read stdin (GNU xargs runs its command once on empty input; `gofmt -w`
# then refuses stdin). Stdin carries a parse error so a gofmt that reads it
# fails or prints instead of blocking. The message text is not part of the
# contract; only the exit status and the absence of gofmt output are.
empty="$workdir/empty"
mkdir -p "$empty"
for target in fmt-check fmt; do
  status=0
  printf 'package p\nfunc {\n' |
    make -s -C "$empty" -f "$makefile" "$target" >"$workdir/last.out" 2>"$workdir/last.err" || status=$?
  if [[ "$status" == 0 && ! -s "$workdir/last.err" ]] && ! grep -q -e '<standard input>' -e 'func' "$workdir/last.out"; then
    pass "empty tree: $target exits 0 without running gofmt on stdin"
  else
    fail "empty tree: $target exited $status or gofmt read stdin"
    cat "$workdir/last.out" "$workdir/last.err" >&2
  fi
done

# e. The recipe does not grow with the file count.
small="$workdir/small"
mkdir -p "$small"
printf '%b' "$formatted" >"$small/one.go"
for target in fmt-check fmt; do
  small_size="$(recipe_bytes "$small" "$target")"
  big_size="$(recipe_bytes "$big" "$target")"
  if [[ "$small_size" == "$big_size" && "$small_size" -gt 0 ]]; then
    pass "e: $target recipe is $small_size bytes for 1 file and for the big tree"
  else
    fail "e: $target recipe is $small_size bytes for 1 file but $big_size bytes for the big tree"
  fi
done

exit "$failures"
