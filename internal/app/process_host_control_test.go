package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type foregroundTestInput struct {
	Socket   string
	Identity localipc.SocketIdentity
	Host     coremetadata.ProcessIdentity
	Codex    bool
	Request  processForegroundRequest
}

func TestProcessForegroundClientHelper(t *testing.T) {
	if os.Getenv("PMX_TEST_FOREGROUND_CLIENT") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var in foregroundTestInput
		if json.Unmarshal(scanner.Bytes(), &in) != nil {
			os.Exit(2)
		}
		var wire any = claudeProcessCheck{Foreground: &in.Request}
		if in.Codex {
			wire = codexProcessExchange{Foreground: &in.Request}
		}
		ctx, cancel := context.WithTimeout(context.Background(), localipc.Deadline)
		result, err := callProcessForeground(ctx, in.Socket, in.Identity, in.Host, wire)
		cancel()
		if err != nil {
			result = processForegroundResult{Stale: true}
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
	}
}

func TestCodexProcessForegroundExactPeerControls(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			var socket, root string
			var handle interface {
				Observe(processhost.Binding) (processhost.Snapshot, error)
			}
			var binding processhost.Binding
			var store *intmetadata.Store
			if provider == "codex" {
				f := newProcessCodexFixture(t, nil)
				socket, root, handle, binding = f.endpoint.socket, f.root, f.endpoint.handle, f.endpoint.binding
				store = f.store
			} else {
				f := newProcessClaudeFixture(t, nil)
				socket, root, handle, binding = filepath.Join(f.root, "fg", "host.sock"), f.root, f.handle, f.binding
				store = f.store
				listener, closeLease, err := listenProcessHost(socket)
				if err != nil {
					t.Fatal(err)
				}
				snap, _ := f.handle.Observe(f.binding)
				child, _, err := localipc.Process(snap.PID)
				if err != nil {
					t.Fatal(err)
				}
				ownedHost, _, err := localipc.Process(os.Getpid())
				if err != nil {
					t.Fatal(err)
				}
				s := &claudeProcessService{handle: f.handle, binding: f.binding, ownedProcess: child, ownedHostProcess: ownedHost, registryPath: f.path, listener: listener, ready: make(chan struct{})}
				close(s.ready)
				t.Cleanup(func() {
					if err := closeLease(context.Background()); err != nil {
						t.Error(err)
					}
				})
				go s.serve(context.Background())
			}
			host, _, err := localipc.Process(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			identity, err := localipc.InspectOwnedSocket(socket)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestProcessForegroundClientHelper$")
			cmd.Env = append(os.Environ(), "PMX_TEST_FOREGROUND_CLIENT=1", "HOME="+root)
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = stdin.Close()
				if err := cmd.Wait(); err != nil {
					t.Error(err)
				}
			})
			peer, _, err := localipc.Process(cmd.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("foreground client pid=%d host=%d", peer.PID, host.PID)
			encoder, decoder := json.NewEncoder(stdin), json.NewDecoder(stdout)
			snap, err := handle.Observe(binding)
			if err != nil {
				t.Fatal(err)
			}
			prompt := "hold"
			if provider == "claude" {
				prompt = "interrupt"
			}
			base := processForegroundRequest{Authority: processhost.Authority{Binding: binding, Session: snap.Session, Connection: snap.Connection}, Action: "turn", Operation: "foreground-1", Prompt: prompt}
			exchange := func(request processForegroundRequest, h coremetadata.ProcessIdentity, i localipc.SocketIdentity) processForegroundResult {
				t.Helper()
				if err := encoder.Encode(foregroundTestInput{socket, i, h, provider == "codex", request}); err != nil {
					t.Fatal(err)
				}
				var result processForegroundResult
				if err := decoder.Decode(&result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			refused := func(r processForegroundRequest, h coremetadata.ProcessIdentity, i localipc.SocketIdentity) {
				t.Helper()
				if result := exchange(r, h, i); !result.Stale || result.Accepted {
					t.Fatalf("unverified peer admitted: %+v", result)
				}
			}
			bad := base
			bad.Authority.Session = "foreign"
			refused(bad, host, identity)
			bad = base
			bad.Authority.Binding.Generation = "old"
			refused(bad, host, identity)
			bad = base
			bad.Authority.Binding.Host = "other"
			refused(bad, host, identity)
			bad = base
			bad.Authority.Binding.Agent = "other"
			refused(bad, host, identity)
			bad = base
			bad.Authority.Connection = "old"
			refused(bad, host, identity)
			badHost := host
			badHost.Start += "-stale"
			refused(base, badHost, identity)
			refused(base, host, localipc.SocketIdentity{})
			if result := exchange(base, host, identity); !result.Accepted {
				t.Fatalf("exact client refused: %+v", result)
			}
			refused(base, host, identity)
			base.Action = "interrupt"
			deadline := time.Now().Add(localipc.Deadline)
			for {
				snap, err = handle.Observe(binding)
				if err != nil {
					t.Fatal(err)
				}
				if snap.Turn != "" && snap.State == "ready" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("turn not observed")
				}
				time.Sleep(time.Millisecond)
			}
			base.Authority.Session = snap.Session
			base.Turn = snap.Turn
			if result := exchange(base, host, identity); !result.Accepted {
				t.Fatalf("exact interrupt refused: %+v", result)
			}
			wait := func(predicate func(processhost.Snapshot) bool) processhost.Snapshot {
				t.Helper()
				deadline := time.Now().Add(localipc.Deadline)
				for {
					snap, err := handle.Observe(binding)
					if err != nil {
						t.Fatal(err)
					}
					if predicate(snap) {
						return snap
					}
					if time.Now().After(deadline) {
						t.Fatalf("foreground progress missing: %+v", snap)
					}
					time.Sleep(time.Millisecond)
				}
			}
			wait(func(s processhost.Snapshot) bool { return s.Turn == "" })
			generation := func(value string) {
				t.Helper()
				_, err := store.Update(func(reg *coremetadata.Registry) error {
					p, ok := reg.Pane(binding.Pane)
					if !ok {
						return processhost.ErrStale
					}
					p.Status.Activation.Generation = value
					if provider == "claude" {
						p.Status.Activation.Process.Binding.Generation = value
						p.Status.ProcessSession.Binding.Generation = value
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			base.Action = "turn"
			base.Operation = "registry-fence"
			base.Prompt = "normal"
			generation("replacement-generation")
			refused(base, host, identity)
			generation(binding.Generation)
			if result := exchange(base, host, identity); !result.Accepted {
				t.Fatalf("current Registry did not recover: %+v", result)
			}
			wait(func(s processhost.Snapshot) bool { return s.Turn == "" })
			prompts := []string{"controls"}
			if provider == "claude" {
				prompts = []string{"question", "permission"}
			}
			for _, prompt := range prompts {
				base.Action = "turn"
				base.Operation = "foreground-" + prompt
				base.Prompt = prompt
				if result := exchange(base, host, identity); !result.Accepted {
					t.Fatalf("control turn refused: %+v", result)
				}
				count := 1
				if provider == "codex" {
					count = 2
				}
				pending := wait(func(s processhost.Snapshot) bool { return len(s.Pending) == count }).Pending
				for _, token := range pending {
					base.Token = processForegroundToken{Request: token, Input: token.Input}
					if provider == "claude" {
						base.Action = "respond"
						base.Response = processhost.Response{Deny: "declined"}
						if token.Kind == "question" {
							base.Response = processhost.Response{Answers: map[string]string{"Color?": "blue"}}
						}
					} else if token.Kind == "question" {
						base.Action = "question"
						base.Selections = map[int]agentquestion.Selection{0: {Labels: []string{"blue"}}}
					} else {
						base.Action = "approval"
						base.Decision = codexappserver.DecisionDecline
					}
					bad := base
					bad.Token.Connection = "old"
					refused(bad, host, identity)
					if result := exchange(base, host, identity); !result.Accepted {
						t.Fatalf("exact response refused: %+v", result)
					}
					refused(base, host, identity)
				}
				wait(func(s processhost.Snapshot) bool { return s.Turn == "" })
			}
			base.Action = "stop"
			if result := exchange(base, host, identity); !result.Accepted {
				t.Fatalf("exact stop refused: %+v", result)
			}
		})
	}
}

func TestClaudeProcessHostLeaseBusyIsRetryableWithoutAdoption(t *testing.T) {
	socket := filepath.Join(shortTempDomain(t), "lease", "host.sock")
	_, closeLease, err := listenProcessHost(socket)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = listenProcessHost(socket); !errors.Is(err, processhost.ErrBusy) || !errors.Is(err, os.ErrExist) {
		t.Fatalf("missing explicit busy: %v", err)
	}
	if err = closeLease(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, closeNext, err := listenProcessHost(socket)
	if err != nil {
		t.Fatal(err)
	}
	if err = closeNext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeProcessForegroundRejectsForeignKernelUser(t *testing.T) {
	peer, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	peer.OwnerUID++
	writes := 0
	result := controlProcessForeground(context.Background(), peer, processForegroundRequest{}, func(context.Context, processhost.Authority) error {
		t.Fatal("foreign user reached authority check")
		return nil
	}, func() error { writes++; return nil })
	if !result.Stale || result.Accepted || writes != 0 {
		t.Fatalf("foreign user wrote: %+v %d", result, writes)
	}
}
