package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/web"
)

// webClientFixture gives the fixture's codex agent a Claude identity with a
// transcript on disk, so the client reads have something real to parse.
func webClientFixture(t *testing.T, transcriptLines ...string) (*webBackend, string) {
	t.Helper()
	backend, _ := webFixtureBackend(t)
	registry := resourceFixtureRegistry(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(transcriptLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent, _ := registry.Agent("agt-alpha-codex")
	agent.Spec.Provider = "claude"
	agent.Status.SessionRef = &coremetadata.AgentSessionRef{
		Provider: "claude",
		Claude:   &coremetadata.ClaudeSessionRef{SessionID: "s-1", TranscriptPath: path},
	}
	backend.loadRegistry = func() (coremetadata.Registry, error) { return registry.Clone(), nil }
	return backend, path
}

func TestWebMessagesServeOnlyTheWebCatalog(t *testing.T) {
	t.Setenv("PROJMUX_LOCALE", "ko-KR")
	backend, _ := webFixtureBackend(t)
	code, body := webGet(t, web.New(backend, nil).Handler(), "/api/v1/web/i18n")
	if code != 200 || body["locale"] != "ko-KR" {
		t.Fatalf("i18n = %d %v", code, body["locale"])
	}
	messages, _ := body["messages"].(map[string]any)
	if len(messages) == 0 {
		t.Fatal("no web messages")
	}
	for key := range messages {
		if !strings.HasPrefix(key, "web.") {
			t.Errorf("non-web key %q served", key)
		}
	}
}

func TestWebTranscriptCarriesSurfaceAndTurns(t *testing.T) {
	backend, _ := webClientFixture(t,
		`{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"hi there"}]}}`,
	)
	handler := web.New(backend, nil).Handler()
	code, body := webGet(t, handler, "/api/v1/web/agents/agt-alpha-codex/transcript")
	if code != 200 {
		t.Fatalf("transcript = %d %v", code, body)
	}
	surface := body["surface"].(map[string]any)
	if surface["mode"] != "message" || surface["sourceRequired"] != true || surface["maxBytes"] != float64(webClaudeMessageBytes) {
		t.Errorf("surface = %v", surface)
	}
	read := body["transcript"].(map[string]any)
	turns, _ := read["turns"].([]any)
	if len(turns) != 2 || turns[1].(map[string]any)["text"] != "hi there" {
		t.Errorf("turns = %v", read["turns"])
	}
	if _, leaked := read["path"]; leaked {
		t.Errorf("the transcript path is served: %v", read["path"])
	}

	// An agent with no transcript is a note, not a failure.
	code, body = webGet(t, handler, "/api/v1/web/agents/agt-beta-codex/transcript")
	read, _ = body["transcript"].(map[string]any)
	if code != 200 || read["note"] != "no-transcript" || body["surface"].(map[string]any)["mode"] != "turn" {
		t.Errorf("no transcript = %d %v", code, body)
	}
	if code, _ := webGet(t, handler, "/api/v1/web/agents/agt-missing/transcript"); code != 404 {
		t.Errorf("missing agent = %d", code)
	}
	if code, _ := webGet(t, handler, "/api/v1/web/agents/agt-alpha-codex/transcript?limit=0"); code != 400 {
		t.Errorf("limit 0 = %d", code)
	}
}

func TestWebFollowTranscriptReturnsAppendedTurns(t *testing.T) {
	backend, path := webClientFixture(t,
		`{"type":"user","message":{"role":"user","content":"old"}}`,
	)
	follower, err := backend.FollowTranscript(t.Context(), "agt-alpha-codex", -1)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(`{"type":"user","message":{"role":"user","content":"new"}}` + "\n")
	_ = file.Close()
	items, err := follower.Next()
	if err != nil || len(items) != 1 {
		t.Fatalf("Next = %v %v, want only the appended turn", items, err)
	}
	if _, err := backend.FollowTranscript(t.Context(), "agt-beta-codex", -1); err == nil {
		t.Error("following an agent with no transcript succeeded")
	}
}

func TestWebResumeCandidatesListOfflineAgents(t *testing.T) {
	backend, _ := webFixtureBackend(t)
	handler := web.New(backend, nil).Handler()
	code, body := webGet(t, handler, "/api/v1/web/windows/win-beta-main/resume-candidates")
	items, _ := body["items"].([]any)
	if code != 200 || len(items) != 1 || items[0].(map[string]any)["uid"] != "agt-beta-codex" {
		t.Fatalf("candidates = %d %v", code, body)
	}
	if note := items[0].(map[string]any)["note"]; note == nil || note == "" {
		t.Errorf("a candidate with no transcript has no note: %v", items[0])
	}
	if code, _ := webGet(t, handler, "/api/v1/web/windows/win-missing/resume-candidates"); code != 404 {
		t.Errorf("missing window = %d", code)
	}
}

// The preview is the argv the create route runs, not a second rendering.
func TestWebPreviewAgentIsTheCreateArgv(t *testing.T) {
	handler, recorder := webMutationHarness(t)
	recorder.reply = func([]string) (string, error) {
		return `{"items":[{"metadata":{"uid":"agt-alpha-codex"}}]}`, nil
	}
	body := `{"provider":"claude","anchorPane":"pan-alpha-log","payload":"do it","model":"sonnet","effort":"low"}`
	code, preview := webSend(t, handler, "POST", "/api/v1/web/projects/prj-alpha/windows/win-alpha-main/agents/preview", body)
	if code != 200 {
		t.Fatalf("preview = %d %v", code, preview)
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("the preview ran something: %v", recorder.calls)
	}
	var shown []string
	for _, part := range preview["argv"].([]any) {
		shown = append(shown, part.(string))
	}
	if code, _ := webSend(t, handler, "POST", webWindowAlpha+"/agents", strings.TrimSuffix(body, "}")+`,"confirm":true}`); code != 201 {
		t.Fatalf("create = %d", code)
	}
	if !strings.Contains(strings.Join(shown, " "), "--model sonnet --effort low --") {
		t.Fatalf("preview %q does not carry the model and effort", shown)
	}
	if got := "projmux " + recorder.calls[0]; strings.Join(shown, " ") != got {
		t.Fatalf("preview %q\nran     %q", strings.Join(shown, " "), got)
	}
	code, refused := webSend(t, handler, "POST", "/api/v1/web/projects/prj-alpha/windows/win-alpha-main/agents/preview", `{"provider":"shell"}`)
	if code != 400 || errorCode(refused) != web.CodeInvalidRequest {
		t.Fatalf("shell preview = %d %v", code, refused)
	}
}

func TestWebCapturesRefuseAWindowOrPaneThatIsNotLive(t *testing.T) {
	backend, _ := webFixtureBackend(t)
	handler := web.New(backend, nil).Handler()
	for path, want := range map[string]string{
		"/api/v1/web/windows/win-alpha-main/layout": web.CodeNotLive,
		"/api/v1/web/panes/pan-alpha-zsh/screen":    web.CodeNotLive,
		"/api/v1/web/windows/win-missing/layout":    web.CodeNotFound,
		"/api/v1/web/panes/pan-missing/screen":      web.CodeNotFound,
	} {
		_, body := webGet(t, handler, path)
		if errorCode(body) != want {
			t.Errorf("GET %s = %v, want %s", path, body, want)
		}
	}
}
