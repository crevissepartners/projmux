package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Model the observation boundary without depending on scheduler timing: a
// deadline can have elapsed before its context timer publishes Err/Done.
type detachedIdentityTimerContext struct {
	context.Context
	deadline time.Time
	done     chan struct{}
	err      error
}

func (c *detachedIdentityTimerContext) Deadline() (time.Time, bool) {
	return c.deadline, !c.deadline.IsZero()
}
func (c *detachedIdentityTimerContext) Done() <-chan struct{} { return c.done }
func (c *detachedIdentityTimerContext) Err() error            { return c.err }

func TestProcessDetachedIdentityFailureTimerOrder(t *testing.T) {
	for _, order := range []string{"socket-first", "context-first"} {
		t.Run(order, func(t *testing.T) {
			ctx := &detachedIdentityTimerContext{Context: context.Background(), deadline: time.Now().Add(-time.Second), done: make(chan struct{})}
			if order == "context-first" {
				ctx.err = context.DeadlineExceeded
				close(ctx.done)
			}
			readErr := &net.OpError{Op: "read", Net: "unix", Err: os.ErrDeadlineExceeded}
			wantText := fmt.Sprintf("process-detached-identity-handshake-failed: %v", errors.Join(readErr, ctx.Err()))
			err := processDetachedIdentityFailure(ctx, ctx.deadline, readErr)
			var transport *net.OpError
			if err.Error() != wantText || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, os.ErrDeadlineExceeded) || !errors.As(err, &transport) || transport != readErr {
				t.Fatalf("%s: text=%q want=%q context=%v transport=%v", order, err, wantText, errors.Is(err, context.DeadlineExceeded), transport)
			}
			if order == "socket-first" && ctx.Err() != nil {
				t.Fatal("fixture published context timer before socket observation")
			}
		})
	}
}

func TestProcessDetachedIdentityFailureOtherCauses(t *testing.T) {
	past, future := time.Now().Add(-time.Second), time.Now().Add(time.Hour)
	for _, tc := range []struct {
		name                string
		deadline, applied   time.Time
		readErr, contextErr error
	}{
		{"early-eof", future, future, io.EOF, nil},
		{"eof-after-deadline", past, past, io.EOF, nil},
		{"manual-cancel", past, past, os.ErrDeadlineExceeded, context.Canceled},
		{"independent-io-deadline", future, past, os.ErrDeadlineExceeded, nil},
		{"different-elapsed-deadline", past, past.Add(-time.Second), os.ErrDeadlineExceeded, nil},
		{"no-context-deadline", time.Time{}, past, os.ErrDeadlineExceeded, nil},
		{"timeout-before-context-deadline", future, future, os.ErrDeadlineExceeded, nil},
		{"no-applied-deadline", past, time.Time{}, os.ErrDeadlineExceeded, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &detachedIdentityTimerContext{Context: context.Background(), deadline: tc.deadline, done: make(chan struct{}), err: tc.contextErr}
			if ctx.err != nil {
				close(ctx.done)
			}
			err := processDetachedIdentityFailure(ctx, tc.applied, tc.readErr)
			want := fmt.Sprintf("process-detached-identity-handshake-failed: %v", errors.Join(tc.readErr, tc.contextErr))
			if err.Error() != want || !errors.Is(err, tc.readErr) || errors.Is(err, context.DeadlineExceeded) || (tc.contextErr != nil && !errors.Is(err, tc.contextErr)) {
				t.Fatalf("cause/text changed: %q want %q", err, want)
			}
		})
	}
}

func TestProcessDetachedIdentitySocketFirstHasNoBinding(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "silent-launcher")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexec sleep 20\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := &detachedIdentityTimerContext{Context: context.Background(), deadline: time.Now().Add(-time.Second), done: make(chan struct{})}
	var output bytes.Buffer
	launcher := ProcessDetachedLauncher{Executable: executable, PayloadDir: root, Verify: func(ProcessDetachedReply, coremetadata.ProcessIdentity) error {
		t.Fatal("identity timeout reached binding verification")
		return nil
	}}
	reply, err := launcher.Launch(ctx, struct{}{}, &output, &output)
	var transport *net.OpError
	if !errors.As(err, &transport) || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, os.ErrDeadlineExceeded) || output.Len() != 0 || !reflect.DeepEqual(reply, ProcessDetachedReply{}) {
		t.Fatalf("identity timeout: reply=%+v error=%v output=%q", reply, err, &output)
	}
	if want := "process-detached-identity-handshake-failed: " + transport.Error(); err.Error() != want {
		t.Fatalf("text=%q want=%q", err, want)
	}
}
