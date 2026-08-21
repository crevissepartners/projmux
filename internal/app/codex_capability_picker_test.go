package app

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	corecap "github.com/crevissepartners/projmux/internal/core/aicapability"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/i18n"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

type fakeCodexCapabilitySession struct {
	snapshot     corecap.Snapshot
	refreshed    corecap.Snapshot
	refreshErr   error
	refreshCalls int
	closeCalls   int
}

type capabilityPlanningPaneCreator struct {
	cmd     *aiCommand
	intents []agentPaneIntent
	argv    []string
}

func (c *capabilityPlanningPaneCreator) createFromIntent(intent agentPaneIntent, _, _ io.Writer) error {
	c.intents = append(c.intents, intent)
	if intent.codexCapability == nil {
		return errors.New("capability selection missing from create intent")
	}
	_, argv, err := c.cmd.PlanAgentLaunchWithCapability(intent.provider, coremetadata.AgentWorkspace{CWD: "/repo"}, []string{"task"}, *intent.codexCapability)
	c.argv = argv
	return err
}

func (f *fakeCodexCapabilitySession) Snapshot() corecap.Snapshot { return f.snapshot.Clone() }

func (f *fakeCodexCapabilitySession) Refresh(context.Context) (corecap.Snapshot, error) {
	f.refreshCalls++
	if f.refreshErr != nil {
		return corecap.Snapshot{}, f.refreshErr
	}
	if f.refreshed.Epoch.Valid() {
		return f.refreshed.Clone(), nil
	}
	return f.snapshot.Clone(), nil
}

func (f *fakeCodexCapabilitySession) Close() error {
	f.closeCalls++
	return nil
}

func testCodexCapabilitySnapshot() corecap.Snapshot {
	return corecap.Snapshot{
		Epoch: corecap.Epoch{Connection: "connection-1", Version: "0.149.0"},
		Models: []corecap.Model{
			{ID: "model-a", LaunchName: "gpt-a", DisplayName: "GPT A", Default: true, DefaultEffort: "medium", Efforts: []string{"low", "medium"}, InputModalities: []string{"text", "image"}, SupportsPersonality: true},
			{ID: "model-b", LaunchName: "gpt-b", DisplayName: "GPT B", Efforts: []string{"high"}, InputModalities: []string{"text"}},
		},
		Review: corecap.ReviewCapability{Available: true},
	}
}

func TestCodexCapabilityRowsRenderOnlyVisibleModelsAndSupportedEfforts(t *testing.T) {
	t.Parallel()
	rows, selections := codexCapabilityRows(i18n.FallbackLocale, testCodexCapabilitySnapshot())
	if got, want := entryValues(rows), []string{"capability:0:0", "capability:0:1", "capability:1:0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("row values = %#v, want %#v", got, want)
	}
	wantLabels := []string{
		"GPT A                    low        text+image, personality",
		"GPT A                    medium     text+image, personality [DEFAULT]",
		"GPT B                    high       text",
	}
	for i, want := range wantLabels {
		if rows[i].Label != want {
			t.Fatalf("row[%d] label = %q, want %q", i, rows[i].Label, want)
		}
	}
	for value, selection := range selections {
		if selection.Epoch.Connection != "connection-1" || strings.TrimSpace(value) == "" {
			t.Fatalf("selection %q = %#v", value, selection)
		}
	}
}

func TestCodexCapabilityRowsLocalizeSemanticLabelsOnly(t *testing.T) {
	t.Parallel()
	snapshot := corecap.Snapshot{
		Epoch: corecap.Epoch{Connection: "connection-1", Version: "0.149.0"},
		Models: []corecap.Model{{
			ID: "model-provider-literal", LaunchName: "gpt-provider-literal", DisplayName: "Provider Model", Default: true,
			DefaultEffort: "provider-effort", Efforts: []string{"provider-effort"}, SupportsPersonality: true,
		}},
	}
	rows, _ := codexCapabilityRows(i18n.Locale("ko-KR"), snapshot)
	if len(rows) != 1 || rows[0].Label != "Provider Model           provider-effort 입력 모달리티 미지정, 성격 지원 [기본값]" {
		t.Fatalf("localized capability row = %#v", rows)
	}
}

func TestCodexProviderPickerProjectsSelectionIntoCreateArgs(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	creator := &capabilityPlanningPaneCreator{cmd: cmd}
	cmd.panes = creator
	runner := &sequencingAIRunner{results: []intpickercompat.Result{
		{Key: "enter", Value: aiModeCodex},
		{Key: "enter", Value: "capability:0:1"},
	}}
	cmd.nativePicker = nativePickerFromCompatRunner(runner)
	session := &fakeCodexCapabilitySession{snapshot: testCodexCapabilitySnapshot()}
	cmd.openCodexCapabilitySession = func(context.Context) (codexCapabilitySession, error) {
		return session, nil
	}
	codexPath := writeExecutable(t, filepath.Join(home, "bin", "codex"))
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && reflect.DeepEqual(args, []string{"-v", "codex"}) {
			return []byte(codexPath + "\n"), nil
		}
		return nil, errors.New("unexpected command lookup")
	}

	if err := cmd.runAgentPickerSelection("right"); err != nil {
		t.Fatal(err)
	}
	if len(creator.intents) != 1 || creator.intents[0].codexCapability == nil {
		t.Fatalf("intents = %#v, want one capability-bound Codex intent", creator.intents)
	}
	selection := *creator.intents[0].codexCapability
	if selection.LaunchName != "gpt-a" || selection.Effort != "medium" {
		t.Fatalf("selection = %#v", selection)
	}
	args := execArgvTail(t, creator.argv, aiModeCodex)
	want := []string{"--model", "gpt-a", "--config", `model_reasoning_effort="medium"`, "-C", "/repo", "task"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("create args = %#v, want %#v", args, want)
	}
	if session.refreshCalls != 1 || session.closeCalls != 1 {
		t.Fatalf("capability session refresh=%d close=%d, want 1/1", session.refreshCalls, session.closeCalls)
	}
}

func TestCodexProviderPickerFallsBackStaticAndRejectsStaleEpoch(t *testing.T) {
	t.Run("discovery unavailable keeps static launch", func(t *testing.T) {
		home := t.TempDir()
		cmd, creator := intentAICommand(t, home)
		cmd.nativePicker = nativePickerFromCompatRunner(&capturingAIRunner{result: intpickercompat.Result{Key: "enter", Value: aiModeCodex}})
		cmd.openCodexCapabilitySession = func(context.Context) (codexCapabilitySession, error) {
			return nil, corecap.ErrUnavailable
		}
		if err := cmd.runAgentPickerSelection("right"); err != nil {
			t.Fatal(err)
		}
		if len(creator.intents) != 1 || creator.intents[0].provider != aiModeCodex || creator.intents[0].codexCapability != nil {
			t.Fatalf("fallback intents = %#v, want unchanged static Codex intent", creator.intents)
		}
	})

	t.Run("connection changes while picker is open", func(t *testing.T) {
		home := t.TempDir()
		cmd, creator := intentAICommand(t, home)
		session := &fakeCodexCapabilitySession{snapshot: testCodexCapabilitySnapshot()}
		cmd.openCodexCapabilitySession = func(context.Context) (codexCapabilitySession, error) {
			return session, nil
		}
		cmd.nativePicker = nativePickerFromCompatRunner(switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
			cmd.codexCapabilityCache.Replace(corecap.Snapshot{Epoch: corecap.Epoch{Connection: "connection-2", Version: "0.150.0"}})
			return intpickercompat.Result{Key: "enter", Value: "capability:0:0"}, nil
		}))
		_, _, err := cmd.runCodexCapabilityPicker()
		if !errors.Is(err, corecap.ErrStaleSelection) {
			t.Fatalf("stale selection error = %v, want ErrStaleSelection", err)
		}
		if len(creator.intents) != 0 {
			t.Fatalf("stale selection created intents %#v", creator.intents)
		}
	})
}

func TestCodexCapabilityProductionFlowRefreshesAndRejectsDisappearedOption(t *testing.T) {
	home := t.TempDir()
	cmd, creator := intentAICommand(t, home)
	initial := testCodexCapabilitySnapshot()
	replaced := initial.Clone()
	replaced.Models[0].Efforts = []string{"low"}
	replaced.Models[0].DefaultEffort = "low"
	session := &fakeCodexCapabilitySession{snapshot: initial, refreshed: replaced}
	cmd.openCodexCapabilitySession = func(context.Context) (codexCapabilitySession, error) { return session, nil }
	cmd.nativePicker = nativePickerFromCompatRunner(&capturingAIRunner{result: intpickercompat.Result{Key: "enter", Value: "capability:0:1"}})

	selection, dynamic, err := cmd.runCodexCapabilityPicker()
	if err != nil || !dynamic || selection.Effort != "medium" {
		t.Fatalf("picker selection = %#v dynamic=%v err=%v", selection, dynamic, err)
	}
	if _, _, err := cmd.PlanAgentLaunchWithCapability(aiModeCodex, coremetadata.AgentWorkspace{CWD: "/repo"}, nil, selection); !errors.Is(err, corecap.ErrStaleSelection) {
		t.Fatalf("pre-create refresh error = %v, want ErrStaleSelection", err)
	}
	if session.refreshCalls != 1 || session.closeCalls != 1 {
		t.Fatalf("capability session refresh=%d close=%d, want 1/1", session.refreshCalls, session.closeCalls)
	}
	if len(creator.intents) != 0 {
		t.Fatalf("direct picker test unexpectedly created intents: %#v", creator.intents)
	}
}

func TestCodexCapabilitySessionClosesWhenCanonicalCreateFailsBeforePlan(t *testing.T) {
	home := t.TempDir()
	cmd, creator := intentAICommand(t, home)
	creator.err = errors.New("create preflight failed")
	session := &fakeCodexCapabilitySession{snapshot: testCodexCapabilitySnapshot()}
	cmd.openCodexCapabilitySession = func(context.Context) (codexCapabilitySession, error) { return session, nil }
	cmd.nativePicker = nativePickerFromCompatRunner(&sequencingAIRunner{results: []intpickercompat.Result{
		{Key: "enter", Value: aiModeCodex},
		{Key: "enter", Value: "capability:0:1"},
	}})

	err := cmd.runAgentPickerSelection("right")
	if err == nil || !strings.Contains(err.Error(), "create preflight failed") {
		t.Fatalf("create error = %v", err)
	}
	if session.refreshCalls != 0 || session.closeCalls != 1 {
		t.Fatalf("capability session refresh=%d close=%d, want 0/1", session.refreshCalls, session.closeCalls)
	}
}
