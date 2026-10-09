package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// protocolZeroObservation is the observation an owner built before control
// protocol v1 answers with, and the shape a client built before it decodes.
type protocolZeroObservation struct {
	Binding         processhost.Binding
	Host, Child     coremetadata.ProcessIdentity
	Provider, State string
	Exit            *processhost.Exit `json:",omitempty"`
}

type protocolZeroResult struct {
	Accepted            bool
	Stale, Busy, Closed bool
	Observation         *protocolZeroObservation `json:",omitempty"`
}

const testProcessHostRevision = "0123456789abcdef0123456789abcdef01234567"

func stubProcessHostRevision(t *testing.T, revision string) {
	t.Helper()
	previous := processHostRevision
	processHostRevision = func() string { return revision }
	t.Cleanup(func() { processHostRevision = previous })
}

// processProtocolHost is one live fixture owner with a pending control request
// inside a running turn, and the exact current activation recorded for it.
type processProtocolHost struct {
	binding  processhost.Binding
	path     string
	socket   string
	registry coremetadata.Registry
	snap     processhost.Snapshot
	actions  []string
	pending  int
}

func newProcessProtocolHost(t *testing.T, provider string, lifetime ...processOwnerLifetime) processProtocolHost {
	t.Helper()
	var h processProtocolHost
	var store *intmetadata.Store
	if provider == aiModeClaude {
		f := newProcessClaudeFixtureAt(t, nil, "", lifetime...)
		f.turn(t, "protocol-pending", "question")
		h.snap = f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) > 0 })
		h.binding, h.path, store = f.binding, f.path, f.store
		h.socket = processClaudeHostSocket(f.path, f.binding.Pane, f.binding.Generation)
		h.actions, h.pending = claudeForegroundActions, 1
	} else {
		f := newProcessCodexFixtureWithEvents(t, nil, processhost.DefaultLimits().Events, lifetime...)
		f.turn(t, "protocol-pending", "controls")
		h.snap = f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
		h.binding, h.path, store = f.endpoint.binding, f.path, f.store
		h.socket = f.endpoint.socket
		h.actions, h.pending = codexForegroundActions, 2
	}
	h.registry = installProcessObservationSchema(t, store, h.binding, h.snap)
	return h
}

func (h processProtocolHost) view(t *testing.T) processHostObservation {
	t.Helper()
	pane, _ := h.registry.Pane(h.binding.Pane)
	agent, _ := h.registry.Agent(h.binding.Agent)
	r := remoteProcessObserver{ctx: context.Background(), registry: h.registry, registryPath: h.path, binding: h.binding}
	view, err := r.observeHostView(h.binding, *pane.Status.Activation.Process, agent.Spec.Provider)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func (h processProtocolHost) foreground(t *testing.T, provider, action string) processForegroundResult {
	t.Helper()
	pane, _ := h.registry.Pane(h.binding.Pane)
	identity, err := localipc.InspectOwnedSocket(h.socket)
	if err != nil {
		t.Fatal(err)
	}
	request := processForegroundRequest{Authority: processhost.Authority{Binding: h.binding, Session: h.snap.Session, Connection: h.snap.Connection}, Action: action, Operation: "protocol-operation"}
	result, err := callProcessForeground(context.Background(), h.socket, identity, pane.Status.Activation.Process.HostProcess, processHostRequest(provider, nil, &request))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestProcessHostObservationReportsControlProtocolV1(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			stubProcessHostRevision(t, testProcessHostRevision)
			h := newProcessProtocolHost(t, provider)
			view := h.view(t)
			if view.Protocol != 1 || view.Revision != testProcessHostRevision || view.hostRevision() != testProcessHostRevision {
				t.Fatalf("protocol/revision: %+v", view)
			}
			if !slices.Equal(view.Actions, h.actions) || !slices.IsSorted(view.Actions) {
				t.Fatalf("actions = %v, want %v", view.Actions, h.actions)
			}
			if !view.Turn || view.Pending != h.pending || view.OwnerMode != "foreground" {
				t.Fatalf("turn/pending/owner mode: %+v", view)
			}
			raw, err := json.Marshal(processForegroundResult{Accepted: true, Observation: &view})
			if err != nil {
				t.Fatal(err)
			}
			// The count crosses; the pending requests and turn identity do not.
			for _, leaked := range []string{"Color?", "fixture-tool", h.snap.Turn, h.snap.Session} {
				if leaked != "" && bytes.Contains(raw, []byte(leaked)) {
					t.Fatalf("observation carried %q: %s", leaked, raw)
				}
			}
		})
	}
}

func TestProcessHostObservationWithoutBuildRevisionIsUnknown(t *testing.T) {
	stubProcessHostRevision(t, "")
	h := newProcessProtocolHost(t, aiModeCodex)
	view := h.view(t)
	if view.Protocol != 1 || view.Revision != "" || view.hostRevision() != "" {
		t.Fatalf("revision-less owner: %+v", view)
	}
	for _, revision := range []string{"0123456", strings.ToUpper(testProcessHostRevision), testProcessHostRevision + "-dirty"} {
		if got := (processHostObservation{Protocol: 1, Revision: revision}).hostRevision(); got != "" {
			t.Fatalf("revision %q reported as %q", revision, got)
		}
	}
}

// A new client reads a protocol-0 owner's answer without failing, as protocol
// 0 with no revision; an old client reads a v1 answer by ignoring the fields.
func TestProcessHostObservationProtocolZeroCompatibility(t *testing.T) {
	binding := processhost.Binding{Host: "host", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "generation", Operation: "operation"}
	identity := coremetadata.ProcessIdentity{PID: 42, OwnerUID: 1000, Start: "1"}
	old, err := json.Marshal(protocolZeroResult{Accepted: true, Observation: &protocolZeroObservation{Binding: binding, Host: identity, Child: identity, Provider: aiModeClaude, State: "ready"}})
	if err != nil {
		t.Fatal(err)
	}
	var current processForegroundResult
	if err = localipc.ReadJSON(bytes.NewReader(old), &current); err != nil {
		t.Fatalf("new client refused protocol-0 answer: %v", err)
	}
	view := current.Observation
	if !current.Accepted || view == nil || view.Binding != binding || view.State != "ready" || view.Protocol != 0 || view.Actions != nil || view.Turn || view.Pending != 0 || view.OwnerMode != "" || view.hostRevision() != "" {
		t.Fatalf("protocol-0 answer: %+v", current)
	}

	v1 := processHostObservation{Binding: binding, Host: identity, Child: identity, Provider: aiModeCodex, State: "ready", Protocol: processHostProtocol, Revision: testProcessHostRevision, Actions: codexForegroundActions, Turn: true, Pending: 2, OwnerMode: processHostOwnerForeground}
	raw, err := json.Marshal(processForegroundResult{Accepted: true, Observation: &v1})
	if err != nil {
		t.Fatal(err)
	}
	var legacy protocolZeroResult
	if err = localipc.ReadJSON(bytes.NewReader(raw), &legacy); err != nil {
		t.Fatalf("old client refused v1 answer: %v", err)
	}
	want := protocolZeroObservation{Binding: binding, Host: identity, Child: identity, Provider: aiModeCodex, State: "ready"}
	if !legacy.Accepted || legacy.Observation == nil || *legacy.Observation != want {
		t.Fatalf("old client read: %+v", legacy)
	}
	// An unsupported refusal is not stale to a new client and reads as an
	// unaccepted answer to an old one.
	refused, err := json.Marshal(processForegroundResult{Unsupported: true})
	if err != nil {
		t.Fatal(err)
	}
	legacy = protocolZeroResult{}
	if err = localipc.ReadJSON(bytes.NewReader(refused), &legacy); err != nil || legacy.Accepted {
		t.Fatalf("old client read refusal: %+v %v", legacy, err)
	}
}

func TestProcessHostRefusesUnsupportedActionDistinctFromStale(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			h := newProcessProtocolHost(t, provider)
			result := h.foreground(t, provider, "rewind")
			if result.Accepted || result.Stale || result.Busy || result.Closed || !result.Unsupported {
				t.Fatalf("unknown action: %+v", result)
			}
			err := processTurnAcceptance(result)
			if !errors.Is(err, errProcessHostUnsupportedAction) || errors.Is(err, processhost.ErrStale) || !strings.Contains(err.Error(), "process-host-unsupported-action") || strings.Contains(err.Error(), "stale") {
				t.Fatalf("client refusal: %v", err)
			}
			// The refusal leaves the owner and its pending controls intact.
			view := h.view(t)
			if view.State != "ready" || !view.Turn || view.Pending != h.pending {
				t.Fatalf("refusal changed owner: %+v", view)
			}
			// Stale authority stays stale for a supported action.
			stale := h
			stale.snap.Connection = "other-connection"
			if result := stale.foreground(t, provider, "interrupt"); !result.Stale || result.Unsupported || result.Accepted {
				t.Fatalf("stale authority: %+v", result)
			}
		})
	}
}

func TestProcessForegroundActionListsDecideRefusal(t *testing.T) {
	for name, actions := range map[string][]string{aiModeClaude: claudeForegroundActions, aiModeCodex: codexForegroundActions} {
		if !slices.IsSorted(actions) || len(slices.Compact(slices.Clone(actions))) != len(actions) {
			t.Fatalf("%s actions not sorted and unique: %v", name, actions)
		}
		for _, action := range actions {
			if err := processForegroundSupported(actions, action); err != nil {
				t.Fatalf("%s %s refused: %v", name, action, err)
			}
		}
		for _, action := range []string{"", "rewind", "Turn", "observe"} {
			if err := processForegroundSupported(actions, action); !errors.Is(err, errProcessHostUnsupportedAction) {
				t.Fatalf("%s %q accepted", name, action)
			}
		}
	}
	// An unknown action reaches the owner's dispatcher as a refusal, before
	// any provider handle is used.
	if err := applyClaudeForeground(context.Background(), nil, processForegroundRequest{Action: "rewind"}, nil); !errors.Is(err, errProcessHostUnsupportedAction) {
		t.Fatalf("Claude dispatcher: %v", err)
	}
}

func TestDescribeProcessAgentShowsHostRevision(t *testing.T) {
	raw, err := os.ReadFile("../core/metadata/testdata/registry-v5-process.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var reg coremetadata.Registry
	if err = json.Unmarshal(raw, &reg); err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane("pane-02")
	agentUID := pane.Status.Activation.Process.Binding.AgentUID
	for _, tc := range []struct {
		name, revision, want string
	}{
		{"v1-owner", testProcessHostRevision, testProcessHostRevision},
		{"protocol-0-or-unreachable-owner", "", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var asked string
			c := &describeCommand{processRevision: func(_ coremetadata.Registry, p coremetadata.Pane) string {
				asked = p.Metadata.UID
				return tc.revision
			}}
			rows := c.processHostRevisionRows(reg, agentUID)
			if asked != "pane-02" || len(rows) != 1 || rows[0] != [2]string{"HostRevision", tc.want} {
				t.Fatalf("rows = %v (asked %q)", rows, asked)
			}
			var out bytes.Buffer
			if err := writeResourceDescription(&out, "describe agent", coremetadata.KindAgent, selector.Match{Kind: coremetadata.KindAgent, UID: agentUID}, reg, rows...); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "HostRevision:") || !strings.Contains(out.String(), tc.want+"\n") {
				t.Fatalf("description:\n%s", out.String())
			}
		})
	}
	// Without a current activation there is no owner to name.
	retired := reg.Clone()
	p, _ := retired.Pane("pane-02")
	p.Status.Activation.Process = nil
	c := &describeCommand{processRevision: func(coremetadata.Registry, coremetadata.Pane) string {
		t.Fatal("asked an absent owner")
		return ""
	}}
	if rows := c.processHostRevisionRows(retired, agentUID); rows != nil {
		t.Fatalf("retired activation rows = %v", rows)
	}
}

func TestDefaultProcessHostRevisionLookupReadsLiveOwner(t *testing.T) {
	stubProcessHostRevision(t, testProcessHostRevision)
	h := newProcessProtocolHost(t, aiModeCodex)
	// The fixture registry lives at <state home>/projmux/metadata/registry.json.
	t.Setenv("XDG_STATE_HOME", filepath.Dir(filepath.Dir(filepath.Dir(h.path))))
	paneRef, _ := h.registry.Pane(h.binding.Pane)
	if got := defaultProcessHostRevisionLookup()(h.registry, *paneRef); got != testProcessHostRevision {
		t.Fatalf("live owner revision = %q", got)
	}
}

// The declaration reaches real provider endpoints before their observation
// socket is published, including contexts that suppress startup cancellation.
func TestProcessHostObservationOwnerLifetime(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, tc := range []struct {
			name     string
			lifetime processOwnerLifetime
			want     string
		}{
			{"stdin EOF", processOwnerLifetime{stdinEOF: processStdinEOFTrigger}, processHostOwnerForeground},
			{"explicit stop or signal", processOwnerLifetime{}, processHostOwnerDetached},
		} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				h := newProcessProtocolHost(t, provider, tc.lifetime)
				if view := h.view(t); view.OwnerMode != tc.want {
					t.Fatalf("OwnerMode = %q, want %q", view.OwnerMode, tc.want)
				}
			})
		}
	}
}

func TestProcessForegroundLifetimeModeSurvivesContextWrapping(t *testing.T) {
	ctx, cancel := processForegroundLifetime()
	cancel()
	for _, derived := range []context.Context{ctx, context.WithoutCancel(ctx)} {
		if got := processOwnerMode(derived); got != processHostOwnerForeground {
			t.Fatalf("public CLI OwnerMode = %q", got)
		}
	}
}
