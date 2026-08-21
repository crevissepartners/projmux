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

// deletedPaneMirrorPrefix is a transport tombstone used only while a
// self-target delete is being handed to tmux. A queued kill can fail after the
// Registry result is durable; keeping that live pane out of orphan import is
// safer than silently minting a new identity for a resource the operator just
// deleted.
const deletedPaneMirrorPrefix = coremetadata.DeletedPaneMirrorPrefix

type paneDeleteRuntime interface {
	// useExactTarget pins every read and write of this seam to one resolved
	// server. It is called before the first inventory of an invocation.
	useExactTarget(explicitTmuxTarget)
	preflight(context.Context, coremetadata.Registry, deletePlan) (paneLiveDeletePlan, error)
	kill(context.Context, paneLiveDeleteTarget) error
	tombstoneSelfKill(context.Context, []paneLiveDeleteTarget) error
	restoreSelfKill(context.Context, []paneLiveDeleteTarget) error
	queueSelfKill(context.Context, []paneLiveDeleteTarget) error
}

type paneLiveDeleteTarget struct {
	ResourceUID string
	PaneUID     string
	PaneID      string
	WindowUID   string
	WindowID    string
	SessionID   string
	SessionName string
	RootKind    coremetadata.Kind
	RootUID     string
	EndsWindow  bool
	EndsSession bool
	Self        bool
}

type paneLiveDeletePlan struct {
	Targets []paneLiveDeleteTarget
}

func (p paneLiveDeletePlan) signature() string {
	var b strings.Builder
	for _, target := range p.Targets {
		fmt.Fprintf(&b, "%s,%s,%s,%s,%s,%s,%s,%s,%s,%t,%t,%t;", target.ResourceUID,
			target.PaneUID, target.PaneID, target.WindowUID, target.WindowID,
			target.SessionID, target.SessionName, target.RootKind, target.RootUID,
			target.EndsWindow, target.EndsSession, target.Self)
	}
	return b.String()
}

func (p paneLiveDeletePlan) endsWindows() int {
	total := 0
	for _, target := range p.Targets {
		if target.EndsWindow {
			total++
		}
	}
	return total
}

func (p paneLiveDeletePlan) endsSessions() int {
	total := 0
	for _, target := range p.Targets {
		if target.EndsSession {
			total++
		}
	}
	return total
}

func (p paneLiveDeletePlan) hasSelfTarget() bool {
	for _, target := range p.Targets {
		if target.Self {
			return true
		}
	}
	return false
}

type tmuxPaneDeleteRuntime struct {
	runner tmuxCommandRunner
	target explicitTmuxTarget
	getenv func(string) string
}

// newTmuxPaneDeleteRuntime builds the live half with no server bound yet.
//
// There is deliberately no default target. The route resolves the exact server
// from the invocation's own flags or inherited $TMUX and installs it with
// useExactTarget; a runtime that was never given one refuses rather than
// reaching for the app socket.
func newTmuxPaneDeleteRuntime() *tmuxPaneDeleteRuntime {
	return &tmuxPaneDeleteRuntime{runner: inttmux.ExecRunner{}, getenv: os.Getenv}
}

func (r *tmuxPaneDeleteRuntime) useExactTarget(target explicitTmuxTarget) {
	if r == nil {
		return
	}
	r.target = target
}

type livePaneDeleteRow struct {
	sessionID   string
	sessionName string
	windowID    string
	paneID      string
	projectUID  string
	windowUID   string
	paneUID     string
}

func (r *tmuxPaneDeleteRuntime) routed() explicitTmuxRunner {
	return explicitTmuxRunner{runner: r.runner, target: r.target}
}

func (r *tmuxPaneDeleteRuntime) inventory(ctx context.Context) ([]livePaneDeleteRow, error) {
	if r == nil || r.runner == nil {
		return nil, errors.New("delete pane: tmux runtime is not configured")
	}
	if r.target.flag == "" || r.target.value == "" {
		return nil, errors.New("delete pane: no exact tmux target is bound")
	}
	format := tmuxRowFormat(
		"#{session_id}", "#{session_name}", "#{window_id}", "#{pane_id}",
		"#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.WindowUID+"}", "#{"+tmuxopts.PaneUID+"}",
	)
	out, err := r.routed().Run(ctx, "tmux", "list-panes", "-a", "-F", format)
	if err != nil {
		return nil, tmuxError("delete pane: inventory exact tmux socket: %v", err)
	}
	out = []byte(strings.ReplaceAll(string(out), tmuxRowSepFormat, tmuxRowSep))
	var rows []livePaneDeleteRow
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, tmuxRowSep)
		if len(fields) != 7 {
			return nil, fmt.Errorf("delete pane: malformed exact tmux inventory row %q", line)
		}
		row := livePaneDeleteRow{
			sessionID: strings.TrimSpace(fields[0]), sessionName: strings.TrimSpace(fields[1]),
			windowID: strings.TrimSpace(fields[2]), paneID: strings.TrimSpace(fields[3]),
			projectUID: strings.TrimSpace(fields[4]), windowUID: strings.TrimSpace(fields[5]),
			paneUID: strings.TrimSpace(fields[6]),
		}
		if exactTmuxHandle(row.sessionID, "$") == "" || exactTmuxHandle(row.windowID, "@") == "" || exactTmuxHandle(row.paneID, "%") == "" {
			return nil, fmt.Errorf("delete pane: malformed exact tmux handles session=%q window=%q pane=%q",
				row.sessionID, row.windowID, row.paneID)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

type plannedPaneDelete struct {
	resourceUID string
	paneUID     string
}

func plannedPaneDeletes(plan deletePlan) []plannedPaneDelete {
	var out []plannedPaneDelete
	for _, target := range plan.Targets {
		switch plan.Kind {
		case coremetadata.KindPane:
			out = append(out, plannedPaneDelete{resourceUID: target.Match.UID, paneUID: target.Match.UID})
		case coremetadata.KindAgent:
			for _, descendant := range target.Descendants {
				if descendant.Kind == coremetadata.KindPane {
					out = append(out, plannedPaneDelete{resourceUID: target.Match.UID, paneUID: descendant.UID})
				}
			}
		}
	}
	return out
}

func paneRegistryAncestry(registry coremetadata.Registry, paneUID string) (coremetadata.Pane, coremetadata.Window, deleteRootOwner, error) {
	pane, ok := registry.Pane(paneUID)
	if !ok {
		return coremetadata.Pane{}, coremetadata.Window{}, deleteRootOwner{},
			fmt.Errorf("registry Pane uid %q disappeared during live preflight", paneUID)
	}
	windowUID := pane.Metadata.OwnerUID()
	if pane.Metadata.OwnerRef != nil && pane.Metadata.OwnerRef.Kind == coremetadata.KindAgent {
		agent, ok := registry.Agent(windowUID)
		if !ok {
			return coremetadata.Pane{}, coremetadata.Window{}, deleteRootOwner{},
				fmt.Errorf("registry Pane uid %q has no owning Agent %q", paneUID, windowUID)
		}
		windowUID = agent.Metadata.OwnerUID()
	}
	window, ok := registry.Window(windowUID)
	if !ok {
		return coremetadata.Pane{}, coremetadata.Window{}, deleteRootOwner{},
			fmt.Errorf("registry Pane uid %q has no owning Window %q", paneUID, windowUID)
	}
	root, err := deleteRootForWindow(registry, *window)
	if err != nil {
		return coremetadata.Pane{}, coremetadata.Window{}, deleteRootOwner{}, err
	}
	return *pane, *window, root, nil
}

func (r *tmuxPaneDeleteRuntime) preflight(ctx context.Context, registry coremetadata.Registry, plan deletePlan) (paneLiveDeletePlan, error) {
	rows, err := r.inventory(ctx)
	if err != nil {
		return paneLiveDeletePlan{}, err
	}
	planned := plannedPaneDeletes(plan)
	targetUIDs := make(map[string]bool, len(planned))
	for _, target := range planned {
		targetUIDs[target.paneUID] = true
	}
	paneCount := map[string]int{}
	windowCount := map[string]int{}
	removedCount := map[string]int{}
	byUID := map[string][]livePaneDeleteRow{}
	seenWindow := map[string]bool{}
	windowIDsByUID := map[string]map[string]bool{}
	for _, row := range rows {
		paneCount[row.windowID]++
		if !seenWindow[row.windowID] {
			windowCount[row.sessionID]++
			seenWindow[row.windowID] = true
		}
		if row.windowUID != "" {
			if windowIDsByUID[row.windowUID] == nil {
				windowIDsByUID[row.windowUID] = map[string]bool{}
			}
			windowIDsByUID[row.windowUID][row.windowID] = true
		}
		if targetUIDs[row.paneUID] {
			removedCount[row.windowID]++
			byUID[row.paneUID] = append(byUID[row.paneUID], row)
		}
	}

	currentPaneID, currentSocket, err := r.currentInvocationPane(ctx)
	if err != nil {
		return paneLiveDeletePlan{}, err
	}
	if plan.Implicit && currentSocket == "" {
		return paneLiveDeletePlan{}, errors.New("delete pane: implicit active target is not attached to the exact projmux tmux socket")
	}

	live := paneLiveDeletePlan{}
	for _, target := range planned {
		_, window, root, ancestryErr := paneRegistryAncestry(registry, target.paneUID)
		if ancestryErr != nil {
			return paneLiveDeletePlan{}, fmt.Errorf("delete %s: %w", strings.ToLower(string(plan.Kind)), ancestryErr)
		}
		matches := byUID[target.paneUID]
		if len(matches) == 0 {
			return paneLiveDeletePlan{}, fmt.Errorf("delete %s: registry Pane uid %q has no exact live tmux Pane mirror on -L %s; nothing was changed",
				strings.ToLower(string(plan.Kind)), target.paneUID, r.target.value)
		}
		if len(matches) != 1 {
			return paneLiveDeletePlan{}, fmt.Errorf("delete %s: registry Pane uid %q has %d live tmux Pane mirrors on -L %s; exact target is ambiguous and nothing was changed",
				strings.ToLower(string(plan.Kind)), target.paneUID, len(matches), r.target.value)
		}
		row := matches[0]
		if row.windowUID != window.Metadata.UID {
			observed := row.windowUID
			if observed == "" {
				observed = "<missing>"
			}
			return paneLiveDeletePlan{}, fmt.Errorf("delete %s: live tmux Pane %s mirrors registry uid %q under foreign Window uid %q, want %q; nothing was changed",
				strings.ToLower(string(plan.Kind)), row.paneID, target.paneUID, observed, window.Metadata.UID)
		}
		if mirrors := len(windowIDsByUID[window.Metadata.UID]); mirrors != 1 {
			return paneLiveDeletePlan{}, fmt.Errorf("delete %s: registry Window uid %q has %d live tmux Window mirrors on -L %s while resolving Pane uid %q; exact owner is ambiguous and nothing was changed",
				strings.ToLower(string(plan.Kind)), window.Metadata.UID, mirrors, r.target.value, target.paneUID)
		}
		spelling := "delete " + strings.ToLower(string(plan.Kind))
		if err := root.validateLiveSession(spelling, "Pane", row.paneID, target.paneUID, row.projectUID, row.sessionName); err != nil {
			return paneLiveDeletePlan{}, err
		}
		if plan.Implicit && row.paneID != currentPaneID {
			return paneLiveDeletePlan{}, fmt.Errorf("delete %s: implicit active registry uid %q mirrors live Pane %s but the exact caller is in %s; nothing was changed",
				strings.ToLower(string(plan.Kind)), target.paneUID, row.paneID, currentPaneID)
		}
		live.Targets = append(live.Targets, paneLiveDeleteTarget{
			ResourceUID: target.resourceUID, PaneUID: target.paneUID, PaneID: row.paneID,
			WindowUID: window.Metadata.UID, WindowID: row.windowID,
			SessionID: row.sessionID, SessionName: row.sessionName, RootKind: root.Kind, RootUID: root.UID,
			Self: row.paneID == currentPaneID && currentSocket != "",
		})
	}

	endingWindows := map[string]bool{}
	markedWindow := map[string]bool{}
	for i := len(live.Targets) - 1; i >= 0; i-- {
		target := &live.Targets[i]
		if !markedWindow[target.WindowID] && removedCount[target.WindowID] == paneCount[target.WindowID] {
			target.EndsWindow = true
			endingWindows[target.WindowID] = true
			markedWindow[target.WindowID] = true
		}
	}
	endingWindowCount := map[string]int{}
	for _, target := range live.Targets {
		if target.EndsWindow {
			endingWindowCount[target.SessionID]++
		}
	}
	markedSession := map[string]bool{}
	for i := len(live.Targets) - 1; i >= 0; i-- {
		target := &live.Targets[i]
		if target.EndsWindow && !markedSession[target.SessionID] && endingWindowCount[target.SessionID] == windowCount[target.SessionID] {
			target.EndsSession = true
			markedSession[target.SessionID] = true
		}
	}
	return live, nil
}

func (r *tmuxPaneDeleteRuntime) currentInvocationPane(ctx context.Context) (string, string, error) {
	if r.getenv == nil || strings.TrimSpace(r.getenv("TMUX")) == "" || strings.TrimSpace(r.getenv("TMUX_PANE")) == "" {
		return "", "", nil
	}
	inheritedSocket, _, _ := strings.Cut(strings.TrimSpace(r.getenv("TMUX")), ",")
	serverSocket, err := r.routed().Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		return "", "", tmuxError("delete pane: inspect exact caller socket: %v", err)
	}
	if strings.TrimSpace(string(serverSocket)) != inheritedSocket {
		return "", "", nil
	}
	out, err := r.routed().Run(ctx, "tmux", "display-message", "-p", "-t", strings.TrimSpace(r.getenv("TMUX_PANE")),
		"-F", tmuxRowFormat("#{socket_path}", "#{pane_id}"))
	if err != nil {
		return "", "", tmuxError("delete pane: inspect exact caller pane %s: %v", strings.TrimSpace(r.getenv("TMUX_PANE")), err)
	}
	rows := splitTmuxRows(string(out), 2)
	if len(rows) != 1 || strings.TrimSpace(rows[0][0]) != inheritedSocket {
		return "", "", nil
	}
	paneID := exactTmuxHandle(strings.TrimSpace(rows[0][1]), "%")
	if paneID == "" {
		return "", "", errors.New("delete pane: exact caller tmux Pane handle is malformed")
	}
	return paneID, inheritedSocket, nil
}

func (r *tmuxPaneDeleteRuntime) kill(ctx context.Context, target paneLiveDeleteTarget) error {
	_, err := r.routed().Run(ctx, "tmux", "kill-pane", "-t", target.PaneID)
	if err != nil {
		return tmuxError("delete pane: kill exact live Pane %s in Window %s session %s (%s): %v",
			target.PaneID, target.WindowID, target.SessionName, target.SessionID, err)
	}
	return nil
}

func (r *tmuxPaneDeleteRuntime) tombstoneSelfKill(ctx context.Context, targets []paneLiveDeleteTarget) error {
	// Validate the entire set before changing any mirror. This runs under the
	// Registry transaction lock and before its delete is committed, so failure
	// can restore every earlier marker while all resource identities still exist.
	for _, target := range targets {
		out, err := r.routed().Run(ctx, "tmux", "show-options", "-pqv", "-t", target.PaneID, tmuxopts.PaneUID)
		if err != nil {
			return tmuxError("revalidate exact live Pane %s before self-target tombstone: %v", target.PaneID, err)
		}
		observed := strings.TrimSpace(string(out))
		if observed != target.PaneUID {
			if observed == "" {
				observed = "<missing>"
			}
			return fmt.Errorf("delete pane: live Pane %s mirrors uid %q, want registry uid %q; no self-target tombstone was written and the Registry remains unchanged",
				target.PaneID, observed, target.PaneUID)
		}
	}
	for i, target := range targets {
		if _, err := r.routed().Run(ctx, "tmux", "set-option", "-pq", "-t", target.PaneID,
			tmuxopts.PaneUID, deletedPaneMirrorPrefix+target.PaneUID); err != nil {
			marked := targets[:i]
			if restoreErr := r.restoreSelfKill(ctx, marked); restoreErr != nil {
				return fmt.Errorf("%w; rollback of earlier exact Pane tombstone(s) %s was incomplete: %v; Registry resources remain authoritative and the reported tombstone drift cannot be orphan-imported",
					tmuxError("tombstone exact live Pane %s before Registry commit: %v", target.PaneID, err),
					paneDeleteIDs(marked), restoreErr)
			}
			return fmt.Errorf("%w; earlier exact Pane tombstone(s) %s were restored and Registry resources remain unchanged",
				tmuxError("tombstone exact live Pane %s before Registry commit: %v", target.PaneID, err),
				paneDeleteIDs(marked))
		}
	}
	return nil
}

func (r *tmuxPaneDeleteRuntime) restoreSelfKill(ctx context.Context, targets []paneLiveDeleteTarget) error {
	var failures []string
	for _, target := range targets {
		out, err := r.routed().Run(ctx, "tmux", "show-options", "-pqv", "-t", target.PaneID, tmuxopts.PaneUID)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s inspect: %v", target.PaneID, err))
			continue
		}
		observed := strings.TrimSpace(string(out))
		want := deletedPaneMirrorPrefix + target.PaneUID
		if observed != want {
			if observed == "" {
				observed = "<missing>"
			}
			failures = append(failures, fmt.Sprintf("%s mirrors %q, want tombstone %q", target.PaneID, observed, want))
			continue
		}
		if _, err := r.routed().Run(ctx, "tmux", "set-option", "-pq", "-t", target.PaneID,
			tmuxopts.PaneUID, target.PaneUID); err != nil {
			failures = append(failures, fmt.Sprintf("%s restore: %v", target.PaneID, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("restore exact Pane tombstone(s): %s", strings.Join(failures, "; "))
	}
	return nil
}

func paneDeleteIDs(targets []paneLiveDeleteTarget) string {
	if len(targets) == 0 {
		return "<none>"
	}
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, fmt.Sprintf("%s/pane-uid=%s", target.PaneID, target.PaneUID))
	}
	return strings.Join(ids, ",")
}

func (r *tmuxPaneDeleteRuntime) queueSelfKill(ctx context.Context, targets []paneLiveDeleteTarget) error {
	// Every target was tombstoned before the Registry commit. Revalidate the
	// whole set before queueing so a recycled %N is never mutated.
	var verified []paneLiveDeleteTarget
	var drift []string
	for _, target := range targets {
		out, err := r.routed().Run(ctx, "tmux", "show-options", "-pqv", "-t", target.PaneID, tmuxopts.PaneUID)
		if err != nil {
			drift = append(drift, fmt.Sprintf("%s/pane-uid=%s inspect-error=%v", target.PaneID, target.PaneUID, err))
			continue
		}
		observed := strings.TrimSpace(string(out))
		want := deletedPaneMirrorPrefix + target.PaneUID
		if observed != want {
			if observed == "" {
				observed = "<missing>"
			}
			drift = append(drift, fmt.Sprintf("%s/pane-uid=%s observed=%q want=%q", target.PaneID, target.PaneUID, observed, want))
			continue
		}
		verified = append(verified, target)
	}
	if len(drift) > 0 {
		return fmt.Errorf("delete pane: post-commit exact Pane tombstone revalidation failed; no self-target kill was queued; unverified drift: %s; verified tombstone(s) %s remain retryable and cannot be orphan-imported",
			strings.Join(drift, "; "), paneDeleteIDs(verified))
	}
	ordered := selfLastPaneDeleteTargets(targets)
	for _, target := range ordered {
		command := "exec " + shellQuote("tmux") + " " + shellQuote(r.target.flag) + " " +
			shellQuote(r.target.value) + " kill-pane -t " + shellQuote(target.PaneID)
		if _, err := r.routed().Run(ctx, "tmux", "run-shell", "-b", command); err != nil {
			queued := ordered[:indexPaneDeleteTarget(ordered, target.PaneID)]
			remaining := ordered[len(queued):]
			return fmt.Errorf("%w; queued exact Pane(s) %s may complete, while tombstoned unqueued Pane(s) %s remain as retryable drift and cannot be orphan-imported",
				tmuxError("queue exact live Pane %s in Window %s session %s (%s) for self-target deletion: %v",
					target.PaneID, target.WindowID, target.SessionName, target.SessionID, err),
				paneDeleteIDs(queued), paneDeleteIDs(remaining))
		}
	}
	return nil
}

func selfLastPaneDeleteTargets(targets []paneLiveDeleteTarget) []paneLiveDeleteTarget {
	ordered := make([]paneLiveDeleteTarget, 0, len(targets))
	for _, target := range targets {
		if !target.Self {
			ordered = append(ordered, target)
		}
	}
	for _, target := range targets {
		if target.Self {
			ordered = append(ordered, target)
		}
	}
	return ordered
}

func indexPaneDeleteTarget(targets []paneLiveDeleteTarget, paneID string) int {
	for i, target := range targets {
		if target.PaneID == paneID {
			return i
		}
	}
	return 0
}
