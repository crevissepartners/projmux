package codexappserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

// TestInstalledHermeticTopologyQualification is the single lifecycle owner
// for the installed direct and managed topologies. The old daemon-only smoke's
// root, socket, readiness, and classification assertions are intentionally
// gone; the shared fixture now owns those boundaries and always emits one
// validated terminal result per topology when the opt-in root is supplied.
func TestInstalledHermeticTopologyQualification(t *testing.T) {
	root, enabled, err := codexinstalled.SmokeRoot(codexinstalled.DefaultSmokeRootEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("set PROJMUX_CODEX_DAEMON_SMOKE_ROOT for the installed topology qualification")
	}
	fixture, err := codexinstalled.NewClean(root)
	if err != nil {
		t.Fatal(err)
	}
	fixture.ApplyEnv(t.Setenv)
	t.Cleanup(func() {
		if err := fixture.Cleanup(); err != nil {
			t.Errorf("installed topology cleanup: %v", err)
		}
	})

	provision := fixture.ProvisionManagedPayload()
	logInstalledResult(t, provision)
	if provision.Class != codexinstalled.ResultPass && provision.Class != codexinstalled.ResultUnsupported {
		t.Fatalf("managed payload provision = %+v", provision)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	direct, ready := fixture.StartDirect(ctx, "installed-topology-qualification")
	logInstalledResult(t, ready)
	if ready.Class != codexinstalled.ResultPass {
		t.Fatalf("direct ready = %+v", ready)
	}
	if health := direct.Health(); health.EndpointReadiness != codexappserver.EndpointReady ||
		health.ManagerOwnership != codexappserver.ManagerUnmanaged ||
		health.VersionRelation != codexappserver.VersionCurrent ||
		health.NativeAction != codexappserver.NativeActionRefused ||
		health.NativeRefusal != codexappserver.NativeActionRefusalUnmanaged ||
		health.Lifecycle != codexappserver.LifecycleNotAttempted ||
		health.LifecycleReason != codexappserver.LifecycleReasonReadOnly {
		t.Fatalf("direct topology diagnostics = endpoint=%s ownership=%s version=%s native=%s/%s lifecycle=%s/%s",
			health.EndpointReadiness, health.ManagerOwnership, health.VersionRelation,
			health.NativeAction, health.NativeRefusal, health.Lifecycle, health.LifecycleReason)
	}
	closeCtx, closeCancel := context.WithTimeout(ctx, 20*time.Second)
	closed := direct.Close(closeCtx)
	closeCancel()
	logInstalledResult(t, closed)
	if closed.Class != codexinstalled.ResultPass {
		t.Fatalf("direct close = %+v", closed)
	}

	managed := fixture.RunManagedLifecycle(ctx, "installed-topology-qualification")
	logInstalledResult(t, managed)
	if managed.Class != codexinstalled.ResultPass && managed.Class != codexinstalled.ResultUnsupported {
		t.Fatalf("managed topology = %+v", managed)
	}
	if err := fixture.Ledger().AssertNoAmbientMutation(); err != nil {
		t.Fatal(err)
	}
}

func logInstalledResult(t *testing.T, result codexinstalled.Result) {
	t.Helper()
	encoded, err := result.JSON()
	if err != nil {
		t.Fatalf("invalid installed qualification result: %v", err)
	}
	t.Logf("installed-result: %s", encoded)
}

// This opt-in uses the same installed fixture's ownership and cleanup contract,
// but a separate root so topology qualification never shares dirty state.
func TestInstalledBoundedPaginatedGrowingLifecycle(t *testing.T) {
	root, enabled, err := codexinstalled.SmokeRoot(codexinstalled.DefaultSmokeRootEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("installed lifecycle qualification is opt-in")
	}
	executable, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	for _, bodyBytes := range []int{17 << 20, 21 << 20} {
		t.Run(fmt.Sprint(bodyBytes), func(t *testing.T) {
			fixture, err := codexinstalled.NewClean(root + "b" + fmt.Sprint(bodyBytes>>20))
			if err != nil {
				t.Fatal(err)
			}
			fixture.ApplyEnv(t.Setenv)
			t.Cleanup(func() {
				if err := fixture.Cleanup(); err != nil {
					t.Error(err)
				}
			})
			modelStarted := make(chan struct{}, 2)
			release := make(chan struct{})
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				modelStarted <- struct{}{}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}))
			t.Cleanup(model.Close)
			defer close(release)
			config := fmt.Sprintf("model=\"fixture-model\"\nmodel_provider=\"fixture\"\n[model_providers.fixture]\nname=\"fixture\"\nbase_url=%q\nwire_api=\"responses\"\nrequires_openai_auth=false\nrequest_max_retries=0\nstream_max_retries=0\n", model.URL+"/v1")
			if err := os.WriteFile(filepath.Join(fixture.CodexHome, "config.toml"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			thread := "11111111-1111-4111-8111-111111111111"
			sessionDir := filepath.Join(fixture.CodexHome, "sessions", "2026", "10", "06")
			if err := os.MkdirAll(sessionDir, 0700); err != nil {
				t.Fatal(err)
			}
			records := []map[string]any{
				{"type": "session_meta", "payload": map[string]any{"id": thread, "session_id": "22222222-2222-4222-8222-222222222222", "timestamp": "2026-10-06T00:00:00Z", "cwd": fixture.Workspace, "originator": "projmux", "source": "vscode", "cli_version": "0.160.0", "model_provider": "fixture", "history_mode": "paginated"}},
				{"type": "event_msg", "payload": map[string]any{"type": "task_started", "turn_id": "old-turn", "model_context_window": nil}},
				{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "fixture history"}}}},
				{"type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": strings.Repeat("x", bodyBytes)}}}},
				{"type": "event_msg", "payload": map[string]any{"type": "task_complete", "turn_id": "old-turn", "last_agent_message": nil}},
			}
			var history bytes.Buffer
			for i, record := range records {
				record["ordinal"] = i
				record["timestamp"] = "2026-10-06T00:00:00Z"
				raw, _ := json.Marshal(record)
				history.Write(raw)
				history.WriteByte('\n')
			}
			historyPath := filepath.Join(sessionDir, "rollout-2026-10-06T00-00-00-"+thread+".jsonl")
			if err := os.WriteFile(historyPath, history.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			client, cleanup := startInstalledLifecycleUnix(t, ctx, fixture, executable)
			defer func() {
				if err := client.Close(); err != nil {
					t.Error(err)
				}
			}()
			if err := client.Request(ctx, "thread/resume", map[string]any{"threadId": thread, "excludeTurns": true, "modelProvider": "fixture", "model": "fixture-model"}, nil); err != nil {
				t.Fatal(err)
			}
			previousBytes := int64(history.Len())
			previousTurn := ""
			for iteration := range 2 {
				turn, err := client.StartExactTurn(ctx, thread, strings.Repeat("i", (iteration+1)*1024))
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-modelStarted:
				case <-ctx.Done():
					t.Fatal("fixture model did not start")
				}
				operation, operationCancel := context.WithTimeout(ctx, 750*time.Millisecond)
				started := time.Now()
				owned, err := codexappserver.OpenPrivateUnixLifecycle(operation, fixture.SocketPath, "bounded-lifecycle", true, client.PeerIdentity())
				if err != nil {
					operationCancel()
					t.Fatal(err)
				}
				snapshot, readErr := owned.ReadLifecycleSnapshot(operation, thread)
				closeErr := owned.Close()
				elapsed := time.Since(started)
				operationCancel()
				if readErr != nil || closeErr != nil {
					t.Fatalf("read=%s close=%v", codexappserver.Diagnostic(readErr).String(), closeErr)
				}
				if snapshot.ThreadID != thread || snapshot.ThreadState != codexappserver.ThreadStateActive || snapshot.TurnID != turn.TurnID || snapshot.TurnState != codexappserver.TurnStateInProgress || snapshot.TurnCount != -1 {
					t.Fatalf("snapshot=%+v", snapshot)
				}
				if elapsed >= 750*time.Millisecond {
					t.Fatalf("operation exceeded existing budget: %s", elapsed)
				}
				// The subscribed sibling still serves a bodyless read after owned cleanup.
				if err := client.Request(ctx, "thread/read", map[string]any{"threadId": thread, "includeTurns": false}, nil); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(historyPath)
				if err != nil {
					t.Fatal(err)
				}
				if info.Size() <= previousBytes || turn.TurnID == previousTurn {
					t.Fatalf("actual history/latest did not grow/change: bytes=%d prior=%d", info.Size(), previousBytes)
				}
				t.Logf("same-thread iteration=%d historyBytes=%d growth=%d operation=%s exactLatestChanged=true", iteration, info.Size(), info.Size()-previousBytes, elapsed)
				previousBytes, previousTurn = info.Size(), turn.TurnID
				if _, err := client.InterruptExactTurn(ctx, thread, turn.TurnID); err != nil {
					t.Fatal(err)
				}
				completed := false
				for !completed {
					select {
					case event, ok := <-client.Notifications():
						if !ok {
							t.Fatal("sibling notification transport ended")
						}
						if event.Method != "turn/completed" {
							continue
						}
						var ended struct {
							ThreadID string `json:"threadId"`
							Turn     struct {
								ID     string `json:"id"`
								Status string `json:"status"`
							} `json:"turn"`
						}
						if json.Unmarshal(event.Params, &ended) == nil && ended.ThreadID == thread && ended.Turn.ID == turn.TurnID && ended.Turn.Status == "interrupted" {
							completed = true
						}
					case <-ctx.Done():
						t.Fatal("exact interrupted turn did not complete")
					}
				}
			}
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			cleanup()
			t.Logf("installed paginated initialHistoryBytes=%d exact-active=true sibling=true owned/direct-cleanup=true", history.Len())
		})
	}
}

// The installed topology fixture's proxy readiness is a separate legacy seam.
// This test owns an explicit Unix child and verifies its kernel peer instead.
func startInstalledLifecycleUnix(t *testing.T, ctx context.Context, fixture *codexinstalled.Fixture, executable string) (*codexappserver.Client, func()) {
	t.Helper()
	// Resolve the qualified installed executable before the fixture adds its shim.
	if !filepath.IsAbs(executable) {
		t.Fatal("installed executable is not absolute")
	}
	socket := fixture.SocketPath
	if _, err := os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("fixture socket was not absent")
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, executable, "app-server", "--listen", "unix://"+socket) // #nosec G204 -- qualified executable and exact owned socket.
	command.Env = os.Environ()
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	var witness, linkWitness os.FileInfo
	var resolved string
	var peer codexappserver.PeerIdentity
	closed := false
	cleanup := func() {
		if closed {
			return
		}
		closed = true
		started := time.Now()
		var waitErr error
		select {
		case waitErr = <-exited:
		default:
			if err := command.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Error(err)
			}
			select {
			case waitErr = <-exited:
			case <-time.After(time.Second):
				if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
					t.Error(err)
				}
				select {
				case waitErr = <-exited:
				case <-time.After(time.Second):
					t.Error("owned child Wait did not complete")
					return
				}
			}
		}
		for _, owned := range []struct {
			path string
			info os.FileInfo
		}{{resolved, witness}, {socket, linkWitness}} {
			if owned.path == "" {
				continue
			}
			actual, err := os.Lstat(owned.path)
			if err == nil {
				if owned.info == nil || !os.SameFile(owned.info, actual) {
					t.Error("unknown or replacement fixture socket preserved")
				} else if err := os.Remove(owned.path); err != nil {
					t.Error(err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Error(err)
			}
		}
		if waitErr != nil {
			var exitErr *exec.ExitError
			if !errors.As(waitErr, &exitErr) {
				t.Error(waitErr)
			}
		}
		t.Logf("owned PID=%d birth=%s actualWaitRC=%d cleanup=%s socketWitness=%t", command.Process.Pid, peer.Start, command.ProcessState.ExitCode(), time.Since(started), witness != nil)
	}
	t.Cleanup(cleanup)
	ready, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		client, err := codexappserver.OpenPrivateUnix(ready, socket, 250*time.Millisecond, "bounded-lifecycle", true)
		if err == nil {
			closeClient := func() {
				if err := client.Close(); err != nil {
					t.Error(err)
				}
			}
			t.Cleanup(closeClient)
			peer = client.PeerIdentity()
			if !peer.Valid() || peer.PID != command.Process.Pid {
				closeClient()
				t.Fatal("readiness peer is not the exact owned child")
			}
			linkWitness, err = os.Lstat(socket)
			if err != nil {
				closeClient()
				t.Fatal("owned socket link witness unavailable")
			}
			resolved, err = filepath.EvalSymlinks(socket)
			if err != nil {
				closeClient()
				t.Fatal("owned socket target unavailable")
			}
			witness, err = os.Lstat(resolved)
			if err != nil || witness.Mode()&os.ModeSocket == 0 {
				closeClient()
				t.Fatal("owned socket witness unavailable")
			}
			if resolved == socket {
				linkWitness = nil
			} // a direct socket has one inode
			t.Logf("qualified CLI=%s exactPeerPID=%d birth=%s socketBytes=%d", fixture.Versions().CLI, peer.PID, peer.Start, len(socket))
			return client, cleanup
		}
		select {
		case <-ready.Done():
			t.Fatal("explicit Unix readiness timed out")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
