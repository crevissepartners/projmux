package app

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The private Codex generation pool was removed, and nothing reads the files
// it left on disk anymore. `config apply` is the step every installer runs, so
// it reclaims the leftovers there:
//
//   - `<state>/projmux/codex-generations/rolling-upgrade.json` and its
//     `.flock`;
//   - `<state>/projmux/codex-generations/qualification/*.json`;
//   - `<state>/projmux/codex-generations/bundles/sha256-<64 hex>/` release
//     bundle trees;
//   - `<state>/projmux/g/<32 hex>/` private host directories: the durable
//     launch intent, its guard, and the stale app-server socket.
//
// Only entries whose names have exactly those shapes are removed, and only
// directories that end up empty are removed after them. Nothing is followed
// through a symlink: a symlink is unlinked where it stands inside a bundle
// tree, and is never descended anywhere. A tree whose socket still accepts a
// connection belongs to a retired host that outlived projmux, so it is left
// alone and reported. Any other entry is kept and listed on the report line. A
// failure is reported and never fails the apply; a rerun converges because a
// target that is already gone is not an error.
const (
	retiredCodexGenerationsDirName = "codex-generations"
	retiredCodexJournalFileName    = "rolling-upgrade.json"
	retiredCodexJournalLockName    = "rolling-upgrade.json.flock"
	retiredCodexQualificationDir   = "qualification"
	retiredCodexBundlesDirName     = "bundles"
	retiredCodexBundlePrefix       = "sha256-"
	retiredCodexBundleDigestLen    = 64
	retiredCodexPrivateHostsDir    = "g"
	retiredCodexPrivateKeyLen      = 2 * managedCodexRuntimeKeyBytes
	retiredCodexLaunchPrefix       = ".projmux-launch-"
	retiredCodexLaunchSuffix       = ".json"
	retiredCodexGuardSuffix        = ".json.guard"
	retiredCodexLaunchTokenMax     = 128
	retiredCodexSocketName         = "s"
	// retiredCodexTreeDepthMax bounds the bundle walk. A release bundle is
	// three levels deep; anything deeper is reported instead of removed.
	retiredCodexTreeDepthMax = 8
	// retiredCodexSocketProbeTimeout is a connect to a local unix socket that
	// either has a listener or does not. It never waits on a protocol reply.
	retiredCodexSocketProbeTimeout = 200 * time.Millisecond
)

type retiredCodexGenerationReclaim struct {
	// removed counts removed files, sockets and symlinks. Emptied directories
	// are removed silently and never make the report line appear on their own.
	removed int
	kept    []string
	// live lists the tree roots that still hold an answering socket.
	live   []string
	failed []string
}

// reclaimRetiredCodexGenerationFiles runs the reclamation for one apply and
// writes at most one line to stdout. It never returns an error: the apply
// result does not depend on it. Without an injected home resolver it does
// nothing, so a partially constructed command never falls back to the real
// home directory.
func (c *tmuxCommand) reclaimRetiredCodexGenerationFiles(stdout io.Writer) {
	if c.homeDir == nil || c.lookupEnv == nil {
		return
	}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	var r retiredCodexGenerationReclaim
	if err != nil {
		r.failed = append(r.failed, "resolve directories: "+err.Error())
	} else {
		r.run(paths.StateDir)
	}
	if line := r.line(); line != "" && stdout != nil {
		fmt.Fprintln(stdout, line)
	}
}

func (r *retiredCodexGenerationReclaim) run(stateDir string) {
	r.reclaimGenerations(filepath.Join(stateDir, retiredCodexGenerationsDirName))
	r.reclaimPrivateHosts(filepath.Join(stateDir, retiredCodexPrivateHostsDir))
}

// reclaimGenerations empties the pool state directory: the rolling upgrade
// journal and its lock, the stored qualifications, and the leased release
// bundles.
func (r *retiredCodexGenerationReclaim) reclaimGenerations(root string) {
	entries, ok := r.readDir(root)
	if !ok {
		return
	}
	clean := true
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		switch entry.Name() {
		case retiredCodexJournalFileName, retiredCodexJournalLockName:
			clean = r.removeFile(path) && clean
		case retiredCodexQualificationDir:
			clean = r.reclaimQualification(path) && clean
		case retiredCodexBundlesDirName:
			clean = r.reclaimBundles(path) && clean
		default:
			r.keep(path)
			clean = false
		}
	}
	if clean {
		r.removeEmptyDir(root)
	}
}

func (r *retiredCodexGenerationReclaim) reclaimQualification(root string) bool {
	entries, ok := r.readDir(root)
	if !ok {
		return false
	}
	clean := true
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if matched, _ := filepath.Match("*"+retiredCodexLaunchSuffix, entry.Name()); matched {
			clean = r.removeFile(path) && clean
		} else {
			r.keep(path)
			clean = false
		}
	}
	return clean && r.removeEmptyDir(root)
}

func (r *retiredCodexGenerationReclaim) reclaimBundles(root string) bool {
	entries, ok := r.readDir(root)
	if !ok {
		return false
	}
	clean := true
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if entry.IsDir() && retiredCodexBundleDirName(entry.Name()) {
			clean = r.removeTree(path) && clean
		} else {
			r.keep(path)
			clean = false
		}
	}
	return clean && r.removeEmptyDir(root)
}

// reclaimPrivateHosts empties `<state>/projmux/g`, whose children are the
// deterministic private roots `managedCodexRuntimeLocation` derived for one
// state domain and version.
func (r *retiredCodexGenerationReclaim) reclaimPrivateHosts(root string) {
	entries, ok := r.readDir(root)
	if !ok {
		return
	}
	clean := true
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if entry.IsDir() && retiredCodexPrivateHostDirName(entry.Name()) {
			clean = r.reclaimPrivateHost(path) && clean
		} else {
			r.keep(path)
			clean = false
		}
	}
	if clean {
		r.removeEmptyDir(root)
	}
}

// reclaimPrivateHost removes the three files one retired private host could
// have left: the durable launch intent, its guard, and the app-server socket
// nothing listens on anymore. Unlike a release bundle, a private root has a
// closed set of names, so every other entry is kept and reported.
func (r *retiredCodexGenerationReclaim) reclaimPrivateHost(root string) bool {
	live, ok := r.answeringSocket(root, 0)
	if !ok {
		return false
	}
	if live != "" {
		r.live = append(r.live, root)
		return false
	}
	entries, ok := r.readDir(root)
	if !ok {
		return false
	}
	clean := true
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		switch {
		case retiredCodexLaunchFileName(entry.Name()):
			clean = r.removeFile(path) && clean
		case entry.Name() == retiredCodexSocketName && entry.Type() == fs.ModeSocket:
			clean = r.unlink(path) && clean
		default:
			r.keep(path)
			clean = false
		}
	}
	return clean && r.removeEmptyDir(root)
}

// removeTree removes path and everything under it, unless the tree still
// holds a socket that accepts a connection. It reports whether path is gone.
func (r *retiredCodexGenerationReclaim) removeTree(path string) bool {
	live, ok := r.answeringSocket(path, 0)
	if !ok {
		return false
	}
	if live != "" {
		r.live = append(r.live, path)
		return false
	}
	return r.removeEntry(path, 0)
}

// answeringSocket returns the first socket under path that accepts a
// connection, and reports whether the tree could be read at all. It never
// descends a symlink, so a socket outside the tree is never probed.
func (r *retiredCodexGenerationReclaim) answeringSocket(path string, depth int) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", true
		}
		r.fail(path, err)
		return "", false
	}
	if info.Mode().Type() == fs.ModeSocket {
		if dialRetiredCodexSocket(path) {
			return path, true
		}
		return "", true
	}
	if !info.IsDir() {
		return "", true
	}
	if depth >= retiredCodexTreeDepthMax {
		r.keep(path)
		return "", false
	}
	entries, ok := r.readDir(path)
	if !ok {
		return "", false
	}
	for _, entry := range entries {
		live, ok := r.answeringSocket(filepath.Join(path, entry.Name()), depth+1)
		if !ok || live != "" {
			return live, ok
		}
	}
	return "", true
}

// removeEntry unlinks one entry, descending only into real directories. It
// reports whether path is gone afterwards.
func (r *retiredCodexGenerationReclaim) removeEntry(path string, depth int) bool {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true
		}
		r.fail(path, err)
		return false
	}
	if !info.IsDir() {
		return r.unlink(path)
	}
	if depth >= retiredCodexTreeDepthMax {
		r.keep(path)
		return false
	}
	entries, ok := r.readDir(path)
	if !ok {
		return false
	}
	clean := true
	for _, entry := range entries {
		clean = r.removeEntry(filepath.Join(path, entry.Name()), depth+1) && clean
	}
	return clean && r.removeEmptyDir(path)
}

// readDir lists path only when it is a real directory. A missing path is
// quietly absent; a symlink or any other kind is kept and never descended.
func (r *retiredCodexGenerationReclaim) readDir(path string) ([]os.DirEntry, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			r.fail(path, err)
		}
		return nil, false
	}
	if !info.IsDir() {
		r.keep(path)
		return nil, false
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		r.fail(path, err)
		return nil, false
	}
	return entries, true
}

// removeFile removes path when it is a regular file. It reports whether path
// is gone afterwards.
func (r *retiredCodexGenerationReclaim) removeFile(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true
		}
		r.fail(path, err)
		return false
	}
	if !info.Mode().IsRegular() {
		r.keep(path)
		return false
	}
	return r.unlink(path)
}

// unlink removes one non-directory entry. A symlink is unlinked, not followed.
func (r *retiredCodexGenerationReclaim) unlink(path string) bool {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true
		}
		r.fail(path, err)
		return false
	}
	r.removed++
	return true
}

// removeEmptyDir removes path only if it is an empty directory. rmdir never
// unlinks a file and never recurses. It reports whether path is gone.
func (r *retiredCodexGenerationReclaim) removeEmptyDir(path string) bool {
	err := syscall.Rmdir(path)
	switch {
	case err == nil:
		return true
	case errors.Is(err, fs.ErrNotExist):
		return true
	case errors.Is(err, syscall.ENOTEMPTY), errors.Is(err, syscall.EEXIST), errors.Is(err, syscall.ENOTDIR):
		r.keep(path)
	default:
		r.fail(path, err)
	}
	return false
}

func (r *retiredCodexGenerationReclaim) keep(path string) {
	r.kept = append(r.kept, path)
}

func (r *retiredCodexGenerationReclaim) fail(path string, err error) {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	r.failed = append(r.failed, path+": "+err.Error())
}

// line renders the single report line, or "" when nothing was removed, no
// live tree was skipped and nothing failed. Kept entries alone never print:
// they are listed on the apply that removes the targets next to them, not on
// every later apply.
func (r *retiredCodexGenerationReclaim) line() string {
	if r.removed == 0 && len(r.live) == 0 && len(r.failed) == 0 {
		return ""
	}
	noun := "files"
	if r.removed == 1 {
		noun = "file"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "reclaimed retired Codex generation files: removed %d %s", r.removed, noun)
	if len(r.kept) > 0 {
		fmt.Fprintf(&b, "; kept %d (%s)", len(r.kept), strings.Join(r.kept, ", "))
	}
	if len(r.live) > 0 {
		fmt.Fprintf(&b, "; still running %d (%s)", len(r.live), strings.Join(r.live, ", "))
	}
	if len(r.failed) > 0 {
		fmt.Fprintf(&b, "; failed %d (%s)", len(r.failed), strings.Join(r.failed, ", "))
	}
	return b.String()
}

// retiredCodexBundleDirName matches the content-addressed name
// `codexbundle` leased a standalone release under.
func retiredCodexBundleDirName(name string) bool {
	digest, ok := strings.CutPrefix(name, retiredCodexBundlePrefix)
	return ok && len(digest) == retiredCodexBundleDigestLen && lowerHex(digest)
}

// retiredCodexPrivateHostDirName matches the truncated sha256 of one state
// domain and version that `managedCodexRuntimeLocation` hex-encodes.
func retiredCodexPrivateHostDirName(name string) bool {
	return len(name) == retiredCodexPrivateKeyLen && lowerHex(name)
}

// retiredCodexLaunchFileName matches the durable launch intent and its guard
// as the private host wrote them: `.projmux-launch-<generation>.json`, plus
// the same name with a `.guard` suffix. <generation> repeats the launch token
// rule the host enforced before it built that name.
func retiredCodexLaunchFileName(name string) bool {
	rest, ok := strings.CutPrefix(name, retiredCodexLaunchPrefix)
	if !ok {
		return false
	}
	generation, ok := strings.CutSuffix(rest, retiredCodexGuardSuffix)
	if !ok {
		if generation, ok = strings.CutSuffix(rest, retiredCodexLaunchSuffix); !ok {
			return false
		}
	}
	if generation == "" || len(generation) > retiredCodexLaunchTokenMax {
		return false
	}
	for _, char := range generation {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9',
			char == '-', char == '_', char == '.', char == ':':
		default:
			return false
		}
	}
	return true
}

func lowerHex(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char >= '0' && char <= '9' || char >= 'a' && char <= 'f' {
			continue
		}
		return false
	}
	return true
}

// dialRetiredCodexSocket connects without speaking any protocol, the way the
// retired-generation check does. A refused or timed out connect means no
// retired host is holding that tree.
func dialRetiredCodexSocket(path string) bool {
	conn, err := net.DialTimeout("unix", path, retiredCodexSocketProbeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
