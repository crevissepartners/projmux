package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

// These are test evidence DTOs, deliberately separate from Registry/response
// types which can contain prompts, progress text, approvals and raw errors.
type installedRecoveryAttempt struct {
	InputIndex   int                            `json:"inputIndex"`
	Sample       string                         `json:"sample"`
	Stage        string                         `json:"stage"`
	AgentUID     string                         `json:"agentUID,omitempty"`
	Selection    *installedConnectionSelection  `json:"selection,omitempty"`
	Observations []installedRecoveryObservation `json:"observations,omitempty"`
	Failure      *installedRecoveryFailure      `json:"failure,omitempty"`
}

// Every fresh create/turn input crosses the same verified selection guard as
// the zero-input connection diagnostic. A refused fixture cannot increment the
// submission ledger or invoke the installed command through a fallback CLI.
func submitInstalledRecoveryInput(ledger *installedRecoveryLedger, attempt *installedRecoveryAttempt, selection installedConnectionSelection, save func(), submit func() string) (string, error) {
	attempt.Selection = &selection
	if !selection.verified() {
		attempt.Stage = "selection-refused"
		save()
		return "", errors.New("fixture execution selection is unverified")
	}
	ledger.Submissions++
	save()
	return submit(), nil
}

type installedRecoveryObservation struct {
	At              string                         `json:"at,omitempty"`
	Stage           string                         `json:"stage"`
	Error           string                         `json:"error,omitempty"`
	Identity        installedRecoveryAgent         `json:"identity"`
	AgentPhase      string                         `json:"agentPhase,omitempty"`
	ActivationState string                         `json:"activationState,omitempty"`
	OwnerAgent      string                         `json:"ownerAgent,omitempty"`
	ActivationAgent string                         `json:"activationAgent,omitempty"`
	PaneThread      string                         `json:"paneThread,omitempty"`
	PaneTurn        string                         `json:"paneTurn,omitempty"`
	HasStartedTurn  bool                           `json:"hasStartedTurn"`
	Endpoint        *coremetadata.CodexEndpointRef `json:"endpoint,omitempty"`
	Lifecycle       string                         `json:"lifecycle,omitempty"`
	Termination     *installedRecoveryTermination  `json:"termination,omitempty"`
	Live            *installedRecoveryLive         `json:"live,omitempty"`
	Control         *installedRecoveryControl      `json:"control,omitempty"`
}

type installedRecoveryTermination struct {
	Source         string `json:"source"`
	Classification string `json:"classification"`
	ExitCode       *int   `json:"exitCode,omitempty"`
	Signal         string `json:"signal,omitempty"`
}

type installedRecoveryLive struct {
	Runtime   string `json:"runtime"`
	Pane      string `json:"pane"`
	Thread    string `json:"thread"`
	Authority string `json:"authority"`
	Epoch     string `json:"epoch"`
	Reason    string `json:"reason"`
	Error     string `json:"error,omitempty"`
}

type installedRecoveryControl struct {
	OK           bool                     `json:"ok"`
	Code         string                   `json:"code,omitempty"`
	Availability agentControlAvailability `json:"availability"`
}

type installedRecoveryFailure struct {
	Processes        []codexinstalled.OwnedProcess `json:"processes,omitempty"`
	ProcessError     string                        `json:"processError,omitempty"`
	PanePID          int                           `json:"panePID,omitempty"`
	PaneDead         bool                          `json:"paneDead"`
	PaneExit         *int                          `json:"paneExit,omitempty"`
	PaneError        string                        `json:"paneError,omitempty"`
	Observer         []installedRecoveryTransition `json:"observer,omitempty"`
	JournalAvailable bool                          `json:"journalAvailable"`
}

type installedRecoveryTransition struct {
	At     string `json:"at"`
	Event  string `json:"event"`
	Reason string `json:"reason"`
	Epoch  string `json:"epoch"`
	Repeat int    `json:"repeat,omitempty"`
}

func recoveryToken(value string, allowed ...string) string {
	if value == "" {
		return ""
	}
	if slices.Contains(allowed, value) {
		return value
	}
	return "unclassified"
}

func recoveryEpoch(value string) string {
	if value == "" {
		return ""
	}
	first, second, ok := strings.Cut(value, "-")
	if !ok {
		return "unclassified"
	}
	for _, part := range []string{first, second} {
		if _, err := strconv.ParseUint(part, 10, 64); err != nil {
			return "unclassified"
		}
	}
	return value
}

func recoveryError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, os.ErrNotExist):
		return "missing"
	case errors.Is(err, os.ErrPermission):
		return "permission"
	case errors.Is(err, codexappserver.ErrEndpointChanged):
		return "endpoint-changed"
	case errors.Is(err, codexappserver.ErrProtocol):
		return "protocol-error"
	case errors.Is(err, codexappserver.ErrUnsupported):
		return "unsupported"
	case errors.Is(err, codexappserver.ErrDisconnected):
		return "transport-disconnected"
	}
	var route *codexNativeRouteError
	if errors.As(err, &route) {
		return "native-route:" + recoveryToken(route.Reason, codexNativeReasonGenerationUnavailable, codexNativeReasonLegacyEndpointMissing)
	}
	var frame *agentControlBindingFrameError
	if errors.As(err, &frame) {
		return "binding-frame:" + recoveryToken(frame.Reason, "multiple output lines", "mixed separator spellings", "separator is missing", "field count is not exact")
	}
	var binding *exactAgentControlBindingError
	if errors.As(err, &binding) {
		return "exact-binding:" + recoveryToken(binding.Reason, "the selected Agent has no current Running Pane", "Agent to Pane ownership is not exact", "activation generation or Codex thread identity is missing", "durable Codex thread does not match the Pane activation", "live activation authority does not match the durable endpoint generation", "live Pane identity no longer matches the activation", "canonical generation consumer fence is unavailable", "canonical generation consumer fence is stale")
	}
	var attach *codexappserver.AttachError
	if errors.As(err, &attach) {
		return "attach:" + recoveryToken(string(attach.Refusal), "none", "endpoint-not-ready", "protocol-mismatch", "version-skew", "runtime-version-unknown", "ownership-unknown", "connect-failed")
	}
	if reason := codexbroker.RefusalOf(err); reason != codexbroker.RefusalNone {
		return "broker:" + recoveryToken(string(reason), "host-unavailable", "discovery-untrusted", "binding-revoked", "endpoint-suspended", "lifecycle-unsupported", "lifecycle-protocol", "lifecycle-busy", "thread-absent", "thread-not-durable", "socket-path-too-long")
	}
	return "unclassified"
}

func recoveryTermination(terminal *coremetadata.TerminationEvidence) *installedRecoveryTermination {
	if terminal == nil {
		return nil
	}
	return &installedRecoveryTermination{Source: recoveryToken(string(terminal.Source), "supervisor", "control-action", "reconcile"), Classification: recoveryToken(string(terminal.Classification), "normal", "abnormal", "unknown", "intentional", "interrupted", "killed"), ExitCode: terminal.ExitCode, Signal: recoveryToken(terminal.Signal, "SIGHUP", "SIGINT", "SIGTERM", "SIGKILL")}
}

func recoveryRegistryObservation(registry coremetadata.Registry, uid, generation string, retired *coremetadata.CodexAuthorityRef) installedRecoveryObservation {
	out := installedRecoveryObservation{Stage: "agent-missing", Identity: installedRecoveryAgent{Agent: uid}}
	agent, ok := registry.Agent(uid)
	if !ok {
		return out
	}
	out.AgentPhase = recoveryToken(string(agent.Status.Phase), "Pending", "Running", "Offline", "Failed")
	out.Termination = recoveryTermination(agent.Status.LastTermination)
	out.ActivationState = recoveryToken(string(agent.Status.Activation.State), "not_requested", "pending", "acknowledged", "unconfirmed")
	out.Identity.Window = agent.Metadata.OwnerUID()
	out.Identity.Pane = agent.Status.PaneRef
	if window, ok := registry.Window(out.Identity.Window); ok {
		out.Identity.Project = window.Metadata.OwnerUID()
	}
	if agent.Status.SessionRef != nil && agent.Status.SessionRef.Codex != nil {
		ref := agent.Status.SessionRef.Codex
		out.Identity.Thread, out.Identity.Session = ref.ThreadID, ref.SessionID
		out.Endpoint = ref.Endpoint
		out.HasStartedTurn = ref.HasStartedTurn
		if ref.Lifecycle != nil {
			out.Lifecycle = recoveryToken(string(ref.Lifecycle.State), "preparing", "qualified", "current", "draining", "retired")
		}
	}
	pane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok {
		out.Stage = "pane-missing"
		return out
	}
	out.OwnerAgent = pane.Metadata.OwnerUID()
	out.ActivationAgent = pane.Status.Activation.AgentUID
	out.Identity.Runtime, out.Identity.Activation = pane.Status.Activation.RuntimeID, pane.Status.Activation.Generation
	if pane.Status.LastTermination != nil {
		out.Termination = recoveryTermination(pane.Status.LastTermination)
	}

	if pane.Status.Activation.Codex != nil {
		out.PaneThread, out.PaneTurn = pane.Status.Activation.Codex.ThreadID, pane.Status.Activation.Codex.TurnID
		if pane.Status.Activation.Codex.Authority != nil {
			out.Identity.Authority = *pane.Status.Activation.Codex.Authority
		}
	}
	switch {
	case out.Identity.Thread == "":
		out.Stage = "session-ref-missing"
	case out.OwnerAgent != uid || out.ActivationAgent != uid || out.PaneThread != out.Identity.Thread:
		out.Stage = "identity-mismatch"
	case !out.Identity.Authority.Valid():
		out.Stage = "authority-missing-or-invalid"
	case out.Endpoint == nil || !out.Endpoint.Same(out.Identity.Authority.Endpoint()):
		out.Stage = "endpoint-mismatch"
	case out.Identity.Authority.EndpointGenerationID != generation:
		out.Stage = "generation-mismatch"
	case retired != nil && out.Identity.Authority == *retired:
		out.Stage = "authority-retired"
	case out.Identity.Project == "":
		out.Stage = "window-missing"
	default:
		out.Stage = "control-pending"
	}
	return out
}

func observeInstalledRecovery(ctx context.Context, uid, generation string, retired *coremetadata.CodexAuthorityRef, socket string) installedRecoveryObservation {
	registry, err := snapshotResourceRegistry()
	if err != nil {
		return installedRecoveryObservation{Stage: "registry-read", Error: recoveryError(err), Identity: installedRecoveryAgent{Agent: uid}}
	}
	out := recoveryRegistryObservation(registry, uid, generation, retired)
	if out.Identity.Pane != "" {
		runner := explicitTmuxRunner{runner: inttmux.ExecRunner{}, target: tmuxTransport{Kind: tmuxSocketPath, Value: socket, Source: tmuxSocketPathSource}}
		lookup := &tmuxAgentControlBindingLookup{lookup: intmetadata.NewMirror(runner), runner: runner}
		live, found, err := lookup.Live(ctx, out.Identity.Pane)
		out.Live = &installedRecoveryLive{Error: recoveryError(err)}
		if found && err == nil {
			out.Live = &installedRecoveryLive{Runtime: live.RuntimeID, Pane: live.PaneUID, Thread: live.ThreadID, Authority: recoveryToken(live.Authority, codexAuthorityControlPlane, codexAuthorityHook, codexAuthorityPending, "invalidating"), Epoch: recoveryEpoch(live.Epoch), Reason: string(codexObserverReasonFor(live.Reason))}
			if live.Reason != "" && out.Live.Reason == "" {
				out.Live.Reason = "unclassified"
			}
		}
	}
	if out.Stage != "control-pending" {
		return out
	}
	control := newAgentCommand()
	control.controlTimeout = 2 * time.Second
	out = readInstalledRecoveryControl(control, out, retired != nil)
	if out.Stage == "ready" {
		latest, err := snapshotResourceRegistry()
		confirmed := recoveryRegistryObservation(latest, uid, generation, retired)
		if err != nil || confirmed.Stage != "control-pending" || confirmed.Identity != out.IdentityWithoutControlEpoch() {
			out.Stage = "authority-changed-during-control"
			out.Error = recoveryError(err)
		}
	}
	return out
}

type installedRecoveryControlReader interface {
	resolveControlBinding(string, string) (exactAgentControlBinding, error)
	callControl(exactAgentControlBinding, agentControlRequest) (agentControlResponse, error)
}

func readInstalledRecoveryControl(control installedRecoveryControlReader, out installedRecoveryObservation, requireStart bool) installedRecoveryObservation {
	// The resolver interprets this spelling as a provider capability action.
	// Prove the same route as the later installed turn command, then issue only
	// a read-only status request; readiness itself never submits an input.
	binding, err := control.resolveControlBinding("agent turn start", "uid:"+out.Identity.Agent)
	if err != nil {
		out.Stage = "control-binding"
		out.Error = recoveryError(err)
		return out
	}
	response, err := control.callControl(binding, agentControlRequest{Operation: agentControlOpStatus})
	return decideInstalledRecoveryControl(out, binding, response, err, requireStart)
}

func (out installedRecoveryObservation) IdentityWithoutControlEpoch() installedRecoveryAgent {
	identity := out.Identity
	identity.ControlEpoch = ""
	return identity
}

func decideInstalledRecoveryControl(out installedRecoveryObservation, binding exactAgentControlBinding, response agentControlResponse, err error, requireStart bool) installedRecoveryObservation {
	if err != nil {
		out.Stage = "control-status-transport"
		out.Error = recoveryError(err)
		return out
	}
	out.Control = &installedRecoveryControl{OK: response.OK, Code: recoveryToken(response.Code, "stale-epoch", "stale-binding", "unavailable", "timeout", "stale-turn", "turn-state-unavailable", "turn-in-progress"), Availability: response.Availability}
	switch {
	case binding.Identity.AgentUID != out.Identity.Agent || binding.Identity.PaneUID != out.Identity.Pane || binding.Identity.RuntimeID != out.Identity.Runtime || binding.Identity.Generation != out.Identity.Activation || binding.Identity.ThreadID != out.Identity.Thread || binding.Endpoint != out.Identity.Authority.Endpoint():
		out.Stage = "control-identity-mismatch"
	case !response.OK:
		out.Stage = "control-status-refused"
	case requireStart && !response.Availability.Start:
		out.Stage = "control-not-startable"
	default:
		out.Stage = "ready"
		out.Identity.ControlEpoch = binding.Epoch
	}
	return out
}

func (attempt *installedRecoveryAttempt) observe(out installedRecoveryObservation) bool {
	// Keep the first and up to 31 changes; the last slot always retains the
	// newest observation, including the terminal one. No unbounded poll ledger.
	if n := len(attempt.Observations); n > 0 {
		prior := attempt.Observations[n-1]
		prior.At = ""
		out.At = ""
		previous, _ := json.Marshal(prior)
		current, _ := json.Marshal(out)
		if string(previous) == string(current) {
			return false
		}
		if n == 32 {
			out.At = time.Now().UTC().Format(time.RFC3339Nano)
			attempt.Observations[n-1] = out
			return true
		}
	}
	out.At = time.Now().UTC().Format(time.RFC3339Nano)
	attempt.Observations = append(attempt.Observations, out)
	return true
}

func captureInstalledRecoveryFailure(ctx context.Context, fixture *codexinstalled.Fixture, socket string, identity installedRecoveryAgent) *installedRecoveryFailure {
	out := &installedRecoveryFailure{}
	if processes, err := fixture.OwnedProcesses(); err != nil {
		out.ProcessError = recoveryError(err)
	} else {
		out.Processes = processes
	}
	if identity.Runtime != "" && exactTmuxHandle(identity.Runtime, "%") != "" {
		runner := explicitTmuxRunner{runner: inttmux.ExecRunner{}, target: tmuxTransport{Kind: tmuxSocketPath, Value: socket, Source: tmuxSocketPathSource}}
		raw, err := runner.Run(ctx, "tmux", "display-message", "-p", "-t", identity.Runtime, tmuxRowFormat("#{@projmux_pane_uid}", "#{pane_pid}", "#{pane_dead}", "#{pane_dead_status}"))
		out.PaneError = recoveryError(err)
		fields := strings.Split(strings.TrimSpace(string(raw)), tmuxRowSepFormat)
		if len(fields) == 4 && fields[0] == identity.Pane {
			out.PanePID, _ = strconv.Atoi(fields[1])
			out.PaneDead = fields[2] == "1"
			if code, err := strconv.Atoi(fields[3]); err == nil {
				out.PaneExit = &code
			}
		} else if err == nil {
			out.PaneError = "exact-pane-frame-unavailable"
		}
	}
	if paths, err := config.DefaultPathsFromEnv(); err == nil {
		entries, available := readAIIngestLogTail(filepath.Join(paths.StateDir, aiIngestLogName), 64<<10, 128)
		out.JournalAvailable = available
		out.Observer = recoveryObserverTransitions(entries, identity)
	}
	return out
}

func recoveryObserverTransitions(entries []aiIngestLogEntry, identity installedRecoveryAgent) []installedRecoveryTransition {
	var out []installedRecoveryTransition
	for _, entry := range entries {
		if identity.Thread == "" || entry.Source != aiIngestCodexObserverSource || entry.ThreadID != identity.Thread || entry.Pane != identity.Runtime || !codexObserverTransitionValid(codexObserverTransition(entry.Event)) {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, entry.At)
		if err != nil {
			continue
		}
		reason := codexObserverReasonFor(string(entry.Reason))
		if reason == "" {
			reason = codexObserverReasonUnrecorded
		}
		out = append(out, installedRecoveryTransition{At: at.UTC().Format(time.RFC3339Nano), Event: entry.Event, Reason: string(reason), Epoch: recoveryEpoch(entry.Epoch), Repeat: entry.Repeat})
	}
	return out
}
