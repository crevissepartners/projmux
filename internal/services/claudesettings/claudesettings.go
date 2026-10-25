// Package claudesettings writes Claude Code's settings file for CLI and SDK.
//
// Replace is the one write policy for that file. It never edits the file in
// place: the new bytes are written beside it, synced, given the file's own
// mode, and renamed over it, so a reader sees the old file or the new one and
// never a torn one. A settings file that is a symbolic link stays one: the
// link's target is replaced. Because the file is replaced, its inode changes
// and a hard link to the old file keeps the old bytes.
//
// A caller that planned from the file passes what it read as a Base. The file
// is read again just before the rename, and one that no longer holds the
// planned bytes is refused with ChangedError; nothing is written then. The
// check is a re-read, not a lock: a write that lands between it and the
// rename is not detected.
package claudesettings

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CodeChanged is the refusal token for a settings file that changed after
// the plan read it.
const CodeChanged = "claude-settings-changed"

// maxLinkHops bounds the symbolic links Replace follows to the file it
// replaces, as the kernel bounds a path lookup.
const maxLinkHops = 40

// Base is the settings file as a plan read it. Exists separates a file that
// does not exist from an empty one.
type Base struct {
	Exists bool
	Data   []byte
}

// ChangedError reports a settings file that no longer holds the bytes its
// plan was made from. Nothing was written.
type ChangedError struct {
	Path string
}

func (e *ChangedError) Error() string {
	return CodeChanged + ": Claude settings " + e.Path + " changed after it was read; nothing was written, run the command again"
}

// Replace writes data as the settings file at path. The directory is created
// 0755 when missing. An existing file keeps its mode; a new one is created
// 0644 less the process umask, as os.WriteFile creates it. A file the path
// names through symbolic links is replaced at its target. A target that is
// not a regular file is refused. With a non-nil base the file must still hold
// base's bytes, or exist only if base does, right before the rename;
// otherwise Replace returns ChangedError. It leaves no temporary file behind
// on any failure.
func Replace(path string, base *Base, data []byte) error {
	// #nosec G301 -- Claude Code's own ~/.claude directory, created 0755 as the CLI has always created it.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	target, info, err := resolveTarget(path)
	if err != nil {
		return err
	}
	createPerm := os.FileMode(0o644)
	if info != nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", target)
		}
		createPerm = 0o600
	}
	dir := filepath.Dir(target)
	tmp, err := createTemp(dir, "."+filepath.Base(target)+".projmux-", createPerm)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	if err := writeSynced(tmp, data, info); err != nil {
		return err
	}
	if base != nil {
		now, err := os.ReadFile(target) // #nosec G304 -- the settings file the caller planned from, or its link's target.
		exists := err == nil
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if exists != base.Exists || !bytes.Equal(now, base.Data) {
			return &ChangedError{Path: path}
		}
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	keep = true
	if d, err := os.Open(dir); err == nil { // #nosec G304 -- the settings file's own directory, opened only to sync the rename.
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// resolveTarget follows path's symbolic links to the file Replace writes,
// including a link whose target does not exist yet. info is nil when that
// file does not exist. A relative link is resolved against the real
// directory that holds it, one component at a time, so a ".." after a
// symbolically linked directory climbs from where that link points, as the
// kernel's lookup does.
func resolveTarget(path string) (string, fs.FileInfo, error) {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		info, err := os.Stat(target)
		return target, info, err
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", nil, err
	}
	target := path
	for range maxLinkHops {
		info, err := os.Lstat(target)
		if errors.Is(err, fs.ErrNotExist) {
			return target, nil, nil
		}
		if err != nil {
			return "", nil, err
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			return target, info, nil
		}
		link, err := os.Readlink(target)
		if err != nil {
			return "", nil, err
		}
		dir := string(filepath.Separator)
		if !filepath.IsAbs(link) {
			if dir, err = filepath.EvalSymlinks(filepath.Dir(target)); err != nil {
				return "", nil, err
			}
		}
		if target, err = walkLink(dir, link); err != nil {
			return "", nil, err
		}
	}
	return "", nil, fmt.Errorf("%s: too many levels of symbolic links", path)
}

// walkLink is the path link names from the real directory dir. Every
// component but the last is resolved before the next is applied; the last is
// left for the caller, since it may be a link or may not exist.
func walkLink(dir, link string) (string, error) {
	var parts []string
	for _, part := range strings.Split(link, string(filepath.Separator)) {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	current := dir
	for i, part := range parts {
		if part == ".." {
			current = filepath.Dir(current)
			continue
		}
		next := filepath.Join(current, part)
		if i == len(parts)-1 {
			return next, nil
		}
		resolved, err := filepath.EvalSymlinks(next)
		if err != nil {
			return "", err
		}
		current = resolved
	}
	return current, nil
}

// createTemp is os.CreateTemp with a chosen create mode, so that a new
// settings file gets the umask the kernel applies at create.
func createTemp(dir, prefix string, perm os.FileMode) (*os.File, error) {
	for range 10000 {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return nil, err
		}
		name := filepath.Join(dir, prefix+hex.EncodeToString(suffix[:]))
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm) // #nosec G304 -- a new file beside the settings file.
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return f, err
	}
	return nil, fmt.Errorf("create temporary file in %s: too many collisions", dir)
}

// writeSynced writes data to f, gives it the replaced file's mode when there
// is one, syncs it and closes it.
func writeSynced(f *os.File, data []byte, replaced fs.FileInfo) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if replaced != nil {
		if err := f.Chmod(replaced.Mode().Perm()); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
