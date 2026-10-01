package diagnostics

import (
	"fmt"
	"time"
)

// codexBrokerRefusalEvent is the record of one typed refusal a projmux process
// received from the Codex endpoint broker: a refused request, bind, or dial.
// The broker process itself never writes it; the record belongs to the side
// that received the refusal.
const codexBrokerRefusalEvent = "codex.broker.refusal"

// codexBrokerComponent is the journal component the refusal record alone uses.
const codexBrokerComponent = "codex-broker"

// CodexBrokerRole is the closed name of the process that received a refusal,
// carried in the `source` field.
type CodexBrokerRole string

const (
	// CodexBrokerRoleObserver is a managed Codex lifecycle observer, the
	// broker client that carries one Agent's binding and its control plane.
	CodexBrokerRoleObserver CodexBrokerRole = "observer"
	// CodexBrokerRoleProbe is the operator-run `internal codex-broker probe`.
	CodexBrokerRoleProbe CodexBrokerRole = "probe"
)

// CodexBrokerOperation is the closed name of the broker operation that was
// refused, carried in the `operation` field.
type CodexBrokerOperation string

const (
	// CodexBrokerOperationEnsure reaches the runtime: discovery, dial,
	// handshake, and, when allowed, starting one.
	CodexBrokerOperationEnsure CodexBrokerOperation = "ensure"
	// CodexBrokerOperationBind opens one exact-thread binding.
	CodexBrokerOperationBind CodexBrokerOperation = "bind"
	// CodexBrokerOperationLifecycleRead is the fresh exact turn state read a
	// control write makes first.
	CodexBrokerOperationLifecycleRead  CodexBrokerOperation = "lifecycle-read"
	CodexBrokerOperationTurnStart      CodexBrokerOperation = "turn-start"
	CodexBrokerOperationTurnSteer      CodexBrokerOperation = "turn-steer"
	CodexBrokerOperationTurnInterrupt  CodexBrokerOperation = "turn-interrupt"
	CodexBrokerOperationApprovalAnswer CodexBrokerOperation = "approval-answer"
)

// codexBrokerOperationRoles is the one authority for which role may record
// which operation. The probe only reaches and binds; every control write is
// the observer's.
var codexBrokerOperationRoles = map[CodexBrokerOperation][]CodexBrokerRole{
	CodexBrokerOperationEnsure:         {CodexBrokerRoleObserver, CodexBrokerRoleProbe},
	CodexBrokerOperationBind:           {CodexBrokerRoleObserver, CodexBrokerRoleProbe},
	CodexBrokerOperationLifecycleRead:  {CodexBrokerRoleObserver},
	CodexBrokerOperationTurnStart:      {CodexBrokerRoleObserver},
	CodexBrokerOperationTurnSteer:      {CodexBrokerRoleObserver},
	CodexBrokerOperationTurnInterrupt:  {CodexBrokerRoleObserver},
	CodexBrokerOperationApprovalAnswer: {CodexBrokerRoleObserver},
}

// codexBrokerRefusals mirrors every codexbroker.Refusal except `none`. The
// record's `code` is the broker's own token, unprefixed, so the journal and the
// refusal lines the CLI prints spell a reason the same way. A test holds this
// set to the broker package's declared constants.
var codexBrokerRefusals = stringSet(
	"broker-closed", "endpoint-unknown", "endpoint-identity-invalid", "route-mismatch", "broker-runtime-stale",
	"admission-closed", "binding-restore-failed", "broker-restarting", "generation-capacity-exceeded",
	"thread-required", "binding-exists", "binding-closed", "control-not-open", "stale-connection-epoch",
	"stale-binding-epoch", "resync-required", "snapshot-unavailable", "lease-identity-mismatch",
	"response-already-answered", "disconnect-boundary", "payload-too-large", "lifecycle-unsupported",
	"lifecycle-busy", "lifecycle-retry", "lifecycle-protocol", "thread-absent", "thread-not-durable",
	"domain-required", "socket-path-too-long", "discovery-untrusted", "unsupported-platform",
	"host-unavailable", "host-live", "runtime-exists", "runtime-replaced", "host-closed",
	"credential-rejected", "endpoint-mismatch", "protocol-incompatible", "drain-required",
	"frame-invalid", "request-unknown", "endpoint-refused",
)

// codexBrokerDialStages mirrors codexbroker.DialStage. Only a refused ensure
// can carry one.
var codexBrokerDialStages = stringSet("discovery", "dial", "handshake")

// CodexBrokerRefusal is one refusal as the receiving process states it.
// Reason is the broker's Refusal token and DialStage its DialStage, both as
// strings so this package does not depend on the broker's.
type CodexBrokerRefusal struct {
	Role      CodexBrokerRole
	Operation CodexBrokerOperation
	Reason    string
	DialStage string
}

// CodexBrokerRecorder appends `codex.broker.refusal` records under the
// invocation run ID. It owns no top-level outcome and no once: a long-lived
// observer receives many refusals, and each one records itself, including a
// refusal the caller absorbs by retrying. Appends are best-effort and never
// flow back into the broker operation.
type CodexBrokerRecorder struct {
	lifecycle *LifecycleRecorder
}

func (r *LifecycleRecorder) CodexBroker() *CodexBrokerRecorder {
	if r == nil {
		return nil
	}
	return &CodexBrokerRecorder{lifecycle: r}
}

// RecordRefusal appends one record. A role, operation, reason, or stage
// outside its closed set, an operation that role may not record, or a stage on
// anything but an ensure drops the record. The thread id, Agent and Pane, the
// state domain and socket paths, the runtime id, and the wrapped transport
// cause have no field here and are never recorded.
func (r *CodexBrokerRecorder) RecordRefusal(refusal CodexBrokerRefusal) {
	if r == nil || r.lifecycle == nil {
		return
	}
	owner := r.lifecycle
	event := Event{
		At: owner.now().UTC().Format(time.RFC3339Nano), Level: "info", Component: codexBrokerComponent,
		Event: codexBrokerRefusalEvent, Result: "success",
		RunID: owner.runID, Version: owner.version, MuxBackend: owner.muxBackend,
		Source: string(refusal.Role), Operation: string(refusal.Operation), Code: refusal.Reason, DialStage: refusal.DialStage,
	}
	if validateCodexBrokerRefusalEvent(event) != nil {
		return
	}
	owner.writeMu.Lock()
	defer owner.writeMu.Unlock()
	owner.append(event)
}

// validateCodexBrokerRefusalEvent admits exactly the shape RecordRefusal
// writes. The record is info/success, like a teardown refusal decision: it
// states a refusal that was received, not a failure of the command, which keeps
// its own outcome record.
func validateCodexBrokerRefusalEvent(event Event) error {
	rest := event
	rest.At, rest.Level, rest.Component, rest.Event, rest.Result = "", "", "", "", ""
	rest.DurationMS, rest.RunID, rest.Version, rest.MuxBackend = 0, "", "", ""
	rest.Source, rest.Operation, rest.Code, rest.DialStage = "", "", "", ""
	if rest != (Event{}) || event.Component != codexBrokerComponent || event.Level != "info" || event.Result != "success" {
		return fmt.Errorf("invalid codex broker refusal shape")
	}
	roles, ok := codexBrokerOperationRoles[CodexBrokerOperation(event.Operation)]
	if !ok {
		return fmt.Errorf("invalid codex broker refusal operation")
	}
	allowed := false
	for _, role := range roles {
		allowed = allowed || string(role) == event.Source
	}
	if !allowed {
		return fmt.Errorf("invalid codex broker refusal role")
	}
	if _, ok := codexBrokerRefusals[event.Code]; !ok {
		return fmt.Errorf("invalid codex broker refusal reason")
	}
	if event.DialStage != "" {
		if _, ok := codexBrokerDialStages[event.DialStage]; !ok || event.Operation != string(CodexBrokerOperationEnsure) {
			return fmt.Errorf("invalid codex broker refusal dial stage")
		}
	}
	return nil
}
