package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// claudeObservedFrameCorpus is the preserved real-provider frame corpus. Its
// frames carry only observed key shape; see the corpus description.
type claudeObservedFrameCorpus struct {
	Description string                    `json:"description"`
	Items       []claudeObservedFrameItem `json:"items"`
	Gaps        []struct {
		Shape  string `json:"shape"`
		Reason string `json:"reason"`
	} `json:"gaps"`
}

type claudeObservedFrameItem struct {
	Name       string `json:"name"`
	Provenance struct {
		ObservedAt      string   `json:"observedAt"`
		Provider        string   `json:"provider"`
		Evidence        string   `json:"evidence"`
		EvidenceNote    string   `json:"evidenceNote"`
		ObservedKeys    []string `json:"observedKeys"`
		DerivedKeys     []string `json:"derivedKeys"`
		ScaffoldingKeys []string `json:"scaffoldingKeys"`
		Values          string   `json:"values"`
	} `json:"provenance"`
	State   string          `json:"state"`
	Frame   json.RawMessage `json:"frame"`
	Verdict struct {
		Kind        string   `json:"kind"`
		Observation string   `json:"observation,omitempty"`
		Rule        string   `json:"rule"`
		RuleKeys    []string `json:"ruleKeys,omitempty"`
	} `json:"verdict"`
}

func TestClaudeDialogueStreamReplaysObservedFrameCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/claude-dialogue-observed-frames.json")
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var corpus claudeObservedFrameCorpus
	if err := decoder.Decode(&corpus); err != nil {
		t.Fatal("observed frame corpus does not decode:", err)
	}
	if len(corpus.Items) == 0 {
		t.Fatal("observed frame corpus has no items")
	}
	names := map[string]bool{}
	for _, item := range corpus.Items {
		if item.Name == "" || names[item.Name] {
			t.Fatalf("observed frame corpus item name %q is empty or repeated", item.Name)
		}
		names[item.Name] = true
		label := fmt.Sprintf("observed frame %q (rule: %s)", item.Name, item.Verdict.Rule)
		t.Run(item.Name, func(t *testing.T) {
			frame := claudeObservedFrameObject(t, label, item.Frame)
			claudeObservedFrameCheckLabel(t, label, item, frame)
			line := &bytes.Buffer{}
			if err := json.Compact(line, item.Frame); err != nil {
				t.Fatal(label, err)
			}
			if item.Verdict.Rule == "" {
				t.Errorf("%s: recorded verdict has no rule", label)
			}
			want := item.Verdict.Kind
			switch {
			case want == "observation" && item.Verdict.Observation != "" && len(item.Verdict.RuleKeys) == 0:
				want += " " + item.Verdict.Observation
			case want == "drop" && item.Verdict.Observation == "" && len(item.Verdict.RuleKeys) == 0:
			case want == "reject" && item.Verdict.Observation == "" && len(item.Verdict.RuleKeys) > 0:
			default:
				t.Fatalf("%s: recorded verdict %+v is malformed", label, item.Verdict)
			}
			if got := claudeObservedFrameVerdict(t, label, item.State, frame, line.Bytes()); got != want {
				t.Errorf("%s: recorded %s, got %s", label, want, got)
			}
			if want != "reject" {
				return
			}
			// inspect returns one opaque error, so attribute the reject: without
			// the keys its recorded rule names, the same frame must stop rejecting.
			for _, key := range item.Verdict.RuleKeys {
				if _, exists := frame[key]; !exists {
					t.Fatalf("%s: recorded rule key %q is not a top-level frame key", label, key)
				}
				delete(frame, key)
			}
			stripped, err := json.Marshal(frame)
			if err != nil {
				t.Fatal(label, err)
			}
			if got := claudeObservedFrameVerdict(t, label, item.State, frame, stripped); got == "reject" {
				t.Errorf("%s: still rejected without recorded rule keys %v, so the recorded rule does not explain the reject", label, item.Verdict.RuleKeys)
			}
		})
	}
}

// claudeObservedFrameObject decodes one corpus frame as a JSON object.
func claudeObservedFrameObject(t *testing.T, label string, raw json.RawMessage) map[string]any {
	t.Helper()
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil || frame == nil {
		t.Fatalf("%s: frame is not a JSON object: %v", label, err)
	}
	return frame
}

// claudeObservedFrameCheckLabel requires every item to carry its provenance and
// to label each top-level frame key exactly once as observed, derived or
// scaffolding, so no key enters the corpus unexplained.
func claudeObservedFrameCheckLabel(t *testing.T, label string, item claudeObservedFrameItem, frame map[string]any) {
	t.Helper()
	provenance := item.Provenance
	if _, err := time.Parse("2006-01-02", provenance.ObservedAt); err != nil || provenance.Provider == "" || !slices.Contains([]string{"top-level-key-list", "allowlist-diff", "diagnosis-quote"}, provenance.Evidence) || provenance.EvidenceNote == "" || provenance.Values == "" {
		t.Errorf("%s: provenance label is incomplete", label)
	}
	if len(provenance.ObservedKeys) == 0 {
		t.Errorf("%s: no observed key", label)
	}
	labelled := map[string]int{}
	for _, keys := range [][]string{provenance.ObservedKeys, provenance.DerivedKeys, provenance.ScaffoldingKeys} {
		for _, key := range keys {
			top, _, _ := strings.Cut(key, ".")
			if _, exists := frame[top]; !exists {
				t.Errorf("%s: labelled key %q is not in the frame", label, key)
			}
			if key == top {
				labelled[key]++
			}
		}
	}
	for key := range frame {
		if labelled[key] != 1 {
			t.Errorf("%s: top-level key %q is labelled %d times, want exactly once", label, key, labelled[key])
		}
	}
}

// claudeObservedFrameVerdict replays one line through a fresh stream in the
// declared state and names the result: reject, drop, or observation <kind>.
func claudeObservedFrameVerdict(t *testing.T, label, state string, frame map[string]any, line []byte) string {
	t.Helper()
	session, ok := frame["session_id"].(string)
	if !ok {
		t.Fatalf("%s: frame has no string session_id to share with scaffolding", label)
	}
	out, err := claudeObservedFrameStream(t, label, state, session).inspect(line)
	switch {
	case err != nil:
		return "reject"
	case out == nil:
		return "drop"
	default:
		return "observation " + out.Kind
	}
}

// claudeObservedFrameStream builds a fresh stream in one declared state.
//
// SYNTHETIC SCAFFOLDING, NOT CORPUS: these frames mirror the validator schema
// only to move the stream into the state an item needs. They prove nothing about
// the provider, and a verdict must never be recorded against them.
func claudeObservedFrameStream(t *testing.T, label, state, session string) *claudeDialogueStream {
	t.Helper()
	s := &claudeDialogueStream{}
	scaffold := func(event map[string]any) *claudeDialogueObservation {
		event["session_id"] = session
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(label, err)
		}
		out, err := s.inspect(data)
		if err != nil {
			t.Fatalf("%s: synthetic scaffolding for state %q was rejected", label, state)
		}
		return out
	}
	if !slices.Contains([]string{"fresh-after-startup-hooks", "initialized", "ready"}, state) {
		t.Fatalf("%s: unknown stream state %q", label, state)
	}
	for i := range 2 {
		for _, subtype := range []string{"hook_started", "hook_response"} {
			scaffold(map[string]any{"type": "system", "subtype": subtype, "hook_id": fmt.Sprintf("scaffold-hook-%d", i), "hook_event": "SessionStart", "hook_name": "SessionStart:startup", "exit_code": 0, "outcome": "success"})
		}
	}
	if state == "fresh-after-startup-hooks" {
		return s
	}
	if out := scaffold(map[string]any{"type": "system", "subtype": "init", "tools": []string{"Bash"}, "mcp_servers": []any{}, "plugins": []any{}, "permissionMode": "dontAsk", "claude_code_version": claudeFrozenFrameProviderVersion}); out == nil || out.Kind != "init" {
		t.Fatalf("%s: synthetic init did not initialize the stream", label)
	}
	if state == "initialized" {
		return s
	}
	if out := scaffold(map[string]any{"type": "result", "subtype": "success", "is_error": false}); out == nil || out.Kind != "ready" {
		t.Fatalf("%s: synthetic result did not make the stream ready", label)
	}
	return s
}
