#!/usr/bin/env bash
set -euo pipefail

usage() {
	echo "usage: $0 run <primitive> <output.json> | aggregate <children-dir> <output.json>" >&2
	exit 2
}

(( $# == 3 )) || usage
mode="$1"
input="$2"
output="$3"

case "$mode" in
	run)
		env -u TMUX -u TMUX_PANE go run ./internal/testutil/codexinstalled/cmd/qualification \
			--primitive "$input" \
			--preflight "${PROJMUX_CODEX_INSTALL_OUTCOME:-success}" \
			--expected-version "${PROJMUX_CODEX_EXPECTED_VERSION:-}" \
			--output "$output"
		;;
	aggregate)
		env -u TMUX -u TMUX_PANE go run ./internal/testutil/codexinstalled/cmd/qualification \
			--aggregate-dir "$input" \
			--output "$output"
		;;
	*)
		usage
		;;
esac
