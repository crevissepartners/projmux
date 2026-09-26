package agentquestion

import (
	"encoding/json"
	"testing"
)

func TestCodexQuestionsKeepIDsAndTextOnlyAnswers(t *testing.T) {
	raw := json.RawMessage(`  [{"id":"first","question":"Same text","options":[{"label":"A"}]},{"id":"second","question":"Same text","options":null,"isSecret":true}]`)
	questions, err := ParseCodexQuestions(raw)
	if err != nil || len(questions) != 2 {
		t.Fatalf("parse = %d questions, %v", len(questions), err)
	}
	answers, err := BuildCodexAnswers(questions, map[int]Selection{0: {Labels: []string{"A"}}, 1: {Text: "private", HasText: true}})
	if err != nil || answers["first"] != `["A"]` || answers["second"] != `["private"]` {
		t.Fatalf("answers = %#v, %v", answers, err)
	}
	if _, err := ParseCodexQuestions(json.RawMessage(`[{"id":"same","question":"a"},{"id":"same","question":"b"}]`)); err == nil {
		t.Fatal("duplicate Codex question IDs accepted")
	}
}

func TestCodexOptionQuestionAcceptsTextOnlyWithOther(t *testing.T) {
	questions, err := ParseCodexQuestions(json.RawMessage(`[{"id":"q","question":"Pick","options":[{"label":"A"}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildCodexAnswers(questions, map[int]Selection{0: {Text: "free", HasText: true}}); err == nil {
		t.Fatal("free text accepted without isOther")
	}
	questions[0].IsOther = true
	if _, err := BuildCodexAnswers(questions, map[int]Selection{0: {Text: "free", HasText: true}}); err != nil {
		t.Fatal(err)
	}
}
