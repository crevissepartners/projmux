package lifecycle

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestTaggedKillerExecuteKillsTargetsWithoutSwitchWhenPlannerDoesNotNeedIt(t *testing.T) {
	t.Parallel()

	switcher := &recordingSessionSwitcher{}
	killer := &recordingSessionKiller{}
	service := NewTaggedKiller(switcher, killer)

	got, err := service.Execute(context.Background(), TaggedKillInputs{
		CurrentSession: "repo-z",
		KillTargets:    []string{"repo-a", "repo-b"},
		RecentSessions: []string{"repo-b", "repo-c"},
		HomeSession:    "home",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if switcher.calls != nil {
		t.Fatalf("SwitchClient called with %#v, want none", switcher.calls)
	}

	wantKilled := []string{"repo-a", "repo-b"}
	if !reflect.DeepEqual(killer.calls, wantKilled) {
		t.Fatalf("KillSession calls = %#v, want %#v", killer.calls, wantKilled)
	}

	if got.SwitchPlan.SwitchNeeded {
		t.Fatalf("Execute() SwitchNeeded = true, want false")
	}
	if !reflect.DeepEqual(got.KilledSessions, wantKilled) {
		t.Fatalf("Execute() KilledSessions = %#v, want %#v", got.KilledSessions, wantKilled)
	}
}

func TestTaggedKillerExecuteSwitchesBeforeKillingTargets(t *testing.T) {
	t.Parallel()

	switcher := &recordingSessionSwitcher{}
	killer := &recordingSessionKiller{}
	service := NewTaggedKiller(switcher, killer)

	got, err := service.Execute(context.Background(), TaggedKillInputs{
		CurrentSession: "repo-a",
		KillTargets:    []string{"repo-a", "repo-b", "repo-a", "  "},
		RecentSessions: []string{"repo-b", "repo-c", "repo-d"},
		HomeSession:    "home",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !reflect.DeepEqual(switcher.calls, []string{"repo-c"}) {
		t.Fatalf("SwitchClient calls = %#v, want %#v", switcher.calls, []string{"repo-c"})
	}

	wantKilled := []string{"repo-a", "repo-b"}
	if !reflect.DeepEqual(killer.calls, wantKilled) {
		t.Fatalf("KillSession calls = %#v, want %#v", killer.calls, wantKilled)
	}

	if !got.SwitchPlan.SwitchNeeded {
		t.Fatalf("Execute() SwitchNeeded = false, want true")
	}
	if got.SwitchPlan.Target != "repo-c" {
		t.Fatalf("Execute() SwitchPlan.Target = %q, want %q", got.SwitchPlan.Target, "repo-c")
	}
	if !reflect.DeepEqual(got.KilledSessions, wantKilled) {
		t.Fatalf("Execute() KilledSessions = %#v, want %#v", got.KilledSessions, wantKilled)
	}
}

func TestTaggedKillerExecuteFallsBackToHomeBeforeKill(t *testing.T) {
	t.Parallel()

	switcher := &recordingSessionSwitcher{}
	killer := &recordingSessionKiller{}
	service := NewTaggedKiller(switcher, killer)

	got, err := service.Execute(context.Background(), TaggedKillInputs{
		CurrentSession: "repo-a",
		KillTargets:    []string{"repo-a", "repo-b"},
		RecentSessions: []string{"repo-b", "repo-a"},
		HomeSession:    "home",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !reflect.DeepEqual(switcher.calls, []string{"home"}) {
		t.Fatalf("SwitchClient calls = %#v, want %#v", switcher.calls, []string{"home"})
	}
	if got.SwitchPlan.Target != "home" {
		t.Fatalf("Execute() SwitchPlan.Target = %q, want %q", got.SwitchPlan.Target, "home")
	}
}

func TestTaggedKillerExecutePropagatesSwitchFailure(t *testing.T) {
	t.Parallel()

	switcher := &recordingSessionSwitcher{err: errors.New("boom")}
	killer := &recordingSessionKiller{}
	service := NewTaggedKiller(switcher, killer)

	_, err := service.Execute(context.Background(), TaggedKillInputs{
		CurrentSession: "repo-a",
		KillTargets:    []string{"repo-a"},
		HomeSession:    "home",
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}

	if len(killer.calls) != 0 {
		t.Fatalf("KillSession calls = %#v, want none", killer.calls)
	}
}

func TestTaggedKillerExecutePropagatesKillFailure(t *testing.T) {
	t.Parallel()

	switcher := &recordingSessionSwitcher{}
	killer := &recordingSessionKiller{
		failOn: "repo-b",
		err:    errors.New("boom"),
	}
	service := NewTaggedKiller(switcher, killer)

	got, err := service.Execute(context.Background(), TaggedKillInputs{
		CurrentSession: "repo-z",
		KillTargets:    []string{"repo-a", "repo-b", "repo-c"},
		HomeSession:    "home",
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}

	wantCalls := []string{"repo-a", "repo-b"}
	if !reflect.DeepEqual(killer.calls, wantCalls) {
		t.Fatalf("KillSession calls = %#v, want %#v", killer.calls, wantCalls)
	}
	if !reflect.DeepEqual(got, TaggedKillResult{}) {
		t.Fatalf("Execute() result = %#v, want zero value on failure", got)
	}
}

type recordingSessionSwitcher struct {
	calls []string
	err   error
}

func (r *recordingSessionSwitcher) SwitchClient(_ context.Context, sessionName string) error {
	r.calls = append(r.calls, sessionName)
	if r.err != nil {
		return r.err
	}
	return nil
}

type recordingSessionKiller struct {
	calls  []string
	failOn string
	err    error
}

func (r *recordingSessionKiller) KillSession(_ context.Context, sessionName string) error {
	r.calls = append(r.calls, sessionName)
	if sessionName == r.failOn {
		return r.err
	}
	return nil
}
