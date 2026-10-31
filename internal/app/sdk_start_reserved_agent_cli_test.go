package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const startReservedSecret = "hunter2-start-secret"

func newStartFixture(t *testing.T) reserveFixture {
	t.Helper()
	copiedCLI(t, "PMX_TEST_CLI")
	return newReserveFixture(t)
}

func (r reserveFixture) tamper(t *testing.T, agent string, edit func(*createdLaunchRecord)) {
	t.Helper()
	record, err := readCreatedLaunch(r.launchPath(agent))
	if err != nil || record == nil {
		t.Fatalf("read frozen launch: %v %v", record, err)
	}
	edit(record)
	if err = writeCreatedLaunch(r.launchPath(agent), *record); err != nil {
		t.Fatal(err)
	}
}

func (r reserveFixture) reserve(t *testing.T, ctx context.Context) core.ReserveAgentResult {
	t.Helper()
	result, err := r.client.ReserveAgent(ctx, r.request())
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	return result
}

func (r reserveFixture) stop(t *testing.T, ctx context.Context, ref string, binding core.ProcessBinding) {
	t.Helper()
	if _, err := r.client.StopAgent(ctx, core.StopAgentRequest{Ref: ref, Binding: binding}); err != nil {
		_ = os.WriteFile(r.f.root+"/"+firstInputCleanupPending, []byte("start reserved cleanup unconfirmed"), 0600)
		t.Errorf("cleanup stop: %v", err)
	}
}

func wantStartRefusal(t *testing.T, err error, code projmux.Code) {
	t.Helper()
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != code {
		t.Fatalf("want refusal %s, got %T %v", code, err, err)
	}
}

func TestPublicStartReservedAgentStartsFrozenLaunch(t *testing.T) {
	r := newStartFixture(t)
	ctx := reserveContext(t)
	reserved := r.reserve(t, ctx)
	ref := "uid:" + reserved.Agent.Metadata.UID
	r.tamper(t, reserved.Agent.Metadata.UID, func(record *createdLaunchRecord) {
		record.Command.Env = append(record.Command.Env, "PMX_TEST_START_SECRET="+startReservedSecret)
	})
	started, err := r.client.StartReservedAgent(ctx, core.StartReservedAgentRequest{Agent: ref})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer r.stop(t, ctx, ref, started.Binding)
	if started.Binding != reserved.Binding || started.Agent.Metadata.UID != reserved.Agent.Metadata.UID || len(started.Projection.Items) != 1 {
		t.Fatalf("a start must keep the reserved Binding: reserved %+v started %+v", reserved.Binding, started)
	}
	reg, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	pane, ok := reg.Pane(started.Binding.PaneUID)
	if !ok || pane.Status.Activation.Process == nil {
		t.Fatalf("started Pane %+v has no process activation", pane)
	}
	if _, err = os.Stat(r.launchPath(reserved.Agent.Metadata.UID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a started Agent keeps its frozen launch: %v", err)
	}
	// The stored environment reaches the provider, never the result.
	encoded, err := json.Marshal(started)
	if err != nil || bytes.Contains(encoded, []byte(startReservedSecret)) {
		t.Fatalf("a start result carries the stored environment: %v", err)
	}
	if _, err = r.client.StartReservedAgent(ctx, core.StartReservedAgentRequest{Agent: ref}); err == nil {
		t.Fatal("a started Agent was started again")
	}
	wantStartRefusal(t, err, projmux.CodeReservationMissing)
}

func TestPublicStartReservedAgentRetriesAfterEarlyFailure(t *testing.T) {
	r := newStartFixture(t)
	ctx := reserveContext(t)
	reserved := r.reserve(t, ctx)
	uid := reserved.Agent.Metadata.UID
	var good string
	r.tamper(t, uid, func(record *createdLaunchRecord) {
		good, record.Command.Path = record.Command.Path, "/nonexistent/projmux-start-reserved-provider"
	})
	if _, err := r.client.StartReservedAgent(ctx, core.StartReservedAgentRequest{Agent: "uid:" + uid}); err == nil {
		t.Fatal("a start whose provider cannot run succeeded")
	}
	reg, err := r.f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if agent, ok := reg.Agent(uid); !ok || agent.Status.Phase != coremetadata.PhasePending || !agent.Status.Activation.IsZero() {
		t.Fatalf("a failure before the provider must keep the Pending reservation: %+v %v", agent, ok)
	}
	record, err := readCreatedLaunch(r.launchPath(uid))
	if err != nil || record == nil || record.Starting || record.StartingOwner != nil {
		t.Fatalf("the failed start left its mark: %+v %v", record, err)
	}
	r.tamper(t, uid, func(record *createdLaunchRecord) { record.Command.Path = good })
	started, err := r.client.StartReservedAgent(ctx, core.StartReservedAgentRequest{Agent: "uid:" + uid})
	if err != nil {
		t.Fatalf("the retry must start the kept reservation: %v", err)
	}
	r.stop(t, ctx, "uid:"+uid, started.Binding)
}

func TestPublicStartReservedAgentRefusesWhatIsNotItsReservation(t *testing.T) {
	r := newStartFixture(t)
	ctx := reserveContext(t)
	if _, err := r.client.StartReservedAgent(ctx, core.StartReservedAgentRequest{Agent: "uid:agt-unknown"}); err == nil {
		t.Fatal("an unknown Agent was started")
	} else {
		wantStartRefusal(t, err, projmux.CodeReservationMissing)
	}
	if _, err := r.client.StartReservedAgent(ctx, core.StartReservedAgentRequest{Agent: "reserved"}); err == nil {
		t.Fatal("a Agent name was accepted")
	} else {
		wantStartRefusal(t, err, projmux.CodeCreateRequestInvalid)
	}
	cases := []struct {
		name string
		code projmux.Code
		edit func(*createdLaunchRecord)
	}{
		{"version", projmux.CodeReservationStale, func(rec *createdLaunchRecord) { rec.Version++ }},
		{"generation", projmux.CodeReservationStale, func(rec *createdLaunchRecord) { rec.Binding.Generation += "x" }},
		{"provider", projmux.CodeReservationStale, func(rec *createdLaunchRecord) { rec.Provider = "codex" }},
		{"relative command", projmux.CodeReservationStale, func(rec *createdLaunchRecord) { rec.Command.Path = "claude" }},
		{"no environment", projmux.CodeReservationStale, func(rec *createdLaunchRecord) { rec.Command.Env = nil }},
		{"launch file proof", projmux.CodeReservationStale, func(rec *createdLaunchRecord) { rec.Files = map[string]string{"/nonexistent": "0"} }},
		{"started by a live worker", projmux.CodeReservationStarting, func(rec *createdLaunchRecord) {
			self, _, err := localipc.Process(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			rec.Starting, rec.StartingOwner = true, &self
		}},
		{"started by an unknown worker", projmux.CodeReservationStarting, func(rec *createdLaunchRecord) { rec.Starting = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reserved := r.reserve(t, ctx)
			uid := reserved.Agent.Metadata.UID
			r.tamper(t, uid, func(record *createdLaunchRecord) {
				record.Command.Env = append(record.Command.Env, "PMX_TEST_START_SECRET="+startReservedSecret)
				tc.edit(record)
			})
			before, err := os.ReadFile(r.launchPath(uid))
			if err != nil {
				t.Fatal(err)
			}
			result, err := r.client.StartReservedAgent(ctx, core.StartReservedAgentRequest{Agent: "uid:" + uid})
			wantStartRefusal(t, err, tc.code)
			if strings.Contains(err.Error(), startReservedSecret) || strings.Contains(err.Error(), "PMX_TEST_START_SECRET") {
				t.Fatalf("a refusal carries the stored environment: %v", err)
			}
			encoded, _ := json.Marshal(result)
			if bytes.Contains(encoded, []byte(startReservedSecret)) {
				t.Fatalf("a refused result carries the stored environment: %s", encoded)
			}
			after, err := os.ReadFile(r.launchPath(uid))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("a refusal changed the frozen launch: %v", err)
			}
			reg, err := r.f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			if agent, ok := reg.Agent(uid); !ok || agent.Status.Phase != coremetadata.PhasePending {
				t.Fatalf("a refusal changed the Agent: %+v %v", agent, ok)
			}
		})
	}
}

func TestPublicStartReservedAgentRefusesADamagedLaunchFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(t *testing.T, path string)
	}{
		{"malformed json", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"unsafe permissions", func(t *testing.T, path string) {
			if err := os.Chmod(path, 0644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newStartFixture(t)
			ctx := reserveContext(t)
			reserved := r.reserve(t, ctx)
			uid := reserved.Agent.Metadata.UID
			tc.write(t, r.launchPath(uid))
			before, err := os.ReadFile(r.launchPath(uid))
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.client.StartReservedAgent(ctx, core.StartReservedAgentRequest{Agent: "uid:" + uid})
			wantStartRefusal(t, err, projmux.CodeReservationStale)
			after, readErr := os.ReadFile(r.launchPath(uid))
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatalf("a refusal changed the launch file: %v", readErr)
			}
			reg, regErr := r.f.store.LoadReadOnly()
			if regErr != nil {
				t.Fatal(regErr)
			}
			if agent, ok := reg.Agent(uid); !ok || agent.Status.Phase != coremetadata.PhasePending {
				t.Fatalf("a refusal changed the Agent: %+v %v", agent, ok)
			}
		})
	}
}

func TestPublicStartReservedAgentTakesOverFromAGoneWorker(t *testing.T) {
	r := newStartFixture(t)
	ctx := reserveContext(t)
	reserved := r.reserve(t, ctx)
	uid := reserved.Agent.Metadata.UID
	r.tamper(t, uid, func(record *createdLaunchRecord) {
		self, _, err := localipc.Process(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		// Same pid with another start time is a different process: the recorded worker is gone.
		self.Start += "-gone"
		record.Starting, record.StartingOwner = true, &self
	})
	started, err := r.client.StartReservedAgent(ctx, core.StartReservedAgentRequest{Agent: "uid:" + uid})
	if err != nil {
		t.Fatalf("a proven-gone worker's mark must not block a start: %v", err)
	}
	r.stop(t, ctx, "uid:"+uid, started.Binding)
}

func TestPublicStartReservedAgentLateFailureRollsBackLikeCreate(t *testing.T) {
	for _, newWindow := range []bool{false, true} {
		name := "existing window"
		if newWindow {
			name = "new window"
		}
		t.Run(name, func(t *testing.T) {
			r := newStartFixture(t)
			ctx := reserveContext(t)
			request := r.request()
			if newWindow {
				request.Window = ""
				request.NewWindow = &core.ReserveNewWindow{Name: "start-window"}
			}
			reserved, err := r.client.ReserveAgent(ctx, request)
			if err != nil {
				t.Fatalf("reserve: %v", err)
			}
			r.f.config(t, "[hooks.post-create]\nruntime = \"process\"\nrun = \"exit 7\"\n")
			result, err := r.client.StartReservedAgent(ctx, core.StartReservedAgentRequest{Agent: "uid:" + reserved.Agent.Metadata.UID})
			if err == nil || !strings.Contains(err.Error(), "process-post-create-hook-failed") {
				t.Fatalf("want the hook failure, got %v", err)
			}
			var detail *projmux.CreateProcessError
			var refusal *projmux.Refusal
			if !errors.As(err, &detail) || detail.Binding != reserved.Binding || result.Binding != reserved.Binding {
				t.Fatalf("the failure must carry the reserved Binding: result %+v error %v", result.Binding, err)
			}
			if !errors.As(err, &refusal) || refusal.AgentUID != reserved.Agent.Metadata.UID {
				t.Fatalf("the refusal must name the Agent: %v", err)
			}
			reg, regErr := r.f.store.LoadReadOnly()
			if regErr != nil {
				t.Fatal(regErr)
			}
			if _, ok := reg.Agent(reserved.Agent.Metadata.UID); ok {
				t.Fatal("a failure after the provider started left the Agent")
			}
			if _, ok := reg.Window(reserved.Binding.WindowUID); ok != !newWindow {
				t.Fatalf("Window present=%v after rollback, new Window=%v", ok, newWindow)
			}
			if _, statErr := os.Stat(r.launchPath(reserved.Agent.Metadata.UID)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("a rolled-back Agent keeps its frozen launch: %v", statErr)
			}
		})
	}
}

func TestPublicStartReservedAgentVerifyRejectionRollsBack(t *testing.T) {
	r := newStartFixture(t)
	ctx := reserveContext(t)
	reserved := r.reserve(t, ctx)
	uid := reserved.Agent.Metadata.UID
	mutationruntime.StartVerifyHook = func(mutationruntime.ProcessDetachedReply) error { return errors.New("injected start rejection") }
	t.Cleanup(func() { mutationruntime.StartVerifyHook = nil })
	_, err := r.client.StartReservedAgent(ctx, core.StartReservedAgentRequest{Agent: "uid:" + uid})
	if err == nil || !strings.Contains(err.Error(), "injected start rejection") {
		t.Fatalf("want the caller's rejection, got %v", err)
	}
	reg, regErr := r.f.store.LoadReadOnly()
	if regErr != nil {
		t.Fatal(regErr)
	}
	// The provider had started, so the worker rolls back like a failed create; nothing is kept.
	if _, ok := reg.Agent(uid); ok {
		t.Fatal("a started Agent the caller rejected was kept")
	}
	if _, statErr := os.Stat(r.launchPath(uid)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a rolled-back Agent keeps its frozen launch: %v", statErr)
	}
	if _, ok := reg.Window(r.f.window); !ok {
		t.Fatal("the existing Window was removed")
	}
}

func TestCreatedLaunchWithStartingOwnerReadsAsBefore(t *testing.T) {
	owner := coremetadata.ProcessIdentity{PID: 4242, Start: "100", OwnerUID: 1000}
	record := createdLaunchRecord{Version: reservedLaunchVersion, Provider: "claude", Starting: true, StartingOwner: &owner}
	path := t.TempDir() + "/launch.json"
	if err := writeCreatedLaunch(path, record); err != nil {
		t.Fatal(err)
	}
	// The shape 3b-1 wrote: its reader decodes without strictness and ignores a field it lacks.
	var old struct {
		Version  int
		Provider string
		Starting bool
	}
	found, err := readDeferredState(path, &old)
	if err != nil || !found || old.Version != reservedLaunchVersion || old.Provider != "claude" || !old.Starting {
		t.Fatalf("a launch with a StartingOwner must read as before: %+v %v %v", old, found, err)
	}
	got, err := readCreatedLaunch(path)
	if err != nil || got == nil || got.StartingOwner == nil || *got.StartingOwner != owner {
		t.Fatalf("the StartingOwner must round-trip: %+v %v", got, err)
	}
}

func TestStartReservedWireFieldsAreOmittedUnlessSet(t *testing.T) {
	encoded, err := json.Marshal(processDetachedRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"StartReserved", "StartAgent"} {
		if bytes.Contains(encoded, []byte(key)) {
			t.Fatalf("a request that is not a start carries %s: %s", key, encoded)
		}
	}
}
