#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
manifest="${SECURITY_TOOL_MANIFEST:-$root/.security/security-tools.versions}"
bin_dir="${SECURITY_BIN_DIR:-$root/.bin/security-tools}"
go_cmd="${GO:-go}"
# ShellCheck is not a Go tool, so it is pinned by release asset digest instead
# of the Go manifest. The pin file is the only trust anchor: one sha256sum line
# per supported platform asset, and the version comes from the asset names.
shellcheck_pin="${SECURITY_SHELLCHECK_PIN:-$root/.security/shellcheck.sha256}"
shellcheck_url_base="${SECURITY_SHELLCHECK_URL_BASE:-https://github.com/koalaman/shellcheck/releases/download}"

if [[ ! -f "$manifest" || -L "$manifest" ]]; then
	printf 'security-tools: canonical manifest is missing or not a regular file: %s\n' "$manifest" >&2
	exit 2
fi
if ! awk -F= '
	BEGIN {
		expected[1]="govulncheck"
		expected[2]="gosec"
		expected[3]="staticcheck"
		expected[4]="gitleaks"
		expected[5]="actionlint"
	}
	NF != 2 || $1 != expected[NR] || $2 !~ /^v[0-9][^[:space:]=]*$/ { exit 1 }
	END { if (NR != 5) exit 1 }
' "$manifest"; then
	printf 'security-tools: invalid canonical manifest (expected exactly five ordered tool=vVERSION entries): %s\n' "$manifest" >&2
	exit 2
fi

if [[ -L "$bin_dir" || ( -e "$bin_dir" && ! -d "$bin_dir" ) ]]; then
	printf 'security-tools: bin directory is not an owned real directory: %s\n' "$bin_dir" >&2
	exit 2
fi
goos="$($go_cmd env GOOS)"
goarch="$($go_cmd env GOARCH)"

# Fail closed unless the pin names exactly the four supported platform assets
# of one release, each once.
if [[ ! -f "$shellcheck_pin" || -L "$shellcheck_pin" ]]; then
	printf 'security-tools: ShellCheck pin is missing or not a regular file: %s\n' "$shellcheck_pin" >&2
	exit 2
fi
shellcheck_version=""
shellcheck_platforms=""
shellcheck_lines=0
while IFS= read -r line || [[ -n "$line" ]]; do
	shellcheck_lines=$((shellcheck_lines + 1))
	if [[ ! "$line" =~ ^[0-9a-f]{64}\ \ shellcheck-(v[0-9]+\.[0-9]+\.[0-9]+)\.(linux|darwin)\.(x86_64|aarch64)\.tar\.xz$ ]] ||
		[[ -n "$shellcheck_version" && "${BASH_REMATCH[1]}" != "$shellcheck_version" ]] ||
		[[ "$shellcheck_platforms" == *" ${BASH_REMATCH[2]}.${BASH_REMATCH[3]} "* ]]; then
		shellcheck_version=""
		break
	fi
	shellcheck_version="${BASH_REMATCH[1]}"
	shellcheck_platforms+=" ${BASH_REMATCH[2]}.${BASH_REMATCH[3]} "
done <"$shellcheck_pin"
if [[ -z "$shellcheck_version" || "$shellcheck_lines" != "4" ]]; then
	printf 'security-tools: invalid ShellCheck pin (expected exactly four "<sha256>  shellcheck-vX.Y.Z.<os>.<arch>.tar.xz" lines, one per supported platform): %s\n' "$shellcheck_pin" >&2
	exit 2
fi
case "$goos/$goarch" in
	linux/amd64) shellcheck_platform="linux.x86_64" ;;
	linux/arm64) shellcheck_platform="linux.aarch64" ;;
	darwin/amd64) shellcheck_platform="darwin.x86_64" ;;
	darwin/arm64) shellcheck_platform="darwin.aarch64" ;;
	*)
		printf 'security-tools: no pinned ShellCheck release for unsupported platform %s/%s (supported: linux and darwin on amd64 and arm64)\n' "$goos" "$goarch" >&2
		exit 2
		;;
esac
shellcheck_asset="shellcheck-$shellcheck_version.$shellcheck_platform.tar.xz"

# Print the version a ShellCheck binary reports, without the leading "v".
shellcheck_reported_version() {
	"$1" --version 2>/dev/null | awk '$1 == "version:" { print $2 }'
}

mkdir -p "$bin_dir"
tools_ok=1
while IFS='=' read -r tool version; do
	case "$tool" in
		govulncheck)
			package="golang.org/x/vuln/cmd/govulncheck"
			module="golang.org/x/vuln"
			;;
		gosec)
			package="github.com/securego/gosec/v2/cmd/gosec"
			module="github.com/securego/gosec/v2"
			;;
		staticcheck)
			package="honnef.co/go/tools/cmd/staticcheck"
			module="honnef.co/go/tools"
			;;
		gitleaks)
			package="github.com/zricethezav/gitleaks/v8"
			module="github.com/zricethezav/gitleaks/v8"
			;;
		actionlint)
			package="github.com/rhysd/actionlint/cmd/actionlint"
			module="github.com/rhysd/actionlint"
			;;
	esac
	if [[ ! -f "$bin_dir/$tool" || -L "$bin_dir/$tool" || ! -x "$bin_dir/$tool" ]] ||
		! "$go_cmd" version -m "$bin_dir/$tool" 2>/dev/null | awk \
			-v want_package="$package" \
			-v want_module="$module" \
			-v want_version="$version" \
			-v want_goos="GOOS=$goos" \
			-v want_goarch="GOARCH=$goarch" '
				$1 == "path" && $2 == want_package { package_ok=1 }
				$1 == "mod" && $2 == want_module && $3 == want_version { module_ok=1 }
				$1 == "build" && $2 == want_goos { goos_ok=1 }
				$1 == "build" && $2 == want_goarch { goarch_ok=1 }
				END { exit(package_ok && module_ok && goos_ok && goarch_ok ? 0 : 1) }
			'; then
		tools_ok=0
	fi
done <"$manifest"
if [[ ! -f "$bin_dir/.versions" || -L "$bin_dir/.versions" ]] || ! cmp -s "$manifest" "$bin_dir/.versions"; then
	tools_ok=0
fi
# The CI tool cache key hashes the ShellCheck pin, but a bin dir restored without
# that key (local `make security-tools`, or a cache saved under another key) still
# relies on this stamp to turn a changed pin into a reinstall, not a stale binary.
if [[ ! -f "$bin_dir/.shellcheck.sha256" || -L "$bin_dir/.shellcheck.sha256" ]] ||
	! cmp -s "$shellcheck_pin" "$bin_dir/.shellcheck.sha256" ||
	[[ ! -f "$bin_dir/shellcheck" || -L "$bin_dir/shellcheck" || ! -x "$bin_dir/shellcheck" ]] ||
	[[ "$(shellcheck_reported_version "$bin_dir/shellcheck" || true)" != "${shellcheck_version#v}" ]]; then
	tools_ok=0
fi

if [[ "$tools_ok" == "1" ]]; then
	printf '>> pinned security tools already installed in %s\n' "$bin_dir"
	printf 'security_tools_cache=hit\n'
	exit 0
fi

printf '>> installing pinned security tools into %s\n' "$bin_dir"
printf 'security_tools_cache=miss\n'
stage="$(mktemp -d "$(dirname "$bin_dir")/.security-tools.XXXXXX")"
cleanup() {
	rm -rf -- "$stage"
}
trap cleanup EXIT HUP INT TERM

# Download and verify ShellCheck first, so a bad pin or download fails before
# the slower Go builds.
curl -fsSL --retry 3 --retry-delay 2 --connect-timeout 30 \
	-o "$stage/$shellcheck_asset" "$shellcheck_url_base/$shellcheck_version/$shellcheck_asset"
grep -F "  $shellcheck_asset" "$shellcheck_pin" >"$stage/shellcheck-asset.sha256"
if command -v sha256sum >/dev/null 2>&1; then
	(cd "$stage" && sha256sum -c --strict shellcheck-asset.sha256)
else
	(cd "$stage" && shasum -a 256 -c shellcheck-asset.sha256)
fi
mkdir "$stage/shellcheck-extract"
tar -xJf "$stage/$shellcheck_asset" -C "$stage/shellcheck-extract"
if [[ ! -f "$stage/shellcheck-extract/shellcheck-$shellcheck_version/shellcheck" ||
	-L "$stage/shellcheck-extract/shellcheck-$shellcheck_version/shellcheck" ]]; then
	printf 'security-tools: %s does not contain shellcheck-%s/shellcheck\n' "$shellcheck_asset" "$shellcheck_version" >&2
	exit 2
fi
mv "$stage/shellcheck-extract/shellcheck-$shellcheck_version/shellcheck" "$stage/shellcheck"
chmod 0755 "$stage/shellcheck"
if [[ "$(shellcheck_reported_version "$stage/shellcheck" || true)" != "${shellcheck_version#v}" ]]; then
	printf 'security-tools: extracted ShellCheck does not report version %s\n' "${shellcheck_version#v}" >&2
	exit 2
fi
cp "$shellcheck_pin" "$stage/.shellcheck.sha256"

while IFS='=' read -r tool version; do
	case "$tool" in
		govulncheck) package="golang.org/x/vuln/cmd/govulncheck" ;;
		gosec) package="github.com/securego/gosec/v2/cmd/gosec" ;;
		staticcheck) package="honnef.co/go/tools/cmd/staticcheck" ;;
		gitleaks) package="github.com/zricethezav/gitleaks/v8" ;;
		actionlint) package="github.com/rhysd/actionlint/cmd/actionlint" ;;
	esac
	GOBIN="$stage" "$go_cmd" install "$package@$version"
done <"$manifest"
cp "$manifest" "$stage/.versions"
for tool in govulncheck gosec staticcheck gitleaks actionlint shellcheck; do
	mv "$stage/$tool" "$bin_dir/$tool"
done
mv "$stage/.versions" "$bin_dir/.versions"
mv "$stage/.shellcheck.sha256" "$bin_dir/.shellcheck.sha256"
rm -rf -- "$stage"
trap - EXIT HUP INT TERM
