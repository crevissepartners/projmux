package projmux

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// hostLostRegistry is the golden process registry with its Agent's activation
// owned by the test user, so absence can be proven, and a conversation id the
// resume read accepts.
func hostLostRegistry(t *testing.T, change func(*metadata.Registry)) []byte {
	t.Helper()
	var reg metadata.Registry
	if err := json.Unmarshal(graphFixtureRegistry(t), &reg); err != nil {
		t.Fatal(err)
	}
	for i := range reg.Panes {
		p := &reg.Panes[i]
		if p.Status.Activation.Process == nil {
			continue
		}
		a := p.Status.Activation.Process
		a.HostProcess.OwnerUID, a.Child.OwnerUID = uint32(os.Getuid()), uint32(os.Getuid())
		p.Status.ProcessSession.ConnectionID = a.Binding.OperationID
		for j := range p.Status.ProcessSession.Pending {
			p.Status.ProcessSession.Pending[j].ConnectionID = a.Binding.OperationID
		}
	}
	if change != nil {
		change(&reg)
	}
	raw, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// identityStub reports each listed pid as the recorded identity (alive), as a
// different identity (reused) or absent, and counts the reads per pid.
type identityStub struct {
	alive, reused map[int]metadata.ProcessIdentity
	reads         map[int]int
}

func (s *identityStub) read(pid int) (metadata.ProcessIdentity, int, error) {
	s.reads[pid]++
	if id, ok := s.alive[pid]; ok {
		return id, 0, nil
	}
	if id, ok := s.reused[pid]; ok {
		return id, 0, nil
	}
	return metadata.ProcessIdentity{}, 0, localipc.ErrProcessAbsent
}

func hostLostIdentities(t *testing.T, reg []byte) (host, child metadata.ProcessIdentity) {
	t.Helper()
	var r metadata.Registry
	if err := json.Unmarshal(reg, &r); err != nil {
		t.Fatal(err)
	}
	pane, _ := mutationruntime.ProcessAgentPane(r, "agent-01")
	return pane.Status.Activation.Process.HostProcess, pane.Status.Activation.Process.Child
}

func TestReadAgentHostLostVerdictTable(t *testing.T) {
	raw := hostLostRegistry(t, nil)
	host, child := hostLostIdentities(t, raw)
	reused := func(id metadata.ProcessIdentity) metadata.ProcessIdentity {
		id.Start = "reused-start"
		return id
	}
	binding := core.ProcessBinding{HostInstanceID: "host-one", ProjectUID: "project-01", WindowUID: "window-01", AgentUID: "agent-01", PaneUID: "pane-02", Generation: "gen-one", OperationID: "op-one"}
	for _, tc := range []struct {
		name                  string
		stub                  identityStub
		candidate, h, c, both bool
	}{
		{"both absent", identityStub{}, true, true, true, true},
		{"live host", identityStub{alive: map[int]metadata.ProcessIdentity{host.PID: host}}, false, false, true, false},
		{"live child", identityStub{alive: map[int]metadata.ProcessIdentity{child.PID: child}}, false, true, false, false},
		{"both live", identityStub{alive: map[int]metadata.ProcessIdentity{host.PID: host, child.PID: child}}, false, false, false, false},
		{"both reused pids", identityStub{reused: map[int]metadata.ProcessIdentity{host.PID: reused(host), child.PID: reused(child)}}, true, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := tc.stub
			stub.reads = map[int]int{}
			c, _ := graphFixtureClient(t, raw)
			c.processIdentity = stub.read
			prepared, _ := mustPrepare(t, c, nil)
			got, err := c.ReadAgentHostLostFromPrepared(context.Background(), prepared, core.ReadAgentHostLostRequest{Ref: "uid:agent-01"})
			if err != nil {
				t.Fatal(err)
			}
			want := core.AgentHostLostVerdict{AgentUID: "agent-01", PaneUID: "pane-02", Binding: binding, Candidate: tc.candidate, HostIdentityAbsent: tc.h, ChildIdentityAbsent: tc.c, ActivationAbsent: tc.both}
			if tc.candidate {
				want.Conversation = "thread-one"
			}
			if got != want {
				t.Fatalf("verdict\n got %+v\nwant %+v", got, want)
			}
			if stub.reads[host.PID] != 1 || stub.reads[child.PID] != 1 || len(stub.reads) != 2 {
				t.Fatalf("each recorded pid is read once: %v", stub.reads)
			}
			// The internal judgement over the same rows and kernel answers agrees.
			internal, ok := mutationruntime.HostLostResumeCandidate(prepared.registry, "agent-01", stub.read)
			if ok != got.Candidate || (ok && internal.Record.ThreadID != got.Conversation) {
				t.Fatalf("internal judgement differs: %v %+v", ok, internal.Record)
			}
		})
	}
}

func TestReadAgentHostLostVerdictClaudeConversation(t *testing.T) {
	raw := hostLostRegistry(t, func(reg *metadata.Registry) {
		for i := range reg.Agents {
			reg.Agents[i].Spec.Provider = "claude"
		}
		for i := range reg.Panes {
			s := reg.Panes[i].Status.ProcessSession
			if s == nil {
				continue
			}
			s.Provider, s.SessionID, s.ThreadID = "claude", "session-one", ""
			for j := range s.Pending {
				s.Pending[j].SessionID = "session-one"
			}
			if s.History != nil {
				s.History.SessionID = "session-one"
				for j := range s.History.Expired {
					s.History.Expired[j].SessionID = "session-one"
				}
			}
		}
	})
	c, _ := graphFixtureClient(t, raw)
	stub := identityStub{reads: map[int]int{}}
	c.processIdentity = stub.read
	prepared, _ := mustPrepare(t, c, nil)
	got, err := c.ReadAgentHostLostFromPrepared(context.Background(), prepared, core.ReadAgentHostLostRequest{Ref: "agent-01", Project: "uid:project-01"})
	if err != nil || !got.Candidate || got.Conversation != "session-one" {
		t.Fatalf("%+v %v", got, err)
	}
}

// An Agent that cannot be host-lost is a verdict, not a refusal.
func TestReadAgentHostLostVerdictNotCandidates(t *testing.T) {
	raw := hostLostRegistry(t, func(reg *metadata.Registry) {
		for i := range reg.Panes {
			reg.Panes[i].Status.Activation = metadata.PaneActivation{}
		}
	})
	c, _ := graphFixtureClient(t, raw)
	stub := identityStub{reads: map[int]int{}}
	c.processIdentity = stub.read
	prepared, _ := mustPrepare(t, c, nil)
	got, err := c.ReadAgentHostLostFromPrepared(context.Background(), prepared, core.ReadAgentHostLostRequest{Ref: "uid:agent-01"})
	if err != nil || got != (core.AgentHostLostVerdict{AgentUID: "agent-01"}) || len(stub.reads) != 0 {
		t.Fatalf("%+v %v reads %v", got, err, stub.reads)
	}
}

func TestReadAgentHostLostVerdictRefusals(t *testing.T) {
	c, _ := graphFixtureClient(t, hostLostRegistry(t, nil))
	prepared, _ := mustPrepare(t, c, nil)
	other, err := NewClient(Options{ConfigDir: filepath.Join(t.TempDir(), "config"), StateDir: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	req := core.ReadAgentHostLostRequest{Ref: "uid:agent-01"}
	for name, tc := range map[string]struct {
		client   *Client
		prepared *PreparedRegistrySnapshot
		req      core.ReadAgentHostLostRequest
		want     Code
	}{
		"nil handle":      {c, nil, req, CodePreparedSnapshotInvalid},
		"zero handle":     {c, &PreparedRegistrySnapshot{}, req, CodePreparedSnapshotInvalid},
		"foreign root":    {other, prepared, req, CodePreparedSnapshotInvalid},
		"empty ref":       {c, prepared, core.ReadAgentHostLostRequest{}, CodeSelectorInvalid},
		"unknown agent":   {c, prepared, core.ReadAgentHostLostRequest{Ref: "uid:agent-missing"}, ""},
		"wrong project":   {c, prepared, core.ReadAgentHostLostRequest{Ref: "uid:agent-01", Project: "uid:project-missing"}, ""},
		"malformed scope": {c, prepared, core.ReadAgentHostLostRequest{Ref: "uid:agent-01", Window: "uid:"}, ""},
	} {
		_, err := tc.client.ReadAgentHostLostFromPrepared(context.Background(), tc.prepared, tc.req)
		var refusal *Refusal
		if !errors.As(err, &refusal) || (tc.want != "" && refusal.Code != tc.want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ReadAgentHostLostFromPrepared(canceled, nil, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("entry cancellation: %v", err)
	}
	sentinel := errors.New("captured paths")
	broken := *c
	broken.pathsErr = sentinel
	if _, err := broken.ReadAgentHostLostFromPrepared(context.Background(), prepared, req); !errors.Is(err, sentinel) {
		t.Fatalf("paths error: %v", err)
	}
}

// The verdict is judged from the handle and writes nothing: the state tree
// bytes and the handle are identical before and after, and the Registry file
// is not needed.
func TestReadAgentHostLostVerdictIsReadOnly(t *testing.T) {
	c, paths := graphFixtureClient(t, hostLostRegistry(t, nil))
	stub := identityStub{reads: map[int]int{}}
	c.processIdentity = stub.read
	prepared, _ := mustPrepare(t, c, nil)
	tree := func() map[string][32]byte {
		out := map[string][32]byte{}
		for _, root := range []string{paths.StateDir, paths.ConfigDir} {
			_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				b, _ := os.ReadFile(path)
				info, _ := d.Info()
				out[path] = sha256.Sum256(append(b, []byte(info.ModTime().String())...))
				return nil
			})
		}
		return out
	}
	before, handle := tree(), prepared.registry.Clone()
	req := core.ReadAgentHostLostRequest{Ref: "uid:agent-01"}
	first, err := c.ReadAgentHostLostFromPrepared(context.Background(), prepared, req)
	if err != nil || !first.Candidate {
		t.Fatalf("%+v %v", first, err)
	}
	if after := tree(); !reflect.DeepEqual(before, after) {
		var names []string
		for k := range after {
			names = append(names, k)
		}
		sort.Strings(names)
		t.Fatalf("state tree changed: %v", names)
	}
	if !reflect.DeepEqual(handle, prepared.registry) {
		t.Fatal("handle registry changed")
	}
	if err = os.Remove(store.PathFor(paths.StateDir)); err != nil {
		t.Fatal(err)
	}
	again, err := c.ReadAgentHostLostFromPrepared(context.Background(), prepared, req)
	if err != nil || again != first {
		t.Fatalf("second read %+v %v", again, err)
	}
}
