package processhost

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestResumePublishesBirthBeforeInitializationAndOnlyOnce(t *testing.T) {
	var published atomic.Int32
	host := testHost(t, func(tx *Transactions, _ *Limits) {
		tx.Commit = func(context.Context, Binding, string) error {
			if published.Load() != 1 {
				return errors.New("init reached Commit before birth publication")
			}
			return nil
		}
	})
	old := SessionRecord{Provider: "claude", Binding: binding(), Session: "session", Connection: "operation", Turn: "old-turn"}
	launch := Launch{Binding: resumedBinding(), Command: fixtureCommand("resume-claude"), Spawned: &SpawnCallback{Publish: func(ctx context.Context, handle *Handle) error {
		snapshot, err := handle.Observe(resumedBinding())
		if err != nil {
			return err
		}
		if snapshot.PID <= 0 || snapshot.State != "starting" || snapshot.Session != "" {
			return errors.New("callback has no exact uninitialized child")
		}
		published.Add(1)
		return ctx.Err()
	}}}
	handle, err := host.ResumeClaude(context.Background(), launch, old, "new-turn", "new user task")
	if err != nil {
		t.Fatal(err)
	}
	defer stopResume(t, handle)
	retry, err := host.ResumeClaude(context.Background(), launch, old, "new-turn", "new user task")
	if err != nil || retry != handle || published.Load() != 1 {
		t.Fatal("retry repeated spawn publication", err)
	}
}

func TestSpawnPublicationFailureStopsOwnedChildBeforeInit(t *testing.T) {
	refusal := errors.New("birth publication failed")
	host := testHost(t, nil)
	launch := Launch{Binding: binding(), Command: fixtureCommand("resume-claude"), Spawned: &SpawnCallback{Publish: func(context.Context, *Handle) error { return refusal }}}
	handle, err := host.Start(context.Background(), launch)
	if !errors.Is(err, refusal) || handle == nil {
		t.Fatal("spawn error lost its owned handle", err)
	}
	snapshot, err := handle.Wait(context.Background(), binding())
	if err != nil || snapshot.Exit == nil || snapshot.Session != "" {
		t.Fatal("publication failure initialized or failed to reap child", err)
	}
}
