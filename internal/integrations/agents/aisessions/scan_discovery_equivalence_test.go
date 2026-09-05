package aisessions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These fixed discovery rows were captured and passed on main fc287c26 before
// the Phase 2 parser change. They pin the full exported row contract, including
// order, title provenance, cwd/branch, identity and source, for every provider.
// Antigravity is intentionally a control lane: its current storage/history
// adapters do not call scanSessionJSONLReaderContext at this baseline.
func TestSessionScanThreeProviderDiscoveryEquivalence(t *testing.T) {
	const cwd = "/fixture/project"
	root := t.TempDir()
	opts := DiscoverOptions{HomeDir: root, Depth: 1, DeferTurns: true,
		ClaudeProjectsDir: filepath.Join(root, "claude"), CodexSessionsDir: filepath.Join(root, "codex"),
		AntigravityCacheDir: filepath.Join(root, "cache"), AntigravityConversationsDir: filepath.Join(root, "db"),
		AntigravityHistoryPath: filepath.Join(root, "history.jsonl")}
	id := func(n int) string { return fmt.Sprintf("019f0000-0000-7000-8000-%012d", n) }
	at := func(n int) time.Time { return time.Unix(int64(1800000000+n), 0).UTC() }
	write := func(path, body string, second int) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at(second), at(second)); err != nil {
			t.Fatal(err)
		}
	}
	transcript := func(provider, name, body string, second int) {
		dir := opts.CodexSessionsDir
		if provider == AgentClaude {
			dir = filepath.Join(opts.ClaudeProjectsDir, EncodeClaudeProjectPath(cwd))
		}
		write(filepath.Join(dir, name+".jsonl"), body, second)
	}
	transcript(AgentClaude, "a-file-id-differs", fmt.Sprintf(`{"sessionId":%q,"session_id":%q,"id":"ignored","cwd":%q,"current_dir":"/wrong","gitBranch":" feat/one ","branch":"ignored","nested":{"id":"ignored","cwd":"/wrong"}}
{"type":"user","message":{"content":[" Legacy ",{"type":"text","text":"prompt"}]}}
{"type":"ai-title","sessionId":"foreign","aiTitle":"Foreign title"}
{"type":"ai-title","sessionId":%q,"aiTitle":"  Canonical   Claude "}
{"type":"ai-title","aiTitle":"Later title"}`, id(2), id(99), cwd, id(2)), 30)
	transcript(AgentClaude, "b-arbitrary-nested", fmt.Sprintf(`not-json
{"sessionId":7,"cwd":false,"gitBranch":[],"arbitrary":{"deeper":{"session_id":%q,"workingDirectory":"/fixture/project/child","git_branch":"nested"}}}
{"type":"ai-title","aiTitle":42}
{"type":"ai-title","aiTitle":"Nested Claude"}`, id(1)), 30)
	transcript(AgentClaude, id(3), `{"cwd":"/fixture/project","branch":"fallback","type":"user","content":["Fallback",{"text":"prompt"}]}`, 20)
	transcript(AgentClaude, "d-noise", fmt.Sprintf(`{"id":%q,"projectPath":%q,"branch":"noise"}
{"type":"user","content":"<system-reminder>skip this"}`, id(4), cwd), 10)
	transcript(AgentClaude, "z-duplicate", fmt.Sprintf(`{"id":%q,"cwd":%q,"branch":"stale","type":"ai-title","aiTitle":"Stale"}`, id(2), cwd), 1)
	transcript(AgentClaude, "excluded", fmt.Sprintf(`{"id":%q,"cwd":"/elsewhere","branch":"x","type":"ai-title","aiTitle":"Excluded"}`, id(9)), 40)
	transcript(AgentCodex, "rollout-event", fmt.Sprintf(`{"type":"session_meta","payload":{"id":%q,"cwd":%q,"git":{"branch":"codex-event"}}}
{"type":"event_msg","payload":{"message":"Legacy title"}}
{"type":"event_msg","payload":{"type":"agent_message","message":"Assistant"}}
{"type":"ai-title","aiTitle":"Wrong provider title"}
{"type":"event_msg","payload":{"type":"user_message","message":"First Codex prompt"}}
{"type":"event_msg","payload":{"type":"user_message","message":"Later prompt"}}`, id(12), cwd), 30)
	transcript(AgentCodex, "rollout-response", fmt.Sprintf(`{"sessionId":false,"session_id":%q,"id":"ignored","currentDir":"/fixture/project/child","gitBranch":99,"git_branch":"codex-response"}
{"type":"response_item","payload":{"role":"user","content":[{"type":"tool_result","text":"Ignored tool"}]}}
{"type":"response_item","payload":{"role":"user","content":[{"type":"tool_use","text":"Ignored tool"},{"type":"input_text","text":"Response"},"prompt",{"type":"text","text":"blocks"}]}}`, id(11)), 30)
	transcript(AgentCodex, "rollout-nested", fmt.Sprintf(`{"unrecognized":{"metadata":{"id":%q,"working_directory":%q,"branch":"nested"}}}
{"type":"event_msg","payload":{"type":"user_message","message":{"deep":{"message":"Nested message"}}}}`, id(13), cwd), 20)
	transcript(AgentCodex, "rollout-fallback", fmt.Sprintf(`{"id":%q,"cwd":%q,"branch":"fallback"}
{"type":"user","content":"Legacy fallback"}
{"TYPE":"event_msg","payload":{"type":"user_message","message":"Wrong case ignored"}}`, id(14), cwd), 10)
	transcript(AgentCodex, "rollout-duplicate", fmt.Sprintf(`{"id":%q,"cwd":%q,"branch":"stale","type":"event_msg","payload":{"type":"user_message","message":"Stale"}}`, id(12), cwd), 1)
	transcript(AgentCodex, "rollout-excluded", fmt.Sprintf(`{"id":%q,"cwd":"/elsewhere","branch":"x"}`, id(19)), 40)
	// Current cache provenance beats a newer duplicate history record; tie order
	// and child-workspace filtering are checked in this third discovery lane too.
	write(filepath.Join(opts.AntigravityConversationsDir, id(22)+".db"), "opaque fixture", 30)
	write(filepath.Join(opts.AntigravityConversationsDir, id(21)+".db"), "opaque fixture", 30)
	write(filepath.Join(opts.AntigravityCacheDir, "last_conversations.json"), fmt.Sprintf(`{%q:%q}`, cwd, id(22)), 0)
	write(filepath.Join(opts.AntigravityCacheDir, "conversation_metadata.json"), fmt.Sprintf(`{"conversations":{%q:{"workspace":"file:///fixture/project/child","summary":" Metadata   title "}}}`, id(21)), 0)
	write(opts.AntigravityHistoryPath, fmt.Sprintf(`{"conversationId":%q,"workspace":%q,"timestamp":1800000040000,"display":"Losing history duplicate"}
{"conversationId":%q,"workspace":%q,"timestamp":1800000020000,"display":"History title"}
{"conversationId":%q,"workspace":%q,"timestamp":1800000010000,"display":"<command-name>/goal"}
{"conversationId":%q,"workspace":"/elsewhere","timestamp":1800000050000,"display":"Excluded"}
malformed`, id(22), cwd, id(23), cwd, id(24), cwd, id(29)), 0)
	for _, provider := range []string{AgentClaude, AgentCodex, AgentAntigravity} {
		t.Run(provider, func(t *testing.T) {
			discovery, err := DiscoverProviderContext(context.Background(), provider, cwd, opts, 30)
			if err != nil {
				t.Fatal(err)
			}
			// sourcePath is a private temp fixture location, not an exported row field.
			for i := range discovery.Sessions {
				discovery.Sessions[i].sourcePath = ""
			}
			path := filepath.Join("testdata", "scan-discovery-"+provider+".json")
			data, err := json.MarshalIndent(discovery.Sessions, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			baseline, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var want []SessionMeta
			if err := json.Unmarshal(baseline, &want); err != nil {
				t.Fatal(err)
			}
			// Filesystem mtimes use the host's local zone, while the unchanged
			// baseline records the capture machine's offset. Compare exact
			// instants in UTC so host timezone metadata cannot break row parity.
			for _, rows := range [][]SessionMeta{discovery.Sessions, want} {
				for i := range rows {
					rows[i].LastModified = rows[i].LastModified.UTC()
					rows[i].UpdatedAt = rows[i].UpdatedAt.UTC()
				}
			}
			if len(want) != 4 || !reflect.DeepEqual(discovery.Sessions, want) {
				t.Fatalf("discovery changed from fc287c26 fixture:\n%s\nwant:\n%s", data, baseline)
			}
			t.Logf("baseline=fc287c26 provider=%s complete_rows=%d ids=%s", provider, len(want), strings.Join([]string{want[0].ResumeID, want[1].ResumeID, want[2].ResumeID, want[3].ResumeID}, ","))
		})
	}
}
