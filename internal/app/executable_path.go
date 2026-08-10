package app

import (
	"os"
	"regexp"
	"strings"
)

// npmRetiredDirPattern matches the directory name npm's arborist uses when it
// retires an existing package tree before reifying the replacement:
// `.<name>-<8 path-safe base64 chars>` (@npmcli/arborist retire-path.js).
var npmRetiredDirPattern = regexp.MustCompile(`^\.(.+)-[A-Za-z0-9]{8}$`)

// resolveExecutablePath is projmux's replacement for os.Executable.
//
// On Linux os.Executable reads /proc/self/exe, which follows renames. During
// `npm update -g projmux` arborist renames
// `<prefix>/node_modules/projmux` to `<prefix>/node_modules/.projmux-<hash>`,
// installs the new tree, then deletes the retired directory. A projmux process
// running across that window resolves its own path inside the doomed
// directory; persisting it into the generated tmux config, live hooks, or the
// WSL URI-protocol registry leaves the user with `... returned 127` on every
// pane focus once npm removes it.
func resolveExecutablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return canonicalNpmBinaryPath(exe), nil
}

// rawExecutablePath returns the running binary path WITHOUT npm
// canonicalization. Use it for immediate, in-process re-exec (popup handlers,
// sidebar continuations, hook-trust popups) that spawn the binary right now:
// the currently-running path is guaranteed to exist, whereas the canonical npm
// target may not have materialized yet during an update window, which would
// make the immediate spawn fail with `... returned 127`. Paths that outlive the
// process (generated tmux config, live hooks, WSL registry) must keep using
// resolveExecutablePath so they survive npm deleting the retired staging dir.
func rawExecutablePath() (string, error) {
	return os.Executable()
}

// canonicalNpmBinaryPath rewrites npm retire/staging segments back to the
// package directory they were renamed from. Only segments directly under
// `node_modules/` (or under a `node_modules/@scope/` directory) are eligible,
// so unrelated dot-directories are left alone.
//
// The rewrite is deliberately unconditional: it does not check that the
// canonical target exists. At the moment we canonicalize, the replacement tree
// may not have landed yet, while the retired directory is doomed by
// definition — an existence check would reintroduce the bug in exactly the
// window that causes it.
func canonicalNpmBinaryPath(binaryPath string) string {
	if !strings.Contains(binaryPath, "node_modules") {
		return binaryPath
	}
	segments := strings.Split(binaryPath, "/")
	for i, segment := range segments {
		if !isNpmPackageDirSlot(segments, i) {
			continue
		}
		if match := npmRetiredDirPattern.FindStringSubmatch(segment); match != nil {
			segments[i] = match[1]
		}
	}
	return strings.Join(segments, "/")
}

func isNpmPackageDirSlot(segments []string, i int) bool {
	if i == 0 {
		return false
	}
	if segments[i-1] == "node_modules" {
		return true
	}
	return i >= 2 && segments[i-2] == "node_modules" && strings.HasPrefix(segments[i-1], "@")
}
