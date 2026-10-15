package processhost

import (
	"context"
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
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeWire, codexWire(t, log)) {
		t.Fatal("preflight changed host or wire")
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
