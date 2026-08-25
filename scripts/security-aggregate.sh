#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"

mode="${SECURITY_AGGREGATE_MODE:-parallel}"
case "$mode" in
	parallel | serial) ;;
	*)
		printf 'security aggregate: invalid mode: %s (expected parallel or serial)\n' "$mode" >&2
		exit 2
		;;
esac

artifact_root="${SECURITY_EVIDENCE_DIR:-$(mktemp -d)}"
ephemeral_artifacts=0
if [[ -z "${SECURITY_EVIDENCE_DIR:-}" ]]; then
	ephemeral_artifacts=1
fi
mkdir -p "$artifact_root"
cleanup() {
	if [[ "$ephemeral_artifacts" == "1" ]]; then
		rm -rf -- "$artifact_root"
	fi
}
trap cleanup EXIT HUP INT TERM

cache_root="${SECURITY_PARALLEL_GOCACHE_ROOT:-$root/.bin/security-go-cache}"
mkdir -p "$cache_root/go-security" "$cache_root/go-static"

groups=(go-security go-static repository-policy)
declare -A statuses=()
declare -A pids=()

run_group() {
	local group="$1"
	local group_cache=""
	case "$group" in
		go-security | go-static) group_cache="$cache_root/$group" ;;
	esac
	if [[ -n "$group_cache" ]]; then
		GOCACHE="$group_cache" \
			SECURITY_EVIDENCE_DIR="$artifact_root/$group" \
			scripts/security.sh "$group"
	else
		SECURITY_EVIDENCE_DIR="$artifact_root/$group" scripts/security.sh "$group"
	fi
}

started_at="$(date +%s)"
if [[ "$mode" == "parallel" ]]; then
	for group in "${groups[@]}"; do
		run_group "$group" >"$artifact_root/$group.log" 2>&1 &
		pids["$group"]=$!
	done
	set +e
	for group in "${groups[@]}"; do
		wait "${pids[$group]}"
		statuses["$group"]=$?
	done
	set -e
else
	for group in "${groups[@]}"; do
		set +e
		run_group "$group" >"$artifact_root/$group.log" 2>&1
		statuses["$group"]=$?
		set -e
	done
fi

failed=0
for group in "${groups[@]}"; do
	printf '>> security child: %s\n' "$group"
	cat "$artifact_root/$group.log"
	if [[ "${statuses[$group]}" != "0" ]]; then
		printf 'security aggregate: child failed: %s (status %s)\n' "$group" "${statuses[$group]}" >&2
		failed=1
	fi
done

elapsed="$(( $(date +%s) - started_at ))"
printf '{"schema":"projmux.security.aggregate.v1","mode":"%s","elapsed_seconds":%s,"children":{"go-security":%s,"go-static":%s,"repository-policy":%s}}\n' \
	"$mode" "$elapsed" "${statuses[go-security]}" "${statuses[go-static]}" "${statuses[repository-policy]}" \
	>"$artifact_root/aggregate.json"
if [[ "$ephemeral_artifacts" == "1" ]]; then
	printf 'security_aggregate mode=%s elapsed_seconds=%s artifact=ephemeral\n' "$mode" "$elapsed"
else
	printf 'security_aggregate mode=%s elapsed_seconds=%s artifact=%s\n' "$mode" "$elapsed" "$artifact_root/aggregate.json"
fi

if [[ "$failed" != "0" ]]; then
	exit 1
fi
echo ">> security: all three child gates passed"
