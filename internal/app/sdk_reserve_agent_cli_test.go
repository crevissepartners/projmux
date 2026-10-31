package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

type reserveFixture struct {
	f      processCreateCLI
	client *projmux.Client
	state  string
	path   string
}

func newReserveFixture(t *testing.T) reserveFixture {
	t.Helper()
	copiedCLI(t, "PMX_TEST_CLI")
	f := newProcessCreateCLI(t)
	state := filepath.Join(f.root, "state", "projmux")
	c, err := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: filepath.Join(f.root, "config", "projmux"), StateDir: state, ProjmuxExecutable: f.binary})
	if err != nil {
		t.Fatal(err)
	}
	return reserveFixture{f: f, client: c, state: state, path: intmetadata.PathFor(state)}
}

func (r reserveFixture) request() core.ReserveAgentRequest {
	return core.ReserveAgentRequest{Project: "uid:" + r.f.project, Window: "uid:" + r.f.window, Provider: "claude", Profile: "none", Model: "stub-model", Effort: "low", Name: "reserved"}
}

func (r reserveFixture) launchPath(agent string) string {
	return mutationruntime.CreatedLaunchPath(r.path, agent)
}

func reserveContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestPublicReserveAgentFreezesLaunchAndStaysPending(t *testing.T) {
	r := newReserveFixture(t)
	ctx := reserveContext(t)
	result, err := r.client.ReserveAgent(ctx, r.request())
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	reg, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, ok := reg.Agent(result.Agent.Metadata.UID)
	if !ok || agent.Status.Phase != coremetadata.PhasePending || !agent.Status.Activation.IsZero() {
		t.Fatalf("reserved Agent %+v is not Pending without activation", agent)
	}
	pane, ok := reg.Pane(result.Binding.PaneUID)
	if !ok || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding.AgentUID != result.Binding.AgentUID || pane.Status.ProcessSession.Binding.Generation != result.Binding.Generation || !pane.Status.Activation.IsZero() {
		t.Fatalf("reserved Pane %+v does not hold the reserved Binding without an activation", pane)
	}
	path := r.launchPath(result.Agent.Metadata.UID)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("frozen launch: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("frozen launch mode %v, want 0600", info.Mode().Perm())
	}
	record, err := readCreatedLaunch(path)
	if err != nil || record == nil {
		t.Fatalf("read frozen launch: %v %v", record, err)
	}
	if record.Provider != "claude" || record.Starting || record.Command.Path == "" || record.Binding.Agent != result.Agent.Metadata.UID || record.Binding.Generation != result.Binding.Generation {
		t.Fatalf("frozen launch %+v", record)
	}
	if _, err = os.Stat(filepath.Join(r.state, "deferred-launches")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a reservation must not create deferred-launches: %v", err)
	}
	// The stub provider records every frame it reads; a reservation sends none.
	if raw, _ := os.ReadFile(r.f.trace); len(raw) != 0 {
		t.Fatalf("the provider read frames during a reservation: %q", raw)
	}
}

func TestPublicReserveAgentNewWindowReservationIsDeletable(t *testing.T) {
	r := newReserveFixture(t)
	ctx := reserveContext(t)
	req := r.request()
	req.Window = ""
	req.NewWindow = &core.ReserveNewWindow{Name: "reserved-window", Labels: []string{"tier=gold"}}
	result, err := r.client.ReserveAgent(ctx, req)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	reg, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	window, ok := reg.Window(result.Binding.WindowUID)
	if !ok || window.Metadata.Name != "reserved-window" || window.Metadata.Labels["tier"] != "gold" || window.Metadata.UID == r.f.window {
		t.Fatalf("new Window %+v", window)
	}
	if _, err = os.Stat(r.launchPath(result.Agent.Metadata.UID)); err != nil {
		t.Fatalf("frozen launch of the new-Window reservation: %v", err)
	}
	if _, err = r.client.Delete(ctx, core.DeleteRequest{Kind: core.KindAgent, Ref: "uid:" + result.Agent.Metadata.UID, Confirm: true}); err != nil {
		t.Fatalf("delete the new-Window reservation: %v", err)
	}
	after, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := after.Agent(result.Agent.Metadata.UID); ok {
		t.Fatal("deleted Agent remains")
	}
	if _, ok := after.Pane(result.Binding.PaneUID); ok {
		t.Fatal("deleted Agent's Pane remains")
	}
	if _, err = os.Stat(r.launchPath(result.Agent.Metadata.UID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted Agent's frozen launch remains: %v", err)
	}
	if _, ok := after.Window(r.f.window); !ok {
		t.Fatal("the existing Window was removed")
	}
}

func TestPublicReserveAgentRejectsRequestShape(t *testing.T) {
	r := newReserveFixture(t)
	ctx := reserveContext(t)
	both := r.request()
	both.NewWindow = &core.ReserveNewWindow{Name: "other"}
	neither := r.request()
	neither.Window = ""
	for name, req := range map[string]core.ReserveAgentRequest{"both": both, "neither": neither} {
		_, err := r.client.ReserveAgent(ctx, req)
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeCreateRequestInvalid {
			t.Fatalf("%s: %v, want %s", name, err, projmux.CodeCreateRequestInvalid)
		}
	}
}

func TestPublicReserveAgentRejectsCodex(t *testing.T) {
	r := newReserveFixture(t)
	ctx := reserveContext(t)
	before, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	req := r.request()
	req.Provider = "codex"
	_, err = r.client.ReserveAgent(ctx, req)
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeReserveProviderUnsupported {
		t.Fatalf("codex reserve: %v, want %s", err, projmux.CodeReserveProviderUnsupported)
	}
	after, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Agents) != len(before.Agents) || len(after.Panes) != len(before.Panes) {
		t.Fatalf("a refused reservation changed the Registry: %d/%d Agents, %d/%d Panes", len(after.Agents), len(before.Agents), len(after.Panes), len(before.Panes))
	}
}

// A launch that cannot be frozen leaves no Agent, no Pane and no new Window.
func TestPublicReserveAgentRollsBackOnFailure(t *testing.T) {
	r := newReserveFixture(t)
	ctx := reserveContext(t)
	before, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	// A regular file where the created-launches directory belongs refuses the freeze.
	if err = os.MkdirAll(r.state, 0700); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(r.state, mutationruntime.ReservedLaunchSidecar)
	if err = os.WriteFile(blocker, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	req := r.request()
	req.Window = ""
	req.NewWindow = &core.ReserveNewWindow{Name: "rolled-back"}
	_, err = r.client.ReserveAgent(ctx, req)
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("reserve with an unfreezable launch: %v, want a Refusal", err)
	}
	after, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Agents) != len(before.Agents) || len(after.Panes) != len(before.Panes) || len(after.Windows) != len(before.Windows) {
		t.Fatalf("a failed reservation left resources: Agents %d/%d Panes %d/%d Windows %d/%d", len(after.Agents), len(before.Agents), len(after.Panes), len(before.Panes), len(after.Windows), len(before.Windows))
	}
}

func TestPublicReserveAgentDeleteRemovesOnlyItsLaunch(t *testing.T) {
	r := newReserveFixture(t)
	ctx := reserveContext(t)
	first, err := r.client.ReserveAgent(ctx, r.request())
	if err != nil {
		t.Fatal(err)
	}
	second := r.request()
	second.Name = "reserved-two"
	other, err := r.client.ReserveAgent(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	keep, err := os.ReadFile(r.launchPath(other.Agent.Metadata.UID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.client.Delete(ctx, core.DeleteRequest{Kind: core.KindAgent, Ref: "uid:" + first.Agent.Metadata.UID, Confirm: true}); err != nil {
		t.Fatalf("delete reserved Agent: %v", err)
	}
	if _, err = os.Stat(r.launchPath(first.Agent.Metadata.UID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted Agent's frozen launch remains: %v", err)
	}
	got, err := os.ReadFile(r.launchPath(other.Agent.Metadata.UID))
	if err != nil || string(got) != string(keep) {
		t.Fatalf("another reservation's frozen launch changed: %v", err)
	}
	reg, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Agent(first.Agent.Metadata.UID); ok {
		t.Fatal("deleted Agent is still in the Registry")
	}
	if _, ok := reg.Agent(other.Agent.Metadata.UID); !ok {
		t.Fatal("the other reservation was deleted")
	}
}

func registryCounts(reg coremetadata.Registry) [3]int {
	return [3]int{len(reg.Agents), len(reg.Panes), len(reg.Windows)}
}

// A protected Agent survives the rollback of a reservation that could not freeze
// its launch; the failure must still name it.
func TestPublicReserveAgentProtectedFreezeFailureKeepsRecovery(t *testing.T) {
	r := newReserveFixture(t)
	ctx := reserveContext(t)
	if err := os.MkdirAll(r.state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.state, mutationruntime.ReservedLaunchSidecar), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	req := r.request()
	req.Labels = []string{coremetadata.LabelProtected + "=true"}
	result, err := r.client.ReserveAgent(ctx, req)
	var refusal *projmux.Refusal
	var detail *projmux.CreateProcessError
	if !errors.As(err, &refusal) || !errors.As(err, &detail) {
		t.Fatalf("protected freeze failure: %v, want a Refusal reaching a CreateProcessError", err)
	}
	if refusal.AgentUID == "" || result.Binding.AgentUID != refusal.AgentUID || detail.Binding.AgentUID != refusal.AgentUID {
		t.Fatalf("recovery identity lost: refusal %q result %q detail %q", refusal.AgentUID, result.Binding.AgentUID, detail.Binding.AgentUID)
	}
	reg, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, ok := reg.Agent(refusal.AgentUID)
	if !ok || !coremetadata.AgentProtected(*agent) {
		t.Fatalf("the protected Agent was removed: %v", ok)
	}
}

func TestPublicReserveAgentVerifyRejectionLeavesNothing(t *testing.T) {
	r := newReserveFixture(t)
	ctx := reserveContext(t)
	before, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	mutationruntime.ReserveVerifyHook = func(mutationruntime.ProcessDetachedReply) error { return errors.New("verification refused") }
	t.Cleanup(func() { mutationruntime.ReserveVerifyHook = nil })
	req := r.request()
	req.Window = ""
	req.NewWindow = &core.ReserveNewWindow{Name: "verify-refused"}
	result, err := r.client.ReserveAgent(ctx, req)
	if err == nil {
		t.Fatal("a rejected reservation was accepted")
	}
	// The caller waited for the worker's own rollback before returning.
	after, err2 := r.f.store.LoadReadOnly()
	if err2 != nil {
		t.Fatal(err2)
	}
	if registryCounts(before) != registryCounts(after) {
		t.Fatalf("a rejected reservation left resources: %v -> %v", registryCounts(before), registryCounts(after))
	}
	if result.Binding.AgentUID != "" {
		if _, e := os.Stat(r.launchPath(result.Binding.AgentUID)); !errors.Is(e, os.ErrNotExist) {
			t.Fatalf("a rejected reservation left its frozen launch: %v", e)
		}
	}
}

func TestPublicReserveAgentCancelAfterPreparedLeavesNothing(t *testing.T) {
	r := newReserveFixture(t)
	ctx, cancel := context.WithCancel(reserveContext(t))
	before, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	mutationruntime.ReserveVerifyHook = func(mutationruntime.ProcessDetachedReply) error { cancel(); return nil }
	t.Cleanup(func() { mutationruntime.ReserveVerifyHook = nil })
	if _, err = r.client.ReserveAgent(ctx, r.request()); err == nil {
		t.Fatal("a cancelled reservation was accepted")
	}
	after, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if registryCounts(before) != registryCounts(after) {
		t.Fatalf("a cancelled reservation left resources: %v -> %v", registryCounts(before), registryCounts(after))
	}
}
