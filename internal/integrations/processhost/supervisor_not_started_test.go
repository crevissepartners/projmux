package processhost

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestSupervisorNotStartedProvesAFailureBeforeTheSupervisorRan(t *testing.T) {
	cases := map[string]func(t *testing.T) *Host{
		"reserve error": func(t *testing.T) *Host {
			return testHost(t, func(tx *Transactions, _ *Limits) {
				tx.Reserve = func(context.Context, Binding) error { return errors.New("reserve failed") }
			})
		},
		"absent supervisor": func(t *testing.T) *Host {
			h := testHost(t, nil)
			h.supervisor.Path = filepath.Join(t.TempDir(), "absent-supervisor")
			return h
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := build(t).Start(context.Background(), Launch{Binding: binding(), Command: fixtureCommand("idle")})
			if err == nil || p == nil {
				t.Fatalf("a launch that cannot start returned handle %v error %v", p, err)
			}
			if !p.SupervisorNotStarted(binding()) {
				t.Fatal("a failure before the supervisor ran was not proven")
			}
			if p.ProviderStartRejected(binding()) {
				t.Fatal("a failure before the supervisor ran must not count as a rejected provider start")
			}
			other := binding()
			other.Generation = "other"
			if p.SupervisorNotStarted(other) {
				t.Fatal("the proof held for another binding")
			}
		})
	}
}
