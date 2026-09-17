package app

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Project snapshots were removed, and nothing reads their files anymore.
// `config apply` is the step every installer runs, so it reclaims the
// leftovers there:
//
//   - `<state>/projmux/sessions/*.json` (direct children only);
//   - `<config>/projmux/sessionstate-autosave` and
//     `sessionstate-autosave-interval`;
//   - `<config>/projmux/sessionstate-projects/<s>/autosave`.
//
// Only regular files with exactly those names are removed, and only
// directories that end up empty are removed after them. Nothing is followed
// through a symlink. Any other entry is kept, and listed on the report line of
// an apply that removes target files. A failure is reported and never fails
// the apply; a rerun converges because a target that
// is already gone is not an error.
const (
	retiredSnapshotSessionsDirName     = "sessions"
	retiredSnapshotAutosaveFileName    = "sessionstate-autosave"
	retiredSnapshotIntervalFileName    = "sessionstate-autosave-interval"
	retiredSnapshotProjectsDirName     = "sessionstate-projects"
	retiredSnapshotProjectAutosaveName = "autosave"
)

type retiredSnapshotReclaim struct {
	// removed counts target files only. Emptied directories are removed
	// silently and never make the report line appear on their own.
	removed int
	kept    []string
	failed  []string
}

// reclaimRetiredSnapshotFiles runs the reclamation for one apply and writes at
// most one line to stdout. It never returns an error: the apply result does
// not depend on it. Without an injected home resolver it does nothing, so a
// partially constructed command never falls back to the real home directory.
func (c *tmuxCommand) reclaimRetiredSnapshotFiles(stdout io.Writer) {
	if c.homeDir == nil || c.lookupEnv == nil {
		return
	}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	var r retiredSnapshotReclaim
	if err != nil {
		r.failed = append(r.failed, "resolve directories: "+err.Error())
	} else {
		r.run(paths.StateDir, paths.ConfigDir)
	}
	if line := r.line(); line != "" && stdout != nil {
		fmt.Fprintln(stdout, line)
	}
}

func (r *retiredSnapshotReclaim) run(stateDir, configDir string) {
	sessions := filepath.Join(stateDir, retiredSnapshotSessionsDirName)
	if entries, ok := r.readDir(sessions); ok {
		clean := true
		for _, entry := range entries {
			path := filepath.Join(sessions, entry.Name())
			if matched, _ := filepath.Match("*.json", entry.Name()); matched {
				clean = r.removeFile(path) && clean
			} else {
				r.keep(path)
				clean = false
			}
		}
		if clean {
			r.removeEmptyDir(sessions)
		}
	}

	r.removeFile(filepath.Join(configDir, retiredSnapshotAutosaveFileName))
	r.removeFile(filepath.Join(configDir, retiredSnapshotIntervalFileName))

	projects := filepath.Join(configDir, retiredSnapshotProjectsDirName)
	if entries, ok := r.readDir(projects); ok {
		clean := true
		for _, entry := range entries {
			project := filepath.Join(projects, entry.Name())
			children, ok := r.readDir(project)
			if !ok {
				clean = false
				continue
			}
			projectClean := true
			for _, child := range children {
				path := filepath.Join(project, child.Name())
				if child.Name() == retiredSnapshotProjectAutosaveName {
					projectClean = r.removeFile(path) && projectClean
				} else {
					r.keep(path)
					projectClean = false
				}
			}
			if !projectClean || !r.removeEmptyDir(project) {
				clean = false
			}
		}
		if clean {
			r.removeEmptyDir(projects)
		}
	}
}

// readDir lists path only when it is a real directory. A missing path is
// quietly absent; a symlink or any other kind is kept and never descended.
func (r *retiredSnapshotReclaim) readDir(path string) ([]os.DirEntry, bool) {
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
func (r *retiredSnapshotReclaim) removeFile(path string) bool {
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
func (r *retiredSnapshotReclaim) removeEmptyDir(path string) bool {
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

func (r *retiredSnapshotReclaim) keep(path string) {
	r.kept = append(r.kept, path)
}

func (r *retiredSnapshotReclaim) fail(path string, err error) {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	r.failed = append(r.failed, path+": "+err.Error())
}

// line renders the single report line, or "" when no target file was removed
// and nothing failed. Kept entries alone never print: they are listed on the
// apply that removes the targets next to them, not on every later apply.
func (r *retiredSnapshotReclaim) line() string {
	if r.removed == 0 && len(r.failed) == 0 {
		return ""
	}
	noun := "files"
	if r.removed == 1 {
		noun = "file"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "reclaimed retired Project snapshot files: removed %d %s", r.removed, noun)
	if len(r.kept) > 0 {
		fmt.Fprintf(&b, "; kept %d (%s)", len(r.kept), strings.Join(r.kept, ", "))
	}
	if len(r.failed) > 0 {
		fmt.Fprintf(&b, "; failed %d (%s)", len(r.failed), strings.Join(r.failed, ", "))
	}
	return b.String()
}
