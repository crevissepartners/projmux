package codexappserver

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestThreadAndTurnStartCarryCodexModelAndEffort(t *testing.T) {
	t.Parallel()
	client, collect := scriptedEndpoint(t, map[string]string{
		methodThreadStart: `{"thread":{"id":"thread-model"}}`,
		methodTurnStart:   `{"turn":{"id":"turn-model"}}`,
	})
	if _, err := client.StartThreadWithModel(context.Background(), "/work", nil, "", ThreadPolicy{}, "gpt-6"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.StartTurnWithOptions(context.Background(), "thread-model", "review", "request-1", "gpt-6", "high"); err != nil {
		t.Fatal(err)
	}
	methods, raw := collect()
	if len(methods) != 2 || methods[0] != methodThreadStart || methods[1] != methodTurnStart {
		t.Fatalf("methods = %v", methods)
	}
	var thread, turn map[string]json.RawMessage
	if err := json.Unmarshal(raw[0], &thread); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw[1], &turn); err != nil {
		t.Fatal(err)
	}
	if string(thread["model"]) != `"gpt-6"` || bytes.Contains(raw[0], []byte(`"effort"`)) {
		t.Fatalf("thread/start params = %s", raw[0])
	}
	if string(turn["model"]) != `"gpt-6"` || string(turn["effort"]) != `"high"` {
		t.Fatalf("turn/start params = %s", raw[1])
	}
}
