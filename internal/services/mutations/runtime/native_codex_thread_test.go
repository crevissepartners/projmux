package runtime

import (
	"context"
	"errors"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"testing"
)

type nativeThreadFixture struct {
	start, turn, close                       int
	failure                                  error
	instructions, prompt, key, model, effort string
}

func (f *nativeThreadFixture) StartThreadWithModel(_ context.Context, _ string, _ []string, instructions string, _ codexappserver.ThreadPolicy, model string) (codexappserver.ThreadBinding, error) {
	f.start++
	f.instructions = instructions
	f.model = model
	return codexappserver.ThreadBinding{ThreadID: "thread"}, f.failure
}
func (f *nativeThreadFixture) StartTurnWithOptions(_ context.Context, id, prompt, key, model, effort string) (string, error) {
	f.turn++
	f.prompt = prompt
	f.key = key
	f.model = model
	f.effort = effort
	return "turn", f.failure
}
func (f *nativeThreadFixture) Close() error { f.close++; return nil }
func TestNativeThreadCreateNeverRetriesUnknownRPC(t *testing.T) {
	uncertain := errors.New("unknown RPC outcome")
	f := &nativeThreadFixture{failure: uncertain}
	out, err := CreateNativeCodexThread(t.Context(), NativeCodexCreateInput{Workspace: metadata.AgentWorkspace{CWD: "/workspace"}, Prompt: "one"}, func(context.Context, bool) (NativeCodexThreadClient, error) { return f, nil }, func(context.Context, metadata.AgentWorkspace, string) error {
		t.Fatal("unexpected durable resume")
		return nil
	})
	if !errors.Is(err, uncertain) || f.start != 1 || f.turn != 0 || f.close != 1 || out.ThreadID != "thread" {
		t.Fatalf("unknown outcome was retried/lost: %+v %+v %v", f, out, err)
	}
	f = &nativeThreadFixture{}
	out, err = CreateNativeCodexThread(t.Context(), NativeCodexCreateInput{Prompt: "one", DeveloperInstructions: "instructions", RequestKey: "generation", Model: "model", Effort: "low"}, func(context.Context, bool) (NativeCodexThreadClient, error) { return f, nil }, nil)
	if err != nil || f.start != 1 || f.turn != 1 || f.close != 1 || out.ThreadID != "thread" || out.TurnID != "turn" || f.key != "generation" || f.instructions != "instructions" || f.prompt != "one" || f.model != "model" || f.effort != "low" {
		t.Fatalf("initial payload lost: %+v %+v %v", f, out, err)
	}
}
