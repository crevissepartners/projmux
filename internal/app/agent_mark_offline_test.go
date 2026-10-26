package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	runtimeservice "github.com/crevissepartners/projmux/internal/services/runtime"
	sdk "github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// deadIdentity returns the kernel identity of a process that has been reaped.
func deadIdentity(t *testing.T) coremetadata.ProcessIdentity {
	t.Helper()
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	id, _, err := localipc.Process(cmd.Process.Pid)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// hostLostStore writes a Running process Agent whose recorded host and child
// identities belong to reaped processes.
func hostLostStore(t *testing.T, provider string) (*intmetadata.Store, processhost.Binding, coremetadata.ProcessActivation) {
	t.Helper()
	store, b := sessionBindingFixture(t, provider)
	host, child := deadIdentity(t), deadIdentity(t)
	var activation coremetadata.ProcessActivation
	if _, err := store.Update(func(reg *coremetadata.Registry) error {
		pane, _ := reg.Pane(b.Pane)
		pane.Status.Activation.Process.HostProcess = host
		pane.Status.Activation.Process.Child = child
		pane.Status.ProcessSession.ResumeState = coremetadata.ProcessResumeUnknown
		activation = *pane.Status.Activation.Process
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return store, b, activation
}

func markOfflineCommand(store *intmetadata.Store, read processIdentityReader) *agentCommand {
	return &agentCommand{loadRegistry: store.LoadReadOnly, messagePaths: agentMessagePaths{registryPath: store.Path()}, processIdentity: read}
}

// agentRow projects the Agent through the same process observation, graph and
// navigation view that get agents renders.
func agentRow(t *testing.T, store *intmetadata.Store, agentUID string) registryview.Row {
	t.Helper()
	reg, err := store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	inventory := runtimeservice.ObserveRegistryProcesses(context.Background(), reg, store.Path(), nil)
	view := registryview.Build(registryview.Input{Graph: resourcegraph.Resolve(reg, resourcegraph.Inventory{Processes: inventory})})
	row, ok := view.Row(registryview.ProjectID(agentUID))
	if !ok {
		t.Fatal("agent row missing")
	}
	return row
}

func TestAgentMarkOfflineRetiresLostHostWithoutWait(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			store, b, activation := hostLostStore(t, provider)
			before, _ := store.LoadReadOnly()
			oldPane, _ := before.Pane(b.Pane)
			if row := agentRow(t, store, b.Agent); row.Status != resourcegraph.StatusUnknown || !reflect.DeepEqual(row.Actions, []registryview.Action{registryview.ActionResume, registryview.ActionMarkOffline, registryview.ActionDelete}) {
				t.Fatalf("lost host row before mark-offline: %s %v", row.Status, row.Actions)
			}
			var stdout, stderr bytes.Buffer
			if err := markOfflineCommand(store, localipc.Process).Run([]string{"mark-offline", "uid:" + b.Agent}, &stdout, &stderr); err != nil {
				t.Fatal(err, stderr.String())
			}
			if want := "marked offline agent uid:" + b.Agent + " pane uid:" + b.Pane; !strings.HasPrefix(stdout.String(), want) {
				t.Fatalf("stdout %q", stdout.String())
			}
			reg, _ := store.LoadReadOnly()
			pane, _ := reg.Pane(b.Pane)
			agent, _ := reg.Agent(b.Agent)
			r := pane.Status.LastTermination
			if agent.Status.Phase != coremetadata.PhaseOffline || !pane.Status.Activation.IsZero() || pane.Status.ProcessSession.ResumeState != coremetadata.ProcessResumable {
				t.Fatalf("not retired: phase=%s activation=%+v session=%+v", agent.Status.Phase, pane.Status.Activation, pane.Status.ProcessSession)
			}
			if r == nil || r.Source != coremetadata.TerminationSourceReconcile || r.Classification != coremetadata.TerminationUnknown || r.ExitCode != nil || r.Signal != "" || !reflect.DeepEqual(r, agent.Status.LastTermination) || r.Generation != activation.Binding.Generation {
				t.Fatalf("termination %+v", r)
			}
			if coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, r) {
				t.Fatal("unknown retirement became Wait evidence")
			}
			if agent.Status.PaneRef != b.Pane || pane.Status.ProcessSession.Binding != oldPane.Status.ProcessSession.Binding || pane.Status.ProcessSession.SessionID != oldPane.Status.ProcessSession.SessionID || pane.Status.ProcessSession.ThreadID != oldPane.Status.ProcessSession.ThreadID {
				t.Fatal("identity or conversation changed")
			}
			// Resume admits the same conversation through the ordinary path.
			if token := processResumeCandidateToken(reg, b.Agent); token != "" {
				t.Fatal("resume refused after mark-offline:", token)
			}
			candidates := listResumableProcessAgents(reg, processResumeFilter{})
			if len(candidates) != 1 || candidates[0].Record.SessionID != oldPane.Status.ProcessSession.SessionID || candidates[0].Record.ThreadID != oldPane.Status.ProcessSession.ThreadID || candidates[0].HostLost != nil {
				t.Fatalf("resume candidate %+v", candidates)
			}
			if row := agentRow(t, store, b.Agent); row.Status != resourcegraph.StatusOffline || !reflect.DeepEqual(row.Actions, []registryview.Action{registryview.ActionResume, registryview.ActionDelete}) {
				t.Fatalf("retired lost host row: %s %v", row.Status, row.Actions)
			}
			observer := remoteProcessObserver{ctx: context.Background(), registry: reg, registryPath: store.Path(), binding: b}
			if snapshot, err := observer.Observe(b); err != nil || snapshot.State != processhost.StateRetired || snapshot.Exit != nil {
				t.Fatalf("retired observation invented exit: %+v %v", snapshot, err)
			}
			// The same record again is an unchanged success with no write.
			raw, _ := os.ReadFile(store.Path())
			stdout.Reset()
			if err := markOfflineCommand(store, localipc.Process).Run([]string{"mark-offline", "uid:" + b.Agent, "-o", "receipt"}, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			if again, _ := os.ReadFile(store.Path()); !bytes.Equal(raw, again) {
				t.Fatal("no-op rewrote Registry")
			}
			var receipt struct {
				Operation    string `json:"operation"`
				AffectedUIDs []struct {
					Action string `json:"action"`
				} `json:"affectedUIDs"`
				Effects map[string]string `json:"effects"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil || receipt.Operation != "agent.mark-offline" || receipt.Effects["runtime"] != "unchanged" || len(receipt.AffectedUIDs) != 2 || receipt.AffectedUIDs[0].Action != "unchanged" {
				t.Fatalf("receipt %s %v", stdout.String(), err)
			}
		})
	}
}

func TestAgentMarkOfflineRefusesWithoutWrites(t *testing.T) {
	for _, tc := range []struct {
		name, token string
		read        func(coremetadata.ProcessActivation) processIdentityReader
		change      func(*coremetadata.Registry, processhost.Binding)
	}{
		{name: "owner-live", token: mutationruntime.MarkOfflineOwnerPresent, read: func(a coremetadata.ProcessActivation) processIdentityReader {
			return func(pid int) (coremetadata.ProcessIdentity, int, error) {
				if pid == a.HostProcess.PID {
					return a.HostProcess, 0, nil
				}
				return absentProcess(pid)
			}
		}},
		{name: "child-live", token: mutationruntime.MarkOfflineOwnerPresent, read: func(a coremetadata.ProcessActivation) processIdentityReader {
			return func(pid int) (coremetadata.ProcessIdentity, int, error) {
				if pid == a.Child.PID {
					return a.Child, 0, nil
				}
				return absentProcess(pid)
			}
		}},
		{name: "unprovable", token: mutationruntime.MarkOfflineOwnerPresent, read: func(coremetadata.ProcessActivation) processIdentityReader {
			return func(int) (coremetadata.ProcessIdentity, int, error) {
				return coremetadata.ProcessIdentity{}, 0, errors.New("permission denied")
			}
		}},
		{name: "other-uid", token: mutationruntime.MarkOfflineOwnerPresent, change: func(reg *coremetadata.Registry, b processhost.Binding) {
			pane, _ := reg.Pane(b.Pane)
			pane.Status.Activation.Process.HostProcess.OwnerUID++
		}},
		{name: "no-conversation", token: mutationruntime.MarkOfflineConversationUnavailable, change: func(reg *coremetadata.Registry, b processhost.Binding) {
			pane, _ := reg.Pane(b.Pane)
			pane.Status.ProcessSession.SessionID = ""
		}},
		{name: "tmux", token: mutationruntime.MarkOfflineProcessUnsupported, change: func(reg *coremetadata.Registry, b processhost.Binding) {
			pane, _ := reg.Pane(b.Pane)
			pane.Spec.Runtime = coremetadata.PaneRuntimeSpec{}
			pane.Status.ProcessSession = nil
			pane.Status.Activation = coremetadata.PaneActivation{}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, b, activation := hostLostStore(t, aiModeClaude)
			if tc.change != nil {
				if _, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error { tc.change(reg, b); return nil }); err != nil {
					t.Fatal(err)
				}
			}
			read := processIdentityReader(localipc.Process)
			if tc.read != nil {
				read = tc.read(activation)
			}
			raw, _ := os.ReadFile(store.Path())
			var stdout, stderr bytes.Buffer
			err := markOfflineCommand(store, read).Run([]string{"mark-offline", "uid:" + b.Agent}, &stdout, &stderr)
			if err == nil || !strings.HasPrefix(err.Error(), tc.token+":") {
				t.Fatalf("err %v, want %s", err, tc.token)
			}
			if after, _ := os.ReadFile(store.Path()); !bytes.Equal(raw, after) || stdout.Len() != 0 {
				t.Fatal("refusal wrote")
			}
		})
	}
}

// The transaction proves absence again and compares the exact activation.
func TestAgentMarkOfflineRechecksInsideTransaction(t *testing.T) {
	for _, which := range []string{"revived", "replaced", "canceled", "retired-other-generation"} {
		t.Run(which, func(t *testing.T) {
			store, b, activation := hostLostStore(t, aiModeCodex)
			reg, _ := store.LoadReadOnly()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reads := 0
			read := func(pid int) (coremetadata.ProcessIdentity, int, error) {
				reads++
				if which == "revived" && reads > 2 && pid == activation.Child.PID {
					return activation.Child, 0, nil
				}
				return absentProcess(pid)
			}
			var before []byte
			update := func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
				switch which {
				case "replaced":
					if _, err := store.Update(func(r *coremetadata.Registry) error {
						pane, _ := r.Pane(b.Pane)
						pane.Status.Activation.Process.Child.Start = "other"
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				case "canceled":
					// The caller gives up while the transaction waits for the lock.
					cancel()
				case "retired-other-generation":
					// Another generation of the same Pane started and was retired
					// after the caller read the first one.
					var other coremetadata.ProcessActivation
					if _, err := store.Update(func(r *coremetadata.Registry) error {
						pane, _ := r.Pane(b.Pane)
						next := pane.Status.Activation.Process.Binding
						next.Generation, next.OperationID = "new-generation", "new-operation"
						pane.Status.Activation.Process.Binding = next
						pane.Status.Activation.Generation, pane.Status.Activation.OperationID = next.Generation, next.OperationID
						pane.Status.ProcessSession.Binding, pane.Status.ProcessSession.ConnectionID = next, next.OperationID
						other = *pane.Status.Activation.Process
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					if _, _, err := store.UpdateConvergent(func(r *coremetadata.Registry) error {
						return intmetadata.DefaultMutator().MarkProcessHostLostOffline(r, other)
					}); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				if before, err = os.ReadFile(store.Path()); err != nil {
					t.Fatal(err)
				}
				return store.UpdateConvergent(fn)
			}
			marker := mutationruntime.ProcessAgentMarkOffline{Update: update, ReadIdentity: mutationruntime.ProcessIdentityReader(read)}
			err := marker.MarkExact(ctx, reg, b.Agent)
			if which == "canceled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err %v, want context.Canceled", err)
				}
			} else {
				want := mutationruntime.MarkOfflineOwnerPresent
				if which != "revived" {
					want = mutationruntime.MarkOfflineStale
				}
				var refusal *mutationruntime.MarkOfflineRefusal
				if !errors.As(err, &refusal) || refusal.Code != want {
					t.Fatalf("err %v, want %s", err, want)
				}
			}
			after, err := os.ReadFile(store.Path())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("refused transaction wrote the Registry")
			}
			latest, _ := store.LoadReadOnly()
			pane, _ := latest.Pane(b.Pane)
			if which == "retired-other-generation" {
				// The other generation's retirement is left exactly as it was.
				if !latest.ProcessHostLostOffline(b.Pane) || pane.Status.LastTermination.Generation != "new-generation" || marker.Changed {
					t.Fatalf("other generation changed: %+v changed=%v", pane.Status.LastTermination, marker.Changed)
				}
				return
			}
			if pane.Status.Activation.Process == nil || pane.Status.LastTermination != nil {
				t.Fatal("refused transaction retired the generation")
			}
		})
	}
}

func TestSDKMarkAgentOfflineMatchesCLI(t *testing.T) {
	store, b, _ := hostLostStore(t, aiModeClaude)
	state := filepath.Dir(filepath.Dir(store.Path()))
	client, err := sdk.NewClient(sdk.Options{ConfigDir: filepath.Join(state, "config"), StateDir: state, HomeDir: state})
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := store.LoadReadOnly()
	pane, _ := reg.Pane(b.Pane)
	var recorded core.ProcessBinding
	raw, _ := json.Marshal(pane.Status.ProcessSession.Binding)
	_ = json.Unmarshal(raw, &recorded)
	other := recorded
	other.Generation = "other"
	var refusal *sdk.Refusal
	if _, err = client.MarkAgentOffline(context.Background(), core.MarkAgentOfflineRequest{Ref: "uid:" + b.Agent, Binding: other}); !errors.As(err, &refusal) || refusal.Code != sdk.CodeMarkAgentOfflineStale {
		t.Fatal("foreign binding admitted", err)
	}
	result, err := client.MarkAgentOffline(context.Background(), core.MarkAgentOfflineRequest{Ref: "uid:" + b.Agent, Binding: recorded})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.AgentUID != b.Agent || result.PaneUID != b.Pane || result.Binding != recorded || result.Termination == nil || result.Termination.Source != "reconcile" || result.Termination.Classification != "unknown" || result.Termination.ExitCode != nil || result.Receipt.Operation != core.OperationMarkOfflineAgent {
		t.Fatalf("result %+v", result)
	}
	again, err := client.MarkAgentOffline(context.Background(), core.MarkAgentOfflineRequest{Ref: "uid:" + b.Agent})
	if err != nil || again.Changed || !reflect.DeepEqual(again.Termination, result.Termination) {
		t.Fatalf("no-op %+v %v", again, err)
	}
	var stdout, stderr bytes.Buffer
	if err = markOfflineCommand(store, localipc.Process).Run([]string{"mark-offline", "uid:" + b.Agent}, &stdout, &stderr); err != nil || !strings.HasPrefix(stdout.String(), "already offline") {
		t.Fatalf("CLI no-op %q %v", stdout.String(), err)
	}
	// A live owner is refused by both doors with the same token.
	live, lb, _ := hostLostStore(t, aiModeCodex)
	host := exec.Command("sleep", "5")
	if err = host.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Process.Kill(); _ = host.Wait() }()
	hostID, _, err := localipc.Process(host.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = live.Update(func(r *coremetadata.Registry) error {
		p, _ := r.Pane(lb.Pane)
		p.Status.Activation.Process.HostProcess = hostID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	liveState := filepath.Dir(filepath.Dir(live.Path()))
	liveClient, err := sdk.NewClient(sdk.Options{ConfigDir: filepath.Join(liveState, "config"), StateDir: liveState, HomeDir: liveState})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = liveClient.MarkAgentOffline(context.Background(), core.MarkAgentOfflineRequest{Ref: "uid:" + lb.Agent}); !errors.As(err, &refusal) || refusal.Code != sdk.CodeMarkAgentOfflineOwnerPresent {
		t.Fatal("SDK live owner", err)
	}
	if err = markOfflineCommand(live, localipc.Process).Run([]string{"mark-offline", "uid:" + lb.Agent}, &stdout, &stderr); err == nil || !strings.HasPrefix(err.Error(), string(sdk.CodeMarkAgentOfflineOwnerPresent)) {
		t.Fatal("CLI live owner", err)
	}
}
