package app

import (
	"path/filepath"
	"slices"
	"strings"
)

// The binary vintage of one live projmux process, as a diagnostics reader can
// establish it without touching the process.
//
// `make install` replaces the executable on disk; it does not replace the image
// of a process that is already running. A diagnosis taken from the installed
// build therefore describes code that a long-lived control-plane process may
// never have loaded, and reporting that diagnosis as healthy certifies
// "installed, therefore deployed", which is false. These two tokens are what
// separate the two states an operator otherwise has to establish by reading
// /proc/<pid>/exe by hand.
const (
	// codexProcessVintageCurrent is a process whose image is still the file
	// the installed path resolves to.
	codexProcessVintageCurrent = "current"
	// codexProcessVintageReplaced is a process whose image was unlinked out
	// from under it, which is what an atomic install leaves behind.
	codexProcessVintageReplaced = "replaced"
)

// The control-plane roles a Codex diagnosis actually depends on. Both are
// long-lived projmux children, and both are where this track lost an acceptance
// criterion to a stale image: the lifecycle observer in Phase 0 and the broker
// runtime in Phase 3.
const (
	codexControlPlaneRoleBroker   = "broker-runtime"
	codexControlPlaneRoleObserver = "lifecycle-observer"
)

var codexControlPlaneRoleOrder = []string{codexControlPlaneRoleBroker, codexControlPlaneRoleObserver}

// procDeletedSuffix is what the kernel appends to /proc/<pid>/exe once the
// mapped file has been unlinked. It is the whole discriminator: the link target
// still names the installed path, so only this suffix separates a process
// running the installed build from one running an image that no longer exists.
const procDeletedSuffix = " (deleted)"

// codexProcessImage is one entry of the local process table, reduced to the two
// facts this projection reads. Nothing here is provider content: the executable
// link and the argv of this application's own children.
type codexProcessImage struct {
	PID     int
	Exe     string
	Cmdline []string
}

// codexProcessImageLister reads the local process table. It reports false when
// the platform exposes no such table, which is not a failure: a diagnosis that
// cannot establish vintage must say so rather than imply currency.
type codexProcessImageLister func() ([]codexProcessImage, bool)

// codexControlPlaneRoleVintage is the vintage census of one control-plane role.
type codexControlPlaneRoleVintage struct {
	Role      string `json:"role"`
	Processes int    `json:"processes"`
	Current   int    `json:"current"`
	Replaced  int    `json:"replaced"`
}

// codexControlPlaneVintage is the whole answer to "is the diagnosis I am about
// to read taken from the build I just installed".
//
// It counts processes by role and image age and names none of them, so it
// carries no pid, no path, and no provider content onto a diagnostics surface.
type codexControlPlaneVintage struct {
	// Supported reports whether this platform exposes a process table this
	// reader can establish vintage from. False is `unknown`, never `current`.
	Supported bool                           `json:"supported"`
	Roles     []codexControlPlaneRoleVintage `json:"roles,omitempty"`
}

// Replaced is how many observed control-plane processes run an image that is no
// longer the installed one.
func (v codexControlPlaneVintage) Replaced() int {
	total := 0
	for _, role := range v.Roles {
		total += role.Replaced
	}
	return total
}

// Observed is how many control-plane processes this reader could classify.
func (v codexControlPlaneVintage) Observed() int {
	total := 0
	for _, role := range v.Roles {
		total += role.Processes
	}
	return total
}

// projectCodexControlPlaneVintage classifies the control-plane children of this
// executable.
//
// Only processes whose image resolves back to this executable's own path are
// considered. A process whose link cannot be read at all is skipped rather than
// counted as unknown: this reader cannot tell such a process from another
// user's, and inventing a category for it would put a number on the section
// that no operator action follows from.
func projectCodexControlPlaneVintage(self string, selfPID int, images []codexProcessImage, supported bool) codexControlPlaneVintage {
	vintage := codexControlPlaneVintage{Supported: supported}
	if !supported {
		return vintage
	}
	self = strings.TrimSpace(self)
	byRole := map[string]*codexControlPlaneRoleVintage{}
	for _, role := range codexControlPlaneRoleOrder {
		byRole[role] = &codexControlPlaneRoleVintage{Role: role}
	}
	for _, image := range images {
		if image.PID == selfPID {
			// The reader's own image is current by construction, and it is not
			// one of the processes the Codex diagnosis is taken from.
			continue
		}
		path, replaced := codexProcessImagePath(image.Exe)
		if path == "" || self == "" || path != self {
			continue
		}
		role := codexControlPlaneRole(image.Cmdline)
		census, ok := byRole[role]
		if !ok {
			continue
		}
		census.Processes++
		if replaced {
			census.Replaced++
		} else {
			census.Current++
		}
	}
	for _, role := range codexControlPlaneRoleOrder {
		if census := byRole[role]; census.Processes > 0 {
			vintage.Roles = append(vintage.Roles, *census)
		}
	}
	return vintage
}

// codexProcessImagePath splits one /proc/<pid>/exe link target into the path it
// names and whether that file has been unlinked.
func codexProcessImagePath(exe string) (string, bool) {
	exe = strings.TrimSpace(exe)
	if exe == "" {
		return "", false
	}
	if trimmed, found := strings.CutSuffix(exe, procDeletedSuffix); found {
		return filepath.Clean(trimmed), true
	}
	return filepath.Clean(exe), false
}

// codexControlPlaneRole names which control-plane child one argv belongs to.
//
// The match is on the internal route words rather than on argv positions, so a
// flag added ahead of the route does not silently drop a process out of the
// census and report a fleet as smaller than it is.
func codexControlPlaneRole(cmdline []string) string {
	switch {
	case slices.Contains(cmdline, "codex-broker") && slices.Contains(cmdline, "serve"):
		return codexControlPlaneRoleBroker
	case slices.Contains(cmdline, "agent-hook") && slices.Contains(cmdline, "ingest") &&
		slices.Contains(cmdline, codexNativeLifecycleIngestRoute):
		return codexControlPlaneRoleObserver
	default:
		return ""
	}
}
