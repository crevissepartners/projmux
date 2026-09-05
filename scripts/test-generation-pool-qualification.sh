#!/usr/bin/env bash
set -euo pipefail

# Runs the declared-pair isolated generation-pool qualification and always
# leaves one terminal typed record behind.
#
# The runner declares nothing about the outcome. It only names which version
# pair is under test, owns the two isolated roots it creates, and carries the
# canonical receipt out. Every evidence boolean and counter stays measured by
# the harness itself.

usage() {
	echo "usage: $0 <old-version> <new-version> <output-dir>" >&2
	exit 2
}

(($# == 3)) || usage
old_version="$1"
new_version="$2"
output_dir="$3"

for declared in "$old_version" "$new_version"; do
	case "$declared" in
	'' | *[!0-9.-]*)
		echo "declared version is not a receipt version token: '$declared'" >&2
		exit 2
		;;
	esac
done
if [ "$old_version" = "$new_version" ]; then
	echo "declared pair must name two different versions, got '$old_version' twice" >&2
	exit 2
fi

mkdir -p "$output_dir"
output_dir="$(cd "$output_dir" && pwd)"
receipt="$output_dir/receipt.json"
outcome="$output_dir/outcome.json"
rm -f "$receipt" "$outcome"

attempted="false"
class="infra-error"
reason="runner-aborted"
smoke_root=""
bundle_root=""

finish() {
	local status=$?
	local emitted=""
	if [ -f "$receipt" ]; then
		emitted="receipt.json"
	fi
	if [ -n "$smoke_root" ]; then
		rm -rf "$smoke_root"
	fi
	if [ -n "$bundle_root" ]; then
		rm -rf "$bundle_root"
	fi
	cat >"$outcome" <<JSON
{
  "schemaVersion": 1,
  "versions": {"old": "$old_version", "new": "$new_version"},
  "attempted": $attempted,
  "class": "$class",
  "reason": "$reason",
  "receipt": "$emitted"
}
JSON
	echo "generation-pool-qualification class=$class reason=$reason receipt=${emitted:-none}" >&2
	exit "$status"
}
trap finish EXIT

unsupported() {
	class="unsupported"
	reason="$1"
	exit 0
}

old_binary="${PROJMUX_CODEX_GENERATION_OLD:-}"
new_binary="${PROJMUX_CODEX_GENERATION_NEW:-}"
source_home="${PROJMUX_CODEX_GENERATION_SOURCE_HOME:-${CODEX_HOME:-$HOME/.codex}}"

[ -x "$old_binary" ] || unsupported "declared-old-binary-unavailable"
[ -x "$new_binary" ] || unsupported "declared-new-binary-unavailable"
for name in auth.json config.toml; do
	[ -s "$source_home/$name" ] || unsupported "credentialed-state-unavailable"
done

# Both roots are short children of the temporary directory: the harness refuses
# a root outside it, and the private sockets it binds inside must stay within
# the 108-byte sun_path limit.
smoke_root="$(mktemp -d "${TMPDIR:-/tmp}/pmxgq-XXXXXX")"
bundle_root="$(mktemp -d "${PROJMUX_CODEX_GENERATION_BUNDLE_TMPDIR:-${TMPDIR:-/tmp}}/pmxgb-XXXXXX")"

attempted="true"
class="fail"
reason="harness-refused"
env -u TMUX -u TMUX_PANE \
	PROJMUX_CODEX_GENERATION_SMOKE_ROOT="$smoke_root" \
	PROJMUX_CODEX_GENERATION_BUNDLE_SMOKE_ROOT="$bundle_root" \
	PROJMUX_CODEX_GENERATION_OLD="$old_binary" \
	PROJMUX_CODEX_GENERATION_NEW="$new_binary" \
	PROJMUX_CODEX_GENERATION_OLD_VERSION="$old_version" \
	PROJMUX_CODEX_GENERATION_NEW_VERSION="$new_version" \
	PROJMUX_CODEX_GENERATION_SOURCE_HOME="$source_home" \
	PROJMUX_CODEX_GENERATION_RECEIPT="$receipt" \
	go test ./internal/testutil/codexinstalled \
	-run '^TestInstalledIsolatedGenerationPoolQualification$' -count=1 -v -timeout 30m

class="pass"
reason="qualified"
