package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/web"
)

var agentGraphT0 = time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

// agentGraphRegistry is the resource fixture plus the Agents the message
// fixture talks between: two more in prj-alpha (one that never talks), one
// more in prj-beta, and one in prj-gone.
func agentGraphRegistry(t *testing.T) coremetadata.Registry {
	t.Helper()
	registry := resourceFixtureRegistry(t)
	add := func(uid, window string) {
		registry.Agents = append(registry.Agents, coremetadata.Agent{
			APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindAgent,
			Metadata: coremetadata.ObjectMeta{UID: uid, Name: uid,
				OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: window}, CreatedAt: resourceFixtureClock},
			Spec:   coremetadata.AgentSpec{Provider: "codex"},
			Status: coremetadata.AgentStatus{Phase: coremetadata.PhaseOffline, LastTransitionAt: resourceFixtureClock},
		})
	}
	add("agt-alpha-claude", "win-alpha-review")
	add("agt-alpha-idle", "win-alpha-main")
	add("agt-beta-quiet", "win-beta-main")
	add("agt-gone-codex", "win-gone-main")
	return registry
}

type graphMessage struct {
	ref, source, target string
	at                  time.Time
}

func graphRoute(agent, provider string) coremessage.Route {
	return coremessage.Route{AgentUID: agent, PaneUID: "pane-" + agent, ActivationGeneration: "generation-1",
		Provider: provider, Incarnation: "incarnation-" + agent}
}

func graphPayload(ref string) string { return "body of " + ref }

// graphRecord is a delivered store record that passes the store's own
// validation.
func graphRecord(t *testing.T, m graphMessage) messagestore.Record {
	t.Helper()
	envelope := coremessage.Envelope{
		Version: coremessage.Version, MessageRef: m.ref, ConversationRef: "conversation-" + m.ref,
		Source: graphRoute(m.source, "claude"), Target: graphRoute(m.target, "codex"),
		Authority: coremessage.PeerAuthority(), Payload: graphPayload(m.ref),
		AcceptedAt: m.at, Deadline: m.at.Add(time.Hour),
	}
	delivery, ok := coremessage.Reduce(coremessage.Delivery{}, envelope, coremessage.Event{Kind: coremessage.EventAccept,
		MessageRef: m.ref, ConversationRef: envelope.ConversationRef, Target: envelope.Target, ObservedAt: m.at})
	if !ok {
		t.Fatalf("accept %s", m.ref)
	}
	delivery, ok = coremessage.Reduce(delivery, envelope, coremessage.Event{Kind: coremessage.EventDeliver,
		MessageRef: m.ref, ConversationRef: envelope.ConversationRef, Target: envelope.Target,
		Reason: "graph-test", ObservedAt: m.at.Add(time.Second)})
	if !ok {
		t.Fatalf("deliver %s", m.ref)
	}
	return messagestore.Record{Envelope: envelope, Delivery: delivery, Adapter: "codex-inbox"}
}

// graphHistoryLine renders one reclaim-log line with the writer's keys.
func graphHistoryLine(t *testing.T, m graphMessage) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"schemaVersion": 1, "evictedAt": m.at.Add(48 * time.Hour), "reason": "retention", "adapter": "codex-inbox",
		"messageRef": m.ref, "conversationRef": "conversation-" + m.ref, "state": coremessage.StateDelivered,
		"deliveryReason": "graph-test", "outcomeUnknown": false, "handoffObserved": false,
		"acceptedAt": m.at, "deadline": m.at.Add(time.Hour), "terminalAt": m.at.Add(time.Second),
		"payloadBytes": len(graphPayload(m.ref)),
		"source":       graphRoute(m.source, "claude"), "target": graphRoute(m.target, "codex"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(line) + "\n"
}

// graphNarrowHistoryLine renders one reclaim-log line in the shape the writer
// writes now: agentmessage's historyLine, keys in its order, routes narrowed
// to agentUID and provider, no deadline, and outcomeUnknown absent because it
// is false. encodeHistoryLine is unexported, so the struct mirrors it here.
// graphHistoryLine is the full-route shape lines were written in before.
func graphNarrowHistoryLine(t *testing.T, m graphMessage) string {
	t.Helper()
	type route struct {
		AgentUID string `json:"agentUID"`
		Provider string `json:"provider"`
	}
	line, err := json.Marshal(struct {
		SchemaVersion   int               `json:"schemaVersion"`
		EvictedAt       time.Time         `json:"evictedAt"`
		Reason          string            `json:"reason"`
		Adapter         string            `json:"adapter"`
		MessageRef      string            `json:"messageRef"`
		ConversationRef string            `json:"conversationRef"`
		State           coremessage.State `json:"state"`
		DeliveryReason  string            `json:"deliveryReason"`
		HandoffObserved bool              `json:"handoffObserved"`
		AcceptedAt      time.Time         `json:"acceptedAt"`
		TerminalAt      time.Time         `json:"terminalAt"`
		PayloadBytes    int               `json:"payloadBytes"`
		Source          route             `json:"source"`
		Target          route             `json:"target"`
	}{
		SchemaVersion: 1, EvictedAt: m.at.Add(48 * time.Hour), Reason: "retention", Adapter: "codex-inbox",
		MessageRef: m.ref, ConversationRef: "conversation-" + m.ref, State: coremessage.StateDelivered,
		DeliveryReason: "graph-test", AcceptedAt: m.at, TerminalAt: m.at.Add(time.Second),
		PayloadBytes: len(graphPayload(m.ref)),
		Source:       route{AgentUID: m.source, Provider: "claude"}, Target: route{AgentUID: m.target, Provider: "codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(line) + "\n"
}

func graphStorePath(stateDir string) string {
	return filepath.Join(stateDir, "agent-messages", "messages.json")
}

func writeGraphStore(t *testing.T, stateDir string, records ...messagestore.Record) {
	t.Helper()
	path := graphStorePath(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if records == nil {
		records = []messagestore.Record{}
	}
	data, err := json.Marshal(map[string]any{"version": 2, "records": records})
	if err != nil {
		t.Fatal(err)
	}
	// Written aside and renamed, as the store writer commits.
	tmp := path + ".test-tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func appendGraphHistory(t *testing.T, stateDir, name, content string) {
	t.Helper()
	path := filepath.Join(stateDir, "agent-messages", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- test-owned temporary path.
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func agentGraphBackend(t *testing.T, stateDir string) *webBackend {
	t.Helper()
	return agentGraphBackendWith(t, stateDir, nil)
}

// agentGraphBackendWith lets edit change the fixture Registry before it is
// installed.
func agentGraphBackendWith(t *testing.T, stateDir string, edit func(*coremetadata.Registry)) *webBackend {
	t.Helper()
	backend, _ := webFixtureBackend(t)
	registry := agentGraphRegistry(t)
	if edit != nil {
		edit(&registry)
	}
	backend.loadRegistry = func() (coremetadata.Registry, error) { return registry.Clone(), nil }
	backend.paths = func() (config.Paths, error) { return config.Paths{StateDir: stateDir}, nil }
	return backend
}

// The message fixture. Times are hours after agentGraphT0.
var (
	msgCodexToClaude   = graphMessage{"m-codex-claude", "agt-alpha-codex", "agt-alpha-claude", agentGraphT0.Add(3 * time.Hour)}
	msgClaudeToCodex   = graphMessage{"m-claude-codex", "agt-alpha-claude", "agt-alpha-codex", agentGraphT0.Add(4 * time.Hour)}
	msgClaudeToBeta    = graphMessage{"m-claude-beta", "agt-alpha-claude", "agt-beta-codex", agentGraphT0.Add(5 * time.Hour)}
	msgCodexToDeleted  = graphMessage{"m-codex-deleted", "agt-alpha-codex", "agt-deleted", agentGraphT0.Add(6 * time.Hour)}
	msgSelf            = graphMessage{"m-self", "agt-alpha-codex", "agt-alpha-codex", agentGraphT0.Add(-10 * time.Hour)}
	msgLoggedToClaude  = graphMessage{"m-logged-codex-claude", "agt-alpha-codex", "agt-alpha-claude", agentGraphT0.Add(time.Hour)}
	msgDeletedToCodex  = graphMessage{"m-deleted-codex", "agt-deleted", "agt-alpha-codex", agentGraphT0.Add(2 * time.Hour)}
	msgGhosts          = graphMessage{"m-ghosts", "agt-ghost-a", "agt-ghost-b", agentGraphT0.Add(30 * time.Minute)}
	msgBetaToClaude    = graphMessage{"m-beta-claude", "agt-beta-codex", "agt-alpha-claude", agentGraphT0}
	msgQuietToGone     = graphMessage{"m-quiet-gone", "agt-beta-quiet", "agt-gone-codex", agentGraphT0.Add(7 * time.Hour)}
	msgNoSourceToCodex = graphMessage{"m-no-source", "", "agt-alpha-codex", agentGraphT0.Add(-20 * time.Hour)}
)

// writeAgentGraphFixture fills the store and both log generations.
// msgClaudeToCodex is in the store and in the current log.
func writeAgentGraphFixture(t *testing.T, stateDir string) {
	t.Helper()
	writeGraphStore(t, stateDir,
		graphRecord(t, msgCodexToClaude), graphRecord(t, msgClaudeToCodex), graphRecord(t, msgClaudeToBeta),
		graphRecord(t, msgCodexToDeleted), graphRecord(t, msgSelf))
	appendGraphHistory(t, stateDir, "history.jsonl", graphHistoryLine(t, msgClaudeToCodex)+
		graphHistoryLine(t, msgLoggedToClaude)+graphHistoryLine(t, msgDeletedToCodex)+graphHistoryLine(t, msgGhosts))
	appendGraphHistory(t, stateDir, "history.jsonl.1", graphHistoryLine(t, msgBetaToClaude)+
		graphHistoryLine(t, msgQuietToGone)+graphHistoryLine(t, msgNoSourceToCodex))
}

func getAgentGraph(t *testing.T, backend *webBackend, project string) (int, map[string]any) {
	t.Helper()
	return webGet(t, web.New(backend, nil).Handler(), "/api/v1/projects/"+project+"/agent-graph")
}

func graphEdges(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["edges"].([]any)
	if !ok {
		t.Fatalf("edges is not an array: %v", body["edges"])
	}
	edges := make([]map[string]any, 0, len(raw))
	for _, edge := range raw {
		edges = append(edges, edge.(map[string]any))
	}
	return edges
}

func graphAgents(t *testing.T, body map[string]any) map[string]string {
	t.Helper()
	raw, ok := body["agents"].([]any)
	if !ok {
		t.Fatalf("agents is not an array: %v", body["agents"])
	}
	out := make(map[string]string, len(raw))
	var order []string
	for _, item := range raw {
		agent := item.(map[string]any)
		uid := agent["uid"].(string)
		out[uid] = agent["projectUID"].(string)
		order = append(order, uid)
	}
	if !slices.IsSorted(order) {
		t.Fatalf("agents are not sorted by uid: %v", order)
	}
	return out
}

func rfc3339(at time.Time) string { return at.Format(time.RFC3339Nano) }

// TestAgentGraphDrawsOneEdgePerPairAndCountsADuplicateOnce covers acceptance 1:
// the store and both log generations make one edge per pair with exact
// direction counts and last time, and a messageRef in both the store and the
// log counts once.
func TestAgentGraphDrawsOneEdgePerPairAndCountsADuplicateOnce(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentGraphFixture(t, stateDir)
	code, body := getAgentGraph(t, agentGraphBackend(t, stateDir), "prj-alpha")
	if code != http.StatusOK {
		t.Fatalf("GET agent-graph = %d %v", code, body)
	}
	if body["project"] != "prj-alpha" {
		t.Fatalf("project = %v", body["project"])
	}
	want := []map[string]any{
		{"kind": "conversation", "a": "agt-alpha-claude", "b": "agt-alpha-codex", "aToB": 1.0, "bToA": 2.0,
			"lastAcceptedAt": rfc3339(msgClaudeToCodex.at)},
		{"kind": "conversation", "a": "agt-alpha-claude", "b": "agt-beta-codex", "aToB": 1.0, "bToA": 1.0,
			"lastAcceptedAt": rfc3339(msgClaudeToBeta.at)},
	}
	got, _ := json.Marshal(graphEdges(t, body))
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(got, wantJSON) {
		t.Fatalf("edges = %s\nwant %s", got, wantJSON)
	}
}

// TestAgentGraphListsProjectAgentsAndOnlyTheirPartners covers acceptance 2.
func TestAgentGraphListsProjectAgentsAndOnlyTheirPartners(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentGraphFixture(t, stateDir)
	_, body := getAgentGraph(t, agentGraphBackend(t, stateDir), "prj-alpha")
	agents := graphAgents(t, body)
	want := map[string]string{
		"agt-alpha-claude": "prj-alpha",
		"agt-alpha-codex":  "prj-alpha",
		// Talks with nobody, and is still one of the Project's Agents.
		"agt-alpha-idle": "prj-alpha",
		// Another Project's partner carries its own Project.
		"agt-beta-codex": "prj-beta",
	}
	gotJSON, _ := json.Marshal(agents)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("agents = %s\nwant %s", gotJSON, wantJSON)
	}
	// agt-beta-quiet talked only to agt-gone-codex; that pair is in neither
	// graph of prj-alpha, but it is an edge of prj-beta.
	_, beta := getAgentGraph(t, agentGraphBackend(t, stateDir), "prj-beta")
	betaAgents := graphAgents(t, beta)
	if betaAgents["agt-gone-codex"] != "prj-gone" || betaAgents["agt-alpha-claude"] != "prj-alpha" ||
		betaAgents["agt-beta-quiet"] != "prj-beta" || len(betaAgents) != 4 {
		t.Fatalf("prj-beta agents = %v", betaAgents)
	}
}

// TestAgentGraphOmitsPairsWithAMissingAgentAndCountsSelfMessagesNowhere covers
// acceptance 3.
func TestAgentGraphOmitsPairsWithAMissingAgentAndCountsSelfMessagesNowhere(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentGraphFixture(t, stateDir)
	_, body := getAgentGraph(t, agentGraphBackend(t, stateDir), "prj-alpha")
	omitted, _ := body["omitted"].(map[string]any)
	// agt-alpha-codex <-> agt-deleted, both ways: one pair, two messages. The
	// pair of two missing Agents is counted nowhere.
	if omitted["pairs"] != 1.0 || omitted["messages"] != 2.0 {
		t.Fatalf("omitted = %v, want 1 pair and 2 messages", omitted)
	}
	for _, edge := range graphEdges(t, body) {
		if edge["a"] == edge["b"] || edge["a"] == "agt-deleted" || edge["b"] == "agt-deleted" {
			t.Fatalf("edge %v should not be drawn", edge)
		}
	}
	// With only the self message there is nothing to count at all.
	selfOnly := t.TempDir()
	writeGraphStore(t, selfOnly, graphRecord(t, msgSelf))
	_, body = getAgentGraph(t, agentGraphBackend(t, selfOnly), "prj-alpha")
	omitted, _ = body["omitted"].(map[string]any)
	if len(graphEdges(t, body)) != 0 || omitted["pairs"] != 0.0 || omitted["messages"] != 0.0 || body["since"] != nil {
		t.Fatalf("self message was counted: %v", body)
	}
}

// TestAgentGraphSinceIsTheOldestRetainedMessage covers acceptance 4: since is
// the oldest acceptedAt read, excluding self messages and messages without an
// Agent at one end, and is JSON null with an empty edge list when nothing is
// retained.
func TestAgentGraphSinceIsTheOldestRetainedMessage(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentGraphFixture(t, stateDir)
	_, body := getAgentGraph(t, agentGraphBackend(t, stateDir), "prj-alpha")
	if body["since"] != rfc3339(msgBetaToClaude.at) {
		t.Fatalf("since = %v, want %s", body["since"], rfc3339(msgBetaToClaude.at))
	}

	empty := t.TempDir()
	rec := webGetRaw(t, agentGraphBackend(t, empty), "/api/v1/projects/prj-alpha/agent-graph")
	for _, fragment := range []string{`"since":null`, `"edges":[]`, `"skipped":0`, `"omitted":{"pairs":0,"messages":0,"created":0}`} {
		if !strings.Contains(rec, fragment) {
			t.Fatalf("empty archive body %s lacks %s", rec, fragment)
		}
	}
}

// webGetRaw is the response body as the server wrote it, so a test can tell
// null from [] and see the key order.
func webGetRaw(t *testing.T, backend *webBackend, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	web.New(backend, nil).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// TestAgentGraphCountsARecordReclaimedDuringTheReadOnce covers acceptance 5 at
// the route: the archive read sees the store before a reclaim and the log after
// it, which is what a reclaim landing between the two reads gives (the
// agentmessage tests run that interleaving with the real writer). The record
// is then in both, and the edge counts it once.
func TestAgentGraphCountsARecordReclaimedDuringTheReadOnce(t *testing.T) {
	stateDir := t.TempDir()
	writeGraphStore(t, stateDir, graphRecord(t, msgCodexToClaude), graphRecord(t, msgClaudeToCodex))
	backend := agentGraphBackend(t, stateDir)
	backend.readMessages = func(dir string) (messagestore.Archive, error) {
		before, err := messagestore.ReadArchive(dir)
		if err != nil {
			return messagestore.Archive{}, err
		}
		// The writer's order: the log line is durable, then the store is
		// replaced without the record.
		appendGraphHistory(t, dir, "history.jsonl", graphHistoryLine(t, msgCodexToClaude))
		writeGraphStore(t, dir, graphRecord(t, msgClaudeToCodex))
		after, err := messagestore.ReadArchive(dir)
		if err != nil {
			return messagestore.Archive{}, err
		}
		return messagestore.Archive{Records: before.Records, History: after.History, Skipped: after.Skipped}, nil
	}
	_, body := getAgentGraph(t, backend, "prj-alpha")
	edges := graphEdges(t, body)
	if len(edges) != 1 || edges[0]["aToB"] != 1.0 || edges[0]["bToA"] != 1.0 {
		t.Fatalf("edges = %v, want the reclaimed message counted once", edges)
	}
	// A later read, with the record only in the log, counts it the same.
	backend.readMessages = nil
	_, body = getAgentGraph(t, backend, "prj-alpha")
	if edges := graphEdges(t, body); len(edges) != 1 || edges[0]["bToA"] != 1.0 {
		t.Fatalf("edges after the reclaim = %v", edges)
	}
}

// TestAgentGraphReadsCreateNothingInTheStateDir covers acceptance 6.
func TestAgentGraphReadsCreateNothingInTheStateDir(t *testing.T) {
	empty := t.TempDir()
	backend := agentGraphBackend(t, empty)
	if _, err := backend.AgentGraph(t.Context(), "prj-alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.PeerMessages(t.Context(), "agt-alpha-codex", "agt-alpha-claude"); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(empty); err != nil || len(entries) != 0 {
		t.Fatalf("reads created %v (%v) in an empty state dir", entries, err)
	}

	stateDir := t.TempDir()
	writeAgentGraphFixture(t, stateDir)
	path := graphStorePath(stateDir)
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	backend = agentGraphBackend(t, stateDir)
	if _, err := backend.AgentGraph(t.Context(), "prj-alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.PeerMessages(t.Context(), "agt-alpha-codex", "agt-alpha-claude"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("store bytes changed (%v)", err)
	}
	if info, err := os.Stat(path); err != nil || !info.ModTime().Equal(old) {
		t.Fatalf("store mtime = (%v, %v), want %v", info.ModTime(), err, old)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if strings.Join(names, ",") != "history.jsonl,history.jsonl.1,messages.json" {
		t.Fatalf("store dir after reads = %v, want no lock or temporary file", names)
	}
}

// TestPeerMessagesListsBothDirectionsWithBodiesOnlyFromTheStore covers
// acceptance 7.
func TestPeerMessagesListsBothDirectionsWithBodiesOnlyFromTheStore(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentGraphFixture(t, stateDir)
	code, body := webGet(t, web.New(agentGraphBackend(t, stateDir), nil).Handler(),
		"/api/v1/agents/agt-alpha-codex/peers/agt-alpha-claude/messages")
	if code != http.StatusOK {
		t.Fatalf("GET peer messages = %d %v", code, body)
	}
	if body["agent"] != "agt-alpha-codex" || body["peer"] != "agt-alpha-claude" ||
		body["since"] != rfc3339(msgBetaToClaude.at) || body["skipped"] != 0.0 {
		t.Fatalf("envelope = %v", body)
	}
	raw, _ := body["messages"].([]any)
	type item struct {
		ref, direction string
		retained       bool
		payload        any
	}
	var got []item
	for _, entry := range raw {
		message := entry.(map[string]any)
		payload, present := message["payload"]
		if !present {
			payload = "<absent>"
		}
		got = append(got, item{message["messageRef"].(string), message["direction"].(string), message["bodyRetained"].(bool), payload})
		if message["payloadBytes"] != float64(len(graphPayload(message["messageRef"].(string)))) {
			t.Errorf("%v payloadBytes = %v", message["messageRef"], message["payloadBytes"])
		}
	}
	want := []item{
		{msgLoggedToClaude.ref, "outgoing", false, "<absent>"},
		{msgCodexToClaude.ref, "outgoing", true, graphPayload(msgCodexToClaude.ref)},
		// In the store and in the log: the store copy, with its body.
		{msgClaudeToCodex.ref, "incoming", true, graphPayload(msgClaudeToCodex.ref)},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("messages = %+v\nwant %+v", got, want)
	}

	empty := webGetRaw(t, agentGraphBackend(t, t.TempDir()), "/api/v1/agents/agt-alpha-codex/peers/agt-alpha-claude/messages")
	if !strings.Contains(empty, `"messages":[]`) || !strings.Contains(empty, `"since":null`) {
		t.Fatalf("empty peer messages = %s", empty)
	}
}

// TestPeerMessagesRefusesUnknownAgentsAndASelfPair covers acceptance 7's
// refusals.
func TestPeerMessagesRefusesUnknownAgentsAndASelfPair(t *testing.T) {
	stateDir := t.TempDir()
	writeAgentGraphFixture(t, stateDir)
	handler := web.New(agentGraphBackend(t, stateDir), nil).Handler()
	for path, wantCode := range map[string]string{
		"/api/v1/agents/agt-deleted/peers/agt-alpha-codex/messages":     web.CodeNotFound,
		"/api/v1/agents/agt-alpha-codex/peers/agt-deleted/messages":     web.CodeNotFound,
		"/api/v1/agents/agt-alpha-codex/peers/agt-alpha-codex/messages": web.CodeInvalidRequest,
		"/api/v1/projects/prj-missing/agent-graph":                      web.CodeNotFound,
	} {
		_, body := webGet(t, handler, path)
		envelope, _ := body["error"].(map[string]any)
		if envelope == nil || envelope["code"] != wantCode {
			t.Errorf("GET %s = %v, want %s", path, body, wantCode)
		}
	}
}

// TestAgentGraphRefusesAMalformedStoreAndSkipsATornLogLine covers acceptance 8.
func TestAgentGraphRefusesAMalformedStoreAndSkipsATornLogLine(t *testing.T) {
	malformed := t.TempDir()
	writeGraphStore(t, malformed)
	if err := os.WriteFile(graphStorePath(malformed), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := web.New(agentGraphBackend(t, malformed), nil).Handler()
	for _, path := range []string{
		"/api/v1/projects/prj-alpha/agent-graph",
		"/api/v1/agents/agt-alpha-codex/peers/agt-alpha-claude/messages",
	} {
		code, body := webGet(t, handler, path)
		envelope, _ := body["error"].(map[string]any)
		if code != http.StatusInternalServerError || envelope == nil || envelope["code"] != web.CodeInternal {
			t.Errorf("GET %s over a malformed store = %d %v, want an error envelope", path, code, body)
		}
	}

	torn := t.TempDir()
	line := graphHistoryLine(t, msgLoggedToClaude)
	appendGraphHistory(t, torn, "history.jsonl", graphHistoryLine(t, msgClaudeToCodex)+line[:len(line)/2])
	_, body := getAgentGraph(t, agentGraphBackend(t, torn), "prj-alpha")
	edges := graphEdges(t, body)
	if body["skipped"] != 1.0 || len(edges) != 1 || edges[0]["aToB"] != 1.0 || edges[0]["bToA"] != 0.0 {
		t.Fatalf("torn log body = %v, want the whole line read and skipped 1", body)
	}
}

// annotateCreator sets child's creator annotation to creator, which may be
// empty.
func annotateCreator(t *testing.T, registry *coremetadata.Registry, child, creator string) {
	t.Helper()
	for i := range registry.Agents {
		if registry.Agents[i].Metadata.UID == child {
			registry.Agents[i].Metadata.Annotations = map[string]string{coremetadata.AnnotationCreatorAgent: creator}
			return
		}
	}
	t.Fatalf("no agent %s in the fixture", child)
}

// TestAgentGraphDrawsACreatedEdgeFromEachAnnotatedAgentsCreator covers Task 4
// acceptance 1: a created edge runs from the annotated creator to the Agent it
// created when either end is in the Project, carries no counts, pulls an
// outside end into agents, and does not depend on the message archive.
func TestAgentGraphDrawsACreatedEdgeFromEachAnnotatedAgentsCreator(t *testing.T) {
	edit := func(registry *coremetadata.Registry) {
		// Same Project.
		annotateCreator(t, registry, "agt-alpha-idle", "agt-alpha-claude")
		// A creator in another Project.
		annotateCreator(t, registry, "agt-alpha-codex", "agt-beta-quiet")
		// Created in another Project by one of this Project's Agents.
		annotateCreator(t, registry, "agt-gone-codex", "agt-alpha-claude")
		// The same pair as a conversation edge.
		annotateCreator(t, registry, "agt-beta-codex", "agt-alpha-claude")
	}
	stateDir := t.TempDir()
	writeAgentGraphFixture(t, stateDir)
	code, body := getAgentGraph(t, agentGraphBackendWith(t, stateDir, edit), "prj-alpha")
	if code != http.StatusOK {
		t.Fatalf("GET agent-graph = %d %v", code, body)
	}
	created := []map[string]any{
		{"kind": "created", "a": "agt-alpha-claude", "b": "agt-alpha-idle"},
		{"kind": "created", "a": "agt-alpha-claude", "b": "agt-beta-codex"},
		{"kind": "created", "a": "agt-alpha-claude", "b": "agt-gone-codex"},
		{"kind": "created", "a": "agt-beta-quiet", "b": "agt-alpha-codex"},
	}
	want := []map[string]any{
		{"kind": "conversation", "a": "agt-alpha-claude", "b": "agt-alpha-codex", "aToB": 1.0, "bToA": 2.0,
			"lastAcceptedAt": rfc3339(msgClaudeToCodex.at)},
		created[0],
		{"kind": "conversation", "a": "agt-alpha-claude", "b": "agt-beta-codex", "aToB": 1.0, "bToA": 1.0,
			"lastAcceptedAt": rfc3339(msgClaudeToBeta.at)},
		created[1], created[2], created[3],
	}
	got, _ := json.Marshal(graphEdges(t, body))
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(got, wantJSON) {
		t.Fatalf("edges = %s\nwant %s", got, wantJSON)
	}
	agents, _ := json.Marshal(graphAgents(t, body))
	wantAgents, _ := json.Marshal(map[string]string{
		"agt-alpha-claude": "prj-alpha", "agt-alpha-codex": "prj-alpha", "agt-alpha-idle": "prj-alpha",
		"agt-beta-codex": "prj-beta", "agt-beta-quiet": "prj-beta", "agt-gone-codex": "prj-gone",
	})
	if !bytes.Equal(agents, wantAgents) {
		t.Fatalf("agents = %s\nwant %s", agents, wantAgents)
	}
	// As written: a conversation edge keeps its keys and their order, and a
	// created edge has no counts.
	raw := webGetRaw(t, agentGraphBackendWith(t, stateDir, edit), "/api/v1/projects/prj-alpha/agent-graph")
	for _, fragment := range []string{
		`{"kind":"conversation","a":"agt-alpha-claude","b":"agt-alpha-codex","aToB":1,"bToA":2,"lastAcceptedAt":"` +
			rfc3339(msgClaudeToCodex.at) + `"}`,
		`{"kind":"created","a":"agt-alpha-claude","b":"agt-alpha-idle"}`,
	} {
		if !strings.Contains(raw, fragment) {
			t.Fatalf("body %s lacks %s", raw, fragment)
		}
	}

	// With nothing retained the created edges are all there is.
	empty := t.TempDir()
	_, body = getAgentGraph(t, agentGraphBackendWith(t, empty, edit), "prj-alpha")
	got, _ = json.Marshal(graphEdges(t, body))
	wantJSON, _ = json.Marshal(created)
	if !bytes.Equal(got, wantJSON) || body["since"] != nil {
		t.Fatalf("empty archive edges = %s since = %v\nwant %s and null", got, body["since"], wantJSON)
	}
}

// TestAgentGraphOmitsACreatedEdgeWhoseCreatorIsGone covers Task 4 acceptance
// 2: a creator missing from the Registry is no edge and is counted for the
// created Agent's Project only; an empty annotation, no annotation, and an
// annotation naming the Agent itself make no edge and are counted nowhere.
func TestAgentGraphOmitsACreatedEdgeWhoseCreatorIsGone(t *testing.T) {
	edit := func(registry *coremetadata.Registry) {
		annotateCreator(t, registry, "agt-alpha-codex", "agt-deleted-creator")
		annotateCreator(t, registry, "agt-alpha-idle", "")
		annotateCreator(t, registry, "agt-alpha-claude", "agt-alpha-claude")
		annotateCreator(t, registry, "agt-beta-quiet", "agt-deleted-creator")
		// agt-beta-codex has no annotation.
	}
	stateDir := t.TempDir()
	writeAgentGraphFixture(t, stateDir)
	for project, want := range map[string]map[string]any{
		// The pairs and messages are the conversation fixture's own.
		"prj-alpha": {"pairs": 1.0, "messages": 2.0, "created": 1.0},
		"prj-beta":  {"pairs": 0.0, "messages": 0.0, "created": 1.0},
	} {
		_, body := getAgentGraph(t, agentGraphBackendWith(t, stateDir, edit), project)
		got, _ := json.Marshal(body["omitted"])
		wantJSON, _ := json.Marshal(want)
		if !bytes.Equal(got, wantJSON) {
			t.Errorf("%s omitted = %s, want %s", project, got, wantJSON)
		}
		for _, edge := range graphEdges(t, body) {
			if edge["kind"] != "conversation" {
				t.Errorf("%s edge %v should not be drawn", project, edge)
			}
		}
		if _, listed := graphAgents(t, body)["agt-deleted-creator"]; listed {
			t.Errorf("%s lists the missing creator", project)
		}
	}
}

// TestAgentGraphReadsBothHistoryLineShapes pins that a log holding full-route
// lines written before routes were narrowed and narrow lines the writer writes
// now counts both toward one pair: its edge counts, lastAcceptedAt and since
// come from either shape, a narrow line repeating a stored message counts
// once, and the peers route lists a narrow line without its body.
func TestAgentGraphReadsBothHistoryLineShapes(t *testing.T) {
	stateDir := t.TempDir()
	narrowNewest := graphMessage{"m-narrow-claude-codex", "agt-alpha-claude", "agt-alpha-codex", agentGraphT0.Add(8 * time.Hour)}
	narrowOldest := graphMessage{"m-narrow-oldest", "agt-alpha-claude", "agt-alpha-codex", agentGraphT0.Add(-2 * time.Hour)}
	for _, line := range []string{graphNarrowHistoryLine(t, narrowNewest), graphNarrowHistoryLine(t, narrowOldest)} {
		for _, key := range []string{`"deadline"`, `"paneUID"`, `"activationGeneration"`, `"incarnation"`, `"outcomeUnknown"`} {
			if strings.Contains(line, key) {
				t.Fatalf("narrow line carries %s: %s", key, line)
			}
		}
	}
	if old := graphHistoryLine(t, msgLoggedToClaude); !strings.Contains(old, `"deadline"`) || !strings.Contains(old, `"paneUID"`) {
		t.Fatalf("full-route line lost its route or deadline: %s", old)
	}
	writeGraphStore(t, stateDir, graphRecord(t, msgCodexToClaude))
	appendGraphHistory(t, stateDir, "history.jsonl", graphHistoryLine(t, msgLoggedToClaude)+
		graphNarrowHistoryLine(t, narrowNewest)+graphNarrowHistoryLine(t, msgCodexToClaude))
	appendGraphHistory(t, stateDir, "history.jsonl.1", graphNarrowHistoryLine(t, narrowOldest))

	code, body := getAgentGraph(t, agentGraphBackend(t, stateDir), "prj-alpha")
	if code != http.StatusOK {
		t.Fatalf("GET agent-graph = %d %v", code, body)
	}
	// claude -> codex: the two narrow lines. codex -> claude: the store and the
	// full-route line; the narrow copy of the stored message counts once.
	want := []map[string]any{
		{"kind": "conversation", "a": "agt-alpha-claude", "b": "agt-alpha-codex", "aToB": 2.0, "bToA": 2.0,
			"lastAcceptedAt": rfc3339(narrowNewest.at)},
	}
	got, _ := json.Marshal(graphEdges(t, body))
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(got, wantJSON) {
		t.Fatalf("edges = %s\nwant %s", got, wantJSON)
	}
	if body["since"] != rfc3339(narrowOldest.at) || body["skipped"] != 0.0 {
		t.Fatalf("since = %v skipped = %v, want %s and 0", body["since"], body["skipped"], rfc3339(narrowOldest.at))
	}

	code, body = webGet(t, web.New(agentGraphBackend(t, stateDir), nil).Handler(),
		"/api/v1/agents/agt-alpha-codex/peers/agt-alpha-claude/messages")
	if code != http.StatusOK {
		t.Fatalf("GET peer messages = %d %v", code, body)
	}
	raw, _ := body["messages"].([]any)
	type item struct {
		ref, direction, source, target, state, acceptedAt string
		retained, payloadPresent                          bool
		payloadBytes                                      float64
	}
	var items []item
	for _, entry := range raw {
		message := entry.(map[string]any)
		_, present := message["payload"]
		items = append(items, item{message["messageRef"].(string), message["direction"].(string),
			message["source"].(string), message["target"].(string), message["state"].(string),
			message["acceptedAt"].(string), message["bodyRetained"].(bool), present, message["payloadBytes"].(float64)})
	}
	wantItem := func(m graphMessage, direction string, retained bool) item {
		return item{m.ref, direction, m.source, m.target, string(coremessage.StateDelivered), rfc3339(m.at),
			retained, retained, float64(len(graphPayload(m.ref)))}
	}
	wantItems := []item{
		wantItem(narrowOldest, "incoming", false),
		wantItem(msgLoggedToClaude, "outgoing", false),
		wantItem(msgCodexToClaude, "outgoing", true),
		wantItem(narrowNewest, "incoming", false),
	}
	if !slices.Equal(items, wantItems) {
		t.Fatalf("messages = %+v\nwant %+v", items, wantItems)
	}
}
