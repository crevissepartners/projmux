#!/usr/bin/env bash
set -euo pipefail

# The isolated-fleet demonstration of the L2 replacement guarantee.
#
# It builds a real binary, and the Go integration behind it runs a real broker
# runtime on a real Unix socket, republishes the binary those processes are
# executing with the same atomic rename `make install` uses, and then reads the
# `projmux doctor` replacement row an operator would read. No provider binary,
# account, model, or network service is used: the broker publishes and serves
# without ever reaching an endpoint, which is all this observation needs.
#
# The fleet is isolated three ways. `TMUX` and `TMUX_PANE` are dropped so no
# inherited client identity leaks in; `TMUX_TMPDIR` plus a run-unique `-L`
# socket keep every tmux server under one owned root; and `HOME` with the XDG
# variables move the state domain, and the broker's whole discovery contract
# with it, inside that root. Cleanup reads back the exact `#{socket_path}` and
# ends only a socket proven to be under the owned root.
unset TMUX TMUX_PANE
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
build_root="$(mktemp -d "${TMPDIR:-/tmp}/projmux-install-replacement-build.XXXXXX")"
trap 'rm -rf -- "$build_root"' EXIT
cd "$root"
go build -o "$build_root/projmux" ./cmd/projmux
chmod 0700 "$build_root/projmux"
PMX_TEST_INSTALL_REPLACEMENT_BIN="$build_root/projmux" \
  go test ./internal/app -run '^TestInstallReplacementDrainIntegration$' -count=1 -v
