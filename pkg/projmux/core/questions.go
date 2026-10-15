package core

import "time"

type QuestionState string

const (
	QuestionWaiting  QuestionState = "waiting"
	QuestionAnswered QuestionState = "answered"
	QuestionExpired  QuestionState = "expired"
	QuestionClosed   QuestionState = "closed"
)

// QuestionList is the `agent question list -o json` projection.
type QuestionList struct {
	AgentUID       string          `json:"agentUID"`
	AgentName      string          `json:"agentName"`
	Channel        string          `json:"channel"`
	Questions      []QuestionView  `json:"questions"`
	Policy         string          `json:"policy"`
	ExactQuestions []ExactQuestion `json:"exactQuestions,omitempty"`
}

// QuestionView is one question record.
type QuestionView struct {
	ID          string            `json:"id"`
	State       QuestionState     `json:"state"`
	Disposition string            `json:"disposition,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
	Deadline    time.Time         `json:"deadline"`
	UpdatedAt   time.Time         `json:"updatedAt"`
	Prompts     []QuestionPrompt  `json:"prompts"`
	Answers     map[string]string `json:"answers,omitempty"`
	Delivery    string            `json:"delivery,omitempty"`
	CanAnswer   bool              `json:"canAnswer"`
}

// QuestionPrompt is one question of a record, numbered the way `answer`
// addresses it.
type QuestionPrompt struct {
	Number      int              `json:"number"`
	ID          string           `json:"id,omitempty"`
	Header      string           `json:"header,omitempty"`
	Question    string           `json:"question"`
	MultiSelect bool             `json:"multiSelect"`
	IsOther     bool             `json:"isOther"`
	IsSecret    bool             `json:"isSecret,omitempty"`
	Options     []QuestionOption `json:"options"`
}

// QuestionOption is one option, numbered the way `--index` addresses it.
type QuestionOption struct {
	Number      int    `json:"number"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// ExactQuestion is redacted display data for an existing process host question.
type ExactQuestion struct {
	QuestionID string             `json:"questionID"`
	Policy     string             `json:"policy"`
	Delivery   string             `json:"delivery"`
	CanAnswer  bool               `json:"canAnswer"`
	Prompts    []ProviderQuestion `json:"prompts"`
	State      QuestionState      `json:"state"`
	Deadline   *time.Time         `json:"deadline,omitempty"`
}
type ProviderQuestion struct {
	ID          string                   `json:"id,omitempty"`
	Question    string                   `json:"question"`
	Header      string                   `json:"header,omitempty"`
	Options     []ProviderQuestionOption `json:"options"`
	MultiSelect bool                     `json:"multiSelect,omitempty"`
	IsOther     bool                     `json:"isOther,omitempty"`
	IsSecret    bool                     `json:"isSecret,omitempty"`
}
type ProviderQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// QuestionSelection uses 1-based question and option numbers, like the CLI.
// Each occurrence selects exactly one Option, Index, or Text. Text is a pointer
// so an explicitly empty free-text answer is distinct from no text.
type QuestionSelection struct {
	Question int
	Option   *string
	Index    *int
	Text     *string
}
type QuestionAnswerResult struct {
	QuestionID, AgentUID, AgentName string
	Delivery                        string `json:",omitempty"`
	Outcome                         string `json:",omitempty"`
}
