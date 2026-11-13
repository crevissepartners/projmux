package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// windowProcessCommand is a Window delete over the in-memory Registry with the
// synthetic process probe and a fake tmux Window runtime. It never reaches tmux.
func windowProcessCommand(reg *coremetadata.Registry, deleter *processAgentDeleter, windows *fakeWindowDeleteRuntime) *deleteCommand {
	return &deleteCommand{
		store:          deleter.store,
		confirm:        &confirmer{},
		resolveKinds:   deleteRegistryKinds,
		windows:        windows,
		panes:          newFixturePaneDeleteRuntime(),
		lookupEnv:      func(name string) string { return testDeleteEnvironment[name] },
		newOperationID: func() (string, error) { return "op-delete", nil },
		processDeleter: deleter,
	}
}

func runWindowProcessDelete(cmd *deleteCommand, args ...string) (string, error) {
	var stdout bytes.Buffer
	err := cmd.Run(append([]string{"window"}, args...), &stdout, io.Discard)
	return stdout.String(), err
}

// addSecondProcessAgent copies the fixture's process Agent and Pane into a
// second Agent of the same tmux Window, with births the probe tells apart.
func addSecondProcessAgent(t *testing.T, reg *coremetadata.Registry, agentUID, paneUID string) (string, string) {
	t.Helper()
	agent, _ := reg.Agent(agentUID)
	pane, _ := reg.Pane(paneUID)
	na, np := agent.Clone(), pane.Clone()
	na.Metadata.UID, na.Metadata.Name = agentUID+"-two", agent.Metadata.Name+"-two"
	np.Metadata.UID, np.Metadata.Name = paneUID+"-two", pane.Metadata.Name+"-two"
	ref := *np.Metadata.OwnerRef
	ref.UID = na.Metadata.UID
	np.Metadata.OwnerRef = &ref
	na.Status.PaneRef = np.Metadata.UID
	binding := np.Status.ProcessSession.Binding
	binding.AgentUID, binding.PaneUID, binding.Generation, binding.OperationID = na.Metadata.UID, np.Metadata.UID, "gen-two", "op-two"
	activation := *np.Status.Activation.Process
	activation.Binding = binding
	activation.HostProcess = coremetadata.ProcessIdentity{PID: 21, OwnerUID: activation.HostProcess.OwnerUID, Start: "host-start-two"}
	activation.Child = coremetadata.ProcessIdentity{PID: 22, OwnerUID: activation.Child.OwnerUID, Start: "child-start-two"}
	np.Status.Activation = coremetadata.PaneActivation{Kind: coremetadata.RuntimeProcess, Generation: binding.Generation, AgentUID: na.Metadata.UID,
		OperationID: binding.OperationID, Process: &activation}
	session := *np.Status.ProcessSession
	session.Binding = binding
	np.Status.ProcessSession = &session
	reg.Agents = append(reg.Agents, na)
	reg.Panes = append(reg.Panes, np)
	for _, reservation := range slices.Clone(reg.NameReservations) {
		switch reservation.UID {
		case agentUID:
			reservation.Name, reservation.UID = na.Metadata.Name, na.Metadata.UID
		case paneUID:
			reservation.Name, reservation.UID = np.Metadata.Name, np.Metadata.UID
		default:
			continue
		}
		reg.NameReservations = append(reg.NameReservations, reservation)
	}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	return na.Metadata.UID, np.Metadata.UID
}

// A Window plan stops its running process Agent through the owner host before
// the tmux Window is killed, exactly as delete agent and delete pane do.
func TestDeleteWindowStopsRunningProcessAgentBeforeKillingTmux(t *testing.T) {
	reg, agent, pane := processDeleteFixture(t, aiModeClaude)
	a, _ := reg.Agent(agent)
	window := a.Metadata.OwnerUID()
	probe := &processDeleteProbe{host: true, child: true}
	probe.onStop = func() error {
		recordFixtureWait(t, reg, pane)
		probe.host, probe.child = false, false
		return nil
	}
	windows := newFixtureWindowDeleteRuntime()
	windows.killHook = func(windowLiveDeleteTarget) {
		if probe.stops != 1 || probe.host || probe.child {
			t.Errorf("tmux Window killed before the process provider retired: stops=%d host=%v child=%v", probe.stops, probe.host, probe.child)
		}
	}
	cmd := windowProcessCommand(reg, probe.deleter(reg), windows)
	out, err := runWindowProcessDelete(cmd, "uid:"+window, "--dry-run")
	if err != nil || !strings.Contains(out, "would stop through owner host pid=11") || probe.stops != 0 || len(windows.killed) != 0 {
		t.Fatalf("dry-run out=%q err=%v stops=%d", out, err, probe.stops)
	}
	out, err = runWindowProcessDelete(cmd, "uid:"+window, "--yes")
	if err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if probe.stops != 1 || len(windows.killed) != 1 {
		t.Fatalf("stops=%d killed=%d", probe.stops, len(windows.killed))
	}
	for _, want := range []string{"stopped through its owner host and confirmed its exact Wait receipt", "runtime=stopped"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if _, ok := reg.Agent(agent); ok {
		t.Fatal("Agent survived")
	}
	if _, ok := reg.Window(window); ok {
		t.Fatal("Window survived")
	}
}

// An owner and child that are both gone leave nothing to stop: the Window delete
// is Registry-only for the Agent and sends no Stop request.
func TestDeleteWindowUnknownProcessAgentIsRegistryOnly(t *testing.T) {
	reg, agent, _ := processDeleteFixture(t, aiModeClaude)
	a, _ := reg.Agent(agent)
	window := a.Metadata.OwnerUID()
	probe := &processDeleteProbe{}
	windows := newFixtureWindowDeleteRuntime()
	out, err := runWindowProcessDelete(windowProcessCommand(reg, probe.deleter(reg), windows), "uid:"+window, "--yes")
	if err != nil || probe.stops != 0 || len(windows.killed) != 1 || !strings.Contains(out, "registry-only") || !strings.Contains(out, "runtime=unknown evidence=owner-host-and-child-absent") {
		t.Fatalf("out=%q err=%v stops=%d killed=%d", out, err, probe.stops, len(windows.killed))
	}
	if _, ok := reg.Agent(agent); ok {
		t.Fatal("Agent survived")
	}
}

// The same injected stop failure is refused with the same text by delete agent
// and by delete window; the Window path leaves the Registry and tmux untouched
// and withdraws the recorded intent.
func TestDeleteWindowProcessStopFailureMatchesDeleteAgent(t *testing.T) {
	for name, tc := range map[string]struct {
		probe processDeleteProbe
		token string
	}{
		"host unreachable": {probe: processDeleteProbe{host: true, child: true, onStop: func() error {
			return errors.New("process-host-unavailable: dial unix: connection refused")
		}}, token: processDeleteHostUnavailableToken},
		"stop unconfirmed": {probe: processDeleteProbe{host: true, child: true}, token: processDeleteStopUnconfirmedToken},
	} {
		t.Run(name, func(t *testing.T) {
			var texts []string
			for _, viaWindow := range []bool{false, true} {
				reg, agent, _ := processDeleteFixture(t, aiModeClaude)
				a, _ := reg.Agent(agent)
				before := reg.Clone()
				probe := tc.probe
				windows := newFixtureWindowDeleteRuntime()
				var err error
				if viaWindow {
					_, err = runWindowProcessDelete(windowProcessCommand(reg, probe.deleter(reg), windows), "uid:"+a.Metadata.OwnerUID(), "--yes")
				} else {
					_, err = runProcessDelete(reg, probe.deleter(reg), "uid:"+agent, "--yes")
				}
				requireProcessDeleteToken(t, err, tc.token)
				texts = append(texts, err.Error())
				requireRegistryUnchanged(t, "refusal", before, *reg)
				if len(windows.killed) != 0 {
					t.Fatalf("viaWindow=%v: tmux Window killed after a refusal", viaWindow)
				}
			}
			if texts[0] != texts[1] {
				t.Fatalf("refusal text differs:\nagent:  %q\nwindow: %q", texts[0], texts[1])
			}
		})
	}
}

// Two process Agents in one Window stop one at a time. When the second cannot
// be stopped, the first is already Offline and resumable; the Registry keeps
// every row and tmux is not touched.
func TestDeleteWindowSecondAgentStopFailureLeavesFirstOffline(t *testing.T) {
	reg, agent, pane := processDeleteFixture(t, aiModeClaude)
	second, secondPane := addSecondProcessAgent(t, reg, agent, pane)
	a, _ := reg.Agent(agent)
	window := a.Metadata.OwnerUID()
	probe := &processDeleteProbe{host: true, child: true}
	deleter := probe.deleter(reg)
	deleter.alive = func(birth coremetadata.ProcessIdentity) bool {
		if strings.HasSuffix(birth.Start, "-two") {
			return true
		}
		return strings.HasPrefix(birth.Start, "host") && probe.host || strings.HasPrefix(birth.Start, "child") && probe.child
	}
	var stopped []string
	deleter.stop = func(_ context.Context, _ coremetadata.Registry, target coremetadata.Agent) error {
		stopped = append(stopped, target.Metadata.UID)
		if target.Metadata.UID == second {
			// The owner accepts Stop but never records a Wait receipt.
			return nil
		}
		recordFixtureWait(t, reg, pane)
		probe.host, probe.child = false, false
		return nil
	}
	windows := newFixtureWindowDeleteRuntime()
	_, err := runWindowProcessDelete(windowProcessCommand(reg, deleter, windows), "uid:"+window, "--yes")
	requireProcessDeleteToken(t, err, processDeleteStopUnconfirmedToken)
	if !reflect.DeepEqual(stopped, []string{agent, second}) {
		t.Fatalf("stop order = %v", stopped)
	}
	first, ok := reg.Agent(agent)
	if !ok || first.Status.Phase != coremetadata.PhaseOffline {
		t.Fatalf("first Agent = %+v, want Offline", first)
	}
	if _, ok := reg.Pane(pane); !ok {
		t.Fatal("first Pane was removed")
	}
	if _, ok := reg.Agent(second); !ok {
		t.Fatal("second Agent was removed")
	}
	if _, ok := reg.Pane(secondPane); !ok {
		t.Fatal("second Pane was removed")
	}
	if _, ok := reg.Window(window); !ok || len(windows.killed) != 0 {
		t.Fatalf("Window removed or tmux killed: killed=%d", len(windows.killed))
	}
}

// A stoppable Agent and an unknown Agent share a Window: only the running one
// is stopped, and the whole Window is then deleted.
func TestDeleteWindowStopsOnlyRunningAgentOfMixedRuntimes(t *testing.T) {
	reg, agent, pane := processDeleteFixture(t, aiModeClaude)
	second, _ := addSecondProcessAgent(t, reg, agent, pane)
	a, _ := reg.Agent(agent)
	window := a.Metadata.OwnerUID()
	probe := &processDeleteProbe{host: true, child: true}
	deleter := probe.deleter(reg)
	deleter.alive = func(birth coremetadata.ProcessIdentity) bool {
		if strings.HasSuffix(birth.Start, "-two") {
			return false
		}
		return strings.HasPrefix(birth.Start, "host") && probe.host || strings.HasPrefix(birth.Start, "child") && probe.child
	}
	var stopped []string
	deleter.stop = func(_ context.Context, _ coremetadata.Registry, target coremetadata.Agent) error {
		stopped = append(stopped, target.Metadata.UID)
		recordFixtureWait(t, reg, pane)
		probe.host, probe.child = false, false
		return nil
	}
	windows := newFixtureWindowDeleteRuntime()
	out, err := runWindowProcessDelete(windowProcessCommand(reg, deleter, windows), "uid:"+window, "--yes")
	if err != nil || !reflect.DeepEqual(stopped, []string{agent}) || len(windows.killed) != 1 {
		t.Fatalf("out=%q err=%v stopped=%v killed=%d", out, err, stopped, len(windows.killed))
	}
	for _, uid := range []string{agent, second} {
		if _, ok := reg.Agent(uid); ok {
			t.Fatalf("Agent %s survived", uid)
		}
	}
	if !strings.Contains(out, "runtime=stopped") || !strings.Contains(out, "runtime=unknown") {
		t.Fatalf("output lacks both runtimes:\n%s", out)
	}
}

// requireRegistryUnchanged compares two Registries exactly and, on a
// difference, prints the differing fields so a failure names what changed.
func requireRegistryUnchanged(t *testing.T, label string, before, after coremetadata.Registry) {
	t.Helper()
	if reflect.DeepEqual(before, after) {
		return
	}
	render := func(r coremetadata.Registry) []string {
		data, err := json.MarshalIndent(r, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(string(data), "\n")
	}
	want, got := render(before), render(after)
	var diff []string
	for i := 0; i < len(want) || i < len(got); i++ {
		var w, g string
		if i < len(want) {
			w = want[i]
		}
		if i < len(got) {
			g = got[i]
		}
		if w != g {
			diff = append(diff, "line "+strconv.Itoa(i+1)+": before "+strings.TrimSpace(w)+" | after "+strings.TrimSpace(g))
		}
	}
	t.Fatalf("%s changed the Registry:\n%s", label, strings.Join(diff, "\n"))
}

// afterStopUpdate wraps the store so the first Registry write after a process
// Stop is observed: it fails with failWith when set, otherwise it runs and then
// calls change on the stored Registry. That write is the one that records the
// tmux intent, so it separates a stop that succeeded from the steps after it.
func afterStopUpdate(store *resourceStore, probe *processDeleteProbe, failWith error, change func(*coremetadata.Registry)) {
	inner := store.update
	fired := false
	store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		if probe.stops == 0 || fired {
			return inner(fn)
		}
		fired = true
		if failWith != nil {
			return coremetadata.Registry{}, failWith
		}
		got, err := inner(fn)
		if err == nil && change != nil {
			_, err = inner(func(r *coremetadata.Registry) error {
				change(r)
				return nil
			})
		}
		return got, err
	}
}

// stoppedProbe is a running owner whose Stop records its exact Wait.
func stoppedProbe(t *testing.T, reg *coremetadata.Registry, pane string) *processDeleteProbe {
	probe := &processDeleteProbe{host: true, child: true}
	probe.onStop = func() error {
		recordFixtureWait(t, reg, pane)
		probe.host, probe.child = false, false
		return nil
	}
	return probe
}

// requireNoIntentLeft fails when a Pane still carries this delete's intentional
// receipt: a refused delete must withdraw what it recorded.
func requireNoIntentLeft(t *testing.T, reg *coremetadata.Registry) {
	t.Helper()
	for _, pane := range reg.Panes {
		if pane.Status.LastTermination != nil && pane.Status.LastTermination.OperationID == "op-delete" {
			t.Fatalf("Pane %s kept the refused delete's intentional receipt", pane.Metadata.UID)
		}
	}
}

// The stop precedes the intent record. When the intent cannot be made durable
// after the provider was stopped, nothing live is touched: tmux is not killed,
// every row stays, and the stopped Agent is Offline and resumable. This is the
// same kind of outcome as a failed stop of a second Agent.
func TestDeleteWindowIntentFailureAfterStopLeavesAgentOffline(t *testing.T) {
	reg, agent, pane := processDeleteFixture(t, aiModeClaude)
	a, _ := reg.Agent(agent)
	window := a.Metadata.OwnerUID()
	probe := stoppedProbe(t, reg, pane)
	deleter := probe.deleter(reg)
	afterStopUpdate(deleter.store, probe, errors.New("injected intent write failure"), nil)
	windows := newFixtureWindowDeleteRuntime()
	_, err := runWindowProcessDelete(windowProcessCommand(reg, deleter, windows), "uid:"+window, "--yes")
	if err == nil || !strings.Contains(err.Error(), "intentional termination evidence was not made durable") {
		t.Fatalf("err = %v", err)
	}
	if probe.stops != 1 || len(windows.killed) != 0 {
		t.Fatalf("stops=%d killed=%d", probe.stops, len(windows.killed))
	}
	got, ok := reg.Agent(agent)
	if !ok || got.Status.Phase != coremetadata.PhaseOffline {
		t.Fatalf("Agent = %+v, want Offline and present", got)
	}
	if _, ok := reg.Window(window); !ok {
		t.Fatal("Window was removed")
	}
	requireNoIntentLeft(t, reg)
}

// The signed process evidence is verified again inside the lock. If an Agent's
// evidence changed after the stop, the delete is refused with the existing
// token and withdraws its intent; tmux and the rows stay.
func TestDeleteWindowProcessEvidenceChangedAfterStopIsRefused(t *testing.T) {
	reg, agent, pane := processDeleteFixture(t, aiModeClaude)
	a, _ := reg.Agent(agent)
	window := a.Metadata.OwnerUID()
	probe := stoppedProbe(t, reg, pane)
	deleter := probe.deleter(reg)
	afterStopUpdate(deleter.store, probe, nil, func(r *coremetadata.Registry) {
		changed, _ := r.Agent(agent)
		changed.Status.Phase = coremetadata.PhaseFailed
	})
	windows := newFixtureWindowDeleteRuntime()
	_, err := runWindowProcessDelete(windowProcessCommand(reg, deleter, windows), "uid:"+window, "--yes")
	requireProcessDeleteToken(t, err, processDeleteRefusedToken)
	if !strings.Contains(err.Error(), "process Agent evidence changed") || len(windows.killed) != 0 {
		t.Fatalf("err = %v killed=%d", err, len(windows.killed))
	}
	if _, ok := reg.Window(window); !ok {
		t.Fatal("Window was removed")
	}
	requireNoIntentLeft(t, reg)
}

// A plan that changes between the stop and the locked re-verification is
// refused, not executed: the cascade the user approved is not the one left.
func TestDeleteWindowPlanChangedAfterStopIsRefused(t *testing.T) {
	reg, agent, pane := processDeleteFixture(t, aiModeClaude)
	a, _ := reg.Agent(agent)
	window := a.Metadata.OwnerUID()
	probe := stoppedProbe(t, reg, pane)
	deleter := probe.deleter(reg)
	afterStopUpdate(deleter.store, probe, nil, func(r *coremetadata.Registry) {
		// A late Agent with no Pane joins the Window: the cascade the user
		// approved is no longer the one that is left.
		source, _ := r.Agent(agent)
		late := source.Clone()
		late.Metadata.UID, late.Metadata.Name = agent+"-late", source.Metadata.Name+"-late"
		late.Status.PaneRef, late.Status.Phase, late.Status.LastTermination = "", coremetadata.PhasePending, nil
		r.Agents = append(r.Agents, late)
		for _, reservation := range slices.Clone(r.NameReservations) {
			if reservation.UID == agent {
				reservation.Name, reservation.UID = late.Metadata.Name, late.Metadata.UID
				r.NameReservations = append(r.NameReservations, reservation)
			}
		}
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	windows := newFixtureWindowDeleteRuntime()
	_, err := runWindowProcessDelete(windowProcessCommand(reg, deleter, windows), "uid:"+window, "--yes")
	if err == nil || !strings.Contains(err.Error(), "the cascade plan changed between preflight and execution") || len(windows.killed) != 0 {
		t.Fatalf("err = %v killed=%d", err, len(windows.killed))
	}
	if _, ok := reg.Window(window); !ok {
		t.Fatal("Window was removed")
	}
	requireNoIntentLeft(t, reg)
}

// A Window delete drops the frozen reservation launch of the process Agent it
// deleted, after the commit, and keeps the launch of an Agent it did not touch.
func TestDeleteWindowRemovesReservedLaunchOfDeletedProcessAgentOnly(t *testing.T) {
	reg, agent, pane := processDeleteFixture(t, aiModeClaude)
	a, _ := reg.Agent(agent)
	window := a.Metadata.OwnerUID()
	probe := &processDeleteProbe{host: true, child: true}
	probe.onStop = func() error {
		recordFixtureWait(t, reg, pane)
		probe.host, probe.child = false, false
		return nil
	}
	deleter := probe.deleter(reg)
	state := t.TempDir()
	deleter.store.stateDir = func() (string, error) { return state, nil }
	registryPath := deleter.shared().RegistryPath
	own := mutationruntime.CreatedLaunchPath(registryPath, agent)
	other := mutationruntime.CreatedLaunchPath(registryPath, "agent-unrelated-reservation")
	for _, launch := range []string{own, other} {
		if err := os.MkdirAll(filepath.Dir(launch), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(launch, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := windowProcessCommand(reg, deleter, newFixtureWindowDeleteRuntime())
	if out, err := runWindowProcessDelete(cmd, "uid:"+window, "--yes"); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Fatalf("deleted Agent's reservation launch survived: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("unrelated reservation launch was removed: %v", err)
	}
}
