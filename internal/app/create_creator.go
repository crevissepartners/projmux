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
	"github.com/crevissepartners/projmux/internal/core/operatorclient"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

// Creator provenance: every Agent create records, on the new Agent and its
// managed Pane and in the same Registry transaction that commits them, the
// strongest creator evidence it has, and the kind of that evidence
// (coremetadata.CreatorBasis*):
//
//  1. pane-chain: an explicit `create agent` (any spelling, including
//     --create-window and a fan-out) or `create window --provider` ran inside
//     an Agent's managed Pane, proven by the checks below. It always wins.
//  2. process-chain: the caller descends from a live process provider child,
//     verified by kernel identity and current Registry binding.
//  3. explicit: the caller declared `--creator uid:<agent>` and no pane chain
//     or process chain was recorded. A declaration that names no Agent refuses
//     the create before anything changes; one that disagrees with an observed chain
//     is dropped with one stderr line.
//  4. operator: the create ran in process for a named operator client -- the
//     UI intents (client "ui"), or a client layered on top of projmux through
//     recordOperatorCreator. Such a create observes no pane chain, because
//     its process environment says nothing about who asked.
//  5. nothing, when there is no evidence.
//
// The record is provenance, never authentication. Apart from the refused
// declaration, no outcome changes anything else about the create: not the
// target, the route, the topology, stdout, or the exit code. The route's own
// masking of the ambient Pane for an explicit target is untouched; the
// pane-chain observation reads the unmasked environment on its own and never
// feeds back into route authority.
//
// The pane-chain checks are ordered cheapest first. The Registry checks run in
// memory inside the transaction, so an ambient Pane that is not a live Agent
// Pane costs zero extra tmux calls; only when they pass does one
// `display-message` confirm the Pane on the route's own server, and then one
// bounded /proc walk.

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
	basis    string
	// skip is the reason token when an ambient Pane existed but recording was
	// skipped. Empty with no agentUID means there was nothing to observe.
	// Only the tokens creatorSkipIsSilent rejects are printed.
	skip string
}

func (p creatorProvenance) recorded() bool { return p.agentUID != "" && p.paneUID != "" }

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

// observeCreator runs the pane-chain checks against the transaction's working
// Registry, which reconcile has already refreshed. It never fails the create.
func (c *createCommand) observeCreator(ctx context.Context, working *coremetadata.Registry) creatorProvenance {
	if c == nil {
		return creatorProvenance{}
	}
	observed := observePaneChainActor(ctx, c.lookupEnv, c.processAncestors, working,
		func(ctx context.Context, paneID, _ string) (int, string) { return c.confirmCreatorAnchor(ctx, paneID) })
	if observed.recorded() {
		observed.basis = coremetadata.CreatorBasisPaneChain
		return observed
	}
	process := c.observeProcessCreator(working)
	if process.recorded() || process.skip != "" {
		return process
	}
	return observed
}

// Declaration tokens, printed as `creator declaration not recorded: <token>`
// when a valid --creator loses to stronger evidence. A closed vocabulary.
const (
	creatorDeclinedProcessChain        = "process-chain-disagrees"
	creatorDeclinedPaneChain           = "pane-chain-disagrees"
	creatorDeclinedOperator            = "operator-client"
	creatorDeclinedDiagnosticFmt       = "creator declaration not recorded: %s (--creator uid:%s)\n"
	creatorFlagName                    = "creator"
	creatorFlagUsage                   = "Agent that created this one, as uid:<agent>; recorded as the explicit creator basis only when no pane or process chain is observed"
	creatorFlagRequiresAgentRefusalFmt = "%s --creator applies only to an Agent; name an Agent --provider; nothing was created"
)

// uiOperatorClient is the operator client name the UI intents record.
const uiOperatorClient = "ui"

// creatorRecord is what one create invocation records on every Agent it
// allocates. It is decided once per create, before the first allocation, and
// reused for the whole fan-out.
type creatorRecord struct {
	// basis is one coremetadata.CreatorBasis*, or empty for no record.
	basis    string
	agentUID string
	paneUID  string
	client   string
	// observed is the pane-chain observation, kept for its skip line when
	// nothing was recorded.
	observed creatorProvenance
	// declared is the bare Agent UID of a --creator the record did not use,
	// and declined names why.
	declared string
	declined string
}

// newOperatorCreator is the operator creator record, or no record for a name
// outside the client rule. Its producers are the in-process seams only:
// TestNoArgvPathBuildsAnOperatorCreator holds the list.
func newOperatorCreator(client string) creatorRecord {
	if operatorclient.Validate(client) != nil {
		return creatorRecord{}
	}
	return creatorRecord{basis: coremetadata.CreatorBasisOperator, client: client}
}

// recordOperatorCreator makes every later create of this command record the
// operator creator client instead of observing a pane chain. It is the seam
// for an operator client that runs creates in process on the operator's behalf,
// whose inherited environment and parent chain say nothing about who asked.
// There is no argv spelling of it.
func (c *createCommand) recordOperatorCreator(client string) error {
	if err := operatorclient.Validate(client); err != nil {
		return err
	}
	c.operatorCreatorClient = client
	return nil
}

// parseCreatorFlag checks the argv-only shape of a --creator value and returns
// the bare Agent UID. Whether that Agent exists is decided in the transaction.
func parseCreatorFlag(spelling, value string) (string, error) {
	bare, ok := strings.CutPrefix(value, "uid:")
	if !ok || strings.TrimSpace(bare) != bare || bare == "" || strings.ContainsAny(bare, ":/ \t") {
		return "", usageError(fmt.Sprintf("%s --creator must be an exact Agent reference uid:<agent>; got %q; nothing was created", spelling, value))
	}
	return bare, nil
}

// decideCreator picks the one record this create writes. declared is the bare
// Agent UID of --creator, or empty. A declaration that names no Agent in the
// working Registry is refused, whatever else the create would record: the
// argv is wrong on its own terms.
func (c *createCommand) decideCreator(ctx context.Context, spelling string, working *coremetadata.Registry, declared string) (creatorRecord, error) {
	if declared != "" {
		if _, ok := working.Agent(declared); !ok {
			return creatorRecord{}, usageError(fmt.Sprintf("%s --creator uid:%s names no Agent in the Registry; nothing was created", spelling, declared))
		}
	}
	if c != nil && c.operatorCreatorClient != "" {
		record := newOperatorCreator(c.operatorCreatorClient)
		if declared != "" {
			record.declared, record.declined = declared, creatorDeclinedOperator
		}
		return record, nil
	}
	observed := c.observeCreator(ctx, working)
	if observed.recorded() {
		record := creatorRecord{basis: observed.basis, agentUID: observed.agentUID, paneUID: observed.paneUID}
		if declared != "" && declared != observed.agentUID {
			record.declared, record.declined = declared, creatorDeclinedPaneChain
			if observed.basis == coremetadata.CreatorBasisProcessChain {
				record.declined = creatorDeclinedProcessChain
			}
		}
		return record, nil
	}
	if declared != "" {
		return creatorRecord{basis: coremetadata.CreatorBasisExplicit, agentUID: declared, observed: observed}, nil
	}
	return creatorRecord{observed: observed}, nil
}

// annotations is the map CreateAgentOptions.Annotations takes: nil when there
// is no record.
func (r creatorRecord) annotations() map[string]string {
	switch r.basis {
	case coremetadata.CreatorBasisPaneChain:
		return coremetadata.CreatorAnnotations(r.agentUID, r.paneUID)
	case coremetadata.CreatorBasisProcessChain:
		return coremetadata.ProcessCreatorAnnotations(r.agentUID, r.paneUID)
	case coremetadata.CreatorBasisExplicit:
		return coremetadata.ExplicitCreatorAnnotations(r.agentUID)
	case coremetadata.CreatorBasisOperator:
		return coremetadata.OperatorCreatorAnnotations(r.client)
	}
	return nil
}

// withAnnotations returns base with the record's keys added, as a new map. With
// no record it returns base itself.
func (r creatorRecord) withAnnotations(base map[string]string) map[string]string {
	annotations := r.annotations()
	if annotations == nil {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, len(annotations))
	}
	maps.Copy(out, annotations)
	return out
}

// forAgent is the record for one allocated Agent. The creator is never the
// created Agent itself: if the record would name it, the keys are removed from
// the stored Agent and nothing is recorded on its Pane. Every basis already
// resolves its Agent before allocation, so this is a guarantee, not a path.
func (r creatorRecord) forAgent(working *coremetadata.Registry, agentUID string) creatorRecord {
	if r.agentUID == "" || r.agentUID != agentUID {
		return r
	}
	if stored, ok := working.Agent(agentUID); ok {
		for key := range r.annotations() {
			delete(stored.Metadata.Annotations, key)
		}
	}
	return creatorRecord{observed: r.observed}
}

// annotatePane records the same keys on the managed Pane the create just
// attached, in the working Registry of the same transaction.
func (r creatorRecord) annotatePane(working *coremetadata.Registry, pane coremetadata.Pane) coremetadata.Pane {
	annotations := r.annotations()
	if annotations == nil || working == nil {
		return pane
	}
	stored, ok := working.Pane(pane.Metadata.UID)
	if !ok {
		return pane
	}
	if stored.Metadata.Annotations == nil {
		stored.Metadata.Annotations = map[string]string{}
	}
	maps.Copy(stored.Metadata.Annotations, annotations)
	return stored.Clone()
}

// report prints what a committed create owes stderr: the pane-chain skip line
// when nothing was recorded, and one line for a declaration it did not use.
func (r creatorRecord) report(stderr io.Writer) {
	if r.basis == "" {
		r.observed.reportSkip(stderr)
	}
	if r.declined != "" && stderr != nil {
		_, _ = fmt.Fprintf(stderr, creatorDeclinedDiagnosticFmt, r.declined, r.declared)
	}
}

// paneChainAnchorConfirm is the one server-side step of the pane-chain
// judgment: prove the ambient `%N` is the Registry Pane paneUID on the exact
// server the calling operation addresses, and return that Pane's process id.
// A non-empty second result is a creatorSkip* token.
type paneChainAnchorConfirm func(ctx context.Context, paneID, paneUID string) (int, string)

// observePaneChainActor is the pane-chain judgment creation provenance and
// deletion records share. Its four steps run cheapest first: the ambient `%N`
// read from the unmasked environment, the in-memory Registry round trip, the
// injected server confirmation, and the bounded /proc descent. Only the server
// confirmation differs between callers, because each one proves the Pane on
// its own route. It never fails the operation that asks.
func observePaneChainActor(ctx context.Context, lookupEnv func(string) string, processAncestors func() ([]int, error),
	working *coremetadata.Registry, confirm paneChainAnchorConfirm,
) creatorProvenance {
	if processAncestors == nil || lookupEnv == nil || working == nil || confirm == nil {
		return creatorProvenance{}
	}
	// The same ambient order the route resolver reads, but on the unmasked
	// environment and with no explicit route anchor: an explicit target masks
	// the ambient Pane for route authority, not for provenance.
	paneID, err := resolveRuntimeMutationAnchorPane(lookupEnv, "")
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
	panePID, skip := confirm(ctx, paneID, paneUID)
	if skip != "" {
		return creatorProvenance{skip: skip}
	}
	chain, err := processAncestors()
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

// Process candidates must be provider children, never just a shared host.
const (
	creatorSkipProcessIdentity  = "process-child-identity-mismatch"
	creatorSkipProcessBinding   = "process-binding-mismatch"
	creatorSkipProcessAmbiguous = "process-child-ambiguous"
)

func (c *createCommand) observeProcessCreator(working *coremetadata.Registry) creatorProvenance {
	// The existing ancestry seam is also the in-process opt-out from ambient
	// provenance. Honor it for both pane and process observations.
	if c == nil || working == nil || c.processAncestors == nil || c.processCreatorAncestors == nil {
		return creatorProvenance{}
	}
	chain, err := c.processCreatorAncestors()
	// A partial walk is not evidence: no guessed ancestry on read failure.
	if err != nil || len(chain) == 0 || len(chain) > creatorProcessChainMaxSteps {
		// A partially observed provider candidate deserves a stable diagnostic;
		// an unrelated shell still has no creator claim to report.
		for _, identity := range chain {
			for _, pane := range working.Panes {
				if activation := pane.Status.Activation.Process; activation != nil && activation.Child.PID == identity.PID {
					return creatorProvenance{skip: creatorSkipProcessUnobservable}
				}
			}
		}
		return creatorProvenance{}
	}
	var rejected string
	for _, identity := range chain {
		var matches []creatorProvenance
		for _, pane := range working.Panes {
			activation := pane.Status.Activation.Process
			if activation == nil || activation.Child.PID != identity.PID {
				continue
			}
			if !identity.Valid() || int64(identity.OwnerUID) != int64(os.Getuid()) || activation.Child != identity {
				rejected = creatorSkipProcessIdentity
				continue
			}
			binding := activation.Binding
			current, _, ok := working.CurrentProcessActivation(binding)
			agent, found := working.Agent(binding.AgentUID)
			_, missing := pane.HasCondition(coremetadata.ConditionMissingRuntime)
			if !ok || !found || missing || agent.Status.Phase != coremetadata.PhaseRunning ||
				binding.PaneUID != pane.Metadata.UID || current != *activation ||
				pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != binding ||
				pane.Status.ProcessSession.Provider != agent.Spec.Provider {
				rejected = creatorSkipProcessBinding
				continue
			}
			matches = append(matches, creatorProvenance{agentUID: binding.AgentUID, paneUID: binding.PaneUID, basis: coremetadata.CreatorBasisProcessChain})
		}
		if len(matches) > 1 {
			return creatorProvenance{skip: creatorSkipProcessAmbiguous}
		}
		if len(matches) == 1 {
			return matches[0]
		}
	}
	return creatorProvenance{skip: rejected}
}

func processCreatorAncestry() ([]coremetadata.ProcessIdentity, error) {
	pid := os.Getpid()
	chain := make([]coremetadata.ProcessIdentity, 0, 8)
	for range creatorProcessChainMaxSteps {
		identity, parent, err := localipc.Process(pid)
		if err != nil {
			return chain, err
		}
		chain = append(chain, identity)
		if parent <= 1 || parent == pid {
			break
		}
		pid = parent
	}
	return chain, nil
}
