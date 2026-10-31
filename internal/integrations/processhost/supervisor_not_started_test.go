package processhost

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSupervisorNotStartedProvesAFailureBeforeTheSupervisorRan(t *testing.T) {
	cases := []struct {
		name  string
		want  bool
		build func(t *testing.T) *Host
	}{
		{"reserve error", true, func(t *testing.T) *Host {
			return testHost(t, func(tx *Transactions, _ *Limits) {
				tx.Reserve = func(context.Context, Binding) error { return errors.New("reserve failed") }
			})
		}},
		{"absent supervisor", true, func(t *testing.T) *Host {
			h := testHost(t, nil)
			h.supervisor.Path = filepath.Join(t.TempDir(), "absent-supervisor")
			return h
		}},
		{"supervisor exits before its handshake", false, func(t *testing.T) *Host {
			path, err := exec.LookPath("true")
			if err != nil {
				t.Fatal(err)
			}
			h := testHost(t, nil)
			h.supervisor = Command{Path: path, Env: fixtureEnv()}
			return h
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := tc.build(t).Start(context.Background(), Launch{Binding: binding(), Command: fixtureCommand("idle")})
			if err == nil || p == nil {
				t.Fatalf("a launch that cannot start returned handle %v error %v", p, err)
			}
			if got := p.SupervisorNotStarted(binding()); got != tc.want {
				t.Fatalf("SupervisorNotStarted = %v, want %v", got, tc.want)
			}
			if p.ProviderStartRejected(binding()) {
				t.Fatal("a failure before the provider started must not count as a rejected provider start")
			}
			other := binding()
			other.Generation = "other"
			if p.SupervisorNotStarted(other) {
				t.Fatal("the proof held for another binding")
			}
		})
	}
}
