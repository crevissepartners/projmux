package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

func TestResumeLiveDiscoveryStartsProvidersTogetherAndIsolatesFailure(t *testing.T) {
	started := make(chan string, 3)
	release := make(chan struct{})
	cmd := testAICommand(t.TempDir())
	cmd.discoverResumeProvider = func(ctx context.Context, provider, cwd string, _ aisessions.DiscoverOptions, _ int) (aisessions.ProviderDiscovery, error) {
		started <- provider
		select {
		case <-release:
		case <-ctx.Done():
			return aisessions.ProviderDiscovery{}, ctx.Err()
		}
		if provider == aiModeClaude {
			return aisessions.ProviderDiscovery{}, errors.New("claude failed")
		}
		return aisessions.ProviderDiscovery{Sessions: []aisessions.SessionMeta{{Agent: provider, ResumeID: provider + "-exact", LastModified: time.Now()}}}, nil
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	defer controller.close()
	if entries := controller.initialEntries(); len(entries) != 4 || entries[0].Value != aiResumeNewValue {
		t.Fatalf("initial entries = %#v", entries)
	}
	_, _ = controller.update()
	seen := map[string]bool{}
	deadline := time.After(time.Second)
	for len(seen) < 3 {
		select {
		case provider := <-started:
			seen[provider] = true
		case <-deadline:
			t.Fatalf("providers did not start in parallel: %#v", seen)
		}
	}
	close(release)
	for range 3 {
		select {
		case <-controller.events:
			_, _ = controller.update()
		case <-time.After(time.Second):
			t.Fatal("provider completion missing")
		}
	}
	sessions := controller.snapshotSessions()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %#v, one failure must not block peers", sessions)
	}
}

func TestResumeLivePublishesRowsBeforeBoundedTurnEnrichment(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	enrichStarted := make(chan struct{})
	releaseEnrich := make(chan struct{})
	cmd.discoverResumeProvider = func(context.Context, string, string, aisessions.DiscoverOptions, int) (aisessions.ProviderDiscovery, error) {
		return aisessions.ProviderDiscovery{Sessions: []aisessions.SessionMeta{{
			Agent: aiModeClaude, ResumeID: "exact-turn-id", Title: "turn session", LastModified: time.Unix(5, 0),
		}}}, nil
	}
	cmd.enrichResumeTurns = func(sessions []aisessions.SessionMeta) []aisessions.SessionMeta {
		close(enrichStarted)
		<-releaseEnrich
		sessions[0].Turns = 9
		return sessions
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	defer controller.close()
	// This fixture owns the one provider invocation explicitly; prevent update
	// from launching the controller's normal three-provider fan-out.
	controller.startOnce.Do(func() {})
	go controller.discover(aiModeClaude)
	select {
	case <-controller.events:
	case <-time.After(time.Second):
		t.Fatal("provider metadata publication missing")
	}
	first, _ := controller.update()
	value := aiResumePickerValue(aiModeClaude, "exact-turn-id")
	firstItem, ok := pickerItemWithValue(first.Items, value)
	if !ok || strings.Contains(firstItem.EffectiveLabel(), "9t") {
		t.Fatalf("initial provider row waited for turn enrichment: %#v", first.Items)
	}
	if first.AfterApply == nil {
		t.Fatal("initial provider row missing enrichment commit callback")
	}
	first.AfterApply()
	select {
	case <-enrichStarted:
	case <-time.After(time.Second):
		t.Fatal("bounded turn enrichment did not start after first publication")
	}
	close(releaseEnrich)
	select {
	case <-controller.events:
	case <-time.After(time.Second):
		t.Fatal("enriched row publication missing")
	}
	second, _ := controller.update()
	secondItem, ok := pickerItemWithValue(second.Items, value)
	if !ok || !strings.Contains(secondItem.EffectiveLabel(), "9t") {
		t.Fatalf("enriched turn count missing: %#v", second.Items)
	}
	if secondItem.Value != firstItem.Value || secondItem.EffectiveSearchText() != firstItem.EffectiveSearchText() {
		t.Fatalf("turn enrichment changed focus/search identity: first=%#v second=%#v", firstItem, secondItem)
	}
}

func pickerItemWithValue(items []intpicker.Item, value string) (intpicker.Item, bool) {
	for _, item := range items {
		if item.Value == value {
			return item, true
		}
	}
	return intpicker.Item{}, false
}

type scriptedCodexContinuation struct {
	mu              sync.Mutex
	results         []aisessions.CodexContinuationResult
	calls           int
	closeCalls      int
	called          chan int
	started         chan struct{}
	returned        chan struct{}
	closeCh         chan struct{}
	closeOnce       sync.Once
	blockUntilClose bool
	staleAfterClose *aisessions.CodexContinuationResult
}

func (f *scriptedCodexContinuation) Continue(context.Context) (aisessions.CodexContinuationResult, error) {
	if f.returned != nil {
		defer close(f.returned)
	}
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()
	if f.called != nil {
		f.called <- call
	}
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.blockUntilClose {
		<-f.closeCh
		if f.staleAfterClose != nil {
			return *f.staleAfterClose, nil
		}
		return aisessions.CodexContinuationResult{}, errors.New("catalog closed")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if call > len(f.results) {
		return aisessions.CodexContinuationResult{}, errors.New("unexpected continuation call")
	}
	return f.results[call-1], nil
}

func (f *scriptedCodexContinuation) Close() error {
	f.closeOnce.Do(func() {
		f.mu.Lock()
		f.closeCalls++
		f.mu.Unlock()
		if f.closeCh != nil {
			close(f.closeCh)
		}
	})
	return nil
}

func (f *scriptedCodexContinuation) snapshot() (calls, closeCalls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.closeCalls
}

func TestResumeLivePublishesInitialBeforeExactPageFourContinuationAndEnrichment(t *testing.T) {
	const (
		initialID  = "019f0000-0000-7000-8000-000000000041"
		pageFourID = "019f0000-0000-7000-8000-000000000044"
	)
	initial := aisessions.SessionMeta{Agent: aiModeCodex, ResumeID: initialID, Title: "initial", Source: aisessions.SourceCodexAppServer, LastModified: time.Unix(41, 0)}
	// Keep the provider name empty so this fixture exercises #744's next
	// precedence tier: exact Registry topic/name before the short-id fallback.
	pageFour := aisessions.SessionMeta{Agent: aiModeCodex, ResumeID: pageFourID, Source: aisessions.SourceCodexAppServer, LastModified: time.Unix(44, 0)}
	continuation := &scriptedCodexContinuation{
		results: []aisessions.CodexContinuationResult{{Sessions: []aisessions.SessionMeta{initial, pageFour}}},
		called:  make(chan int, 1),
	}
	cmd := testAICommand(t.TempDir())
	registryReads := 0
	cmd.loadRegistry = func() (coremetadata.Registry, error) {
		registryReads++
		return coremetadata.Registry{Agents: []coremetadata.Agent{{
			Spec: coremetadata.AgentSpec{Provider: aiModeCodex},
			Metadata: coremetadata.ObjectMeta{UID: "agt-page-four", Name: "fallback agent name", Annotations: map[string]string{
				coremetadata.AnnotationAgentTopic: "Bound page-four topic",
			}},
			Status: coremetadata.AgentStatus{SessionRef: &coremetadata.AgentSessionRef{
				Provider: aiModeCodex, Codex: &coremetadata.CodexSessionRef{ThreadID: pageFourID},
			}},
		}}}, nil
	}
	cmd.discoverResumeProvider = func(context.Context, string, string, aisessions.DiscoverOptions, int) (aisessions.ProviderDiscovery, error) {
		return aisessions.ProviderDiscovery{
			Sessions:     []aisessions.SessionMeta{initial},
			Codex:        aisessions.CodexCatalogOutcome{Source: aisessions.CatalogSourceNative, Confidence: aisessions.CatalogConfidenceHigh},
			Continuation: continuation,
		}, nil
	}
	pageFourEnrichStarted := make(chan struct{})
	releasePageFourEnrich := make(chan struct{})
	cmd.enrichResumeTurns = func(sessions []aisessions.SessionMeta) []aisessions.SessionMeta {
		for i := range sessions {
			if sessions[i].ResumeID == pageFourID {
				close(pageFourEnrichStarted)
				<-releasePageFourEnrich
				sessions[i].Turns = 9
			}
		}
		return sessions
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	defer controller.close()
	controller.startOnce.Do(func() {})
	go controller.discover(aiModeCodex)
	select {
	case <-controller.events:
	case <-time.After(time.Second):
		t.Fatal("initial Codex result missing")
	}
	initialUpdate, _ := controller.update()
	if _, ok := pickerItemWithValue(initialUpdate.Items, aiResumePickerValue(aiModeCodex, initialID)); !ok {
		t.Fatalf("initial row not published: %#v", initialUpdate.Items)
	}
	if _, ok := pickerItemWithValue(initialUpdate.Items, aiResumePickerValue(aiModeCodex, pageFourID)); ok {
		t.Fatalf("page-four row escaped before continuation apply: %#v", initialUpdate.Items)
	}
	select {
	case call := <-continuation.called:
		t.Fatalf("continuation call %d started before initial update apply", call)
	default:
	}
	if initialUpdate.AfterApply == nil {
		t.Fatal("initial update missing commit callback")
	}
	initialUpdate.AfterApply()
	select {
	case call := <-continuation.called:
		if call != 1 {
			t.Fatalf("continuation call=%d", call)
		}
	case <-time.After(time.Second):
		t.Fatal("page-four continuation did not start after apply")
	}
	select {
	case <-controller.events:
	case <-time.After(time.Second):
		t.Fatal("page-four publication event missing")
	}
	pageFourUpdate, _ := controller.update()
	pageFourItem, ok := pickerItemWithValue(pageFourUpdate.Items, aiResumePickerValue(aiModeCodex, pageFourID))
	if !ok || !strings.Contains(pageFourItem.EffectiveLabel(), "Bound page-four topic") || strings.Contains(pageFourItem.EffectiveLabel(), "9t") {
		t.Fatalf("page-four first publication = %#v", pageFourItem)
	}
	if registryReads != 1 {
		t.Fatalf("Registry reads=%d, exact label cache must remain one read per picker", registryReads)
	}
	if pageFourUpdate.AfterApply == nil {
		t.Fatal("page-four update missing enrichment commit callback")
	}
	pageFourUpdate.AfterApply()
	select {
	case <-pageFourEnrichStarted:
	case <-time.After(time.Second):
		t.Fatal("page-four enrichment did not start after first publication")
	}
	close(releasePageFourEnrich)
	select {
	case <-controller.events:
	case <-time.After(time.Second):
		t.Fatal("page-four enriched event missing")
	}
	enrichedUpdate, _ := controller.update()
	enriched, ok := pickerItemWithValue(enrichedUpdate.Items, aiResumePickerValue(aiModeCodex, pageFourID))
	if !ok || !strings.Contains(enriched.EffectiveLabel(), "9t") || enriched.Value != pageFourItem.Value || enriched.EffectiveSearchText() != pageFourItem.EffectiveSearchText() {
		t.Fatalf("page-four enriched identity changed: first=%#v enriched=%#v", pageFourItem, enriched)
	}
}

func TestResumeLiveContinuationStopsAtTotalTwelvePagesAndShowsContentFreeNotice(t *testing.T) {
	results := make([]aisessions.CodexContinuationResult, aisessions.InteractiveCatalogTotalPages-aisessions.InteractiveCatalogPageBudget)
	for i := range results {
		results[i].HasMore = true
	}
	continuation := &scriptedCodexContinuation{results: results, called: make(chan int, len(results)+1)}
	cmd := testAICommand(t.TempDir())
	cmd.discoverResumeProvider = func(context.Context, string, string, aisessions.DiscoverOptions, int) (aisessions.ProviderDiscovery, error) {
		return aisessions.ProviderDiscovery{Continuation: continuation, Codex: aisessions.CodexCatalogOutcome{Source: aisessions.CatalogSourceNative, Confidence: aisessions.CatalogConfidenceHigh}}, nil
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	defer controller.close()
	controller.startOnce.Do(func() {})
	go controller.discover(aiModeCodex)
	<-controller.events
	initialUpdate, _ := controller.update()
	initialUpdate.AfterApply()
	for want := 1; want <= len(results); want++ {
		select {
		case got := <-continuation.called:
			if got != want {
				t.Fatalf("call=%d want=%d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("continuation stopped before call %d", want)
		}
	}
	deadline := time.After(time.Second)
	for {
		update, _ := controller.update()
		if update.MoreNotLoaded {
			if strings.Contains(update.Footer, "PRIVATE") || update.Preview.TextByValue != nil && strings.Contains(fmt.Sprint(update.Preview.TextByValue), "PRIVATE") {
				t.Fatalf("continuation notice carried content: %#v", update)
			}
			break
		}
		select {
		case <-controller.events:
		case <-deadline:
			t.Fatal("hard-cap notice missing")
		}
	}
	calls, _ := continuation.snapshot()
	if calls != len(results) {
		t.Fatalf("background calls=%d want=%d (12 total pages)", calls, len(results))
	}
}

func TestResumeLiveFakeDeadlineClosesBlockedCatalogAndRejectsLateWrites(t *testing.T) {
	continuation := &scriptedCodexContinuation{
		started: make(chan struct{}, 1), closeCh: make(chan struct{}), blockUntilClose: true,
		staleAfterClose: &aisessions.CodexContinuationResult{Sessions: []aisessions.SessionMeta{{
			Agent: aiModeCodex, ResumeID: "019f0000-0000-7000-8000-000000000099", Title: "late private row", Source: aisessions.SourceCodexAppServer,
		}}},
	}
	cmd := testAICommand(t.TempDir())
	cmd.discoverResumeProvider = func(context.Context, string, string, aisessions.DiscoverOptions, int) (aisessions.ProviderDiscovery, error) {
		return aisessions.ProviderDiscovery{Continuation: continuation, Codex: aisessions.CodexCatalogOutcome{Source: aisessions.CatalogSourceNative, Confidence: aisessions.CatalogConfidenceHigh}}, nil
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	controller.startOnce.Do(func() {})
	deadlineCtx, fireDeadline := context.WithCancel(controller.ctx)
	controller.continuationContext = func(context.Context, time.Duration) (context.Context, context.CancelFunc) {
		return deadlineCtx, fireDeadline
	}
	go controller.discover(aiModeCodex)
	<-controller.events
	initialUpdate, _ := controller.update()
	initialUpdate.AfterApply()
	select {
	case <-continuation.started:
	case <-time.After(time.Second):
		t.Fatal("blocked continuation did not start")
	}
	fireDeadline()
	deadline := time.After(time.Second)
	for {
		update, _ := controller.update()
		if update.MoreNotLoaded {
			break
		}
		select {
		case <-controller.events:
		case <-deadline:
			t.Fatal("fake 2s deadline did not publish terminal notice")
		}
	}
	calls, closes := continuation.snapshot()
	if calls != 1 || closes != 1 {
		t.Fatalf("deadline calls=%d closes=%d", calls, closes)
	}
	for _, session := range controller.snapshotSessions() {
		if session.ResumeID == "019f0000-0000-7000-8000-000000000099" {
			t.Fatalf("post-deadline stale result wrote UI state: %#v", session)
		}
	}
	controller.close()
	callsAfter, closesAfter := continuation.snapshot()
	if callsAfter != calls || closesAfter != closes {
		t.Fatalf("calls after close=%d/%d, closes=%d/%d", callsAfter, calls, closesAfter, closes)
	}
}

func TestResumeLivePeerVisibleLimitCancelsInFlightContinuationWithoutStalePublish(t *testing.T) {
	const staleID = "019f0000-0000-7000-8000-000000000099"
	continuation := &scriptedCodexContinuation{
		started: make(chan struct{}, 1), closeCh: make(chan struct{}), blockUntilClose: true,
		staleAfterClose: &aisessions.CodexContinuationResult{HasMore: true, Sessions: []aisessions.SessionMeta{{
			Agent: aiModeCodex, ResumeID: staleID, Title: "stale continuation", Source: aisessions.SourceCodexAppServer,
		}}},
	}
	cmd := testAICommand(t.TempDir())
	cmd.discoverResumeProvider = func(_ context.Context, provider, _ string, _ aisessions.DiscoverOptions, _ int) (aisessions.ProviderDiscovery, error) {
		switch provider {
		case aiModeCodex:
			return aisessions.ProviderDiscovery{
				Sessions:     []aisessions.SessionMeta{{Agent: aiModeCodex, ResumeID: "019f0000-0000-7000-8000-000000000041", Source: aisessions.SourceCodexAppServer}},
				Continuation: continuation, Codex: aisessions.CodexCatalogOutcome{Source: aisessions.CatalogSourceNative, Confidence: aisessions.CatalogConfidenceHigh},
			}, nil
		case aiModeClaude:
			return aisessions.ProviderDiscovery{Sessions: []aisessions.SessionMeta{{Agent: aiModeClaude, ResumeID: "claude-visible-limit"}}}, nil
		default:
			return aisessions.ProviderDiscovery{}, nil
		}
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 2)
	defer controller.close()
	controller.startOnce.Do(func() {})
	go controller.discover(aiModeCodex)
	<-controller.events
	initialUpdate, _ := controller.update()
	initialUpdate.AfterApply()
	select {
	case <-continuation.started:
	case <-time.After(time.Second):
		t.Fatal("in-flight continuation did not start")
	}
	go controller.discover(aiModeClaude)
	deadline := time.After(time.Second)
	for {
		calls, closes := continuation.snapshot()
		if calls == 1 && closes == 1 {
			break
		}
		select {
		case <-controller.events:
		case <-deadline:
			t.Fatalf("peer visible limit did not cancel continuation: calls=%d closes=%d", calls, closes)
		}
	}
	update, _ := controller.update()
	if !update.MoreNotLoaded {
		t.Fatal("visible-limit cursor remainder missing semantic notice")
	}
	for _, session := range controller.snapshotSessions() {
		if session.ResumeID == staleID {
			t.Fatalf("stale continuation wrote after peer cap: %#v", session)
		}
	}
	calls, _ := continuation.snapshot()
	if calls != 1 {
		t.Fatalf("continuation calls after peer cap=%d", calls)
	}
}

func TestResumeLiveCloseImmediatelyClosesBlockedContinuationAndDropsStaleResult(t *testing.T) {
	const staleID = "019f0000-0000-7000-8000-000000000099"
	continuation := &scriptedCodexContinuation{
		started: make(chan struct{}, 1), returned: make(chan struct{}), closeCh: make(chan struct{}), blockUntilClose: true,
		staleAfterClose: &aisessions.CodexContinuationResult{HasMore: true, Sessions: []aisessions.SessionMeta{{
			Agent: aiModeCodex, ResumeID: staleID, Title: "stale after picker close", Source: aisessions.SourceCodexAppServer,
		}}},
	}
	cmd := testAICommand(t.TempDir())
	cmd.discoverResumeProvider = func(context.Context, string, string, aisessions.DiscoverOptions, int) (aisessions.ProviderDiscovery, error) {
		return aisessions.ProviderDiscovery{Continuation: continuation, Codex: aisessions.CodexCatalogOutcome{Source: aisessions.CatalogSourceNative, Confidence: aisessions.CatalogConfidenceHigh}}, nil
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	controller.startOnce.Do(func() {})
	go controller.discover(aiModeCodex)
	<-controller.events
	initialUpdate, _ := controller.update()
	initialUpdate.AfterApply()
	select {
	case <-continuation.started:
	case <-time.After(time.Second):
		t.Fatal("blocked continuation did not start")
	}
	for {
		select {
		case <-controller.events:
		default:
			goto drained
		}
	}
drained:
	controller.close()
	select {
	case <-continuation.returned:
	case <-time.After(time.Second):
		t.Fatal("catalog Close did not immediately unblock context-ignoring List")
	}
	calls, closes := continuation.snapshot()
	if calls != 1 || closes != 1 {
		t.Fatalf("picker close calls=%d closes=%d", calls, closes)
	}
	if sessions := controller.snapshotSessions(); len(sessions) != 0 {
		t.Fatalf("stale close result wrote sessions: %#v", sessions)
	}
	select {
	case <-controller.events:
		t.Fatal("stale close result published a UI event")
	default:
	}
}

func TestResumePreviewCancellationLatestWinsAndCache(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	started := make(chan string, 4)
	firstContext := make(chan context.Context, 1)
	releaseFirst := make(chan struct{})
	firstReturned := make(chan struct{})
	var mu sync.Mutex
	calls := map[string]int{}
	cmd.readResumePreview = func(ctx context.Context, session aisessions.SessionMeta, _ aisessions.OpenCodexCatalog) (aisessions.Preview, error) {
		mu.Lock()
		calls[session.ResumeID]++
		mu.Unlock()
		started <- session.ResumeID
		if session.ResumeID == "one" {
			firstContext <- ctx
			// Deliberately ignore cancellation. This simulates a provider that
			// returns a successful stale response after a newer focus completed.
			<-releaseFirst
			close(firstReturned)
			return aisessions.Preview{User: "stale first", Assistant: "must never render"}, nil
		}
		return aisessions.Preview{User: "질문", Assistant: "answer"}, nil
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	defer controller.close()
	controller.sessions = []aisessions.SessionMeta{
		{Agent: aiModeCodex, ResumeID: "one", Source: aisessions.SourceCodexAppServer, UpdatedAt: time.Unix(1, 0)},
		{Agent: aiModeCodex, ResumeID: "two", Source: aisessions.SourceCodexAppServer, UpdatedAt: time.Unix(2, 0)},
	}
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
		t.Fatal("first preview context was not cancelled on focus change")
	}
	deadline := time.Now().Add(time.Second)
	for {
		update, _ := controller.update()
		if update.Preview.TextByValue[two] == "User\n질문\n\nAssistant\nanswer" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("latest preview not published: %#v", update.Preview.TextByValue)
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseFirst)
	select {
	case <-firstReturned:
	case <-time.After(time.Second):
		t.Fatal("stale first provider response did not return")
	}
	// Give the stale goroutine a scheduling window, then prove the serial/value
	// guard left the latest preview intact and never published first content.
	time.Sleep(10 * time.Millisecond)
	staleUpdate, _ := controller.update()
	if got := staleUpdate.Preview.TextByValue[two]; got != "User\n질문\n\nAssistant\nanswer" {
		t.Fatalf("stale response overwrote latest preview: %q", got)
	}
	for _, text := range staleUpdate.Preview.TextByValue {
		if strings.Contains(text, "stale first") || strings.Contains(text, "must never render") {
			t.Fatalf("stale response was published: %#v", staleUpdate.Preview.TextByValue)
		}
	}
	controller.focus(aiResumeNewValue)
	controller.focus(two)
	time.Sleep(10 * time.Millisecond)
	mu.Lock()
	got := calls["two"]
	mu.Unlock()
	if got != 1 {
		t.Fatalf("cache miss: exact two calls = %d", got)
	}
}

func TestResumePreviewCacheKeyUsesProviderExactIDAndUpdatedAtFallback(t *testing.T) {
	modified := time.Unix(7, 0)
	updated := time.Unix(8, 0)
	base := aisessions.SessionMeta{Agent: " CODEX ", ResumeID: " exact ", LastModified: modified}
	if got := aiResumePreviewCacheKey(base); got != (aiResumePreviewKey{provider: aiModeCodex, id: "exact", updatedAt: modified}) {
		t.Fatalf("fallback key = %#v", got)
	}
	base.UpdatedAt = updated
	if got := aiResumePreviewCacheKey(base); !got.updatedAt.Equal(updated) {
		t.Fatalf("provider UpdatedAt key = %#v", got)
	}
}

func TestResumePreviewFailureDegradesWithoutChangingRowsOrExactValue(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.readResumePreview = func(context.Context, aisessions.SessionMeta, aisessions.OpenCodexCatalog) (aisessions.Preview, error) {
		return aisessions.Preview{}, errors.New("provider read failed")
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	defer controller.close()
	session := aisessions.SessionMeta{Agent: aiModeClaude, ResumeID: "exact-id", LastModified: time.Unix(3, 0)}
	controller.sessions = []aisessions.SessionMeta{session}
	value := aiResumePickerValue(aiModeClaude, session.ResumeID)
	controller.focus(value)
	deadline := time.Now().Add(time.Second)
	for {
		update, _ := controller.update()
		if update.Preview.TextByValue[value] == "preview unavailable" {
			found := false
			for _, item := range update.Items {
				if item.Value == value {
					found = true
				}
			}
			if !found {
				t.Fatalf("failure removed exact resume row: %#v", update.Items)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("failure did not degrade: %#v", update.Preview.TextByValue)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestResumePreviewTimeoutDegradesWithoutBlockingPickerUpdates(t *testing.T) {
	cmd := testAICommand(t.TempDir())
	cmd.readResumePreview = func(ctx context.Context, _ aisessions.SessionMeta, _ aisessions.OpenCodexCatalog) (aisessions.Preview, error) {
		<-ctx.Done()
		return aisessions.Preview{}, ctx.Err()
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	defer controller.close()
	controller.previewTimeout = 10 * time.Millisecond
	controller.sessions = []aisessions.SessionMeta{{Agent: aiModeClaude, ResumeID: "timeout-id", LastModified: time.Unix(9, 0)}}
	value := aiResumePickerValue(aiModeClaude, "timeout-id")
	controller.focus(value)
	deadline := time.Now().Add(time.Second)
	for {
		update, _ := controller.update()
		if update.Preview.TextByValue[value] == "preview unavailable" {
			if len(update.Items) < 2 {
				t.Fatalf("timeout blocked rows: %#v", update.Items)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("preview timeout did not degrade")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestResumePreviewBytesStayEphemeralAndOutOfRowsRegistryAndCommands(t *testing.T) {
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
	cmd.readResumePreview = func(context.Context, aisessions.SessionMeta, aisessions.OpenCodexCatalog) (aisessions.Preview, error) {
		return aisessions.Preview{User: secret, Assistant: "safe reply"}, nil
	}
	controller := newAIResumeLiveController(cmd, "/work", t.TempDir(), 0, 20)
	defer controller.close()
	session := aisessions.SessionMeta{Agent: aiModeClaude, ResumeID: "exact-private-id", Title: "public title", LastModified: time.Unix(4, 0)}
	controller.sessions = []aisessions.SessionMeta{session}
	value := aiResumePickerValue(session.Agent, session.ResumeID)
	controller.focus(value)
	deadline := time.Now().Add(time.Second)
	for {
		update, _ := controller.update()
		if update.Preview.TextByValue[value] != "" {
			if update.Preview.Command != "" {
				t.Fatalf("preview escaped through shell command: %q", update.Preview.Command)
			}
			for _, item := range update.Items {
				if strings.Contains(item.EffectiveLabel(), secret) || strings.Contains(item.EffectiveSearchText(), secret) || strings.Contains(item.Value, secret) {
					t.Fatalf("preview bytes escaped into row: %#v", item)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("preview did not complete")
		}
		time.Sleep(time.Millisecond)
	}
	afterJSON, _ := json.Marshal(registry)
	if writes != 0 || sha256.Sum256(afterJSON) != beforeHash {
		t.Fatalf("preview persisted Registry state: writes=%d", writes)
	}
	if got := controller.snapshotSessions()[0]; got.ResumeID != session.ResumeID || got.Title != session.Title {
		t.Fatalf("preview mutated session metadata: %#v", got)
	}
	if commands := cmdRecorder(cmd).commands; len(commands) != 0 {
		t.Fatalf("preview invoked external command/notify path: %#v", commands)
	}
}
