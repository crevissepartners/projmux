package app

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// The closed-Project startup setting was removed, and nothing reads the file
// it saved anymore: every registered closed Project shows the startup screen.
// `config apply` is the step every installer runs, so it reclaims that file
// there, under the same rules as the other retired-file reclaims:
//
//   - `<config>/projmux/sidebar-startup-picker`.
//
// Only a regular file with exactly that name is removed. A symlink or any
// other kind is kept and never followed. A failure is reported and never fails
// the apply; a rerun converges because a file that is already gone is not an
// error. The saved value is not migrated anywhere: there is no choice left to
// carry it into.
const retiredClosedStartupFileName = "sidebar-startup-picker"

type retiredSidebarStartupReclaim struct {
	removed bool
	kept    string
	failed  string
}

// reclaimRetiredSidebarStartupFile runs the reclamation for one apply and
// writes at most one line to stdout. It never returns an error: the apply
// result does not depend on it. Without an injected home resolver it does
// nothing, so a partially constructed command never falls back to the real
// home directory.
func (c *tmuxCommand) reclaimRetiredSidebarStartupFile(stdout io.Writer) {
	if c.homeDir == nil || c.lookupEnv == nil {
		return
	}
	var r retiredSidebarStartupReclaim
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		r.failed = "resolve directories: " + err.Error()
	} else {
		r.run(filepath.Join(paths.ConfigDir, retiredClosedStartupFileName))
	}
	if line := r.line(); line != "" && stdout != nil {
		fmt.Fprintln(stdout, line)
	}
}

func (r *retiredSidebarStartupReclaim) run(path string) {
	info, err := os.Lstat(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			r.fail(path, err)
		}
		return
	}
	if !info.Mode().IsRegular() {
		r.kept = path
		return
	}
	if err := os.Remove(path); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			r.fail(path, err)
		}
		return
	}
	r.removed = true
}

func (r *retiredSidebarStartupReclaim) fail(path string, err error) {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	r.failed = path + ": " + err.Error()
}

// line renders the single report line, or "" when nothing was removed and
// nothing failed. A kept non-regular entry alone never prints, like the other
// retired-file reclaims: it would otherwise repeat on every later apply.
func (r *retiredSidebarStartupReclaim) line() string {
	switch {
	case r.failed != "":
		return "reclaimed retired closed-Project startup setting: failed (" + r.failed + ")"
	case r.removed:
		return "reclaimed retired closed-Project startup setting: removed 1 file"
	default:
		return ""
	}
}
