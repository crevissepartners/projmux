package codexappserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Regression: managed restarts used to omit all guidance on thread/resume,
// and create wrote a permanent developer message instead of replacing base.
func TestManagedInstructionsCreateAndRestart(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "restart"}[restart], func(t *testing.T) {
			c, collect := scriptedEndpoint(t, map[string]string{
				methodThreadStart:      `{"thread":{"id":"thread-settings"}}`,
				methodThreadResume:     `{"thread":{"id":"thread-settings"}}`,
				methodThreadLoadedList: `{"data":[]}`,
			})
			settings := ThreadSettings{ManagedInstructions: &ManagedInstructions{Base: "CURRENT-B"}}
			var err error
			if restart {
				_, err = c.ResumeThreadWithSettings(context.Background(), "thread-settings", "/work", nil, settings)
			} else {
				_, err = c.StartThreadWithSettings(context.Background(), "/work", nil, "OLD-DEVELOPER", settings)
			}
			if err != nil {
				t.Fatal(err)
			}
			methods, params := collect()
			found := false
			for i, m := range methods {
				if m != methodThreadStart && m != methodThreadResume {
					continue
				}
				found = true
				var p map[string]json.RawMessage
				if err := json.Unmarshal(params[i], &p); err != nil {
					t.Fatal(err)
				}
				if string(p["baseInstructions"]) != `"CURRENT-B"` {
					t.Fatalf("missing current base: %s", params[i])
				}
				if _, ok := p["developerInstructions"]; ok {
					t.Fatalf("permanent developer instructions: %s", params[i])
				}
			}
			if !found {
				t.Fatal("no thread request")
			}
		})
	}
}

func TestManagedInstructionsResumeRefusalIsNotRetried(t *testing.T) {
	for _, code := range []string{"-32600", "-32601", "-32602", "-32603"} {
		t.Run(code, func(t *testing.T) {
			c, collect := sequencedEndpoint(t, map[string][]string{
				methodThreadResume: {`error:{"code":` + code + `,"message":"resume refused"}`},
			})
			managed := &ManagedInstructions{Base: "B", Previous: "A"}
			_, err := c.ResumeThreadWithSettings(context.Background(), "thread-settings", "/work", nil, ThreadSettings{ManagedInstructions: managed})
			if err == nil || managed.Fallback || managed.Accepted || managed.Base != "B" {
				t.Fatalf("generic refusal changed managed state: %+v %v", managed, err)
			}
			d := Diagnostic(err)
			if d.Method != methodThreadResume || d.RPCCode == nil || fmt.Sprint(*d.RPCCode) != code {
				t.Fatalf("original refusal lost: %+v", d)
			}
			methods, _ := collect()
			if len(methods) != 1 || methods[0] != methodThreadResume {
				t.Fatalf("generic refusal retried: %v", methods)
			}
		})
	}
}

func TestManagedInstructionsWarmLegacyKeepsHistoryAndDisclosesUnconfirmedBase(t *testing.T) {
	c, collect := scriptedEndpoint(t, map[string]string{
		methodThreadResume: `{"thread":{"id":"thread-settings"}}`, methodThreadLoadedList: `{"data":["thread-settings"]}`, "thread/inject_items": `{}`,
	})
	managed := &ManagedInstructions{Base: "CURRENT-B", Legacy: true, Shared: true}
	_, err := c.ResumeThreadWithSettings(context.Background(), "thread-settings", "/work", nil, ThreadSettings{ManagedInstructions: managed})
	if err != nil || !managed.Warm || !managed.Accepted {
		t.Fatalf("warm legacy=%+v %v", managed, err)
	}
	methods, params := collect()
	injected := false
	for i, m := range methods {
		if m == methodThreadStart || m == methodTurnStart || m == "thread/delete" {
			t.Fatalf("history replaced or turn sent: %v", methods)
		}
		if m == "thread/inject_items" {
			injected = true
			var p struct {
				Items []struct {
					Role    string `json:"role"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"items"`
			}
			if err := json.Unmarshal(params[i], &p); err != nil {
				t.Fatal(err)
			}
			if len(p.Items) != 1 || p.Items[0].Role != "developer" || len(p.Items[0].Content) != 1 {
				t.Fatalf("legacy context: %s", params[i])
			}
		}
	}
	if !injected {
		t.Fatal("legacy context not delivered")
	}
}

func TestManagedLegacyInjectionRefusalFallsBackWithoutReplacingHistory(t *testing.T) {
	for _, code := range []string{"-32601", "-32602"} {
		t.Run(code, func(t *testing.T) {
			c, collect := sequencedEndpoint(t, map[string][]string{
				methodThreadResume:    {`{"thread":{"id":"thread-settings"}}`},
				"thread/inject_items": {`error:{"code":` + code + `,"message":"new context refused"}`},
				"thread/unsubscribe":  {`{"status":"unsubscribed"}`}, methodThreadLoadedList: {`{"data":[]}`},
			})
			managed := &ManagedInstructions{Base: "B", Previous: "A", Legacy: true}
			_, err := c.ResumeThreadWithSettings(context.Background(), "thread-settings", "/work", nil, ThreadSettings{ManagedInstructions: managed})
			if err != nil || !managed.Fallback || !managed.Accepted || managed.Base != "A" {
				t.Fatalf("legacy fallback %+v %v", managed, err)
			}
			methods, params := collect()
			var injected []string
			for i, m := range methods {
				if m == methodThreadStart || m == methodTurnStart || m == "thread/delete" {
					t.Fatalf("fallback replaced history or started input: %v", methods)
				}
				if m == "thread/inject_items" {
					injected = append(injected, string(params[i]))
				}
			}
			if len(injected) != 1 || !strings.Contains(injected[0], `\n\nB`) {
				t.Fatalf("refused context was retried or history was duplicated: %v", injected)
			}
		})
	}
}

func TestFirstManagedTransmissionFailureNeverSelectsProviderDefaults(t *testing.T) {
	c, collect := scriptedEndpoint(t, nil)
	managed := &ManagedInstructions{Base: "B"}
	_, err := c.ResumeThreadWithSettings(context.Background(), "thread-settings", "/work", nil, ThreadSettings{ManagedInstructions: managed})
	if err == nil || managed.Accepted || managed.Fallback {
		t.Fatalf("first failure fabricated confirmation/fallback: %+v %v", managed, err)
	}
	methods, _ := collect()
	if len(methods) != 1 || methods[0] != methodThreadResume {
		t.Fatalf("first failure tried another lane or fallback: %v", methods)
	}
}
