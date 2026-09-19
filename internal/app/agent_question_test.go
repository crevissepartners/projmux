package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
)

// questionTestAgent is the fixture Agent the question channel targets: the
// Running agt-alpha-codex, made a Claude Agent bound to pan-alpha-codex.
const (
	questionTestAgent = "agt-alpha-codex"
	questionTestPane  = "pan-alpha-codex"
)

// questionTestQuestions keeps its whitespace on purpose: the decision must
// carry these exact bytes back.
const questionTestQuestions = `[
    {"question": "Which build tool?", "header": "Build", "options": [{"label": "make"}, {"label": "task"}, {"label": "just"}], "multiSelect": true},
    {"question": "Which branch?", "header": "Branch", "options": [{"label": "main"}, {"label": "dev"}], "multiSelect": false}
  ]`

func questionTestPayload(event, tool string) string {
	return `{"hook_event_name":"` + event + `","session_id":"sess-1","cwd":"/srv/alpha","tool_name":"` + tool +
		`","tool_use_id":"toolu_1","tool_input":{"questions": ` + questionTestQuestions + `}}`
}

type questionFixture struct {
	resources *fakeResourceStore
	store     *agentquestion.Store
	command   *agentCommand
	opened    int
}

func newQuestionFixture(t *testing.T, enabled bool) *questionFixture {
	t.Helper()
	resources := newFakeResourceStore(t)
	agent, _ := resources.registry.Agent(questionTestAgent)
	agent.Spec.Provider = aiModeClaude
	agent.Metadata.Labels = nil
	if enabled {
		agent.Metadata.Annotations = map[string]string{coremetadata.AnnotationAgentQuestionChannel: coremetadata.QuestionChannelOn}
	}
	pane, _ := resources.registry.Pane(questionTestPane)
	pane.Status.Activation.AgentUID = questionTestAgent
	pane.Status.Activation.RuntimeID = "%7"
	pane.Status.Activation.Generation = "gen-1"
	if err := resources.registry.Validate(); err != nil {
		t.Fatal(err)
	}
	fixture := &questionFixture{resources: resources, store: agentquestion.NewStore(t.TempDir())}
	command, _, _ := newTestAgentCommand(t, resources)
	command.questionStore = func() (*agentquestion.Store, error) { return fixture.store, nil }
	fixture.command = command
	return fixture
}

func (f *questionFixture) hook(window time.Duration) claudeQuestionHook {
	return claudeQuestionHook{
		loadRegistry: f.resources.store().load,
		store: func() (*agentquestion.Store, error) {
			f.opened++
			return f.store, nil
		},
		window: window,
		poll:   10 * time.Millisecond,
		newID:  agentquestion.NewID,
		now:    time.Now,
	}
}

// startHook runs the hook in the background and waits until it has recorded
// its question.
func (f *questionFixture) startHook(t *testing.T, ctx context.Context, window time.Duration) (string, <-chan string) {
	t.Helper()
	done := make(chan string, 1)
	hook := f.hook(window)
	go func() {
		var stdout bytes.Buffer
		hook.run(ctx, []string{"--pane=" + questionTestPane}, strings.NewReader(questionTestPayload("PreToolUse", "AskUserQuestion")), &stdout, &bytes.Buffer{})
		done <- stdout.String()
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if records, err := f.store.List(questionTestAgent); err == nil && len(records) == 1 {
			return records[0].ID, done
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the hook never recorded its question")
	return "", nil
}

func waitHookOutput(t *testing.T, done <-chan string) string {
	t.Helper()
	select {
	case out := <-done:
		return out
	case <-time.After(10 * time.Second):
		t.Fatal("the hook did not return")
		return ""
	}
}

func TestClaudeQuestionHookAnswersWithTheOriginalQuestionBytes(t *testing.T) {
	t.Parallel()

	fixture := newQuestionFixture(t, true)
	id, done := fixture.startHook(t, context.Background(), time.Minute)
	record, _, _ := fixture.store.Get(id)
	if record.AgentUID != questionTestAgent || record.PaneUID != questionTestPane || record.SessionID != "sess-1" || record.ToolUseID != "toolu_1" || record.State != agentquestion.StateWaiting {
		t.Fatalf("recorded question = %#v", record)
	}
	stdout, _, err := runRoute(t, fixture.command, "question", "answer", "uid:"+questionTestAgent, id,
		"--index", "1=1", "--option", "1=task", "--text", "2=feature branch")
	if err != nil || stdout != id+" answered for agent/codex\n" {
		t.Fatalf("answer stdout=%q err=%v", stdout, err)
	}
	want := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"questions":` +
		questionTestQuestions + `,"answers":{"Which branch?":"feature branch","Which build tool?":"make, task"}}}}` + "\n"
	if got := waitHookOutput(t, done); got != want {
		t.Fatalf("decision =\n%s\nwant\n%s", got, want)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(want), &decoded); err != nil {
		t.Fatalf("decision is not JSON: %v", err)
	}
}

func TestClaudeQuestionHookExpiresSilentlyAndRefusesALateAnswer(t *testing.T) {
	t.Parallel()

	fixture := newQuestionFixture(t, true)
	id, done := fixture.startHook(t, context.Background(), 150*time.Millisecond)
	if got := waitHookOutput(t, done); got != "" {
		t.Fatalf("expired hook printed %q", got)
	}
	if record, _, _ := fixture.store.Get(id); record.State != agentquestion.StateExpired {
		t.Fatalf("state = %s, want expired", record.State)
	}
	_, _, err := runRoute(t, fixture.command, "question", "answer", "uid:"+questionTestAgent, id, "--option", "1=make", "--option", "2=main")
	if err == nil || !strings.Contains(err.Error(), "(question-expired)") || !IsUsageError(err) {
		t.Fatalf("late answer err = %v, want question-expired", err)
	}
}

func TestClaudeQuestionHookCanceledClosesSilentlyAndRefusesALateAnswer(t *testing.T) {
	t.Parallel()

	fixture := newQuestionFixture(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	id, done := fixture.startHook(t, ctx, time.Minute)
	cancel()
	if got := waitHookOutput(t, done); got != "" {
		t.Fatalf("canceled hook printed %q", got)
	}
	if record, _, _ := fixture.store.Get(id); record.State != agentquestion.StateClosed {
		t.Fatalf("state = %s, want closed", record.State)
	}
	_, _, err := runRoute(t, fixture.command, "question", "answer", "uid:"+questionTestAgent, id, "--option", "1=make", "--option", "2=main")
	if err == nil || !strings.Contains(err.Error(), "(question-closed)") {
		t.Fatalf("late answer err = %v, want question-closed", err)
	}
}

func TestClaudeQuestionHookDisableHandsTheQuestionBack(t *testing.T) {
	t.Parallel()

	fixture := newQuestionFixture(t, true)
	id, done := fixture.startHook(t, context.Background(), time.Minute)
	stdout, _, err := runRoute(t, fixture.command, "question", "disable", "uid:"+questionTestAgent)
	if err != nil || !strings.Contains(stdout, "closed 1 waiting question(s)") {
		t.Fatalf("disable stdout=%q err=%v", stdout, err)
	}
	if got := waitHookOutput(t, done); got != "" {
		t.Fatalf("hook printed %q after disable", got)
	}
	if record, _, _ := fixture.store.Get(id); record.State != agentquestion.StateClosed {
		t.Fatalf("state = %s, want closed", record.State)
	}
	agent, _ := fixture.resources.registry.Agent(questionTestAgent)
	if coremetadata.QuestionChannelEnabled(*agent) {
		t.Fatal("disable left the annotation set")
	}
}

func TestClaudeQuestionHookStaysSilentAndOpensNoStoreOutsideItsCase(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		enabled bool
		args    []string
		payload string
	}{
		{name: "channel off", args: []string{"--pane=" + questionTestPane}, payload: questionTestPayload("PreToolUse", "AskUserQuestion")},
		{name: "another tool", enabled: true, args: []string{"--pane=" + questionTestPane}, payload: questionTestPayload("PreToolUse", "Bash")},
		{name: "another event", enabled: true, args: []string{"--pane=" + questionTestPane}, payload: questionTestPayload("PostToolUse", "AskUserQuestion")},
		{name: "unknown pane", enabled: true, args: []string{"--pane=pan-nowhere"}, payload: questionTestPayload("PreToolUse", "AskUserQuestion")},
		{name: "no pane and an unknown session", enabled: true, args: []string{"--pane="}, payload: questionTestPayload("PreToolUse", "AskUserQuestion")},
		{name: "shell pane", enabled: true, args: []string{"--pane=pan-alpha-zsh"}, payload: questionTestPayload("PreToolUse", "AskUserQuestion")},
		{name: "subagent", enabled: true, args: []string{"--pane=" + questionTestPane}, payload: strings.Replace(questionTestPayload("PreToolUse", "AskUserQuestion"), `"session_id"`, `"agent_id":"sub-1","session_id"`, 1)},
		{name: "malformed payload", enabled: true, args: []string{"--pane=" + questionTestPane}, payload: `{"hook_event_name":`},
		{name: "malformed questions", enabled: true, args: []string{"--pane=" + questionTestPane}, payload: `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[]}}`},
		{name: "positional argument", enabled: true, args: []string{"extra"}, payload: questionTestPayload("PreToolUse", "AskUserQuestion")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newQuestionFixture(t, test.enabled)
			var stdout, stderr bytes.Buffer
			started := time.Now()
			fixture.hook(time.Minute).run(context.Background(), test.args, strings.NewReader(test.payload), &stdout, &stderr)
			if elapsed := time.Since(started); elapsed > time.Second {
				t.Fatalf("hook took %s", elapsed)
			}
			if stdout.Len() != 0 || stderr.Len() != 0 || fixture.opened != 0 {
				t.Fatalf("stdout=%q stderr=%q store opened %d times", stdout.String(), stderr.String(), fixture.opened)
			}
			if _, err := os.Stat(filepath.Dir(fixture.store.Path())); !os.IsNotExist(err) {
				t.Fatalf("store directory stat err = %v, want not exist", err)
			}
		})
	}
}

func TestClaudeQuestionHookRouteSkipsAutomaticHookMigration(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"internal", claudeQuestionHookRoute, "--pane=pan-1"},
		{"agent", "question", "list", "uid:agt-1"},
	} {
		if shouldRunLegacyHookMigrations(args) {
			t.Errorf("shouldRunLegacyHookMigrations(%q) = true", args)
		}
	}
}

// createQuestionRecord stores one waiting question for the fixture Agent.
func (f *questionFixture) createQuestionRecord(t *testing.T) agentquestion.Record {
	t.Helper()
	id, err := agentquestion.NewID()
	if err != nil {
		t.Fatal(err)
	}
	record, err := f.store.Create(agentquestion.Record{
		ID: id, AgentUID: questionTestAgent, PaneUID: questionTestPane,
		Questions: json.RawMessage(questionTestQuestions), Deadline: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestAgentQuestionAnswerInputForms(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want map[string]string
	}{
		{name: "labels", args: []string{"--option", "1=just", "--option", "2=dev"}, want: map[string]string{"Which build tool?": "just", "Which branch?": "dev"}},
		{name: "indexes join multi-select in option order", args: []string{"--index", "1=3", "--index", "1=1", "--index", "2=1"}, want: map[string]string{"Which build tool?": "make, just", "Which branch?": "main"}},
		{name: "explicit free text", args: []string{"--text", "1=bazel", "--text", "2=release/1.0"}, want: map[string]string{"Which build tool?": "bazel", "Which branch?": "release/1.0"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newQuestionFixture(t, true)
			record := fixture.createQuestionRecord(t)
			args := append([]string{"question", "answer", "uid:" + questionTestAgent, record.ID}, test.args...)
			if _, _, err := runRoute(t, fixture.command, args...); err != nil {
				t.Fatal(err)
			}
			got, _, _ := fixture.store.Get(record.ID)
			if got.State != agentquestion.StateAnswered || len(got.Answers) != len(test.want) {
				t.Fatalf("record = %#v", got)
			}
			for key, value := range test.want {
				if got.Answers[key] != value {
					t.Fatalf("answers = %#v, want %#v", got.Answers, test.want)
				}
			}
		})
	}
}

func TestAgentQuestionAnswerRefusalsLeaveTheRecordWaiting(t *testing.T) {
	t.Parallel()

	fixture := newQuestionFixture(t, true)
	record := fixture.createQuestionRecord(t)
	for _, test := range []struct {
		name   string
		id     string
		args   []string
		reason string
	}{
		{name: "label outside the options", args: []string{"--option", "1=bazel", "--option", "2=main"}, reason: questionReasonInvalidAnswer},
		{name: "index out of range", args: []string{"--index", "1=4", "--option", "2=main"}, reason: questionReasonInvalidAnswer},
		{name: "single-select with two options", args: []string{"--option", "1=make", "--option", "2=main", "--option", "2=dev"}, reason: questionReasonInvalidAnswer},
		{name: "a question left unanswered", args: []string{"--option", "1=make"}, reason: questionReasonInvalidAnswer},
		{name: "question number out of range", args: []string{"--option", "1=make", "--option", "2=main", "--option", "3=x"}, reason: questionReasonInvalidAnswer},
		{name: "malformed occurrence", args: []string{"--option", "make", "--option", "2=main"}, reason: questionReasonInvalidAnswer},
		{name: "empty free text", args: []string{"--text", "1=", "--option", "2=main"}, reason: questionReasonInvalidAnswer},
		{name: "unknown question", id: "question-00000000000000ff", args: []string{"--option", "1=make", "--option", "2=main"}, reason: questionReasonNotFound},
		{name: "malformed question id", id: "nope", args: []string{"--option", "1=make", "--option", "2=main"}, reason: questionReasonNotFound},
	} {
		id := test.id
		if id == "" {
			id = record.ID
		}
		args := append([]string{"question", "answer", "uid:" + questionTestAgent, id}, test.args...)
		_, _, err := runRoute(t, fixture.command, args...)
		if err == nil || !strings.Contains(err.Error(), "("+test.reason+")") || !IsUsageError(err) {
			t.Errorf("%s: err = %v, want %s", test.name, err, test.reason)
		}
	}
	if got, _, _ := fixture.store.Get(record.ID); got.State != agentquestion.StateWaiting || got.Answers != nil {
		t.Fatalf("refusals changed the record: %#v", got)
	}
	if _, _, err := runRoute(t, fixture.command, "question", "answer", "uid:"+questionTestAgent, record.ID, "--option", "1=make", "--option", "2=main"); err != nil {
		t.Fatal(err)
	}
	_, _, err := runRoute(t, fixture.command, "question", "answer", "uid:"+questionTestAgent, record.ID, "--option", "1=make", "--option", "2=main")
	if err == nil || !strings.Contains(err.Error(), "(question-not-pending)") {
		t.Fatalf("second answer err = %v, want question-not-pending", err)
	}
}

func TestAgentQuestionChannelSwitchAndProviderRefusals(t *testing.T) {
	t.Parallel()

	fixture := newQuestionFixture(t, false)
	record := fixture.createQuestionRecord(t)
	_, _, err := runRoute(t, fixture.command, "question", "answer", "uid:"+questionTestAgent, record.ID, "--option", "1=make", "--option", "2=main")
	if err == nil || !strings.Contains(err.Error(), "(question-channel-off)") {
		t.Fatalf("answer with the channel off err = %v", err)
	}
	stdout, _, err := runRoute(t, fixture.command, "question", "enable", "uid:"+questionTestAgent)
	if err != nil || stdout != "agent/codex question channel on\n" {
		t.Fatalf("enable stdout=%q err=%v", stdout, err)
	}
	agent, _ := fixture.resources.registry.Agent(questionTestAgent)
	if agent.Metadata.Annotations[coremetadata.AnnotationAgentQuestionChannel] != coremetadata.QuestionChannelOn {
		t.Fatalf("annotations = %#v", agent.Metadata.Annotations)
	}
	writes := fixture.resources.writes
	if stdout, _, err := runRoute(t, fixture.command, "question", "enable", "uid:"+questionTestAgent); err != nil || !strings.Contains(stdout, "already on") || fixture.resources.writes != writes {
		t.Fatalf("repeat enable stdout=%q err=%v writes=%d->%d", stdout, err, writes, fixture.resources.writes)
	}
	stdout, _, err = runRoute(t, fixture.command, "question", "list", "uid:"+questionTestAgent, "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var listed agentQuestionList
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil || listed.Channel != "on" || len(listed.Questions) != 1 ||
		listed.Questions[0].ID != record.ID || listed.Questions[0].State != agentquestion.StateWaiting ||
		len(listed.Questions[0].Prompts) != 2 || listed.Questions[0].Prompts[0].Options[2].Label != "just" || !listed.Questions[0].Prompts[0].MultiSelect {
		t.Fatalf("list json = %s (%v)", stdout, err)
	}
	if stdout, _, err := runRoute(t, fixture.command, "question", "list", "uid:"+questionTestAgent); err != nil ||
		!strings.Contains(stdout, record.ID+"\twaiting") || !strings.Contains(stdout, "1. [Build] Which build tool? (multi-select)") || !strings.Contains(stdout, "3) just") {
		t.Fatalf("list text = %q, %v", stdout, err)
	}

	_, _, err = runRoute(t, fixture.command, "question", "enable", "uid:agt-beta-codex")
	if err == nil || !strings.Contains(err.Error(), "(question-provider-unsupported)") {
		t.Fatalf("enable on a Codex Agent err = %v", err)
	}
}
