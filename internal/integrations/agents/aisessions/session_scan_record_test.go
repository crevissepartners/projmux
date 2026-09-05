package aisessions

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The unmodified legacy field/title helpers are an independent reference for
// the sparse decoder's tolerant JSON semantics. End-to-end discovery and row
// order are pinned separately by the three pre-change provider goldens.
func TestSessionScanRecordPreservesLegacyFields(t *testing.T) {
	records := []string{
		`null`, `[]`, `{"id":"valid"} trailing`, `{"id":false,"sessionId":7,"session_id":"chosen","cwd":42,"current_dir":"/work","gitBranch":[],"git_branch":"main"}`,
		`{"arbitrary":{"deep":{"sessionId":"chosen","cwd":"/work","branch":"main"}}}`,
		`{"array":[{"id":"ignored","cwd":"ignored","branch":"ignored"}],"nested":{"id":"chosen","cwd":"/work","branch":"main"}}`,
		`{"id":"top","session_id":"preferred","cwd":"/work","branch":"main","nested":{"sessionId":"ignored","cwd":"ignored","branch":"ignored"}}`,
		`{"id":"first","id":"last","cwd":"/work","type":"user","content":"Correct case","TYPE":"assistant"}`,
		`{"ID":"ignored","CWD":"ignored","TYPE":"user","CONTENT":"ignored"}`,
		`{"nested":{"\u0069d":"escaped","c\u0077d":"/work","git\u0042ranch":"escaped"}}`,
		`{"id":"chosen","cwd":"/work","type":"user","content":["string",{"text":"block"},42,null]}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","text":"legacy tool text"}]}}`,
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"tool_result","text":"tool"},{"type":"input_text","text":"real"}]}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":{"unknown":{"message":"nested prompt"}}}}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"ignored"}}`,
		`{"type":"event_msg","payload":{"message":"legacy event"}}`,
		`{"type":42,"role":"user","content":"Role prompt"}`,
		`{"type":"ai-title","sessionId":"chosen","aiTitle":" Canonical   title "}`,
		`{"type":"ai-title","sessionId":42,"aiTitle":false}`,
		`{"id":"chosen","unused":1e400}`, `{"id":"chosen","unused":0e400}`, `{"id":"chosen","unused":1e-400}`,
		`{"id":"chosen","unused":1.9e308}`, `{"id":"chosen","unused":{"overflow":-1e400}}`,
		`{"id":"chosen","unused":[1e400]}`, `{"unused":1e400,"unused":1,"id":"chosen"}`,
		`{"unknown":{"id":"chosen","cwd":"/work","branch":"한글 🎉 \"quoted\""}}`,
		`{"id":"chosen","unused":"1e400 \\\" still string"}`,
		fmt.Sprintf(`{"id":"chosen","unused":%s}`, strings.Repeat("9", 309)),
	}
	for _, alias := range []string{"sessionId", "session_id", "id", "cwd", "current_dir", "currentDir", "project_dir", "projectDir", "project_path", "projectPath", "working_directory", "workingDirectory", "gitBranch", "git_branch", "branch"} {
		records = append(records, fmt.Sprintf(`{"unknown":{%q:"  value  "}}`, alias))
	}
	for i, line := range records {
		for _, provider := range []string{AgentClaude, AgentCodex, AgentAntigravity} {
			for _, details := range []sessionDetails{{}, {id: "chosen", cwd: "/work", title: "Existing", titleProvenance: TitleExplicitProvider}, {title: "chosen"}, {id: "chosen", cwd: "/work", branch: "main", title: "Existing", titleProvenance: TitleDerivedUserPrompt}} {
				got, err := decodeSessionScanRecord([]byte(line), details, provider)
				var fields map[string]any
				legacyErr := json.Unmarshal([]byte(line), &fields)
				if (err != nil) != (legacyErr != nil) {
					t.Fatalf("record %d provider %s: error=%v legacy=%v", i, provider, err, legacyErr)
				}
				if err != nil {
					continue
				}
				var want sessionScanRecord
				if details.id == "" {
					want.id = firstNestedString(fields, "sessionId", "session_id", "id")
				}
				if details.cwd == "" {
					want.cwd = firstNestedString(fields, "cwd", "current_dir", "currentDir", "project_dir", "projectDir", "project_path", "projectPath", "working_directory", "workingDirectory")
				}
				if details.branch == "" {
					want.branch = firstNestedString(fields, "gitBranch", "git_branch", "branch")
				}
				seekCanonical := (provider == AgentClaude && details.titleProvenance != TitleExplicitProvider) || (provider == AgentCodex && details.titleProvenance != TitleDerivedUserPrompt)
				if details.title == "" || seekCanonical || titleIsResumeID(details.title, want.id) {
					want.title = titleFromRecord(fields)
					want.titleProvenance = titleProvenanceFromRecord(fields)
					// Only meaningful for the explicit-title branch in the scanner.
					if want.titleProvenance == TitleExplicitProvider {
						want.explicitSessionID = stringJSONField(fields, "sessionId")
					}
				}
				// Ignore unused title candidates exactly as the legacy scanner's
				// provider selection does: a noncanonical Claude title cannot
				// replace the already selected title.
				id := details.id
				if id == "" {
					id = want.id
				}
				if provider == AgentClaude && details.title != "" && !titleIsResumeID(details.title, id) && want.titleProvenance != TitleExplicitProvider {
					want.title, want.titleProvenance, want.explicitSessionID = "", TitleProvenanceNone, ""
				}
				if got.titleProvenance != TitleExplicitProvider {
					got.explicitSessionID = ""
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("record %d provider %s details=%+v:\ngot %+v\nwant %+v", i, provider, details, got, want)
				}
			}
		}
	}
}
