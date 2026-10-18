package runtime

import (
	"context"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestDetachedResumeCleanupRecoversOnlyExactOwner(t *testing.T) {
	for _, kind := range []string{"matching-retirement", "different-owner", "missing-retirement", "different-pane", "different-Wait"} {
		t.Run(kind, func(t *testing.T) {
			reg, _, pane := stopFixture()
			activation := *pane.Status.Activation.Process
			// A mismatched kernel start proves this historical test identity is absent.
			owner := m.ProcessIdentity{PID: os.Getpid(), OwnerUID: uint32(os.Getuid()), Start: "retired-test-owner"}
			activation.HostProcess = owner
			previous := activation.Binding
			previous.Generation, previous.OperationID, previous.HostInstanceID = "previous-generation", "previous-op", "previous-host"
			request := ProcessDetachedResumeRequest{Agent: previous.AgentUID, Previous: previous}
			writeStopWait(&reg)
			path := filepath.Join(t.TempDir(), "metadata", "registry.json")
			if kind != "missing-retirement" {
				if err := WriteProcessRetirement(path, activation, reg.Panes[0].Status.LastTermination); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "different-owner":
				owner.Start = "another-retired-owner"
			case "different-pane":
				request.Previous.PaneUID = "another-pane"
			case "different-Wait":
				wait := reg.Panes[0].Status.LastTermination.Clone()
				wait.ObservedAt = wait.ObservedAt.Add(time.Second)
				reg.Panes[0].Status.LastTermination = wait
				reg.Agents[0].Status.LastTermination = wait.Clone()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()
			binding, err := awaitDetachedProcessResumeCleanup(ctx, func() (m.Registry, error) { return reg.Clone(), nil }, path, request, owner, processhost.Binding{})
			if kind == "matching-retirement" {
				if err != nil || binding != processResumeHostBinding(activation.Binding) {
					t.Fatalf("exact retirement not recovered: %+v %v", binding, err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "process-detached-cleanup-unconfirmed") || binding != (processhost.Binding{}) {
				t.Fatalf("unproved generation accepted: %+v %v", binding, err)
			}
		})
	}
}

func TestPreparedResumeCleanupUnreservedLostSource(t *testing.T) {
	for _, mode := range []string{"prepared-refusal", "ordinary", "same-owner", "other-binding"} {
		t.Run(mode, func(t *testing.T) {
			reg, agent, pane := stopFixture()
			owner := m.ProcessIdentity{PID: os.Getpid(), OwnerUID: uint32(os.Getuid()), Start: "absent-owned-preparation-helper"}
			request := ProcessDetachedResumeRequest{Agent: agent.Metadata.UID, Previous: pane.Status.ProcessSession.Binding, Preparation: &ProcessResumePreparation{Binding: pane.Status.ProcessSession.Binding}}
			switch mode {
			case "ordinary":
				request.Preparation = nil
			case "same-owner":
				reg.Panes[0].Status.Activation.Process.HostProcess = owner
			case "other-binding":
				request.Previous.Generation = "other-generation"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			_, err := awaitDetachedProcessResumeCleanup(ctx, func() (m.Registry, error) { return reg.Clone(), nil }, filepath.Join(t.TempDir(), "registry.json"), request, owner, processhost.Binding{})
			if mode == "prepared-refusal" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("cleanup weakened ordinary/owner/binding proof", mode, err)
			}
		})
	}
}

// Exercise the real private-descriptor launcher and the same error/proof wire
// consumed by SDK Resume. The owned fixture helper returns a proof-bearing
// error; only the cleanup observation differs between the two cases.
func TestPreparedResumeUnconfirmedCleanupDropsWireProof(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fixture identity uses Linux procfs")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 fixture unavailable")
	}
	for _, confirmed := range []bool{true, false} {
		t.Run(fmt.Sprint(confirmed), func(t *testing.T) {
			root := t.TempDir()
			script := filepath.Join(root, "helper.py")
			executable := filepath.Join(root, "launcher")
			source := `import os,sys,json,socket,signal
if os.fork(): os._exit(0)
os.setsid()
s=socket.socket(fileno=3)
with os.fdopen(4) as payload: req=json.load(payload)
pid=os.getpid()
raw=open('/proc/self/stat').read(); fields=raw[raw.rfind(')')+2:].split()
owner={'pid':pid,'ownerUID':os.getuid(),'start':'linux:'+open('/proc/sys/kernel/random/boot_id').read().strip()+':'+fields[19]}
p=req['Resume']['Preparation'];b=p['Binding']
wire={'Host':b['hostInstanceID'],'Project':b['projectUID'],'Window':b['windowUID'],'Agent':b['agentUID'],'Pane':b['paneUID'],'Generation':b['generation'],'Operation':b['operationID']}
s.sendall((json.dumps({'Owner':owner})+'\n').encode())
s.sendall((json.dumps({'Owner':owner,'Binding':wire,'Error':'fixture failed first input','ErrorCode':'process-resume-refused','Preparation':p})+'\n').encode())
# Keep the exact owner alive for the caller's identity proof. EOF or the
# launcher's guarded SIGTERM retires only this fixture helper.
while s.recv(1024): pass
`
			if err = os.WriteFile(script, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(executable, fmt.Appendf(nil, "#!/bin/sh\nexec %q -u %q\n", python, script), 0700); err != nil {
				t.Fatal(err)
			}
			reg, agent, pane := stopFixture()
			b := pane.Status.ProcessSession.Binding
			proof := ProcessResumePreparation{AgentUID: agent.Metadata.UID, PaneUID: pane.Metadata.UID, Binding: b, Conversation: "frozen-conversation", LaunchDigest: "frozen-digest", HostLost: true}
			request := ProcessDetachedResumeRequest{Agent: agent.Metadata.UID, Previous: b, Preparation: &proof, Prompt: ProcessResumeFirstFrame{Kind: "user", Text: "explicit input"}}
			calls := 0
			load := func() (m.Registry, error) {
				calls++
				if !confirmed {
					return m.Registry{}, errors.New("fixture cleanup state unavailable")
				}
				return reg.Clone(), nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			reply, err := LaunchDetachedProcessResume(ctx, executable, ProcessDetachedResumeEnvelope{Resume: &request, RegistryPath: filepath.Join(root, "registry.json")}, load, io.Discard, io.Discard)
			if reply.Owner.Valid() {
				defer SignalDetachedProcessOwner(reply.Owner)
			}
			var failure *ProcessResumeFailure
			if !errors.As(err, &failure) || failure.Code != "process-resume-refused" || reply.Error != "fixture failed first input" || calls == 0 {
				t.Fatalf("did not exercise proof-bearing owner failure and cleanup: reply=%+v err=%v loads=%d", reply, err, calls)
			}
			if confirmed {
				if reply.Preparation == nil || *reply.Preparation != proof || strings.Contains(err.Error(), "cleanup-unconfirmed") {
					t.Fatalf("confirmed failure lost proof: %+v %v", reply, err)
				}
			} else if reply.Preparation != nil || !strings.Contains(err.Error(), "cleanup-unconfirmed") {
				t.Fatalf("unconfirmed cleanup leaked retry proof: %+v %v", reply, err)
			}
			if _, _, e := localipc.Process(reply.Owner.PID); !errors.Is(e, localipc.ErrProcessAbsent) {
				t.Fatal("fixture helper remained", e)
			}
		})
	}
}
