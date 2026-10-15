package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

func installCodexRegistry(t *testing.T) coremetadata.Registry {
	t.Helper()
	raw, err := os.ReadFile("../core/metadata/testdata/registry-v5-process.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var reg coremetadata.Registry
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	return reg
}

// Wire fixtures deliberately model capability declarations, not old/new images
// or model consumption. Nil, null and an explicit empty array are different.
func TestInstallCodexTargetWireEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, wire, want string }{
		{"new-target", `{"Protocol":1,"Actions":["peer-message"]}`, "compatible"},
		{"old-target", `{"Protocol":1,"Actions":["message","validate"]}`, "incompatible"},
		{"empty-array", `{"Protocol":1,"Actions":[]}`, "incompatible"},
		{"missing-array", `{"Protocol":1,"Revision":"` + testInstallOwnerRevision + `"}`, "unknown"},
		{"null-array", `{"Protocol":1,"Actions":null}`, "unknown"},
		{"protocol-zero", `{"Actions":["peer-message"]}`, "unknown"},
		{"future-protocol", `{"Protocol":2,"Actions":["peer-message"]}`, "unknown"},
		{"wrong-array-type", `{"Protocol":1,"Actions":"peer-message"}`, "unknown"},
		{"null-action-element", `{"Protocol":1,"Actions":[null,"peer-message"]}`, "unknown"},
		{"empty-action-element", `{"Protocol":1,"Actions":[""]}`, "unknown"},
		{"wrong-action-element-type", `{"Protocol":1,"Actions":[7,"peer-message"]}`, "unknown"},
		{"truncated", `{"Protocol":1,"Actions":[`, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var view processHostObservation
			err := json.Unmarshal([]byte(tc.wire), &view)
			got := classifyInstallCodexTarget(view, err == nil)
			if got.State != tc.want {
				t.Fatalf("%s: %+v, decode=%v", tc.name, got, err)
			}
			if got := classifyInstallCodexTarget(view, false); got.State != "unknown" {
				t.Fatalf("unverified view: %+v", got)
			}
		})
	}
}

func TestInstallPreflightCodexWarningsPreserveVerdicts(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"compatible", "unknown"} {
		for _, target := range []string{"compatible", "incompatible", "unknown"} {
			owner := installProcessOwner{AgentUID: "agent-exact", PaneUID: "pane-exact", Generation: "gen-exact", Provider: aiModeCodex,
				Source: installPeerSupport{State: source, Reason: "fixture-source"}, Target: installPeerSupport{State: target, Reason: "fixture-target"}}
			var out bytes.Buffer
			if err := installPreflightFixture(5, true, []installProcessOwner{owner}).Run(&out); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{">> install preflight: ", "uid:agent-exact pane uid:pane-exact generation gen-exact", "source " + source, "target " + target,
				"Both source and target", "Unknown/busy", "model consumption", "uncertain receipt"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q: %s", want, out.String())
				}
			}
			if source == "compatible" && target == "compatible" {
				if strings.Contains(out.String(), "projmux agent relaunch") || !strings.Contains(out.String(), "No host control/proof upgrade indicated") {
					t.Fatalf("unnecessary recovery: %s", out.String())
				}
			} else {
				for _, want := range []string{"Review the individual dry run with `projmux agent relaunch uid:agent-exact --detach --dry-run`", "explicitly recover with `projmux agent relaunch uid:agent-exact --detach`."} {
					if !strings.Contains(out.String(), want) {
						t.Fatalf("missing recovery %s", out.String())
					}
				}
			}
			for _, forbidden := range []string{"--yes", "--profile", "--model", "agent stop", "agent resume"} {
				if strings.Contains(out.String(), forbidden) {
					t.Fatalf("unexpected recovery %q", forbidden)
				}
			}
			out.Reset()
			if err := installPreflightFixture(4, true, []installProcessOwner{owner}).Run(&out); err == nil {
				t.Fatal("schema refusal lost")
			}
			if !strings.Contains(out.String(), "Codex peer uid:agent-exact") {
				t.Fatal("refusal hid warning")
			}
		}
	}
	command := installPreflightFixture(5, true, nil)
	command.registryVersion = func() (int, bool, error) { return 0, false, fmt.Errorf("fixture unreadable") }
	var out bytes.Buffer
	if err := command.Run(&out); err != nil || !strings.Contains(out.String(), "Codex peer source/target compatibility unknown") {
		t.Fatalf("unreadable: %v %s", err, out.String())
	}
	command = installPreflightFixture(5, true, nil)
	command.owners = func() ([]installProcessOwner, error) { return nil, fmt.Errorf("fixture census failure") }
	out.Reset()
	if err := command.Run(&out); err != nil || !strings.Contains(out.String(), "owner census unavailable") {
		t.Fatalf("census failure: %v %s", err, out.String())
	}
}

// One row budget covers both operations and their return/cleanup. The parent
// deadline is shared across the census, including every unobserved remainder.
func TestInstallCodexCensusSharedBudgetKeepsUnknownRemainder(t *testing.T) {
	reg := installCodexRegistry(t)
	base := reg.Panes[1]
	for i := range 30 {
		p, a := base, reg.Agents[0]
		p.Metadata.UID = fmt.Sprintf("pane-extra-%02d", i)
		a.Metadata.UID = fmt.Sprintf("agent-extra-%02d", i)
		p.Metadata.OwnerRef = &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: a.Metadata.UID}
		a.Status.PaneRef = p.Metadata.UID
		activation := *base.Status.Activation.Process
		activation.Binding.PaneUID = p.Metadata.UID
		activation.Binding.AgentUID = a.Metadata.UID
		p.Status.Activation.Process = &activation
		p.Status.Activation.AgentUID = a.Metadata.UID
		reg.Panes = append(reg.Panes, p)
		reg.Agents = append(reg.Agents, a)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	var hostDeadline time.Time
	calls, sourceCalls := 0, 0
	host := func(ctx context.Context, _ coremetadata.Registry, _ string, _ coremetadata.Pane) (processHostObservation, bool) {
		calls++
		hostDeadline, _ = ctx.Deadline()
		if time.Until(hostDeadline) > processObservationReadLimit {
			t.Error("row exceeds 250ms")
		}
		return processHostObservation{Protocol: 1, Actions: []string{"peer-message"}}, true
	}
	source := func(ctx context.Context, _ string, _ coremetadata.Registry, _ coremetadata.Pane) installPeerSupport {
		sourceCalls++
		deadline, _ := ctx.Deadline()
		if deadline != hostDeadline {
			t.Error("source reset row budget")
		}
		<-ctx.Done()
		return installPeerSupport{State: "unknown", Reason: "fixture-timeout"}
	}
	start := time.Now()
	owners := liveInstallProcessOwners(reg, func(coremetadata.ProcessIdentity) bool { return true }, newInstallProcessOwnerObserver(ctx, "unused", host, source))
	if len(owners) != 31 || calls != 1 || sourceCalls != 1 {
		t.Fatalf("owners=%d host=%d source=%d", len(owners), calls, sourceCalls)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("budget reset: %s", elapsed)
	}
	for _, owner := range owners[1:] {
		if owner.Source.State != "unknown" || owner.Target.State != "unknown" || owner.Source.Reason != "census-budget-exhausted" {
			t.Fatalf("remainder: %+v", owner)
		}
	}
}

func TestInstallCodexOwnerBudgetAndIndependentProofs(t *testing.T) {
	reg := installCodexRegistry(t)
	pane := reg.Panes[1]
	for _, tc := range []struct {
		name                           string
		protocol                       int
		actions                        []string
		observed                       bool
		source, wantSource, wantTarget string
	}{
		{"old-source-new-target", 1, []string{"peer-message"}, true, "unknown", "unknown", "compatible"},
		{"new-source-old-target", 1, []string{"validate"}, true, "compatible", "compatible", "incompatible"},
		{"both-new-hosts", 1, []string{"peer-message"}, true, "compatible", "compatible", "compatible"},
		{"unreachable-host", 1, []string{"peer-message"}, false, "compatible", "unknown", "unknown"},
		{"protocol-zero-host", 0, []string{"peer-message"}, true, "compatible", "unknown", "unknown"},
		{"missing-target-actions", 1, nil, true, "compatible", "compatible", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := func(ctx context.Context, _ coremetadata.Registry, _ string, _ coremetadata.Pane) (processHostObservation, bool) {
				d, _ := ctx.Deadline()
				if remaining := time.Until(d); remaining > processObservationReadLimit || remaining <= 0 {
					t.Errorf("row budget: %s", remaining)
				}
				return processHostObservation{Protocol: tc.protocol, Actions: tc.actions}, tc.observed
			}
			sourceCalls := 0
			source := func(context.Context, string, coremetadata.Registry, coremetadata.Pane) installPeerSupport {
				sourceCalls++
				return installPeerSupport{State: tc.source, Reason: "fixture-proof"}
			}
			read := newInstallProcessOwnerObserver(context.Background(), "unused", host, source)(reg, pane)
			if sourceCalls != 1 || read.Source.State != tc.wantSource || read.Target.State != tc.wantTarget {
				t.Fatalf("independent proofs: %+v calls=%d", read, sourceCalls)
			}
		})
	}
}

// Run a read-only protocol responder under this process's exact kernel birth.
// Requests are captured and checked; responses never enter a provider handle.
func serveInstallProtocolFixture(t *testing.T, socket string, reply func(*net.UnixConn)) {
	t.Helper()
	listener, err := localipc.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Unix.AcceptUnix()
			if err != nil {
				return
			}
			reply(conn)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("protocol responder did not retire")
		}
	})
}

func TestInstallCodexSourceResolverNegativeAndReplacement(t *testing.T) {
	for _, name := range []string{"accepted", "unsupported", "busy", "stale", "empty", "malformed", "timeout", "replaced-socket", "changed-activation", "missing-socket"} {
		t.Run(name, func(t *testing.T) {
			f := newProcessCodexPeerFixture(t, nil)
			snap, err := f.endpoint.handle.Observe(f.endpoint.binding)
			if err != nil {
				t.Fatal(err)
			}
			reg := installProcessObservationSchema(t, f.store, f.endpoint.binding, snap)
			pane, _ := reg.Pane(f.endpoint.binding.Pane)
			prepareCtx, prepareCancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = f.endpoint.peerSource.close(prepareCtx)
			prepareCancel()
			if err != nil {
				t.Fatal(err)
			}
			beforeWire := f.pollWire(t)
			beforeRegistry, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			readContexts := make(chan context.Context, 1)
			socket := processCodexPeerSourceSocket(f.path, pane.Metadata.UID, pane.Status.Activation.Generation)
			if name != "missing-socket" {
				serveInstallProtocolFixture(t, socket, func(conn *net.UnixConn) {
					_ = conn.SetDeadline(time.Now().Add(time.Second))
					var request codexProcessExchange
					if err := localipc.ReadJSON(conn, &request); err != nil {
						t.Error(err)
						return
					}
					requests.Add(1)
					if !reflect.DeepEqual(request, peerSourceRequest(f.endpoint)) {
						t.Errorf("not closed read-only validate: %+v", request)
						return
					}
					switch name {
					case "malformed":
						_, _ = conn.Write([]byte("{\n"))
						return
					case "timeout":
						readCtx := <-readContexts
						<-readCtx.Done()
						return
					case "replaced-socket":
						if err := os.Remove(socket); err != nil {
							t.Error(err)
							return
						}
						l, err := localipc.Listen(socket)
						if err != nil {
							t.Error(err)
							return
						}
						defer l.Close()
					case "changed-activation":
						if _, err := f.store.Update(func(r *coremetadata.Registry) error {
							p, _ := r.Pane(pane.Metadata.UID)
							p.Status.Activation = coremetadata.PaneActivation{}
							return nil
						}); err != nil {
							t.Error(err)
							return
						}
					}
					result := processForegroundResult{}
					switch name {
					case "accepted", "replaced-socket", "changed-activation":
						result.Accepted = true
					case "unsupported":
						result.Unsupported = true
					case "busy":
						result.Busy = true
					case "stale":
						result.Stale = true
					}
					_ = localipc.WriteJSON(conn, result)
				})
			}
			// Fixture preparation does not consume the actual observation budget.
			ctx, cancel := context.WithTimeout(context.Background(), processObservationReadLimit)
			defer cancel()
			readContexts <- ctx
			got := readInstallCodexSource(ctx, f.path, reg, *pane)
			want := "unknown"
			if name == "accepted" {
				want = "compatible"
			}
			if got.State != want {
				t.Fatalf("source=%+v want %s", got, want)
			}
			if name != "missing-socket" && requests.Load() != 1 {
				t.Fatalf("requests=%d, want exactly one", requests.Load())
			}
			if !reflect.DeepEqual(beforeWire, f.pollWire(t)) {
				t.Fatal("census wrote provider input")
			}
			afterRegistry, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatal(err)
			}
			if name != "changed-activation" && !bytes.Equal(beforeRegistry, afterRegistry) {
				t.Fatal("census changed Registry")
			}
			after, err := f.endpoint.handle.Observe(f.endpoint.binding)
			if err != nil || after.Exit != nil || after.PID != snap.PID || after.Session != snap.Session {
				t.Fatalf("census changed lifetime: %+v %v", after, err)
			}
		})
	}
}

func TestInstallCodexCensusLiveProtocolFixtureReadOnly(t *testing.T) {
	f := newProcessCodexPeerFixture(t, nil)
	snap, err := f.endpoint.handle.Observe(f.endpoint.binding)
	if err != nil {
		t.Fatal(err)
	}
	reg := installProcessObservationSchema(t, f.store, f.endpoint.binding, snap)
	before, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	wire := f.pollWire(t)
	ctx, cancel := context.WithTimeout(context.Background(), installProcessObservationLimit)
	defer cancel()
	owners := liveInstallProcessOwners(reg, installProcessHostIsAlive, newInstallProcessOwnerObserver(ctx, f.path, readInstallProcessHost, readInstallCodexSource))
	if len(owners) != 1 || owners[0].Source.State != "compatible" || owners[0].Target.State != "compatible" {
		t.Fatalf("owners: %+v", owners)
	}
	after, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !reflect.DeepEqual(wire, f.pollWire(t)) {
		t.Fatal("census mutated Registry/provider")
	}
	// The default wiring consumes the same state domain, without repairing it.
	t.Setenv("XDG_STATE_HOME", filepath.Dir(filepath.Dir(filepath.Dir(f.path))))
	if got, err := defaultLiveInstallProcessOwners(); err != nil || len(got) != 1 || got[0].Source.State != "compatible" || got[0].Target.State != "compatible" {
		t.Fatalf("default=%+v %v", got, err)
	}
}

// Preserve the old census even when binding proof is incomplete: a known live
// host still protects schema-changing publication and is printed as unknown.
func TestInstallCodexIncompleteActivationRetainsSchemaProtection(t *testing.T) {
	t.Parallel()
	reg := installCodexRegistry(t)
	reg.Panes[1].Status.Activation.Generation = "incomplete-current-generation"
	read := newInstallProcessOwnerObserver(context.Background(), "unused",
		func(context.Context, coremetadata.Registry, string, coremetadata.Pane) (processHostObservation, bool) {
			return processHostObservation{}, false
		},
		func(context.Context, string, coremetadata.Registry, coremetadata.Pane) installPeerSupport {
			return installPeerSupport{State: "unknown", Reason: "incomplete-activation"}
		})
	owners := liveInstallProcessOwners(reg, func(coremetadata.ProcessIdentity) bool { return true }, read)
	if len(owners) != 1 || owners[0].Source.State != "unknown" || owners[0].Target.State != "unknown" {
		t.Fatalf("lost live host: %+v", owners)
	}
	var out bytes.Buffer
	if err := installPreflightFixture(4, true, owners).Run(&out); err == nil || !strings.Contains(out.String(), "install stopped before publication") {
		t.Fatalf("schema protection: %v %s", err, out.String())
	}
}

func TestInstallCodexTargetExactObservationNegative(t *testing.T) {
	for _, name := range []string{"accepted", "refused", "wrong-binding", "wrong-birth", "replaced-socket", "changed-registry", "timeout"} {
		t.Run(name, func(t *testing.T) {
			f := newProcessCodexPeerFixture(t, nil)
			snap, err := f.endpoint.handle.Observe(f.endpoint.binding)
			if err != nil {
				t.Fatal(err)
			}
			reg := installProcessObservationSchema(t, f.store, f.endpoint.binding, snap)
			pane, _ := reg.Pane(f.endpoint.binding.Pane)
			view, ok := observeProcessHostOwner(reg, f.path, *pane)
			if !ok {
				t.Fatal("fixture observation unavailable")
			}
			if err := f.endpoint.listener.Close(); err != nil {
				t.Fatal(err)
			}
			beforeWire := f.pollWire(t)
			beforeRegistry, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			readContexts := make(chan context.Context, 1)
			serveInstallProtocolFixture(t, f.endpoint.socket, func(conn *net.UnixConn) {
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				var request codexProcessExchange
				if err := localipc.ReadJSON(conn, &request); err != nil {
					t.Error(err)
					return
				}
				requests.Add(1)
				expected := codexProcessExchange{Observe: &f.endpoint.binding}
				if !reflect.DeepEqual(request, expected) {
					t.Error("census requested a control effect")
					return
				}
				response := view
				switch name {
				case "wrong-binding":
					response.Binding.Generation = "replaced"
				case "wrong-birth":
					response.Host.Start = "reused-pid"
				case "replaced-socket":
					if err := os.Remove(f.endpoint.socket); err != nil {
						t.Error(err)
						return
					}
					l, err := localipc.Listen(f.endpoint.socket)
					if err != nil {
						t.Error(err)
						return
					}
					defer l.Close()
				case "changed-registry":
					if _, err := f.store.Update(func(r *coremetadata.Registry) error {
						p, _ := r.Pane(pane.Metadata.UID)
						p.Status.Activation = coremetadata.PaneActivation{}
						return nil
					}); err != nil {
						t.Error(err)
						return
					}
				case "timeout":
					readCtx := <-readContexts
					<-readCtx.Done()
					return
				}
				_ = localipc.WriteJSON(conn, processForegroundResult{Accepted: name != "refused", Observation: &response})
			})
			// Start the read deadline only after the replacement listener is ready.
			ctx, cancel := context.WithTimeout(context.Background(), processObservationReadLimit)
			defer cancel()
			readContexts <- ctx
			got, observed := readInstallProcessHost(ctx, reg, f.path, *pane)
			verdict := classifyInstallCodexTarget(got, observed && ctx.Err() == nil)
			want := "unknown"
			if name == "accepted" {
				want = "compatible"
			}
			if verdict.State != want || requests.Load() != 1 {
				t.Fatalf("target=%+v observed=%v requests=%d", verdict, observed, requests.Load())
			}
			afterRegistry, err := os.ReadFile(f.path)
			if err != nil {
				t.Fatal(err)
			}
			if name != "changed-registry" && !bytes.Equal(beforeRegistry, afterRegistry) {
				t.Fatal("census wrote Registry")
			}
			after, err := f.endpoint.handle.Observe(f.endpoint.binding)
			if err != nil || after.Exit != nil || after.PID != snap.PID || after.Session != snap.Session || !reflect.DeepEqual(beforeWire, f.pollWire(t)) {
				t.Fatalf("census changed provider/lifetime: %+v %v", after, err)
			}
		})
	}
}

// Warning work must not consume the safety read that an existing non-Codex
// owner received before Codex census diagnostics were added.
func TestInstallCodexBudgetDoesNotSkipClaudeCoordination(t *testing.T) {
	for _, budget := range []string{"expired-before", "expires-in-source", "expires-during-legacy"} {
		for _, response := range []string{"mismatch", "unanswered", "incomplete-projection"} {
			t.Run(budget+"/"+response, func(t *testing.T) {
				reg := installCodexRegistry(t)
				basePane, baseAgent := reg.Panes[1], reg.Agents[0]
				owner := func(suffix, provider string) (coremetadata.Pane, coremetadata.Agent) {
					p, a := basePane, baseAgent
					p.Metadata.UID, a.Metadata.UID = "pane-"+suffix, "agent-"+suffix
					p.Metadata.OwnerRef = &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: a.Metadata.UID}
					a.Status.PaneRef, a.Spec.Provider = p.Metadata.UID, provider
					activation := *basePane.Status.Activation.Process
					activation.Binding.AgentUID, activation.Binding.PaneUID = a.Metadata.UID, p.Metadata.UID
					p.Status.Activation.Process, p.Status.Activation.AgentUID = &activation, a.Metadata.UID
					return p, a
				}
				first, firstAgent := owner("a-codex", aiModeCodex)
				legacy, legacyAgent := owner("b-claude", aiModeClaude)
				last, lastAgent := owner("c-codex", aiModeCodex)
				if response == "incomplete-projection" {
					legacy.Status.Activation.AgentUID = ""
				}
				reg.Panes = []coremetadata.Pane{first, legacy, last}
				reg.Agents = []coremetadata.Agent{firstAgent, legacyAgent, lastAgent}
				before, err := json.Marshal(reg)
				if err != nil {
					t.Fatal(err)
				}
				duration := 60 * time.Millisecond
				if budget == "expired-before" {
					duration = -time.Second
				}
				ctx, cancel := context.WithTimeout(context.Background(), duration)
				defer cancel()
				parentDeadline, _ := ctx.Deadline()
				codexCalls, legacyCalls, lastCalls, sourceCalls := 0, 0, 0, 0
				var censusStart time.Time
				host := func(readCtx context.Context, _ coremetadata.Registry, _ string, p coremetadata.Pane) (processHostObservation, bool) {
					switch p.Metadata.UID {
					case first.Metadata.UID:
						codexCalls++
						return processHostObservation{Protocol: 1, Actions: []string{"peer-message"}}, true
					case legacy.Metadata.UID:
						legacyCalls++
						deadline, ok := readCtx.Deadline()
						if !ok || !deadline.After(parentDeadline) || deadline.Before(censusStart.Add(processObservationReadLimit)) || time.Until(deadline) > processObservationReadLimit {
							t.Errorf("legacy lost its independent 250ms: deadline=%v, parent=%v", deadline, parentDeadline)
						}
						if readCtx.Err() != nil {
							t.Error("warning cancellation reached legacy read")
						}
						if budget == "expires-during-legacy" {
							<-ctx.Done()
						}
						if readCtx.Err() != nil {
							t.Error("remaining warning time shortened legacy read")
						}
						switch response {
						case "unanswered":
							<-readCtx.Done()
							return processHostObservation{}, false
						case "incomplete-projection":
							// Reaching the original reader is required; it still decides whether
							// the incomplete activation has valid authority. No proof is invented.
							return processHostObservation{}, false
						default:
							return processHostObservation{Protocol: 1, Coordination: 4}, true
						}
					default:
						lastCalls++
						return processHostObservation{}, false
					}
				}
				source := func(readCtx context.Context, _ string, _ coremetadata.Registry, p coremetadata.Pane) installPeerSupport {
					sourceCalls++
					if p.Metadata.UID != first.Metadata.UID {
						t.Error("unexpected source read")
					}
					deadline, _ := readCtx.Deadline()
					if deadline != parentDeadline {
						t.Error("Codex warning deadline reset")
					}
					if budget == "expires-in-source" {
						<-readCtx.Done()
					}
					return installPeerSupport{State: "unknown", Reason: "fixture-source-proof"}
				}
				censusStart = time.Now()
				owners := liveInstallProcessOwners(reg, func(coremetadata.ProcessIdentity) bool { return true }, newInstallProcessOwnerObserver(ctx, "unused", host, source))
				wantCodex := 1
				if budget == "expired-before" {
					wantCodex = 0
				}
				if len(owners) != 3 || legacyCalls != 1 || codexCalls != wantCodex || sourceCalls != wantCodex || lastCalls != 0 {
					t.Fatalf("owners=%d legacy=%d first=%d source=%d last=%d", len(owners), legacyCalls, codexCalls, sourceCalls, lastCalls)
				}
				if owners[2].Source.State != "unknown" || owners[2].Target.State != "unknown" || owners[2].Source.Reason != "census-budget-exhausted" {
					t.Fatalf("lost Codex remainder: %+v", owners[2])
				}
				after, err := json.Marshal(reg)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("census changed Registry")
				}
				var out bytes.Buffer
				err = installPreflightFixture(5, true, owners).Run(&out)
				if response == "mismatch" {
					refusal, ok := err.(installPreflightExitError)
					if !ok || refusal.ExitCode() != 1 || !strings.Contains(out.String(), "Claude coordination version to 5") {
						t.Fatalf("known mismatch no longer refused: %v\n%s", err, out.String())
					}
				} else if err != nil {
					t.Fatalf("legacy unknown became a new refusal: %v\n%s", err, out.String())
				}
			})
		}
	}
}
