#!/usr/bin/env node
"use strict";

const fs = require("fs");
const path = require("path");
const { spawnSync } = require("child_process");

const platformPackages = {
  "darwin arm64": "@projmux/darwin-arm64",
  "darwin x64": "@projmux/darwin-x64",
  "linux arm64": "@projmux/linux-arm64",
  "linux x64": "@projmux/linux-x64"
};

function packageName() {
  return platformPackages[`${process.platform} ${process.arch}`];
}

function executableCandidates() {
  const candidates = [];
  if (process.env.PROJMUX_BINARY) {
    candidates.push(process.env.PROJMUX_BINARY);
  }

  const pkg = packageName();
  if (pkg) {
    try {
      candidates.push(require.resolve(`${pkg}/bin/projmux`));
    } catch (_) {
      // Optional dependency may be omitted by unsupported platforms or install
      // flags. Fall through to the local development binary candidate.
    }
  }

  candidates.push(path.resolve(__dirname, "..", ".bin", "projmux"));
  return candidates;
}

function resolveExecutable() {
  for (const candidate of executableCandidates()) {
    if (candidate && fs.existsSync(candidate)) {
      return candidate;
    }
  }
  return "";
}

const exe = resolveExecutable();
if (!exe) {
  const pkg = packageName();
  const target = `${process.platform}/${process.arch}`;
  const detail = pkg
    ? `Expected optional dependency ${pkg} to provide bin/projmux.`
    : "No npm platform package is defined for this target.";
  console.error(`projmux: unsupported or incomplete npm install for ${target}.`);
  console.error(detail);
  console.error("Install from GitHub Releases or build from source for this platform.");
  process.exit(1);
}

const result = spawnSync(exe, process.argv.slice(2), {
  stdio: "inherit",
  env: {
    ...process.env,
    PROJMUX_INSTALLER: process.env.PROJMUX_INSTALLER || "npm"
  }
});

if (result.error) {
  console.error(`projmux: failed to execute ${exe}: ${result.error.message}`);
  process.exit(1);
}
process.exit(result.status === null ? 1 : result.status);
