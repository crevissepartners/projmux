package app

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// Creator provenance: when an explicit `create agent` (any spelling, including
// --create-window and a fan-out) or `create window --provider` runs inside an
// Agent's managed Pane, the new Agent and its managed Pane record which Agent
// and Pane that was, in the same Registry transaction that commits them.
//
// The record is provenance, never authentication, and it is written only when
// every check below holds. A failed check writes none of the keys and changes
// nothing else about the create: not the target, the route, the topology,
// stdout, or the exit code. The route's own masking of the ambient Pane for an
// explicit target is untouched; this observation reads the unmasked
// environment on its own and never feeds back into route authority.
//
// Cost is ordered cheapest first. The Registry checks run in memory inside the
// transaction, so an ambient Pane that is not a live Agent Pane costs zero
// extra tmux calls; only when they pass does one `display-message` confirm the
// Pane on the route's own server, and then one bounded /proc walk.

// Reason tokens, printed as `creator not recorded: <token>` unless
// creatorSkipIsSilent says otherwise. They are a closed, stable vocabulary.
const (
	creatorSkipAnchorInvalid        = "anchor-invalid"
	creatorSkipPaneUnregistered     = "anchor-pane-unregistered"
	creatorSkipPaneAmbiguous        = "anchor-pane-ambiguous"
	creatorSkipCallerNotAgent       = "caller-pane-not-agent"
	creatorSkipPaneRefMismatch      = "pane-ref-mismatch"
	creatorSkipServerUnproven       = "anchor-server-unproven"
	creatorSkipAnchorQueryFailed    = "anchor-query-failed"
	creatorSkipServerMismatch       = "anchor-server-mismatch"
	creatorSkipAnchorPaneMismatch   = "anchor-pane-mismatch"
	creatorSkipProcessUnobservable  = "process-chain-unobservable"
	creatorSkipNotPaneDescendant    = "not-pane-descendant"
	creatorNotRecordedDiagnosticFmt = "creator not recorded: %s\n"
)

// creatorProcessChainMaxSteps bounds the parent-chain walk.
const creatorProcessChainMaxSteps = 64

// creatorProvenance is one create invocation's creator observation. It is
// computed once per create and reused for every Agent a fan-out allocates.
type creatorProvenance struct {
	agentUID string
	paneUID  string
	// skip is the reason token when an ambient Pane existed but recording was
	// skipped. Empty with no agentUID means there was nothing to observe.
	// Only the tokens creatorSkipIsSilent rejects are printed.
	skip string
}

func (p creatorProvenance) recorded() bool { return p.agentUID != "" && p.paneUID != "" }

// annotations is the map CreateAgentOptions.Annotations takes: nil unless
// every check passed.
func (p creatorProvenance) annotations() map[string]string {
	if !p.recorded() {
		return nil
	}
	return coremetadata.CreatorAnnotations(p.agentUID, p.paneUID)
}

// annotatePane records the same keys on the managed Pane the create just
// attached, in the working Registry of the same transaction.
func (p creatorProvenance) annotatePane(working *coremetadata.Registry, pane coremetadata.Pane) coremetadata.Pane {
	if !p.recorded() || working == nil {
		return pane
	}
	stored, ok := working.Pane(pane.Metadata.UID)
	if !ok {
		return pane
	}
	if stored.Metadata.Annotations == nil {
		stored.Metadata.Annotations = map[string]string{}
	}
	maps.Copy(stored.Metadata.Annotations, p.annotations())
	return stored.Clone()
}

// creatorSkipIsSilent names the skips that print nothing: the ambient Pane
// never looked like an Agent Pane, so a create run from an ordinary shell or
// from outside projmux's Registry is not told about a record it was never
// going to get. Every other skip means a live Agent Pane was the ambient
// anchor and a later check failed, which is worth one line.
func creatorSkipIsSilent(skip string) bool {
	switch skip {
	case "", creatorSkipAnchorInvalid, creatorSkipPaneUnregistered, creatorSkipCallerNotAgent:
		return true
	}
	return false
}

// reportSkip prints the one diagnostic line a committed create owes when the
// ambient Pane was a live Agent Pane and a later check stopped the record. It
// never touches stdout.
func (p creatorProvenance) reportSkip(stderr io.Writer) {
	if p.recorded() || creatorSkipIsSilent(p.skip) || stderr == nil {
		return
	}
	_, _ = fmt.Fprintf(stderr, creatorNotRecordedDiagnosticFmt, p.skip)
}

// withoutCreatorProvenance withdraws the observation seam. A create run
// in-process on behalf of another caller -- the web API -- inherits that
// server's environment and parent chain, which say nothing about who asked.
func (c *createCommand) withoutCreatorProvenance() {
	if c != nil {
		c.processAncestors = nil
	}
}

// observeCreator runs the creator checks against the transaction's working
// Registry, which reconcile has already refreshed. It never fails the create.
func (c *createCommand) observeCreator(ctx context.Context, working *coremetadata.Registry) creatorProvenance {
	if c == nil || c.processAncestors == nil || c.lookupEnv == nil || working == nil {
		return creatorProvenance{}
	}
	// The same ambient order the route resolver reads, but on the unmasked
	// environment and with no explicit route anchor: an explicit target masks
	// the ambient Pane for route authority, not for provenance.
	paneID, err := resolveRuntimeMutationAnchorPane(c.lookupEnv, "")
	if err != nil {
		return creatorProvenance{skip: creatorSkipAnchorInvalid}
	}
	if paneID == "" {
		return creatorProvenance{}
	}
	agentUID, paneUID, skip := registryCreatorPane(working, paneID)
	if skip != "" {
		return creatorProvenance{skip: skip}
	}
	panePID, skip := c.confirmCreatorAnchor(ctx, paneID)
	if skip != "" {
		return creatorProvenance{skip: skip}
	}
	chain, err := c.processAncestors()
	if !slices.Contains(chain, panePID) {
		if err != nil || len(chain) == 0 {
			return creatorProvenance{skip: creatorSkipProcessUnobservable}
		}
		return creatorProvenance{skip: creatorSkipNotPaneDescendant}
	}
	return creatorProvenance{agentUID: agentUID, paneUID: paneUID}
}

// registryCreatorPane is the in-memory half: exactly one live Pane
// Registry-wide carries the ambient runtime id, that Pane is Agent-owned, and
// the owning Agent's status.paneRef names it back.
func registryCreatorPane(working *coremetadata.Registry, paneID string) (string, string, string) {
	var match *coremetadata.Pane
	count := 0
	for i := range working.Panes {
		pane := &working.Panes[i]
		if pane.Status.Activation.RuntimeID != paneID {
			continue
		}
		// Reconcile has run inside this transaction, so a Pane whose uid no
		// live tmux object mirrors carries MissingRuntime here.
		if _, missing := pane.HasCondition(coremetadata.ConditionMissingRuntime); missing {
			continue
		}
		count++
		match = pane
	}
	switch {
	case count == 0:
		return "", "", creatorSkipPaneUnregistered
	case count > 1:
		return "", "", creatorSkipPaneAmbiguous
	}
	owner := match.Metadata.OwnerRef
	if owner == nil || owner.Kind != coremetadata.KindAgent || strings.TrimSpace(owner.UID) == "" {
		return "", "", creatorSkipCallerNotAgent
	}
	agent, ok := working.Agent(owner.UID)
	if !ok || agent.Status.PaneRef != match.Metadata.UID {
		return "", "", creatorSkipPaneRefMismatch
	}
	return agent.Metadata.UID, match.Metadata.UID, ""
}

// confirmCreatorAnchor issues the single extra tmux read: the ambient Pane
// must exist on the exact app-owned server this create's route already bound.
// It returns that Pane's process id.
func (c *createCommand) confirmCreatorAnchor(ctx context.Context, paneID string) (int, string) {
	runtime := c.runtime
	if runtime == nil || runtime.runner == nil || strings.TrimSpace(runtime.expectedSocketPath) == "" ||
		runtime.routeAuthority == nil || runtime.routeAuthority.Class != runtimeMutationRouteApp ||
		strings.TrimSpace(runtime.routeAuthority.ServerPID) == "" {
		return 0, creatorSkipServerUnproven
	}
	out, err := runtime.routedRunner().Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F",
		tmuxRowFormat("#{socket_path}", "#{pid}", "#{pane_id}", "#{pane_pid}"))
	if err != nil {
		return 0, creatorSkipAnchorQueryFailed
	}
	rows := splitTmuxRows(string(out), 4)
	if len(rows) != 1 {
		return 0, creatorSkipAnchorQueryFailed
	}
	row := rows[0]
	if row[0] != runtime.expectedSocketPath || row[1] != runtime.routeAuthority.ServerPID {
		return 0, creatorSkipServerMismatch
	}
	if row[2] != paneID {
		return 0, creatorSkipAnchorPaneMismatch
	}
	pid, err := strconv.Atoi(strings.TrimSpace(row[3]))
	if err != nil || pid <= 1 {
		return 0, creatorSkipAnchorQueryFailed
	}
	return pid, ""
}

// processAncestry is the production parent-chain walker: this process and its
// ancestors, nearest first, bounded, stopping at pid 1. A read that fails
// mid-walk returns what was observed so far with the error.
func processAncestry() ([]int, error) {
	pid := os.Getpid()
	chain := make([]int, 0, 8)
	for range creatorProcessChainMaxSteps {
		_, parent, err := localipc.Process(pid)
		if err != nil {
			return chain, err
		}
		chain = append(chain, pid)
		if parent <= 1 || parent == pid {
			break
		}
		pid = parent
	}
	return chain, nil
}
