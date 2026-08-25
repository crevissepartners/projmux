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
		echo ">> gitleaks (full git history)"
		gitleaks git --redact --no-banner --config="$config" "$repository"
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
