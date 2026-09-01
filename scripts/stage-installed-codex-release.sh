#!/usr/bin/env bash
set -euo pipefail

usage() {
	echo "usage: $0 <npm-prefix> <release-root> <codex-version>" >&2
	exit 2
}

(( $# == 3 )) || usage
npm_prefix="$1"
release_root="$2"
codex_version="$3"

platform_alias="codex-linux-x64"
target="x86_64-unknown-linux-musl"
meta_package="$npm_prefix/node_modules/@openai/codex"
platform_package="$meta_package/node_modules/@openai/$platform_alias"
source_release="$platform_package/vendor/$target"

# The platform optional dependency is the official native release payload.
# Validate its public package and standalone manifests before copying it; do
# not infer a managed version from the JS wrapper or the CLI version output.
jq -e --arg version "$codex_version" \
	'.name == "@openai/codex" and .version == $version' \
	"$meta_package/package.json" >/dev/null
jq -e --arg version "$codex_version-linux-x64" \
	'.name == "@openai/codex" and .version == $version' \
	"$platform_package/package.json" >/dev/null
jq -e --arg version "$codex_version" --arg target "$target" \
	'.layoutVersion == 1 and .version == $version and .target == $target and
	 .variant == "codex" and .entrypoint == "bin/codex" and
	 .resourcesDir == "codex-resources" and .pathDir == "codex-path"' \
	"$source_release/codex-package.json" >/dev/null
test -x "$source_release/bin/codex"

if [[ -e "$release_root" ]]; then
	echo "installed Codex staging root already exists" >&2
	exit 1
fi
mkdir -p "$release_root"
cp -R "$source_release/." "$release_root/"
ln -s bin/codex "$release_root/codex"

resolved_codex="$(readlink -f "$release_root/bin/codex")"
test "$resolved_codex" = "$release_root/bin/codex"
test "$(readlink -f "$release_root/codex")" = "$resolved_codex"
test -x "$resolved_codex"
test -f "$release_root/codex-package.json"
jq -e --arg version "$codex_version" '.version == $version' \
	"$release_root/codex-package.json" >/dev/null
