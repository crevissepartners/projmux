package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/aiprovider"
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
		_, argv, err := c.cmd.PlanAgentLaunch(intent.provider, coremetadata.AgentWorkspace{CWD: "/repo"}, []string{"task"})
		c.argv = argv
		return err
	}
	_, argv, err := c.cmd.PlanAgentLaunchWithCapability(intent.provider, coremetadata.AgentWorkspace{CWD: "/repo"}, []string{"task"}, *intent.codexCapability)
	c.argv = argv
	return err
}

func TestCodexProviderPickerDefaultAndAdvancedRowsGolden(t *testing.T) {
	t.Parallel()
	provider, ok := aiprovider.Lookup(string(aiprovider.Codex))
	if !ok {
		t.Fatal("Codex provider metadata is missing")
	}
	cmd := testAICommand(t.TempDir())
	tests := []struct {
		locale       i18n.Locale
		wantDefault  string
		wantAdvanced string
	}{
		{
			locale:       i18n.FallbackLocale,
			wantDefault:  "codex    [MISSING] Codex default launch",
			wantAdvanced: "codex+   [ADVANCED] Codex advanced launch (choose model and reasoning effort)",
		},
		{
			locale:       i18n.Locale("ko-KR"),
			wantDefault:  "codex    [MISSING] Codex 기본 실행",
			wantAdvanced: "codex+   [고급] Codex 고급 실행 (모델 및 추론 강도 선택)",
		},
	}
	for _, test := range tests {
		defaultRow := cmd.agentRow(provider, test.locale)
		advancedRow := cmd.codexAdvancedLaunchRow(test.locale)
		if got := stripANSI(defaultRow.Label); got != test.wantDefault {
			t.Errorf("locale %s default row = %q, want %q", test.locale, got, test.wantDefault)
		}
		if got := stripANSI(advancedRow.Label); got != test.wantAdvanced {
			t.Errorf("locale %s advanced row = %q, want %q", test.locale, got, test.wantAdvanced)
		}
		if defaultRow.Value != aiModeCodex || advancedRow.Value != aiActionCodexAdvancedLaunch {
			t.Errorf("locale %s actions = %q/%q, want default/advanced routing", test.locale, defaultRow.Value, advancedRow.Value)
		}
	}
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

func TestCodexAdvancedActionProjectsSelectionIntoCreateArgs(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	creator := &capabilityPlanningPaneCreator{cmd: cmd}
	cmd.panes = creator
	runner := &sequencingAIRunner{results: []intpickercompat.Result{
		{Key: "enter", Value: aiActionCodexAdvancedLaunch},
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

func TestCodexDefaultActionSkipsDiscoveryAndLaunchOverrides(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	creator := &capabilityPlanningPaneCreator{cmd: cmd}
	cmd.panes = creator
	cmd.nativePicker = nativePickerFromCompatRunner(&capturingAIRunner{result: intpickercompat.Result{Key: "enter", Value: aiModeCodex}})
	discoveryCalls := 0
	cmd.openCodexCapabilitySession = func(context.Context) (codexCapabilitySession, error) {
		discoveryCalls++
		return nil, errors.New("default launch must not discover capabilities")
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
	if discoveryCalls != 0 {
		t.Fatalf("default Codex discovery calls = %d, want 0", discoveryCalls)
	}
	if len(creator.intents) != 1 || creator.intents[0].provider != aiModeCodex || creator.intents[0].codexCapability != nil {
		t.Fatalf("default intents = %#v, want one canonical Codex intent without capability override", creator.intents)
	}
	args := execArgvTail(t, creator.argv, aiModeCodex)
	want := []string{"-C", "/repo", "task"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("default create args = %#v, want %#v (no model or effort override)", args, want)
	}
}

func TestCodexAdvancedDiscoveryFailureRefusesWithoutLaunchAndLeavesDefaultUsable(t *testing.T) {
	home := t.TempDir()
	cmd, creator := intentAICommand(t, home)
	discoveryCalls := 0
	cmd.openCodexCapabilitySession = func(context.Context) (codexCapabilitySession, error) {
		discoveryCalls++
		return nil, fmt.Errorf("%w: daemon-not-running", corecap.ErrUnavailable)
	}
	cmd.nativePicker = nativePickerFromCompatRunner(&capturingAIRunner{result: intpickercompat.Result{Key: "enter", Value: aiActionCodexAdvancedLaunch}})

	err := cmd.runAgentPickerSelection("right")
	if got, want := errString(err), "Codex advanced launch unavailable: daemon-not-running"; got != want {
		t.Fatalf("advanced unavailable error = %q, want %q", got, want)
	}
	if !errors.Is(err, corecap.ErrUnavailable) {
		t.Fatalf("advanced unavailable error = %v, want ErrUnavailable", err)
	}
	if discoveryCalls != 1 || len(creator.intents) != 0 {
		t.Fatalf("advanced failure discovery/intents = %d/%#v, want 1/zero", discoveryCalls, creator.intents)
	}

	cmd.nativePicker = nativePickerFromCompatRunner(&capturingAIRunner{result: intpickercompat.Result{Key: "enter", Value: aiModeCodex}})
	if err := cmd.runAgentPickerSelection("right"); err != nil {
		t.Fatalf("separate default launch after advanced failure: %v", err)
	}
	if discoveryCalls != 1 || len(creator.intents) != 1 || creator.intents[0].codexCapability != nil {
		t.Fatalf("default after failure discovery/intents = %d/%#v, want no new discovery and one default intent", discoveryCalls, creator.intents)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestCodexAdvancedPickerRejectsStaleEpoch(t *testing.T) {

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
		{Key: "enter", Value: aiActionCodexAdvancedLaunch},
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
