package app

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func peerSourceRequest(e *codexProcessEndpoint) codexProcessExchange {
	return codexProcessExchange{Foreground: &processForegroundRequest{Action: "validate", Authority: e.authority()}}
}

func TestCodexPeerSourceLiveValidateAndClosedShape(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	e := f.endpoint
	before := len(f.pollWire(t))
	requests := map[string]codexProcessExchange{"validate": peerSourceRequest(e)}
	for _, action := range codexForegroundActions {
		if action != "validate" {
			r := peerSourceRequest(e)
			r.Foreground.Action = action
			requests[action] = r
		}
	}
	r := peerSourceRequest(e)
	r.MessageRef = "message-mixed"
	requests["mixed-message"] = r
	r = peerSourceRequest(e)
	r.Questions = &processQuestionRequest{Binding: e.binding, QuestionID: "question-mixed"}
	requests["mixed-question"] = r
	r = peerSourceRequest(e)
	r.Observe = &e.binding
	requests["mixed-observe"] = r
	r = peerSourceRequest(e)
	r.Foreground.Prompt = "arbitrary"
	requests["prompt"] = r
	r = peerSourceRequest(e)
	r.Foreground.Authority.Connection = "replaced"
	requests["connection"] = r
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for name, request := range requests {
		t.Run(name, func(t *testing.T) {
			var result processForegroundResult
			err := localipc.ExchangeOwnedJSON(ctx, e.peerSource.listener.Path, e.peerSource.listener.Identity(), e.evidence.HostProcess, request, &result)
			if err != nil || result.Accepted != (name == "validate") {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
	var result processForegroundResult
	raw, _ := json.Marshal(peerSourceRequest(e))
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	fields["UnknownMutation"] = "stop"
	if err := localipc.ExchangeOwnedJSON(ctx, e.peerSource.listener.Path, e.peerSource.listener.Identity(), e.evidence.HostProcess, fields, &result); err != nil || result.Accepted {
		t.Fatalf("unknown field: %+v %v", result, err)
	}
	if len(f.pollWire(t)) != before {
		t.Fatal("read-only source wrote provider input")
	}
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePeerProcessCodexRoute(ctx, f.path, reg, e.binding.Agent); err != nil {
		t.Fatal(err)
	}
}

func TestCodexPeerSourceRetirementBeforePublicationNeverStarts(t *testing.T) {
	root := codexHostMoveFixtureShortState(t, shortTempDomain(t))
	s, err := newCodexPeerSource(filepath.Join(root, "peer.sock"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.start(ctx, &codexProcessEndpoint{}); !errors.Is(err, processhost.ErrClosed) {
		t.Fatalf("late start: %v", err)
	}
	if s.started {
		t.Fatal("retired initializer resurrected loop")
	}
	select {
	case <-s.done:
		t.Fatal("never-started loop fabricated join")
	default:
	}
	if _, err := os.Lstat(s.listener.Path); !os.IsNotExist(err) {
		t.Fatalf("owned socket remains: %v", err)
	}
}

func TestCodexPeerSourceCloseTerminatesStalledConnectionAndJoins(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	s := f.endpoint.peerSource
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: s.listener.Path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		active := s.active != nil
		s.mu.Unlock()
		if active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stalled client never accepted")
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := f.endpoint.handle.Stop(f.endpoint.binding); err != nil {
		t.Fatal(err)
	}
	wait, err := f.endpoint.handle.Wait(ctx, f.endpoint.binding)
	if err != nil || wait.Exit == nil {
		t.Fatalf("stalled connection actual Wait: %+v %v", wait, err)
	}
	select {
	case <-s.done:
	default:
		t.Fatal("close returned before actual join")
	}
	if _, err := os.Lstat(filepath.Dir(f.endpoint.socket)); !os.IsNotExist(err) {
		t.Fatalf("lease remains: %v", err)
	}
}

func TestCodexPeerSourceConnectedReplacementRefusesAndPreservesInode(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	claimant := secondPeerSourceFixture(t, f)
	e := f.endpoint
	s := e.peerSource
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: s.listener.Path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		active := s.active != nil
		s.mu.Unlock()
		if active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("client never accepted")
		}
		time.Sleep(time.Millisecond)
	}
	if err := os.Remove(s.listener.Path); err != nil {
		t.Fatal(err)
	}
	replacement, err := localipc.Listen(s.listener.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	replacementDone := make(chan struct{})
	go func() {
		defer close(replacementDone)
		conn, err := replacement.Unix.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		var request codexProcessExchange
		if localipc.ReadJSON(conn, &request) == nil {
			_ = localipc.WriteJSON(conn, processForegroundResult{Stale: true})
		}
	}()
	t.Cleanup(func() { _ = replacement.Close(); <-replacementDone })
	before := len(f.pollWire(t))
	if err := localipc.WriteJSON(conn, peerSourceRequest(e)); err != nil {
		t.Fatal(err)
	}
	_ = conn.CloseWrite()
	var result processForegroundResult
	if err := localipc.ReadJSON(conn, &result); err != nil || result.Accepted {
		t.Fatalf("already connected replacement: %+v %v", result, err)
	}
	if len(f.pollWire(t)) != before {
		t.Fatal("replacement claimant caused provider write")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	from, err := processCodexMessageRoute(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	to, err := processCodexMessageRoute(ctx, claimant.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	store := messagestore.NewStore(filepath.Join(f.root, "replacement-claimants"))
	m := &codexProcessMessages{store: store, endpoints: map[string]*codexProcessEndpoint{claimant.endpoint.binding.Agent: claimant.endpoint}}
	claimant.endpoint.messages.Store(m)
	now := time.Now().UTC()
	env := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-replaced-source", ConversationRef: "conversation-replaced-source", Source: from, Target: to, Authority: coremessage.PeerAuthority(), Payload: "must not claim a provider write", AcceptedAt: now, Deadline: now.Add(time.Minute)}
	if _, _, err := store.PutAccepted(env, "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	claimantBefore := len(claimant.pollWire(t))
	claim, err := claimant.endpoint.call(ctx, env.MessageRef)
	if err != nil || claim.Receipt == nil || claim.Receipt.Delivery.State != coremessage.StateStale || claim.Receipt.Delivery.OutcomeUnknown {
		t.Fatalf("replaced source claimant: %+v %v", claim, err)
	}
	if len(claimant.pollWire(t)) != claimantBefore {
		t.Fatal("replaced source caused target provider write")
	}
	if err := e.handle.Stop(e.binding); err != nil {
		t.Fatal(err)
	}
	wait, err := e.handle.Wait(ctx, e.binding)
	if err != nil || wait.Exit == nil {
		t.Fatalf("replacement actual Wait: %+v %v", wait, err)
	}
	if err := e.close(ctx); !errors.Is(err, localipc.ErrSocketReplaced) {
		t.Fatalf("replacement cleanup: %v", err)
	}
	identity, err := localipc.InspectOwnedSocket(replacement.Path)
	if err != nil || identity != replacement.Identity() {
		t.Fatalf("replacement removed: %v", err)
	}
	// Only the test owns the replacement. Retire it and the retained old main
	// lease explicitly; production reports incomplete cleanup rather than this.
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.closeLease(ctx); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Dir(e.socket)); !os.IsNotExist(err) {
		t.Fatalf("owned lease cleanup incomplete: %v", err)
	}
}

// Two independent provider owners use one real Registry, as in production.
// Each receiver knows only itself, forcing remote source validation.
func secondPeerSourceFixture(t *testing.T, first *processCodexFixture, exitAfterReady ...bool) *processCodexFixture {
	t.Helper()
	e := first.endpoint
	b := e.binding
	b.Agent += "-peer"
	b.Pane += "-peer"
	b.Generation += "-peer"
	b.Operation += "-peer"
	b.Host += "-peer"
	_, err := first.store.Update(func(reg *coremetadata.Registry) error {
		a, _ := reg.Agent(e.binding.Agent)
		p, _ := reg.Pane(e.binding.Pane)
		agent, pane := a.Clone(), p.Clone()
		agent.Metadata.UID = b.Agent
		agent.Metadata.Name += "-peer"
		agent.Status.PaneRef = b.Pane
		agent.Status.SessionRef = nil
		agent.Status.Activation = coremetadata.AgentActivation{}
		agent.Status.Phase = coremetadata.PhasePending
		pane.Metadata.UID = b.Pane
		pane.Metadata.Name += "-peer"
		pane.Metadata.OwnerRef.UID = b.Agent
		pane.Status.Activation = coremetadata.PaneActivation{}
		pane.Status.ProcessSession = &coremetadata.ProcessSessionRecord{Provider: aiModeCodex, Binding: coremetadata.ProcessBinding{HostInstanceID: b.Host, ProjectUID: b.Project, WindowUID: b.Window, AgentUID: b.Agent, PaneUID: b.Pane, Generation: b.Generation, OperationID: b.Operation}, ResumeState: coremetadata.ProcessResumeUnknown}
		reg.Agents = append(reg.Agents, agent)
		reg.Panes = append(reg.Panes, pane)
		reg.NameReservations = append(reg.NameReservations,
			coremetadata.NameReservation{Scope: b.Project, Kind: coremetadata.KindAgent, Name: agent.Metadata.Name, UID: b.Agent},
			coremetadata.NameReservation{Scope: b.Project, Kind: coremetadata.KindPane, Name: pane.Metadata.Name, UID: b.Pane})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	current := func(ctx context.Context, got processhost.Binding) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		reg, err := first.store.LoadDegradedReadOnly()
		if err != nil {
			return err
		}
		p, ok := reg.Pane(b.Pane)
		if got != b || !ok || p.Status.ProcessSession == nil || processSchemaBinding(p.Status.ProcessSession.Binding) != b {
			return processhost.ErrStale
		}
		return nil
	}
	root := filepath.Join(first.root, "peer")
	if err := os.MkdirAll(filepath.Join(root, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if key == "HOME" || key == "CODEX_HOME" || key == "TMUX" || key == "TMUX_PANE" || strings.HasPrefix(key, "PROJMUX") || strings.HasPrefix(key, "__PROJMUX") || strings.HasPrefix(key, "PMX_") || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "OPENAI_") || strings.HasPrefix(key, "ANTHROPIC_") || key == "CODEX_API_KEY" {
			continue
		}
		env = append(env, v)
	}
	env = append(env, "HOME="+root, "CODEX_HOME="+filepath.Join(root, ".codex"), "XDG_STATE_HOME="+filepath.Join(root, "state"), "PMX_TEST_PROCESS_CODEX_CHILD=1")
	limits := processhost.DefaultLimits()
	limits.Grace = 200 * time.Millisecond
	host, err := processhost.NewHost(b.Host, processhost.Command{Path: first.binary, Args: []string{"internal", "codex-process-test-supervisor"}, Env: env}, processhost.Transactions{Reserve: current, Current: current, Commit: func(ctx context.Context, b processhost.Binding, _ string) error { return current(ctx, b) }}, limits)
	if err != nil {
		t.Fatal(err)
	}
	cfg := processhost.CodexConfig{Version: "0.1.0", Settings: codexappserver.ThreadSettings{Model: "stub-model", Effort: "low", Policy: codexappserver.ThreadPolicy{Sandbox: "read-only", ApprovalPolicy: "on-request"}}}
	command := processCodexSteerFixture("reply({})")(root, first.binary, env)
	earlyExit := len(exitAfterReady) == 1 && exitAfterReady[0]
	if earlyExit {
		command.Args = []string{"-u", "-c", strings.Replace(processCodexProviderFixture, " elif method=='turn/start':", "  os._exit(0)\n elif method=='turn/start':", 1)}
	}
	endpoint, err := startProcessCodex(context.Background(), host, processhost.Launch{Binding: b, Command: command}, cfg, first.path, nil)
	if endpoint == nil || (err != nil && !earlyExit) {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if endpoint.handle != nil {
			_ = endpoint.handle.Stop(b)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s, err := endpoint.handle.Wait(ctx, b)
			if err != nil || s.Exit == nil {
				t.Errorf("second actual Wait: %+v %v", s, err)
			}
		}
		if _, err := os.Lstat(filepath.Dir(endpoint.socket)); !os.IsNotExist(err) {
			t.Errorf("second lease remains: %v", err)
		}
	})
	return &processCodexFixture{root: root, path: first.path, binary: first.binary, endpoint: endpoint, store: first.store}
}

func TestCodexPeerSourceProviderExitAtReadyCannotResurrectPublication(t *testing.T) {
	first := newProcessCodexFixture(t, nil)
	f := secondPeerSourceFixture(t, first, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if f.endpoint.handle != nil {
		s, err := f.endpoint.handle.Wait(ctx, f.endpoint.binding)
		if err != nil || s.Exit == nil {
			t.Fatalf("ready-exit actual Wait: %+v %v", s, err)
		}
	} else {
		t.Log("provider startup failed before publishing a handle; no caller Wait receipt asserted")
	}
	s := f.endpoint.peerSource
	s.mu.Lock()
	retired, started := s.retired, s.started
	s.mu.Unlock()
	if !retired {
		t.Fatal("completion failed to retire pre-birth auxiliary ownership")
	}
	if err := s.start(ctx, f.endpoint); !errors.Is(err, processhost.ErrClosed) {
		t.Fatalf("late publication after provider exit: %v", err)
	}
	select {
	case <-s.done:
		if !started {
			t.Fatal("never-started loop fabricated join")
		}
	default:
		if started {
			t.Fatal("actual started loop was not joined")
		}
	}
	if _, err := os.Lstat(filepath.Dir(f.endpoint.socket)); !os.IsNotExist(err) {
		t.Fatalf("ready-exit lease remains: %v", err)
	}
}

func TestCodexPeerSourceReciprocalProcessAdmissionsComplete(t *testing.T) {
	left := newProcessCodexFixture(t, processCodexSteerFixture("reply({})"))
	right := secondPeerSourceFixture(t, left)
	left.turn(t, "original-left", "hold")
	right.turn(t, "original-right", "hold")
	ls := left.wait(t, func(s processhost.Snapshot) bool { return s.Turn != "" })
	rs := right.wait(t, func(s processhost.Snapshot) bool { return s.Turn != "" })
	store := messagestore.NewStore(filepath.Join(left.root, "peer-messages"))
	lm := &codexProcessMessages{store: store, endpoints: map[string]*codexProcessEndpoint{left.endpoint.binding.Agent: left.endpoint}}
	rm := &codexProcessMessages{store: store, endpoints: map[string]*codexProcessEndpoint{right.endpoint.binding.Agent: right.endpoint}}
	left.endpoint.messages.Store(lm)
	right.endpoint.messages.Store(rm)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	from, err := processCodexMessageRoute(ctx, left.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	to, err := processCodexMessageRoute(ctx, right.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	records := []coremessage.Envelope{{Version: coremessage.Version, MessageRef: "message-cross-left", ConversationRef: "conversation-cross", Source: from, Target: to, Authority: coremessage.PeerAuthority(), Payload: "left peer", AcceptedAt: now, Deadline: now.Add(time.Minute)}, {Version: coremessage.Version, MessageRef: "message-cross-right", ConversationRef: "conversation-cross", Source: to, Target: from, Authority: coremessage.PeerAuthority(), Payload: "right peer", AcceptedAt: now, Deadline: now.Add(time.Minute)}}
	for _, env := range records {
		if _, _, err := store.PutAccepted(env, "codex-inbox"); err != nil {
			t.Fatal(err)
		}
	}
	// Keep both serial main servers inside peer receive before releasing their
	// admission locks; the source services must remain independently available.
	lm.mu.Lock()
	rm.mu.Lock()
	released := false
	release := func() {
		if !released {
			released = true
			lm.mu.Unlock()
			rm.mu.Unlock()
		}
	}
	defer release()
	results := make(chan error, 2)
	for i, e := range []*codexProcessEndpoint{right.endpoint, left.endpoint} {
		conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: e.socket, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		request := codexProcessExchange{Foreground: &processForegroundRequest{Action: "peer-message", Authority: e.authority(), MessageRef: records[i].MessageRef}}
		if err := localipc.WriteJSON(conn, request); err != nil {
			t.Fatal(err)
		}
		_ = conn.CloseWrite()
		go func() {
			defer conn.Close()
			var result processForegroundResult
			err := localipc.ReadJSON(conn, &result)
			if err == nil && (result.Receipt == nil || result.Receipt.Delivery.State != coremessage.StateDelivered) {
				err = errors.New("peer was not delivered")
			}
			results <- err
		}()
	}
	// Both peer connections precede these main validate connections. Main
	// validate must remain queued while read-only auxiliary validate succeeds.
	for _, e := range []*codexProcessEndpoint{left.endpoint, right.endpoint} {
		short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		_, err := callLiveProcessCodex(short, e.registryPath, e.evidence, *peerSourceRequest(e).Foreground)
		cancel()
		if err == nil {
			t.Fatal("main server was not occupied by peer admission")
		}
		reg, err := left.store.LoadDegradedReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := resolvePeerProcessCodexRoute(ctx, e.registryPath, reg, e.binding.Agent); err != nil {
			t.Fatal(err)
		}
	}
	release()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	for i, f := range []*processCodexFixture{left, right} {
		s, _ := f.endpoint.handle.Observe(f.endpoint.binding)
		want := ls.Turn
		if i == 1 {
			want = rs.Turn
		}
		if s.Turn != want {
			t.Fatal("peer changed original turn")
		}
		starts, injects := processCodexTurnWrites(t, f)
		if starts != 1 || len(injects) != 1 {
			t.Fatalf("wire start=%d inject=%d", starts, len(injects))
		}
	}
}

func TestCodexPeerSourceRemoteReplacementAndOldCapabilityWriteZero(t *testing.T) {
	for _, name := range []string{"source-generation", "target-generation", "old-source", "cancel"} {
		t.Run(name, func(t *testing.T) {
			left := newProcessCodexFixture(t, nil)
			right := secondPeerSourceFixture(t, left)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			from, err := processCodexMessageRoute(ctx, left.endpoint)
			if err != nil {
				t.Fatal(err)
			}
			to, err := processCodexMessageRoute(ctx, right.endpoint)
			if err != nil {
				t.Fatal(err)
			}
			store := messagestore.NewStore(filepath.Join(left.root, "negative-messages"))
			m := &codexProcessMessages{store: store, endpoints: map[string]*codexProcessEndpoint{right.endpoint.binding.Agent: right.endpoint}}
			right.endpoint.messages.Store(m)
			now := time.Now().UTC()
			env := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-source-negative", ConversationRef: "conversation-negative", Source: from, Target: to, Authority: coremessage.PeerAuthority(), Payload: "must not be written", AcceptedAt: now, Deadline: now.Add(time.Minute)}
			if _, _, err := store.PutAccepted(env, "codex-inbox"); err != nil {
				t.Fatal(err)
			}
			original, err := left.store.LoadDegradedReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := left.store.Update(func(reg *coremetadata.Registry) error { *reg = original.Clone(); return nil }); err != nil {
					t.Error(err)
				}
			}()
			if name == "source-generation" || name == "target-generation" {
				uid := left.endpoint.binding.Pane
				if name == "target-generation" {
					uid = right.endpoint.binding.Pane
				}
				if _, err := left.store.Update(func(reg *coremetadata.Registry) error {
					p, _ := reg.Pane(uid)
					p.Status.Activation.Generation = "changed-generation"
					p.Status.Activation.Process.Binding.Generation = "changed-generation"
					p.Status.ProcessSession.Binding.Generation = "changed-generation"
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if name == "old-source" {
				if err := left.endpoint.peerSource.close(ctx); err != nil {
					t.Fatal(err)
				}
				// The existing main read-only endpoint is still valid. Peer
				// source admission must refuse rather than fall back to it.
				result, err := callLiveProcessCodex(ctx, left.path, left.endpoint.evidence, *peerSourceRequest(left.endpoint).Foreground)
				if err != nil || !result.Accepted {
					t.Fatalf("old main validation: %+v %v", result, err)
				}
			}
			before := len(right.pollWire(t))
			if name == "cancel" {
				cancelled, stop := context.WithCancel(ctx)
				stop()
				if _, err := resolveCodexPeerSourceRoute(cancelled, left.path, original, left.endpoint.binding.Agent); !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
			} else {
				result, err := right.endpoint.call(ctx, env.MessageRef)
				if name == "target-generation" {
					if !errors.Is(err, processhost.ErrStale) || result.Valid || result.Receipt != nil {
						t.Fatalf("target rejected before receive: %+v %v", result, err)
					}
				} else if err != nil || result.Receipt == nil || result.Receipt.Delivery.State != coremessage.StateStale || result.Receipt.Delivery.OutcomeUnknown {
					t.Fatalf("negative: %+v %v", result, err)
				}
			}
			if len(right.pollWire(t)) != before {
				t.Fatal("stale source/target or absent capability wrote provider input")
			}
		})
	}
}

func TestCodexPeerSourceStartupFailureRetiresPreBirthOwnership(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	b := f.endpoint.binding
	b.Host += "-failed"
	b.Generation += "-failed"
	b.Operation += "-failed"
	current := func(context.Context, processhost.Binding) error { return nil }
	env := []string{"HOME=" + f.root, "CODEX_HOME=" + filepath.Join(f.root, ".codex"), "PMX_TEST_PROCESS_CODEX_CHILD=1"}
	host, err := processhost.NewHost(b.Host, processhost.Command{Path: f.binary, Args: []string{"internal", "codex-process-test-supervisor"}, Env: env}, processhost.Transactions{Reserve: current, Current: current, Commit: func(ctx context.Context, b processhost.Binding, _ string) error { return current(ctx, b) }}, processhost.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	cfg := processhost.CodexConfig{Version: "0.1.0", Settings: codexappserver.ThreadSettings{Model: "stub-model", Policy: codexappserver.ThreadPolicy{Sandbox: "read-only", ApprovalPolicy: "on-request"}}}
	endpoint, err := startProcessCodex(context.Background(), host, processhost.Launch{Binding: b, Command: processhost.Command{Path: filepath.Join(f.root, "absent-provider"), Env: env}}, cfg, f.path, nil)
	if err == nil || endpoint == nil {
		t.Fatalf("startup failure endpoint=%v err=%v", endpoint, err)
	}
	if !endpoint.peerSource.retired || endpoint.peerSource.started {
		t.Fatal("failed startup published auxiliary service")
	}
	select {
	case <-endpoint.peerSource.done:
		t.Fatal("never-started startup fabricated join")
	default:
	}
	if _, err := os.Lstat(filepath.Dir(endpoint.socket)); !os.IsNotExist(err) {
		t.Fatalf("startup lease remains: %v", err)
	}
	if endpoint.handle != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s, err := endpoint.handle.Wait(ctx, b)
		if err != nil || s.PID != 0 || s.Exit != nil || !strings.Contains(s.Failure, "absent-provider") {
			t.Fatalf("pre-birth startup completion: %+v %v", s, err)
		}
	}
}

func appendPeerFixtureRegistry(t *testing.T, dst *coremetadata.Registry, source coremetadata.Registry) {
	t.Helper()
	reserve := func(kind coremetadata.Kind, meta coremetadata.ObjectMeta) {
		scope := ""
		if kind != coremetadata.KindProject && kind != coremetadata.KindControlSession {
			owner := meta.OwnerUID()
			for {
				if _, ok := dst.Project(owner); ok {
					scope = owner
					break
				}
				if _, ok := dst.ControlSession(owner); ok {
					scope = owner
					break
				}
				if window, ok := dst.Window(owner); ok {
					owner = window.Metadata.OwnerUID()
					continue
				}
				if agent, ok := dst.Agent(owner); ok {
					owner = agent.Metadata.OwnerUID()
					continue
				}
				t.Fatalf("fixture %s %s has no root owner: %s", kind, meta.UID, owner)
			}
		}
		dst.NameReservations = append(dst.NameReservations, coremetadata.NameReservation{Scope: scope, Kind: kind, Name: meta.Name, UID: meta.UID})
	}
	for _, v := range source.Projects {
		if _, ok := dst.Project(v.Metadata.UID); !ok {
			dst.Projects = append(dst.Projects, v.Clone())
			reserve(coremetadata.KindProject, v.Metadata)
		}
	}
	for _, v := range source.Windows {
		if _, ok := dst.Window(v.Metadata.UID); !ok {
			dst.Windows = append(dst.Windows, v.Clone())
			reserve(coremetadata.KindWindow, v.Metadata)
		}
	}
	for _, v := range source.Agents {
		if _, ok := dst.Agent(v.Metadata.UID); !ok {
			dst.Agents = append(dst.Agents, v.Clone())
			reserve(coremetadata.KindAgent, v.Metadata)
		}
	}
	for _, v := range source.Panes {
		if _, ok := dst.Pane(v.Metadata.UID); !ok {
			dst.Panes = append(dst.Panes, v.Clone())
			reserve(coremetadata.KindPane, v.Metadata)
		}
	}
}

func TestCodexPeerSourceProcessAndTmuxBothDirectionsPreserveTurns(t *testing.T) {
	process := newProcessCodexFixture(t, processCodexSteerFixture("reply({})"))
	f, epoch, wire := peerEpochFixture(t, true)
	if _, err := process.store.Update(func(reg *coremetadata.Registry) error { appendPeerFixtureRegistry(t, reg, f.registry); return nil }); err != nil {
		t.Fatal(err)
	}
	reg, err := process.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	f.registry = reg.Clone()
	configureCodexPeer(epoch, filepath.Dir(filepath.Dir(f.store.Path())), func() (coremetadata.Registry, error) { return f.registry.Clone(), nil }, process.path)
	process.turn(t, "original-process", "hold")
	snapshot := process.wait(t, func(s processhost.Snapshot) bool { return s.Turn != "" })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	processRoute, err := processCodexMessageRoute(ctx, process.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	intoTmux := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-process-to-tmux", ConversationRef: "conversation-both-runtimes", Source: processRoute, Target: f.route, Authority: coremessage.PeerAuthority(), Payload: "process peer context", AcceptedAt: now, Deadline: now.Add(time.Minute)}
	if _, _, err := f.store.PutAccepted(intoTmux, "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	m := &codexProcessMessages{store: f.store, endpoints: map[string]*codexProcessEndpoint{process.endpoint.binding.Agent: process.endpoint}}
	process.endpoint.messages.Store(m)
	intoProcess := coremessage.Envelope{Version: coremessage.Version, MessageRef: "message-tmux-to-process", ConversationRef: intoTmux.ConversationRef, Source: f.route, Target: processRoute, Authority: coremessage.PeerAuthority(), Payload: "tmux peer context", AcceptedAt: now, Deadline: now.Add(time.Minute)}
	if _, _, err := f.store.PutAccepted(intoProcess, "codex-inbox"); err != nil {
		t.Fatal(err)
	}
	// A reverse process delivery must not recurse through the source epoch's
	// status socket, even while that tmux epoch is locked.
	epoch.mu.Lock()
	result, err := process.endpoint.call(ctx, intoProcess.MessageRef)
	epoch.mu.Unlock()
	if err != nil || result.Receipt == nil || result.Receipt.Delivery.State != coremessage.StateDelivered {
		t.Fatalf("tmux->process: %+v %v", result, err)
	}
	if result := epoch.Handle(ctx, peerRequest(intoTmux.MessageRef)); !result.OK {
		t.Fatalf("process->tmux: %+v", result)
	}
	if wire.start != 0 || wire.steer != 0 || wire.injected != 1 {
		t.Fatalf("tmux wire: %+v", wire)
	}
	after, err := process.endpoint.handle.Observe(process.endpoint.binding)
	if err != nil || after.Turn != snapshot.Turn {
		t.Fatalf("original process turn changed: %+v %v", after, err)
	}
	starts, injects := processCodexTurnWrites(t, process)
	if starts != 1 || len(injects) != 1 {
		t.Fatalf("process start=%d inject=%d", starts, len(injects))
	}
}

func TestCodexPeerSourceClaudeUsesActualRegistrationAndLease(t *testing.T) {
	f := newProcessClaudeFixture(t, nil)
	proof := f.proof(t)
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	expected, reason := processClaudeRouteResolver(f.path, proof)(reg, f.binding.Agent)
	if reason != "" {
		t.Fatal(reason)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	authority := expected.Authority().(coremetadata.ClaudeAuthorityRef)
	leaseSocket := claudeadapter.LeaseSocket(f.path, expected.PaneUID, expected.Generation, authority.RegistrationGeneration)
	witness := func(stage string, observedErr error, attempts int) {
		t.Helper()
		observedAt := time.Now().UTC()
		current, registryErr := f.store.LoadDegradedReadOnly()
		generation := ""
		if pane, ok := current.Pane(f.binding.Pane); ok && pane.Status.ProcessSession != nil {
			generation = pane.Status.ProcessSession.Binding.Generation
		}
		identity, socketErr := claudeadapter.InspectSocket(leaseSocket)
		t.Logf("post-observation stage=%s at=%s attempts=%d expected_registration=%s current_generation=%s registry_err=%v lease_identity=%+v socket_err=%v caller_deadline=%s caller_err=%v observed_err=%v",
			stage, observedAt.Format(time.RFC3339Nano), attempts, authority.RegistrationGeneration, generation, registryErr, identity, socketErr, deadline.Format(time.RFC3339Nano), ctx.Err(), observedErr)
	}
	// Registration publication precedes serving the lease. Prepare the actual
	// positive lease premise synchronously within this same caller deadline.
	attempts := 0
	for {
		attempts++
		err = validatePeerClaudeLease(ctx, f.path, expected)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			witness("lease preparation failed", err, attempts)
			t.Fatalf("Claude lease preparation: %v", err)
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			witness("lease preparation deadline", err, attempts)
			t.Fatalf("Claude lease preparation: %v (caller: %v)", err, ctx.Err())
		case <-timer.C:
		}
	}
	witness("lease prepared", nil, attempts)
	// The resolver itself has one attempt; remaining caller budget is shared.
	route, err := resolveCodexPeerSourceRoute(ctx, f.path, reg, f.binding.Agent)
	if err != nil || !route.Same(expected) {
		witness("resolver failed", err, attempts)
		t.Fatalf("actual Claude source: %+v %v", route, err)
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, err := resolveCodexPeerSourceRoute(cancelled, f.path, reg, f.binding.Agent); !errors.Is(err, context.Canceled) {
		t.Fatalf("Claude caller cancellation: %v", err)
	}
}

func TestCodexPeerSourceTmuxClaudeLeaseReadinessAndProbe(t *testing.T) {
	for _, mode := range []string{"ready", "readiness-zero", "lease-stale", "probe-stale", "caller-budget", "readiness-budget", "probe-budget"} {
		t.Run(mode, func(t *testing.T) {
			h := newSessionRefHarness(t, aiModeClaude)
			process, _, err := localipc.Process(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			a, _ := h.registry.Agent(h.agentUID)
			p, _ := h.registry.Pane(h.paneUID)
			a.Status.Phase = coremetadata.PhaseRunning
			authority := coremetadata.ClaudeAuthorityRef{SessionID: "session-peer-source", RegistrationGeneration: "registration-peer-source", Process: process, LeaseProcess: process}
			if mode == "lease-stale" {
				authority.LeaseProcess.Start = "stale-birth"
			}
			registration := coremetadata.ClaudeRegistration{Authority: authority, Ready: true}
			p.Status.Activation = coremetadata.PaneActivation{AgentUID: h.agentUID, Generation: "generation-peer-source", RuntimeID: "%40", Claude: &coremetadata.ClaudeActivationBinding{Process: process, RegistrationSessionID: authority.SessionID, RegistrationGeneration: authority.RegistrationGeneration, Registration: &registration}}
			route, reason := coremetadata.ResolveAgentRoute(*h.registry, h.agentUID)
			if reason != "" {
				t.Fatal(reason)
			}
			path := intmetadata.PathFor(t.TempDir())
			target, ok := claudeadapter.TargetForRoute(route)
			if !ok {
				t.Fatal("missing target")
			}
			readiness, err := localipc.Listen(claudeadapter.LeaseSocket(path, route.PaneUID, route.Generation, authority.RegistrationGeneration))
			if err != nil {
				t.Fatal(err)
			}
			probe, err := localipc.Listen(claudeadapter.CoordinationSocket(path, target))
			if err != nil {
				_ = readiness.Close()
				t.Fatal(err)
			}
			release := make(chan struct{})
			readDone, probeDone := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(readDone)
				conn, err := readiness.Unix.AcceptUnix()
				if err != nil {
					return
				}
				defer conn.Close()
				if mode == "caller-budget" || mode == "readiness-budget" {
					<-release
				}
				value := byte(1)
				if mode == "readiness-zero" {
					value = 0
				}
				_, _ = conn.Write([]byte{value})
			}()
			go func() {
				defer close(probeDone)
				conn, err := probe.Unix.AcceptUnix()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				var request claudeCoordinationRequest
				if localipc.ReadJSON(conn, &request) != nil {
					return
				}
				if request.Operation != "probe" {
					t.Errorf("source requested non-probe operation %q", request.Operation)
					return
				}
				if mode == "probe-budget" {
					<-release
				}
				kind := "ready"
				if mode == "probe-stale" {
					kind = "stale"
				}
				_ = localipc.WriteJSON(conn, claudeCoordinationResponse{Version: claudeCoordinationVersion, Kind: kind})
			}()
			t.Cleanup(func() {
				close(release)
				_ = readiness.Close()
				_ = probe.Close()
				<-readDone
				<-probeDone
				if err := os.Remove(filepath.Dir(readiness.Path)); err != nil {
					t.Errorf("fixture lease cleanup: %v", err)
				}
			})
			budget := 2 * time.Second
			if mode == "caller-budget" {
				budget = 40 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			started := time.Now()
			got, err := resolveCodexPeerSourceRoute(ctx, path, *h.registry, h.agentUID)
			if mode == "ready" {
				if err != nil || !got.Same(route) {
					t.Fatalf("tmux Claude: %+v %v", got, err)
				}
			} else if err == nil {
				t.Fatal("invalid Claude source accepted")
			}
			if mode == "caller-budget" && time.Since(started) > 300*time.Millisecond {
				t.Fatal("readiness exceeded caller budget")
			}
			if (mode == "readiness-budget" || mode == "probe-budget") && time.Since(started) > time.Second {
				t.Fatal("stage exceeded existing shorter bound")
			}
		})
	}
}

func TestCodexPeerSourceAuthorityIsFreshAfterClaimPruning(t *testing.T) {
	f, epoch, wire := peerEpochFixture(t, true)
	record := acceptDistinctPeer(t, f, "message-source-after-prune")
	epoch.peerPrune = func(context.Context) error {
		p, _ := f.registry.Pane("pan-source-codex")
		p.Status.Activation.Generation = "replaced-during-prune"
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if result := epoch.Handle(ctx, peerRequest(record.Envelope.MessageRef)); result.OK || result.Code != "peer-invalid" {
		t.Fatalf("fresh source after pruning: %+v", result)
	}
	if wire.start+wire.steer+wire.injected != 0 || len(epoch.peerUsed) != 0 {
		t.Fatal("stale source consumed claim or provider input")
	}
}

func TestCodexPeerSourceClaudeLookupConnectionsShareCallerBudget(t *testing.T) {
	f := newProcessClaudeFixture(t, nil)
	proof := f.proof(t)
	registration, err := lookupClaudeProcessRegistration(proof)
	if err != nil {
		t.Fatal(err)
	}
	l, err := localipc.Listen(filepath.Join(f.root, "peer-lookup.sock"))
	if err != nil {
		t.Fatal(err)
	}
	proof.Socket, proof.HostSocket = l.Path, l.Identity()
	done := make(chan struct{})
	requests := make(chan claudeProcessCheck, 2)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		defer close(done)
		for i := range 2 {
			conn, err := l.Unix.AcceptUnix()
			if err != nil {
				return
			}
			var request claudeProcessCheck
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			if localipc.ReadJSON(conn, &request) != nil {
				_ = conn.Close()
				return
			}
			requests <- request
			if i == 1 {
				<-ctx.Done()
				time.Sleep(20 * time.Millisecond)
			}
			_ = localipc.WriteJSON(conn, claudeProcessCheckResult{Valid: true, Process: proof.Process, Binding: proof.Binding, Registration: registration})
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = l.Close(); <-done })
	started := time.Now()
	if _, err := lookupPeerClaudeRegistration(ctx, proof); err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	if _, err := lookupPeerClaudeRegistration(ctx, proof); err == nil {
		t.Fatal("second lookup refreshed the caller deadline")
	}
	if time.Since(started) > 1500*time.Millisecond {
		t.Fatal("multiple lookups exceeded one caller budget")
	}
	for range 2 {
		request := <-requests
		if request != (claudeProcessCheck{Binding: proof.Binding, Session: proof.Session, Lookup: true}) {
			t.Fatalf("lookup changed existing request shape: %+v", request)
		}
	}
}
