package processhost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
)

func TestAsyncAnswerFramePreflightHasNoEffects(t *testing.T) {
	c, _, log := codexStart(t, testHost(t, nil), "codex-normal")
	a := codexAuthority(c)
	// The fixture emits thread/started after the thread/start reply. Consume
	// that startup event before taking a fresh snapshot and wire baseline.
	observeUntil(t, c.handle, func(Snapshot) bool {
		events, _, err := c.Events(a.Binding, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Kind != "provider-event" || event.Session != a.Session {
				continue
			}
			var started struct {
				Thread struct {
					ID string `json:"id"`
				} `json:"thread"`
			}
			if json.Unmarshal(event.Raw, &started) == nil && started.Thread.ID == a.Session {
				return true
			}
		}
		return false
	})
	before, _ := c.Observe(a.Binding)
	beforeWire := codexWire(t, log)
	if err := c.ValidateUserTurnFrame(context.Background(), a, "answer", "explicit answer"); err != nil {
		t.Fatal(err)
	}
	wrong := a
	wrong.Binding.Generation = "wrong"
	if err := c.ValidateUserTurnFrame(context.Background(), wrong, "answer", "x"); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	if err := c.ValidateUserTurnFrame(context.Background(), a, "answer", strings.Repeat("x", 1<<20)); !errors.Is(err, agentquestion.ErrCapacity) {
		t.Fatal(err)
	}
	after, _ := c.Observe(a.Binding)
	afterWire := codexWire(t, log)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeWire, afterWire) {
		var beforeMethods, afterMethods, eventKinds []string
		for _, message := range beforeWire {
			beforeMethods = append(beforeMethods, string(message["method"]))
		}
		for _, message := range afterWire {
			afterMethods = append(afterMethods, string(message["method"]))
		}
		events, _, eventsErr := c.Events(a.Binding, 0)
		for _, event := range events {
			eventKinds = append(eventKinds, event.Kind)
		}
		t.Fatalf("preflight changed host or wire: hostEqual=%t wireEqual=%t state=%s/%s sequence=%d/%d events=%v eventsErr=%v wireMethods=%v/%v", reflect.DeepEqual(before, after), reflect.DeepEqual(beforeWire, afterWire), before.State, after.State, before.Sequence, after.Sequence, eventKinds, eventsErr, beforeMethods, afterMethods)
	}
	result, err := c.DeliverUserTurn(context.Background(), a, "answer", "hold")
	if err != nil || !result.WriteAttempted || result.Mode != UserTurnStart {
		t.Fatal(result, err)
	}
	result, err = c.DeliverUserTurn(context.Background(), a, "answer", "retry")
	if !errors.Is(err, ErrStale) || result.WriteAttempted {
		t.Fatal("before-write classification", result, err)
	}
}

func TestAsyncAnswerNativeCallBoundaryIsConservative(t *testing.T) {
	for _, mode := range []string{"codex-steer-refusal", "codex-steer-stall"} {
		t.Run(mode, func(t *testing.T) {
			c, _, _ := codexStart(t, testHost(t, func(_ *Transactions, l *Limits) { l.Startup = 300 * time.Millisecond }), mode)
			a := codexAuthority(c)
			if err := c.Turn(context.Background(), a, "initial", "hold"); err != nil {
				t.Fatal(err)
			}
			result, err := c.DeliverUserTurn(context.Background(), a, "answer", "explicit")
			if err == nil || !result.WriteAttempted {
				t.Fatal("post-call failure misclassified", result, err)
			}
		})
	}
}
