package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// agent_pane_intent_test.go covers the Projmux split UI after the legacy split
// was retired: every producer states a canonical create intent, and no producer
// reaches tmux's `split-window` on its own.
//
// The three producers are the saved-default binding
// (`internal agent-pane launch-default`), the Alt-7 picker and the resume picker
// (both `internal agent-pane picker`). The provider and shell direct actions were
// already canonical `create` invocations and are covered by the create tests;
// what is new here is that the other three reach the same route.

// recordingPaneCreator records the intents a split-UI producer emitted and
// creates nothing. It is the seam that makes "which intent" assertable without a
// registry or a tmux server in the way.
type recordingPaneCreator struct {
	intents []agentPaneIntent
	err     error
}

func (r *recordingPaneCreator) createFromIntent(intent agentPaneIntent, _, _ io.Writer) error {
	r.intents = append(r.intents, intent)
	return r.err
}

type appServerPickerCatalog struct{ client *codexappserver.Client }

func (c *appServerPickerCatalog) List(ctx context.Context, query codexappserver.CatalogQuery) (codexappserver.CatalogPage, error) {
	return c.client.ListCatalogThreads(ctx, query)
}

func (c *appServerPickerCatalog) Read(ctx context.Context, threadID string) (codexappserver.CatalogThread, error) {
	return c.client.ReadCatalogThread(ctx, threadID)
}

func (c *appServerPickerCatalog) Close() error { return c.client.Close() }

func intentAICommand(t *testing.T, home string) (*aiCommand, *recordingPaneCreator) {
	t.Helper()
	cmd := testAICommand(home)
	creator := &recordingPaneCreator{}
	cmd.panes = creator
	return cmd, creator
}

// stubAIPickerSelection makes the native picker return one selection.
func stubAIPickerSelection(cmd *aiCommand, value string) {
	result := intpickercompat.Result{}
	if value != "" {
		result = intpickercompat.Result{Key: "enter", Value: value}
	}
	cmd.nativePicker = nativePickerFromCompatRunner(&capturingAIRunner{result: result})
}

func enableAgents(t *testing.T, home string, providers ...config.AIAgentProvider) {
	t.Helper()
	path := filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName)
	if err := config.SaveAIEnabledAgentsFile(path, providers); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}
}

// TestSavedDefaultSplitStatesOneCanonicalIntent is the default-AI-split half of
// the split UI contract.
//
// The saved mode is the one piece of hidden state the split UI is allowed to
// read -- which is precisely why `create agent` refuses to read it -- and its
// whole job is to name one intent. A provider mode names an Agent, `shell` names
// a Pane, and the two picker adapters name no launch at all and open their popup
// instead.
func TestSavedDefaultSplitStatesOneCanonicalIntent(t *testing.T) {
	tests := []struct {
		mode      string
		direction string
		want      *agentPaneIntent
		popup     string
	}{
		{mode: aiModeCodex, direction: "right", want: &agentPaneIntent{producer: canonicalProducerSavedDefault, provider: aiModeCodex, placement: "right"}},
		{mode: aiModeClaude, direction: "down", want: &agentPaneIntent{producer: canonicalProducerSavedDefault, provider: aiModeClaude, placement: "down"}},
		{mode: aiModeAntigravity, direction: "right", want: &agentPaneIntent{producer: canonicalProducerSavedDefault, provider: aiModeAntigravity, placement: "right"}},
		{mode: aiModeShell, direction: "down", want: &agentPaneIntent{producer: canonicalProducerSavedDefault, placement: "down"}},
		{mode: aiModeSelective, direction: "right", popup: "ai-split-picker-right"},
		{mode: aiModeResume, direction: "down", popup: "ai-split-resume-down"},
	}
	for _, tt := range tests {
		t.Run(tt.mode+"/"+tt.direction, func(t *testing.T) {
			home := t.TempDir()
			cmd, creator := intentAICommand(t, home)
			if err := cmd.setMode(tt.mode); err != nil {
				t.Fatalf("setMode(%s) error = %v", tt.mode, err)
			}
			cmdRecorder(cmd).commands = nil

			if err := cmd.Run([]string{"launch-default", tt.direction}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("launch-default %s error = %v", tt.direction, err)
			}
			if tt.want == nil {
				if len(creator.intents) != 0 {
					t.Fatalf("picker mode %q created %+v, want a popup and no intent", tt.mode, creator.intents)
				}
				if !containsAICommandArgs(cmdRecorder(cmd).commands, "/tmp/projmux",
					[]string{"internal", "tmux", "popup-toggle", tt.popup}) {
					t.Fatalf("commands = %#v, want the %s popup toggle", cmdRecorder(cmd).commands, tt.popup)
				}
				return
			}
			if want := []agentPaneIntent{*tt.want}; !reflect.DeepEqual(creator.intents, want) {
				t.Fatalf("intents = %+v, want %+v", creator.intents, want)
			}
			// A launch intent is the whole of what this producer does: it runs no
			// provider binary, probes no pane, and issues no tmux command of its
			// own.
			if len(cmdRecorder(cmd).commands) != 0 {
				t.Fatalf("commands = %#v, want none; create owns every runtime call", cmdRecorder(cmd).commands)
			}
		})
	}
}

// TestSavedDefaultSplitRefusesADisabledProviderBeforeAnyIntent pins the Settings
// gate on the default path.
//
// A saved default that has since been switched off fails clearly rather than
// silently launching a different provider, and it fails before the intent
// exists, so the refusal costs zero Registry and zero tmux mutations.
func TestSavedDefaultSplitRefusesADisabledProviderBeforeAnyIntent(t *testing.T) {
	home := t.TempDir()
	enableAgents(t, home, config.AIAgentClaude)
	cmd, creator := intentAICommand(t, home)
	if err := cmd.setMode(aiModeCodex); err != nil {
		t.Fatalf("setMode(codex) error = %v", err)
	}
	cmdRecorder(cmd).commands = nil

	err := cmd.Run([]string{"launch-default", "down"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("a disabled saved default was launched")
	}
	for _, want := range []string{"AI split default codex is disabled", "choose another default"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err.Error(), want)
		}
	}
	if len(creator.intents) != 0 {
		t.Fatalf("intents = %+v, want none", creator.intents)
	}
	if len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("commands = %#v, want none", cmdRecorder(cmd).commands)
	}
}

// TestSavedDefaultSplitRejectsAMalformedDirection keeps the closed placement
// enum at the producer boundary rather than only inside create.
func TestSavedDefaultSplitRejectsAMalformedDirection(t *testing.T) {
	home := t.TempDir()
	cmd, creator := intentAICommand(t, home)
	for _, args := range [][]string{{"launch-default", "left"}, {"launch-default", "right", "down"}} {
		if err := cmd.Run(args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("launch-default accepted %v", args)
		}
	}
	if len(creator.intents) != 0 {
		t.Fatalf("intents = %+v, want none", creator.intents)
	}
}

// TestPickerSelectionStatesOneCanonicalIntent is the Alt-7 half.
//
// The picker resolves what the operator chose and then does exactly what the
// saved default does with the same answer. Selecting a provider row is an Agent
// intent, selecting the shell row is a Pane intent, and an empty or non-Enter
// result is a no-op.
func TestPickerSelectionStatesOneCanonicalIntent(t *testing.T) {
	tests := []struct {
		selection string
		direction string
		want      []agentPaneIntent
	}{
		{selection: aiModeCodex, direction: "right", want: []agentPaneIntent{{producer: canonicalProducerProviderPicker, provider: aiModeCodex, placement: "right"}}},
		{selection: aiModeClaude, direction: "down", want: []agentPaneIntent{{producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "down"}}},
		{selection: aiModeAntigravity, direction: "right", want: []agentPaneIntent{{producer: canonicalProducerProviderPicker, provider: aiModeAntigravity, placement: "right"}}},
		{selection: aiModeShell, direction: "down", want: []agentPaneIntent{{producer: canonicalProducerProviderPicker, placement: "down"}}},
		{selection: "", direction: "right", want: nil},
	}
	for _, tt := range tests {
		name := tt.selection
		if name == "" {
			name = "no-selection"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			cmd, creator := intentAICommand(t, home)
			stubAIPickerSelection(cmd, tt.selection)

			if err := cmd.Run([]string{"picker", "--inside", tt.direction}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("picker error = %v", err)
			}
			if !reflect.DeepEqual(creator.intents, tt.want) {
				t.Fatalf("intents = %+v, want %+v", creator.intents, tt.want)
			}
		})
	}
}

// TestPickerShellFlagStatesAPaneIntent covers the picker's shell shortcut, which
// bypasses the selection UI entirely.
func TestPickerShellFlagStatesAPaneIntent(t *testing.T) {
	home := t.TempDir()
	cmd, creator := intentAICommand(t, home)

	if err := cmd.Run([]string{"picker", "--shell", "--inside", "right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("picker --shell error = %v", err)
	}
	if want := []agentPaneIntent{{producer: canonicalProducerDirectShell, placement: "right"}}; !reflect.DeepEqual(creator.intents, want) {
		t.Fatalf("intents = %+v, want %+v", creator.intents, want)
	}
}

// TestPickerSelectionRefusesADisabledProvider pins the Settings gate on the
// picker path, whose message differs from the default path's because
// `--force-agent` never existed here.
func TestPickerSelectionRefusesADisabledProvider(t *testing.T) {
	home := t.TempDir()
	enableAgents(t, home, config.AIAgentCodex)
	cmd, creator := intentAICommand(t, home)
	stubAIPickerSelection(cmd, aiModeClaude)

	err := cmd.Run([]string{"picker", "--inside", "right"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("a disabled provider was launched from the picker")
	}
	if !strings.Contains(err.Error(), "AI agent claude is disabled") {
		t.Fatalf("error = %q, want the disabled-agent refusal", err.Error())
	}
	if len(creator.intents) != 0 {
		t.Fatalf("intents = %+v, want none", creator.intents)
	}
}

// TestResumeSelectionStatesAResumedIntentWithTheProviderNormalizedID is the
// resume-picker half.
//
// The conversation id on the intent is the provider's own normalized form, taken
// from the same resume-argv builder `agent resume` uses, so a row the provider
// cannot address is caught before any intent exists.
func TestResumeSelectionStatesAResumedIntentWithTheProviderNormalizedID(t *testing.T) {
	tests := []struct {
		provider string
		resumeID string
		want     string
	}{
		{provider: aiModeClaude, resumeID: "11111111-2222-3333-4444-555555555555", want: "11111111-2222-3333-4444-555555555555"},
		{provider: aiModeCodex, resumeID: "0199aaaa-bbbb-cccc-dddd-eeeeeeeeeeee", want: "0199aaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		{provider: aiModeAntigravity, resumeID: "99999999-8888-7777-6666-555555555555", want: "99999999-8888-7777-6666-555555555555"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			home := t.TempDir()
			cmd, creator := intentAICommand(t, home)

			if err := cmd.runSelectedResumeSession(aiResumeSelection{
				agent: tt.provider, resumeID: tt.resumeID,
			}, "right"); err != nil {
				t.Fatalf("runSelectedResumeSession error = %v", err)
			}
			want := []agentPaneIntent{{producer: canonicalProducerResumePicker, provider: tt.provider, placement: "right", conversationID: tt.want}}
			if !reflect.DeepEqual(creator.intents, want) {
				t.Fatalf("intents = %+v, want %+v", creator.intents, want)
			}
		})
	}
}

func TestNativeResumeSelectionPreservesCatalogSourceOnIntent(t *testing.T) {
	home := t.TempDir()
	cmd, creator := intentAICommand(t, home)
	id := "0199aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee"
	if err := cmd.runSelectedResumeSession(aiResumeSelection{
		agent: aiModeCodex, resumeID: id, source: aisessions.SourceCodexAppServer,
	}, "down"); err != nil {
		t.Fatal(err)
	}
	want := []agentPaneIntent{{
		producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "down",
		conversationID: id, resumeSource: aisessions.SourceCodexAppServer,
	}}
	if !reflect.DeepEqual(creator.intents, want) {
		t.Fatalf("intents=%+v want=%+v", creator.intents, want)
	}
}

func TestNonCodexResumeSelectionDoesNotChangeIntentProvenance(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeAntigravity} {
		t.Run(provider, func(t *testing.T) {
			home := t.TempDir()
			cmd, creator := intentAICommand(t, home)
			id := "0199aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee"
			if err := cmd.runSelectedResumeSession(aiResumeSelection{
				agent: provider, resumeID: id, source: "provider-existing-source",
			}, "down"); err != nil {
				t.Fatal(err)
			}
			if len(creator.intents) != 1 || creator.intents[0].resumeSource != "" {
				t.Fatalf("intent=%+v, non-Codex provenance must remain unchanged", creator.intents)
			}
		})
	}
}

func TestAppServerCatalogPickerPreservesListedThreadOnResumeIntent(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "repo")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	enableAgents(t, home, config.AIAgentCodex)

	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { _ = serverConn.Close() })
	serverErr := make(chan error, 1)
	id := "019f0000-0000-7000-8000-000000000041"
	go func() {
		line, err := bufio.NewReader(serverConn).ReadBytes('\n')
		if err != nil {
			serverErr <- err
			return
		}
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(line, &request); err != nil {
			serverErr <- err
			return
		}
		if request.Method != "thread/list" {
			serverErr <- errors.New("picker did not request thread/list")
			return
		}
		response := map[string]any{"id": request.ID, "result": map[string]any{
			"data": []map[string]any{{
				"id": id, "cwd": work, "name": "", "createdAt": int64(10),
				"updatedAt": int64(20), "recencyAt": int64(30), "status": map[string]any{"type": "idle"},
			}}, "nextCursor": nil,
		}}
		encoded, err := json.Marshal(response)
		if err == nil {
			_, err = serverConn.Write(append(encoded, '\n'))
		}
		serverErr <- err
	}()

	cmd, creator := intentAICommand(t, home)
	cmd.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX_SPLIT_CONTEXT_DIR":
			return work
		default:
			return ""
		}
	}
	cmd.openCodexCatalog = func(context.Context) (aisessions.CodexCatalog, error) {
		return &appServerPickerCatalog{client: codexappserver.NewClient(clientConn)}, nil
	}
	cmd.loadRegistry = func() (coremetadata.Registry, error) {
		return coremetadata.Registry{Agents: []coremetadata.Agent{{
			Spec: coremetadata.AgentSpec{Provider: aiModeCodex},
			Metadata: coremetadata.ObjectMeta{UID: "agt-picker", Name: "picker-agent", Annotations: map[string]string{
				coremetadata.AnnotationAgentTopic: "Exact registry topic",
			}},
			Status: coremetadata.AgentStatus{SessionRef: &coremetadata.AgentSessionRef{
				Provider: aiModeCodex, Codex: &coremetadata.CodexSessionRef{ThreadID: id},
			}},
		}}}, nil
	}
	runner := &capturingAIRunner{result: intpickercompat.Result{Key: "enter", Value: aiResumePickerValue(aiModeCodex, id)}}
	cmd.nativePicker = nativePickerFromCompatRunner(runner)
	if err := cmd.runResumePicker("right"); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	want := []agentPaneIntent{{
		producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right",
		conversationID: id, resumeSource: aisessions.SourceCodexAppServer,
	}}
	if !reflect.DeepEqual(creator.intents, want) {
		t.Fatalf("intents=%+v want=%+v", creator.intents, want)
	}
	if len(runner.options.Entries) < 2 || !strings.Contains(runner.options.Entries[1].Label, "Exact registry topic") {
		t.Fatalf("picker entries=%#v, want exact-bound Registry topic", runner.options.Entries)
	}
}

func TestCatalogOpenFailurePickerUsesOneVisibleRolloutRowAndIntent(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		reason string
	}{
		{name: "unavailable", err: errors.New("socket unavailable"), reason: aisessions.ReasonAppServerUnavailable},
		{name: "unsupported", err: codexappserver.ErrUnsupported, reason: aisessions.ReasonAppServerUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			work := filepath.Join(home, "repo")
			if err := os.MkdirAll(work, 0o755); err != nil {
				t.Fatal(err)
			}
			enableAgents(t, home, config.AIAgentCodex)
			id := "019f0000-0000-7000-8000-000000000077"
			writeCodexResumeSession(t, home, id, work, "feat/fallback", "Fallback title")

			cmd, creator := intentAICommand(t, home)
			cmd.lookupEnv = func(name string) string {
				switch name {
				case "HOME":
					return home
				case "TMUX_SPLIT_CONTEXT_DIR":
					return work
				default:
					return ""
				}
			}
			opens := 0
			cmd.openCodexCatalog = func(context.Context) (aisessions.CodexCatalog, error) {
				opens++
				return nil, test.err
			}
			runner := &capturingAIRunner{result: intpickercompat.Result{Key: "enter", Value: aiResumePickerValue(aiModeCodex, id)}}
			cmd.nativePicker = nativePickerFromCompatRunner(runner)
			if err := cmd.runResumePicker("right"); err != nil {
				t.Fatal(err)
			}
			if opens != 1 || len(runner.options.Entries) < 2 {
				t.Fatalf("catalog opens=%d entries=%#v, want one open and a rollout row plus provider status rows", opens, runner.options.Entries)
			}
			row := runner.options.Entries[1]
			if !strings.Contains(row.Label, "[fallback]") || strings.Contains(row.Label, aisessions.SourceCodexRollout) ||
				!strings.Contains(row.SearchKey, aisessions.SourceCodexRollout) || !strings.Contains(row.SearchKey, test.reason) {
				t.Fatalf("row=%#v, want compact fallback and searchable raw provenance", row)
			}
			if len(creator.intents) != 1 || creator.intents[0].conversationID != id ||
				creator.intents[0].resumeSource != aisessions.SourceCodexRollout {
				t.Fatalf("intents=%+v, rollout selection must stay on rollout lane", creator.intents)
			}
		})
	}
}

// TestResumeSelectionWithAnUnusableIDFallsBackToAFreshIntent keeps the one
// deliberate difference between this interactive path and `agent resume`.
//
// The operator picked a row and the picker already told them it could not be
// resumed, so opening a fresh conversation is the useful answer here. The
// canonical `agent resume` route must never do this, which is why it has a
// separate launch seam that cannot build a fresh-start argv at all.
func TestResumeSelectionWithAnUnusableIDFallsBackToAFreshIntent(t *testing.T) {
	home := t.TempDir()
	cmd, creator := intentAICommand(t, home)

	if err := cmd.runSelectedResumeSession(aiResumeSelection{
		agent: aiModeClaude, resumeID: "bad\x01id",
	}, "down"); err != nil {
		t.Fatalf("runSelectedResumeSession error = %v", err)
	}
	want := []agentPaneIntent{{producer: canonicalProducerResumePicker, provider: aiModeClaude, placement: "down"}}
	if !reflect.DeepEqual(creator.intents, want) {
		t.Fatalf("intents = %+v, want a fresh %+v", creator.intents, want)
	}
}

// TestAnUnwiredCreateRouteFailsInsteadOfCreatingNothing pins the nil seam.
//
// The failure that matters for a UI action is one that reports success and
// produces no pane. A missing create route says so instead.
func TestAnUnwiredCreateRouteFailsInsteadOfCreatingNothing(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	if err := cmd.createShellPane(canonicalProducerDirectShell, "right"); err == nil {
		t.Fatal("an unwired create route silently succeeded")
	}
}

// TestTheLegacySplitRouteIsGone proves the retirement.
//
// `ai split` was the only route that called tmux's `split-window` on the split
// UI's behalf, and its flags -- `--agent`, `--force-agent`, `--print-pane-id`,
// the `--` payload -- were the compatibility surface. External automation was
// moved to the canonical `create` routes before this Phase; nothing forwards to
// the split handler any more because there is no split handler.
func TestTheLegacySplitRouteIsGone(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	for _, args := range [][]string{
		{"split", "right"},
		{"split", "--agent", "codex", "right"},
		{"split", "--agent", "shell", "--print-pane-id", "right"},
	} {
		err := cmd.Run(args, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil {
			t.Fatalf("%v still dispatched", args)
		}
		if !strings.Contains(err.Error(), "unknown ai subcommand: split") {
			t.Fatalf("error = %q, want the unknown-subcommand refusal", err.Error())
		}
	}
}

// TestOnlyTheMaterializerRunsSplitWindow is the forbidden-direct-split audit.
//
// It reads the package's own sources because the property is about the code
// rather than about one execution: the split UI must not be able to reach a raw
// split, and the way to keep that true is to have exactly one place in the
// application that spells `split-window` at all. A raw unmanaged split remains
// reachable -- by the operator typing `tmux split-window`, and by tmux's own
// pane-context-menu entries in the generated config -- which is a different thing
// from projmux producing one.
func TestOnlyTheMaterializerRunsSplitWindow(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	// materialize.go owns the one detached `split-window`, and tmux.go carries the
	// generated pane-context-menu entries, which are tmux's own menu items rather
	// than a projmux-issued split. Snapshot replay lives in its own package and
	// restores a recorded argv, which is outside this Phase.
	allowed := map[string]bool{"materialize.go": true, "runtime_mutation_plan.go": true, "runtime_mutation_surface.go": true, "tmux.go": true}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(body), "SplitWindow(") {
			t.Errorf("%s calls a SplitWindow helper; the materializer owns every split", name)
		}
		if strings.Contains(string(body), `"split-window"`) && !allowed[name] {
			t.Errorf("%s issues a raw split-window argv; the materializer owns every split", name)
		}
	}
}

// TestTheIntentRendersTheExactCanonicalCreateArgv proves the projection.
//
// A UI action and a typed command have to mean the same thing, and the cheapest
// way to guarantee that is for the UI to reach the same parser with the same
// argv. Asserting the rendering directly is what makes that a property of the
// mapping rather than of whatever the create happened to do next.
func TestTheIntentRendersTheExactCanonicalCreateArgv(t *testing.T) {
	tests := []struct {
		name         string
		intent       agentPaneIntent
		want         []string
		wantProvider string
		wantResume   string
	}{
		{
			name:   "shell pane",
			intent: agentPaneIntent{placement: "down"},
			want:   []string{"pane", "--placement", "down"},
		},
		{
			name:         "provider agent",
			intent:       agentPaneIntent{provider: aiModeCodex, placement: "right"},
			want:         []string{"agent", "--provider", "codex", "--placement", "right"},
			wantProvider: "codex",
		},
		{
			name:         "resumed provider agent",
			intent:       agentPaneIntent{provider: aiModeClaude, placement: "down", conversationID: "conv-7"},
			want:         []string{"agent", "--provider", "claude", "--placement", "down"},
			wantProvider: "claude",
			wantResume:   "conv-7",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argv, provider, conversation, err := tt.intent.canonicalArgv()
			if err != nil {
				t.Fatalf("canonicalArgv error = %v", err)
			}
			if !reflect.DeepEqual(argv, tt.want) {
				t.Fatalf("argv = %#v, want %#v", argv, tt.want)
			}
			if provider != tt.wantProvider || conversation != tt.wantResume {
				t.Fatalf("provider/conversation = %q/%q, want %q/%q", provider, conversation, tt.wantProvider, tt.wantResume)
			}
		})
	}
}

// TestTheIntentRefusesAnIncoherentCombination pins the two refusals the intent
// owns rather than delegating.
func TestTheIntentRefusesAnIncoherentCombination(t *testing.T) {
	create := &createCommand{}
	for _, tt := range []struct {
		name   string
		intent agentPaneIntent
		want   string
	}{
		{
			name:   "left placement",
			intent: agentPaneIntent{provider: aiModeCodex, placement: "left"},
			want:   "placement must be one of",
		},
		{
			name:   "empty placement",
			intent: agentPaneIntent{provider: aiModeCodex},
			want:   "placement must be one of",
		},
		{
			name:   "shell cannot resume",
			intent: agentPaneIntent{placement: "right", conversationID: "abc"},
			want:   "cannot resume a conversation without a provider",
		},
		{
			name:   "picker adapter is not a provider",
			intent: agentPaneIntent{provider: aiModeSelective, placement: "right"},
			want:   "is an interactive picker, not a provider",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := create.createFromIntent(tt.intent, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatalf("intent %+v was accepted", tt.intent)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
			if !IsUsageError(err) {
				t.Fatalf("error = %v, want a usage error so exit 2 keeps meaning invalid input", err)
			}
		})
	}
}

// TestTheResumeIntentReachesTheSharedAgentBodyWithTheConversation proves the one
// branch that cannot be spelled in argv still reaches the same allocation path.
func TestTheResumeIntentReachesTheSharedAgentBodyWithTheConversation(t *testing.T) {
	launcher := &recordingAgentLauncher{}
	create := &createCommand{agents: launcher, resumes: launcher}

	err := create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerResumePicker,
		provider: aiModeCodex, placement: "right", conversationID: "conv-7",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("an unconfigured create command reported success")
	}
	if IsUsageError(err) {
		t.Fatalf("the resume intent was refused by the parser: %v", err)
	}
	// RequireAgentEnabled is the first thing the shared body does, so reaching it
	// proves the intent got past the parser and the provider enum.
	if launcher.gated != aiModeCodex {
		t.Fatalf("Settings gate provider = %q, want codex", launcher.gated)
	}
}

// recordingAgentLauncher answers both launch seams and records the gate.
type recordingAgentLauncher struct {
	gated string
}

func (r *recordingAgentLauncher) RequireAgentEnabled(provider string) error {
	r.gated = provider
	return nil
}

func (r *recordingAgentLauncher) PlanAgentLaunch(string, coremetadata.AgentWorkspace, []string) (string, []string, error) {
	return "", nil, errors.New("fresh launch is not expected on the resume intent")
}

func (r *recordingAgentLauncher) BindManagedAgentPane(string, string, string, string) {}

func (r *recordingAgentLauncher) AwaitAgentActivation(context.Context, tmuxCommandRunner, string, time.Duration, time.Duration) (bool, string, error) {
	return false, "", nil
}

func (r *recordingAgentLauncher) PlanAgentResume(string, coremetadata.AgentWorkspace, string) (string, []string, error) {
	return "", nil, errors.New("resume launch reached")
}

func (r *recordingAgentLauncher) BindResumedAgentPane(string, string, string, string, string) {}
