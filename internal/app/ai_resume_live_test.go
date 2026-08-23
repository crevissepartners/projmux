package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
	cmd.discoverResumeProvider = func(ctx context.Context, provider, cwd string, _ aisessions.DiscoverOptions, _ int) ([]aisessions.SessionMeta, error) {
		started <- provider
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if provider == aiModeClaude {
			return nil, errors.New("claude failed")
		}
		return []aisessions.SessionMeta{{Agent: provider, ResumeID: provider + "-exact", LastModified: time.Now()}}, nil
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
	cmd.discoverResumeProvider = func(context.Context, string, string, aisessions.DiscoverOptions, int) ([]aisessions.SessionMeta, error) {
		return []aisessions.SessionMeta{{
			Agent: aiModeClaude, ResumeID: "exact-turn-id", Title: "turn session", LastModified: time.Unix(5, 0),
		}}, nil
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
