#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"

group="${1:-all}"
security_bin_dir="${SECURITY_BIN_DIR:-$root/.bin/security-tools}"
export PATH="$security_bin_dir:$PATH"

case "$group" in
	--list)
		cat <<'EOF'
go-security:govulncheck,gosec
go-static:staticcheck
repository-policy:gitleaks-git,gitleaks-dir,actionlint,shellcheck
EOF
		exit 0
		;;
	go-security | go-static | repository-policy | all) ;;
	*)
		printf 'security: invalid group: %s\n' "$group" >&2
		exit 2
		;;
esac

required_tools=(git python3)
case "$group" in
	go-security) required_tools+=(go govulncheck gosec) ;;
	go-static) required_tools+=(go staticcheck) ;;
	repository-policy) required_tools+=(gitleaks actionlint shellcheck) ;;
	all) required_tools+=(go govulncheck gosec staticcheck gitleaks actionlint shellcheck) ;;
esac
missing=()
for tool in "${required_tools[@]}"; do
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
cleanup() {
	rm -rf -- "$tmpdir"
}
trap cleanup EXIT HUP INT TERM

evidence_dir="${SECURITY_EVIDENCE_DIR:-}"
if [[ -n "$evidence_dir" ]]; then
	mkdir -p "$evidence_dir"
	: >"$evidence_dir/events.jsonl"
fi

emit_event() {
	local scanner="$1"
	local phase="$2"
	local status="$3"
	case "$scanner" in
		govulncheck | gosec | staticcheck | gitleaks-git | gitleaks-dir | actionlint | shellcheck) ;;
		*) return 2 ;;
	esac
	case "$phase:$status" in
		begin:running | terminal:pass | terminal:fail) ;;
		*) return 2 ;;
	esac
	if [[ -n "$evidence_dir" ]]; then
		printf '{"schema":"projmux.security.scanner.v1","group":"%s","scanner":"%s","phase":"%s","status":"%s"}\n' \
			"$group" "$scanner" "$phase" "$status" >>"$evidence_dir/events.jsonl"
	fi
}

run_scanner() {
	local scanner="$1"
	shift
	emit_event "$scanner" begin running
	local status=0
	if "$@"; then
		status=0
	else
		status=$?
	fi
	if ((status == 0)); then
		emit_event "$scanner" terminal pass
	else
		emit_event "$scanner" terminal fail
	fi
	return "$status"
}

scan_govulncheck() {
	echo ">> govulncheck"
	govulncheck ./...
}

scan_gosec() {
	echo ">> gosec"
	local gosec_rules
	gosec_rules="G101,G102,G103,G104,G106,G107,G108,G109,G110,G111,G112,G113,G114,G115,G116,G117,G118,G119,G120,G121,G122,G123,G124,G201,G202,G203,G204,G301,G302,G303,G304,G305,G306,G307,G401,G402,G403,G404,G405,G406,G407,G408,G501,G502,G503,G504,G505,G506,G507,G601,G602,G701,G702,G703,G704,G705,G706,G707,G708,G709,G710"
	local scanner_status=0
	if gosec -quiet -include="$gosec_rules" -fmt=json -out="$tmpdir/gosec.json" ./...; then
		scanner_status=0
	else
		scanner_status=$?
	fi
	local evidence_args=()
	if [[ -n "$evidence_dir" ]]; then
		evidence_args=(--evidence "$evidence_dir/gosec-parity.json")
	fi
	python3 scripts/security-baseline.py \
		--tool gosec \
		--root "$root" \
		--report "$tmpdir/gosec.json" \
		--baseline .security/gosec-baseline.json \
		--scanner-status "$scanner_status" \
		--expected-parity .security/security-current-findings.json \
		"${evidence_args[@]}"
}

scan_staticcheck() {
	echo ">> staticcheck"
	local scanner_status=0
	if staticcheck -f json ./... >"$tmpdir/staticcheck.jsonl" 2>"$tmpdir/staticcheck.stderr"; then
		scanner_status=0
	else
		scanner_status=$?
	fi
	if [[ -s "$tmpdir/staticcheck.stderr" ]]; then
		cat "$tmpdir/staticcheck.stderr" >&2
		echo "security: staticcheck wrote diagnostics to stderr" >&2
		return 2
	fi
	local evidence_args=()
	if [[ -n "$evidence_dir" ]]; then
		evidence_args=(--evidence "$evidence_dir/staticcheck-parity.json")
	fi
	python3 scripts/security-baseline.py \
		--tool staticcheck \
		--root "$root" \
		--report "$tmpdir/staticcheck.jsonl" \
		--baseline .security/staticcheck-baseline.json \
		--scanner-status "$scanner_status" \
		--expected-parity .security/security-current-findings.json \
		"${evidence_args[@]}"
}

scan_gitleaks_git() {
	SECURITY_GITLEAKS_CONFIG="$root/.gitleaks.toml" scripts/security-gitleaks.sh "$root"
}

scan_gitleaks_dir() {
	echo ">> gitleaks (working tree)"
	# Keep the scan target repository-relative. An absolute path to a wt-managed
	# checkout contains `/.wt/` and would match the intentional sibling-worktree
	# exclusion in the canonical config.
	gitleaks dir --redact --no-banner --config="$root/.gitleaks.toml" .
}

scan_actionlint() {
	echo ">> actionlint"
	actionlint
}

scan_shellcheck() {
	echo ">> shellcheck"
	local shell_files=()
	local regular_shell_files=()
	readarray -d '' shell_files < <(git ls-files -z -- '*.sh' '*.bash')
	for file in "${shell_files[@]}"; do
		case "$file" in
			test/lib/smoke.sh | test/e2e/codex-lifecycle.sh | test/e2e/linux-smoke.sh | test/e2e/npm-staging-path.sh | test/install/smoke.sh | test/integration/linux-smoke.sh) ;;
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
		test/e2e/codex-lifecycle.sh \
		test/e2e/linux-smoke.sh \
		test/e2e/npm-staging-path.sh \
		test/install/smoke.sh \
		test/integration/linux-smoke.sh
	# The shared variable is consumed by the entrypoints after they source it.
	shellcheck --exclude=SC2034 test/lib/smoke.sh
}

run_go_security() {
	run_scanner govulncheck scan_govulncheck
	run_scanner gosec scan_gosec
}

run_go_static() {
	run_scanner staticcheck scan_staticcheck
}

run_repository_policy() {
	run_scanner gitleaks-git scan_gitleaks_git
	run_scanner gitleaks-dir scan_gitleaks_dir
	run_scanner actionlint scan_actionlint
	run_scanner shellcheck scan_shellcheck
}

case "$group" in
	go-security) run_go_security ;;
	go-static) run_go_static ;;
	repository-policy) run_repository_policy ;;
	all)
		run_go_security
		run_go_static
		run_repository_policy
		;;
esac

printf '>> security: %s passed\n' "$group"
