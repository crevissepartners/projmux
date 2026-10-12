package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestCreateExecutableIsExplicitBeforeEffects(t *testing.T) {
	for _, binary := range []string{"", "relative", filepath.Join(t.TempDir(), "missing")} {
		c, err := NewClient(Options{StateDir: t.TempDir(), ConfigDir: t.TempDir(), ProjmuxExecutable: binary})
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "example"})
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeCreateExecutableInvalid {
			t.Fatalf("%q: %T %v", binary, err, err)
		}
	}
}
func TestRefusalJSONMeaningPreserved(t *testing.T) {
	// Legacy order and tags before Task 6b. Normalize only object key order.
	type legacy struct {
		AgentUID      string `json:"agentUID,omitempty"`
		Code          Code
		Name          string
		Users         []string
		Field, Reason string
	}
	for _, uid := range []string{"", "agent-protected"} {
		previous := legacy{uid, CodeAgentProtected, "named", []string{"one"}, "field", "reason"}
		current := Refusal{Code: previous.Code, AgentUID: uid, Name: previous.Name, Users: previous.Users, Field: previous.Field, Reason: previous.Reason}
		a, err := json.Marshal(previous)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(current)
		if err != nil {
			t.Fatal(err)
		}
		var x, y map[string]any
		if err = json.Unmarshal(a, &x); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(b, &y); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(x, y) {
			t.Fatalf("JSON meaning changed: %s / %s", a, b)
		}
	}
}

func TestCreateResultEmptyExecutionFails(t *testing.T) {
	_, err := createFirstResult(nil)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeCreateExecutionFailed {
		t.Fatalf("%T %v", err, err)
	}
}
