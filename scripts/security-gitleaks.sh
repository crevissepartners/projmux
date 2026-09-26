#!/usr/bin/env bash
set -euo pipefail

if (($# != 1)); then
	echo "usage: scripts/security-gitleaks.sh <repository>" >&2
	exit 2
fi

repository="$1"
mode="${SECURITY_GITLEAKS_HISTORY_MODE:-full}"
config="${SECURITY_GITLEAKS_CONFIG:-$repository/.gitleaks.toml}"

if [[ ! -d "$repository" || ! -f "$config" || -L "$config" ]]; then
	echo "security: gitleaks repository or canonical config is missing" >&2
	exit 2
fi

case "$mode" in
	full)
		# Without --log-opts gitleaks runs `git log --full-history --all`, which
		# reads every fetched branch, so a finding on an open branch would fail
		# main. Scan only history reachable from HEAD; branch commits are
		# covered by the pull-request range scan.
		head_commit="$(git -C "$repository" rev-parse --verify 'HEAD^{commit}')"
		echo ">> gitleaks (history reachable from HEAD)"
		gitleaks git --redact --no-banner --config="$config" --log-opts="$head_commit" "$repository"
		;;
	range)
		base="${SECURITY_GITLEAKS_BASE:-}"
		head="${SECURITY_GITLEAKS_HEAD:-}"
		if [[ ! "$base" =~ ^[0-9a-fA-F]{40}$ || ! "$head" =~ ^[0-9a-fA-F]{40}$ ]]; then
			echo "security: range scan requires exact 40-hex SECURITY_GITLEAKS_BASE and SECURITY_GITLEAKS_HEAD" >&2
			exit 2
		fi
		git -C "$repository" cat-file -e "$base^{commit}"
		git -C "$repository" cat-file -e "$head^{commit}"
		if ! git -C "$repository" merge-base --is-ancestor "$base" "$head"; then
			printf 'security: range base is not an ancestor of head: %s..%s\n' "$base" "$head" >&2
			exit 2
		fi
		echo ">> gitleaks (pull-request commit range)"
		gitleaks git --redact --no-banner --config="$config" --log-opts="$base..$head" "$repository"
		;;
	*)
		printf 'security: invalid SECURITY_GITLEAKS_HISTORY_MODE: %s (expected full or range)\n' "$mode" >&2
		exit 2
		;;
esac
