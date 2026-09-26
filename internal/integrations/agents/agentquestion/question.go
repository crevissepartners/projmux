// Package agentquestion persists Agent questions while their operator opted
// into answering them from the command line, and owns answer validation.
// Claude questions arrive through a PreToolUse hook; Codex questions arrive
// through a blocking app-server request. In both cases, `projmux agent question
// answer` writes the answer that the waiting provider receives.
package agentquestion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// MultiSelectSeparator joins the labels of a multi-select answer. Claude Code
// reads a multi-select answer as its labels joined with a comma; the separator
// it writes itself is ", ".
const MultiSelectSeparator = ", "

const (
	// maxQuestions bounds one question set. Claude Code asks at most four at a
	// time; the bound only keeps a malformed payload out of the store.
	maxQuestions = 16
	// maxOptions bounds the options of one question for the same reason.
	maxOptions = 32
	// MaxQuestionsBytes bounds the raw question set one record stores.
	MaxQuestionsBytes = 16 << 10
)

// ErrInvalidQuestions reports a question set this package will not hold open:
// the hook then leaves the question to Claude Code's own prompt.
var ErrInvalidQuestions = errors.New("invalid AskUserQuestion question set")

// ErrInvalidAnswer reports an answer that does not fit its question set.
var ErrInvalidAnswer = errors.New("invalid question answer")

// Option is one choice of a question.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Question is one provider question. Codex uses ID, IsOther, and IsSecret;
// Claude uses the remaining fields.
type Question struct {
	ID          string   `json:"id,omitempty"`
	Question    string   `json:"question"`
	Header      string   `json:"header,omitempty"`
	Options     []Option `json:"options"`
	MultiSelect bool     `json:"multiSelect,omitempty"`
	IsOther     bool     `json:"isOther,omitempty"`
	IsSecret    bool     `json:"isSecret,omitempty"`
}

// ParseCodexQuestions keeps Codex's question IDs, including text-only and
// secret questions. Claude's stricter ParseQuestions contract is unchanged.
func ParseCodexQuestions(raw json.RawMessage) ([]Question, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > MaxQuestionsBytes || raw[0] != '[' {
		return nil, ErrInvalidQuestions
	}
	var questions []Question
	if json.Unmarshal(raw, &questions) != nil || len(questions) == 0 || len(questions) > maxQuestions {
		return nil, ErrInvalidQuestions
	}
	seen := make(map[string]bool, len(questions))
	for _, question := range questions {
		if strings.TrimSpace(question.ID) == "" || seen[question.ID] || strings.TrimSpace(question.Question) == "" || len(question.Options) > maxOptions {
			return nil, ErrInvalidQuestions
		}
		seen[question.ID] = true
		for _, option := range question.Options {
			if strings.TrimSpace(option.Label) == "" {
				return nil, ErrInvalidQuestions
			}
		}
	}
	return questions, nil
}

// BuildCodexAnswers validates CLI selections and stores each response array as
// JSON under the question ID. The array is preserved for the app-server reply.
func BuildCodexAnswers(questions []Question, selections map[int]Selection) (map[string]string, error) {
	if len(selections) != len(questions) {
		return nil, ErrInvalidAnswer
	}
	answers := make(map[string]string, len(questions))
	for i, question := range questions {
		selection, ok := selections[i]
		if !ok || (selection.HasText && len(selection.Labels) != 0) {
			return nil, ErrInvalidAnswer
		}
		var values []string
		if selection.HasText {
			if strings.TrimSpace(selection.Text) == "" || (len(question.Options) != 0 && !question.IsOther && !question.IsSecret) {
				return nil, ErrInvalidAnswer
			}
			values = []string{selection.Text}
		} else {
			if len(selection.Labels) == 0 || len(question.Options) == 0 {
				return nil, ErrInvalidAnswer
			}
			for _, label := range selection.Labels {
				if !slices.ContainsFunc(question.Options, func(option Option) bool { return option.Label == label }) || slices.Contains(values, label) {
					return nil, ErrInvalidAnswer
				}
				values = append(values, label)
			}
		}
		encoded, _ := json.Marshal(values)
		answers[question.ID] = string(encoded)
	}
	return answers, nil
}

// ParseQuestions decodes and validates a raw `questions` array. It accepts only
// a set every answer can address unambiguously: at least one question, each
// with a distinct non-empty question text and at least one option with a
// non-empty label.
func ParseQuestions(raw json.RawMessage) ([]Question, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > MaxQuestionsBytes || raw[0] != '[' {
		return nil, ErrInvalidQuestions
	}
	var questions []Question
	if err := json.Unmarshal(raw, &questions); err != nil {
		return nil, ErrInvalidQuestions
	}
	if len(questions) == 0 || len(questions) > maxQuestions {
		return nil, ErrInvalidQuestions
	}
	seen := make(map[string]bool, len(questions))
	for _, question := range questions {
		if strings.TrimSpace(question.Question) == "" || seen[question.Question] {
			return nil, ErrInvalidQuestions
		}
		seen[question.Question] = true
		if len(question.Options) == 0 || len(question.Options) > maxOptions {
			return nil, ErrInvalidQuestions
		}
		for _, option := range question.Options {
			if strings.TrimSpace(option.Label) == "" {
				return nil, ErrInvalidQuestions
			}
		}
	}
	return questions, nil
}

// Selection is the answer to one question before provider encoding: option
// labels, or free text given explicitly as free text.
type Selection struct {
	Labels []string
	// Text is free text. It is used only when HasText is true, so a label that
	// is not one of the options is never taken for free text by accident.
	Text    string
	HasText bool
}

// invalidAnswer wraps ErrInvalidAnswer with the reason a person can act on.
func invalidAnswer(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidAnswer, fmt.Sprintf(format, args...))
}

// BuildAnswers turns one Selection per question into the answers map Claude
// Code reads: the exact question text to the selected label, the selected
// labels joined with MultiSelectSeparator in option order, or the free text.
// selections is keyed by the 0-based question index. Every question needs a
// non-empty answer; a single-select question takes exactly one label; a label
// must be one of that question's options; free text is taken only as free text
// and not together with labels.
func BuildAnswers(questions []Question, selections map[int]Selection) (map[string]string, error) {
	for index := range selections {
		if index < 0 || index >= len(questions) {
			return nil, invalidAnswer("question %d does not exist; this question set has %d", index+1, len(questions))
		}
	}
	answers := make(map[string]string, len(questions))
	for index, question := range questions {
		selection, ok := selections[index]
		if !ok || (len(selection.Labels) == 0 && !selection.HasText) {
			return nil, invalidAnswer("question %d (%q) has no answer; every question needs one", index+1, question.Question)
		}
		if selection.HasText {
			if len(selection.Labels) != 0 {
				return nil, invalidAnswer("question %d takes free text or options, not both", index+1)
			}
			text := strings.TrimSpace(selection.Text)
			if text == "" {
				return nil, invalidAnswer("question %d has empty free text", index+1)
			}
			answers[question.Question] = text
			continue
		}
		chosen := make([]bool, len(question.Options))
		count := 0
		for _, label := range selection.Labels {
			position := slices.IndexFunc(question.Options, func(option Option) bool { return option.Label == label })
			if position < 0 {
				return nil, invalidAnswer("question %d has no option %q; give free text as free text", index+1, label)
			}
			if !chosen[position] {
				chosen[position] = true
				count++
			}
		}
		if !question.MultiSelect && count != 1 {
			return nil, invalidAnswer("question %d is single-select and takes exactly one option, got %d", index+1, count)
		}
		labels := make([]string, 0, count)
		for position, option := range question.Options {
			if chosen[position] {
				labels = append(labels, option.Label)
			}
		}
		answers[question.Question] = strings.Join(labels, MultiSelectSeparator)
	}
	return answers, nil
}

// ValidateAnswers is the structural check the store repeats under its lock:
// exactly one non-empty answer for every question text, and nothing else.
func ValidateAnswers(questions []Question, answers map[string]string) error {
	if len(answers) != len(questions) {
		return invalidAnswer("got %d answers for %d questions", len(answers), len(questions))
	}
	for _, question := range questions {
		if strings.TrimSpace(answers[question.Question]) == "" {
			return invalidAnswer("question %q has no answer", question.Question)
		}
	}
	return nil
}
