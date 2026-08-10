#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"

security_bin_dir="${SECURITY_BIN_DIR:-$root/.bin/security-tools}"
export PATH="$security_bin_dir:$PATH"

missing=()
for tool in go govulncheck gosec staticcheck gitleaks actionlint shellcheck git python3; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		missing+=("$tool")
	fi
done
if ((${#missing[@]} > 0)); then
	printf 'security: missing required tools: %s\n' "${missing[*]}" >&2
	printf 'Run "make security-tools" for Go-based tools; install shellcheck, git, and python3 with your OS package manager.\n' >&2
	exit 2
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

echo ">> govulncheck"
govulncheck ./...

echo ">> gosec"
package_output="$(go list -f '{{.Dir}}' ./...)"
if [[ -z "$package_output" ]]; then
	echo "security: go list returned no packages" >&2
	exit 2
fi
readarray -t package_dirs <<<"$package_output"
gosec_rules="G101,G102,G103,G104,G106,G107,G108,G109,G110,G111,G112,G113,G114,G115,G116,G117,G118,G119,G120,G121,G122,G123,G124,G201,G202,G203,G204,G301,G302,G303,G304,G305,G306,G307,G401,G402,G403,G404,G405,G406,G407,G408,G501,G502,G503,G504,G505,G506,G507,G601,G602,G701,G702,G703,G704,G705,G706,G707,G708,G709,G710"
set +e
gosec -quiet -include="$gosec_rules" -fmt=json -out="$tmpdir/gosec.json" "${package_dirs[@]}"
gosec_status=$?
set -e
python3 scripts/security-baseline.py \
	--tool gosec \
	--root "$root" \
	--report "$tmpdir/gosec.json" \
	--baseline .security/gosec-baseline.json \
	--scanner-status "$gosec_status"

echo ">> staticcheck"
set +e
staticcheck -f json ./... >"$tmpdir/staticcheck.jsonl" 2>"$tmpdir/staticcheck.stderr"
staticcheck_status=$?
set -e
if [[ -s "$tmpdir/staticcheck.stderr" ]]; then
	cat "$tmpdir/staticcheck.stderr" >&2
	echo "security: staticcheck wrote diagnostics to stderr" >&2
	exit 2
fi
python3 scripts/security-baseline.py \
	--tool staticcheck \
	--root "$root" \
	--report "$tmpdir/staticcheck.jsonl" \
	--baseline .security/staticcheck-baseline.json \
	--scanner-status "$staticcheck_status"

echo ">> gitleaks (git history)"
gitleaks git --redact --no-banner --config=.gitleaks.toml .

echo ">> gitleaks (working tree)"
gitleaks dir --redact --no-banner --config=.gitleaks.toml .

echo ">> actionlint"
actionlint

echo ">> shellcheck"
readarray -d '' shell_files < <(git ls-files -z -- '*.sh' '*.bash')
regular_shell_files=()
for file in "${shell_files[@]}"; do
	case "$file" in
		test/lib/smoke.sh | test/e2e/linux-smoke.sh | test/e2e/npm-staging-path.sh | test/install/smoke.sh | test/integration/linux-smoke.sh) ;;
		*) regular_shell_files+=("$file") ;;
	esac
done
if ((${#regular_shell_files[@]} > 0)); then
	shellcheck "${regular_shell_files[@]}"
else
	echo ">> shellcheck: no tracked shell files"
fi
# These entrypoints dynamically source the shared library, which shellcheck
# cannot resolve statically. Keep the suppressions scoped to those files.
shellcheck --exclude=SC1091,SC2154 \
	test/e2e/linux-smoke.sh \
	test/e2e/npm-staging-path.sh \
	test/install/smoke.sh \
	test/integration/linux-smoke.sh
# The shared variable is consumed by the entrypoints after they source it.
shellcheck --exclude=SC2034 test/lib/smoke.sh

echo ">> security: all gates passed"
