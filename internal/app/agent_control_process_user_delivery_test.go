package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessUserDeliveryRequiresActualTypedReceipt(t *testing.T) {
	valid := processhost.UserTurnDelivery{Mode: processhost.UserTurnStart, Operation: "op", TurnID: "turn"}
	for _, mode := range []processhost.UserTurnMode{processhost.UserTurnStart, processhost.UserTurnSteer} {
		receipt := valid
		receipt.Mode = mode
		got, err := processUserDeliveryAcceptance(processForegroundResult{Accepted: true, UserDelivery: &receipt}, "op")
		if err != nil || got != receipt {
			t.Fatalf("receipt=%+v err=%v", got, err)
		}
	}
	for _, name := range []string{"missing", "mode", "operation", "empty-turn", "malformed-turn", "stale", "busy", "closed", "peer-receipt", "not-accepted"} {
		t.Run(name, func(t *testing.T) {
			receipt := valid
			result := processForegroundResult{Accepted: true, UserDelivery: &receipt}
			switch name {
			case "missing":
				result.UserDelivery = nil
			case "mode":
				receipt.Mode = "legacy"
			case "operation":
				receipt.Operation = "other"
			case "empty-turn":
				receipt.TurnID = ""
			case "malformed-turn":
				receipt.TurnID = " turn "
			case "stale":
				result.Stale = true
			case "busy":
				result.Busy = true
			case "closed":
				result.Closed = true
			case "peer-receipt":
				result.Receipt = &codexProcessReceipt{}
			case "not-accepted":
				result.Accepted = false
			}
			got, err := processUserDeliveryAcceptance(result, "op")
			if !errors.Is(err, processhost.ErrStale) || got != (processhost.UserTurnDelivery{}) {
				t.Fatalf("accepted malformed receipt: %+v %v", got, err)
			}
		})
	}
}

// A legacy host may return Accepted without a user receipt. Transport success
// cannot turn that into start, and malformed/lost responses cannot trigger a
// second connection or provider fallback.
func TestProcessUserDeliveryCallerRejectsLegacyAndMalformedResponses(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent(f.endpoint.binding.Agent)
	paths := config.Paths{StateDir: filepath.Join(f.root, "receipt-client")}
	socket := processHostSocket(aiModeCodex, intmetadata.PathFor(paths.StateDir), f.endpoint.binding.Pane, f.endpoint.binding.Generation)
	listener, closeLease, err := listenProcessHost(socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeLease(context.Background()); err != nil {
			t.Error(err)
		}
	})
	c := &agentCommand{controlPaths: func() (config.Paths, error) { return paths, nil }}
	for _, name := range []string{"legacy", "mode", "operation", "turn", "lost"} {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				conn, err := listener.Unix.AcceptUnix()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(localipc.Deadline))
				var request codexProcessExchange
				if err := localipc.ReadJSON(conn, &request); err != nil {
					done <- err
					return
				}
				if request.Foreground == nil || request.Foreground.Action != "user-deliver" {
					done <- fmt.Errorf("wrong route: %+v", request)
					return
				}
				receipt := processhost.UserTurnDelivery{Mode: processhost.UserTurnStart, Operation: request.Foreground.Operation, TurnID: "turn"}
				result := processForegroundResult{Accepted: true, UserDelivery: &receipt}
				switch name {
				case "legacy":
					result.UserDelivery = nil
				case "mode":
					receipt.Mode = "unknown"
				case "operation":
					receipt.Operation = "other"
				case "turn":
					receipt.TurnID = ""
				case "lost":
					done <- nil
					return
				}
				done <- localipc.WriteJSON(conn, result)
			}()
			got, err := c.callProcessUserDelivery(reg, *agent, "input")
			if err == nil || got != (processhost.UserTurnDelivery{}) {
				t.Fatalf("%s fabricated receipt: %+v %v", name, got, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			_ = listener.Unix.SetDeadline(time.Now().Add(20 * time.Millisecond))
			conn, err := listener.Unix.AcceptUnix()
			_ = listener.Unix.SetDeadline(time.Time{})
			if err == nil {
				conn.Close()
				t.Fatal("client retried after bad receipt")
			}
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatalf("retry check: %v", err)
			}
		})
	}
}
