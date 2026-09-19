package app

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

// aiCommandGuardedIngestFields are the mutexes and the state they guard. A
// migration clone must start with its own zero value of each.
var aiCommandGuardedIngestFields = []string{
	"agentObservationMu",
	"pendingAgentSessionRefs",
	"pendingCodexBindings",
	"paneWriteMu",
	"paneWriteFailure",
}

type managedIngestMigrationAIBuilder struct {
	name string
	// replaced names the aiCommand seams the builder swaps for its own
	// closures in addition to the tmuxCommand overrides.
	replaced []string
	build    func(*tmuxCommand) *aiCommand
}

func managedIngestMigrationAIBuilders() []managedIngestMigrationAIBuilder {
	return []managedIngestMigrationAIBuilder{
		{
			name:  "managedIngestMigrationAI",
			build: func(c *tmuxCommand) *aiCommand { return c.managedIngestMigrationAI() },
		},
		{
			name:     "managedIngestMigrationAIForRoute",
			replaced: []string{"runCommand", "readCommand"},
			build: func(c *tmuxCommand) *aiCommand {
				return c.managedIngestMigrationAIForRoute(runtimeMutationRoute{
					socketName:         "projmux",
					expectedSocketPath: "/tmp/tmux-test/projmux",
				})
			},
		},
	}
}

func TestManagedIngestMigrationAIKeepsGuardedIngestStateSeparate(t *testing.T) {
	for _, builder := range managedIngestMigrationAIBuilders() {
		t.Run(builder.name, func(t *testing.T) {
			// The original's maps and failure exist before the clone is built,
			// so a clone that shared them would see and consume them.
			orig := &aiCommand{}
			orig.stageAgentSessionRef("%1", coremetadata.AgentSessionObservation{Provider: "claude", SessionID: "orig-session"})
			orig.stageCodexBinding("%1", "orig-thread", "orig-turn")
			orig.noteAIPaneMarkerWriteFailure(errors.New("orig marker write failed"))

			clone := builder.build(&tmuxCommand{ai: orig})

			if obs, ok := clone.takeAgentSessionRef("%1"); ok {
				t.Fatalf("clone took the original's staged session ref %+v", obs)
			}
			if native, ok := clone.takeCodexBinding("%1"); ok {
				t.Fatalf("clone took the original's staged codex binding %+v", native)
			}
			if got := clone.recordedAIPaneWriteFailure(); got != "" {
				t.Fatalf("clone inherited the original's pane write failure %q", got)
			}

			clone.stageAgentSessionRef("%2", coremetadata.AgentSessionObservation{Provider: "claude", SessionID: "clone-session"})
			clone.stageCodexBinding("%2", "clone-thread", "clone-turn")
			if obs, ok := orig.takeAgentSessionRef("%2"); ok {
				t.Fatalf("original took the clone's staged session ref %+v", obs)
			}
			if native, ok := orig.takeCodexBinding("%2"); ok {
				t.Fatalf("original took the clone's staged codex binding %+v", native)
			}

			if obs, ok := orig.takeAgentSessionRef("%1"); !ok || obs.SessionID != "orig-session" {
				t.Fatalf("original session ref = %+v, %v; want orig-session", obs, ok)
			}
			if native, ok := orig.takeCodexBinding("%1"); !ok || native.ThreadID != "orig-thread" {
				t.Fatalf("original codex binding = %+v, %v; want orig-thread", native, ok)
			}
			if obs, ok := clone.takeAgentSessionRef("%2"); !ok || obs.SessionID != "clone-session" {
				t.Fatalf("clone session ref = %+v, %v; want clone-session", obs, ok)
			}
			if native, ok := clone.takeCodexBinding("%2"); !ok || native.ThreadID != "clone-thread" {
				t.Fatalf("clone codex binding = %+v, %v; want clone-thread", native, ok)
			}
		})
	}

	t.Run("pane write failure noted on the clone", func(t *testing.T) {
		orig := &aiCommand{}
		clone := (&tmuxCommand{ai: orig}).managedIngestMigrationAI()
		clone.noteAIPaneMarkerWriteFailure(errors.New("clone marker write failed"))
		if got := clone.recordedAIPaneWriteFailure(); got == "" {
			t.Fatal("clone did not record its own pane write failure")
		}
		if got := orig.recordedAIPaneWriteFailure(); got != "" {
			t.Fatalf("original recorded the clone's pane write failure %q", got)
		}
	})
}

type cloneSeamPicker struct{ intpicker.Runner }

type cloneSeamProducer struct{ attentionNotifyProducer }

type cloneSeamNotifyStore struct{ notifyStore }

type cloneSeamEvents struct{ notifyQueueRefreshEvents }

type cloneSeamCodexNative struct{ codexNativeThreadController }

type cloneSeamPanes struct{ canonicalPaneCreator }

// fullyWiredAICommandFixture sets every aiCommand field to a distinct non-zero
// value. Each func is its own literal so its code pointer identifies it.
func fullyWiredAICommandFixture(t *testing.T) *aiCommand {
	t.Helper()
	c := &aiCommand{
		nativePicker:           &cloneSeamPicker{},
		executable:             func() (string, error) { return "orig-executable", nil },
		lookupEnv:              func(string) string { return "orig-env" },
		homeDir:                func() (string, error) { return "orig-home", nil },
		stdin:                  strings.NewReader("orig-stdin"),
		readFile:               func(string) ([]byte, error) { return nil, nil },
		writeFile:              func(string, []byte, os.FileMode) error { return nil },
		mkdirAll:               func(string, os.FileMode) error { return nil },
		runCommand:             func(context.Context, string, ...string) error { return nil },
		readCommand:            func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
		now:                    func() time.Time { return time.Unix(1, 0) },
		sleep:                  func(time.Duration) {},
		producer:               &cloneSeamProducer{},
		notifyStore:            &cloneSeamNotifyStore{},
		events:                 &cloneSeamEvents{},
		notifyDiagnostics:      &diagnostics.NotifyFocusRecorder{},
		operationalDiagnostics: &diagnostics.AIRecorder{},
		openCodexCatalog:       func(context.Context) (aisessions.CodexCatalog, error) { return nil, nil },
		codexNative:            &cloneSeamCodexNative{},
		discoverResumeSummaryProvider: func(context.Context, string, string, aisessions.ResumeSummaryOptions, int) (d aisessions.ResumeSummaryDiscovery, err error) {
			return
		},
		readResumeDetail: func(context.Context, aisessions.ResumeDetailRef, aisessions.OpenCodexCatalog) (d aisessions.ResumeDetail, err error) {
			return
		},
		readResumePreview: func(context.Context, aisessions.ResumeDetailRef, aisessions.OpenCodexCatalog) (p aisessions.Preview, err error) {
			return
		},
		acquireCodexAuthority:      func(string) (func(), error) { return func() {}, nil },
		notifyDeliveryOwnsTopLevel: true,
		loadRegistry:               func() (r coremetadata.Registry, err error) { return },
		updateRegistry:             func(func(*coremetadata.Registry) error) (r coremetadata.Registry, err error) { return },
		panes:                      &cloneSeamPanes{},
		paneDelete:                 func(string, io.Writer, io.Writer) error { return nil },
		heldRelease: heldMessageRelease{
			store:  func() (agentMessageHeldLister, error) { return nil, nil },
			launch: func(string) error { return nil },
		},
		pendingAgentSessionRefs: map[string]coremetadata.AgentSessionObservation{"%9": {Provider: "claude", SessionID: "orig"}},
		pendingCodexBindings:    map[string]coremetadata.CodexActivationObservation{"%9": {ThreadID: "orig"}},
		paneWriteFailure:        aiPaneWriteReasonMarkerUnavailable,
	}
	// A held lock is the non-zero state a value copy would carry over.
	c.agentObservationMu.Lock()
	t.Cleanup(c.agentObservationMu.Unlock)
	c.paneWriteMu.Lock()
	t.Cleanup(c.paneWriteMu.Unlock)
	return c
}

func TestManagedIngestMigrationAICarriesEverySeam(t *testing.T) {
	orig := fullyWiredAICommandFixture(t)
	origValue := reflect.ValueOf(orig).Elem()
	fields := origValue.Type()
	for i := range fields.NumField() {
		if origValue.Field(i).IsZero() {
			t.Fatalf("fixture leaves aiCommand.%s zero; set it and decide whether the migration clone carries it", fields.Field(i).Name)
		}
	}

	tmux := &tmuxCommand{
		executable: func() (string, error) { return "tmux-executable", nil },
		lookupEnv:  func(string) string { return "tmux-env" },
		homeDir:    func() (string, error) { return "tmux-home", nil },
		readFile:   func(string) ([]byte, error) { return []byte("tmux"), nil },
		writeFile:  func(string, []byte, os.FileMode) error { return errors.New("tmux") },
		ai:         orig,
	}
	overrides := map[string]reflect.Value{
		"homeDir":    reflect.ValueOf(tmux.homeDir),
		"lookupEnv":  reflect.ValueOf(tmux.lookupEnv),
		"executable": reflect.ValueOf(tmux.executable),
		"readFile":   reflect.ValueOf(tmux.readFile),
		"writeFile":  reflect.ValueOf(tmux.writeFile),
		"mkdirAll":   reflect.ValueOf(os.MkdirAll),
	}
	guarded := map[string]bool{}
	for _, name := range aiCommandGuardedIngestFields {
		guarded[name] = true
	}
	for name := range guarded {
		if _, ok := fields.FieldByName(name); !ok {
			t.Fatalf("aiCommand has no guarded field %s", name)
		}
	}

	for _, builder := range managedIngestMigrationAIBuilders() {
		t.Run(builder.name, func(t *testing.T) {
			replaced := map[string]bool{}
			for _, name := range builder.replaced {
				replaced[name] = true
			}
			clone := builder.build(tmux)
			if clone == orig {
				t.Fatal("migration clone is the original aiCommand")
			}
			cloneValue := reflect.ValueOf(clone).Elem()
			for i := range fields.NumField() {
				name := fields.Field(i).Name
				got, original := cloneValue.Field(i), origValue.Field(i)
				switch {
				case guarded[name]:
					if !got.IsZero() {
						t.Errorf("clone.%s is not zero; guarded ingest state must not be copied", name)
					}
				case replaced[name]:
					if got.IsNil() {
						t.Errorf("clone.%s is nil; want the route closure", name)
					} else if got.Pointer() == original.Pointer() {
						t.Errorf("clone.%s is the original's seam; want the route closure", name)
					}
				case overrides[name].IsValid():
					want := overrides[name]
					if want.Pointer() == original.Pointer() {
						t.Fatalf("fixture: tmuxCommand override of %s equals the original's seam", name)
					}
					if !sameAISeam(t, name, got, want) {
						t.Errorf("clone.%s is not the tmuxCommand override", name)
					}
				default:
					if !sameAISeam(t, name, got, original) {
						t.Errorf("clone.%s is not the original's seam", name)
					}
				}
			}
		})
	}
}

// sameAISeam reports whether two seam values are the same object: the same
// func code, pointer, map, or boolean, compared field by field for a struct.
func sameAISeam(t *testing.T, name string, a, b reflect.Value) bool {
	t.Helper()
	switch a.Kind() {
	case reflect.Func, reflect.Pointer, reflect.Map, reflect.Chan, reflect.Slice, reflect.UnsafePointer:
		return a.IsNil() == b.IsNil() && a.Pointer() == b.Pointer()
	case reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() && b.IsNil()
		}
		return a.Elem().Type() == b.Elem().Type() && sameAISeam(t, name, a.Elem(), b.Elem())
	case reflect.Struct:
		for i := range a.NumField() {
			if !sameAISeam(t, name, a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Bool:
		return a.Bool() == b.Bool()
	case reflect.String:
		return a.String() == b.String()
	default:
		t.Fatalf("aiCommand.%s has kind %s; teach sameAISeam to compare it", name, a.Kind())
		return false
	}
}
