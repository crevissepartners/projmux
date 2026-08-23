package codexappserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestExactTurnControlFramesPreserveTextAndSendNoStickyOverrides(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := NewClient(clientConn)
	t.Cleanup(func() { _ = client.Close(); _ = serverConn.Close() })
	frames := make(chan map[string]any, 3)
	go func() {
		reader := bufio.NewReader(serverConn)
		for id := 1; id <= 3; id++ {
			line, _ := reader.ReadBytes('\n')
			var request struct {
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			_ = json.Unmarshal(line, &request)
			request.Params["__method"] = request.Method
			frames <- request.Params
			result := `{}`
			if id == 1 {
				result = `{"turn":{"id":"turn-1","status":"inProgress"}}`
			}
			_, _ = serverConn.Write([]byte(`{"id":` + string(rune('0'+id)) + `,"result":` + result + `}` + "\n"))
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	text := "  preserve exact whitespace  "
	if _, err := client.StartExactTurn(ctx, "thread-1", text); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SteerExactTurn(ctx, "thread-1", "turn-1", text); err != nil {
		t.Fatal(err)
	}
	if _, err := client.InterruptExactTurn(ctx, "thread-1", "turn-1"); err != nil {
		t.Fatal(err)
	}
	start, steer, interrupt := <-frames, <-frames, <-frames
	if got := sortedMapKeys(start); !reflect.DeepEqual(got, []string{"__method", "input", "threadId"}) {
		t.Fatalf("turn/start keys = %v", got)
	}
	if got := start["input"].([]any)[0].(map[string]any)["text"]; got != text {
		t.Fatalf("turn/start text = %q", got)
	}
	if got := sortedMapKeys(steer); !reflect.DeepEqual(got, []string{"__method", "expectedTurnId", "input", "threadId"}) {
		t.Fatalf("turn/steer keys = %v", got)
	}
	if got := steer["input"].([]any)[0].(map[string]any)["text"]; got != text {
		t.Fatalf("turn/steer text = %q", got)
	}
	if got := sortedMapKeys(interrupt); !reflect.DeepEqual(got, []string{"__method", "threadId", "turnId"}) {
		t.Fatalf("turn/interrupt keys = %v", got)
	}
}

func TestServerRequestResponsePreservesRawScalarIDAndRejectsNonInt64(t *testing.T) {
	for _, raw := range []string{`7`, `"7"`} {
		t.Run(raw, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			client := NewClient(clientConn)
			defer client.Close()
			defer serverConn.Close()
			line := make(chan []byte, 1)
			go func() { got, _ := bufio.NewReader(serverConn).ReadBytes('\n'); line <- got }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := client.RespondServerRequest(ctx, json.RawMessage(raw), struct {
				Decision string `json:"decision"`
			}{"accept"}); err != nil {
				t.Fatal(err)
			}
			var response struct {
				ID     json.RawMessage   `json:"id"`
				Result map[string]string `json:"result"`
			}
			if err := json.Unmarshal(<-line, &response); err != nil {
				t.Fatal(err)
			}
			if string(response.ID) != raw || response.Result["decision"] != "accept" {
				t.Fatalf("response = id:%s result:%v", response.ID, response.Result)
			}
		})
	}
	for _, raw := range []string{`null`, `{}`, `[]`, `1.5`, `9223372036854775808`, `7 8`} {
		t.Run("reject-"+raw, func(t *testing.T) {
			clientConn, serverConn := net.Pipe()
			client := NewClient(clientConn)
			defer client.Close()
			defer serverConn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if err := client.RespondServerRequest(ctx, json.RawMessage(raw), struct{}{}); err == nil {
				t.Fatalf("RespondServerRequest(%s) succeeded", raw)
			}
		})
	}
}

func TestApprovalDecisionIntersectionAndPermissionTurnEcho(t *testing.T) {
	command := Notification{Method: "item/commandExecution/requestApproval", RequestID: "9", RawRequestID: json.RawMessage(`9`), Params: json.RawMessage(`{
        "threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,
        "command":"curl https://example.test","cwd":"/work","approvalId":"opaque",
        "availableDecisions":["accept","acceptForSession","decline",{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"example.test","action":"allow"}}},"cancel"],
        "networkApprovalContext":{"host":"example.test","protocol":"https"}}
    `)}
	envelope, ok, err := DecodeApprovalEnvelope(command)
	if err != nil || !ok {
		t.Fatalf("DecodeApprovalEnvelope = %#v %v %v", envelope, ok, err)
	}
	if !reflect.DeepEqual(envelope.Decisions, []ApprovalDecision{DecisionAccept, DecisionDecline, DecisionCancel}) {
		t.Fatalf("decisions = %v", envelope.Decisions)
	}

	command.Params = json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"x","approvalId":"","availableDecisions":["accept"]}`)
	if _, _, err := DecodeApprovalEnvelope(command); err == nil {
		t.Fatal("empty approvalId accepted")
	}

	for _, test := range []struct {
		name string
		json string
		want *string
	}{
		{name: "absent", json: `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"x"}`},
		{name: "null", json: `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"x","approvalId":null}`},
		{name: "opaque", json: `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"x","approvalId":" opaque "}`, want: func() *string { value := " opaque "; return &value }()},
	} {
		t.Run("approval-id-"+test.name, func(t *testing.T) {
			command.Params = json.RawMessage(test.json)
			envelope, _, err := DecodeApprovalEnvelope(command)
			if err != nil || !reflect.DeepEqual(envelope.ApprovalID, test.want) {
				t.Fatalf("approvalId = %#v, want %#v, err=%v", envelope.ApprovalID, test.want, err)
			}
		})
	}

	for _, test := range []Notification{
		{Method: command.Method, RequestID: "9", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"x"}`)},
		{Method: command.Method, RequestID: "9", RawRequestID: json.RawMessage(`9`), Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"","startedAtMs":1,"command":"x"}`)},
		{Method: command.Method, RequestID: "9", RawRequestID: json.RawMessage(`9`), Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","command":"x"}`)},
	} {
		if _, _, err := DecodeApprovalEnvelope(test); err == nil {
			t.Fatalf("lost approval identity accepted: %+v", test)
		}
	}
	for _, startedAt := range []string{"", `null`, `"1"`, `1.5`, `9223372036854775808`} {
		field := ""
		if startedAt != "" {
			field = `,"startedAtMs":` + startedAt
		}
		test := Notification{Method: command.Method, RequestID: "9", RawRequestID: json.RawMessage(`9`), Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","command":"x"` + field + `}`)}
		if _, _, err := DecodeApprovalEnvelope(test); err == nil {
			t.Fatalf("invalid startedAtMs %q accepted", startedAt)
		}
	}

	permission := Notification{Method: "item/permissions/requestApproval", RequestID: "p", RawRequestID: json.RawMessage(`"p"`), Params: json.RawMessage(`{
        "threadId":"thread-1","turnId":"turn-1","itemId":"item-p","startedAtMs":1,"cwd":"/work",
		"permissions":{"fileSystem":{"globScanMaxDepth":2,"entries":[{"access":"write","path":{"type":"special","value":{"kind":"project_roots","subpath":null}}}]},"network":{"enabled":true}}}
    `)}
	grant, ok, err := DecodeApprovalEnvelope(permission)
	if err != nil || !ok || !reflect.DeepEqual(grant.Decisions, []ApprovalDecision{DecisionGrantTurn}) || grant.RequestCWD != "/work" {
		t.Fatalf("permission envelope = %#v %v %v", grant, ok, err)
	}
	response, err := ApprovalResponse(grant, DecisionGrantTurn)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(response)
	var got map[string]any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	_ = decoder.Decode(&got)
	if got["scope"] != "turn" || got["strictAutoReview"] != nil {
		t.Fatalf("grant response = %s", payload)
	}
	var wantPermissions, gotPermissions any
	decoder = json.NewDecoder(bytes.NewReader(grant.Permissions))
	decoder.UseNumber()
	_ = decoder.Decode(&wantPermissions)
	gotPermissions = got["permissions"]
	if !reflect.DeepEqual(gotPermissions, wantPermissions) {
		t.Fatalf("permissions widened: got=%v want=%v", gotPermissions, wantPermissions)
	}
}

func TestApprovalRequestKindAndAvailableDecisionSafetyTable(t *testing.T) {
	commandBase := `"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"  echo ok  ","cwd":" /work "`
	for _, test := range []struct {
		name   string
		method string
		params string
		want   []ApprovalDecision
	}{
		{name: "command absent decisions", method: "item/commandExecution/requestApproval", params: `{` + commandBase + `}`, want: []ApprovalDecision{DecisionAccept, DecisionDecline, DecisionCancel}},
		{name: "command null decisions", method: "item/commandExecution/requestApproval", params: `{` + commandBase + `,"availableDecisions":null}`, want: []ApprovalDecision{DecisionAccept, DecisionDecline, DecisionCancel}},
		{name: "command empty decisions", method: "item/commandExecution/requestApproval", params: `{` + commandBase + `,"availableDecisions":[]}`, want: []ApprovalDecision{}},
		{name: "command safe intersection", method: "item/commandExecution/requestApproval", params: `{` + commandBase + `,"availableDecisions":["acceptForSession","decline","acceptWithExecpolicyAmendment","cancel"]}`, want: []ApprovalDecision{DecisionDecline, DecisionCancel}},
		{name: "command additional permissions", method: "item/commandExecution/requestApproval", params: `{` + commandBase + `,"additionalPermissions":{"network":{"enabled":true}},"availableDecisions":["accept","decline","cancel"]}`, want: []ApprovalDecision{DecisionDecline, DecisionCancel}},
		{name: "network exact context", method: "item/commandExecution/requestApproval", params: `{` + commandBase + `,"networkApprovalContext":{"host":"example.test","protocol":"https"},"availableDecisions":["accept"]}`, want: []ApprovalDecision{DecisionAccept}},
		{name: "network inexact context", method: "item/commandExecution/requestApproval", params: `{` + commandBase + `,"networkApprovalContext":{"host":"example.test","protocol":"https "},"availableDecisions":["accept","decline"]}`, want: []ApprovalDecision{DecisionDecline}},
		{name: "file stable", method: "item/fileChange/requestApproval", params: `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-f","startedAtMs":1,"grantRoot":null}`, want: []ApprovalDecision{DecisionAccept, DecisionDecline, DecisionCancel}},
		{name: "file unstable root", method: "item/fileChange/requestApproval", params: `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-f","startedAtMs":1,"grantRoot":"/root"}`, want: []ApprovalDecision{DecisionDecline, DecisionCancel}},
		{name: "permission supported", method: "item/permissions/requestApproval", params: `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-p","startedAtMs":1,"cwd":" /work ","permissions":{"network":{"enabled":true}}}`, want: []ApprovalDecision{DecisionGrantTurn}},
		{name: "permission empty", method: "item/permissions/requestApproval", params: `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-p","startedAtMs":1,"cwd":"/work","permissions":{}}`},
		{name: "permission unknown widening", method: "item/permissions/requestApproval", params: `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-p","startedAtMs":1,"cwd":"/work","permissions":{"network":{"enabled":true,"hosts":["*"]}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			n := Notification{Method: test.method, RequestID: "11", RawRequestID: json.RawMessage(`11`), Params: json.RawMessage(test.params)}
			envelope, recognized, err := DecodeApprovalEnvelope(n)
			if err != nil || !recognized || !reflect.DeepEqual(envelope.Decisions, test.want) {
				t.Fatalf("envelope decisions=%v want=%v recognized=%v err=%v", envelope.Decisions, test.want, recognized, err)
			}
			if strings.Contains(test.name, "command") && envelope.Command != "  echo ok  " {
				t.Fatalf("command content changed: %q", envelope.Command)
			}
			if test.name == "permission supported" && envelope.RequestCWD != " /work " {
				t.Fatalf("permission cwd changed: %q", envelope.RequestCWD)
			}
		})
	}
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
