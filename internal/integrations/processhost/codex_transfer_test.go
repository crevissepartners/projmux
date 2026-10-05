package processhost

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func codexTransferFixture() (Launch, CodexTransfer) {
	b := resumedBinding()
	b.Pane = "new-pane"
	source := TmuxCodexSource{Project: b.Project, Window: b.Window, Agent: b.Agent, Pane: "old-pane", Generation: "old-gen", Operation: "old-op", RuntimeID: "%1", Thread: "thread", BrokerRuntime: "broker", Endpoint: "endpoint", ConnectionEpoch: 1, BindingEpoch: 2}
	return Launch{Binding: b}, CodexTransfer{Source: source, Verify: func(context.Context, TmuxCodexSource, Binding) error { return nil }}
}
func TestCodexTransferRejectsInvalidSourceBeforeSpawn(t *testing.T) {
	for _, name := range []string{"project", "window", "agent", "pane", "generation", "operation", "thread", "broker", "endpoint", "connection", "binding", "verify", "unretired", "resume"} {
		t.Run(name, func(t *testing.T) {
			launch, transfer := codexTransferFixture()
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
			case "thread":
				transfer.Source.Thread = " thread "
			case "broker":
				transfer.Source.BrokerRuntime = ""
			case "endpoint":
				transfer.Source.Endpoint = ""
			case "connection":
				transfer.Source.ConnectionEpoch = 0
			case "binding":
				transfer.Source.BindingEpoch = 0
			case "verify":
				transfer.Verify = nil
			case "unretired":
				transfer.Verify = func(context.Context, TmuxCodexSource, Binding) error { return ErrStale }
			case "resume":
				launch.resume = &SessionRecord{}
			}
			host := &Host{instance: launch.Binding.Host}
			handle, err := host.TransferCodex(t.Context(), launch, codexSettings(), transfer)
			if handle != nil || !errors.Is(err, ErrResumeRefused) {
				t.Fatalf("handle=%v err=%v", handle, err)
			}
		})
	}
}
func TestCodexTransferSameThreadFreshIdleAndNoStartFallback(t *testing.T) {
	for _, mode := range []string{"codex-normal", "codex-resume-wrong-thread", "codex-resume-refused"} {
		t.Run(mode, func(t *testing.T) {
			commits := 0
			host := testHost(t, func(tx *Transactions, _ *Limits) {
				tx.Commit = func(context.Context, Binding, string) error { commits++; return nil }
			})
			launch, transfer := codexTransferFixture()
			log := filepath.Join(t.TempDir(), "wire.jsonl")
			launch.Command = fixtureCommand(mode)
			launch.Command.Dir = t.TempDir()
			launch.Command.Env = append(launch.Command.Env, "PROCESSHOST_CODEX_LOG="+log)
			checks := 0
			transfer.Verify = func(context.Context, TmuxCodexSource, Binding) error { checks++; return nil }
			handle, err := host.TransferCodex(t.Context(), launch, codexSettings(), transfer)
			if handle == nil {
				t.Fatalf("no owned handle: %v", err)
			}
			t.Cleanup(func() { stopResume(t, handle.handle) })
			if checks != 2 {
				t.Fatalf("checks=%d", checks)
			}
			if mode == "codex-normal" {
				if err != nil {
					t.Fatal(err)
				}
				snap, err := handle.Observe(launch.Binding)
				if err != nil || snap.Session != "thread" || snap.Turn != "" || len(snap.Pending) != 0 || snap.Resume != nil || commits != 1 {
					t.Fatalf("snap=%+v commits=%d err=%v", snap, commits, err)
				}
			} else if !errors.Is(err, ErrResumeRefused) || commits != 0 {
				t.Fatalf("commits=%d err=%v", commits, err)
			}
			for _, request := range codexWire(t, log) {
				if string(request["method"]) == `"thread/start"` {
					t.Fatal("transfer started a new thread")
				}
			}
		})
	}
}
