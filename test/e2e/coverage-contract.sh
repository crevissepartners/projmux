#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"

mkdir -p "$root/.bin"
tmpdir="$(mktemp -d "$root/.bin/e2e-coverage.XXXXXX")"
cleanup() {
	rm -rf -- "$tmpdir"
}
trap cleanup EXIT HUP INT TERM

manifest="test/e2e/ags-oedr-manifest.json"
python3 scripts/e2e-coverage.py --manifest "$manifest" --output "$tmpdir/first.json" >"$tmpdir/first.stdout"
python3 scripts/e2e-coverage.py --manifest "$manifest" --output "$tmpdir/repeat.json" >"$tmpdir/repeat.stdout"
cmp "$tmpdir/first.json" "$tmpdir/repeat.json"
grep -Fq '"orphan_count":0' "$tmpdir/first.json"
grep -Fq '"scenario_count":21' "$tmpdir/first.json"
grep -Fq '"moved_former_cells":60' "$tmpdir/first.json"
grep -Fq '"moved_e2e_sentinel_cells":5' "$tmpdir/first.json"
grep -Fq '"merged_evidence_count":6' "$tmpdir/first.json"
grep -Fq '"merged_lower_test_count":59' "$tmpdir/first.json"

if [[ "${E2E_COVERAGE_SKIP_GO:-0}" != "1" ]]; then
	mkdir -p "$root/.bin/e2e-coverage-go-cache"
	GOCACHE="$root/.bin/e2e-coverage-go-cache" \
		go test ./internal/app -run '^TestPluralReadContextSelectorMatrix$' -count=1
fi

python3 - "$manifest" "$tmpdir/orphan.json" "$tmpdir/evidence.json" "$tmpdir/symbol.json" \
	"$tmpdir/merged-missing.json" "$tmpdir/merged-marker.json" \
	"$tmpdir/merged-duplicate.json" "$tmpdir/merged-symbol.json" \
	"$tmpdir/merged-lower-missing.json" "$tmpdir/merged-guarantee.json" \
	"$tmpdir/merged-supporting-missing.json" "$tmpdir/merged-supporting-marker.json" <<'PY'
import copy
import json
import pathlib
import sys

source = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))

def merged_row(document, evidence_id):
    return next(row for row in document["merged_evidence"] if row["id"] == evidence_id)

orphan = copy.deepcopy(source)
orphan["scenarios"] = [row for row in orphan["scenarios"] if row["id"] != "L19"]
pathlib.Path(sys.argv[2]).write_text(json.dumps(orphan) + "\n", encoding="utf-8")
evidence = copy.deepcopy(source)
evidence["moved_matrices"][0]["lower_layer"]["negative"] = ""
pathlib.Path(sys.argv[3]).write_text(json.dumps(evidence) + "\n", encoding="utf-8")
symbol = copy.deepcopy(source)
symbol["moved_matrices"][0]["lower_layer"]["symbol"] = "TestMissingLowerEvidence"
symbol["moved_matrices"][0]["lower_layer"]["selector"] = "go test ./internal/app -run ^TestMissingLowerEvidence$ -count=1"
pathlib.Path(sys.argv[4]).write_text(json.dumps(symbol) + "\n", encoding="utf-8")
merged_missing = copy.deepcopy(source)
merged_missing["merged_evidence"] = [
    row for row in merged_missing["merged_evidence"]
    if row["id"] != "L19.merged-768-control-stop"
]
pathlib.Path(sys.argv[5]).write_text(json.dumps(merged_missing) + "\n", encoding="utf-8")
merged_marker = copy.deepcopy(source)
merged_row(merged_marker, "L17.merged-768-project-stop")["e2e"]["marker"] += " typo"
pathlib.Path(sys.argv[6]).write_text(json.dumps(merged_marker) + "\n", encoding="utf-8")
merged_duplicate = copy.deepcopy(source)
merged_duplicate["merged_evidence"].append(copy.deepcopy(merged_duplicate["merged_evidence"][0]))
pathlib.Path(sys.argv[7]).write_text(json.dumps(merged_duplicate) + "\n", encoding="utf-8")
merged_symbol = copy.deepcopy(source)
merged_row(merged_symbol, "L17.merged-768-project-stop")["lower_layer"][0]["symbol"] = "TestMissingMergedEvidence"
merged_row(merged_symbol, "L17.merged-768-project-stop")["lower_layer"][0]["selector"] = "go test ./internal/app -run ^TestMissingMergedEvidence$ -count=1"
pathlib.Path(sys.argv[8]).write_text(json.dumps(merged_symbol) + "\n", encoding="utf-8")
merged_lower_missing = copy.deepcopy(source)
merged_row(merged_lower_missing, "L11.merged-770-runtime-fresh")["lower_layer"].pop()
pathlib.Path(sys.argv[9]).write_text(json.dumps(merged_lower_missing) + "\n", encoding="utf-8")
merged_guarantee = copy.deepcopy(source)
del merged_row(merged_guarantee, "L19.merged-768-control-stop")["guarantees"]["fixed_point"]
pathlib.Path(sys.argv[10]).write_text(json.dumps(merged_guarantee) + "\n", encoding="utf-8")
merged_supporting_missing = copy.deepcopy(source)
merged_row(merged_supporting_missing, "L17.merged-771-create-authority")["supporting"].pop()
pathlib.Path(sys.argv[11]).write_text(json.dumps(merged_supporting_missing) + "\n", encoding="utf-8")
merged_supporting_marker = copy.deepcopy(source)
merged_row(merged_supporting_marker, "L17.merged-771-create-authority")["supporting"][0]["marker"] += " typo"
pathlib.Path(sys.argv[12]).write_text(json.dumps(merged_supporting_marker) + "\n", encoding="utf-8")
PY

for invalid in \
	"$tmpdir/orphan.json" \
	"$tmpdir/evidence.json" \
	"$tmpdir/symbol.json" \
	"$tmpdir/merged-missing.json" \
	"$tmpdir/merged-marker.json" \
	"$tmpdir/merged-duplicate.json" \
	"$tmpdir/merged-symbol.json" \
	"$tmpdir/merged-lower-missing.json" \
	"$tmpdir/merged-guarantee.json" \
	"$tmpdir/merged-supporting-missing.json" \
	"$tmpdir/merged-supporting-marker.json"; do
	set +e
	python3 scripts/e2e-coverage.py --manifest "${invalid#"$root"/}" >"$invalid.out" 2>"$invalid.err"
	status=$?
	set -e
	if [[ "$status" != "1" ]]; then
		printf 'coverage contract: invalid manifest passed: %s (status %s)\n' "$invalid" "$status" >&2
		exit 1
	fi
done

if grep -Eq '/home/|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY|github_pat_|ghp_' "$manifest"; then
	echo "coverage contract: manifest contains a machine path or secret-shaped value" >&2
	exit 1
fi

echo ">> E2E coverage contract: 21 scenarios, four shards, moved matrix and merged L11/L17/L18/L19 parity, orphan 0"
