package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// windowDeleteRuntime is the live half of delete window. The route owns the
// resource transaction; this seam owns only exact-server inventory and exact
// tmux Window mutation.
type windowDeleteRuntime interface {
	// useExactTarget pins every read and write of this seam to one resolved
	// server. It is called before the first inventory of an invocation.
	useExactTarget(explicitTmuxTarget)
	preflight(context.Context, coremetadata.Registry, deletePlan) (windowLiveDeletePlan, error)
	kill(context.Context, windowLiveDeleteTarget) error
	queueSelfKill(context.Context, []windowLiveDeleteTarget) error
}

type windowLiveDeleteTarget struct {
	UID         string
	WindowID    string
	SessionID   string
	SessionName string
	ProjectUID  string
	EndsSession bool
	Self        bool
}

type windowLiveDeletePlan struct {
	Targets []windowLiveDeleteTarget
}

func (p windowLiveDeletePlan) signature() string {
	var b strings.Builder
	for _, target := range p.Targets {
		fmt.Fprintf(&b, "%s,%s,%s,%s,%s,%t,%t;", target.UID, target.WindowID,
			target.SessionID, target.SessionName, target.ProjectUID, target.EndsSession, target.Self)
	}
	return b.String()
}

func (p windowLiveDeletePlan) endsSessions() int {
	total := 0
	for _, target := range p.Targets {
		if target.EndsSession {
			total++
		}
	}
	return total
}

func (p windowLiveDeletePlan) hasSelfTarget() bool {
	for _, target := range p.Targets {
		if target.Self {
			return true
		}
	}
	return false
}

type tmuxWindowDeleteRuntime struct {
	runner tmuxCommandRunner
	target explicitTmuxTarget
	getenv func(string) string
}

// newTmuxWindowDeleteRuntime builds the live half with no server bound yet.
// See newTmuxPaneDeleteRuntime for why there is no default target.
func newTmuxWindowDeleteRuntime() *tmuxWindowDeleteRuntime {
	return &tmuxWindowDeleteRuntime{
		runner: inttmux.ExecRunner{},
		getenv: os.Getenv,
	}
}

func (r *tmuxWindowDeleteRuntime) useExactTarget(target explicitTmuxTarget) {
	if r == nil {
		return
	}
	r.target = target
}

type liveWindowRow struct {
	sessionID   string
	sessionName string
	windowID    string
	projectUID  string
	windowUID   string
}

func (r *tmuxWindowDeleteRuntime) routed() explicitTmuxRunner {
	return explicitTmuxRunner{runner: r.runner, target: r.target}
}

func (r *tmuxWindowDeleteRuntime) inventory(ctx context.Context) ([]liveWindowRow, bool, error) {
	if r == nil || r.runner == nil {
		return nil, false, errors.New("delete window: tmux runtime is not configured")
	}
	if r.target.flag == "" || r.target.value == "" {
		return nil, false, errors.New("delete window: no exact tmux target is bound")
	}
	format := tmuxRowFormat(
		"#{session_id}",
		"#{session_name}",
		"#{window_id}",
		"#{"+tmuxopts.ProjectUIDSession+"}",
		"#{"+tmuxopts.WindowUID+"}",
	)
	out, err := r.routed().Run(ctx, "tmux", "list-windows", "-a", "-F", format)
	if err != nil {
		// An absent named server is the strongest possible observation that this
		// selected socket projects zero Windows. Accept only the typed subprocess
		// classification: permissions, missing executables, arbitrary runner
		// failures, and plain errors containing similar words cannot authorize a
		// destructive Registry-only path.
		if inttmux.IsNoServerFailure(err) {
			return nil, true, nil
		}
		return nil, false, tmuxError("delete window: inventory exact tmux socket: %v", err)
	}
	out = []byte(strings.ReplaceAll(string(out), tmuxRowSepFormat, tmuxRowSep))
	var rows []liveWindowRow
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, tmuxRowSep)
		if len(fields) != 5 {
			return nil, false, fmt.Errorf("delete window: malformed exact tmux inventory row %q", line)
		}
		row := liveWindowRow{
			sessionID: strings.TrimSpace(fields[0]), sessionName: strings.TrimSpace(fields[1]),
			windowID: strings.TrimSpace(fields[2]), projectUID: strings.TrimSpace(fields[3]),
			windowUID: strings.TrimSpace(fields[4]),
		}
		if exactTmuxHandle(row.sessionID, "$") == "" || exactTmuxHandle(row.windowID, "@") == "" {
			return nil, false, fmt.Errorf("delete window: malformed exact tmux handles session=%q window=%q",
				row.sessionID, row.windowID)
		}
		rows = append(rows, row)
	}
	return rows, false, nil
}

func (r *tmuxWindowDeleteRuntime) preflight(ctx context.Context, registry coremetadata.Registry, plan deletePlan) (windowLiveDeletePlan, error) {
	rows, serverAbsent, err := r.inventory(ctx)
	if err != nil {
		return windowLiveDeletePlan{}, err
	}
	targetUIDs := make(map[string]bool, len(plan.Targets))
	for _, target := range plan.Targets {
		targetUIDs[target.Match.UID] = true
	}
	windowCount := map[string]int{}
	removedCount := map[string]int{}
	byUID := map[string][]liveWindowRow{}
	for _, row := range rows {
		windowCount[row.sessionID]++
		if targetUIDs[row.windowUID] {
			removedCount[row.sessionID]++
			byUID[row.windowUID] = append(byUID[row.windowUID], row)
		}
	}

	var currentWindowID, currentSocket string
	if !serverAbsent {
		currentWindowID, currentSocket, err = r.currentInvocationWindow(ctx)
		if err != nil {
			return windowLiveDeletePlan{}, err
		}
	}
	if plan.Implicit && currentSocket == "" {
		return windowLiveDeletePlan{}, errors.New("delete window: implicit active target is not attached to the exact projmux tmux socket")
	}

	live := windowLiveDeletePlan{}
	for _, target := range plan.Targets {
		window, ok := registry.Window(target.Match.UID)
		if !ok {
			return windowLiveDeletePlan{}, fmt.Errorf("delete window: registry window uid %q disappeared during live preflight", target.Match.UID)
		}
		ownerUID := window.Metadata.OwnerUID()
		project, ok := registry.Project(ownerUID)
		if !ok {
			return windowLiveDeletePlan{}, fmt.Errorf("delete window: registry window uid %q has no owning Project %q", target.Match.UID, ownerUID)
		}
		matches := byUID[target.Match.UID]
		if len(matches) == 0 {
			// A non-implicit Window target (an explicit selector or --all) is desired
			// Registry topology even when the selected tmux socket currently projects
			// none of it. Canonical deletion must be able to retire that offline
			// topology without a raw Registry edit. The empty live target is part of
			// the signed preflight:
			// if a matching Window appears before the locked execution pass, the
			// live signature changes and the route fails closed instead of skipping
			// an unapproved kill.
			//
			// An implicit active target cannot legitimately be offline. Keeping its
			// zero-match case closed also protects against a caller/window race.
			if plan.Implicit {
				return windowLiveDeletePlan{}, fmt.Errorf("delete window: registry window uid %q has no exact live tmux Window mirror on -L %s; nothing was changed",
					target.Match.UID, r.target.value)
			}
			continue
		}
		if len(matches) != 1 {
			return windowLiveDeletePlan{}, fmt.Errorf("delete window: registry window uid %q has %d live tmux Window mirrors on -L %s; exact target is ambiguous and nothing was changed",
				target.Match.UID, len(matches), r.target.value)
		}
		row := matches[0]
		// ProjectUIDSession is an optional transport mirror on existing
		// sessions. The Registry owner graph plus its exact session projection
		// remains authoritative; when the mirror is present it must agree.
		if row.projectUID != "" && row.projectUID != ownerUID {
			return windowLiveDeletePlan{}, fmt.Errorf("delete window: live tmux Window %s mirrors registry uid %q under foreign Project uid %q, want %q; nothing was changed",
				row.windowID, target.Match.UID, row.projectUID, ownerUID)
		}
		if project.Status.Session == nil || strings.TrimSpace(project.Status.Session.Name) == "" {
			return windowLiveDeletePlan{}, fmt.Errorf("delete window: owning Project uid %q has no registry session projection for live tmux Window %s; nothing was changed",
				ownerUID, row.windowID)
		}
		if want := strings.TrimSpace(project.Status.Session.Name); row.sessionName != want {
			return windowLiveDeletePlan{}, fmt.Errorf("delete window: live tmux Window %s is in stale session %q, registry Project uid %q projects session %q; nothing was changed",
				row.windowID, row.sessionName, ownerUID, want)
		}
		if plan.Implicit && row.windowID != currentWindowID {
			return windowLiveDeletePlan{}, fmt.Errorf("delete window: implicit active registry uid %q mirrors live Window %s but the exact caller is in %s; nothing was changed",
				target.Match.UID, row.windowID, currentWindowID)
		}
		live.Targets = append(live.Targets, windowLiveDeleteTarget{
			UID: target.Match.UID, WindowID: row.windowID, SessionID: row.sessionID,
			SessionName: row.sessionName, ProjectUID: ownerUID,
			Self: row.windowID == currentWindowID && currentSocket != "",
		})
	}
	// Only the final planned kill in a session carries the implicit session
	// cascade. A multi-Window --all must report one ended session, not one per
	// selected sibling.
	markedSession := map[string]bool{}
	for i := len(live.Targets) - 1; i >= 0; i-- {
		target := &live.Targets[i]
		if !markedSession[target.SessionID] && removedCount[target.SessionID] == windowCount[target.SessionID] {
			target.EndsSession = true
			markedSession[target.SessionID] = true
		}
	}
	return live, nil
}

// currentInvocationWindow distinguishes a self target from an external target
// and proves that an implicit target came from the same exact socket. A leaked
// TMUX_PANE from another server is not enough.
func (r *tmuxWindowDeleteRuntime) currentInvocationWindow(ctx context.Context) (string, string, error) {
	if r.getenv == nil || strings.TrimSpace(r.getenv("TMUX")) == "" || strings.TrimSpace(r.getenv("TMUX_PANE")) == "" {
		return "", "", nil
	}
	inheritedSocket, _, _ := strings.Cut(strings.TrimSpace(r.getenv("TMUX")), ",")
	serverSocket, err := r.routed().Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		return "", "", tmuxError("delete window: inspect exact caller socket: %v", err)
	}
	if strings.TrimSpace(string(serverSocket)) != inheritedSocket {
		return "", "", nil
	}
	out, err := r.routed().Run(ctx, "tmux", "display-message", "-p", "-t", strings.TrimSpace(r.getenv("TMUX_PANE")),
		"-F", tmuxRowFormat("#{socket_path}", "#{window_id}"))
	if err != nil {
		return "", "", tmuxError("delete window: inspect exact caller pane %s: %v", strings.TrimSpace(r.getenv("TMUX_PANE")), err)
	}
	rows := splitTmuxRows(string(out), 2)
	if len(rows) != 1 || strings.TrimSpace(rows[0][0]) != inheritedSocket {
		return "", "", nil
	}
	windowID := exactTmuxHandle(strings.TrimSpace(rows[0][1]), "@")
	if windowID == "" {
		return "", "", errors.New("delete window: exact caller tmux Window handle is malformed")
	}
	return windowID, inheritedSocket, nil
}

func (r *tmuxWindowDeleteRuntime) kill(ctx context.Context, target windowLiveDeleteTarget) error {
	_, err := r.routed().Run(ctx, "tmux", "kill-window", "-t", target.WindowID)
	if err != nil {
		return tmuxError("delete window: kill exact live Window %s in session %s (%s): %v",
			target.WindowID, target.SessionName, target.SessionID, err)
	}
	return nil
}

func (r *tmuxWindowDeleteRuntime) queueSelfKill(ctx context.Context, targets []windowLiveDeleteTarget) error {
	// The Registry commit and user-visible result deliberately precede this
	// delayed mutation. Re-prove every Window mirror at the queue boundary so a
	// handle recycled during that interval cannot make the background command
	// target a foreign Window. Validate the entire set before queueing any kill.
	for _, target := range targets {
		out, err := r.routed().Run(ctx, "tmux", "show-options", "-wqv", "-t", target.WindowID, tmuxopts.WindowUID)
		if err != nil {
			return tmuxError("revalidate exact live Window %s before self-target queue: %v", target.WindowID, err)
		}
		observed := strings.TrimSpace(string(out))
		if observed != target.UID {
			if observed == "" {
				observed = "<missing>"
			}
			return fmt.Errorf("delete window: delayed live Window %s mirrors uid %q, want registry uid %q; no self-target kill was queued",
				target.WindowID, observed, target.UID)
		}
	}
	for _, target := range targets {
		command := "exec " + shellQuote("tmux") + " " + shellQuote(r.target.flag) + " " +
			shellQuote(r.target.value) + " kill-window -t " + shellQuote(target.WindowID)
		if _, err := r.routed().Run(ctx, "tmux", "run-shell", "-b", command); err != nil {
			return tmuxError("queue exact live Window %s in session %s (%s) for self-target deletion: %v",
				target.WindowID, target.SessionName, target.SessionID, err)
		}
	}
	return nil
}
