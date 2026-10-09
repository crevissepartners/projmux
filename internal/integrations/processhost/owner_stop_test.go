package processhost

import (
	"context"
	"reflect"
	"syscall"
	"testing"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestOwnerStopClassificationPreservesWait(t *testing.T) {
	for _, tc := range []struct {
		name, mode, signal string
		owner              bool
		code               int
		classification     metadata.TerminationClassification
	}{
		{"explicit TERM exit143", "stop-exit143", "", true, 143, metadata.TerminationNormal},
		{"explicit TERM signal", "ignore-eof", "TERM", true, -1, metadata.TerminationNormal},
		{"explicit KILL escalation", "ignore-eof-term", "KILL", true, -1, metadata.TerminationNormal},
		{"external TERM exit143", "stop-exit143", "", false, 143, metadata.TerminationAbnormal},
		{"external TERM signal", "ignore-eof", "TERM", false, -1, metadata.TerminationAbnormal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := start(t, testHost(t, nil), tc.mode)
			turn(t, p, "ready", "normal")
			observeUntil(t, p, func(s Snapshot) bool { return s.State == "ready" && s.Turn == "" })
			if tc.owner {
				if err := p.Stop(binding()); err != nil {
					t.Fatal(err)
				}
			} else {
				s, _ := p.Observe(binding())
				if err := syscall.Kill(s.PID, syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s, err := p.Wait(ctx, binding())
			if err != nil {
				t.Fatal(err)
			}
			receipt, ok := s.Termination(time.Now())
			if !ok || receipt.Classification != tc.classification || s.Exit.Code != tc.code || s.Exit.Signal != tc.signal || receipt.Signal != tc.signal {
				t.Fatalf("Wait=%+v receipt=%+v snapshot=%+v", s.Exit, receipt, s)
			}
			if tc.signal == "" && (receipt.ExitCode == nil || *receipt.ExitCode != tc.code) {
				t.Fatal(receipt)
			}
			if tc.signal != "" && receipt.ExitCode != nil {
				t.Fatal("signal Wait gained a fabricated code", receipt)
			}
			if err := p.Stop(binding()); err != nil {
				t.Fatal(err)
			}
			after, _ := p.Observe(binding())
			again, _ := after.Termination(receipt.ObservedAt)
			if !reflect.DeepEqual(receipt, again) {
				t.Fatal("late Stop relabeled observed Wait", receipt, again)
			}
			t.Logf("actual Wait=%+v classification=%s", s.Exit, receipt.Classification)
		})
	}
}

func TestOwnerStopDoesNotNormalizeFailureCleanup(t *testing.T) {
	for _, mode := range []string{"exit7", "stdout-eof"} {
		t.Run(mode, func(t *testing.T) {
			p := start(t, testHost(t, nil), mode)
			s := observeUntil(t, p, func(s Snapshot) bool { return s.State == "exited" })
			if err := p.Stop(binding()); err != nil {
				t.Fatal(err)
			}
			receipt, ok := s.Termination(time.Now())
			if !ok || receipt.Classification != metadata.TerminationAbnormal {
				t.Fatal(receipt, s)
			}
		})
	}
}
