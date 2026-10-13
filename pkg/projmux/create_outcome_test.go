package projmux

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestCreateRecorderAdapterPreservesTimingsAndOwnership(t *testing.T) {
	held, spawn := 21*time.Millisecond, 7*time.Millisecond
	var phases mutationruntime.CreatePhases
	for i := range phases {
		d := time.Duration(i+1) * time.Millisecond
		phases[i] = &d
	}
	for _, kind := range []mutationruntime.CreateKind{mutationruntime.CreateKindWindow, mutationruntime.CreateKindPane, mutationruntime.CreateKindAgent, mutationruntime.CreateKindResume} {
		for _, result := range []string{"success", "error"} {
			original := mutationruntime.CreateOutcome{Kind: kind, Result: result, Duration: 30 * time.Millisecond, LockHeld: &held, Phases: phases}
			if kind == mutationruntime.CreateKindAgent || kind == mutationruntime.CreateKindResume {
				original.SpawnToRelease = &spawn
			}
			calls := 0
			adapter := publicCreateRecorder{func(o CreateOutcome) error {
				calls++
				if string(o.Kind) != string(kind) || string(o.Result) != result || o.Duration != original.Duration || !reflect.DeepEqual(o.Phases, CreatePhases(phases)) || *o.LockHeld != held || !reflect.DeepEqual(o.SpawnToRelease, original.SpawnToRelease) {
					t.Fatalf("lost outcome: %+v", o)
				}
				*o.LockHeld = 0
				for _, p := range o.Phases {
					*p = 0
				}
				if o.SpawnToRelease != nil {
					*o.SpawnToRelease = 0
				}
				return errors.New("sink unavailable")
			}}
			adapter.Record(original)
			if calls != 1 || held != 21*time.Millisecond || spawn != 7*time.Millisecond || *phases[0] != time.Millisecond {
				t.Fatal("callback altered source")
			}
		}
	}
	var got CreateOutcome
	publicCreateRecorder{func(o CreateOutcome) error { got = o; return nil }}.Record(mutationruntime.CreateOutcome{Kind: mutationruntime.CreateKindWindow, Result: "error"})
	if got.LockHeld != nil || got.SpawnToRelease != nil || got.Phases != (CreatePhases{}) {
		t.Fatal("nil timing lost")
	}
	if (&Client{}).createOutcomes() != nil {
		t.Fatal("nil option must disable recorder")
	}
	publicCreateRecorder{}.Record(mutationruntime.CreateOutcome{})
	publicCreateRecorder{func(CreateOutcome) error { panic("failed sink") }}.Record(mutationruntime.CreateOutcome{})
}

func TestSDKCreateRecorderConcurrentTransactionsAndPreflight(t *testing.T) {
	var calls atomic.Int32
	c, err := NewClient(Options{StateDir: t.TempDir(), ConfigDir: t.TempDir(), SocketPath: filepath.Join(t.TempDir(), "absent"), ProjmuxExecutable: "/bin/sh", CreateRecorder: func(o CreateOutcome) error {
		calls.Add(1)
		if o.Kind != CreateKindWindow || o.Result != CreateError || o.LockHeld != nil || o.Duration < 0 {
			t.Errorf("unexpected outcome: %+v", o)
		}
		return errors.New("ignored")
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []core.CreateWindowRequest{{}, {Project: "sample", Name: "bad/name"}, {Project: "sample", Labels: []string{"bad"}}} {
		if _, err = c.CreateWindow(context.Background(), req); err == nil {
			t.Fatal("expected refusal")
		}
	}
	_, _ = c.CreatePane(context.Background(), core.CreatePaneRequest{})
	_, _ = c.CreateAgent(context.Background(), core.CreateAgentRequest{})
	_, _ = c.ResumeAgent(context.Background(), core.ResumeAgentRequest{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.CreateWindow(ctx, core.CreateWindowRequest{Project: "sample"})
	if !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatal("preflight recorded")
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			_, err := c.CreateWindow(context.Background(), core.CreateWindowRequest{Project: "sample"})
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Code != CodeCreateRouteRefused {
				t.Errorf("changed result: %v", err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 12 {
		t.Fatalf("transactions=%d", calls.Load())
	}
}
