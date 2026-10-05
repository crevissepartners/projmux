package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type retirementWireFunc func(context.Context, string, any, any) error

func (f retirementWireFunc) Request(ctx context.Context, method string, params, result any) error {
	return f(ctx, method, params, result)
}
func TestRetirementBarrierUsesAllPagesAndRejectsCursorCycles(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		t.Run(map[bool]string{false: "second-page-thread", true: "cursor-cycle"}[cycle], func(t *testing.T) {
			calls := 0
			wire := retirementWireFunc(func(_ context.Context, method string, _, result any) error {
				if method != "thread/loaded/list" {
					t.Fatal(method)
				}
				calls++
				raw := `{"data":["sibling"],"nextCursor":"next"}`
				if calls == 2 && !cycle {
					raw = `{"data":["exact"],"nextCursor":null}`
				}
				return json.Unmarshal([]byte(raw), result)
			})
			present, err := retirementThreadLoaded(t.Context(), wire, "exact", func() error { return nil })
			if calls != 2 || (!cycle && (err != nil || !present)) || (cycle && !errors.Is(err, ErrProtocol)) {
				t.Fatalf("present=%v calls=%d err=%v", present, calls, err)
			}
		})
	}
}
func TestRetirementBarrierRejectsAuthorityChangeAndCanceledAbsence(t *testing.T) {
	changed := errors.New("changed endpoint")
	guards := 0
	wire := retirementWireFunc(func(_ context.Context, method string, _, result any) error {
		return json.Unmarshal([]byte(`{"data":[],"nextCursor":null}`), result)
	})
	err := AwaitThreadRetirement(t.Context(), wire, "exact", func() error {
		guards++
		if guards == 2 {
			return changed
		}
		return nil
	})
	if !errors.Is(err, changed) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err = AwaitThreadRetirement(ctx, wire, "exact", func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestUnsubscribeAcknowledgementIsNotBarrier(t *testing.T) {
	for _, status := range []string{"unsubscribed", "notSubscribed", "notLoaded", "unknown"} {
		wire := retirementWireFunc(func(_ context.Context, method string, _, result any) error {
			if method != "thread/unsubscribe" {
				t.Fatal(method)
			}
			return json.Unmarshal([]byte(`{"status":"`+status+`"}`), result)
		})
		_, err := UnsubscribeThread(t.Context(), wire, "exact")
		if (err != nil) != (status == "unknown") {
			t.Fatal(status, err)
		}
	}
}
