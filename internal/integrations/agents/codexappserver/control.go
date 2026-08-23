package codexappserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

type ControlResult struct {
	ThreadID string
	TurnID   string
}

// StartExactTurn is intentionally separate from the create-time StartTurn.
// Its serialized params contain exactly threadId and one text input: no
// client message id and no sticky model/effort/cwd/sandbox/permission fields.
func (c *Client) StartExactTurn(ctx context.Context, threadID, text string) (ControlResult, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" || strings.TrimSpace(text) == "" {
		return ControlResult{}, fmt.Errorf("%w: turn/start requires exact thread and text", ErrProtocol)
	}
	var result turnStartResult
	if err := c.Request(ctx, methodTurnStart, exactTurnStartParams{ThreadID: threadID, Input: []wireUserInput{{Type: "text", Text: text}}}, &result); err != nil {
		return ControlResult{}, err
	}
	turnID := strings.TrimSpace(result.Turn.ID)
	if turnID == "" {
		return ControlResult{}, fmt.Errorf("%w: turn/start returned no turn id", ErrProtocol)
	}
	return ControlResult{ThreadID: threadID, TurnID: turnID}, nil
}

func (c *Client) SteerExactTurn(ctx context.Context, threadID, expectedTurnID, text string) (ControlResult, error) {
	threadID, expectedTurnID = strings.TrimSpace(threadID), strings.TrimSpace(expectedTurnID)
	if threadID == "" || expectedTurnID == "" || strings.TrimSpace(text) == "" {
		return ControlResult{}, fmt.Errorf("%w: turn/steer requires exact thread, expected turn, and text", ErrProtocol)
	}
	if err := c.Request(ctx, methodTurnSteer, turnSteerParams{
		ThreadID: threadID, ExpectedTurnID: expectedTurnID, Input: []wireUserInput{{Type: "text", Text: text}},
	}, nil); err != nil {
		return ControlResult{}, err
	}
	return ControlResult{ThreadID: threadID, TurnID: expectedTurnID}, nil
}

func (c *Client) InterruptExactTurn(ctx context.Context, threadID, turnID string) (ControlResult, error) {
	threadID, turnID = strings.TrimSpace(threadID), strings.TrimSpace(turnID)
	if threadID == "" || turnID == "" {
		return ControlResult{}, fmt.Errorf("%w: turn/interrupt requires exact thread and turn", ErrProtocol)
	}
	if err := c.Request(ctx, methodTurnInterrupt, turnInterruptParams{ThreadID: threadID, TurnID: turnID}, nil); err != nil {
		return ControlResult{}, err
	}
	return ControlResult{ThreadID: threadID, TurnID: turnID}, nil
}

type ApprovalDecision string

const (
	DecisionAccept    ApprovalDecision = "accept"
	DecisionDecline   ApprovalDecision = "decline"
	DecisionCancel    ApprovalDecision = "cancel"
	DecisionGrantTurn ApprovalDecision = "grant-turn"
)

type ApprovalEnvelope struct {
	RawRequestID    json.RawMessage    `json:"-"`
	RequestID       string             `json:"requestId"`
	Kind            ApprovalKind       `json:"kind"`
	ThreadID        string             `json:"threadId"`
	TurnID          string             `json:"turnId"`
	ItemID          string             `json:"itemId"`
	ApprovalID      *string            `json:"approvalId,omitempty"`
	Command         string             `json:"command,omitempty"`
	CWD             string             `json:"cwd,omitempty"`
	NetworkHost     string             `json:"networkHost,omitempty"`
	NetworkProtocol string             `json:"networkProtocol,omitempty"`
	RequestCWD      string             `json:"requestCwd,omitempty"`
	Reason          string             `json:"reason,omitempty"`
	GrantRoot       *string            `json:"grantRoot,omitempty"`
	Permissions     json.RawMessage    `json:"permissions,omitempty"`
	Decisions       []ApprovalDecision `json:"decisions"`
}

func (e ApprovalEnvelope) RawIDKey() string {
	return string(e.RawRequestID)
}

func DecodeApprovalEnvelope(notification Notification) (ApprovalEnvelope, bool, error) {
	kind := ApprovalKind("")
	switch strings.TrimSpace(notification.Method) {
	case "item/commandExecution/requestApproval":
		kind = ApprovalCommand
	case "item/fileChange/requestApproval":
		kind = ApprovalFileChange
	case "item/permissions/requestApproval":
		kind = ApprovalPermissions
	default:
		return ApprovalEnvelope{}, false, nil
	}
	if len(notification.RawRequestID) == 0 {
		return ApprovalEnvelope{}, true, fmt.Errorf("%w: approval request lost raw id", ErrProtocol)
	}
	normalized, err := normalizeServerRequestID(notification.RawRequestID)
	if err != nil || normalized != notification.RequestID {
		return ApprovalEnvelope{}, true, fmt.Errorf("%w: approval request id mismatch", ErrProtocol)
	}
	var common struct {
		ThreadID    string `json:"threadId"`
		TurnID      string `json:"turnId"`
		ItemID      string `json:"itemId"`
		StartedAtMs *int64 `json:"startedAtMs"`
	}
	if err := decodeStrictJSON(notification.Params, &common); err != nil {
		return ApprovalEnvelope{}, true, err
	}
	e := ApprovalEnvelope{RawRequestID: append(json.RawMessage(nil), notification.RawRequestID...), RequestID: normalized, Kind: kind,
		ThreadID: strings.TrimSpace(common.ThreadID), TurnID: strings.TrimSpace(common.TurnID), ItemID: strings.TrimSpace(common.ItemID)}
	if e.ThreadID == "" || e.TurnID == "" || e.ItemID == "" || common.StartedAtMs == nil {
		return ApprovalEnvelope{}, true, fmt.Errorf("%w: incomplete approval identity", ErrProtocol)
	}
	switch kind {
	case ApprovalCommand:
		var p struct {
			Command    *string           `json:"command"`
			CWD        *string           `json:"cwd"`
			ApprovalID *string           `json:"approvalId"`
			Available  []json.RawMessage `json:"availableDecisions"`
			Additional json.RawMessage   `json:"additionalPermissions"`
			Network    *struct {
				Host     string `json:"host"`
				Protocol string `json:"protocol"`
			} `json:"networkApprovalContext"`
			Reason *string `json:"reason"`
		}
		if err := json.Unmarshal(notification.Params, &p); err != nil {
			return ApprovalEnvelope{}, true, fmt.Errorf("%w: malformed command approval", ErrProtocol)
		}
		e.ApprovalID = cloneApprovalStringPointer(p.ApprovalID)
		if e.ApprovalID != nil && strings.TrimSpace(*e.ApprovalID) == "" {
			return ApprovalEnvelope{}, true, fmt.Errorf("%w: empty approval id", ErrProtocol)
		}
		if p.Command != nil {
			e.Command = *p.Command
		}
		if p.CWD != nil {
			e.CWD = *p.CWD
		}
		if p.Reason != nil {
			e.Reason = *p.Reason
		}
		if p.Network != nil {
			e.NetworkHost, e.NetworkProtocol = p.Network.Host, p.Network.Protocol
		}
		e.Decisions = safeCommandDecisions(p.Available, len(p.Additional) == 0 || bytes.Equal(bytes.TrimSpace(p.Additional), []byte("null")), e)
	case ApprovalFileChange:
		var p struct {
			GrantRoot *string `json:"grantRoot"`
			Reason    *string `json:"reason"`
		}
		if err := json.Unmarshal(notification.Params, &p); err != nil {
			return ApprovalEnvelope{}, true, fmt.Errorf("%w: malformed file approval", ErrProtocol)
		}
		e.GrantRoot = cloneApprovalStringPointer(p.GrantRoot)
		if p.Reason != nil {
			e.Reason = *p.Reason
		}
		e.Decisions = []ApprovalDecision{DecisionDecline, DecisionCancel}
		if p.GrantRoot == nil {
			e.Decisions = append([]ApprovalDecision{DecisionAccept}, e.Decisions...)
		}
	case ApprovalPermissions:
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(notification.Params, &raw); err != nil {
			return ApprovalEnvelope{}, true, fmt.Errorf("%w: malformed permissions approval", ErrProtocol)
		}
		permissions, ok := raw["permissions"]
		if !ok || !supportedPermissionProfile(permissions) {
			return e, true, nil
		}
		var cwd string
		if rawCWD, ok := raw["cwd"]; !ok || json.Unmarshal(rawCWD, &cwd) != nil || strings.TrimSpace(cwd) == "" {
			return e, true, nil
		}
		e.RequestCWD = cwd
		if rawReason, ok := raw["reason"]; ok && !bytes.Equal(bytes.TrimSpace(rawReason), []byte("null")) {
			_ = json.Unmarshal(rawReason, &e.Reason)
		}
		e.Permissions = append(json.RawMessage(nil), permissions...)
		e.Decisions = []ApprovalDecision{DecisionGrantTurn}
	}
	return e, true, nil
}

func safeCommandDecisions(raw []json.RawMessage, additionalSafe bool, envelope ApprovalEnvelope) []ApprovalDecision {
	allowed := []ApprovalDecision{DecisionAccept, DecisionDecline, DecisionCancel}
	if raw != nil {
		allowed = allowed[:0]
		for _, item := range raw {
			var value string
			if json.Unmarshal(item, &value) == nil {
				decision := ApprovalDecision(value)
				if slices.Contains([]ApprovalDecision{DecisionAccept, DecisionDecline, DecisionCancel}, decision) && !slices.Contains(allowed, decision) {
					allowed = append(allowed, decision)
				}
			}
		}
	}
	networkSafe := envelope.NetworkHost == "" && envelope.NetworkProtocol == ""
	if envelope.NetworkHost != "" {
		networkSafe = slices.Contains([]string{"http", "https", "socks5Tcp", "socks5Udp"}, envelope.NetworkProtocol)
	}
	if !additionalSafe || strings.TrimSpace(envelope.Command) == "" || !networkSafe {
		allowed = slices.DeleteFunc(allowed, func(d ApprovalDecision) bool { return d == DecisionAccept })
	}
	return allowed
}

func ApprovalResponse(envelope ApprovalEnvelope, decision ApprovalDecision) (any, error) {
	if !slices.Contains(envelope.Decisions, decision) {
		return nil, fmt.Errorf("%w: decision is not safe for this request", ErrProtocol)
	}
	switch envelope.Kind {
	case ApprovalCommand, ApprovalFileChange:
		return struct {
			Decision ApprovalDecision `json:"decision"`
		}{Decision: decision}, nil
	case ApprovalPermissions:
		if decision != DecisionGrantTurn || !supportedPermissionProfile(envelope.Permissions) {
			return nil, fmt.Errorf("%w: permission grant is not exact", ErrProtocol)
		}
		return struct {
			Permissions      json.RawMessage `json:"permissions"`
			Scope            string          `json:"scope"`
			StrictAutoReview *bool           `json:"strictAutoReview"`
		}{Permissions: append(json.RawMessage(nil), envelope.Permissions...), Scope: "turn", StrictAutoReview: nil}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported approval kind", ErrProtocol)
	}
}

func supportedPermissionProfile(raw json.RawMessage) bool {
	var profile any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&profile) != nil {
		return false
	}
	obj, ok := profile.(map[string]any)
	if !ok || len(obj) == 0 {
		return false
	}
	for key, value := range obj {
		switch key {
		case "network":
			if value == nil {
				continue
			}
			net, ok := value.(map[string]any)
			if !ok {
				return false
			}
			for k, v := range net {
				if k != "enabled" || (v != nil && reflect.TypeOf(v).Kind() != reflect.Bool) {
					return false
				}
			}
		case "fileSystem":
			if !supportedFilePermissions(value) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func supportedFilePermissions(value any) bool {
	if value == nil {
		return true
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key, raw := range obj {
		switch key {
		case "read", "write":
			if raw == nil {
				continue
			}
			values, ok := raw.([]any)
			if !ok {
				return false
			}
			for _, v := range values {
				if _, ok := v.(string); !ok {
					return false
				}
			}
		case "globScanMaxDepth":
			if raw != nil {
				number, ok := raw.(json.Number)
				if !ok {
					return false
				}
				value, err := number.Int64()
				if err != nil || value < 1 {
					return false
				}
			}
		case "entries":
			if raw == nil {
				continue
			}
			values, ok := raw.([]any)
			if !ok {
				return false
			}
			for _, v := range values {
				if !supportedFileEntry(v) {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

func supportedFileEntry(value any) bool {
	entry, ok := value.(map[string]any)
	if !ok || len(entry) != 2 {
		return false
	}
	access, ok := entry["access"].(string)
	if !ok || !slices.Contains([]string{"read", "write", "deny"}, access) {
		return false
	}
	path, ok := entry["path"].(map[string]any)
	if !ok {
		return false
	}
	typeName, ok := path["type"].(string)
	if !ok {
		return false
	}
	switch typeName {
	case "path":
		_, ok = path["path"].(string)
		return ok && len(path) == 2
	case "glob_pattern":
		_, ok = path["pattern"].(string)
		return ok && len(path) == 2
	case "special":
		value, ok := path["value"].(map[string]any)
		if !ok {
			return false
		}
		kind, ok := value["kind"].(string)
		if !ok || !slices.Contains([]string{"root", "minimal", "project_roots", "tmpdir", "slash_tmp"}, kind) {
			return false
		}
		if len(value) == 1 {
			return true
		}
		if kind != "project_roots" || len(value) != 2 {
			return false
		}
		subpath, exists := value["subpath"]
		if !exists || subpath == nil {
			return exists
		}
		_, ok = subpath.(string)
		return ok
	default:
		return false
	}
}

func cloneApprovalStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func decodeStrictJSON(raw json.RawMessage, target any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, target) != nil {
		return fmt.Errorf("%w: malformed approval params", ErrProtocol)
	}
	return nil
}
