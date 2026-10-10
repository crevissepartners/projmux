package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestProcessOwnerStopConcurrentCausesRecordOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata", "registry.json")
	_, r := newProcessOwnerStop(context.Background(), path, processhost.Binding{Agent: "agent-fixture", Pane: "pane-fixture", Generation: "gen-fixture", Operation: "op-fixture"})
	var stderr bytes.Buffer
	r.setStderr(&stderr)
	var wg sync.WaitGroup
	for _, reason := range []diagnostics.OwnerStopReason{diagnostics.OwnerStopSIGINT, diagnostics.OwnerStopSIGTERM, diagnostics.OwnerStopStdinEOF, diagnostics.OwnerStopControl, diagnostics.OwnerStopGeneration, diagnostics.OwnerStopProviderExit, diagnostics.OwnerStopOther} {
		wg.Go(func() { r.record(reason) })
	}
	wg.Wait()
	events, err := diagnostics.NewStore(filepath.Join(filepath.Dir(filepath.Dir(path)), diagnostics.LogDirName, diagnostics.LogFileName)).Read()
	if err != nil || len(events) != 1 || bytes.Count(stderr.Bytes(), []byte("agent owner stop:")) != 1 {
		t.Fatalf("events=%+v stderr=%s err=%v", events, stderr.String(), err)
	}
	if events[0].OwnerPID != os.Getpid() || events[0].OwnerPPID != os.Getppid() {
		t.Fatal("wrong owner identity")
	}
}

func TestProcessOwnerStopContextFirstCauseWins(t *testing.T) {
	for _, reason := range []diagnostics.OwnerStopReason{diagnostics.OwnerStopSIGINT, diagnostics.OwnerStopSIGTERM, diagnostics.OwnerStopStdinEOF} {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(processStopCause(reason))
		cancel(processStopCause(diagnostics.OwnerStopOther))
		if got := processContextStopReason(ctx); got != reason {
			t.Fatalf("got %s want %s", got, reason)
		}
	}
}

func TestProcessOwnedWaitOtherFailureHasCause(t *testing.T) {
	for _, detached := range []bool{false, true} {
		t.Run(fmt.Sprint(detached), func(t *testing.T) {
			store, b := sessionBindingFixture(t, aiModeCodex)
			h := newGenerationOwnedHandle(store, aiModeCodex, b)
			owner := &processAgentCreateResult{Binding: b, Handle: h, Provider: aiModeCodex, registryPath: store.Path()}
			ctx := context.Background()
			if detached {
				ctx = (processOwnerLifetime{}).withContext(ctx)
			}
			_, err := owner.waitProcessAgent(ctx, func(processhost.Snapshot) error { return errors.New("synchronization failed") })
			if err == nil || h.stops.Load() != 1 {
				t.Fatalf("err=%v stops=%d", err, h.stops.Load())
			}
			// Direct typed waits retain the real owner's stderr; inspect journal alone.
			events, e := diagnostics.NewStore(filepath.Join(filepath.Dir(filepath.Dir(store.Path())), diagnostics.LogDirName, diagnostics.LogFileName)).Read()
			if e != nil || len(events) != 1 || events[0].Code != "owner.stop.other" {
				t.Fatalf("events=%+v err=%v", events, e)
			}
		})
	}
}

func TestProcessOwnerStopRetainsDefaultStderrForTypedWait(t *testing.T) {
	_, r := newProcessOwnerStop(context.Background(), filepath.Join(t.TempDir(), "metadata", "registry.json"), processhost.Binding{})
	r.setStderr(nil)
	if r.stderr != os.Stderr {
		t.Fatal("typed Wait suppressed the owner stderr")
	}
}

func TestProcessOwnerStopJournalFailureKeepsStderr(t *testing.T) {
	root := t.TempDir()
	// A file in place of the log directory makes Append fail immediately.
	if err := os.WriteFile(filepath.Join(root, "logs"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	_, r := newProcessOwnerStop(context.Background(), filepath.Join(root, "metadata", "registry.json"), processhost.Binding{Agent: "agent-fixture", Pane: "pane-fixture", Generation: "gen-fixture", Operation: "op-fixture"})
	var stderr bytes.Buffer
	r.setStderr(&stderr)
	r.record(diagnostics.OwnerStopControl)
	r.record(diagnostics.OwnerStopProviderExit)
	if bytes.Count(stderr.Bytes(), []byte("agent owner stop:")) != 1 || !bytes.Contains(stderr.Bytes(), []byte("reason=control-stop")) {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func TestProcessOwnerStopCauseSurvivesDetachedHandoff(t *testing.T) {
	foreground, cancel := context.WithCancelCause(context.Background())
	// A detached web owner shadows stdin lifetime and handoff detaches cancellation.
	ctx := context.WithValue(foreground, processForegroundCauseKey{}, foreground)
	ctx = (processOwnerLifetime{}).withContext(ctx)
	detached, end := context.WithCancel(context.WithoutCancel(ctx))
	cancel(processStopCause(diagnostics.OwnerStopSIGTERM))
	end()
	if got := processContextStopReason(detached); got != diagnostics.OwnerStopSIGTERM {
		t.Fatalf("handoff cause=%s", got)
	}
}

type processFailedStdin struct{}

func (processFailedStdin) Read([]byte) (int, error) { return 0, errors.New("stdin read failed") }
func TestProcessStdinEndDistinguishesEOFAndReadFailure(t *testing.T) {
	for _, tc := range []struct {
		reader io.Reader
		want   diagnostics.OwnerStopReason
	}{
		{bytes.NewReader(nil), diagnostics.OwnerStopStdinEOF},
		{processFailedStdin{}, diagnostics.OwnerStopOther},
	} {
		ended := make(chan diagnostics.OwnerStopReason, 1)
		processStdinEndTrigger(tc.reader, func(reason diagnostics.OwnerStopReason) { ended <- reason })
		select {
		case got := <-ended:
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		case <-time.After(time.Second):
			t.Fatal("stdin end was not reported")
		}
	}
}
