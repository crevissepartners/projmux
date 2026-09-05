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

// The remaining long-lived children of this executable, named without reference
// to a provider.
//
// `internal supervise` is one per pane and is provider-neutral: it supervises a
// Claude pane exactly as it supervises a Codex one. Naming it as a Codex
// control-plane role would answer the wrong question, so the roles below are
// counted in their own census and never appear on the Codex section's line.
const (
	// projmuxProcessRoleSupervisor is the per-pane supervisor.
	projmuxProcessRoleSupervisor = "supervisor"
	// projmuxProcessRoleOther is every remaining child of this executable.
	//
	// It exists so that a route this census does not know cannot make the
	// fleet look smaller than it is. Before this bucket, a child that matched
	// no named route was dropped on the floor, and the section reported six of
	// thirty-two live children as if that were the whole fleet.
	projmuxProcessRoleOther = "other"
)

// projmuxProcessRoleOrder is the render order of the whole-fleet census. Named
// roles come first and the unnamed remainder last, so the roles an operator can
// act on lead and the bucket that only guards the total trails.
var projmuxProcessRoleOrder = []string{
	codexControlPlaneRoleBroker,
	codexControlPlaneRoleObserver,
	projmuxProcessRoleSupervisor,
	projmuxProcessRoleOther,
}

// procDeletedSuffix is what the kernel appends to /proc/<pid>/exe once the
// mapped file has been unlinked. It is the whole discriminator: the link target
// still names the installed path, so only this suffix separates a process
// running the installed build from one running an image that no longer exists.
const procDeletedSuffix = " (deleted)"

// codexProcessImage is one entry of the local process table, reduced to the two
// facts this projection reads. Nothing here is provider content: the executable
// link and the argv of this application's own children.
//
// The reader that produces these reports whether the platform exposes such a
// table at all. A platform that does not is reported as unknown rather than as
// current, because a diagnosis that cannot establish vintage must say so rather
// than imply currency.
type codexProcessImage struct {
	PID     int
	Exe     string
	Cmdline []string
}

// projmuxProcessRoleVintage is the vintage census of one process role.
type projmuxProcessRoleVintage struct {
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
	Supported bool                        `json:"supported"`
	Roles     []projmuxProcessRoleVintage `json:"roles,omitempty"`
}

// Replaced is how many observed control-plane processes run an image that is no
// longer the installed one.
func (v codexControlPlaneVintage) Replaced() int { return projmuxProcessRolesReplaced(v.Roles) }

// Observed is how many control-plane processes this reader could classify.
func (v codexControlPlaneVintage) Observed() int { return projmuxProcessRolesObserved(v.Roles) }

// projmuxProcessVintage is the census of every child of this executable,
// counted without reference to a provider.
//
// It answers a different question from codexControlPlaneVintage, and the two
// are kept apart on purpose. That one qualifies the Codex verdicts printed
// under it and therefore counts only the two roles those verdicts are read
// from. This one answers "how many projmux processes are still running the
// image from before the last install", which is a fleet question, so it counts
// the per-pane supervisors and keeps an unnamed remainder rather than letting a
// route it does not know shrink the total.
//
// Like the Codex census it names no process: no pid, no path, no argv reaches
// a diagnostics surface from here.
type projmuxProcessVintage struct {
	// Supported reports whether this platform exposes a process table this
	// reader can establish vintage from. False is `unknown`, never `current`.
	Supported bool                        `json:"supported"`
	Roles     []projmuxProcessRoleVintage `json:"roles,omitempty"`
}

// Replaced is how many observed projmux processes run an image that is no
// longer the installed one.
func (v projmuxProcessVintage) Replaced() int { return projmuxProcessRolesReplaced(v.Roles) }

// Observed is how many projmux processes this reader classified.
func (v projmuxProcessVintage) Observed() int { return projmuxProcessRolesObserved(v.Roles) }

func projmuxProcessRolesReplaced(roles []projmuxProcessRoleVintage) int {
	total := 0
	for _, role := range roles {
		total += role.Replaced
	}
	return total
}

func projmuxProcessRolesObserved(roles []projmuxProcessRoleVintage) int {
	total := 0
	for _, role := range roles {
		total += role.Processes
	}
	return total
}

// projectCodexControlPlaneVintage classifies the control-plane children of this
// executable.
//
// It counts only the two roles the Codex section's verdicts are read from, so
// that line keeps qualifying exactly those verdicts. A provider-neutral process
// is not one of them and is never named here.
func projectCodexControlPlaneVintage(self string, selfPID int, images []codexProcessImage, supported bool) codexControlPlaneVintage {
	if !supported {
		return codexControlPlaneVintage{}
	}
	return codexControlPlaneVintage{
		Supported: true,
		Roles:     censusProjmuxProcessImages(self, selfPID, images, codexControlPlaneRoleOrder, codexControlPlaneRole),
	}
}

// projectProjmuxProcessVintage classifies every child of this executable.
//
// This is the projection that must not lose anyone. `make install` replaces the
// file on disk and leaves every running process on the image it started with,
// and the number an operator needs is how many processes that is — not how many
// of them happen to sit on a route this reader has a name for.
func projectProjmuxProcessVintage(self string, selfPID int, images []codexProcessImage, supported bool) projmuxProcessVintage {
	if !supported {
		return projmuxProcessVintage{}
	}
	return projmuxProcessVintage{
		Supported: true,
		Roles:     censusProjmuxProcessImages(self, selfPID, images, projmuxProcessRoleOrder, projmuxProcessRole),
	}
}

// censusProjmuxProcessImages counts this executable's children by role and
// image age.
//
// Only processes whose image resolves back to this executable's own path are
// considered. A process whose link cannot be read at all is skipped rather than
// counted as unknown: this reader cannot tell such a process from another
// user's, and inventing a category for it would put a number on the section
// that no operator action follows from.
//
// roleOf naming a role that order does not carry is what keeps a process out of
// a census on purpose, and it is the only difference between the two
// projections above.
func censusProjmuxProcessImages(
	self string,
	selfPID int,
	images []codexProcessImage,
	order []string,
	roleOf func([]string) string,
) []projmuxProcessRoleVintage {
	self = strings.TrimSpace(self)
	byRole := map[string]*projmuxProcessRoleVintage{}
	for _, role := range order {
		byRole[role] = &projmuxProcessRoleVintage{Role: role}
	}
	for _, image := range images {
		if image.PID == selfPID {
			// The reader's own image is current by construction, and it is not
			// one of the processes a diagnosis is taken from.
			continue
		}
		path, replaced := codexProcessImagePath(image.Exe)
		if path == "" || self == "" || path != self {
			continue
		}
		census, ok := byRole[roleOf(image.Cmdline)]
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
	var rows []projmuxProcessRoleVintage
	for _, role := range order {
		if census := byRole[role]; census.Processes > 0 {
			rows = append(rows, *census)
		}
	}
	return rows
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

// projmuxProcessRouteWords is the part of one argv that names an internal
// route.
//
// It drops argv[0] and everything after a bare `--`. Neither is a route:
// argv[0] is the executable path, and the words behind `--` are a caller's own
// text. `projmux agent turn ... -- <message>` carries arbitrary prose there,
// including the very words this census matches on, and reading a message about
// the broker as a broker would overcount the fleet with a process that is not
// one.
func projmuxProcessRouteWords(cmdline []string) []string {
	if len(cmdline) < 2 {
		return nil
	}
	words := cmdline[1:]
	if index := slices.Index(words, "--"); index >= 0 {
		words = words[:index]
	}
	return words
}

// projmuxProcessRole names which child of this executable one argv belongs to.
//
// The match is on the internal route words rather than on argv positions, so a
// flag added ahead of the route does not silently drop a process out of the
// census and report a fleet as smaller than it is. For the same reason there is
// no unmatched case: an argv this function cannot name still belongs to a
// running projmux process, and the remainder bucket is where it stays counted.
func projmuxProcessRole(cmdline []string) string {
	words := projmuxProcessRouteWords(cmdline)
	switch {
	case slices.Contains(words, "codex-broker") && slices.Contains(words, "serve"):
		return codexControlPlaneRoleBroker
	case slices.Contains(words, "agent-hook") && slices.Contains(words, "ingest") &&
		slices.Contains(words, codexNativeLifecycleIngestRoute):
		return codexControlPlaneRoleObserver
	case slices.Contains(words, "supervise"):
		return projmuxProcessRoleSupervisor
	default:
		return projmuxProcessRoleOther
	}
}

// codexControlPlaneRole names which Codex control-plane child one argv belongs
// to, and "" for every other process.
//
// The empty answer is the whole point: it keeps a provider-neutral process off
// the line that qualifies the Codex verdicts. Those two roles are the ones
// those verdicts are read from; a supervisor is not, and calling it one would
// answer a question the section never asked.
func codexControlPlaneRole(cmdline []string) string {
	switch role := projmuxProcessRole(cmdline); role {
	case codexControlPlaneRoleBroker, codexControlPlaneRoleObserver:
		return role
	default:
		return ""
	}
}
