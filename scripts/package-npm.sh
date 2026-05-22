#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="$root/dist/npm"
pack=0
version=""
platform_packages=(
  "@projmux/linux-x64"
  "@projmux/linux-arm64"
  "@projmux/darwin-x64"
  "@projmux/darwin-arm64"
)

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
fs.writeFileSync(file, `${JSON.stringify(pkg, null, 2)}\n`);
NODE
}

patch_root_package() {
  local file="$1"
  node - "$file" "$version" "${platform_packages[@]}" <<'NODE'
const fs = require("fs");
const [file, version, ...platformPackages] = process.argv.slice(2);
const pkg = JSON.parse(fs.readFileSync(file, "utf8"));
pkg.version = version;
pkg.optionalDependencies = Object.fromEntries(
  platformPackages.map((name) => [name, version])
);
fs.writeFileSync(file, `${JSON.stringify(pkg, null, 2)}\n`);
NODE
}

assert_staged_versions() {
  node - "$out" "$version" "${platform_packages[@]}" <<'NODE'
const fs = require("fs");
const path = require("path");
const [out, version, ...platformPackages] = process.argv.slice(2);

function readPackage(packagePath) {
  return JSON.parse(fs.readFileSync(packagePath, "utf8"));
}

function fail(message) {
  console.error(message);
  process.exitCode = 1;
}

const rootPackagePath = path.join(out, "projmux", "package.json");
const rootPackage = readPackage(rootPackagePath);
if (rootPackage.version !== version) {
  fail(`expected projmux version ${version}, got ${rootPackage.version}`);
}

const optionalDependencies = rootPackage.optionalDependencies || {};
const optionalNames = Object.keys(optionalDependencies).sort();
const expectedNames = [...platformPackages].sort();
if (JSON.stringify(optionalNames) !== JSON.stringify(expectedNames)) {
  fail(`expected root optionalDependencies ${expectedNames.join(", ")}, got ${optionalNames.join(", ")}`);
}
for (const name of platformPackages) {
  if (optionalDependencies[name] !== version) {
    fail(`expected root optionalDependency ${name}@${version}, got ${optionalDependencies[name]}`);
  }
}

for (const name of platformPackages) {
  const platformPackagePath = path.join(out, name, "package.json");
  const platformPackage = readPackage(platformPackagePath);
  if (platformPackage.version !== version) {
    fail(`expected ${name} version ${version}, got ${platformPackage.version}`);
  }
}
NODE
}

stage_main() {
  local dir="$out/projmux"
  mkdir -p "$dir/npm"
  mkdir -p "$dir/docs"
  cp "$root/package.json" "$dir/package.json"
  cp "$root/npm/projmux.js" "$dir/npm/projmux.js"
  cp "$root/README.md" "$root/README-ko.md" "$root/LICENSE" "$dir/"
  cp "$root"/docs/*.md "$dir/docs/"
  if [[ -d "$root/docs/assets" ]]; then
    mkdir -p "$dir/docs/assets"
    cp "$root"/docs/assets/* "$dir/docs/assets/"
  fi
  chmod 0755 "$dir/npm/projmux.js"
  patch_root_package "$dir/package.json"
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

stage_jobs=()
stage_names=()

run_stage() {
  local name="$1"
  shift
  "$@" &
  stage_jobs+=("$!")
  stage_names+=("$name")
}

run_stage "@projmux/linux-x64" stage_platform linux amd64 x64
run_stage "@projmux/linux-arm64" stage_platform linux arm64 arm64
run_stage "@projmux/darwin-x64" stage_platform darwin amd64 x64
run_stage "@projmux/darwin-arm64" stage_platform darwin arm64 arm64
run_stage "projmux" stage_main

stage_status=0
for i in "${!stage_jobs[@]}"; do
  if ! wait "${stage_jobs[$i]}"; then
    echo "failed to stage ${stage_names[$i]}" >&2
    stage_status=1
  fi
done
if [[ "$stage_status" -ne 0 ]]; then
  exit "$stage_status"
fi

assert_staged_versions

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
