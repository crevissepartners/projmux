package app_test

import (
	"fmt"
	"sort"
	"strings"
)

type lifecycleOperationKind uint8

const (
	lifecycleSnapshot lifecycleOperationKind = iota
	lifecycleProviderEvent
	lifecycleDuplicate
	lifecycleAllowedReorder
	lifecycleInvalidation
	lifecycleEpochReplace
)

type lifecycleAuthority uint8

const (
	lifecycleCurrent lifecycleAuthority = iota
	lifecycleStale
	lifecycleFuture
	lifecycleForeign
)

type lifecycleOperation struct {
	kind       lifecycleOperationKind
	authority  lifecycleAuthority
	snapshot   lifecycleModelSnapshot
	event      lifecycleModelEvent
	reorderKey byte
}

type lifecycleModelSnapshot struct {
	threadID    string
	threadState string
	turnID      string
	turnState   string
}

type lifecycleModelEvent struct {
	kind         string
	threadID     string
	turnID       string
	itemID       string
	requestID    string
	threadState  string
	turnState    string
	approvalKind string
}

type lifecycleConcreteKind uint8

const (
	lifecycleConcreteSnapshot lifecycleConcreteKind = iota
	lifecycleConcreteEvent
	lifecycleConcreteInvalidation
)

type lifecycleConcreteOperation struct {
	kind     lifecycleConcreteKind
	epoch    uint64
	snapshot lifecycleModelSnapshot
	event    lifecycleModelEvent
}

type lifecycleModelPending struct {
	turnID       string
	itemID       string
	requestID    string
	approvalKind string
	notified     bool
}

type lifecycleModelState struct {
	epoch            uint64
	active           bool
	threadID         string
	threadState      string
	currentTurnID    string
	currentTurnState string
	interaction      string
	pending          map[string]lifecycleModelPending
	terminalTurns    map[string]string
}

type lifecycleModelResult struct {
	accepted    bool
	invalidated bool
}

var lifecycleThreadStates = [...]string{
	"unknown", "not-loaded", "idle", "active", "waiting-on-approval", "waiting-on-user-input", "system-error",
}

var lifecycleTurnStates = [...]string{"unknown", "in-progress", "completed", "failed", "interrupted"}

func decodeLifecycleOperations(data []byte) []lifecycleOperation {
	operations := make([]lifecycleOperation, 0, (len(data)+2)/3)
	for offset := 0; offset < len(data); offset += 3 {
		opcode := data[offset]
		var payload, authority byte
		if offset+1 < len(data) {
			payload = data[offset+1]
		}
		if offset+2 < len(data) {
			authority = data[offset+2]
		}
		operations = append(operations, decodeLifecycleOperation(opcode, payload, authority))
	}
	return operations
}

func decodeLifecycleOperation(opcode, payload, authority byte) lifecycleOperation {
	op := lifecycleOperation{
		kind:       lifecycleOperationKind(opcode % 6),
		authority:  lifecycleAuthority(authority % 4),
		reorderKey: payload,
	}
	turnIDs := [...]string{"turn-1", "turn-2", ""}
	requestIDs := [...]string{"request-1", "request-2", "request-missing"}
	itemIDs := [...]string{"item-1", "item-2", ""}
	op.snapshot = lifecycleModelSnapshot{
		threadID:    "thread-1",
		threadState: lifecycleThreadStates[payload%byte(len(lifecycleThreadStates))],
		turnID:      turnIDs[(payload/7)%byte(len(turnIDs))],
		turnState:   lifecycleTurnStates[(payload/21)%byte(len(lifecycleTurnStates))],
	}
	if op.authority == lifecycleForeign {
		op.snapshot.threadID = "thread-foreign"
	}
	if op.kind == lifecycleSnapshot && op.authority != lifecycleForeign {
		op.authority = lifecycleCurrent
	}
	if op.kind == lifecycleEpochReplace {
		op.authority = lifecycleCurrent
		op.snapshot.threadID = "thread-1"
	}
	variant := payload / 5
	op.event = lifecycleModelEvent{
		threadID:     "thread-1",
		turnID:       turnIDs[variant%byte(len(turnIDs))],
		itemID:       itemIDs[(variant/3)%byte(len(itemIDs))],
		requestID:    requestIDs[(variant/9)%byte(len(requestIDs))],
		threadState:  lifecycleThreadStates[variant%byte(len(lifecycleThreadStates))],
		turnState:    lifecycleTurnStates[variant%byte(len(lifecycleTurnStates))],
		approvalKind: [...]string{"command", "file-change", "permissions"}[(variant/5)%3],
	}
	if op.authority == lifecycleForeign {
		op.event.threadID = "thread-foreign"
	}
	switch payload % 5 {
	case 0:
		op.event.kind = "turn-started"
	case 1:
		op.event.kind = "thread-status"
	case 2:
		op.event.kind = "approval-pending"
	case 3:
		op.event.kind = "request-resolved"
	case 4:
		op.event.kind = "turn-completed"
	}
	return op
}

func (m *lifecycleModelState) apply(operation lifecycleConcreteOperation) lifecycleModelResult {
	switch operation.kind {
	case lifecycleConcreteSnapshot:
		return m.begin(operation.epoch, operation.snapshot)
	case lifecycleConcreteEvent:
		return m.applyEvent(operation.epoch, operation.event)
	case lifecycleConcreteInvalidation:
		return m.invalidate(operation.epoch)
	default:
		return lifecycleModelResult{}
	}
}

func (m *lifecycleModelState) begin(epoch uint64, snapshot lifecycleModelSnapshot) lifecycleModelResult {
	if epoch == 0 || strings.TrimSpace(snapshot.threadID) != "thread-1" {
		return lifecycleModelResult{}
	}
	m.epoch = epoch
	m.active = true
	m.threadID = "thread-1"
	m.threadState = snapshot.threadState
	m.currentTurnID = strings.TrimSpace(snapshot.turnID)
	m.currentTurnState = snapshot.turnState
	m.pending = map[string]lifecycleModelPending{}
	m.terminalTurns = map[string]string{}
	if snapshot.threadState == "not-loaded" {
		return m.invalidate(epoch)
	}
	if m.currentTurnID != "" && terminalTurnState(snapshot.turnState) {
		m.terminalTurns[m.currentTurnID] = snapshot.turnState
	}
	m.interaction = m.snapshotInteraction()
	return lifecycleModelResult{accepted: true}
}

func (m *lifecycleModelState) invalidate(epoch uint64) lifecycleModelResult {
	if !m.active || epoch != m.epoch {
		return lifecycleModelResult{}
	}
	m.active = false
	m.pending = nil
	m.currentTurnID = ""
	m.currentTurnState = "unknown"
	m.interaction = "unknown"
	return lifecycleModelResult{accepted: true, invalidated: true}
}

func (m *lifecycleModelState) applyEvent(epoch uint64, event lifecycleModelEvent) lifecycleModelResult {
	if !m.active || epoch != m.epoch || strings.TrimSpace(event.threadID) != strings.TrimSpace(m.threadID) {
		return lifecycleModelResult{}
	}
	result := lifecycleModelResult{accepted: true}
	switch event.kind {
	case "turn-started":
		if event.turnID == "" {
			return lifecycleModelResult{}
		}
		m.pending = map[string]lifecycleModelPending{}
		m.terminalTurns = map[string]string{}
		m.currentTurnID = event.turnID
		m.currentTurnState = "in-progress"
		m.threadState = "active"
		m.interaction = "in_progress"
	case "thread-status":
		if event.threadState == "not-loaded" {
			return m.invalidate(epoch)
		}
		if m.threadState == "waiting-on-approval" && event.threadState != "waiting-on-approval" {
			for requestID, pending := range m.pending {
				pending.notified = false
				m.pending[requestID] = pending
			}
		}
		m.threadState = event.threadState
		m.interaction = m.liveInteraction()
		if m.interaction == "approval_required" {
			m.markActionableApprovalsNotified()
		}
	case "approval-pending":
		if event.turnID == "" || event.itemID == "" || event.requestID == "" || event.turnID != m.currentTurnID {
			return lifecycleModelResult{}
		}
		if m.pending == nil {
			m.pending = map[string]lifecycleModelPending{}
		}
		if existing, ok := m.pending[event.requestID]; ok {
			if existing.turnID != event.turnID || existing.itemID != event.itemID {
				return lifecycleModelResult{}
			}
		} else {
			m.pending[event.requestID] = lifecycleModelPending{
				turnID: event.turnID, itemID: event.itemID, requestID: event.requestID, approvalKind: event.approvalKind,
			}
		}
		m.interaction = m.liveInteraction()
		if m.interaction == "approval_required" {
			m.markActionableApprovalsNotified()
		}
	case "request-resolved":
		pending, ok := m.pending[event.requestID]
		if !ok || pending.turnID != m.currentTurnID {
			return lifecycleModelResult{}
		}
		delete(m.pending, event.requestID)
		m.interaction = m.liveInteraction()
	case "turn-completed":
		if event.turnID == "" || event.turnID != m.currentTurnID {
			return lifecycleModelResult{}
		}
		if _, duplicate := m.terminalTurns[event.turnID]; duplicate || !terminalTurnState(event.turnState) {
			return lifecycleModelResult{}
		}
		m.pending = map[string]lifecycleModelPending{}
		m.currentTurnState = event.turnState
		m.terminalTurns[event.turnID] = event.turnState
		if event.turnState == "completed" {
			m.interaction = "response_complete"
		} else {
			m.interaction = "idle"
		}
	default:
		return lifecycleModelResult{}
	}
	return result
}

func (m *lifecycleModelState) snapshotInteraction() string {
	if m.currentTurnState == "completed" {
		return "response_complete"
	}
	if m.currentTurnState == "failed" || m.currentTurnState == "interrupted" {
		return "idle"
	}
	return m.liveInteraction()
}

func (m *lifecycleModelState) liveInteraction() string {
	if m.currentTurnState == "completed" {
		return "response_complete"
	}
	switch m.threadState {
	case "idle", "system-error":
		return "idle"
	case "waiting-on-user-input":
		return "input_required"
	case "waiting-on-approval":
		for _, pending := range m.pending {
			if pending.turnID == m.currentTurnID && pending.itemID != "" && pending.requestID != "" {
				return "approval_required"
			}
		}
		return "in_progress"
	case "active":
		return "in_progress"
	default:
		return "unknown"
	}
}

func (m *lifecycleModelState) markActionableApprovalsNotified() {
	for requestID, pending := range m.pending {
		if pending.turnID == m.currentTurnID && !pending.notified {
			pending.notified = true
			m.pending[requestID] = pending
		}
	}
}

func (m lifecycleModelState) sortedPending() []lifecycleModelPending {
	pending := make([]lifecycleModelPending, 0, len(m.pending))
	for _, request := range m.pending {
		pending = append(pending, request)
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].requestID < pending[j].requestID })
	return pending
}

func terminalTurnState(state string) bool {
	return state == "completed" || state == "failed" || state == "interrupted"
}

func (o lifecycleOperation) String() string {
	switch o.kind {
	case lifecycleSnapshot:
		return fmt.Sprintf("snapshot[%s](thread=%s,turn=%s/%s)", o.authority, o.snapshot.threadState, o.snapshot.turnID, o.snapshot.turnState)
	case lifecycleProviderEvent:
		return fmt.Sprintf("event[%s](%s,thread_id=%s,turn=%s,item=%s,request=%s,thread_state=%s,turn_state=%s)", o.authority, o.event.kind, o.event.threadID, o.event.turnID, o.event.itemID, o.event.requestID, o.event.threadState, o.event.turnState)
	case lifecycleDuplicate:
		return "duplicate(last-provider-event)"
	case lifecycleAllowedReorder:
		return fmt.Sprintf("allowed-reorder(pending-%d-a,pending-%d-b)", o.reorderKey, o.reorderKey)
	case lifecycleInvalidation:
		return fmt.Sprintf("invalidation[%s]", o.authority)
	case lifecycleEpochReplace:
		return fmt.Sprintf("epoch-replace(thread=%s,turn=%s/%s)", o.snapshot.threadState, o.snapshot.turnID, o.snapshot.turnState)
	default:
		return fmt.Sprintf("operation(%d)", o.kind)
	}
}

func (a lifecycleAuthority) String() string {
	switch a {
	case lifecycleCurrent:
		return "current"
	case lifecycleStale:
		return "stale"
	case lifecycleFuture:
		return "future"
	case lifecycleForeign:
		return "foreign"
	default:
		return fmt.Sprintf("authority(%d)", a)
	}
}

func formatLifecycleTrace(operations []lifecycleOperation) string {
	parts := make([]string, len(operations))
	for index, operation := range operations {
		parts[index] = operation.String()
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
