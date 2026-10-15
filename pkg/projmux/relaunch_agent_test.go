package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestRelaunchAgentAdmissionAndProcessRefusal(t *testing.T) {
	c, _, uid, _ := sdkStopFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: "uid:" + uid}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		req  core.RelaunchAgentRequest
		code Code
	}{
		{core.RelaunchAgentRequest{}, CodeSelectorInvalid},
		{core.RelaunchAgentRequest{Ref: "uid:missing", Reset: []string{"invalid"}}, CodeRelaunchRequestInvalid},
		{core.RelaunchAgentRequest{Ref: "uid:" + uid, SocketPath: "relative"}, CodeRuntimeRequestInvalid},
		{core.RelaunchAgentRequest{Ref: "uid:" + uid, DryRun: true}, CodeRelaunchProcessUnsupported},
	} {
		_, err := c.RelaunchAgent(context.Background(), tc.req)
		var refused *Refusal
		if !errors.As(err, &refused) || refused.Code != tc.code {
			t.Fatalf("want %s got %v", tc.code, err)
		}
	}
}

func TestRelaunchLayerConflictAndExactRecovery(t *testing.T) {
	none := ""
	for _, q := range []core.RelaunchAgentRequest{
		{Model: "sonnet", Reset: []string{"model"}},
		{Instructions: &none, Reset: []string{"instructions"}},
		{Reset: []string{"invalid"}},
	} {
		err := validateRelaunchLayers(q)
		var refused *Refusal
		if !errors.As(err, &refused) || refused.Code != CodeRelaunchRequestInvalid {
			t.Fatalf("%+v: %v", q, err)
		}
	}
	if err := validateRelaunchLayers(core.RelaunchAgentRequest{Instructions: &none, Reset: []string{"model", "effort"}}); err != nil {
		t.Fatal(err)
	}
	// Multiple resets must form one CLI flag: a repeated --reset replaces the
	// earlier list. Recovery cannot silently lose a requested layer reset.
	got := relaunchRecovery(core.RelaunchAgentRequest{Reset: []string{"model", "effort"}}, "exact-agent", runtime.AgentResumePlan{ProjectUID: "exact-project", WindowUID: "exact-window"}, false)
	if strings.Count(got, "--reset") != 1 || !strings.Contains(got, "model,effort") || !strings.Contains(got, "agent relaunch uid:exact-agent") {
		t.Fatal(got)
	}
}

func TestRelaunchNativeProbeCancellationRetainsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := relaunchCodexProbe(ctx, nil, runtime.AgentRestartPlan{}, func(string, string) error { t.Fatal("canceled admission reached refusal callback"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// This protocol runner implements only reads and the one exact failing Stop.
// It never delegates to tmux or a provider, even for an unexpected command.
type relaunchFailureRunner struct {
	socket, project, window, pane, shell string

	mode string

	observationOnly, resumeReadFailure bool

	stopCause, observeCause error

	calls, inventories, stops, resumes int
}

func (r *relaunchFailureRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.calls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name != "tmux" || len(args) < 3 || args[0] != "-S" || args[1] != r.socket {
		return nil, errors.New("fixture refused non-exact socket or executable")
	}
	args = args[2:]
	var output strings.Builder
	render := func(format, pane, id string) string {
		return strings.NewReplacer(
			"#{socket_path}", r.socket, "#{pid}", "4242",
			"#{session_id}", "$1", "#{session_name}", "alpha", "#{window_id}", "@1", "#{pane_id}", id,
			"#{"+tmuxopts.ProjectUIDSession+"}", r.project,
			"#{"+tmuxopts.WindowUID+"}", r.window,
			"#{"+tmuxopts.PaneUID+"}", pane,
		).Replace(format)
	}
	for len(args) > 0 {
		n := slices.Index(args, ";")
		if n < 0 {
			n = len(args)
		}
		command := args[:n]
		if n == len(args) {
			args = nil
		} else {
			args = args[n+1:]
		}
		if len(command) == 0 {
			continue
		}
		format := command[len(command)-1]
		switch command[0] {
		case "show-options":
			switch format {
			case tmuxopts.AppGlobal:
				output.WriteString("1")
			case runtime.RuntimeMutationSocketNameOption:
				output.WriteString("projmux")
			default:
				return nil, errors.New("fixture refused unexpected option read")
			}
		case "display-message":
			output.WriteString(render(format, r.pane, "%2"))
		case "list-sessions":
			if r.stops > 0 && r.resumeReadFailure {
				return nil, r.observeCause
			}
			return nil, errors.New("fixture refused unexpected session read")
		case "list-panes":
			r.inventories++
			if r.observationOnly || r.stops > 0 {
				switch r.mode {
				case "error":
					return nil, r.observeCause
				case "empty":
					continue
				case "closed":
					output.WriteString(render(format, r.shell, "%1"))
					output.WriteByte('\n')
					continue
				}
			}
			output.WriteString(render(format, r.shell, "%1"))
			output.WriteByte('\n')
			output.WriteString(render(format, r.pane, "%2"))
		case "kill-pane":
			if len(command) != 3 || command[1] != "-t" || command[2] != "%2" {
				return nil, errors.New("fixture refused a different Stop target")
			}
			r.stops++
			return nil, r.stopCause
		case "split-window", "new-window", "new-session", "respawn-pane":
			r.resumes++
			return nil, errors.New("fixture refused unexpected resume or replacement")
		default:
			return nil, errors.New("fixture refused unexpected command " + command[0])
		}
		output.WriteByte('\n')
	}
	return []byte(output.String()), nil
}

func relaunchFailureFixture(t *testing.T) (*Client, metadata.Registry, runtime.AgentRestartPlan, *relaunchFailureRunner) {
	t.Helper()
	reg, window, state, _ := deleteFailureFixture(t)
	mut := store.DefaultMutator()
	workspace := reg.Projects[0].Spec.Root
	agent, err := mut.CreateAgent(&reg, window, metadata.CreateAgentOptions{Name: "worker", Provider: "claude", Workspace: metadata.AgentWorkspace{CWD: workspace}})
	if err != nil {
		t.Fatal(err)
	}
	pane, err := mut.AttachAgentPane(&reg, agent.Metadata.UID, metadata.BootstrapPane{Name: "worker-pane", CWD: workspace}, "attach-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := mut.RecordAgentSessionRef(&reg, agent.Metadata.UID, metadata.AgentSessionObservation{Provider: "claude", SessionID: "11111111-2222-4333-8444-555555555555"}); err != nil {
		t.Fatal(err)
	}
	agentRow, _ := reg.Agent(agent.Metadata.UID)
	agent = agentRow.Clone()
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.PathFor(state), data, 0600); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.NewStore(store.PathFor(state)).LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reg, persisted) {
		t.Logf("fixture baseline roundtrip field paths (values omitted): %v", relaunchRegistryDifferencePaths(reg, persisted))
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, executable := range []string{"claude", "projmux"} {
		if err := os.WriteFile(filepath.Join(bin, executable), []byte("#!/bin/sh\nexit 97\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(t.TempDir(), "s")
	client, err := NewClient(Options{StateDir: state, ConfigDir: filepath.Join(home, "config"), HomeDir: home, DefaultShell: "/bin/sh", SocketPath: socket, ProjmuxExecutable: filepath.Join(bin, "projmux")})
	if err != nil {
		t.Fatal(err)
	}
	client.agentEnvironment = map[string]string{"PATH": bin}
	runner := &relaunchFailureRunner{socket: socket, project: reg.Projects[0].Metadata.UID, window: window, pane: pane.Metadata.UID, shell: reg.Panes[0].Metadata.UID, stopCause: errors.New("injected exact Stop failure"), observeCause: errors.New("injected exact inventory failure")}
	return client, persisted, runtime.AgentRestartPlan{Target: agent, Provider: "claude", Running: true, PaneUID: pane.Metadata.UID}, runner
}

func TestRelaunchLivenessSelectsExactPane(t *testing.T) {
	for _, name := range []string{"alive", "closed", "empty", "error", "offline", "other-pane", "missing-pane", "missing-agent", "invalid-socket", "lost-registry"} {
		t.Run(name, func(t *testing.T) {
			client, reg, plan, runner := relaunchFailureFixture(t)
			runner.observationOnly = true
			runner.mode = name
			want, wantErr, wantInventory := runtime.AgentRestartClosed, false, 0
			socket := runner.socket
			path := store.PathFor(client.paths.StateDir)
			switch name {
			case "alive":
				want, wantInventory = runtime.AgentRestartAlive, 1
			case "closed":
				wantInventory = 1 // Other live Pane is positive absence evidence.
			case "empty", "error":
				want, wantErr, wantInventory = runtime.AgentRestartUnknown, true, 1
			case "offline", "other-pane":
				mut := store.DefaultMutator()
				if _, err := mut.ReleaseAgentPane(&reg, plan.Target.Metadata.UID, metadata.AgentExitNormal, ""); err != nil {
					t.Fatal(err)
				}
				if name == "other-pane" {
					if _, err := mut.AttachAgentPane(&reg, plan.Target.Metadata.UID, metadata.BootstrapPane{Name: "replacement", CWD: reg.Projects[0].Spec.Root}, "replacement-binding"); err != nil {
						t.Fatal(err)
					}
				}
			case "missing-pane":
				// Read-only projections accept a degraded graph; this is not
				// authority to mutate or to invent a replacement binding.
				reg.Panes = slices.DeleteFunc(reg.Panes, func(p metadata.Pane) bool { return p.Metadata.UID == plan.PaneUID })
			case "missing-agent":
				plan.Target.Metadata.UID = "absent-planned-agent"
				want, wantErr = runtime.AgentRestartUnknown, true
			case "invalid-socket":
				socket = "relative"
				want, wantErr = runtime.AgentRestartUnknown, true
			case "lost-registry":
				want, wantErr = runtime.AgentRestartUnknown, true
			}
			data, err := json.Marshal(reg)
			if err != nil {
				t.Fatal(err)
			}
			if name == "lost-registry" {
				data = []byte("{")
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.relaunchLiveness(plan, socket, runner)
			if got != want || (err != nil) != wantErr || runner.inventories != wantInventory || runner.stops != 0 || runner.resumes != 0 {
				t.Fatalf("liveness=%v err=%v inventory=%d Stop=%d Resume=%d; want=%v err=%v inventory=%d", got, err, runner.inventories, runner.stops, runner.resumes, want, wantErr, wantInventory)
			}
			if name == "error" && !errors.Is(err, runner.observeCause) {
				t.Fatalf("inventory cause lost: %v", err)
			}
			if wantInventory == 0 && runner.calls != 0 {
				t.Fatal("Registry/route-only disposition consulted tmux")
			}
			after, err := os.ReadFile(path)
			if err != nil || !slices.Equal(before, after) {
				t.Fatalf("liveness observation wrote Registry: %v", err)
			}
		})
	}
}

func TestRelaunchAgentSDKStopFailureEvidence(t *testing.T) {
	for _, mode := range []string{"alive", "error", "empty", "closed"} {
		t.Run(mode, func(t *testing.T) {
			client, before, plan, runner := relaunchFailureFixture(t)
			runner.mode = mode
			out, err := client.relaunchAgent(context.Background(), core.RelaunchAgentRequest{Ref: "uid:" + plan.Target.Metadata.UID, Model: "opus", Confirm: true}, runner)
			wantCode, wantOutcome := CodeRelaunchStopAlive, "stop-failed"
			if mode == "error" || mode == "empty" {
				wantCode = CodeRelaunchStopUnknown
			}
			if mode == "closed" {
				// Positive live absence continues into Resume, not another Stop.
				// This injected kill error leaves the Registry preimage Running,
				// so the existing resume phase gate must retain a partial failure
				// rather than invent an Offline commit or a successful rebind.
				wantCode, wantOutcome = CodeRelaunchExecutionFailed, "resume-failed"
			}
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Code != wantCode {
				t.Fatalf("typed failure lost: errorType=%T wantCode=%s err=%v", err, wantCode, err)
			}
			if mode == "closed" {
				var phase interface{ MetadataUsageError() bool }
				phaseCause := errors.As(err, &phase) && phase.MetadataUsageError()
				cause := refusal.Unwrap()
				phaseDiagnostic := cause != nil && strings.Contains(cause.Error(), "is Running and already owns a managed Pane")
				stopNotice := len(out.Notices) == 2 && strings.Contains(out.Notices[0], runner.stopCause.Error())
				if !phaseCause || !phaseDiagnostic || !stopNotice {
					t.Fatalf("closed evidence lost: phaseCause=%t unwrapped=%t phaseDiagnostic=%t noticeCount=%d stopNotice=%t", phaseCause, cause != nil, phaseDiagnostic, len(out.Notices), stopNotice)
				}
			} else if !errors.Is(err, runner.stopCause) || len(out.Notices) != 0 {
				t.Fatalf("Stop cause/warning differs: stopCause=%t noticeCount=%d", errors.Is(err, runner.stopCause), len(out.Notices))
			}
			if out.Outcome != wantOutcome || out.AgentUID != plan.Target.Metadata.UID || out.PaneUID != plan.PaneUID || out.NewPaneUID != "" || out.Stop.Committed || out.Resume.PaneID != "" {
				t.Fatalf("partial result differs: outcome=%s agentMatch=%t paneMatch=%t newPaneEmpty=%t stopCommitted=%t resumePaneEmpty=%t", out.Outcome, out.AgentUID == plan.Target.Metadata.UID, out.PaneUID == plan.PaneUID, out.NewPaneUID == "", out.Stop.Committed, out.Resume.PaneID == "")
			}
			if len(out.Stop.Panes) != 1 || out.Stop.Panes[0].PaneUID != plan.PaneUID || out.Stop.Panes[0].PaneID != "%2" || len(out.Stop.Plan.Targets) != 1 {
				t.Fatalf("exact uncommitted Stop plan lost: paneCount=%d targetCount=%d", len(out.Stop.Panes), len(out.Stop.Plan.Targets))
			}
			if mode == "closed" {
				if out.Recovery != "" || strings.Contains(err.Error(), "recover with") || strings.Contains(err.Error(), "projmux agent resume") || strings.Contains(err.Error(), "projmux agent relaunch") {
					t.Fatalf("phase refusal advertised an immediate recovery: recoveryEmpty=%t errorType=%T", out.Recovery == "", err)
				}
				for _, text := range []string{err.Error(), out.Notices[1]} {
					for _, want := range []string{"uid:" + plan.Target.Metadata.UID, "uid:" + plan.PaneUID, runner.socket, "Registry phase is Running", "Registry and runtime state", "additional action is required", "No immediate recovery command is available"} {
						if !strings.Contains(text, want) {
							t.Fatalf("phase refusal lost exact inspection guidance: requiredPart=%q", want)
						}
					}
				}
			} else if !strings.Contains(out.Recovery, "agent relaunch uid:"+plan.Target.Metadata.UID) || !strings.Contains(out.Recovery, runner.socket) || !strings.Contains(out.Recovery, "--model "+runtime.ShellQuote("opus")) {
				t.Fatalf("exact recovery lost: %q", out.Recovery)
			}
			if mode == "error" && !strings.Contains(err.Error(), runner.observeCause.Error()) {
				t.Fatalf("unknown observation detail lost: %v", err)
			}
			if runner.stops != 1 || runner.resumes != 0 {
				t.Fatalf("Stop failure was replayed or resumed: Stop=%d Resume=%d", runner.stops, runner.resumes)
			}
			after, loadErr := store.NewStore(store.PathFor(client.paths.StateDir)).LoadReadOnly()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			// Intent admission/withdrawal changes the store clock only.
			after.UpdatedAt = before.UpdatedAt
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("failed Stop changed Registry field paths (values omitted): %v", relaunchRegistryDifferencePaths(before, after))
			}
		})
	}
}

func TestRelaunchAgentSDKResumableFailureKeepsRecovery(t *testing.T) {
	client, before, plan, runner := relaunchFailureFixture(t)
	// Stop succeeds and commits Offline. The later resume transaction's runtime
	// read fails; this is different from a fresh phase refusal before rebind.
	runner.stopCause = nil
	runner.mode = "closed"
	runner.resumeReadFailure = true
	out, err := client.relaunchAgent(context.Background(), core.RelaunchAgentRequest{Ref: "uid:" + plan.Target.Metadata.UID, Model: "opus", Confirm: true}, runner)
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeRelaunchExecutionFailed || !errors.Is(err, runner.observeCause) {
		t.Fatalf("resumable failure cause lost: errorType=%T observationCause=%t", err, errors.Is(err, runner.observeCause))
	}
	if out.Outcome != "resume-failed" || !out.Stop.Committed || out.NewPaneUID != "" || out.Resume.PaneID != "" || len(out.Notices) != 0 {
		t.Fatalf("resumable failure receipt changed: outcome=%s stopCommitted=%t newPaneEmpty=%t resumePaneEmpty=%t notices=%d", out.Outcome, out.Stop.Committed, out.NewPaneUID == "", out.Resume.PaneID == "", len(out.Notices))
	}
	if !strings.Contains(out.Recovery, "agent resume uid:"+plan.Target.Metadata.UID) || !strings.Contains(out.Recovery, runner.socket) || !strings.Contains(out.Recovery, "--model "+runtime.ShellQuote("opus")) || !strings.Contains(err.Error(), "recover with: "+out.Recovery) {
		t.Fatal("resumable failure lost its existing exact recovery guidance")
	}
	if runner.stops != 1 || runner.resumes != 0 {
		t.Fatalf("resumable failure replayed effects: Stop=%d physicalResume=%d", runner.stops, runner.resumes)
	}
	after, loadErr := store.NewStore(store.PathFor(client.paths.StateDir)).LoadReadOnly()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	old, _ := before.Agent(plan.Target.Metadata.UID)
	agent, ok := after.Agent(plan.Target.Metadata.UID)
	if !ok || agent.Status.Phase != metadata.PhaseOffline || agent.Status.PaneRef != "" || !reflect.DeepEqual(old.Status.SessionRef, agent.Status.SessionRef) || !reflect.DeepEqual(old.Metadata.Annotations, agent.Metadata.Annotations) {
		t.Fatal("committed Stop did not preserve the resumable conversation/settings")
	}
	if _, exists := after.Pane(plan.PaneUID); exists {
		t.Fatal("committed Stop retained the old Pane")
	}
}

// Diagnostic paths only: at most 16 fixed field/index paths and no state values.
// This never normalizes or removes anything from the equality assertion.
func relaunchRegistryDifferencePaths(before, after metadata.Registry) []string {
	const limit = 16
	var paths []string
	truncated := false
	var walk func(reflect.Value, reflect.Value, string)
	walk = func(a, b reflect.Value, path string) {
		if len(paths) >= limit {
			truncated = true
			return
		}
		if a.IsValid() && b.IsValid() && a.Type() == b.Type() {
			if a.CanInterface() && b.CanInterface() && reflect.DeepEqual(a.Interface(), b.Interface()) {
				return
			}
			if a.Comparable() && a.Equal(b) {
				return
			}
			switch a.Kind() {
			case reflect.Pointer, reflect.Interface:
				if !a.IsNil() && !b.IsNil() {
					walk(a.Elem(), b.Elem(), path)
					return
				}
			case reflect.Struct:
				// Treat time internals as one field; do not inspect location data.
				if a.Type().PkgPath() != "time" {
					for i := range a.NumField() {
						walk(a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name)
					}
					return
				}
			case reflect.Slice, reflect.Array:
				if a.Len() == b.Len() && (a.Kind() != reflect.Slice || a.IsNil() == b.IsNil()) {
					for i := range a.Len() {
						walk(a.Index(i), b.Index(i), path+"["+strconv.Itoa(i)+"]")
					}
					return
				}
			}
		}
		// Maps stay at their fixed field path: never print dynamic keys/values.
		paths = append(paths, path)
	}
	walk(reflect.ValueOf(before), reflect.ValueOf(after), "Registry")
	if truncated {
		paths = append(paths, "<field-path-limit>")
	}
	return paths
}
