#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="$root/dist/npm"
pack=0
version=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --out)
      out="$2"
      shift 2
      ;;
    --version)
      version="$2"
      shift 2
      ;;
    --pack)
      pack=1
      shift
      ;;
    -h|--help)
      cat <<'USAGE'
Usage: scripts/package-npm.sh [--version X.Y.Z] [--out DIR] [--pack]

Builds Go binaries for npm platform packages and stages:
  - projmux
  - @projmux/linux-x64
  - @projmux/linux-arm64
  - @projmux/darwin-x64
  - @projmux/darwin-arm64

Use --pack to run npm pack in each staged package directory.
USAGE
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$version" ]]; then
  version="$(node -p "require('./package.json').version" 2>/dev/null)"
fi

rm -rf "$out"
mkdir -p "$out"

patch_version() {
  local file="$1"
  node - "$file" "$version" <<'NODE'
const fs = require("fs");
const [file, version] = process.argv.slice(2);
const pkg = JSON.parse(fs.readFileSync(file, "utf8"));
pkg.version = version;
if (pkg.optionalDependencies) {
  for (const name of Object.keys(pkg.optionalDependencies)) {
    pkg.optionalDependencies[name] = version;
  }
}
fs.writeFileSync(file, `${JSON.stringify(pkg, null, 2)}\n`);
NODE
}

stage_main() {
  local dir="$out/projmux"
  mkdir -p "$dir/npm"
  cp "$root/package.json" "$dir/package.json"
  cp "$root/npm/projmux.js" "$dir/npm/projmux.js"
  cp "$root/README.md" "$root/README-ko.md" "$root/LICENSE" "$dir/"
  chmod 0755 "$dir/npm/projmux.js"
  patch_version "$dir/package.json"
}

stage_platform() {
  local goos="$1"
  local goarch="$2"
  local npm_arch="$3"
  local pkg="@projmux/${goos}-${npm_arch}"
  local dir="$out/$pkg"

  mkdir -p "$dir/bin"
  cp "$root/npm/platform/${goos}-${npm_arch}/package.json" "$dir/package.json"
  cp "$root/README.md" "$root/LICENSE" "$dir/"
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
    go build -trimpath \
      -ldflags "-s -w -X github.com/crevissepartners/projmux/internal/version.current=${version}" \
      -o "$dir/bin/projmux" "$root/cmd/projmux"
  chmod 0755 "$dir/bin/projmux"
  patch_version "$dir/package.json"
}

stage_platform linux amd64 x64
stage_platform linux arm64 arm64
stage_platform darwin amd64 x64
stage_platform darwin arm64 arm64
stage_main

if [[ "$pack" -eq 1 ]]; then
  for dir in \
    "$out/@projmux/linux-x64" \
    "$out/@projmux/linux-arm64" \
    "$out/@projmux/darwin-x64" \
    "$out/@projmux/darwin-arm64" \
    "$out/projmux"; do
    (cd "$dir" && npm pack --dry-run)
  done
fi

echo "staged npm packages in $out"
