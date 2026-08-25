#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"
export PATH="${SECURITY_BIN_DIR:-$root/.bin/security-tools}:$PATH"

evidence_dir="${SECURITY_CONTRACT_EVIDENCE_DIR:-$(mktemp -d)}"
ephemeral_evidence=0
if [[ -z "${SECURITY_CONTRACT_EVIDENCE_DIR:-}" ]]; then
	ephemeral_evidence=1
fi
mkdir -p "$evidence_dir"

synthetic_repo="$(mktemp -d)"
fake_bin="$(mktemp -d)"
failure_evidence="$(mktemp -d)"
cache_bin="$(mktemp -d)"
cleanup() {
	rm -rf -- "$synthetic_repo" "$fake_bin" "$failure_evidence" "$cache_bin"
	if [[ "$ephemeral_evidence" == "1" ]]; then
		rm -rf -- "$evidence_dir"
	fi
}
trap cleanup EXIT HUP INT TERM

expected_inventory=$'go-security:govulncheck,gosec\ngo-static:staticcheck\nrepository-policy:gitleaks-git,gitleaks-dir,actionlint,shellcheck'
actual_inventory="$(scripts/security.sh --list)"
if [[ "$actual_inventory" != "$expected_inventory" ]]; then
	echo "security contract: scanner inventory changed" >&2
	diff -u <(printf '%s\n' "$expected_inventory") <(printf '%s\n' "$actual_inventory") >&2 || true
	exit 1
fi

python3 - "$evidence_dir/parity-contract.json" <<'PY'
import hashlib
import json
import pathlib
import re
import subprocess
import sys

root = pathlib.Path.cwd()
security = (root / "scripts/security.sh").read_text(encoding="utf-8")
expected_rules = "G101,G102,G103,G104,G106,G107,G108,G109,G110,G111,G112,G113,G114,G115,G116,G117,G118,G119,G120,G121,G122,G123,G124,G201,G202,G203,G204,G301,G302,G303,G304,G305,G306,G307,G401,G402,G403,G404,G405,G406,G407,G408,G501,G502,G503,G504,G505,G506,G507,G601,G602,G701,G702,G703,G704,G705,G706,G707,G708,G709,G710"
match = re.search(r'gosec_rules="([G0-9,]+)"', security)
if match is None or match.group(1) != expected_rules:
    raise SystemExit("security contract: exact gosec rule set changed")

required_fragments = (
    "govulncheck ./...",
    'gosec -quiet -include="$gosec_rules" -fmt=json -out="$tmpdir/gosec.json" ./...',
    "staticcheck -f json ./...",
    "--baseline .security/gosec-baseline.json",
    "--baseline .security/staticcheck-baseline.json",
    "run_scanner gitleaks-git",
    "run_scanner gitleaks-dir",
    "run_scanner actionlint",
    "run_scanner shellcheck",
)
for fragment in required_fragments:
    if fragment not in security:
        raise SystemExit(f"security contract: scanner/rule/baseline fragment missing: {fragment}")

baseline_expectations = {
    ".security/gosec-baseline.json": "9772d6328c5d21a164610a1a4e279ec112c5e582b60811e9ef5d2bafcf161713",
    ".security/staticcheck-baseline.json": "a7ea8324b7cfad1a71ee3413f4c0401a6d574ab3671a4d31cd394e4530fc6807",
}
baseline_digests = {}
for name, expected in baseline_expectations.items():
    digest = hashlib.sha256((root / name).read_bytes()).hexdigest()
    if digest != expected:
        raise SystemExit(f"security contract: reviewed baseline changed: {name}")
    baseline_digests[name] = digest

packages = subprocess.check_output(
    ["go", "list", "-f", "{{.ImportPath}}", "./..."], text=True
).splitlines()
packages = sorted(packages)
package_bytes = ("\n".join(packages) + "\n").encode()
current = json.loads(
    (root / ".security/security-current-findings.json").read_text(encoding="utf-8")
)
package_digest = hashlib.sha256(package_bytes).hexdigest()
if len(packages) != current["package_count"] or package_digest != current["package_set_sha256"]:
    raise SystemExit("security contract: canonical ./... package set differs from controlled pre-split parity")
for name, digest in baseline_digests.items():
    scanner = "gosec" if "gosec" in name else "staticcheck"
    if current["scanners"][scanner]["baseline_sha256"] != digest:
        raise SystemExit(f"security contract: {scanner} parity baseline digest differs")
artifact = {
    "schema": "projmux.security.parity-contract.v1",
    "scanner_inventory": [
        "govulncheck", "gosec", "staticcheck", "gitleaks-git",
        "gitleaks-dir", "actionlint", "shellcheck",
    ],
    "gosec_rule_sha256": hashlib.sha256(expected_rules.encode()).hexdigest(),
    "baseline_sha256": baseline_digests,
    "package_count": len(packages),
    "package_set_sha256": package_digest,
}
pathlib.Path(sys.argv[1]).write_text(
    json.dumps(artifact, sort_keys=True, separators=(",", ":")) + "\n",
    encoding="utf-8",
)
PY

python3 - <<'PY'
import pathlib
import re

workflow = pathlib.Path(".github/workflows/ci.yml").read_text(encoding="utf-8")
for job in ("security-go", "security-static", "security-policy"):
    if not re.search(rf"^  {re.escape(job)}:\n", workflow, re.MULTILINE):
        raise SystemExit(f"security contract: missing scanner child job {job}")
if len(re.findall(r"^  security-(?:go|static|policy):\n", workflow, re.MULTILINE)) != 3:
    raise SystemExit("security contract: scanner topology is not exactly three-way")
required = (
    '    name: Test',
    '    if: always()',
    'SECURITY_GITLEAKS_HISTORY_MODE: ${{ github.event_name == \'pull_request\' && \'range\' || \'full\' }}',
    'SECURITY_GITLEAKS_BASE: ${{ github.event.pull_request.base.sha }}',
    'SECURITY_GITLEAKS_HEAD: ${{ github.event.pull_request.head.sha }}',
    'fetch-depth: 0',
    'schedule:',
    "hashFiles('.security/security-tools.versions')",
    "hashFiles('scripts/security-tools.sh')",
    'steps.setup-go.outputs.go-version',
    '${{ runner.os }}-${{ runner.arch }}',
)
for fragment in required:
    if fragment not in workflow:
        raise SystemExit(f"security contract: workflow contract missing: {fragment}")
if workflow.count("uses: actions/cache/save@v5") != 1:
    raise SystemExit("security contract: exact tool cache must have one writer")
if "hashFiles('go.mod')" in workflow:
    raise SystemExit("security contract: tool cache must not key on application go.mod")
PY

# Exercise the real installer against one exact empty bin directory, then the
# same directory again. The first call must populate every pinned binary; the
# second must be a verified hit without reinstalling anything.
SECURITY_BIN_DIR="$cache_bin" \
	SECURITY_TOOL_MANIFEST="$root/.security/security-tools.versions" \
	GOCACHE="$root/.bin/security-cache-contract" \
	scripts/security-tools.sh >"$evidence_dir/cache-miss.log" 2>&1
SECURITY_BIN_DIR="$cache_bin" \
	SECURITY_TOOL_MANIFEST="$root/.security/security-tools.versions" \
	GOCACHE="$root/.bin/security-cache-contract" \
	scripts/security-tools.sh >"$evidence_dir/cache-hit.log" 2>&1
if ! grep -Fxq 'security_tools_cache=miss' "$evidence_dir/cache-miss.log" ||
	! grep -Fxq 'security_tools_cache=hit' "$evidence_dir/cache-hit.log"; then
	echo "security contract: real installer did not converge from miss to hit" >&2
	exit 1
fi
cmp "$root/.security/security-tools.versions" "$cache_bin/.versions"
for tool in govulncheck gosec staticcheck gitleaks actionlint; do
	[[ -x "$cache_bin/$tool" && ! -L "$cache_bin/$tool" ]]
done

printf '{"Golang errors":{},"Issues":[]}\n' >"$failure_evidence/empty-gosec.json"
set +e
python3 scripts/security-baseline.py \
	--tool gosec \
	--root "$root" \
	--report "$failure_evidence/empty-gosec.json" \
	--baseline .security/gosec-baseline.json \
	--scanner-status 0 \
	--expected-parity .security/security-current-findings.json \
	>"$failure_evidence/missing-findings.log" 2>&1
parity_status=$?
set -e
if [[ "$parity_status" != "2" ]] || ! grep -Fq 'differs from controlled parity' "$failure_evidence/missing-findings.log"; then
	echo "security contract: missing scanner findings did not fail exact parity" >&2
	exit 1
fi

# A secret introduced and removed inside the PR range must still fail the
# history scan. Build the synthetic value from short pieces so the repository
# policy scan does not contain the test credential itself.
git -C "$synthetic_repo" init -q
git -C "$synthetic_repo" config user.email security-contract@invalid
git -C "$synthetic_repo" config user.name security-contract
printf 'clean\n' >"$synthetic_repo/payload.txt"
git -C "$synthetic_repo" add payload.txt
git -C "$synthetic_repo" commit -qm base
base="$(git -C "$synthetic_repo" rev-parse HEAD)"
piece_one='a9F3xK7m'
piece_two='Q2vR8pL4'
piece_three='nT6yW1cD'
piece_four='5sH0jB7uE9zN3gC8'
synthetic_value="$piece_one$piece_two$piece_three$piece_four"
printf 'api_key = "%s"\n' "$synthetic_value" >"$synthetic_repo/payload.txt"
git -C "$synthetic_repo" commit -qam add-secret
printf 'clean\n' >"$synthetic_repo/payload.txt"
git -C "$synthetic_repo" commit -qam remove-secret
head="$(git -C "$synthetic_repo" rev-parse HEAD)"
set +e
SECURITY_GITLEAKS_HISTORY_MODE=range \
	SECURITY_GITLEAKS_BASE="$base" \
	SECURITY_GITLEAKS_HEAD="$head" \
	SECURITY_GITLEAKS_CONFIG="$root/.gitleaks.toml" \
	scripts/security-gitleaks.sh "$synthetic_repo" >"$evidence_dir/synthetic-secret.log" 2>&1
secret_status=$?
set -e
if [[ "$secret_status" != "1" ]]; then
	printf 'security contract: removed synthetic secret was not detected (status %s)\n' "$secret_status" >&2
	exit 1
fi
if grep -Fq "$synthetic_value" "$evidence_dir/synthetic-secret.log"; then
	echo "security contract: gitleaks log exposed the synthetic secret" >&2
	exit 1
fi

# Scanner failures must preserve terminal typed evidence and must not fall
# through to a later scanner as if the failed child were green.
printf '#!/usr/bin/env bash\nexit 7\n' >"$fake_bin/govulncheck"
printf '#!/usr/bin/env bash\nexit 0\n' >"$fake_bin/gosec"
chmod +x "$fake_bin/govulncheck" "$fake_bin/gosec"
set +e
SECURITY_BIN_DIR="$fake_bin" SECURITY_EVIDENCE_DIR="$failure_evidence" \
	scripts/security.sh go-security >/dev/null 2>&1
failure_status=$?
set -e
if [[ "$failure_status" != "7" ]]; then
	printf 'security contract: intentional scanner failure returned %s, expected 7\n' "$failure_status" >&2
	exit 1
fi
expected_failure=$'{"schema":"projmux.security.scanner.v1","group":"go-security","scanner":"govulncheck","phase":"begin","status":"running"}\n{"schema":"projmux.security.scanner.v1","group":"go-security","scanner":"govulncheck","phase":"terminal","status":"fail"}'
if [[ "$(cat "$failure_evidence/events.jsonl")" != "$expected_failure" ]]; then
	echo "security contract: intentional scanner failure evidence is incomplete" >&2
	exit 1
fi

children=(security-go security-static security-policy fmt unit darwin-native npm-pack integration e2e)
success_json="$(python3 - "${children[@]}" <<'PY'
import json, sys
print(json.dumps({name: {"result": "success"} for name in sys.argv[1:]}))
PY
)"
required_args=()
for child in "${children[@]}"; do
	required_args+=(--required "$child")
done
python3 scripts/required-gate.py --results-json "$success_json" "${required_args[@]}" >"$evidence_dir/aggregate-success.log"
failure_json="$(python3 - "${children[@]}" <<'PY'
import json, sys
print(json.dumps({
    name: {"result": "failure" if name == "security-static" else "success"}
    for name in sys.argv[1:]
}))
PY
)"
set +e
python3 scripts/required-gate.py --results-json "$failure_json" "${required_args[@]}" >"$evidence_dir/aggregate-failure.log" 2>&1
aggregate_status=$?
set -e
if [[ "$aggregate_status" != "1" ]] || ! grep -Fq 'security-static=failure' "$evidence_dir/aggregate-failure.log"; then
	echo "security contract: child failure did not make the stable aggregate red" >&2
	exit 1
fi

actionlint
echo ">> security contract: scanner parity, history range, evidence, aggregate, and actionlint passed"
