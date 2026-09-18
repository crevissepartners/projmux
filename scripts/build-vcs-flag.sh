#!/usr/bin/env bash
# Print the VCS flag for `make build`.
#
# Go treats only a `.git` directory as a repository root. In a linked
# worktree `.git` is a file, so `go build` keeps walking up: a worktree nested
# in another checkout gets that checkout's HEAD stamped as `vcs.revision`, a
# valid-looking but wrong sha. In a linked worktree we build with
# -buildvcs=false so the binary carries no revision rather than a wrong one.
# In the primary checkout, or outside git, print nothing and change nothing.
set -euo pipefail

git_dir="$(git rev-parse --path-format=absolute --git-dir 2>/dev/null)" || exit 0
common_dir="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" || exit 0

if [[ "$git_dir" != "$common_dir" ]]; then
  echo ">> linked worktree: building with -buildvcs=false because go would stamp the enclosing checkout's revision; prove provenance with the binary's sha256 and the merge commit" >&2
  echo "-buildvcs=false"
fi
