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

// The install residue notice.
//
// `npm install -g projmux` replaces the binary on disk and leaves every
// long-lived projmux process on the image it started with. npm users have no
// reason to run `projmux doctor`, so without this they are never told.
//
// There is no postinstall hook here on purpose. A lifecycle script is skipped
// by `--ignore-scripts` and by many global and CI installs, and npm buffers or
// reorders its output unless `--foreground-scripts`. This wrapper is the bin
// entrypoint: it cannot be skipped, and it writes straight to the user's
// terminal.
//
// The install signal is the absence of this watermark. npm deletes and rewrites
// the package directory on install and update, so a missing watermark is
// exactly "this package directory is new". No fingerprinting, no second copy of
// the Go side's XDG path logic, and nothing written outside this directory.
const installResidueWatermark = path.join(__dirname, ".install-residue-reported");

// claimInstallResidueReport reports whether this invocation owns the one notice
// this install gets.
//
// The watermark is created before the report runs, so a crash in the report
// cannot arm it again. A watermark that cannot be written means the notice is
// never shown: an unwritable package directory must not produce this notice on
// every single invocation forever.
//
// A non-TTY run (a hook, CI, a pipe) claims nothing and leaves the watermark
// missing, so the next interactive run is the one that reports. That is the
// point: the notice should land on a run a human is looking at.
function claimInstallResidueReport() {
  try {
    if (!process.stderr.isTTY) {
      return false;
    }
    fs.writeFileSync(installResidueWatermark, `${new Date().toISOString()}\n`, {
      flag: "wx"
    });
    return true;
  } catch (_) {
    return false;
  }
}

// reportInstallResidue runs the census after the user's command has finished,
// so it is the last thing on screen and never delays or interleaves with the
// real command. It is strictly additive: it cannot throw and it cannot change
// this wrapper's exit code.
function reportInstallResidue(binary) {
  try {
    if (!claimInstallResidueReport()) {
      return;
    }
    spawnSync(binary, ["internal", "install-residue"], {
      stdio: ["ignore", "ignore", "inherit"],
      env: {
        ...process.env,
        PROJMUX_INSTALLER: "npm"
      }
    });
  } catch (_) {
    // The wrapper's job is to run projmux. A failed notice is not a failure.
  }
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
reportInstallResidue(exe);
process.exit(result.status === null ? 1 : result.status);
