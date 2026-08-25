#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
manifest="${SECURITY_TOOL_MANIFEST:-$root/.security/security-tools.versions}"
bin_dir="${SECURITY_BIN_DIR:-$root/.bin/security-tools}"
go_cmd="${GO:-go}"

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
mkdir -p "$bin_dir"
goos="$($go_cmd env GOOS)"
goarch="$($go_cmd env GOARCH)"
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
for tool in govulncheck gosec staticcheck gitleaks actionlint; do
	mv "$stage/$tool" "$bin_dir/$tool"
done
mv "$stage/.versions" "$bin_dir/.versions"
rmdir "$stage"
trap - EXIT HUP INT TERM
