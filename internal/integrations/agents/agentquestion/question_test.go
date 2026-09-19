package agentquestion

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

const testQuestionsJSON = `[
  {"question": "Which build tool?", "header": "Build", "options": [{"label": "make", "description": "GNU make"}, {"label": "task"}, {"label": "just"}], "multiSelect": true},
  {"question": "Which branch?", "header": "Branch", "options": [{"label": "main"}, {"label": "dev"}], "multiSelect": false}
]`

func testQuestions(t *testing.T) []Question {
	t.Helper()
	questions, err := ParseQuestions(json.RawMessage(testQuestionsJSON))
	if err != nil {
		t.Fatal(err)
	}
	return questions
}

func TestParseQuestionsRejectsUnaddressableSets(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]string{
		"empty":              ``,
		"not an array":       `{"question":"x"}`,
		"no questions":       `[]`,
		"blank question":     `[{"question":" ","options":[{"label":"a"}]}]`,
		"duplicate question": `[{"question":"q","options":[{"label":"a"}]},{"question":"q","options":[{"label":"b"}]}]`,
		"no options":         `[{"question":"q","options":[]}]`,
		"blank label":        `[{"question":"q","options":[{"label":""}]}]`,
		"malformed":          `[{"question":`,
	} {
		if _, err := ParseQuestions(json.RawMessage(raw)); !errors.Is(err, ErrInvalidQuestions) {
			t.Errorf("%s: err = %v, want ErrInvalidQuestions", name, err)
		}
	}
	if questions, err := ParseQuestions(json.RawMessage(testQuestionsJSON)); err != nil || len(questions) != 2 || !questions[0].MultiSelect {
		t.Fatalf("valid set = %#v, %v", questions, err)
	}
}

func TestBuildAnswersValidationTable(t *testing.T) {
	t.Parallel()

	questions := testQuestions(t)
	branch := Selection{Labels: []string{"main"}}
	for _, test := range []struct {
		name       string
		selections map[int]Selection
		want       map[string]string
	}{
		{
			name:       "multi-select joins labels in option order",
			selections: map[int]Selection{0: {Labels: []string{"task", "make"}}, 1: branch},
			want:       map[string]string{"Which build tool?": "make, task", "Which branch?": "main"},
		},
		{
			name:       "a repeated label counts once",
			selections: map[int]Selection{0: {Labels: []string{"just", "just"}}, 1: branch},
			want:       map[string]string{"Which build tool?": "just", "Which branch?": "main"},
		},
		{
			name:       "explicit free text is taken as free text",
			selections: map[int]Selection{0: {Text: "bazel", HasText: true}, 1: branch},
			want:       map[string]string{"Which build tool?": "bazel", "Which branch?": "main"},
		},
		{name: "question number out of range", selections: map[int]Selection{0: {Labels: []string{"make"}}, 1: branch, 2: {Labels: []string{"x"}}}},
		{name: "label outside the options without free text", selections: map[int]Selection{0: {Labels: []string{"bazel"}}, 1: branch}},
		{name: "a question left unanswered", selections: map[int]Selection{0: {Labels: []string{"make"}}}},
		{name: "empty selection", selections: map[int]Selection{0: {}, 1: branch}},
		{name: "single-select with two labels", selections: map[int]Selection{0: {Labels: []string{"make"}}, 1: {Labels: []string{"main", "dev"}}}},
		{name: "empty free text", selections: map[int]Selection{0: {Text: "  ", HasText: true}, 1: branch}},
		{name: "free text together with a label", selections: map[int]Selection{0: {Labels: []string{"make"}, Text: "bazel", HasText: true}, 1: branch}},
		{name: "label differing only in case", selections: map[int]Selection{0: {Labels: []string{"Make"}}, 1: branch}},
	} {
		got, err := BuildAnswers(questions, test.selections)
		if test.want == nil {
			if !errors.Is(err, ErrInvalidAnswer) || got != nil {
				t.Errorf("%s: answers = %#v, err = %v, want ErrInvalidAnswer", test.name, got, err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s: answers = %#v, err = %v, want %#v", test.name, got, err, test.want)
		}
	}
}

func TestValidateAnswersRequiresExactQuestionTextKeys(t *testing.T) {
	t.Parallel()

	questions := testQuestions(t)
	for name, answers := range map[string]map[string]string{
		"wrong key":    {"Which build tool": "make", "Which branch?": "main"},
		"missing key":  {"Which build tool?": "make"},
		"extra key":    {"Which build tool?": "make", "Which branch?": "main", "Other?": "x"},
		"empty answer": {"Which build tool?": "", "Which branch?": "main"},
	} {
		if err := ValidateAnswers(questions, answers); !errors.Is(err, ErrInvalidAnswer) {
			t.Errorf("%s: err = %v, want ErrInvalidAnswer", name, err)
		}
	}
	if err := ValidateAnswers(questions, map[string]string{"Which build tool?": "make", "Which branch?": "main"}); err != nil {
		t.Fatal(err)
	}
}
