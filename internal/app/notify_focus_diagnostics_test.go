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
	"strings"
	"testing"
	"time"

	corefocus "github.com/crevissepartners/projmux/internal/core/focus"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

func TestNotifyFocusDiagnosticsProductionGraphSharesOneRecorder(t *testing.T) {
	lifecycle := diagnostics.NewLifecycleRecorder(&appLifecycleWriter{}, "shared-run", "0.10.0", "tmux")
	app := NewWithLifecycleDiagnostics(lifecycle)
	want := app.notify.diagnostics
	if want == nil || app.ai.notifyDiagnostics != want || app.focus.notifyDiagnostics != want {
		t.Fatalf("top-level recorders are not shared: notify=%p ai=%p focus=%p", want, app.ai.notifyDiagnostics, app.focus.notifyDiagnostics)
	}
	aiProducer, ok := app.ai.producer.(*storeAttentionNotifyProducer)
	if !ok || aiProducer.diagnostics != want {
		t.Fatalf("AI producer = %#v, want shared recorder %p", app.ai.producer, want)
	}
	attentionProducer, ok := app.attention.producer.(*storeAttentionNotifyProducer)
	if !ok || attentionProducer.diagnostics != want {
		t.Fatalf("attention producer = %#v, want shared recorder %p", app.attention.producer, want)
	}
}

func TestNotifyPushDiagnosticsOwnsTopLevelAndDropsRawInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.jsonl")
	journal := diagnostics.NewStore(path)
	lifecycle := diagnostics.NewLifecycleRecorder(journal, "notify-run", "0.10.0", "tmux")
	app := NewWithLifecycleDiagnostics(lifecycle)
	queue := &stubNotifyStore{
		pushEntry:  notify.Notification{ID: "seed-uuid-123", Text: "seed summary/body"},
		pushResult: notify.PushResult{ID: "seed-uuid-123", QueueLen: 1},
	}
	app.notify.store, app.notify.storeErr, app.notify.hooks, app.notify.events = queue, nil, nil, nil
	args := []string{"create", "notification", "--text", "seed summary/body tag/group title /seed/private/path 123e4567-e89b-12d3-a456-426614174000", "--target", "secret-session:1.0", "--id", "raw-uuid", "--source", "ai"}
	started := time.Now()
	err := app.Run(args, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if err := diagnostics.RecordOutcome(journal, args, "notify-run", "0.10.0", "tmux", started, err, false, lifecycle.RecordedOutcome()); err != nil {
		t.Fatal(err)
	}
	events, err := journal.Read()
	if err != nil || len(events) != 1 {
		t.Fatalf("events = %#v err=%v, want one owned enqueue", events, err)
	}
	event := events[0]
	if event.Event != "notify.transition" || event.Transition != "enqueue" || event.Disposition != "queued" || event.Provider != "ai" || event.Category != "other" || event.Route != "queue" {
		t.Fatalf("event = %#v", event)
	}
	raw, _ := json.Marshal(events)
	for _, forbidden := range []string{"seed summary", "body", "tag", "group", "title", "/seed/private/path", "123e4567", "secret-session", "raw-uuid"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("diagnostics leaked %q: %s", forbidden, raw)
		}
	}
}

func TestNotifyPushDiagnosticsDedupeFailureAndAppendBestEffort(t *testing.T) {
	tests := []struct {
		name            string
		queue           *stubNotifyStore
		writerErr       error
		dropWriter      bool
		wantCommandErr  bool
		wantDisposition diagnostics.Disposition
		wantCode        diagnostics.Code
	}{
		{
			name: "deduplicated", queue: &stubNotifyStore{pushResult: notify.PushResult{ID: "stable", QueueLen: 1, Replaced: true}},
			wantDisposition: diagnostics.DispositionDeduplicated,
		},
		{
			name: "queue failure", queue: &stubNotifyStore{pushErr: errors.New("raw queue path /seed/private/notify.json")},
			wantCommandErr: true, wantDisposition: diagnostics.DispositionFailed, wantCode: diagnostics.CodeNotifyEnqueueFailed,
		},
		{
			name: "append failure preserves enqueue success", queue: &stubNotifyStore{pushResult: notify.PushResult{ID: "stable", QueueLen: 1}},
			writerErr: errors.New("journal denied"), dropWriter: true, wantDisposition: diagnostics.DispositionQueued,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &appLifecycleWriter{err: tt.writerErr, drop: tt.dropWriter}
			lifecycle := diagnostics.NewLifecycleRecorder(writer, "notify-attempt", "0.10.0", "tmux")
			cmd := newCmd(tt.queue)
			cmd.diagnostics, cmd.hooks, cmd.events = lifecycle.NotifyFocus(), nil, nil
			err := cmd.Run([]string{"push", "--text", "raw body", "--target", "raw-session", "--id", "stable", "--source", "external"}, &bytes.Buffer{}, &bytes.Buffer{})
			if (err != nil) != tt.wantCommandErr {
				t.Fatalf("Run() error = %v, wantErr=%v", err, tt.wantCommandErr)
			}
			if !lifecycle.RecordedOutcome() {
				t.Fatal("explicit notify attempt did not logically own top-level outcome")
			}
			if tt.dropWriter {
				if len(writer.events) != 0 {
					t.Fatalf("dropped writer events = %#v", writer.events)
				}
				return
			}
			if len(writer.events) != 1 || writer.events[0].Disposition != string(tt.wantDisposition) || writer.events[0].Code != string(tt.wantCode) {
				t.Fatalf("events = %#v", writer.events)
			}
		})
	}
}

func TestSecondaryAttentionEnqueueFailureDoesNotOwnOuterCommand(t *testing.T) {
	writer := &appLifecycleWriter{}
	lifecycle := diagnostics.NewLifecycleRecorder(writer, "secondary-run", "0.10.0", "tmux")
	recorder := lifecycle.NotifyFocus()
	producer := &storeAttentionNotifyProducer{
		store: &stubNotifyStore{pushErr: errors.New("raw queue path /seed/private/notify.json")},
		ttl:   time.Minute, diagnostics: recorder,
	}
	lookup := newFakeAttentionLookup(map[string]string{
		"%9|@projmux_ai_agent": "codex", "%9|@projmux_ai_topic": "raw terminal title",
		"%9|#S": "raw-session", "%9|#{window_id}": "@7", "%9|#{pane_id}": "%9", "%9|#{socket_path}": "/seed/private/socket",
	})
	producer.PushReplyReady(attentionNotifyInput{PaneID: "%9", Lookup: lookup})
	if lifecycle.RecordedOutcome() {
		t.Fatal("secondary enqueue failure claimed unrelated outer command")
	}
	if len(writer.events) != 1 || writer.events[0].Code != string(diagnostics.CodeNotifyEnqueueFailed) || writer.events[0].Provider != "codex" {
		t.Fatalf("events = %#v", writer.events)
	}
}

func TestSecondaryNotifyReconcileRecordsPushWithoutOwningOuterCommand(t *testing.T) {
	tests := []struct {
		name       string
		paneOutput []byte
		wantEvents int
	}{
		{
			name:       "push is secondary",
			paneOutput: reconcilePaneRow("seed-session", "@4", "%16", "reply", "waiting", "claude", "seed terminal title", "/seed/private/socket"),
			wantEvents: 1,
		},
		{name: "no change stays zero", paneOutput: []byte(""), wantEvents: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &appLifecycleWriter{}
			lifecycle := diagnostics.NewLifecycleRecorder(writer, "nested-reconcile", "0.10.0", "tmux")
			cmd := newReconcileCmd(&memNotifyStore{}, &reconcileTmuxRunner{output: tt.paneOutput})
			cmd.diagnostics = lifecycle.NotifyFocus()
			if err := cmd.runReconcileWithOwnership(nil, &bytes.Buffer{}, &bytes.Buffer{}, false); err != nil {
				t.Fatal(err)
			}
			if lifecycle.RecordedOutcome() {
				t.Fatal("nested prune reconcile claimed the outer command")
			}
			if len(writer.events) != tt.wantEvents {
				t.Fatalf("events = %#v, want %d", writer.events, tt.wantEvents)
			}
			if tt.wantEvents == 1 {
				event := writer.events[0]
				if event.Transition != "enqueue" || event.Disposition != "queued" || event.Provider != "claude" || event.Category != "response_complete" || event.Route != "queue" {
					t.Fatalf("event = %#v", event)
				}
				raw, _ := json.Marshal(event)
				for _, forbidden := range []string{"seed-session", "seed terminal title", "/seed/private/socket", "%16", "@4"} {
					if strings.Contains(string(raw), forbidden) {
						t.Fatalf("nested reconcile event leaked %q: %s", forbidden, raw)
					}
				}
			}
		})
	}
}

func TestNotifyDeliveryDiagnosticsRoutesAndPrivacy(t *testing.T) {
	tests := []struct {
		name            string
		configure       func(*aiCommand)
		build           func(*aiCommand, notifyDeliveryDiagnostics) aiNotifier
		wantRoute       diagnostics.Route
		wantDisposition diagnostics.Disposition
		wantCode        diagnostics.Code
		wantErr         bool
	}{
		{
			name: "external hook", wantRoute: diagnostics.RouteHook, wantDisposition: diagnostics.DispositionDelivered,
			build: func(cmd *aiCommand, d notifyDeliveryDiagnostics) aiNotifier {
				return aiHookNotifier{command: cmd, hook: "/seed/private/fake-notify-hook", deliveryDiagnostics: d}
			},
		},
		{
			name: "linux builtin", wantRoute: diagnostics.RouteNotifySend, wantDisposition: diagnostics.DispositionDelivered,
			configure: func(cmd *aiCommand) {
				cmd.lookupEnv = func(name string) string {
					if name == desktopNotifyModeEnv {
						return "notify"
					}
					return ""
				}
				cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
					if name == "command" && reflect.DeepEqual(args, []string{"-v", "notify-send"}) {
						return []byte("/usr/bin/notify-send\n"), nil
					}
					return nil, os.ErrNotExist
				}
			},
			build: func(cmd *aiCommand, d notifyDeliveryDiagnostics) aiNotifier {
				return aiDesktopNotifier{command: cmd, deliveryDiagnostics: d}
			},
		},
		{
			name: "disabled", wantRoute: diagnostics.RouteDisabled, wantDisposition: diagnostics.DispositionSuppressed,
			configure: func(cmd *aiCommand) {
				cmd.lookupEnv = func(name string) string {
					if name == desktopNotifyModeEnv {
						return "none"
					}
					return ""
				}
			},
			build: func(cmd *aiCommand, d notifyDeliveryDiagnostics) aiNotifier {
				return aiDesktopNotifier{command: cmd, deliveryDiagnostics: d}
			},
		},
		{
			name: "wsl toast", wantRoute: diagnostics.RouteWSLToast, wantDisposition: diagnostics.DispositionDelivered,
			configure: func(cmd *aiCommand) {
				cmd.lookupEnv = func(name string) string {
					switch name {
					case desktopNotifyModeEnv:
						return "notify"
					case "WSL_DISTRO_NAME":
						return "Ubuntu"
					}
					return ""
				}
				cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
					if name == "command" && reflect.DeepEqual(args, []string{"-v", "powershell.exe"}) {
						return []byte("/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe\n"), nil
					}
					return nil, os.ErrNotExist
				}
			},
			build: func(cmd *aiCommand, d notifyDeliveryDiagnostics) aiNotifier {
				return aiDesktopNotifier{command: cmd, deliveryDiagnostics: d}
			},
		},
		{
			name: "wsl fallback", wantRoute: diagnostics.RouteWSLNotifySend, wantDisposition: diagnostics.DispositionDelivered,
			configure: func(cmd *aiCommand) {
				cmd.lookupEnv = func(name string) string {
					switch name {
					case desktopNotifyModeEnv:
						return "notify"
					case "WSL_DISTRO_NAME":
						return "Ubuntu"
					}
					return ""
				}
				cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
					if name == "command" && len(args) == 2 && args[0] == "-v" {
						switch args[1] {
						case "powershell.exe", "wsl-notify-send.exe":
							return []byte("/seed/private/" + args[1] + "\n"), nil
						}
					}
					return nil, os.ErrNotExist
				}
				cmd.runCommand = func(_ context.Context, name string, _ ...string) error {
					if strings.HasSuffix(name, "powershell.exe") {
						return errors.New("seeded PowerShell failure")
					}
					return nil
				}
			},
			build: func(cmd *aiCommand, d notifyDeliveryDiagnostics) aiNotifier {
				return aiDesktopNotifier{command: cmd, deliveryDiagnostics: d}
			},
		},
		{
			name: "sender unavailable", wantRoute: diagnostics.RouteNotifySend, wantDisposition: diagnostics.DispositionFailed,
			wantCode: diagnostics.CodeNotifyDeliveryUnavailable, wantErr: true,
			configure: func(cmd *aiCommand) {
				cmd.lookupEnv = func(name string) string {
					if name == desktopNotifyModeEnv {
						return "notify"
					}
					return ""
				}
			},
			build: func(cmd *aiCommand, d notifyDeliveryDiagnostics) aiNotifier {
				return aiDesktopNotifier{command: cmd, deliveryDiagnostics: d}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &appLifecycleWriter{}
			lifecycle := diagnostics.NewLifecycleRecorder(writer, "delivery-run", "0.10.0", "tmux")
			cmd := testAICommand(t.TempDir())
			if tt.configure != nil {
				tt.configure(cmd)
			}
			notifier := tt.build(cmd, notifyDeliveryDiagnostics{recorder: lifecycle.NotifyFocus(), provider: diagnostics.ProviderCodex, category: diagnostics.CategoryApprovalRequired})
			err := notifier.Notify(aiNotification{
				Summary: "seed summary", Body: "seed body /seed/private/path", Tag: "seed-tag-uuid", Group: "seed-group", AppName: "projmux", Urgency: "normal",
				diagnosticProvider: diagnostics.ProviderCodex, diagnosticCategory: diagnostics.CategoryApprovalRequired,
			})
			if err != nil && !tt.wantErr {
				t.Fatal(err)
			}
			if err == nil && tt.wantErr {
				t.Fatal("Notify() succeeded, want error")
			}
			if len(writer.events) != 1 {
				t.Fatalf("events = %#v, want one terminal delivery", writer.events)
			}
			event := writer.events[0]
			if event.Route != string(tt.wantRoute) || event.Disposition != string(tt.wantDisposition) || event.Code != string(tt.wantCode) {
				t.Fatalf("event = %#v", event)
			}
			raw, _ := json.Marshal(event)
			for _, forbidden := range []string{"seed summary", "seed body", "/seed/private/path", "seed-tag", "seed-group"} {
				if strings.Contains(string(raw), forbidden) {
					t.Fatalf("delivery event leaked %q: %s", forbidden, raw)
				}
			}
		})
	}
}

func TestNotifyDeliveryDiagnosticsCoalescesActualSuppressionHotPaths(t *testing.T) {
	writer := &appLifecycleWriter{}
	lifecycle := diagnostics.NewLifecycleRecorder(writer, "suppression-run", "0.10.0", "tmux")
	cmd := testAICommand(t.TempDir())
	cmd.notifyDiagnostics = lifecycle.NotifyFocus()
	cmd.producer = noopAttentionNotifyProducer{}
	cmd.now = func() time.Time { return time.Unix(1000, 0) }
	key := "input_required|waiting for input"
	cmd.lookupEnv = func(name string) string {
		if name == "PROJMUX_TMUX_NOTIFY_DEDUPE_SECONDS" {
			return "120"
		}
		return ""
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "tmux" {
			return nil, os.ErrNotExist
		}
		switch {
		case reflect.DeepEqual(args, []string{"list-clients", "-F", "#{client_active_pane}"}):
			return []byte("%15\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@projmux_desktop_notified}"}):
			return []byte("\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{pane_title}"}):
			return []byte("waiting for input\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@projmux_desktop_notification_key}"}):
			return []byte(key + "\n"), nil
		case reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%3", "#{@projmux_desktop_notification_at}"}):
			return []byte("950\n"), nil
		default:
			return []byte("\n"), nil
		}
	}

	for range 10 {
		if err := cmd.notifyAI("%3"); err != nil {
			t.Fatal(err)
		}
		if err := cmd.applyAIStatusInternal("waiting", "%15", attentionNotifyInput{
			Metadata: map[string]string{"agent": "codex", "category": "response_complete"},
		}, true, false); err != nil {
			t.Fatal(err)
		}
	}
	if lifecycle.RecordedOutcome() {
		t.Fatal("automatic suppression claimed an unrelated top-level command")
	}
	if len(writer.events) != 2 {
		t.Fatalf("events = %#v, want one dedupe and one visible-pane tuple", writer.events)
	}
	routes := map[string]int{}
	for _, event := range writer.events {
		routes[event.Route]++
	}
	if routes[string(diagnostics.RouteDedupe)] != 1 || routes[string(diagnostics.RouteVisiblePane)] != 1 {
		t.Fatalf("routes = %#v", routes)
	}
}

func TestFocusTelemetryAndOutcomeUseClosedClassifications(t *testing.T) {
	telemetryTests := []struct {
		opts         focusOptions
		wantProvider diagnostics.Provider
		wantCategory diagnostics.Category
		wantRoute    diagnostics.Route
	}{
		{focusOptions{Source: "status-bar", Kind: "segment-click"}, diagnostics.ProviderProjmux, diagnostics.CategorySegmentClick, diagnostics.RouteFocusQueue},
		{focusOptions{Source: "toast", Kind: "toast-click"}, diagnostics.ProviderProjmux, diagnostics.CategoryToastClick, diagnostics.RouteFocusToast},
		{focusOptions{Source: "/seed/private/raw", Kind: "seeded title"}, diagnostics.ProviderProjmux, diagnostics.CategoryOther, diagnostics.RouteFocusDirect},
	}
	for _, tt := range telemetryTests {
		got := newFocusTelemetry(tt.opts, corefocus.Target{}, "/seed/private/socket")
		if got.provider != tt.wantProvider || got.category != tt.wantCategory || got.route != tt.wantRoute {
			t.Errorf("newFocusTelemetry(%#v) = provider=%q category=%q route=%q", tt.opts, got.provider, got.category, got.route)
		}
	}
	outcomeTests := []struct {
		result          focusResult
		wantDisposition diagnostics.Disposition
	}{
		{focusResult{Fallback: "session-only"}, diagnostics.DispositionSessionOnly},
		{focusResult{Fallback: "window-only"}, diagnostics.DispositionWindowOnly},
		{focusResult{}, diagnostics.DispositionFocused},
	}
	for _, tt := range outcomeTests {
		disposition, code := focusDiagnosticOutcome(tt.result, nil)
		if disposition != tt.wantDisposition || code != "" {
			t.Errorf("focusDiagnosticOutcome(%#v) = %q, %q", tt.result, disposition, code)
		}
	}
}

func TestFocusDiagnosticsCoexistsWithSessionSwitchAndSuppressesGeneric(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.jsonl")
	journal := diagnostics.NewStore(path)
	lifecycle := diagnostics.NewLifecycleRecorder(journal, "focus-run", "0.10.0", "tmux")
	app := NewWithLifecycleDiagnostics(lifecycle)
	app.focus.lookupEnv = func(string) string { return "" }
	app.focus.homeDir = func() (string, error) { return t.TempDir(), nil }
	const seededSession = "secret-123e4567-e89b-12d3-a456-426614174000"
	app.focus.runner = &focusFakeRunner{respond: func(args []string) ([]byte, error) {
		switch {
		case containsArg(args, "list-sessions"):
			return []byte("100\t" + seededSession + "\t1\n"), nil
		case containsArg(args, "list-clients"):
			return []byte("/dev/pts/0\t" + seededSession + "\n"), nil
		default:
			return nil, nil
		}
	}}
	args := []string{"internal", "focus", "--target", seededSession + ":1.0", "--socket", "/seed/private/socket", "--source", "status-bar", "--kind", "segment-click"}
	started := time.Now()
	err := app.Run(args, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if err := diagnostics.RecordOutcome(journal, args, "focus-run", "0.10.0", "tmux", started, err, false, lifecycle.RecordedOutcome()); err != nil {
		t.Fatal(err)
	}
	events, err := journal.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %#v, want lifecycle pair plus focus outcome", events)
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.Event]++
		if event.RunID != "focus-run" {
			t.Fatalf("event run_id = %q", event.RunID)
		}
	}
	if counts["lifecycle.start"] != 1 || counts["lifecycle.outcome"] != 1 || counts["focus.transition"] != 1 || counts["command.outcome"] != 0 {
		t.Fatalf("event counts = %#v", counts)
	}
	focusEvent := events[2]
	if focusEvent.Disposition != "focused" || focusEvent.Route != "focus-queue" || focusEvent.Category != "segment_click" {
		t.Fatalf("focus event = %#v", focusEvent)
	}
	raw, _ := json.Marshal(focusEvent)
	for _, forbidden := range []string{seededSession, "123e4567", "/seed/private/socket", "/dev/pts/0"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("focus event leaked %q: %s", forbidden, raw)
		}
	}
}

func TestFocusDiagnosticsUnresolvedAndNotifyOnlyPaths(t *testing.T) {
	tests := []struct {
		name            string
		listSessions    []byte
		listClients     []byte
		wantErr         bool
		wantDisposition diagnostics.Disposition
		wantCode        diagnostics.Code
	}{
		{name: "unresolved", wantErr: true, wantDisposition: diagnostics.DispositionFailed, wantCode: diagnostics.CodeFocusResolveFailed},
		{name: "notify only", listSessions: []byte("100\tworkspace\t0\n"), wantDisposition: diagnostics.DispositionNotifyOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &appLifecycleWriter{}
			lifecycle := diagnostics.NewLifecycleRecorder(writer, "focus-edge", "0.10.0", "tmux")
			cmd := newFocusCommand(lifecycle)
			cmd.notifyDiagnostics = lifecycle.NotifyFocus()
			cmd.lookupEnv = func(string) string { return "" }
			cmd.homeDir = func() (string, error) { return t.TempDir(), nil }
			cmd.notifierOnce = func(io.Writer) focusNotifier { return &focusFakeNotifier{} }
			cmd.runner = &focusFakeRunner{respond: func(args []string) ([]byte, error) {
				switch {
				case containsArg(args, "list-sessions"):
					return tt.listSessions, nil
				case containsArg(args, "list-clients"):
					return tt.listClients, nil
				default:
					return nil, nil
				}
			}}
			finish := lifecycle.BeginCommand()
			err := cmd.Run([]string{"--target", "workspace", "--source", "external", "--kind", "custom"}, &bytes.Buffer{}, &bytes.Buffer{})
			finish(err)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Run() error = %v, wantErr=%v", err, tt.wantErr)
			}
			if len(writer.events) != 1 {
				t.Fatalf("events = %#v, want one focus transition without switch lifecycle", writer.events)
			}
			event := writer.events[0]
			if event.Disposition != string(tt.wantDisposition) || event.Code != string(tt.wantCode) || event.Provider != "external" || event.Route != "focus-direct" {
				t.Fatalf("event = %#v", event)
			}
		})
	}
}

func TestNotifyFocusSupportReportDropsSeededUnknownPayloadFields(t *testing.T) {
	stateHome := t.TempDir()
	path := filepath.Join(stateHome, "projmux", "logs", diagnostics.LogFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := `{"at":"2026-08-14T00:00:00Z","level":"error","component":"notify","event":"notify.transition","result":"error","duration_ms":1,"run_id":"safe-run","version":"0.10.0","mux_backend":"tmux","kind":"runtime","code":"notify.delivery.failed","transition":"delivery","disposition":"failed","provider":"codex","category":"approval_required","route":"hook","summary":"seed summary","body":"seed body","tag":"seed-tag","group":"seed-group","terminal_title":"seed title","path":"/seed/private/path","uuid":"123e4567-e89b-12d3-a456-426614174000"}` + "\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &diagnosticsCommand{
		lookupEnv: func(name string) string {
			if name == "XDG_STATE_HOME" {
				return stateHome
			}
			return ""
		},
		homeDir: func() (string, error) { return t.TempDir(), nil },
	}
	data, entry, err := cmd.supportOperationalErrors()
	if err != nil || entry.Status != "included" {
		t.Fatalf("supportOperationalErrors() = %s, %#v, %v", data, entry, err)
	}
	output := string(data)
	for _, forbidden := range []string{"seed summary", "seed body", "seed-tag", "seed-group", "seed title", "/seed/private/path", "123e4567", "terminal_title", "uuid"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("support output leaked %q: %s", forbidden, output)
		}
	}
	for _, want := range []string{"notify.delivery.failed", "codex", "approval_required", "hook"} {
		if !strings.Contains(output, want) {
			t.Fatalf("support output missing %q: %s", want, output)
		}
	}
}
