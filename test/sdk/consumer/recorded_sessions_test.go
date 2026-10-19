package consumer_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestExternalRecordedSessionsConsumer(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(filepath.Join(state, "metadata"), 0700); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(state, "metadata", "registry.json")
	// A current session must not be added even when the Registry is valid.
	data := []byte(`{"apiVersion":"projmux.io/v1alpha1","schemaVersion":5,"agents":[{"metadata":{"uid":"deleted"},"spec":{"provider":"codex"},"status":{"sessionRef":{"provider":"codex","codex":{"threadId":"current-only"}}}}]}`)
	if err := os.WriteFile(registry, data, 0600); err != nil {
		t.Fatal(err)
	}
	history := []byte(`{"agentUID":"deleted","provider":"codex","sessionId":"recorded","projectUID":"gone","source":"observed"}` + "\n")
	if err := os.WriteFile(filepath.Join(state, "agent-session-history.jsonl"), history, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := projmux.NewClient(projmux.Options{HomeDir: root, ConfigDir: filepath.Join(root, "config"), StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	var result core.RecordedSessionsList
	result, err = c.ListRecordedSessions(context.Background())
	if err != nil || len(result.Sessions) != 1 || result.Sessions[0].SessionID != "recorded" || result.Sessions[0].ProjectUID != "gone" {
		t.Fatalf("recorded: %+v %v", result, err)
	}
	after, err := os.ReadFile(registry)
	if err != nil || string(after) != string(data) {
		t.Fatalf("Registry changed: %v", err)
	}
	if err := os.Remove(filepath.Join(state, "agent-session-history.jsonl")); err != nil {
		t.Fatal(err)
	}
	result, err = c.ListRecordedSessions(context.Background())
	body, _ := json.Marshal(result)
	if err != nil || string(body) != `{"sessions":[],"corruptLines":0}` {
		t.Fatalf("current leaked: %s %v", body, err)
	}
}
