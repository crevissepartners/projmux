package processhost

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// These deliberately invalid owned status streams are test-only supervisor
// fixtures. Product supervisor dispatch and its wire format remain unchanged.
func init() {
	if len(os.Args) != 3 || os.Args[1] != "processhost-start-rejected-fixture" {
		return
	}
	status := os.NewFile(5, "status")
	encoder := json.NewEncoder(status)
	_ = encoder.Encode(processStatus{Prepared: true})
	var spec supervisorSpec
	_ = json.NewDecoder(os.NewFile(4, "spec")).Decode(&spec)
	switch os.Args[2] {
	case "eof":
	case "partial":
		_, _ = io.WriteString(status, `{"Error":`)
	case "empty":
		_ = encoder.Encode(processStatus{})
	case "error-with-pid":
		_ = encoder.Encode(processStatus{Error: "fixture rejection", PID: 2000000000})
	case "error-with-exit":
		_ = encoder.Encode(processStatus{Error: "fixture rejection", Exit: &Exit{Code: 1}})
	case "error-with-prepared":
		_ = encoder.Encode(processStatus{Error: "fixture rejection", Prepared: true})
	}
	os.Exit(1)
}

func rejectedProviderCommand(t *testing.T) Command {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "provider")
	if err := os.WriteFile(path, []byte("#!"+filepath.Join(root, "absent-interpreter")+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return Command{Path: path, Env: fixtureEnv()}
}

func TestProviderStartRejectedRequiresOwnedCompletion(t *testing.T) {
	for _, provider := range []string{"plain", "codex"} {
		t.Run(provider, func(t *testing.T) {
			for _, mode := range []string{"no-callback", "empty-completion", "success", "error", "timeout-late-nil"} {
				t.Run(mode, func(t *testing.T) {
					h := testHost(t, nil)
					release, returned := make(chan struct{}), make(chan struct{})
					during := make(chan bool, 1)
					var releaseOnce sync.Once
					var completion *Completion
					if mode == "empty-completion" {
						completion = &Completion{}
					} else if mode != "no-callback" {
						completion = &Completion{Cleanup: func(ctx context.Context) error {
							defer close(returned)
							h.mu.Lock()
							owned := h.operations[binding().Operation]
							h.mu.Unlock()
							// The callback itself is still running, so even a validated
							// Start rejection cannot yet authorize reservation removal.
							proof := owned.ProviderStartRejected(binding())
							if (&CodexHandle{handle: owned}).ProviderStartRejected(binding()) != proof {
								t.Error("pending Codex wrapper changed completion proof")
							}
							during <- proof
							if mode == "error" {
								return errors.New("rejection cleanup fixture error")
							}
							if mode == "timeout-late-nil" {
								<-ctx.Done()
								<-release
							}
							return nil
						}}
					}
					launch := Launch{Binding: binding(), Command: rejectedProviderCommand(t), Completion: completion}
					var p *Handle
					var wrapped *CodexHandle
					var startErr error
					if provider == "codex" {
						wrapped, startErr = h.StartCodex(context.Background(), launch, CodexConfig{})
						if wrapped != nil {
							p = wrapped.handle
						}
					} else {
						p, startErr = h.Start(context.Background(), launch)
					}
					assertSameProof := func(b Binding) {
						t.Helper()
						if wrapped != nil && wrapped.ProviderStartRejected(b) != p.ProviderStartRejected(b) {
							t.Fatal("Codex wrapper changed the owned Handle proof")
						}
					}
					if mode == "timeout-late-nil" {
						// Release the late callback even if a subsequent assertion fails.
						defer func() { releaseOnce.Do(func() { close(release) }); <-returned }()
					}
					if startErr == nil || p == nil || !strings.Contains(startErr.Error(), "absent-interpreter") && !strings.Contains(startErr.Error(), "no such file") {
						t.Fatalf("actual supervisor did not reject provider Start: %v handle=%p", startErr, p)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					snapshot, err := p.Wait(ctx, binding())
					if err != nil || snapshot.PID != 0 || snapshot.SupervisorPID <= 0 || snapshot.Exit != nil {
						t.Fatalf("rejected Start fabricated provider birth/Wait: %+v %v", snapshot, err)
					}
					if _, ok := snapshot.Termination(time.Now()); ok {
						t.Fatal("rejected Start manufactured provider termination")
					}
					if completion != nil && completion.Cleanup != nil {
						select {
						case got := <-during:
							if got {
								t.Fatal("unfinished completion authorized rollback")
							}
						case <-ctx.Done():
							t.Fatal("completion did not report its pending proof")
						}
					}
					assertSameProof(binding())
					want := mode != "error" && mode != "timeout-late-nil"
					if got := p.ProviderStartRejected(binding()); got != want {
						t.Fatalf("completion=%s rejection proof=%t want=%t diagnostic=%q", mode, got, want, snapshot.Diagnostic)
					}
					wrong := binding()
					wrong.Generation += "-replaced"
					assertSameProof(wrong)
					if p.ProviderStartRejected(wrong) {
						t.Fatal("Start rejection escaped its exact generation")
					}
					if !want && !strings.Contains(snapshot.Diagnostic, "owned completion cleanup:") {
						t.Fatal("cleanup failure diagnostic changed", snapshot.Diagnostic)
					}
					if mode == "timeout-late-nil" {
						// The asynchronous nil return occurs after done has closed and
						// must never upgrade the recorded timeout to cleanup success.
						releaseOnce.Do(func() { close(release) })
						<-returned
						assertSameProof(binding())
						if p.ProviderStartRejected(binding()) {
							t.Fatal("late nil completion upgraded failed cleanup")
						}
					}
				})
			}
		})
	}
}

func TestProviderStartRejectedRejectsUnknownStatus(t *testing.T) {
	t.Run("nil-wrapper", func(t *testing.T) {
		var empty *CodexHandle
		if empty.ProviderStartRejected(binding()) || (&CodexHandle{}).ProviderStartRejected(binding()) {
			t.Fatal("nil or empty Codex wrapper authorized rollback")
		}
	})
	for _, mode := range []string{"eof", "partial", "empty", "error-with-pid", "error-with-exit", "error-with-prepared"} {
		t.Run(mode, func(t *testing.T) {
			h := testHost(t, nil)
			h.supervisor.Args = []string{"processhost-start-rejected-fixture", mode}
			p, err := h.Start(context.Background(), Launch{Binding: binding(), Command: rejectedProviderCommand(t)})
			if err == nil || p == nil {
				t.Fatalf("invalid status unexpectedly started: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := p.Wait(ctx, binding()); err != nil {
				t.Fatal(err)
			}
			if p.ProviderStartRejected(binding()) || (&CodexHandle{handle: p}).ProviderStartRejected(binding()) {
				t.Fatalf("%s status became a trusted no-birth receipt", mode)
			}
		})
	}
	// Reserve failure is a Start error too, but carries no supervisor proof.
	t.Run("reserve-error", func(t *testing.T) {
		h := testHost(t, func(tx *Transactions, _ *Limits) {
			tx.Reserve = func(context.Context, Binding) error { return errors.New("fixture reserve failure") }
		})
		p, err := h.Start(context.Background(), Launch{Binding: binding(), Command: rejectedProviderCommand(t)})
		if err == nil || p == nil || p.ProviderStartRejected(binding()) || (&CodexHandle{handle: p}).ProviderStartRejected(binding()) {
			t.Fatal("generic Start failure became provider rejection proof", err)
		}
	})
}

func TestProviderStartRejectedPreservesBornProviderWait(t *testing.T) {
	h := testHost(t, nil)
	p, err := h.Start(context.Background(), Launch{Binding: binding(), Command: fixtureCommand("normal")})
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = p.Stop(binding())
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = p.Wait(ctx, binding())
		}
	})
	if p.ProviderStartRejected(binding()) || (&CodexHandle{handle: p}).ProviderStartRejected(binding()) {
		t.Fatal("live provider became a child-free launch")
	}
	stopped = true
	if err := p.Stop(binding()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snapshot, err := p.Wait(ctx, binding())
	if err != nil || snapshot.PID <= 0 || snapshot.Exit == nil {
		t.Fatalf("born provider lost actual Wait: %+v %v", snapshot, err)
	}
	if p.ProviderStartRejected(binding()) || (&CodexHandle{handle: p}).ProviderStartRejected(binding()) {
		t.Fatal("retired born provider became a child-free launch")
	}
	if _, ok := snapshot.Termination(time.Now()); !ok {
		t.Fatal("born provider lost termination evidence")
	}
}
