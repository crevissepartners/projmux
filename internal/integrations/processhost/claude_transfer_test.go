package processhost

import (
	"context"
	"errors"
	"testing"
)

func transferFixture() (Launch, ClaudeTransfer) {
	b := resumedBinding()
	b.Pane = "new-pane"
	s := TmuxConversationSource{Project: b.Project, Window: b.Window, Agent: b.Agent, Pane: "old-pane", Generation: "old-generation", Operation: "old-operation", RuntimeID: "%1", Session: "session"}
	return Launch{Binding: b}, ClaudeTransfer{Source: s, Verify: func(context.Context, TmuxConversationSource, Binding) error { return nil }}
}

func TestClaudeTransferRejectsUnprovenSourcesBeforeSpawn(t *testing.T) {
	for _, name := range []string{"project", "window", "agent", "pane", "generation", "operation", "session", "verifier", "unretired", "prompt", "session-argument"} {
		t.Run(name, func(t *testing.T) {
			launch, transfer := transferFixture()
			prompt := "continue"
			switch name {
			case "project":
				transfer.Source.Project = "foreign"
			case "window":
				transfer.Source.Window = "foreign"
			case "agent":
				transfer.Source.Agent = "foreign"
			case "pane":
				transfer.Source.Pane = launch.Binding.Pane
			case "generation":
				transfer.Source.Generation = launch.Binding.Generation
			case "operation":
				transfer.Source.Operation = launch.Binding.Operation
			case "session":
				transfer.Source.Session = " session "
			case "verifier":
				transfer.Verify = nil
			case "unretired":
				transfer.Verify = func(context.Context, TmuxConversationSource, Binding) error { return errors.New("old writer is live") }
			case "prompt":
				prompt = " "
			case "session-argument":
				launch.Command.Args = []string{"--session-id=foreign"}
			}
			// No executable, supervisor or transaction is configured: admitting
			// any child instead of refusing here fails the test.
			host := &Host{instance: launch.Binding.Host}
			handle, err := host.TransferClaude(context.Background(), launch, transfer, "new-turn", prompt)
			if handle != nil || !errors.Is(err, ErrResumeRefused) {
				t.Fatalf("handle=%v err=%v", handle, err)
			}
		})
	}
}

func TestClaudeTransferAcceptsOnlyTheRecordedSession(t *testing.T) {
	for _, mode := range []string{"resume-claude", "resume-claude-wrong-session"} {
		t.Run(mode, func(t *testing.T) {
			host := testHost(t, nil)
			launch, transfer := transferFixture()
			launch.Command = fixtureCommand(mode)
			verified := 0
			transfer.Verify = func(_ context.Context, source TmuxConversationSource, target Binding) error {
				verified++
				if source.Agent != target.Agent || source.Pane == target.Pane {
					t.Fatal("invalid transfer ownership")
				}
				return nil
			}
			handle, err := host.TransferClaude(context.Background(), launch, transfer, "new-turn", "ordinary")
			if handle == nil {
				t.Fatalf("owned handle missing: %v", err)
			}
			t.Cleanup(func() { stopResume(t, handle) })
			if verified != 1 {
				t.Fatalf("retirement verifier called %d times", verified)
			}
			if mode == "resume-claude-wrong-session" {
				if !errors.Is(err, ErrResumeRefused) {
					t.Fatalf("foreign session accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			snapshot := waitResume(t, handle, func(s Snapshot) bool { return s.Session == "session" && s.Turn == "" })
			if snapshot.Binding != launch.Binding || snapshot.Resume != nil {
				t.Fatalf("synthetic process history: %+v", snapshot)
			}
		})
	}
}
