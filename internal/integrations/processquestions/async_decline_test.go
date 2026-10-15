package processquestions

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestAsyncDeclineIPCActionAndReceipt(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		t.Run(map[bool]string{false: "notice", true: "unsupported"}[unsupported], func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "pmx-decline-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			socket := filepath.Join(root, "q.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err = os.Chmod(socket, 0600); err != nil {
				t.Fatal(err)
			}
			inode, err := localipc.InspectOwnedSocket(socket)
			if err != nil {
				t.Fatal(err)
			}
			host, _, err := localipc.Process(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				conn, err := listener.AcceptUnix()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				var wire envelope
				if err = localipc.ReadJSON(conn, &wire); err != nil {
					done <- err
					return
				}
				if wire.Questions == nil || wire.Questions.Action != "decline" || wire.Questions.QuestionID != "question-0000000000000001" || len(wire.Questions.Selections) != 0 {
					done <- errors.New("incorrect decline wire")
					return
				}
				receipt := &AnswerReceipt{QuestionID: wire.Questions.QuestionID, Delivery: "async", Outcome: "submitted"}
				if unsupported {
					receipt.Outcome = ""
					receipt.Code = "question-decline-unsupported"
				}
				done <- localipc.WriteJSON(conn, Result{Accepted: !unsupported, QuestionReceipt: receipt})
			}()
			result, err := call(context.Background(), socket, inode, host, envelope{Questions: &Request{Action: "decline", QuestionID: "question-0000000000000001"}}, true)
			if err != nil || result.QuestionReceipt == nil || result.Accepted == unsupported {
				t.Fatal(result, err)
			}
			if unsupported {
				if result.QuestionReceipt.Code != "question-decline-unsupported" || result.QuestionReceipt.Outcome != "" {
					t.Fatal(result)
				}
			} else if result.QuestionReceipt.Outcome != "submitted" {
				t.Fatal(result)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
	// Adding the optional action must not change any old request's JSON bytes.
	legacy := struct {
		Binding    processhost.Binding
		QuestionID string
		Selections map[int]agentquestion.Selection
	}{QuestionID: "question-0000000000000001", Selections: map[int]agentquestion.Selection{0: {Text: "explicit", HasText: true}}}
	before, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(Request{Binding: legacy.Binding, QuestionID: legacy.QuestionID, Selections: legacy.Selections})
	if err != nil || string(before) != string(after) {
		t.Fatal(string(before), string(after), err)
	}
}

func TestAsyncDeclineOldHostAndLostReceipt(t *testing.T) {
	for _, mode := range []string{"old-host", "lost"} {
		t.Run(mode, func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "pmx-decline-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			socket := filepath.Join(root, "q.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err = os.Chmod(socket, 0600); err != nil {
				t.Fatal(err)
			}
			inode, err := localipc.InspectOwnedSocket(socket)
			if err != nil {
				t.Fatal(err)
			}
			host, _, err := localipc.Process(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				conn, err := listener.AcceptUnix()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				// Frozen old host request shape ignores the new Action but refuses an ID
				// with no selections before callback/native effects.
				var wire struct {
					Questions *struct {
						Binding    processhost.Binding
						QuestionID string
						Selections map[int]agentquestion.Selection
					}
				}
				if err = localipc.ReadJSON(conn, &wire); err != nil {
					done <- err
					return
				}
				if wire.Questions == nil || wire.Questions.QuestionID == "" || len(wire.Questions.Selections) != 0 {
					done <- errors.New("old host request shape not refused")
					return
				}
				if mode == "lost" {
					done <- nil
					return
				}
				done <- localipc.WriteJSON(conn, Result{})
			}()
			result, err := call(context.Background(), socket, inode, host, envelope{Questions: &Request{Action: "decline", QuestionID: "question-0000000000000001"}}, true)
			if mode == "lost" {
				var typed *DeliveryError
				if !errors.As(err, &typed) || !errors.Is(err, localipc.ErrFrameMalformed) || result.QuestionReceipt == nil || result.QuestionReceipt.Outcome != "unknown" {
					t.Fatal(result, err)
				}
			} else if err != nil || result.Accepted || result.QuestionReceipt != nil {
				t.Fatal("old wire refusal", result, err)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
