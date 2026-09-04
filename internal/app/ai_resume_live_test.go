package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func summaryDiscovery(provider, id, source string, modified time.Time) aisessions.ResumeSummaryDiscovery {
	summary := aisessions.ResumeSummary{
		Provider: provider, ResumeID: id, Label: provider + " summary", LastModified: modified,
		Source: source, Branch: "main",
	}
	return aisessions.ResumeSummaryDiscovery{
		Summaries:  []aisessions.ResumeSummary{summary},
		DetailRefs: []aisessions.ResumeDetailRef{{Provider: provider, ResumeID: id, LastModified: modified, Source: source}},
	}
}

func nativeSummaryDiscovery(id string, modified time.Time, route codexNativeEndpointRoute) aisessions.ResumeSummaryDiscovery {
	discovery := summaryDiscovery(aiModeCodex, id, aisessions.SourceCodexAppServer, modified)
	for i := range discovery.Summaries {
		discovery.Summaries[i].StateDomainID = route.Endpoint.StateDomainID
		discovery.Summaries[i].EndpointGenerationID = route.Endpoint.EndpointGenerationID
		discovery.Summaries[i].GenerationState = string(route.State)
	}
	for i := range discovery.DetailRefs {
		discovery.DetailRefs[i].StateDomainID = route.Endpoint.StateDomainID
		discovery.DetailRefs[i].EndpointGenerationID = route.Endpoint.EndpointGenerationID
		discovery.DetailRefs[i].GenerationState = string(route.State)
	}
	return discovery
}

type blockingResumeSummaryCatalog struct {
	closeOnce sync.Once
	closed    chan struct{}
}

func (c *blockingResumeSummaryCatalog) List(context.Context, codexappserver.CatalogQuery) (codexappserver.CatalogPage, error) {
	<-c.closed
	return codexappserver.CatalogPage{}, context.Canceled
}

func (*blockingResumeSummaryCatalog) Read(context.Context, string) (codexappserver.CatalogThread, error) {
	return codexappserver.CatalogThread{}, nil
}

func (c *blockingResumeSummaryCatalog) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func resumeSummaryProviderStatus(footer, provider string) string {
	display := map[string]string{aiModeCodex: "Codex", aiModeClaude: "Claude", aiModeAntigravity: "Antigravity"}[provider]
	providerLine, _, _ := strings.Cut(footer, "\n")
	for part := range strings.SplitSeq(providerLine, " · ") {
		if index := strings.Index(part, display+" "); index >= 0 {
			return strings.TrimSpace(part[index+len(display):])
		}
	}
	return ""
}

func TestResumeSummaryHundredsOfCodexRolloutsSettleBeforeBlockedNativeBudget(t *testing.T) {
	home := t.TempDir()
	rollouts := filepath.Join(home, ".codex", "sessions", "2026", "08", "24")
	if err := os.MkdirAll(rollouts, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 600 {
		id := fmt.Sprintf("019f0000-0000-7000-8000-%012d", i)
		body := fmt.Sprintf("{\"type\":\"session_meta\",\"payload\":{\"id\":%q,\"cwd\":\"/work/app\"}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"message\":%q}}\n", id, fmt.Sprintf("rollout %03d", i))
		if err := os.WriteFile(filepath.Join(rollouts, fmt.Sprintf("rollout-%03d.jsonl", i)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	catalog := &blockingResumeSummaryCatalog{closed: make(chan struct{})}
	cmd := testAICommand(home)
	cmd.openCodexCatalog = func(context.Context) (aisessions.CodexCatalog, error) { return catalog, nil }
	controller := newAIResumeLiveController(cmd, "/work/app", home, 0, 20)
	defer controller.close()
	started := time.Now()
	entries := controller.initialEntries()
	elapsed := time.Since(started)
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("settled first frame took %s, want <500ms", elapsed)
	}
	footer, _ := controller.footer()
	status := resumeSummaryProviderStatus(footer, aiModeCodex)
	if status != "80 found" {
		t.Fatalf("Codex status = %q, want settled pre-cap count; footer=%q", status, footer)
	}
	var codexRows int
	for _, entry := range entries {
		if strings.HasPrefix(entry.Value, "resume\t"+aiModeCodex+"\t") {
			codexRows++
		}
	}
	if codexRows == 0 {
		t.Fatalf("settled frame has no Codex rollout rows: %#v", entries)
	}
}

func TestResumeSummaryProviderFooterDistinguishesFallbackEmptyAndUnavailable(t *testing.T) {
	t.Run("empty rollout store", func(t *testing.T) {
		home := t.TempDir()
		cmd := testAICommand(home)
		cmd.openCodexCatalog = func(context.Context) (aisessions.CodexCatalog, error) {
			return nil, errors.New("native unavailable")
		}
		controller := newAIResumeLiveController(cmd, "/work/app", home, 0, 20)
		defer controller.close()
		entries := controller.initialEntries()
		footer, _ := controller.footer()
		status := resumeSummaryProviderStatus(footer, aiModeCodex)
		if status != "0 found" {
			t.Fatalf("empty fallback Codex status = %q, want zero count; entries=%#v", status, entries)
		}
	})

	t.Run("population envelope", func(t *testing.T) {
		cmd := testAICommand(t.TempDir())
		cmd.discoverResumeSummaryProvider = func(ctx context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
			if provider != aiModeCodex {
				return aisessions.ResumeSummaryDiscovery{}, nil
			}
			<-ctx.Done()
			return aisessions.ResumeSummaryDiscovery{}, ctx.Err()
		}
		controller := newAIResumeLiveController(cmd, "/work/app", t.TempDir(), 0, 20)
		controller.populationTimeout = 10 * time.Millisecond
		defer controller.close()
		entries := controller.initialEntries()
		footer, _ := controller.footer()
		status := resumeSummaryProviderStatus(footer, aiModeCodex)
		if status != "search failed" {
			t.Fatalf("envelope-expired Codex status = %q, want search failure; entries=%#v", status, entries)
		}
	})

	t.Run("genuine provider failure", func(t *testing.T) {
		cmd := testAICommand(t.TempDir())
		cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
			if provider == aiModeCodex {
				return aisessions.ResumeSummaryDiscovery{}, errors.New("broken provider")
			}
			return aisessions.ResumeSummaryDiscovery{}, nil
		}
		controller := newAIResumeLiveController(cmd, "/work/app", t.TempDir(), 0, 20)
		defer controller.close()
		entries := controller.initialEntries()
		footer, _ := controller.footer()
		status := resumeSummaryProviderStatus(footer, aiModeCodex)
		if status != "search failed" {
			t.Fatalf("failed Codex status = %q, want search failure", status)
		}
		if len(entries) != 1 || entries[0].Value != aiResumeNewValue {
			t.Fatalf("unavailable provider entered list: %#v", entries)
		}
	})
}

func TestResumeProviderStateTableProjectsFooterWithoutInformationalItems(t *testing.T) {
	tests := []struct {
		name   string
		states map[string]aiResumeProviderProjection
		want   map[string]string
	}{
		{
			name: "counts and disabled",
			states: map[string]aiResumeProviderProjection{
				aiModeCodex: {state: aiResumeProviderCount, count: 4}, aiModeClaude: {state: aiResumeProviderCount}, aiModeAntigravity: {state: aiResumeProviderDisabled},
			},
			want: map[string]string{aiModeCodex: "4 found", aiModeClaude: "0 found", aiModeAntigravity: "disabled"},
		},
		{
			name: "search failure remains provider-specific",
			states: map[string]aiResumeProviderProjection{
				aiModeCodex: {state: aiResumeProviderCount, count: 1}, aiModeClaude: {state: aiResumeProviderSearchFailed}, aiModeAntigravity: {state: aiResumeProviderCount, count: 2},
			},
			want: map[string]string{aiModeCodex: "1 found", aiModeClaude: "search failed", aiModeAntigravity: "2 found"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := testAICommand(t.TempDir())
			controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
			controller.startOnce.Do(func() {})
			controller.providerStates = test.states
			controller.summaries = []aisessions.ResumeSummary{
				{Provider: aiModeCodex, ResumeID: "codex-exact", Source: aisessions.SourceCodexRollout},
				{Provider: aiModeClaude, ResumeID: "claude-exact", Source: aisessions.SourceClaudeTranscript},
				{Provider: aiModeAntigravity, ResumeID: "antigravity-exact", Source: aisessions.SourceAntigravityMetadata},
			}
			entries := controller.initialEntries()
			for _, entry := range entries {
				if strings.HasPrefix(entry.Value, "status\t") {
					t.Fatalf("provider state leaked into item value: %#v", entry)
				}
				if entry.Value == aiResumeNewValue {
					continue
				}
				if _, ok := parseAIResumePickerValue(entry.Value); !ok {
					t.Fatalf("non-actionable item value = %q", entry.Value)
				}
			}
			footer, _ := controller.footer()
			if lines := strings.Split(footer, "\n"); len(lines) != 2 {
				t.Fatalf("provider footer lines = %#v, want provider then shown count", lines)
			}
			for provider, want := range test.want {
				if got := resumeSummaryProviderStatus(footer, provider); got != want {
					t.Fatalf("%s footer = %q, want %q; footer=%q", provider, got, want, footer)
				}
			}
			controller.close()
		})
	}
}

func TestResumeProviderProjectionCountFailureAndDisabled(t *testing.T) {
	states := map[string]aiResumeProviderProjection{
		aiModeCodex:       {state: aiResumeProviderCount},
		aiModeClaude:      {state: aiResumeProviderSearchFailed},
		aiModeAntigravity: {state: aiResumeProviderDisabled},
	}
	for _, test := range []struct {
		locale i18n.Locale
		want   []string
	}{
		{locale: i18n.FallbackLocale, want: []string{"Codex 0 found", "Claude search failed", "Antigravity disabled"}},
		{locale: i18n.Locale("ko-KR"), want: []string{"Codex 0건 발견", "Claude 검색 실패", "Antigravity 설정 꺼짐"}},
	} {
		footer := resumeProviderFooterLine(states, test.locale)
		for _, want := range test.want {
			if !strings.Contains(footer, want) {
				t.Fatalf("%s footer = %q, want %q", test.locale, footer, want)
			}
		}
		for _, forbidden := range []string{"available", "empty", "fallback", "unavailable", "대체", "사용 가능", "비어 있음", "사용 불가"} {
			if strings.Contains(footer, forbidden) {
				t.Fatalf("%s footer leaked transport vocabulary %q: %q", test.locale, forbidden, footer)
			}
		}
	}
	home := t.TempDir()
	if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), []config.AIAgentProvider{config.AIAgentCodex, config.AIAgentClaude}); err != nil {
		t.Fatal(err)
	}
	cmd := testAICommand(home)
	var antigravityReads atomic.Int32
	cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
		if provider == aiModeAntigravity {
			antigravityReads.Add(1)
		}
		return summaryDiscovery(provider, provider+"-exact", provider+"-source", time.Time{}), nil
	}
	controller := newAIResumeLiveController(cmd, "/work", home, 0, 20)
	defer controller.close()
	entries := controller.initialEntries()
	footer, _ := controller.footer()
	if antigravityReads.Load() != 0 || resumeSummaryProviderStatus(footer, aiModeAntigravity) != "disabled" {
		t.Fatalf("disabled provider reads=%d footer=%q", antigravityReads.Load(), footer)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Value, "resume\t"+aiModeAntigravity+"\t") {
			t.Fatalf("disabled provider entered selectable catalog: %#v", entry)
		}
	}
}

func TestResumeProviderStateUsesSettledAuthorityBeforeGlobalCap(t *testing.T) {
	tests := []struct {
		name    string
		result  aiResumeProviderResult
		enabled bool
		want    aiResumeProviderProjection
	}{
		{name: "native count", enabled: true, result: aiResumeProviderResult{provider: aiModeCodex, discovery: summaryDiscovery(aiModeCodex, "native", aisessions.SourceCodexAppServer, time.Time{})}, want: aiResumeProviderProjection{state: aiResumeProviderCount, count: 1}},
		{name: "native zero", enabled: true, result: aiResumeProviderResult{provider: aiModeCodex, discovery: aisessions.ResumeSummaryDiscovery{Codex: aisessions.CodexCatalogOutcome{Source: aisessions.CatalogSourceNative}}}, want: aiResumeProviderProjection{state: aiResumeProviderCount}},
		{name: "fallback stays count", enabled: true, result: aiResumeProviderResult{provider: aiModeCodex, discovery: aisessions.ResumeSummaryDiscovery{Codex: aisessions.CodexCatalogOutcome{Source: aisessions.CatalogSourceFallback}}}, want: aiResumeProviderProjection{state: aiResumeProviderCount}},
		{name: "provider failure", enabled: true, result: aiResumeProviderResult{provider: aiModeClaude, err: errors.New("failed")}, want: aiResumeProviderProjection{state: aiResumeProviderSearchFailed}},
		{name: "envelope expiry", enabled: true, result: aiResumeProviderResult{provider: aiModeAntigravity, err: context.DeadlineExceeded, envelopeExpired: true}, want: aiResumeProviderProjection{state: aiResumeProviderSearchFailed}},
		{name: "settings disabled", result: aiResumeProviderResult{provider: aiModeClaude}, want: aiResumeProviderProjection{state: aiResumeProviderDisabled}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resumeProviderProjection(test.result, test.enabled); got != test.want {
				t.Fatalf("state = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResumeProviderFooterPrecedesShownCountAndContentFreeContinuation(t *testing.T) {
	controller := newAIResumeLiveController(testAICommand(t.TempDir()), "/work", t.TempDir(), 0, 20)
	controller.startOnce.Do(func() {})
	controller.summaries = []aisessions.ResumeSummary{
		{Provider: aiModeCodex, ResumeID: "one", Source: aisessions.SourceCodexAppServer},
		{Provider: aiModeClaude, ResumeID: "two", Source: aisessions.SourceClaudeTranscript},
	}
	controller.providerStates = map[string]aiResumeProviderProjection{
		aiModeCodex: {state: aiResumeProviderCount, count: 1}, aiModeClaude: {state: aiResumeProviderCount, count: 1}, aiModeAntigravity: {state: aiResumeProviderCount},
	}
	controller.moreNotLoaded = true
	footer, moreNotLoaded := controller.footer()
	update, err := controller.update()
	if err != nil {
		t.Fatal(err)
	}
	if !moreNotLoaded || update.MoreNotLoaded || update.SetMoreNotLoaded {
		t.Fatalf("footer/continuation = footer=%q initial=%t update=%#v", footer, moreNotLoaded, update)
	}
	if update.Items != nil || update.SetChromeBands || update.SetFooter || update.Footer != "" || update.SelectionDetail == nil {
		t.Fatalf("footer update changed rows, upper chrome, or footer content: %#v", update)
	}
	lines := strings.Split(footer, "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "Providers Codex 1 found") || lines[1] != "Showing latest 2 resume sessions." {
		t.Fatalf("footer order = %#v, want provider line then shown count", lines)
	}
	controller.close()
}

func TestResumeSummaryPopulationEnvelopeKeepsCancellationSettledCodexPartial(t *testing.T) {
	const exactID = "01a032ae-129b-7b73-95f9-e15300f130e7"
	cmd := testAICommand(t.TempDir())
	lateReturned := make(chan struct{})
	cmd.discoverResumeSummaryProvider = func(ctx context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
		if provider != aiModeCodex {
			return aisessions.ResumeSummaryDiscovery{}, nil
		}
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond) // make the result-send lose to ctx.Done
		close(lateReturned)
		return summaryDiscovery(provider, exactID, aisessions.SourceCodexRollout, time.Unix(30, 0)), nil
	}
	controller := newAIResumeLiveController(cmd, "/work/app", t.TempDir(), 0, 20)
	controller.populationTimeout = 10 * time.Millisecond
	defer controller.close()
	startedAt := time.Now()
	entries := controller.initialEntries()
	if elapsed := time.Since(startedAt); elapsed >= 500*time.Millisecond {
		t.Fatalf("settled first frame took %s, want <500ms", elapsed)
	}
	select {
	case <-lateReturned:
	default:
		t.Fatal("Codex cancellation-settled result was not collected")
	}
	wantValue := aiResumePickerValueForSession(aiResumeSessionMetaFromSummary(
		summaryDiscovery(aiModeCodex, exactID, aisessions.SourceCodexRollout, time.Unix(30, 0)).Summaries[0], ""))
	entry, ok := resumeSummaryEntryWithValue(entries, wantValue)
	if !ok {
		t.Fatalf("settled frame lost matching Codex partial: %#v", entries)
	}
	selection, ok := parseAIResumePickerValue(entry.Value)
	if !ok {
		t.Fatalf("parse exact picker value %q", entry.Value)
	}
	selection = enrichAIResumeSelectionFromSummaries(selection, controller.snapshotSummaries())
	if selection.resumeID != exactID || selection.source != aisessions.SourceCodexRollout {
		t.Fatalf("settled selection intent = %#v", selection)
	}
	frameHash := pickerEntryHash(entries)
	for range 3 {
		if got := pickerEntryHash(controller.initialEntries()); got != frameHash {
			t.Fatalf("settled frame changed: got=%x want=%x", got, frameHash)
		}
		update, err := controller.update()
		if err != nil || update.Items != nil {
			t.Fatalf("post-frame update mutated items: update=%#v err=%v", update, err)
		}
	}
}

func pickerEntryHash(entries []intpickercompat.Entry) [32]byte {
	var stable strings.Builder
	for _, entry := range entries {
		stable.WriteString(entry.Value)
		stable.WriteByte(0)
		stable.WriteString(entry.Label)
		stable.WriteByte(0)
		stable.WriteString(entry.SearchKey)
		stable.WriteByte('\n')
	}
	return sha256.Sum256([]byte(stable.String()))
}

func TestResumeSummaryFirstPopulatedFrameSettlesEveryProviderPermutationOnce(t *testing.T) {
	providers := []string{aiModeCodex, aiModeClaude, aiModeAntigravity}
	permutations := [][]string{
		{aiModeCodex, aiModeClaude, aiModeAntigravity},
		{aiModeCodex, aiModeAntigravity, aiModeClaude},
		{aiModeClaude, aiModeCodex, aiModeAntigravity},
		{aiModeClaude, aiModeAntigravity, aiModeCodex},
		{aiModeAntigravity, aiModeCodex, aiModeClaude},
		{aiModeAntigravity, aiModeClaude, aiModeCodex},
	}
	var wantHash [32]byte
	for permutationNo, order := range permutations {
		t.Run(strings.Join(order, "-"), func(t *testing.T) {
			releases := map[string]chan struct{}{}
			started := make(chan string, 3)
			cmd := testAICommand(t.TempDir())
			for _, provider := range providers {
				releases[provider] = make(chan struct{})
			}
			cmd.discoverResumeSummaryProvider = func(ctx context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
				started <- provider
				select {
				case <-releases[provider]:
				case <-ctx.Done():
					return aisessions.ResumeSummaryDiscovery{}, ctx.Err()
				}
				return summaryDiscovery(provider, provider+"-exact-id", provider+"-source", time.Unix(int64(len(provider)), 0)), nil
			}
			controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
			defer controller.close()
			entriesCh := make(chan []intpickercompat.Entry, 1)
			go func() { entriesCh <- controller.initialEntries() }()
			seen := map[string]bool{}
			for len(seen) != 3 {
				select {
				case provider := <-started:
					seen[provider] = true
				case <-time.After(time.Second):
					t.Fatalf("providers did not start together: %#v", seen)
				}
			}
			for i, provider := range order {
				close(releases[provider])
				if i != len(order)-1 {
					select {
					case entries := <-entriesCh:
						t.Fatalf("populated frame published before all providers settled: %#v", entries)
					case <-time.After(5 * time.Millisecond):
					}
				}
			}
			var entries []intpickercompat.Entry
			select {
			case entries = <-entriesCh:
			case <-time.After(time.Second):
				t.Fatal("settled first frame missing")
			}
			for _, provider := range providers {
				if _, ok := resumeSummaryEntryWithValue(entries, aiResumePickerValue(provider, provider+"-exact-id")); !ok {
					t.Fatalf("settled frame missing %s: %#v", provider, entries)
				}
			}
			hash := pickerEntryHash(entries)
			if permutationNo == 0 {
				wantHash = hash
			} else if hash != wantHash {
				t.Fatalf("provider completion order changed settled frame: got=%x want=%x", hash, wantHash)
			}
			for range 3 {
				update, err := controller.update()
				if err != nil || update.Items != nil {
					t.Fatalf("post-frame update mutated items: update=%#v err=%v", update, err)
				}
			}
		})
	}
}

func TestResumeSummaryCodexDelayNeverMutatesFrameAfterOpen(t *testing.T) {
	route := nativeTestRoute("generation-delay", coremetadata.CodexGenerationCurrent)
	tests := []struct {
		name  string
		delay time.Duration
		block bool
	}{
		{name: "0ms"},
		{name: "50ms", delay: 50 * time.Millisecond},
		{name: "500ms", delay: 500 * time.Millisecond},
		{name: "blocked", block: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lateReturned := make(chan struct{})
			releaseBlocked := make(chan struct{})
			cmd := testAICommand(t.TempDir())
			cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
				if provider != aiModeCodex {
					return summaryDiscovery(provider, provider+"-exact", provider+"-source", time.Unix(20, 0)), nil
				}
				if tc.block {
					<-releaseBlocked
				} else if tc.delay > 0 {
					time.Sleep(tc.delay)
				}
				close(lateReturned)
				return nativeSummaryDiscovery("codex-native-exact", time.Unix(30, 0), route), nil
			}
			controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
			controller.populationTimeout = 100 * time.Millisecond
			entries := controller.initialEntries()
			frameHash := pickerEntryHash(entries)
			if tc.delay < controller.populationTimeout && !tc.block {
				want := aiResumePickerValueForSession(aiResumeSessionMetaFromSummary(nativeSummaryDiscovery("codex-native-exact", time.Unix(30, 0), route).Summaries[0], ""))
				if _, ok := resumeSummaryEntryWithValue(entries, want); !ok {
					t.Fatalf("in-budget native summary missing: %#v", entries)
				}
			} else if _, ok := resumeSummaryEntryWithValue(entries, aiResumePickerValueForSession(aiResumeSessionMetaFromSummary(nativeSummaryDiscovery("codex-native-exact", time.Unix(30, 0), route).Summaries[0], ""))); ok {
				t.Fatalf("late native summary entered current frame: %#v", entries)
			}
			if tc.block {
				close(releaseBlocked)
			}
			select {
			case <-lateReturned:
			case <-time.After(time.Second):
				t.Fatal("late provider did not return")
			}
			if got := pickerEntryHash(controller.initialEntries()); got != frameHash {
				t.Fatalf("late completion changed frozen frame: got=%x want=%x", got, frameHash)
			}
			update, _ := controller.update()
			if update.Items != nil {
				t.Fatalf("late completion produced list update: %#v", update.Items)
			}
			controller.close()
		})
	}
}

func TestResumeSummaryGlobalSortCapAndExactSource(t *testing.T) {
	nativeRoute := nativeTestRoute("generation-sort", coremetadata.CodexGenerationCurrent)
	cmd := testAICommand(t.TempDir())
	cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
		switch provider {
		case aiModeCodex:
			return nativeSummaryDiscovery("codex-exact", time.Unix(30, 0), nativeRoute), nil
		case aiModeClaude:
			return summaryDiscovery(provider, "claude-exact", aisessions.SourceClaudeTranscript, time.Unix(20, 0)), nil
		default:
			return summaryDiscovery(provider, "antigravity-exact", aisessions.SourceAntigravityMetadata, time.Unix(10, 0)), nil
		}
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 2)
	defer controller.close()
	entries := controller.initialEntries()
	wantValues := []string{
		aiResumeNewValue,
		aiResumePickerValueForSession(aiResumeSessionMetaFromSummary(nativeSummaryDiscovery("codex-exact", time.Unix(30, 0), nativeRoute).Summaries[0], "")),
		aiResumePickerValue(aiModeClaude, "claude-exact"),
	}
	var gotValues []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Value, "resume\t") || entry.Value == aiResumeNewValue {
			gotValues = append(gotValues, entry.Value)
		}
	}
	if !reflect.DeepEqual(gotValues, wantValues) {
		t.Fatalf("global order/cap = %#v, want %#v", gotValues, wantValues)
	}
	for _, test := range []struct{ value, source string }{
		{wantValues[1], aisessions.SourceCodexAppServer},
		{wantValues[2], aisessions.SourceClaudeTranscript},
	} {
		selection, ok := parseAIResumePickerValue(test.value)
		if !ok {
			t.Fatalf("parse exact value %q", test.value)
		}
		selection = enrichAIResumeSelectionFromSummaries(selection, controller.snapshotSummaries())
		if selection.resumeID != strings.Split(test.value, "\t")[2] || selection.source != test.source {
			t.Fatalf("selection intent lost exact id/source: %#v", selection)
		}
	}
}

func TestResumeSummaryEveryProviderAndCodexLanePreservesExactIDAndSource(t *testing.T) {
	summaries := []aisessions.ResumeSummary{
		{Provider: aiModeCodex, ResumeID: "codex-native-exact", Source: aisessions.SourceCodexAppServer,
			StateDomainID: "state-exact", EndpointGenerationID: "generation-exact", GenerationState: string(coremetadata.CodexGenerationCurrent)},
		{Provider: aiModeCodex, ResumeID: "codex-fallback-exact", Source: aisessions.SourceCodexRollout},
		{Provider: aiModeClaude, ResumeID: "claude-exact", Source: aisessions.SourceClaudeTranscript},
		{Provider: aiModeAntigravity, ResumeID: "antigravity-exact", Source: aisessions.SourceAntigravityHistory},
	}
	for _, summary := range summaries {
		value := aiResumePickerValueForSession(aiResumeSessionMetaFromSummary(summary, ""))
		selection, ok := parseAIResumePickerValue(value)
		if !ok {
			t.Fatalf("parse exact picker value %q", value)
		}
		selection = enrichAIResumeSelectionFromSummaries(selection, summaries)
		if selection.agent != summary.Provider || selection.resumeID != summary.ResumeID || selection.source != summary.Source {
			t.Fatalf("summary=%#v selection=%#v", summary, selection)
		}
	}
}

func TestCodexResumePickerPinnedValueShapeIsClosed(t *testing.T) {
	value := func(source, stateDomain, generation string, state coremetadata.CodexGenerationState) string {
		return strings.Join([]string{"resume", aiModeCodex, "thread-shape", source, stateDomain, generation, string(state)}, "\t")
	}
	for _, test := range []struct {
		name  string
		value string
		want  bool
	}{
		{name: "native exact", value: value(aisessions.SourceCodexAppServer, "state-shape", "generation-shape", coremetadata.CodexGenerationCurrent), want: true},
		{name: "rollout exact", value: value(aisessions.SourceCodexRollout, "", "", ""), want: true},
		{name: "rollout state domain", value: value(aisessions.SourceCodexRollout, "state-shape", "", "")},
		{name: "rollout generation", value: value(aisessions.SourceCodexRollout, "", "generation-shape", "")},
		{name: "rollout state", value: value(aisessions.SourceCodexRollout, "", "", coremetadata.CodexGenerationCurrent)},
		{name: "native missing state domain", value: value(aisessions.SourceCodexAppServer, "", "generation-shape", coremetadata.CodexGenerationCurrent)},
		{name: "native missing generation", value: value(aisessions.SourceCodexAppServer, "state-shape", "", coremetadata.CodexGenerationCurrent)},
		{name: "native missing state", value: value(aisessions.SourceCodexAppServer, "state-shape", "generation-shape", "")},
		{name: "native unknown state", value: value(aisessions.SourceCodexAppServer, "state-shape", "generation-shape", "unknown")},
		{name: "unknown source", value: value("codex-unknown", "", "", "")},
	} {
		t.Run(test.name, func(t *testing.T) {
			selection, got := parseAIResumePickerValue(test.value)
			if got != test.want {
				t.Fatalf("parse=%t selection=%#v want=%t value=%q", got, selection, test.want, test.value)
			}
		})
	}
}

func TestNativeResumePickerRowsPreserveExactGenerationThreadAndSource(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	draining := nativeTestRoute("generation-picker-old", coremetadata.CodexGenerationDraining)
	current := nativeTestRoute("generation-picker-current", coremetadata.CodexGenerationCurrent)
	cmd.codexNative = &fakeNativeThreadController{catalogRoutes: []codexNativeEndpointRoute{draining, current}}
	var codexCalls atomic.Int32
	cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, opts aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
		if provider != aiModeCodex {
			return aisessions.ResumeSummaryDiscovery{}, nil
		}
		if opts.FallbackBudget != codexRouteReadRolloutBudget {
			// The invocation's single rollout scan, entered with the provider.
			// It is not a generation catalog read and names no route.
			return summaryDiscovery(provider, "thread-picker-rollout", aisessions.SourceCodexRollout, time.Unix(0, 0)), nil
		}
		call := codexCalls.Add(1)
		return summaryDiscovery(provider, fmt.Sprintf("thread-picker-%d", call), aisessions.SourceCodexAppServer, time.Unix(int64(call), 0)), nil
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	defer controller.close()
	entries := controller.initialEntries()
	if got := codexCalls.Load(); got != 2 {
		t.Fatalf("generation catalog reads=%d, want one per exact route", got)
	}

	want := map[string]codexNativeEndpointRoute{"thread-picker-1": draining, "thread-picker-2": current}
	for _, entry := range entries {
		selection, parsed := parseAIResumePickerValue(entry.Value)
		if !parsed || selection.agent != aiModeCodex {
			continue
		}
		route, ok := want[selection.resumeID]
		if !ok {
			continue
		}
		if !selection.rowPinned || selection.agent != aiModeCodex ||
			selection.source != aisessions.SourceCodexAppServer || !selection.endpoint.Same(route.Endpoint) ||
			selection.state != route.State {
			t.Fatalf("picker identity lost an axis: entry=%#v selection=%#v wantRoute=%+v", entry, selection, route)
		}
		if route.State == coremetadata.CodexGenerationDraining && !strings.Contains(stripANSI(entry.Label), "[handover-required]") {
			t.Fatalf("draining row does not visibly refuse: %q", stripANSI(entry.Label))
		}
		if route.State == coremetadata.CodexGenerationCurrent && strings.Contains(stripANSI(entry.Label), "required]") {
			t.Fatalf("current row rendered blocked: %q", stripANSI(entry.Label))
		}

		// A refresh/current change after the row was displayed cannot retarget
		// Enter: the exact route axes live in Entry.Value itself.
		refreshed := []aisessions.ResumeSummary{{
			Provider: aiModeCodex, ResumeID: selection.resumeID, Source: aisessions.SourceCodexAppServer,
			StateDomainID: current.Endpoint.StateDomainID, EndpointGenerationID: current.Endpoint.EndpointGenerationID,
			GenerationState: string(coremetadata.CodexGenerationCurrent),
		}}
		if got := enrichAIResumeSelectionFromSummaries(selection, refreshed); !got.rowPinned || !got.endpoint.Same(route.Endpoint) || got.state != route.State {
			t.Fatalf("displayed row was retargeted by refresh: before=%#v after=%#v", selection, got)
		}
		delete(want, selection.resumeID)
	}
	if len(want) != 0 {
		t.Fatalf("generation picker rows missing: %v", want)
	}
}

func TestNativeResumePickerDuplicateVisibilityUsesKnownOwnerOrRefusesAmbiguous(t *testing.T) {
	const threadID = "thread-visible-from-two-generations"
	draining := nativeTestRoute("generation-collision-old", coremetadata.CodexGenerationDraining)
	current := nativeTestRoute("generation-collision-current", coremetadata.CodexGenerationCurrent)
	for _, test := range []struct {
		name       string
		knownOwner bool
		wantState  coremetadata.CodexGenerationState
	}{
		{name: "durable owner selects exact draining row", knownOwner: true, wantState: coremetadata.CodexGenerationDraining},
		{name: "unknown owner is disabled", wantState: coremetadata.CodexGenerationBlocked},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := testAICommand(t.TempDir())
			cmd.codexNative = &fakeNativeThreadController{catalogRoutes: []codexNativeEndpointRoute{draining, current}}
			if test.knownOwner {
				store := newFakeResourceStore(t)
				ref := nativeTestSessionRef(draining, threadID)
				setFixtureSessionRef(t, store, "agt-beta-codex", ref)
				cmd.loadRegistry = func() (coremetadata.Registry, error) { return store.registry.Clone(), nil }
			}
			var codexCalls atomic.Int32
			cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
				if provider != aiModeCodex {
					return aisessions.ResumeSummaryDiscovery{}, nil
				}
				call := codexCalls.Add(1)
				return summaryDiscovery(provider, threadID, aisessions.SourceCodexAppServer, time.Unix(int64(call), 0)), nil
			}
			controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
			defer controller.close()
			entries := controller.initialEntries()
			var found []aisessions.ResumeSummary
			for _, summary := range controller.snapshotSummaries() {
				if summary.Provider == aiModeCodex && summary.ResumeID == threadID {
					found = append(found, summary)
				}
			}
			if len(found) != 1 || coremetadata.CodexGenerationState(found[0].GenerationState) != test.wantState {
				t.Fatalf("collision rows=%#v, want one state=%s", found, test.wantState)
			}
			var selected aiResumeSelection
			var selectedLabel string
			for _, entry := range entries {
				selection, ok := parseAIResumePickerValue(entry.Value)
				if ok && selection.agent == aiModeCodex && selection.resumeID == threadID {
					selected, selectedLabel = selection, stripANSI(entry.Label)
					break
				}
			}
			if !selected.rowPinned || selected.source != aisessions.SourceCodexAppServer || selected.state != test.wantState ||
				!selected.endpoint.Same(codexSummaryEndpoint(found[0])) {
				t.Fatalf("collision picker row lost exact route: selection=%#v summary=%#v", selected, found[0])
			}
			wantVisible := "[generation-unavailable]"
			if test.knownOwner {
				wantVisible = "[handover-required]"
			}
			if !strings.Contains(selectedLabel, wantVisible) {
				t.Fatalf("collision row state is hidden: label=%q want=%q", selectedLabel, wantVisible)
			}
			key := aiModeCodex + "\x00" + threadID
			detail, hasDetail := controller.detailRefs[key]
			if test.knownOwner {
				if endpoint := codexSummaryEndpoint(found[0]); !endpoint.Same(draining.Endpoint) ||
					!hasDetail || !codexDetailEndpoint(detail).Same(draining.Endpoint) || detail.GenerationState != string(coremetadata.CodexGenerationDraining) {
					t.Fatalf("known owner/detail crossed generation: summary=%#v detail=%#v", found[0], detail)
				}
				return
			}
			if hasDetail {
				t.Fatalf("ambiguous row retained a cross-readable detail ref: %#v", detail)
			}

			fx := canonicalFixture(t, false)
			fx.create.codexNative = &fakeNativeThreadController{catalogRoutes: []codexNativeEndpointRoute{draining, current}}
			legacy := newFakeResumeLauncher()
			fx.create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: &fakeNativePaneLauncher{}}
			before := fx.store.snapshot()
			err := fx.create.createFromIntent(agentPaneIntent{
				producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right", anchorPaneID: fx.originID,
				conversationID: threadID, resumeSource: found[0].Source, resumeEndpoint: codexSummaryEndpoint(found[0]),
				resumeGenerationState: coremetadata.CodexGenerationState(found[0].GenerationState),
			}, ioDiscard{}, ioDiscard{})
			if err == nil || !strings.Contains(err.Error(), codexNativeReasonGenerationUnavailable) || fx.store.transactions != 0 ||
				fx.store.writes != 0 || fx.store.snapshot() != before || len(legacy.plans) != 0 || len(splitWindowCalls(fx.tmux)) != 0 {
				t.Fatalf("ambiguous selection was not write-zero: err=%v tx=%d writes=%d plans=%v splits=%v",
					err, fx.store.transactions, fx.store.writes, legacy.plans, splitWindowCalls(fx.tmux))
			}
		})
	}
}

func TestNativeResumePickerSoleCurrentVisibilityNeverOverridesKnownOldOwner(t *testing.T) {
	const threadID = "thread-owned-by-temporarily-missing-old"
	old := nativeTestRoute("generation-owner-offline", coremetadata.CodexGenerationDraining)
	current := nativeTestRoute("generation-visible-current", coremetadata.CodexGenerationCurrent)
	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-beta-codex", nativeTestSessionRef(old, threadID))

	cmd := testAICommand(t.TempDir())
	cmd.codexNative = &fakeNativeThreadController{catalogRoutes: []codexNativeEndpointRoute{current}}
	cmd.loadRegistry = func() (coremetadata.Registry, error) { return store.registry.Clone(), nil }
	cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
		if provider != aiModeCodex {
			return aisessions.ResumeSummaryDiscovery{}, nil
		}
		return summaryDiscovery(provider, threadID, aisessions.SourceCodexAppServer, time.Unix(1, 0)), nil
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	defer controller.close()
	entries := controller.initialEntries()
	summaries := controller.snapshotSummaries()
	if len(summaries) != 1 || summaries[0].GenerationState != string(coremetadata.CodexGenerationBlocked) ||
		!codexSummaryEndpoint(summaries[0]).Same(current.Endpoint) {
		t.Fatalf("sole visible current row gained old-thread authority: %#v", summaries)
	}
	if _, exists := controller.detailRefs[aiModeCodex+"\x00"+threadID]; exists {
		t.Fatal("blocked sole-visible row retained a cross-generation detail ref")
	}
	var selection aiResumeSelection
	var label string
	for _, entry := range entries {
		parsed, ok := parseAIResumePickerValue(entry.Value)
		if ok && parsed.agent == aiModeCodex && parsed.resumeID == threadID {
			selection, label = parsed, stripANSI(entry.Label)
			break
		}
	}
	if !selection.rowPinned || selection.state != coremetadata.CodexGenerationBlocked ||
		!selection.endpoint.Same(current.Endpoint) || !strings.Contains(label, "[generation-unavailable]") {
		t.Fatalf("blocked sole-visible picker row is not exact/visible: selection=%#v label=%q", selection, label)
	}

	fx := canonicalFixture(t, false)
	legacy := newFakeResumeLauncher()
	fx.create.codexNative = &fakeNativeThreadController{catalogRoutes: []codexNativeEndpointRoute{current}, resolvedRoute: current}
	fx.create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: &fakeNativePaneLauncher{}}
	before := fx.store.snapshot()
	err := fx.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right", anchorPaneID: fx.originID,
		conversationID: selection.resumeID, resumeSource: selection.source, resumeEndpoint: selection.endpoint,
		resumeGenerationState: selection.state,
	}, ioDiscard{}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), codexNativeReasonGenerationUnavailable) || fx.store.transactions != 0 ||
		fx.store.writes != 0 || fx.store.snapshot() != before || len(legacy.plans) != 0 || len(splitWindowCalls(fx.tmux)) != 0 {
		t.Fatalf("sole current visibility crossed durable old ownership: err=%v tx=%d writes=%d plans=%v splits=%v",
			err, fx.store.transactions, fx.store.writes, legacy.plans, splitWindowCalls(fx.tmux))
	}
}

func TestResumeSummaryFocusPreviewUpdatesDetailOnly(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	previewDone := make(chan struct{})
	cmd.readResumePreview = func(context.Context, aisessions.ResumeDetailRef, aisessions.OpenCodexCatalog) (aisessions.Preview, error) {
		close(previewDone)
		return aisessions.Preview{User: "question", Assistant: "answer"}, nil
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	controller.startOnce.Do(func() {})
	controller.summaries = []aisessions.ResumeSummary{{Provider: aiModeClaude, ResumeID: "exact-preview", LastModified: time.Unix(7, 0), Source: aisessions.SourceClaudeTranscript}}
	controller.detailRefs[aiModeClaude+"\x00exact-preview"] = aisessions.ResumeDetailRef{Provider: aiModeClaude, ResumeID: "exact-preview", Source: aisessions.SourceClaudeTranscript}
	value := aiResumePickerValue(aiModeClaude, "exact-preview")
	controller.focus(value)
	select {
	case <-previewDone:
	case <-time.After(time.Second):
		t.Fatal("detail preview did not complete")
	}
	deadline := time.Now().Add(time.Second)
	for {
		update, err := controller.update()
		if err != nil || update.Items != nil || update.SetHeader || update.SetFooter || update.SetMoreNotLoaded {
			t.Fatalf("focus/detail update mutated list: update=%#v err=%v", update, err)
		}
		if update.SelectionDetail != nil && strings.Contains(update.SelectionDetail.TextByValue[value], "User\nquestion\n\nAssistant\nanswer") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("detail not published: %#v", update.SelectionDetail)
		}
	}
	controller.close()
}

func TestResumeSummaryFirstInputTargetsFinalSnapshot(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
		return summaryDiscovery(provider, provider+"-exact", provider+"-source", time.Unix(int64(len(provider)), 0)), nil
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	defer controller.close()
	entries := controller.initialEntries()
	query := "antigravity-exact"
	var filtered []intpickercompat.Entry
	for _, entry := range entries {
		if strings.Contains(entry.SearchKey, query) {
			filtered = append(filtered, entry)
		}
	}
	if len(filtered) != 1 || filtered[0].Value != aiResumePickerValue(aiModeAntigravity, "antigravity-exact") {
		t.Fatalf("first query did not target final snapshot: %#v", filtered)
	}
	controller.focus(filtered[0].Value)
	if controller.focusedValue != filtered[0].Value {
		t.Fatalf("first focus = %q, want %q", controller.focusedValue, filtered[0].Value)
	}
}

func resumeSummaryEntryWithValue(entries []intpickercompat.Entry, value string) (intpickercompat.Entry, bool) {
	for _, entry := range entries {
		if entry.Value == value {
			return entry, true
		}
	}
	return intpickercompat.Entry{}, false
}

func TestResumeSummaryPreviewCacheKeyUsesProviderExactIDAndUpdatedAtFallback(t *testing.T) {
	modified := time.Unix(7, 0)
	updated := time.Unix(8, 0)
	base := aisessions.ResumeSummary{Provider: " CODEX ", ResumeID: " exact ", LastModified: modified}
	if got := aiResumePreviewCacheKey(base); got != (aiResumePreviewKey{provider: aiModeCodex, id: "exact", updatedAt: modified}) {
		t.Fatalf("fallback key = %#v", got)
	}
	base.UpdatedAt = updated
	if got := aiResumePreviewCacheKey(base); !got.updatedAt.Equal(updated) {
		t.Fatalf("provider UpdatedAt key = %#v", got)
	}
}

func TestResumeDetailProjectionOwnsTurnsRuntimeSourceAndPreview(t *testing.T) {
	summary := aisessions.ResumeSummary{Provider: aiModeCodex, ResumeID: "exact", Label: "Conversation"}
	ref := aisessions.ResumeDetailRef{
		Provider: aiModeCodex, ResumeID: "exact", Source: aisessions.SourceCodexRollout,
		Confidence: aisessions.ConfidenceMedium, Reason: aisessions.ReasonAppServerUnavailable, RuntimeStatus: "idle",
	}
	detail := aisessions.ResumeDetail{
		Turns: 31, Source: ref.Source, Confidence: ref.Confidence, Reason: ref.Reason, RuntimeStatus: ref.RuntimeStatus,
	}
	for _, test := range []struct {
		name   string
		locale i18n.Locale
		wants  []string
	}{
		{name: "en-US", locale: i18n.FallbackLocale, wants: []string{"Source: codex-rollout", "Turns: 31", "Runtime: idle", "Confidence: medium", "Reason: app-server-unavailable", "Preview", "User\nquestion"}},
		{name: "ko-KR", locale: i18n.Locale("ko-KR"), wants: []string{"소스: codex-rollout", "턴: 31", "런타임: idle", "신뢰도: medium", "사유: app-server-unavailable", "미리 보기", "User\nquestion"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			text := aiResumeDetailProjection(test.locale, summary, ref, detail, "User\nquestion")
			for _, want := range test.wants {
				if !strings.Contains(text, want) {
					t.Fatalf("detail projection = %q, want %q", text, want)
				}
			}
		})
	}
}

func TestResumeDetailHelpAndUnavailableTurnsAreLocalized(t *testing.T) {
	for _, test := range []struct {
		name            string
		locale          i18n.Locale
		wantHelp        string
		wantUnavailable string
	}{
		{name: "en-US", locale: i18n.FallbackLocale, wantHelp: "Select a resume session to see details.", wantUnavailable: "Turns: unavailable"},
		{name: "ko-KR", locale: i18n.Locale("ko-KR"), wantHelp: "재개 세션을 선택하면 상세 정보를 볼 수 있습니다.", wantUnavailable: "턴: 사용할 수 없음"},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller := &aiResumeLiveController{locale: test.locale}
			if got := controller.initialDetail().TextByValue[aiResumeNewValue]; got != test.wantHelp {
				t.Fatalf("help = %q, want %q", got, test.wantHelp)
			}
			text := aiResumeDetailProjection(test.locale, aisessions.ResumeSummary{Provider: aiModeClaude, Label: "Conversation"}, aisessions.ResumeDetailRef{}, aisessions.ResumeDetail{}, "preview unavailable")
			if !strings.Contains(text, test.wantUnavailable) {
				t.Fatalf("detail projection = %q, want %q", text, test.wantUnavailable)
			}
		})
	}
}

func TestResumeSummaryExactDetailKeyStartsOneReadWhileRefocused(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	cmd.readResumePreview = func(context.Context, aisessions.ResumeDetailRef, aisessions.OpenCodexCatalog) (aisessions.Preview, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return aisessions.Preview{User: "stable", Assistant: "detail"}, nil
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	controller.startOnce.Do(func() {})
	controller.summaries = []aisessions.ResumeSummary{{Provider: aiModeClaude, ResumeID: "exact", Source: aisessions.SourceClaudeTranscript, UpdatedAt: time.Unix(4, 0)}}
	controller.detailRefs[aiModeClaude+"\x00exact"] = aisessions.ResumeDetailRef{Provider: aiModeClaude, ResumeID: "exact", Source: aisessions.SourceClaudeTranscript}
	value := aiResumePickerValue(aiModeClaude, "exact")
	controller.focus(value)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("exact detail read did not start")
	}
	controller.focus(aiResumeNewValue)
	controller.focus(value)
	if got := calls.Load(); got != 1 {
		t.Fatalf("exact detail reads = %d, want 1 while in flight", got)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		update, _ := controller.update()
		if update.SelectionDetail != nil && strings.Contains(update.SelectionDetail.TextByValue[value], "User\nstable") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("refocused exact detail did not publish: %#v", update.SelectionDetail)
		}
	}
	controller.close()
}

func TestResumeSummaryConcurrentFocusAndUpdatesRaceSafe(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.readResumePreview = func(context.Context, aisessions.ResumeDetailRef, aisessions.OpenCodexCatalog) (aisessions.Preview, error) {
		return aisessions.Preview{User: "u", Assistant: "a"}, nil
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	controller.startOnce.Do(func() {})
	controller.summaries = []aisessions.ResumeSummary{{Provider: aiModeCodex, ResumeID: "exact", Source: aisessions.SourceCodexAppServer}}
	controller.detailRefs[aiModeCodex+"\x00exact"] = aisessions.ResumeDetailRef{Provider: aiModeCodex, ResumeID: "exact", Source: aisessions.SourceCodexAppServer}
	value := aiResumePickerValue(aiModeCodex, "exact")
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(2)
		go func() { defer wg.Done(); controller.focus(value); controller.focus(aiResumeNewValue) }()
		go func() {
			defer wg.Done()
			update, _ := controller.update()
			if update.Items != nil {
				t.Errorf("Items mutated: %#v", update.Items)
			}
		}()
	}
	wg.Wait()
	controller.close()
}

func TestResumeSummaryPreviewCancellationLatestWinsAndCache(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	started := make(chan string, 4)
	firstContext := make(chan context.Context, 1)
	releaseFirst := make(chan struct{})
	var mu sync.Mutex
	calls := map[string]int{}
	cmd.readResumePreview = func(ctx context.Context, ref aisessions.ResumeDetailRef, _ aisessions.OpenCodexCatalog) (aisessions.Preview, error) {
		mu.Lock()
		calls[ref.ResumeID]++
		mu.Unlock()
		started <- ref.ResumeID
		if ref.ResumeID == "one" {
			firstContext <- ctx
			<-releaseFirst // deliberately return stale success after cancellation
			return aisessions.Preview{User: "stale first", Assistant: "must never render"}, nil
		}
		return aisessions.Preview{User: "latest", Assistant: "answer"}, nil
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	controller.startOnce.Do(func() {})
	controller.summaries = []aisessions.ResumeSummary{
		{Provider: aiModeCodex, ResumeID: "one", Source: aisessions.SourceCodexAppServer, UpdatedAt: time.Unix(1, 0)},
		{Provider: aiModeCodex, ResumeID: "two", Source: aisessions.SourceCodexAppServer, UpdatedAt: time.Unix(2, 0)},
	}
	controller.detailRefs[aiModeCodex+"\x00one"] = aisessions.ResumeDetailRef{Provider: aiModeCodex, ResumeID: "one", Source: aisessions.SourceCodexAppServer}
	controller.detailRefs[aiModeCodex+"\x00two"] = aisessions.ResumeDetailRef{Provider: aiModeCodex, ResumeID: "two", Source: aisessions.SourceCodexAppServer}
	one, two := aiResumePickerValue(aiModeCodex, "one"), aiResumePickerValue(aiModeCodex, "two")
	controller.focus(one)
	if <-started != "one" {
		t.Fatal("first exact preview did not start")
	}
	oneCtx := <-firstContext
	controller.focus(two)
	if <-started != "two" {
		t.Fatal("second exact preview did not start")
	}
	select {
	case <-oneCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("first preview context was not cancelled")
	}
	deadline := time.Now().Add(time.Second)
	for {
		update, _ := controller.update()
		if update.Items != nil {
			t.Fatalf("preview update mutated list: %#v", update.Items)
		}
		if update.SelectionDetail != nil && strings.Contains(update.SelectionDetail.TextByValue[two], "User\nlatest\n\nAssistant\nanswer") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("latest preview not published: %#v", update.SelectionDetail)
		}
	}
	close(releaseFirst)
	time.Sleep(10 * time.Millisecond)
	stale, _ := controller.update()
	if stale.SelectionDetail != nil && strings.Contains(stale.SelectionDetail.TextByValue[two], "stale first") {
		t.Fatalf("stale preview overwrote latest: %#v", stale.SelectionDetail)
	}
	controller.focus(aiResumeNewValue)
	controller.focus(two)
	time.Sleep(10 * time.Millisecond)
	mu.Lock()
	twoCalls := calls["two"]
	mu.Unlock()
	if twoCalls != 1 {
		t.Fatalf("preview cache calls=%d, want 1", twoCalls)
	}
	controller.close()
}

func TestResumeSummaryPreviewTimeoutIsDetailOnly(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.readResumePreview = func(ctx context.Context, _ aisessions.ResumeDetailRef, _ aisessions.OpenCodexCatalog) (aisessions.Preview, error) {
		<-ctx.Done()
		return aisessions.Preview{}, ctx.Err()
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	controller.startOnce.Do(func() {})
	controller.previewTimeout = 10 * time.Millisecond
	controller.summaries = []aisessions.ResumeSummary{{Provider: aiModeClaude, ResumeID: "timeout-id", Source: aisessions.SourceClaudeTranscript}}
	controller.detailRefs[aiModeClaude+"\x00timeout-id"] = aisessions.ResumeDetailRef{Provider: aiModeClaude, ResumeID: "timeout-id", Source: aisessions.SourceClaudeTranscript}
	value := aiResumePickerValue(aiModeClaude, "timeout-id")
	controller.focus(value)
	deadline := time.Now().Add(time.Second)
	for {
		update, _ := controller.update()
		if update.Items != nil {
			t.Fatalf("timeout update mutated list: %#v", update.Items)
		}
		if update.SelectionDetail != nil && strings.Contains(update.SelectionDetail.TextByValue[value], "preview unavailable") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("preview timeout did not settle")
		}
	}
	controller.close()
}

func TestResumeSummaryPreviewBytesStayOutOfRowsRegistryAndCommands(t *testing.T) {
	const secret = "PRIVATE-PREVIEW-BYTES-한글"
	cmd := testAICommand(t.TempDir())
	registry := coremetadata.Registry{Agents: []coremetadata.Agent{{Metadata: coremetadata.ObjectMeta{UID: "agt-stable", Name: "stable"}}}}
	beforeJSON, _ := json.Marshal(registry)
	beforeHash := sha256.Sum256(beforeJSON)
	writes := 0
	cmd.loadRegistry = func() (coremetadata.Registry, error) { return registry.Clone(), nil }
	cmd.updateRegistry = func(func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		writes++
		return registry, nil
	}
	cmd.readResumePreview = func(context.Context, aisessions.ResumeDetailRef, aisessions.OpenCodexCatalog) (aisessions.Preview, error) {
		return aisessions.Preview{User: secret, Assistant: "safe reply"}, nil
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	controller.startOnce.Do(func() {})
	summary := aisessions.ResumeSummary{Provider: aiModeClaude, ResumeID: "exact-private-id", Label: "public title", Source: aisessions.SourceClaudeTranscript}
	controller.summaries = []aisessions.ResumeSummary{summary}
	controller.detailRefs[aiModeClaude+"\x00exact-private-id"] = aisessions.ResumeDetailRef{Provider: aiModeClaude, ResumeID: "exact-private-id", Source: aisessions.SourceClaudeTranscript}
	entries := controller.entries(controller.summaries)
	value := aiResumePickerValue(aiModeClaude, "exact-private-id")
	controller.focus(value)
	deadline := time.Now().Add(time.Second)
	for {
		update, _ := controller.update()
		if update.SelectionDetail != nil && update.SelectionDetail.TextByValue[value] != "" {
			if update.Items != nil || update.Preview.Command != "" || update.SetHeader || update.SetFooter || update.SetMoreNotLoaded {
				t.Fatalf("preview escaped detail-only seam: %#v", update)
			}
			for _, entry := range entries {
				if strings.Contains(entry.Label, secret) || strings.Contains(entry.SearchKey, secret) || strings.Contains(entry.Value, secret) {
					t.Fatalf("preview escaped row projection: %#v", entry)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("preview did not complete")
		}
	}
	afterJSON, _ := json.Marshal(registry)
	if writes != 0 || sha256.Sum256(afterJSON) != beforeHash || len(cmdRecorder(cmd).commands) != 0 {
		t.Fatalf("preview escaped invocation-local state: writes=%d commands=%#v", writes, cmdRecorder(cmd).commands)
	}
	controller.close()
}

// virtualResumeBudgetClock drives the staged resume budgets without sleeping.
// Every span it hands out expires exactly when the virtual clock passes that
// span's own bound, so a boundary table can name the contract durations
// (450ms, 1.97s, 2.5s) instead of scaled stand-ins.
type virtualResumeBudgetClock struct {
	mu      sync.Mutex
	now     time.Time
	pending []*virtualResumeBudgetSpan
}

type virtualResumeBudgetSpan struct {
	budget   time.Duration
	deadline time.Time
	cancel   context.CancelCauseFunc
	expired  bool
}

func newVirtualResumeBudgetClock() *virtualResumeBudgetClock {
	return &virtualResumeBudgetClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *virtualResumeBudgetClock) instant() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *virtualResumeBudgetClock) withTimeout(parent context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	c.mu.Lock()
	c.pending = append(c.pending, &virtualResumeBudgetSpan{budget: budget, deadline: c.now.Add(budget), cancel: cancel})
	c.mu.Unlock()
	return ctx, func() { cancel(context.Canceled) }
}

// advance moves the virtual clock and expires every span whose own bound has
// passed, with the same cause a real context.WithTimeout would report.
func (c *virtualResumeBudgetClock) advance(step time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(step)
	var due []*virtualResumeBudgetSpan
	for _, span := range c.pending {
		if !span.expired && !span.deadline.After(c.now) {
			span.expired = true
			due = append(due, span)
		}
	}
	c.mu.Unlock()
	for _, span := range due {
		span.cancel(context.DeadlineExceeded)
	}
}

func (c *virtualResumeBudgetClock) budgets() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	granted := make([]time.Duration, 0, len(c.pending))
	for _, span := range c.pending {
		granted = append(granted, span.budget)
	}
	return granted
}

// virtualBudgetController wires one controller onto the virtual clock so both
// the provider discovery envelope and every stage span are driven by it.
func virtualBudgetController(cmd *aiCommand, clock *virtualResumeBudgetClock, home string, depth, limit int) *aiResumeLiveController {
	controller := newAIResumeLiveController(cmd, "/work", home, depth, limit)
	controller.populationContext = clock.withTimeout
	budgets := defaultAIResumeBudgets()
	budgets.now = clock.instant
	budgets.withTimeout = clock.withTimeout
	controller.budgets = budgets
	return controller
}

func TestAIResumeStageBudgetsAreDeclaredNotDerived(t *testing.T) {
	budgets := defaultAIResumeBudgets()
	table := []struct {
		stage aiResumeBudgetStage
		want  time.Duration
	}{
		{stage: aiResumeStageRoute, want: 2500 * time.Millisecond},
		{stage: aiResumeStageNative, want: 300 * time.Millisecond},
		{stage: aiResumeStageFallback, want: 1250 * time.Millisecond},
		{stage: aiResumeStageHandoff, want: 35 * time.Millisecond},
	}
	for _, row := range table {
		if got := budgets.stage(row.stage); got != row.want {
			t.Fatalf("%s budget = %s want %s", row.stage, got, row.want)
		}
		// A zero field is filled from the declared constant, never from the
		// time another stage already spent.
		if got := (aiResumeBudgets{}).stage(row.stage); got != row.want {
			t.Fatalf("zero-value %s budget = %s want %s", row.stage, got, row.want)
		}
	}
	if got := budgets.providerTerminal(); got != 2835*time.Millisecond {
		t.Fatalf("codex provider terminal = %s want 2.835s", got)
	}
	if aiResumeSummaryPopulationBudget != 450*time.Millisecond {
		t.Fatalf("provider discovery envelope = %s want 450ms", aiResumeSummaryPopulationBudget)
	}
	if aiResumeNativeBudget != aisessions.DefaultResumeSummaryNativeBudget {
		t.Fatalf("declared native budget %s drifted from summary default %s", aiResumeNativeBudget, aisessions.DefaultResumeSummaryNativeBudget)
	}
	// Every stage is measured from its own start instant.
	clock := newVirtualResumeBudgetClock()
	budgets.now, budgets.withTimeout = clock.instant, clock.withTimeout
	route := budgets.start(context.Background(), aiResumeStageRoute)
	defer route.stop()
	clock.advance(400 * time.Millisecond)
	fallback := budgets.start(context.Background(), aiResumeStageFallback)
	defer fallback.stop()
	clock.advance(600 * time.Millisecond)
	if got := budgets.elapsed(route); got != time.Second {
		t.Fatalf("route elapsed = %s want 1s", got)
	}
	if got := budgets.elapsed(fallback); got != 600*time.Millisecond {
		t.Fatalf("fallback elapsed = %s want 600ms", got)
	}
	timeout := budgets.timeout(route)
	if timeout.Stage != aiResumeStageRoute || timeout.Budget != 2500*time.Millisecond || timeout.Elapsed != time.Second ||
		!errors.Is(timeout, context.DeadlineExceeded) {
		t.Fatalf("typed route timeout = %#v (%v)", timeout, error(timeout))
	}
}

func TestResumeRouteBudgetIsNotCutByTheProviderEnvelope(t *testing.T) {
	route := nativeTestRoute("generation-route-budget", coremetadata.CodexGenerationCurrent)
	tests := []struct {
		name         string
		routeElapsed time.Duration
		wantRoutes   bool
	}{
		{name: "450ms envelope cutoff", routeElapsed: 450 * time.Millisecond, wantRoutes: true},
		{name: "1.97s route", routeElapsed: 1970 * time.Millisecond, wantRoutes: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clock := newVirtualResumeBudgetClock()
			routeCtx := make(chan context.Context, 1)
			release := make(chan struct{})
			native := &budgetNativeThreadController{
				routes: []codexNativeEndpointRoute{route},
				observed: func(ctx context.Context) {
					routeCtx <- ctx
					<-release
				},
			}
			cmd := testAICommand(t.TempDir())
			cmd.codexNative = native
			var codexCtx atomic.Value
			cmd.discoverResumeSummaryProvider = func(ctx context.Context, provider, _ string, opts aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
				if provider != aiModeCodex {
					return summaryDiscovery(provider, provider+"-exact", provider+"-source", time.Unix(20, 0)), nil
				}
				codexCtx.Store(ctx)
				if opts.NativeBudget != aiResumeNativeBudget {
					t.Errorf("native budget delivered to summary layer = %s want %s", opts.NativeBudget, aiResumeNativeBudget)
				}
				return nativeSummaryDiscovery("codex-route-budget-exact", time.Unix(30, 0), route), nil
			}
			controller := virtualBudgetController(cmd, clock, t.TempDir(), 0, 20)
			defer controller.close()
			entries := make(chan []intpickercompat.Entry, 1)
			go func() { entries <- controller.initialEntries() }()

			observed := <-routeCtx
			clock.advance(tc.routeElapsed)
			if err := observed.Err(); err != nil {
				t.Fatalf("route context cancelled after %s: %v", tc.routeElapsed, err)
			}
			close(release)

			settled := <-entries
			if got := settledProviderState(controller, aiModeCodex); got.state != aiResumeProviderCount || got.count != 1 {
				t.Fatalf("codex provider projection after %s route = %+v", tc.routeElapsed, got)
			}
			want := aiResumePickerValueForSession(aiResumeSessionMetaFromSummary(nativeSummaryDiscovery("codex-route-budget-exact", time.Unix(30, 0), route).Summaries[0], ""))
			if _, ok := resumeSummaryEntryWithValue(settled, want); !ok {
				t.Fatalf("route resolved inside its own budget but its rows are missing: %#v", settled)
			}
			controller.mu.Lock()
			_, published := controller.catalogRoutes[codexCatalogRouteKey(route.Endpoint)]
			controller.mu.Unlock()
			if published != tc.wantRoutes {
				t.Fatalf("route published = %t want %t", published, tc.wantRoutes)
			}
			// The Codex stages never consume the shared 450ms envelope.
			if ctx, _ := codexCtx.Load().(context.Context); ctx == nil || ctx == controller.ctx {
				t.Fatalf("codex discovery context = %v", ctx)
			}
			if granted := clock.budgets(); !containsBudget(granted, aiResumeRouteBudget) || !containsBudget(granted, aiResumeFallbackBudget) {
				t.Fatalf("granted budgets %v missing route/fallback stage bounds", granted)
			}
		})
	}
}

func TestResumeRouteBudgetOverrunIsTypedWithoutLateRouteWrite(t *testing.T) {
	route := nativeTestRoute("generation-route-overrun", coremetadata.CodexGenerationCurrent)
	clock := newVirtualResumeBudgetClock()
	routeCtx := make(chan context.Context, 1)
	release := make(chan struct{})
	returned := make(chan struct{})
	native := &budgetNativeThreadController{
		routes: []codexNativeEndpointRoute{route},
		observed: func(ctx context.Context) {
			routeCtx <- ctx
			<-release
			close(returned)
		},
	}
	cmd := testAICommand(t.TempDir())
	cmd.codexNative = native
	var routeErr atomic.Value
	cmd.discoverResumeSummaryProvider = func(ctx context.Context, provider, _ string, opts aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
		if provider != aiModeCodex {
			return summaryDiscovery(provider, provider+"-exact", provider+"-source", time.Unix(20, 0)), nil
		}
		if opts.DiscoverOptions.OpenCodexCatalog == nil {
			t.Error("codex fallback lost its refusing opener")
			return aisessions.ResumeSummaryDiscovery{}, nil
		}
		if _, err := opts.DiscoverOptions.OpenCodexCatalog(ctx); err != nil {
			routeErr.Store(err)
		}
		return summaryDiscovery(aiModeCodex, "codex-rollout-exact", aisessions.SourceCodexRollout, time.Unix(30, 0)), nil
	}
	controller := virtualBudgetController(cmd, clock, t.TempDir(), 0, 20)
	defer controller.close()
	entries := make(chan []intpickercompat.Entry, 1)
	go func() { entries <- controller.initialEntries() }()

	observed := <-routeCtx
	clock.advance(aiResumeRouteBudget)
	<-observed.Done()
	settled := <-entries

	var timeout *aiResumeBudgetTimeoutError
	err, _ := routeErr.Load().(error)
	if !errors.As(err, &timeout) || timeout.Stage != aiResumeStageRoute || timeout.Budget != aiResumeRouteBudget ||
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("route overrun terminal reason = %#v (%v)", timeout, err)
	}
	controller.mu.Lock()
	published := len(controller.catalogRoutes)
	controller.mu.Unlock()
	if published != 0 {
		t.Fatalf("timed-out route published %d inventory entries", published)
	}
	if _, ok := resumeSummaryEntryWithValue(settled, aiResumePickerValueForSession(aiResumeSessionMetaFromSummary(summaryDiscovery(aiModeCodex, "codex-rollout-exact", aisessions.SourceCodexRollout, time.Unix(30, 0)).Summaries[0], ""))); !ok {
		t.Fatalf("route timeout discarded the rollout rows of the same invocation: %#v", settled)
	}
	frameHash := pickerEntryHash(settled)

	// The blocked route returns after the frame settled: no inventory write, no
	// row mutation, no list update.
	close(release)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("late route never returned")
	}
	controller.mu.Lock()
	published = len(controller.catalogRoutes)
	controller.mu.Unlock()
	if published != 0 {
		t.Fatalf("late route wrote %d inventory entries after settlement", published)
	}
	if got := pickerEntryHash(controller.initialEntries()); got != frameHash {
		t.Fatalf("late route changed the settled frame: got=%x want=%x", got, frameHash)
	}
	if update, _ := controller.update(); update.Items != nil {
		t.Fatalf("late route produced a list update: %#v", update.Items)
	}
}

func TestResumeFallbackClockRunsWhileRouteResolvesWithoutTouchingSiblings(t *testing.T) {
	route := nativeTestRoute("generation-route-sibling", coremetadata.CodexGenerationCurrent)
	clock := newVirtualResumeBudgetClock()
	routeCtx := make(chan context.Context, 1)
	release := make(chan struct{})
	native := &budgetNativeThreadController{
		routes: []codexNativeEndpointRoute{route},
		observed: func(ctx context.Context) {
			routeCtx <- ctx
			<-release
		},
	}
	cmd := testAICommand(t.TempDir())
	cmd.codexNative = native
	siblings := make(chan context.Context, 2)
	codexDiscovery := make(chan context.Context, 1)
	cmd.discoverResumeSummaryProvider = func(ctx context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
		if provider != aiModeCodex {
			siblings <- ctx
			return summaryDiscovery(provider, provider+"-exact", provider+"-source", time.Unix(20, 0)), nil
		}
		codexDiscovery <- ctx
		return nativeSummaryDiscovery("codex-sibling-exact", time.Unix(30, 0), route), nil
	}
	controller := virtualBudgetController(cmd, clock, t.TempDir(), 0, 20)
	defer controller.close()
	entries := make(chan []intpickercompat.Entry, 1)
	go func() { entries <- controller.initialEntries() }()

	observed := <-routeCtx
	claudeCtx, antigravityCtx := <-siblings, <-siblings

	// Inside every declared bound the four clocks are all still running and no
	// sibling provider context has been touched by the Codex route span.
	clock.advance(400 * time.Millisecond)
	for name, ctx := range map[string]context.Context{aiModeClaude: claudeCtx, aiModeAntigravity: antigravityCtx, "route": observed} {
		if err := ctx.Err(); err != nil {
			t.Fatalf("%s context cancelled at 400ms: %v", name, err)
		}
	}
	// The rollout clock started with the provider, so it terminalizes on its own
	// 1.25s bound while the route is still inside its 2.5s bound.
	clock.advance(aiResumeFallbackBudget - 400*time.Millisecond)
	if err := observed.Err(); err != nil {
		t.Fatalf("fallback bound cancelled the route span: %v", err)
	}
	clock.advance(aiResumeRouteBudget - aiResumeFallbackBudget - time.Millisecond)
	if err := observed.Err(); err != nil {
		t.Fatalf("route span cancelled before its own bound: %v", err)
	}
	close(release)

	discoveryCtx := <-codexDiscovery
	<-entries
	if !errors.Is(context.Cause(discoveryCtx), context.DeadlineExceeded) {
		t.Fatalf("codex rollout context cause = %v want its own deadline", context.Cause(discoveryCtx))
	}
	if discoveryCtx == observed {
		t.Fatal("rollout and route share one context")
	}
	// A Codex route that spends its whole bound leaves both sibling provider
	// results exactly as they settled.
	for _, name := range []string{aiModeClaude, aiModeAntigravity} {
		if got := settledProviderState(controller, name); got.state != aiResumeProviderCount || got.count != 1 {
			t.Fatalf("%s projection after a 2.5s Codex route = %+v", name, got)
		}
	}
}

func settledProviderState(controller *aiResumeLiveController, provider string) aiResumeProviderProjection {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.providerStates[provider]
}

func containsBudget(granted []time.Duration, want time.Duration) bool {
	return slices.Contains(granted, want)
}

// budgetNativeThreadController observes the exact context CatalogRoutes is
// called with and holds the call until the boundary table releases it.
type budgetNativeThreadController struct {
	fakeNativeThreadController
	routes   []codexNativeEndpointRoute
	err      error
	observed func(context.Context)
}

func (f *budgetNativeThreadController) CatalogRoutes(ctx context.Context) ([]codexNativeEndpointRoute, error) {
	if f.observed != nil {
		f.observed(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	return append([]codexNativeEndpointRoute(nil), f.routes...), nil
}

func TestResumeRouteTerminalReasonTableKeepsOneResultWithoutLateMutation(t *testing.T) {
	route := nativeTestRoute("generation-route-terminal", coremetadata.CodexGenerationCurrent)
	tests := []struct {
		name           string
		routes         []codexNativeEndpointRoute
		routeErr       error
		cancelInvoke   bool
		wantTypedRoute bool
		wantReason     string
	}{
		{name: "empty routes", wantReason: codexNativeReasonGenerationUnavailable},
		{name: "invalid routes", routes: []codexNativeEndpointRoute{{Endpoint: route.Endpoint}}, wantReason: codexNativeReasonGenerationUnavailable},
		{name: "inventory error", routeErr: errFakeNativeUnavailable},
		{name: "parent cancellation", routes: []codexNativeEndpointRoute{route}, cancelInvoke: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clock := newVirtualResumeBudgetClock()
			routeCtx := make(chan context.Context, 1)
			release := make(chan struct{})
			native := &budgetNativeThreadController{
				routes: tc.routes,
				err:    tc.routeErr,
				observed: func(ctx context.Context) {
					if !tc.cancelInvoke {
						return
					}
					routeCtx <- ctx
					<-release
				},
			}
			cmd := testAICommand(t.TempDir())
			cmd.codexNative = native
			var openerErr atomic.Value
			discoveries := atomic.Int64{}
			cmd.discoverResumeSummaryProvider = func(ctx context.Context, provider, _ string, opts aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
				if provider != aiModeCodex {
					return aisessions.ResumeSummaryDiscovery{}, nil
				}
				discoveries.Add(1)
				if opts.DiscoverOptions.OpenCodexCatalog != nil {
					if _, err := opts.DiscoverOptions.OpenCodexCatalog(ctx); err != nil {
						openerErr.Store(err)
					}
				}
				return aisessions.ResumeSummaryDiscovery{}, nil
			}
			controller := virtualBudgetController(cmd, clock, t.TempDir(), 0, 20)
			defer controller.close()
			entries := make(chan []intpickercompat.Entry, 1)
			go func() { entries <- controller.initialEntries() }()
			if tc.cancelInvoke {
				<-routeCtx
				controller.close()
				close(release)
			}
			settled := <-entries

			if got := discoveries.Load(); got != 1 {
				t.Fatalf("codex provider produced %d discovery attempts, want exactly one terminal result", got)
			}
			if len(settled) != 1 || settled[0].Value != aiResumeNewValue {
				t.Fatalf("terminal result rows = %#v", settled)
			}
			controller.mu.Lock()
			published := len(controller.catalogRoutes)
			controller.mu.Unlock()
			if published != 0 {
				t.Fatalf("refused route inventory published %d entries", published)
			}
			err, _ := openerErr.Load().(error)
			var timeout *aiResumeBudgetTimeoutError
			if errors.As(err, &timeout) {
				t.Fatalf("%s reported a route budget timeout: %#v", tc.name, timeout)
			}
			if tc.wantReason != "" {
				var routeErr *codexNativeRouteError
				if !errors.As(err, &routeErr) || routeErr.Reason != tc.wantReason {
					t.Fatalf("route refusal reason = %v want %s", err, tc.wantReason)
				}
			}
			if tc.routeErr != nil && !errors.Is(err, tc.routeErr) {
				t.Fatalf("inventory error lost on the way to the fallback opener: %v", err)
			}
			if tc.cancelInvoke && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled invocation route reason = %v want context.Canceled", err)
			}
		})
	}
}

// codexResumeCallLedger records the exact clock instant at which each Codex
// discovery call was entered, split by what that call is: the invocation's one
// rollout scan, or a native read of one resolved route.
type codexResumeCallLedger struct {
	mu    sync.Mutex
	scans []time.Time
	reads []time.Time
}

func (l *codexResumeCallLedger) enter(at time.Time, opts aisessions.ResumeSummaryOptions) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if opts.FallbackBudget == codexRouteReadRolloutBudget {
		l.reads = append(l.reads, at)
		return
	}
	l.scans = append(l.scans, at)
}

func (l *codexResumeCallLedger) snapshot() (scans, reads []time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]time.Time(nil), l.scans...), append([]time.Time(nil), l.reads...)
}

func rolloutSummaryDiscovery(id string, modified time.Time) aisessions.ResumeSummaryDiscovery {
	discovery := summaryDiscovery(aiModeCodex, id, aisessions.SourceCodexRollout, modified)
	discovery.Codex = aisessions.CodexCatalogOutcome{Source: aisessions.CatalogSourceFallback, Confidence: aisessions.CatalogConfidenceMedium}
	return discovery
}

func resumeEntryPresent(entries []intpickercompat.Entry, discovery aisessions.ResumeSummaryDiscovery) bool {
	value := aiResumePickerValueForSession(aiResumeSessionMetaFromSummary(discovery.Summaries[0], ""))
	_, ok := resumeSummaryEntryWithValue(entries, value)
	return ok
}

func TestResumeRolloutScanIsEnteredOnceWithTheProviderWhileTheRouteResolves(t *testing.T) {
	route := nativeTestRoute("generation-concurrent-scan", coremetadata.CodexGenerationCurrent)
	clock := newVirtualResumeBudgetClock()
	start := clock.instant()
	routeCtx := make(chan context.Context, 1)
	release := make(chan struct{})
	native := &budgetNativeThreadController{
		routes: []codexNativeEndpointRoute{route},
		observed: func(ctx context.Context) {
			routeCtx <- ctx
			<-release
		},
	}
	cmd := testAICommand(t.TempDir())
	cmd.codexNative = native
	ledger := &codexResumeCallLedger{}
	scanEntered := make(chan struct{}, 1)
	cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, opts aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
		if provider != aiModeCodex {
			return summaryDiscovery(provider, provider+"-exact", provider+"-source", time.Unix(20, 0)), nil
		}
		ledger.enter(clock.instant(), opts)
		if opts.FallbackBudget == codexRouteReadRolloutBudget {
			return nativeSummaryDiscovery("codex-native-exact", time.Unix(30, 0), route), nil
		}
		select {
		case scanEntered <- struct{}{}:
		default:
		}
		return rolloutSummaryDiscovery("codex-rollout-exact", time.Unix(10, 0)), nil
	}
	controller := virtualBudgetController(cmd, clock, t.TempDir(), 0, 20)
	defer controller.close()
	entries := make(chan []intpickercompat.Entry, 1)
	go func() { entries <- controller.initialEntries() }()

	observed := <-routeCtx
	// The scan is entered before the route can name an endpoint, so it is
	// already running while the route spends the first 1.97s of its own bound.
	<-scanEntered
	clock.advance(1970 * time.Millisecond)
	if err := observed.Err(); err != nil {
		t.Fatalf("route context cancelled at 1.97s: %v", err)
	}
	scans, _ := ledger.snapshot()
	if len(scans) != 1 || !scans[0].Equal(start) {
		t.Fatalf("rollout scan entries while the route is still resolving = %v, want exactly one at the provider start", scans)
	}
	close(release)
	<-entries

	scans, reads := ledger.snapshot()
	if len(scans) != 1 {
		t.Fatalf("rollout scan entered %d times, want exactly one per invocation", len(scans))
	}
	if got := scans[0].Sub(start); got != 0 {
		t.Fatalf("rollout scan entered %s after the provider start, want 0", got)
	}
	if len(reads) != 1 || reads[0].Sub(start) != 1970*time.Millisecond {
		t.Fatalf("native route reads = %v, want exactly one at the route terminal", reads)
	}
}

func TestRouteSlowInvocationPublishesTheRolloutRowsItAlreadyFound(t *testing.T) {
	route := nativeTestRoute("generation-route-slow-rows", coremetadata.CodexGenerationCurrent)
	clock := newVirtualResumeBudgetClock()
	start := clock.instant()
	routeCtx := make(chan context.Context, 1)
	release := make(chan struct{})
	scanCompleted := make(chan struct{})
	scanReturned := make(chan struct{})
	native := &budgetNativeThreadController{
		routes: []codexNativeEndpointRoute{route},
		observed: func(ctx context.Context) {
			routeCtx <- ctx
			<-release
		},
	}
	cmd := testAICommand(t.TempDir())
	cmd.codexNative = native
	rollout := rolloutSummaryDiscovery("codex-rollout-route-slow", time.Unix(10, 0))
	var scanSettledAt atomic.Value
	cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, opts aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
		if provider != aiModeCodex {
			return summaryDiscovery(provider, provider+"-exact", provider+"-source", time.Unix(20, 0)), nil
		}
		if opts.FallbackBudget == codexRouteReadRolloutBudget {
			// The native read spends its own bound and settles on no catalog.
			return aisessions.ResumeSummaryDiscovery{Codex: aisessions.CodexCatalogOutcome{Source: aisessions.CatalogSourceFallback}}, nil
		}
		<-scanCompleted
		scanSettledAt.Store(clock.instant())
		close(scanReturned)
		return rollout, nil
	}
	controller := virtualBudgetController(cmd, clock, t.TempDir(), 0, 20)
	defer controller.close()
	entries := make(chan []intpickercompat.Entry, 1)
	go func() { entries <- controller.initialEntries() }()

	<-routeCtx
	// depth-5 scan: 0.95s, entered with the provider, so it settles while the
	// 1.97s route is still inside its own bound.
	clock.advance(950 * time.Millisecond)
	close(scanCompleted)
	<-scanReturned
	clock.advance(1970*time.Millisecond - 950*time.Millisecond)
	close(release)
	settled := <-entries

	settledAt, _ := scanSettledAt.Load().(time.Time)
	if got := settledAt.Sub(start); got != 950*time.Millisecond {
		t.Fatalf("rollout rows became ready %s after the provider start, want 950ms (before the 1.97s route terminal)", got)
	}
	if got := settledProviderState(controller, aiModeCodex); got.state != aiResumeProviderCount || got.count != 1 {
		t.Fatalf("codex projection after a route-slow invocation = %+v, want a count of the retained rollout rows", got)
	}
	if !resumeEntryPresent(settled, rollout) {
		t.Fatalf("route-slow invocation dropped the rollout rows it already had: %#v", settled)
	}
	for _, summary := range controller.snapshotSummaries() {
		if summary.Provider != aiModeCodex {
			continue
		}
		if summary.Source != aisessions.SourceCodexRollout || summary.StateDomainID != "" ||
			summary.EndpointGenerationID != "" || summary.GenerationState != "" {
			t.Fatalf("rollout row gained native/generation authority: %#v", summary)
		}
	}
}

func TestCodexProviderTerminalTableStaysWithinTheDeclaredWorstCase(t *testing.T) {
	budgets := defaultAIResumeBudgets()
	if got := budgets.providerTerminal(); got != budgets.stage(aiResumeStageRoute)+budgets.stage(aiResumeStageNative)+budgets.stage(aiResumeStageHandoff) {
		t.Fatalf("provider terminal %s is not route + native + handoff", got)
	}
	route := nativeTestRoute("generation-terminal-table", coremetadata.CodexGenerationCurrent)
	tests := []struct {
		name         string
		routeElapsed time.Duration
		routeRoutes  []codexNativeEndpointRoute
		wantReads    int
	}{
		{name: "route spends its whole bound", routeElapsed: aiResumeRouteBudget, wantReads: 0},
		{name: "route resolves inside its bound", routeElapsed: aiResumeRouteBudget - time.Millisecond, routeRoutes: []codexNativeEndpointRoute{route}, wantReads: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clock := newVirtualResumeBudgetClock()
			start := clock.instant()
			routeCtx := make(chan context.Context, 1)
			release := make(chan struct{})
			native := &budgetNativeThreadController{
				routes: tc.routeRoutes,
				observed: func(ctx context.Context) {
					routeCtx <- ctx
					<-release
				},
			}
			cmd := testAICommand(t.TempDir())
			cmd.codexNative = native
			ledger := &codexResumeCallLedger{}
			scanEntered := make(chan struct{}, 1)
			rollout := rolloutSummaryDiscovery("codex-rollout-terminal", time.Unix(10, 0))
			cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, opts aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
				if provider != aiModeCodex {
					return summaryDiscovery(provider, provider+"-exact", provider+"-source", time.Unix(20, 0)), nil
				}
				ledger.enter(clock.instant(), opts)
				if opts.FallbackBudget == codexRouteReadRolloutBudget {
					return nativeSummaryDiscovery("codex-native-terminal", time.Unix(30, 0), route), nil
				}
				select {
				case scanEntered <- struct{}{}:
				default:
				}
				return rollout, nil
			}
			controller := virtualBudgetController(cmd, clock, t.TempDir(), 0, 20)
			defer controller.close()
			entries := make(chan []intpickercompat.Entry, 1)
			go func() { entries <- controller.initialEntries() }()

			<-routeCtx
			<-scanEntered
			clock.advance(tc.routeElapsed)
			close(release)
			settled := <-entries

			scans, reads := ledger.snapshot()
			if len(scans) != 1 || !scans[0].Equal(start) {
				t.Fatalf("rollout scan entries = %v, want exactly one at the provider start", scans)
			}
			if len(reads) != tc.wantReads {
				t.Fatalf("native route reads = %d want %d", len(reads), tc.wantReads)
			}
			// The rollout clock starts with the provider, so its terminal is
			// inside the declared provider terminal instead of route_end + 1.25s.
			rolloutTerminal := scans[0].Add(aiResumeFallbackBudget).Sub(start)
			if rolloutTerminal > controller.budgets.providerTerminal() {
				t.Fatalf("rollout terminal %s exceeds the declared provider terminal %s", rolloutTerminal, controller.budgets.providerTerminal())
			}
			if got := settledProviderState(controller, aiModeCodex); got.state != aiResumeProviderCount || got.count != 1 {
				t.Fatalf("codex projection = %+v, want one settled row inside the declared terminal", got)
			}
			if tc.wantReads == 0 && !resumeEntryPresent(settled, rollout) {
				t.Fatalf("route overrun dropped the concurrent scan's rows: %#v", settled)
			}
		})
	}
}

func TestCodexRouteReadAuthorityIsUnchangedByTheConcurrentScan(t *testing.T) {
	route := nativeTestRoute("generation-authority-scan", coremetadata.CodexGenerationCurrent)
	nativeRows := nativeSummaryDiscovery("codex-native-authority", time.Unix(30, 0), route)
	nativeEmpty := aisessions.ResumeSummaryDiscovery{Codex: aisessions.CodexCatalogOutcome{Source: aisessions.CatalogSourceNative, Confidence: aisessions.CatalogConfidenceHigh}}
	nativeFailed := aisessions.ResumeSummaryDiscovery{Codex: aisessions.CodexCatalogOutcome{Source: aisessions.CatalogSourceFallback}}
	tests := []struct {
		name        string
		read        aisessions.ResumeSummaryDiscovery
		wantCount   int
		wantRollout bool
		wantNative  bool
	}{
		{name: "native rows keep authority", read: nativeRows, wantCount: 1, wantNative: true},
		{name: "native empty keeps authority", read: nativeEmpty},
		{name: "native failure selects the concurrent scan", read: nativeFailed, wantCount: 1, wantRollout: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := testAICommand(t.TempDir())
			cmd.codexNative = &fakeNativeThreadController{catalogRoutes: []codexNativeEndpointRoute{route}}
			rollout := rolloutSummaryDiscovery("codex-rollout-authority", time.Unix(10, 0))
			cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, opts aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
				if provider != aiModeCodex {
					return aisessions.ResumeSummaryDiscovery{}, nil
				}
				if opts.FallbackBudget == codexRouteReadRolloutBudget {
					return tc.read, nil
				}
				return rollout, nil
			}
			controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
			defer controller.close()
			entries := controller.initialEntries()

			if got := settledProviderState(controller, aiModeCodex); got.state != aiResumeProviderCount || got.count != tc.wantCount {
				t.Fatalf("codex projection = %+v want count %d", got, tc.wantCount)
			}
			if got := resumeEntryPresent(entries, rollout); got != tc.wantRollout {
				t.Fatalf("rollout rows published = %t want %t", got, tc.wantRollout)
			}
			if got := resumeEntryPresent(entries, nativeRows); got != tc.wantNative {
				t.Fatalf("native rows published = %t want %t", got, tc.wantNative)
			}
			for _, summary := range controller.snapshotSummaries() {
				if summary.Provider != aiModeCodex || summary.Source != aisessions.SourceCodexRollout {
					continue
				}
				if summary.StateDomainID != "" || summary.EndpointGenerationID != "" || summary.GenerationState != "" {
					t.Fatalf("rollout row gained a generation axis: %#v", summary)
				}
			}
		})
	}
}

// providerRowsDiscovery builds one provider result with an exact row count and
// distinct ids, so a settlement table can state "Claude 143" and "Antigravity
// 4" as the observed footer counts rather than as a shape.
func providerRowsDiscovery(provider, source string, rows int, modified time.Time) aisessions.ResumeSummaryDiscovery {
	var discovery aisessions.ResumeSummaryDiscovery
	for i := range rows {
		row := summaryDiscovery(provider, fmt.Sprintf("%s-exact-%03d", provider, i), source, modified.Add(time.Duration(i)*time.Second))
		discovery.Summaries = append(discovery.Summaries, row.Summaries...)
		discovery.DetailRefs = append(discovery.DetailRefs, row.DetailRefs...)
	}
	return discovery
}

func snapshotProviderElapsed(controller *aiResumeLiveController, provider string) (time.Duration, bool) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	elapsed, ok := controller.providerElapsed[provider]
	return elapsed, ok
}

// awaitProviderElapsed blocks until one provider has terminalized and written
// its own witness. The witness is recorded at that provider's terminal rather
// than at the frame's, so waiting on it pins the virtual clock to the exact
// instant that provider settled.
func awaitProviderElapsed(t *testing.T, controller *aiResumeLiveController, provider string) time.Duration {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if elapsed, ok := snapshotProviderElapsed(controller, provider); ok {
			return elapsed
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never terminalized", provider)
		}
		runtime.Gosched()
	}
}

// siblingSettlementFixture is the sibling half of every Codex settlement arm:
// Claude reports 143 rows and Antigravity 4, exactly as the observed picker
// footer does, so any Codex outcome that moved them would change these counts.
const (
	claudeSettlementRows      = 143
	antigravitySettlementRows = 4
)

func siblingSettlementDiscovery(provider string) aisessions.ResumeSummaryDiscovery {
	switch provider {
	case aiModeClaude:
		return providerRowsDiscovery(aiModeClaude, aisessions.SourceClaudeTranscript, claudeSettlementRows, time.Unix(20, 0))
	default:
		// Antigravity holds the newest rows, so the one global cap keeps all
		// four of them and fills the rest of the visible window from Claude.
		return providerRowsDiscovery(aiModeAntigravity, aisessions.SourceAntigravityMetadata, antigravitySettlementRows, time.Unix(1000, 0))
	}
}

func settlementBudgets() aiResumeBudgets {
	budgets := defaultAIResumeBudgets()
	budgets.route, budgets.native, budgets.fallback, budgets.handoff =
		60*time.Millisecond, 20*time.Millisecond, 40*time.Millisecond, 10*time.Millisecond
	return budgets
}

// TestCodexSettlementLeavesSiblingProviderStatusAndCountUnchanged is C-4
// Guarantee for the sibling half: across the whole Codex slow/fallback/failure
// matrix, Claude 143 and Antigravity 4 keep the same status, the same count,
// and the same rows.
func TestCodexSettlementLeavesSiblingProviderStatusAndCountUnchanged(t *testing.T) {
	route := nativeTestRoute("generation-settlement", coremetadata.CodexGenerationCurrent)
	nativeRows := nativeSummaryDiscovery("codex-native-settlement", time.Unix(30, 0), route)
	rollout := rolloutSummaryDiscovery("codex-rollout-settlement", time.Unix(15, 0))
	routeReadFailure := errors.New("route read failed")
	tests := []struct {
		name       string
		routes     []codexNativeEndpointRoute
		routeErr   error
		codex      func(opts aisessions.ResumeSummaryOptions) (aisessions.ResumeSummaryDiscovery, error)
		blockCodex bool
		wantCodex  aiResumeProviderProjection
	}{
		{
			name:   "native rows",
			routes: []codexNativeEndpointRoute{route},
			codex: func(aisessions.ResumeSummaryOptions) (aisessions.ResumeSummaryDiscovery, error) {
				return nativeRows, nil
			},
			wantCodex: aiResumeProviderProjection{state: aiResumeProviderCount, count: 1},
		},
		{
			name:   "route read declines and the scan is the source",
			routes: []codexNativeEndpointRoute{route},
			codex: func(opts aisessions.ResumeSummaryOptions) (aisessions.ResumeSummaryDiscovery, error) {
				if opts.FallbackBudget == codexRouteReadRolloutBudget {
					return aisessions.ResumeSummaryDiscovery{Codex: aisessions.CodexCatalogOutcome{Source: aisessions.CatalogSourceFallback}}, nil
				}
				return rollout, nil
			},
			wantCodex: aiResumeProviderProjection{state: aiResumeProviderCount, count: 1},
		},
		{
			name:     "route inventory unavailable",
			routeErr: errFakeNativeUnavailable,
			codex: func(aisessions.ResumeSummaryOptions) (aisessions.ResumeSummaryDiscovery, error) {
				return rollout, nil
			},
			wantCodex: aiResumeProviderProjection{state: aiResumeProviderCount, count: 1},
		},
		{
			name:   "every route read fails",
			routes: []codexNativeEndpointRoute{route},
			codex: func(opts aisessions.ResumeSummaryOptions) (aisessions.ResumeSummaryDiscovery, error) {
				if opts.FallbackBudget == codexRouteReadRolloutBudget {
					return aisessions.ResumeSummaryDiscovery{}, routeReadFailure
				}
				return rollout, nil
			},
			wantCodex: aiResumeProviderProjection{state: aiResumeProviderCount, count: 1},
		},
		{
			name:   "genuine failure with nothing found",
			routes: []codexNativeEndpointRoute{route},
			codex: func(opts aisessions.ResumeSummaryOptions) (aisessions.ResumeSummaryDiscovery, error) {
				if opts.FallbackBudget == codexRouteReadRolloutBudget {
					return aisessions.ResumeSummaryDiscovery{}, routeReadFailure
				}
				return aisessions.ResumeSummaryDiscovery{}, nil
			},
			wantCodex: aiResumeProviderProjection{state: aiResumeProviderSearchFailed},
		},
		{
			name:       "codex never returns inside its own envelope",
			routes:     []codexNativeEndpointRoute{route},
			blockCodex: true,
			wantCodex:  aiResumeProviderProjection{state: aiResumeProviderSearchFailed},
		},
	}
	var wantSiblingHash [32]byte
	for index, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blocked := make(chan struct{})
			cmd := testAICommand(t.TempDir())
			cmd.codexNative = &budgetNativeThreadController{routes: tc.routes, err: tc.routeErr}
			cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, opts aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
				if provider != aiModeCodex {
					return siblingSettlementDiscovery(provider), nil
				}
				if tc.blockCodex {
					<-blocked
					return aisessions.ResumeSummaryDiscovery{}, context.Canceled
				}
				return tc.codex(opts)
			}
			controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 500)
			controller.budgets = settlementBudgets()
			controller.populationTimeout = 60 * time.Millisecond
			defer func() {
				close(blocked)
				controller.close()
			}()
			entries := controller.initialEntries()
			footer, _ := controller.footer()

			if got := settledProviderState(controller, aiModeCodex); got != tc.wantCodex {
				t.Fatalf("codex projection = %+v want %+v", got, tc.wantCodex)
			}
			for provider, want := range map[string]int{aiModeClaude: claudeSettlementRows, aiModeAntigravity: antigravitySettlementRows} {
				got := settledProviderState(controller, provider)
				if got.state != aiResumeProviderCount || got.count != want {
					t.Fatalf("%s projection under codex %q = %+v want %d found", provider, tc.name, got, want)
				}
			}
			if got := resumeSummaryProviderStatus(footer, aiModeClaude); got != "143 found" {
				t.Fatalf("claude footer under codex %q = %q", tc.name, got)
			}
			if got := resumeSummaryProviderStatus(footer, aiModeAntigravity); got != "4 found" {
				t.Fatalf("antigravity footer under codex %q = %q", tc.name, got)
			}
			// The sibling rows themselves are identical in every arm, so no Codex
			// outcome reordered, dropped, or replaced a sibling row either.
			var siblings []intpickercompat.Entry
			for _, entry := range entries {
				if strings.HasPrefix(entry.Value, "resume\t"+aiModeClaude+"\t") || strings.HasPrefix(entry.Value, "resume\t"+aiModeAntigravity+"\t") {
					siblings = append(siblings, entry)
				}
			}
			// Codex rows are older than every sibling row, so the one global cap
			// fills the visible window from the siblings in every arm.
			if len(siblings) != hooks.AIResumePickerLimitMax {
				t.Fatalf("sibling rows under codex %q = %d", tc.name, len(siblings))
			}
			hash := pickerEntryHash(siblings)
			if index == 0 {
				wantSiblingHash = hash
			} else if hash != wantSiblingHash {
				t.Fatalf("codex %q changed the sibling row set: got=%x want=%x", tc.name, hash, wantSiblingHash)
			}
		})
	}

	// Whether Codex is configured at all is a Codex fact. It used to move the
	// instant every sibling terminalized, because one shared envelope stretched
	// to the longest declared provider bound; now each sibling keeps its own.
	t.Run("codex configuration does not move the claude terminal", func(t *testing.T) {
		for _, codexEnabled := range []bool{true, false} {
			home := t.TempDir()
			agents := []config.AIAgentProvider{config.AIAgentClaude, config.AIAgentAntigravity}
			if codexEnabled {
				agents = append(agents, config.AIAgentCodex)
			}
			if err := config.SaveAIEnabledAgentsFile(filepath.Join(home, ".config", "projmux", config.AIEnabledAgentsFileName), agents); err != nil {
				t.Fatal(err)
			}
			blocked := make(chan struct{})
			cmd := testAICommand(home)
			cmd.codexNative = &budgetNativeThreadController{routes: []codexNativeEndpointRoute{route}}
			cmd.discoverResumeSummaryProvider = func(ctx context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
				switch provider {
				case aiModeCodex:
					<-blocked
					return aisessions.ResumeSummaryDiscovery{}, context.Canceled
				case aiModeClaude:
					// Claude answers 200ms after its own start: well past its own
					// 20ms bound and the handoff after it, yet comfortably inside
					// the 430ms envelope the old shared settlement would have
					// granted it whenever Codex was configured.
					_ = ctx
					time.Sleep(200 * time.Millisecond)
					return siblingSettlementDiscovery(aiModeClaude), nil
				default:
					return siblingSettlementDiscovery(provider), nil
				}
			}
			controller := newAIResumeLiveController(cmd, "/work", home, 0, 500)
			budgets := settlementBudgets()
			budgets.route = 400 * time.Millisecond
			controller.budgets = budgets
			controller.populationTimeout = 20 * time.Millisecond
			controller.initialEntries()
			claude := settledProviderState(controller, aiModeClaude)
			antigravity := settledProviderState(controller, aiModeAntigravity)
			close(blocked)
			controller.close()
			if claude.state != aiResumeProviderSearchFailed {
				t.Fatalf("codex enabled=%t moved the claude terminal: %+v", codexEnabled, claude)
			}
			if antigravity.state != aiResumeProviderCount || antigravity.count != antigravitySettlementRows {
				t.Fatalf("codex enabled=%t moved the antigravity result: %+v", codexEnabled, antigravity)
			}
		}
	})
}

// TestCodexFallbackRowsProjectFoundInsteadOfSearchFailed is C-3 Failure and
// C-4 Failure: rows this invocation already found are published as a count,
// and search_failed is reserved for a Codex terminal that has none.
func TestCodexFallbackRowsProjectFoundInsteadOfSearchFailed(t *testing.T) {
	route := nativeTestRoute("generation-fallback-found", coremetadata.CodexGenerationCurrent)
	rollout := providerRowsDiscovery(aiModeCodex, aisessions.SourceCodexRollout, 3, time.Unix(15, 0))
	rollout.Codex = aisessions.CodexCatalogOutcome{Source: aisessions.CatalogSourceFallback, Confidence: aisessions.CatalogConfidenceMedium}
	readFailure := errors.New("native read failed")
	tests := []struct {
		name      string
		routes    []codexNativeEndpointRoute
		routeErr  error
		scan      aisessions.ResumeSummaryDiscovery
		wantState aiResumeProviderState
		wantCount int
		wantRows  int
	}{
		{name: "route reads fail while the scan holds rows", routes: []codexNativeEndpointRoute{route}, scan: rollout, wantState: aiResumeProviderCount, wantCount: 3, wantRows: 3},
		{name: "route inventory unavailable while the scan holds rows", routeErr: errFakeNativeUnavailable, scan: rollout, wantState: aiResumeProviderCount, wantCount: 3, wantRows: 3},
		{name: "route reads fail and the scan found nothing", routes: []codexNativeEndpointRoute{route}, wantState: aiResumeProviderSearchFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := testAICommand(t.TempDir())
			cmd.codexNative = &budgetNativeThreadController{routes: tc.routes, err: tc.routeErr}
			cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, opts aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
				if provider != aiModeCodex {
					return aisessions.ResumeSummaryDiscovery{}, nil
				}
				if opts.FallbackBudget == codexRouteReadRolloutBudget {
					return aisessions.ResumeSummaryDiscovery{}, readFailure
				}
				return tc.scan, nil
			}
			controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
			defer controller.close()
			entries := controller.initialEntries()
			footer, _ := controller.footer()

			got := settledProviderState(controller, aiModeCodex)
			if got.state != tc.wantState || got.count != tc.wantCount {
				t.Fatalf("codex projection = %+v want %s/%d", got, tc.wantState, tc.wantCount)
			}
			wantStatus := "search failed"
			if tc.wantState == aiResumeProviderCount {
				wantStatus = fmt.Sprintf("%d found", tc.wantCount)
			}
			if status := resumeSummaryProviderStatus(footer, aiModeCodex); status != wantStatus {
				t.Fatalf("codex footer = %q want %q", status, wantStatus)
			}
			published := 0
			for _, summary := range controller.snapshotSummaries() {
				if summary.Provider != aiModeCodex {
					continue
				}
				published++
				if summary.Source != aisessions.SourceCodexRollout {
					t.Fatalf("published codex row lost its rollout source: %#v", summary)
				}
				if summary.StateDomainID != "" || summary.EndpointGenerationID != "" || summary.GenerationState != "" {
					t.Fatalf("rollout row gained a generation axis: %#v", summary)
				}
			}
			if published != tc.wantRows {
				t.Fatalf("published codex rows = %d want %d; entries=%d", published, tc.wantRows, len(entries))
			}
		})
	}

	// A partial that settles on its own cancellation still carries rows, so it
	// is a count too: the envelope expiring is not by itself a search failure.
	t.Run("cancellation-settled partial keeps its rows", func(t *testing.T) {
		cmd := testAICommand(t.TempDir())
		cmd.discoverResumeSummaryProvider = func(ctx context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
			if provider != aiModeCodex {
				return aisessions.ResumeSummaryDiscovery{}, nil
			}
			<-ctx.Done()
			return rollout, ctx.Err()
		}
		controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
		controller.populationTimeout = 10 * time.Millisecond
		defer controller.close()
		controller.initialEntries()
		footer, _ := controller.footer()

		if got := settledProviderState(controller, aiModeCodex); got.state != aiResumeProviderCount || got.count != 3 {
			t.Fatalf("cancellation-settled codex projection = %+v want 3 found", got)
		}
		if status := resumeSummaryProviderStatus(footer, aiModeCodex); status != "3 found" {
			t.Fatalf("cancellation-settled codex footer = %q", status)
		}
		published := 0
		for _, summary := range controller.snapshotSummaries() {
			if summary.Provider == aiModeCodex {
				published++
			}
		}
		if published != 3 {
			t.Fatalf("cancellation-settled codex rows published = %d want 3", published)
		}
	})
}

// TestProviderElapsedIsMeasuredOnEachProviderOwnClock is C-4 Scope: every
// provider's elapsed runs from that provider's own start to that provider's
// own terminal, so a 1.97s Codex route is never added to a sibling that
// already settled.
func TestProviderElapsedIsMeasuredOnEachProviderOwnClock(t *testing.T) {
	route := nativeTestRoute("generation-elapsed", coremetadata.CodexGenerationCurrent)
	clock := newVirtualResumeBudgetClock()
	routeCtx := make(chan context.Context, 1)
	routeRelease := make(chan struct{})
	native := &budgetNativeThreadController{
		routes: []codexNativeEndpointRoute{route},
		observed: func(ctx context.Context) {
			routeCtx <- ctx
			<-routeRelease
		},
	}
	cmd := testAICommand(t.TempDir())
	cmd.codexNative = native
	started := make(chan string, 3)
	releases := map[string]chan struct{}{aiModeClaude: make(chan struct{}), aiModeAntigravity: make(chan struct{})}
	cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
		if provider == aiModeCodex {
			return nativeSummaryDiscovery("codex-elapsed-exact", time.Unix(30, 0), route), nil
		}
		started <- provider
		<-releases[provider]
		return summaryDiscovery(provider, provider+"-elapsed-exact", provider+"-source", time.Unix(20, 0)), nil
	}
	controller := virtualBudgetController(cmd, clock, t.TempDir(), 0, 20)
	defer controller.close()
	entries := make(chan []intpickercompat.Entry, 1)
	go func() { entries <- controller.initialEntries() }()

	observedRoute := <-routeCtx
	for range 2 {
		<-started
	}

	// Claude terminalizes 120ms after its own start, while the Codex route is
	// still inside its own bound.
	clock.advance(120 * time.Millisecond)
	close(releases[aiModeClaude])
	if got := awaitProviderElapsed(t, controller, aiModeClaude); got != 120*time.Millisecond {
		t.Fatalf("claude elapsed = %s want 120ms", got)
	}
	// Antigravity terminalizes on its own clock too: 200ms from its own start.
	clock.advance(80 * time.Millisecond)
	close(releases[aiModeAntigravity])
	if got := awaitProviderElapsed(t, controller, aiModeAntigravity); got != 200*time.Millisecond {
		t.Fatalf("antigravity elapsed = %s want 200ms", got)
	}
	// The observed 1.97s route now resolves. It belongs to Codex alone.
	clock.advance(1970*time.Millisecond - 200*time.Millisecond)
	if err := observedRoute.Err(); err != nil {
		t.Fatalf("route cancelled inside its own 2.5s bound: %v", err)
	}
	close(routeRelease)
	<-entries
	codexElapsed := awaitProviderElapsed(t, controller, aiModeCodex)
	if codexElapsed < 1970*time.Millisecond {
		t.Fatalf("codex elapsed = %s want at least its 1.97s route", codexElapsed)
	}
	if got, _ := snapshotProviderElapsed(controller, aiModeClaude); got != 120*time.Millisecond {
		t.Fatalf("claude elapsed absorbed codex route time: %s", got)
	}
	if got, _ := snapshotProviderElapsed(controller, aiModeAntigravity); got != 200*time.Millisecond {
		t.Fatalf("antigravity elapsed absorbed codex route time: %s", got)
	}
	for _, provider := range []string{aiModeClaude, aiModeAntigravity, aiModeCodex} {
		if got := settledProviderState(controller, provider); got.state != aiResumeProviderCount || got.count != 1 {
			t.Fatalf("%s projection = %+v", provider, got)
		}
	}
	// Each provider was granted its own declared bound: the population budget
	// for the siblings, the declared provider terminal for Codex.
	granted := clock.budgets()
	envelopes := 0
	for _, budget := range granted {
		if budget == aiResumeSummaryPopulationBudget {
			envelopes++
		}
	}
	if envelopes != 2 || !containsBudget(granted, defaultAIResumeBudgets().providerTerminal()) {
		t.Fatalf("granted provider envelopes %v want two population budgets and one provider terminal", granted)
	}
	// The witness stays internal: it never reaches a row value or the footer.
	footer, _ := controller.footer()
	for _, forbidden := range []string{"120ms", "200ms", "1.97s", codexElapsed.String()} {
		if strings.Contains(footer, forbidden) {
			t.Fatalf("provider elapsed leaked into the footer: %q", footer)
		}
	}
}

// TestGlobalCapAppliesOnceAndProviderFooterCountsStayPreCap is C-4 Guarantee
// for the aggregation half: one global dedupe/sort/cap over the settled rows
// of all three providers, with provider counts fixed before it runs.
func TestGlobalCapAppliesOnceAndProviderFooterCountsStayPreCap(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
		switch provider {
		case aiModeCodex:
			return providerRowsDiscovery(aiModeCodex, aisessions.SourceCodexRollout, 3, time.Unix(300, 0)), nil
		case aiModeClaude:
			return providerRowsDiscovery(aiModeClaude, aisessions.SourceClaudeTranscript, 2, time.Unix(200, 0)), nil
		default:
			return providerRowsDiscovery(aiModeAntigravity, aisessions.SourceAntigravityMetadata, 1, time.Unix(100, 0)), nil
		}
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 2)
	defer controller.close()
	entries := controller.initialEntries()
	footer, moreNotLoaded := controller.footer()

	for provider, want := range map[string]string{aiModeCodex: "3 found", aiModeClaude: "2 found", aiModeAntigravity: "1 found"} {
		if got := resumeSummaryProviderStatus(footer, provider); got != want {
			t.Fatalf("%s footer count = %q want the pre-cap %q", provider, got, want)
		}
	}
	// The cap ran once over the union: the two globally newest rows survive,
	// and they are Codex rows because Codex owns the newest timestamps.
	visible := controller.snapshotSummaries()
	if len(visible) != 2 {
		t.Fatalf("global cap left %d rows, want the declared limit of 2", len(visible))
	}
	newest := providerRowsDiscovery(aiModeCodex, aisessions.SourceCodexRollout, 3, time.Unix(300, 0)).Summaries
	wantValues := []string{
		aiResumeNewValue,
		aiResumePickerValueForSession(aiResumeSessionMetaFromSummary(newest[2], "")),
		aiResumePickerValueForSession(aiResumeSessionMetaFromSummary(newest[1], "")),
	}
	var gotValues []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Value, "resume\t") || entry.Value == aiResumeNewValue {
			gotValues = append(gotValues, entry.Value)
		}
	}
	if !reflect.DeepEqual(gotValues, wantValues) {
		t.Fatalf("global sort/cap = %#v want %#v", gotValues, wantValues)
	}
	if !moreNotLoaded {
		t.Fatal("capped frame did not report a continuation")
	}
	if line := strings.Split(footer, "\n"); len(line) != 2 || line[1] != "Showing latest 2 resume sessions." {
		t.Fatalf("shown count line = %#v want the post-cap visible count", line)
	}
}

// TestProviderSettlementKeepsFooterVocabularyLocalesAndGeometry is the
// 변경 금지 경계 audit: provider settlement changes values, never the footer
// vocabulary, its two localizations, its provider order, or its geometry.
func TestProviderSettlementKeepsFooterVocabularyLocalesAndGeometry(t *testing.T) {
	states := map[string]aiResumeProviderProjection{
		aiModeCodex:       {state: aiResumeProviderCount, count: 158},
		aiModeClaude:      {state: aiResumeProviderSearchFailed},
		aiModeAntigravity: {state: aiResumeProviderDisabled},
	}
	golden := map[i18n.Locale]string{
		i18n.FallbackLocale:  "Providers Codex 158 found · Claude search failed · Antigravity disabled",
		i18n.Locale("ko-KR"): "제공자 Codex 158건 발견 · Claude 검색 실패 · Antigravity 설정 꺼짐",
	}
	for locale, want := range golden {
		if got := resumeProviderFooterLine(states, locale); got != want {
			t.Fatalf("%s provider line = %q want %q", locale, got, want)
		}
		footer := resumePickerFooter(states, 20, locale)
		lines := strings.Split(footer, "\n")
		if len(lines) != 2 || !strings.Contains(lines[0], want) {
			t.Fatalf("%s footer geometry = %#v", locale, lines)
		}
	}
	// The only three provider states are the existing ones; settlement added no
	// fourth name and no elapsed or capability token.
	stateText := map[aiResumeProviderState]string{
		aiResumeProviderCount: "7 found", aiResumeProviderSearchFailed: "search failed", aiResumeProviderDisabled: "disabled",
	}
	for state, want := range stateText {
		if got := resumeProviderStateText(i18n.FallbackLocale, aiResumeProviderProjection{state: state, count: 7}); got != want {
			t.Fatalf("provider state %q text = %q want %q", state, got, want)
		}
	}
	for _, forbidden := range []string{"elapsed", "ms", "budget", "route", "settled", "terminal", "경과", "예산"} {
		for locale := range golden {
			if strings.Contains(resumeProviderFooterLine(states, locale), forbidden) {
				t.Fatalf("%s footer leaked settlement vocabulary %q", locale, forbidden)
			}
		}
	}
}

// TestLateProviderSendAndCloseNeverMutateSettledItemsOrFooter is the race row:
// a provider that returns after its own terminal, and a controller close on
// top of it, change neither the settled Items nor the settled footer.
func TestLateProviderSendAndCloseNeverMutateSettledItemsOrFooter(t *testing.T) {
	release := make(chan struct{})
	lateReturned := make(chan struct{})
	cmd := testAICommand(t.TempDir())
	cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
		if provider != aiModeCodex {
			return siblingSettlementDiscovery(provider), nil
		}
		<-release
		close(lateReturned)
		return providerRowsDiscovery(aiModeCodex, aisessions.SourceCodexRollout, 7, time.Unix(900, 0)), nil
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 500)
	controller.budgets = settlementBudgets()
	controller.populationTimeout = 30 * time.Millisecond
	entries := controller.initialEntries()
	footer, moreNotLoaded := controller.footer()
	frameHash := pickerEntryHash(entries)

	if got := settledProviderState(controller, aiModeCodex); got.state != aiResumeProviderSearchFailed {
		t.Fatalf("codex past its own envelope = %+v want search failed", got)
	}
	if got := resumeSummaryProviderStatus(footer, aiModeClaude); got != "143 found" {
		t.Fatalf("claude footer = %q", got)
	}

	close(release)
	select {
	case <-lateReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("late provider never returned")
	}
	controller.close()

	if got := pickerEntryHash(controller.initialEntries()); got != frameHash {
		t.Fatalf("late provider send changed the settled frame: got=%x want=%x", got, frameHash)
	}
	gotFooter, gotMore := controller.footer()
	if gotFooter != footer || gotMore != moreNotLoaded {
		t.Fatalf("late provider send changed the footer: %q -> %q", footer, gotFooter)
	}
	if update, _ := controller.update(); update.Items != nil || update.SetFooter {
		t.Fatalf("late provider send produced a list or footer update: %#v", update)
	}
}
