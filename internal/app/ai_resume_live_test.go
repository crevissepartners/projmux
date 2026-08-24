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
	"strings"
	"sync"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
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

func resumeSummaryProviderStatus(entries []intpickercompat.Entry, provider string) string {
	for _, entry := range entries {
		if entry.Value == "status\t"+provider {
			return entry.SearchKey
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
	status := resumeSummaryProviderStatus(entries, aiModeCodex)
	if status != "codex available" {
		t.Fatalf("Codex status = %q, want available; entries=%#v", status, entries)
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

func TestResumeSummaryEmptyCodexIsAvailableAndEnvelopeExpiryIsNotFailure(t *testing.T) {
	t.Run("empty rollout store", func(t *testing.T) {
		home := t.TempDir()
		cmd := testAICommand(home)
		cmd.openCodexCatalog = func(context.Context) (aisessions.CodexCatalog, error) {
			return nil, errors.New("native unavailable")
		}
		controller := newAIResumeLiveController(cmd, "/work/app", home, 0, 20)
		defer controller.close()
		status := resumeSummaryProviderStatus(controller.initialEntries(), aiModeCodex)
		if status != "codex available · no conversations" {
			t.Fatalf("empty Codex status = %q, want available empty", status)
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
		status := resumeSummaryProviderStatus(controller.initialEntries(), aiModeCodex)
		if status != "codex available · no conversations" {
			t.Fatalf("envelope-expired Codex status = %q, want available empty", status)
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
		status := resumeSummaryProviderStatus(controller.initialEntries(), aiModeCodex)
		if status != "codex unavailable" {
			t.Fatalf("failed Codex status = %q, want unavailable", status)
		}
	})
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
	wantValue := aiResumePickerValue(aiModeCodex, exactID)
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
				return summaryDiscovery(provider, "codex-native-exact", aisessions.SourceCodexAppServer, time.Unix(30, 0)), nil
			}
			controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
			controller.populationTimeout = 100 * time.Millisecond
			entries := controller.initialEntries()
			frameHash := pickerEntryHash(entries)
			if tc.delay < controller.populationTimeout && !tc.block {
				if _, ok := resumeSummaryEntryWithValue(entries, aiResumePickerValue(aiModeCodex, "codex-native-exact")); !ok {
					t.Fatalf("in-budget native summary missing: %#v", entries)
				}
			} else if _, ok := resumeSummaryEntryWithValue(entries, aiResumePickerValue(aiModeCodex, "codex-native-exact")); ok {
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
	cmd := testAICommand(t.TempDir())
	cmd.discoverResumeSummaryProvider = func(_ context.Context, provider, _ string, _ aisessions.ResumeSummaryOptions, _ int) (aisessions.ResumeSummaryDiscovery, error) {
		switch provider {
		case aiModeCodex:
			return summaryDiscovery(provider, "codex-exact", aisessions.SourceCodexAppServer, time.Unix(30, 0)), nil
		case aiModeClaude:
			return summaryDiscovery(provider, "claude-exact", aisessions.SourceClaudeTranscript, time.Unix(20, 0)), nil
		default:
			return summaryDiscovery(provider, "antigravity-exact", aisessions.SourceAntigravityMetadata, time.Unix(10, 0)), nil
		}
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 2)
	defer controller.close()
	entries := controller.initialEntries()
	wantValues := []string{aiResumeNewValue, aiResumePickerValue(aiModeCodex, "codex-exact"), aiResumePickerValue(aiModeClaude, "claude-exact")}
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
		{Provider: aiModeCodex, ResumeID: "codex-native-exact", Source: aisessions.SourceCodexAppServer},
		{Provider: aiModeCodex, ResumeID: "codex-fallback-exact", Source: aisessions.SourceCodexRollout},
		{Provider: aiModeClaude, ResumeID: "claude-exact", Source: aisessions.SourceClaudeTranscript},
		{Provider: aiModeAntigravity, ResumeID: "antigravity-exact", Source: aisessions.SourceAntigravityHistory},
	}
	for _, summary := range summaries {
		value := aiResumePickerValue(summary.Provider, summary.ResumeID)
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
		if err != nil || update.Items != nil {
			t.Fatalf("focus/detail update mutated list: update=%#v err=%v", update, err)
		}
		if update.Preview.TextByValue[value] == "User\nquestion\n\nAssistant\nanswer" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("preview not published: %#v", update.Preview.TextByValue)
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
		if update.Preview.TextByValue[two] == "User\nlatest\n\nAssistant\nanswer" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("latest preview not published: %#v", update.Preview.TextByValue)
		}
	}
	close(releaseFirst)
	time.Sleep(10 * time.Millisecond)
	stale, _ := controller.update()
	if strings.Contains(stale.Preview.TextByValue[two], "stale first") {
		t.Fatalf("stale preview overwrote latest: %#v", stale.Preview.TextByValue)
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
		if update.Preview.TextByValue[value] == "preview unavailable" {
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
	entries := controller.entries(controller.summaries, map[string]bool{aiModeClaude: true}, map[string]bool{})
	value := aiResumePickerValue(aiModeClaude, "exact-private-id")
	controller.focus(value)
	deadline := time.Now().Add(time.Second)
	for {
		update, _ := controller.update()
		if update.Preview.TextByValue[value] != "" {
			if update.Items != nil || update.Preview.Command != "" {
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
