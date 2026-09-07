#!/usr/bin/env bash
set -euo pipefail

# Disposable own-child SessionStart registration plus Projmux-owned UDS/pipe
# handoff. The synthetic provider accepts only the frozen auth+user test frame;
# The public reply CLI resolves a disposable, separately contained tmux pane;
# no provider binary/account, model, MCP, connector, or network service is used.
unset TMUX TMUX_PANE
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
build_root="$(mktemp -d "${TMPDIR:-/tmp}/projmux-claude-endpoint-build.XXXXXX")"
trap 'rm -rf -- "$build_root"' EXIT
cd "$root"
go build -o "$build_root/projmux" ./cmd/projmux
chmod 0700 "$build_root/projmux"
PMX_TEST_CLAUDE_ENDPOINT_BIN="$build_root/projmux" go test ./internal/app -run '^TestClaudeEndpointProcessIntegration$' -count=1 -v
