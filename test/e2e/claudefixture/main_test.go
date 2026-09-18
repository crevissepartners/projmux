package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// observedFrameCorpus is the preserved real-provider frame corpus that L20
// hands to this fixture through PROJMUX_FAKE_CLAUDE_OBSERVED_FRAMES.
const observedFrameCorpus = "../../../internal/app/testdata/claude-dialogue-observed-frames.json"

// fixtureEnvelopeVocabulary is the frame vocabulary this fixture legitimately
// spells for its own synthetic events. Every other key or type value that a
// dropped corpus frame carries must reach the stream only from the corpus.
var fixtureEnvelopeVocabulary = []string{"type", "subtype", "session_id", "message", "role", "content", "text", "assistant", "system"}

type corpusItem struct {
	Name    string         `json:"name"`
	State   string         `json:"state"`
	Frame   map[string]any `json:"frame"`
	Verdict struct {
		Kind string `json:"kind"`
	} `json:"verdict"`
}

// readCorpus returns the raw corpus and its drop items, decoded independently
// of the fixture's own selection.
func readCorpus(t *testing.T) ([]byte, []corpusItem) {
	t.Helper()
	raw, err := os.ReadFile(observedFrameCorpus)
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Items []corpusItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal("observed frame corpus does not decode:", err)
	}
	var drops []corpusItem
	for _, item := range corpus.Items {
		if item.Verdict.Kind == "drop" {
			drops = append(drops, item)
		}
	}
	if len(drops) == 0 {
		t.Fatal("observed frame corpus has no drop item, so L20 would emit no side frame")
	}
	return raw, drops
}

func TestObservedSideFramesAreExactlyCorpusDropItems(t *testing.T) {
	raw, drops := readCorpus(t)
	got, err := selectObservedSideFrames(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(drops) {
		t.Fatalf("fixture selected %d side frames, corpus has %d drop items", len(got), len(drops))
	}
	for i, item := range drops {
		if got[i].Name != item.Name || !reflect.DeepEqual(got[i].Frame, item.Frame) {
			t.Errorf("side frame %d is %q %v, want corpus drop item %q %v", i, got[i].Name, got[i].Frame, item.Name, item.Frame)
		}
	}
}

func TestObservedSideFramesFailClosed(t *testing.T) {
	raw, _ := readCorpus(t)
	mutated := func(change func(drop map[string]any, items []any)) []byte {
		var corpus map[string]any
		if err := json.Unmarshal(raw, &corpus); err != nil {
			t.Fatal(err)
		}
		items := corpus["items"].([]any)
		for _, value := range items {
			if item := value.(map[string]any); item["verdict"].(map[string]any)["kind"] == "drop" {
				change(item, items)
				break
			}
		}
		data, err := json.Marshal(corpus)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	for name, test := range map[string]struct {
		data []byte
		want string
	}{
		"malformed": {[]byte(`{"items":[`), "malformed"},
		"no items":  {[]byte(`{"items":[]}`), "no dropped side frame"},
		"zero drop items": {mutated(func(_ map[string]any, items []any) {
			for _, value := range items {
				value.(map[string]any)["verdict"].(map[string]any)["kind"] = "reject"
			}
		}), "no dropped side frame"},
		"state not initialized": {mutated(func(drop map[string]any, _ []any) { drop["state"] = "ready" }), "only places frames in state initialized"},
		"frame not an object":   {mutated(func(drop map[string]any, _ []any) { drop["frame"] = []any{} }), "is not a JSON object"},
		"frame missing":         {mutated(func(drop map[string]any, _ []any) { delete(drop, "frame") }), "is not a JSON object"},
		"no session_id": {mutated(func(drop map[string]any, _ []any) {
			delete(drop["frame"].(map[string]any), "session_id")
		}), "no string session_id slot"},
		"session_id not a string": {mutated(func(drop map[string]any, _ []any) {
			drop["frame"].(map[string]any)["session_id"] = nil
		}), "no string session_id slot"},
		"name not one line": {mutated(func(drop map[string]any, _ []any) { drop["name"] = "two\nlines" }), "not one line"},
	} {
		t.Run(name, func(t *testing.T) {
			frames, err := selectObservedSideFrames(test.data)
			if err == nil {
				t.Fatalf("selected %d side frames, want a fail-closed error", len(frames))
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not name the %q failure", err, test.want)
			}
		})
	}
}

func TestObservedSideFramesLoadOnlyAnAbsoluteRegularCorpus(t *testing.T) {
	absolute, err := filepath.Abs(observedFrameCorpus)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", observedFrameCorpus, filepath.Dir(absolute), filepath.Join(t.TempDir(), "missing.json")} {
		t.Setenv(observedFramesEnv, path)
		if _, err := loadObservedSideFrames(); err == nil {
			t.Errorf("corpus path %q loaded, want a fail-closed error", path)
		}
	}
	t.Setenv(observedFramesEnv, absolute)
	if _, err := loadObservedSideFrames(); err != nil {
		t.Fatal(err)
	}
}

// TestFixtureSourceSpellsNoCorpusSideFrameVocabulary keeps the corpus the only
// source of side-frame shape: if main.go spells a key or type value that the
// dropped corpus frames carry, the corpus and the fixture can diverge silently.
func TestFixtureSourceSpellsNoCorpusSideFrameVocabulary(t *testing.T) {
	_, drops := readCorpus(t)
	vocabulary := map[string]bool{}
	var collect func(value any)
	collect = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for key, nested := range value {
				vocabulary[key] = true
				if text, ok := nested.(string); ok && (key == "type" || key == "subtype") {
					vocabulary[text] = true
				}
				collect(nested)
			}
		case []any:
			for _, nested := range value {
				collect(nested)
			}
		}
	}
	for _, item := range drops {
		collect(item.Frame)
	}
	for word := range vocabulary {
		if slices.Contains(fixtureEnvelopeVocabulary, word) {
			delete(vocabulary, word)
		}
	}
	if len(vocabulary) == 0 {
		t.Fatal("corpus drop frames carry no vocabulary beyond the fixture envelope, so this check proves nothing")
	}
	files := token.NewFileSet()
	source, err := parser.ParseFile(files, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(source, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil {
			t.Fatalf("%s: unquote %s: %v", files.Position(literal.Pos()), literal.Value, err)
		}
		if vocabulary[value] {
			t.Errorf("%s spells %q, which only the corpus drop frames carry; emit it from the corpus instead (envelope allowlist: %s)",
				files.Position(literal.Pos()), value, strings.Join(fixtureEnvelopeVocabulary, " "))
		}
		return true
	})
}

// TestCoordinationContentDecodesEveryFrameShape feeds the fixture's strict
// decoder each frame shape the producer renders at schemaVersion 2. The
// fixture rejects unknown fields, so a producer key it does not declare would
// stop L20 at its first frame; operator input is decoded here although no e2e
// scenario sends it yet.
func TestCoordinationContentDecodesEveryFrameShape(t *testing.T) {
	const tail = `"payload":"marker","sourceNotice":"notice","replyAction":""}`
	for name, frame := range map[string]string{
		"peer": `{"kind":"projmux-coordination","schemaVersion":2,"authority":"untrusted-coordination-only",` +
			`"messageRef":"message-peer","conversationRef":"conversation-peer","replyTo":"message-earlier",` +
			`"source":{"agentUID":"codex-agent","provider":"codex"},"target":{"agentUID":"claude-agent","provider":"claude"},` + tail,
		"operator": `{"kind":"projmux-coordination","schemaVersion":2,"authority":"untrusted-coordination-only",` +
			`"messageRef":"message-operator","conversationRef":"conversation-operator",` +
			`"source":{"kind":"operator","client":"web"},"target":{"agentUID":"claude-agent","provider":"claude"},` + tail,
	} {
		var content coordinationContent
		if err := decodeExact([]byte(frame), &content); err != nil || content.SchemaVersion != 2 || content.MessageRef == "" {
			t.Fatalf("%s frame did not decode: %+v %v", name, content, err)
		}
		if name == "operator" && (content.Source["kind"] != "operator" || content.Source["client"] != "web" || content.Source["agentUID"] != "") {
			t.Fatalf("operator source = %v", content.Source)
		}
	}
}
