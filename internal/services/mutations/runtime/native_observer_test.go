package runtime

import (
	"context"
	"reflect"
	"testing"
)

func TestNativeObserverStarterAdmissionAndConvergence(t *testing.T) {
	for _, test := range []struct {
		name    string
		current bool
		cancel  bool
		receipt NativeObserverStartup
		want    []string
	}{
		{name: "cancelled", current: true, cancel: true},
		{name: "replaced"},
		{name: "fallback", current: true, receipt: NativeObserverStartup{Status: "fallback", Reason: "observer-exited"}, want: []string{"pending", "launch", CodexAuthorityHook}},
		{name: "committed", current: true, receipt: NativeObserverStartup{Status: "fallback", Reason: "unavailable", Committed: true}, want: []string{"pending", "launch"}},
		{name: "slow", current: true, receipt: NativeObserverStartup{Status: "retrying", Reason: "observer-timeout"}, want: []string{"pending", "launch", "pending->" + CodexAuthorityHook}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.cancel {
				cancel()
			}
			var calls []string
			starter := NativeObserverStarter{BindingCurrent: func() bool { return test.current }, SetAuthority: func(source, _, _ string) error { calls = append(calls, source); return nil }, SetAuthorityFrom: func(from, source, _, _ string) (bool, error) {
				calls = append(calls, from+"->"+source)
				return true, nil
			}, Launch: func() NativeObserverStartup { calls = append(calls, "launch"); return test.receipt }}
			result := starter.Start(ctx)
			if !reflect.DeepEqual(calls, test.want) {
				t.Fatalf("effects=%v want=%v", calls, test.want)
			}
			if (test.cancel || !test.current) && result.Status != "stale" {
				t.Fatalf("admission=%+v", result)
			}
			if test.current && !test.cancel && result != test.receipt {
				t.Fatalf("receipt=%+v want=%+v", result, test.receipt)
			}
		})
	}
}
