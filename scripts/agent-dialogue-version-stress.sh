#!/usr/bin/env bash
set -euo pipefail

[[ "${PMX_DIALOGUE_VERSION_STRESS:-}" == "1" ]] || {
  echo "refusing optional provider/version stress without PMX_DIALOGUE_VERSION_STRESS=1" >&2
  exit 2
}
matrix="${PMX_DIALOGUE_VERSION_MATRIX:-}"
[[ -f "$matrix" ]] || { echo "PMX_DIALOGUE_VERSION_MATRIX must name a TSV file" >&2; exit 2; }

rows=0
while IFS=$'\t' read -r label runner extra; do
  [[ -n "$label" ]] || continue
  [[ "$label" != \#* ]] || continue
  [[ -z "${extra:-}" && "$runner" == /* && -x "$runner" ]] || {
    echo "invalid matrix row: expected label<TAB>/absolute/executable-runner" >&2
    exit 2
  }
  rows=$((rows+1))
  echo ">> dialogue version row: $label"
  PMX_DIALOGUE_MATRIX_LABEL="$label" "$runner"
done <"$matrix"
[[ "$rows" -gt 0 ]] || { echo "version matrix has no rows" >&2; exit 2; }
