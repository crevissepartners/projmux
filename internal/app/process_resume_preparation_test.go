package app

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"reflect"
	"testing"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

func TestSDKPreparedResumeValidationReadOnly(t *testing.T) {
	for _, change := range []string{"unchanged", "digest", "missing", "attempt", "file", "owner-live", "binding"} {
		t.Run(change, func(t *testing.T) {
			c, opts, _, a := hostLostPreparedFixture(t)
			if _, err := c.prepareOwnedClaudeResume(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			p, record, err := c.readResumePreparation(context.Background(), opts, nil)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "digest":
				p.LaunchDigest = "tampered"
			case "missing":
				if err = os.Remove(c.deferredStatePath("deferred-launches", a.Binding.AgentUID)); err != nil {
					t.Fatal(err)
				}
			case "attempt":
				record.Attempt = &deferredLaunchAttempt{Source: record.Retired, Target: a.Binding}
				if err = writeDeferredState(c.deferredStatePath("deferred-launches", a.Binding.AgentUID), record); err != nil {
					t.Fatal(err)
				}
			case "file":
				for path := range record.Files {
					if err = os.WriteFile(path, []byte("changed frozen file"), 0600); err != nil {
						t.Fatal(err)
					}
					break
				}
				p.LaunchDigest = "also-stale"
			case "owner-live":
				c.processIdentity = func(pid int) (m.ProcessIdentity, int, error) {
					if pid == a.HostProcess.PID {
						return a.HostProcess, 0, nil
					}
					return absentProcess(pid)
				}
			case "binding":
				p.Binding.Generation = "changed"
			}
			before := sdkCreateDirectorySnapshot(t, os.Getenv("HOME"))
			got, _, err := c.readResumePreparation(context.Background(), opts, &p)
			if change == "unchanged" {
				if err != nil || got != p {
					t.Fatal(got, err)
				}
			} else if err == nil {
				t.Fatal("invalid proof accepted", change)
			}
			if !reflect.DeepEqual(before, sdkCreateDirectorySnapshot(t, os.Getenv("HOME"))) {
				t.Fatal("Validate reconciled or wrote state", change)
			}
		})
	}
}

// No SDK caller admission runs in this test. The independent owner's wire must
// reject the acknowledged digest itself before sidecar/Registry effects.
func TestSDKPreparedResumeRechecksProof(t *testing.T) {
	c, opts, store, a := hostLostPreparedFixture(t)
	if _, err := c.prepareOwnedClaudeResume(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	p, _, err := c.readResumePreparation(context.Background(), opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.LaunchDigest = "caller-was-validated-before-plan-changed"
	wire := processDetachedResumeRequest{Agent: a.Binding.AgentUID, Previous: a.Binding, Prompt: processResumeFirstFrame{Kind: "user", Text: "must not send"}, Preparation: &p}
	before := sdkCreateDirectorySnapshot(t, os.Getenv("HOME"))
	owner, caller := net.Pipe()
	defer owner.Close()
	defer caller.Close()
	done := make(chan error, 1)
	go func() {
		done <- runProcessDetachedResumeOwner(owner, json.NewEncoder(owner), json.NewDecoder(owner), processDetachedRequest{Resume: &wire}, m.ProcessIdentity{}, c)
	}()
	var reply processDetachedReply
	if err = json.NewDecoder(caller).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.ErrorCode != mutationruntime.ResumePreparationStale || reply.Preparation != nil {
		t.Fatalf("owner failed to fence proof: %+v", reply)
	}
	if err = <-done; !errors.Is(err, mutationruntime.ErrResumePreparationStale) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, sdkCreateDirectorySnapshot(t, os.Getenv("HOME"))) {
		t.Fatal("owner refusal wrote state")
	}
	reg, err := store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane(a.Binding.PaneUID)
	if pane.Status.ProcessSession.Binding != a.Binding {
		t.Fatal("owner reserved before fence")
	}
}

// This invokes reservation directly after an earlier successful read. Removing
// the under-claim-guard fence would mutate the attempt sidecar before CAS.
func TestSDKPreparedResumeReservationRechecksProof(t *testing.T) {
	c, opts, _, a := hostLostPreparedFixture(t)
	if _, err := c.prepareOwnedClaudeResume(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	p, record, err := c.readResumePreparation(context.Background(), opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := c.processResumeCandidate(processAgentResumeRequest{options: opts})
	if err != nil {
		t.Fatal(err)
	}
	p.LaunchDigest = "stale-after-admission"
	target := processSchemaBinding(a.Binding)
	target.Host = "next-host"
	target.Generation = "next-generation"
	target.Operation = "next-op"
	before := sdkCreateDirectorySnapshot(t, os.Getenv("HOME"))
	err = c.reserveProcessResumeWithPreparation(context.Background(), candidate, agentSettingsLaunch{}, target, nil, &p, record)
	if !errors.Is(err, mutationruntime.ErrResumePreparationStale) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, sdkCreateDirectorySnapshot(t, os.Getenv("HOME"))) {
		t.Fatal("guard wrote attempt before proof fence")
	}
}

func TestSDKPreparedResumeFailureDoesNotInventProof(t *testing.T) {
	for _, change := range []string{"unchanged", "unknown", "replacement", "missing", "recipe"} {
		t.Run(change, func(t *testing.T) {
			c, opts, store, a := hostLostPreparedFixture(t)
			if _, err := c.prepareOwnedClaudeResume(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			p, record, err := c.readResumePreparation(context.Background(), opts, nil)
			if err != nil {
				t.Fatal(err)
			}
			if change == "missing" {
				if err = os.Remove(c.deferredStatePath("deferred-launches", a.Binding.AgentUID)); err != nil {
					t.Fatal(err)
				}
			}
			if change == "unknown" {
				c.processIdentity = func(int) (m.ProcessIdentity, int, error) { return m.ProcessIdentity{}, 0, errors.New("unknown") }
			}
			if change == "replacement" || change == "recipe" {
				_, _, err = store.UpdateConvergent(func(reg *m.Registry) error {
					pane, _ := reg.Pane(a.Binding.PaneUID)
					if change == "replacement" {
						pane.Status.ProcessSession.Binding.Generation = "external-winner"
						pane.Status.Activation.Process.Binding.Generation = "external-winner"
						pane.Status.Activation.Generation = "external-winner"
					} else {
						agent, _ := reg.Agent(a.Binding.AgentUID)
						agent.Metadata.Annotations[m.AnnotationAgentModel] = "other"
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			before := sdkCreateDirectorySnapshot(t, os.Getenv("HOME"))
			got := c.failedResumePreparation(&p, record, processAgentResumeResult{})
			if change == "unchanged" {
				if got == nil || *got != p {
					t.Fatal("unchanged valid proof lost", got)
				}
			} else if got != nil {
				t.Fatal("invented failure proof", got)
			}
			if !reflect.DeepEqual(before, sdkCreateDirectorySnapshot(t, os.Getenv("HOME"))) {
				t.Fatal("failure projection prepared or replayed")
			}
		})
	}
}
