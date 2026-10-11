#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
repo=$PWD
scratch=$(mktemp -d "$repo/test/sdk/.boundary-XXXXXX")
trap 'rm -rf "$scratch"' EXIT
export GOWORK=off
"${GO:-go}" build -o "$scratch/sdkboundary" ./internal/tools/sdkboundary
"$scratch/sdkboundary" github.com/crevissepartners/projmux/pkg/projmux
(cd test/sdk/consumer && "${GO:-go}" test -mod=readonly ./...)
"${GO:-go}" test ./internal/tools/sdkboundary
echo 'SDK boundary: public SDK and external consumer passed; implementation and public-type negative fixtures passed'
