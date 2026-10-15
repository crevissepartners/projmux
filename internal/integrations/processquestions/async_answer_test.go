package processquestions

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

func TestAsyncQuestionIPCReceiptAndLostReply(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipt", true: "reply-lost"}[lost], func(t *testing.T) {
			// Keep the Unix socket within its platform path bound even when CI
			// deliberately supplies a long TMPDIR. Do not change that environment.
			root, err := os.MkdirTemp("/tmp", "pmx-question-")
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
				var request envelope
				if err = localipc.ReadJSON(conn, &request); err != nil {
					done <- err
					return
				}
				if lost {
					done <- nil
					return
				}
				done <- localipc.WriteJSON(conn, Result{Accepted: true, QuestionReceipt: &AnswerReceipt{QuestionID: request.Questions.QuestionID, Delivery: "async", Outcome: "submitted"}})
			}()
			result, err := call(context.Background(), socket, inode, host, envelope{Questions: &Request{QuestionID: "question-0000000000000001"}}, true)
			if lost {
				var unknown *DeliveryError
				if !errors.As(err, &unknown) || unknown.Receipt.Code != "question-delivery-unknown" || result.QuestionReceipt == nil || result.QuestionReceipt.Outcome != "unknown" || result.QuestionReceipt.QuestionID != "question-0000000000000001" || strings.Contains(err.Error(), "nothing was changed") {
					t.Fatal(result, err)
				}
			} else if err != nil || result.QuestionReceipt == nil || result.QuestionReceipt.Outcome != "submitted" {
				t.Fatal(result, err)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAsyncQuestionIPCCancelBeforeDialIsNotUnknown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := (Dispatcher{RegistryPath: filepath.Join(t.TempDir(), "registry.json")}).call(ctx, "agent", "question-0000000000000001", nil, true)
	var unknown *DeliveryError
	if !errors.Is(err, context.Canceled) || errors.As(err, &unknown) || result.QuestionReceipt != nil {
		t.Fatal(result, err)
	}
}
