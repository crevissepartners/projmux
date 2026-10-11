package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/integrations/processquestions"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func questionJSON(t *testing.T, v any) string {
	t.Helper()
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
func ptr[T any](v T) *T { return &v }

type sdkQuestionFixture struct {
	reg    metadata.Registry
	client *projmux.Client
	cmd    *agentCommand
	store  *agentquestion.Store
	state  string
	record agentquestion.Record
}

func newSDKQuestionFixture(t *testing.T, process bool) sdkQuestionFixture {
	t.Helper()
	reg, _ := sdkResourceFixture(t)
	state := filepath.Join(t.TempDir(), "state")
	configDir := filepath.Join(t.TempDir(), "config")
	a := &reg.Agents[0]
	a.Spec.Provider = "claude"
	a.Status.PaneRef = "pan-alpha-zsh"
	a.Status.Phase = metadata.PhaseOffline
	if process {
		p, _ := reg.Pane(a.Status.PaneRef)
		p.Spec.Runtime.Kind = metadata.RuntimeProcess
		p.Metadata.OwnerRef = &metadata.OwnerRef{Kind: metadata.KindAgent, UID: a.Metadata.UID}
		a.Status.Phase = metadata.PhaseRunning
	}
	path := store.PathFor(state)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(reg)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := projmux.NewClient(projmux.Options{StateDir: state, ConfigDir: configDir})
	if err != nil {
		t.Fatal(err)
	}
	st := agentquestion.NewStore(state)
	record, err := st.Create(agentquestion.Record{ID: "question-0000000000000001", AgentUID: a.Metadata.UID, PaneUID: a.Status.PaneRef, Questions: json.RawMessage(questionTestQuestions), Deadline: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	cmd := &agentCommand{loadRegistry: func() (metadata.Registry, error) { return reg, nil }, questionStore: func() (*agentquestion.Store, error) { return st, nil }, questionAnswering: func() config.AgentQuestionAnswering { return config.AgentQuestionAnsweringClaude }, now: time.Now}
	return sdkQuestionFixture{reg, c, cmd, st, state, record}
}
func TestQuestionSDKStoredListParity(t *testing.T) {
	f := newSDKQuestionFixture(t, false)
	for _, policy := range []config.AgentQuestionAnswering{config.AgentQuestionAnsweringClaude, config.AgentQuestionAnsweringProjmux} {
		f.cmd.questionAnswering = func() config.AgentQuestionAnswering { return policy }
		// Capture an explicit config root for the same policy.
		configDir := t.TempDir()
		paths := config.DefaultPaths(configDir, "")
		paths.ConfigDir = configDir
		if err := os.MkdirAll(configDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(paths.AgentQuestionAnsweringFile(), []byte(string(policy)+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		c, err := projmux.NewClient(projmux.Options{StateDir: f.state, ConfigDir: configDir})
		if err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		err = f.cmd.runQuestion([]string{"list", "reader", "-o", "json"}, &out, &stderr)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.ListQuestions(context.Background(), "reader")
		if err != nil {
			t.Fatal(err)
		}
		if questionJSON(t, got) != out.String() {
			t.Fatalf("list differs:\n%s\n%s", questionJSON(t, got), out.String())
		}
		got.Questions[0].Prompts[0].Question = "mutated"
		again, _ := c.ListQuestions(context.Background(), "reader")
		if again.Questions[0].Prompts[0].Question == "mutated" {
			t.Fatal("shared result")
		}
	}
}
func TestQuestionSDKStoredAnswerParity(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		sel  []core.QuestionSelection
	}{
		{"option", []string{"--option", "1=just", "--option", "2=dev"}, []core.QuestionSelection{{Question: 1, Option: ptr("just")}, {Question: 2, Option: ptr("dev")}}},
		{"index", []string{"--index", "1=3", "--index", "2=2"}, []core.QuestionSelection{{Question: 1, Index: ptr(3)}, {Question: 2, Index: ptr(2)}}},
		{"text", []string{"--text", "1=a=b <value>", "--text", "2=custom"}, []core.QuestionSelection{{Question: 1, Text: ptr("a=b <value>")}, {Question: 2, Text: ptr("custom")}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cli := newSDKQuestionFixture(t, false)
			sdk := newSDKQuestionFixture(t, false)
			var out, stderr bytes.Buffer
			args := append([]string{"answer", "reader", cli.record.ID}, test.args...)
			if err := cli.cmd.runQuestion(args, &out, &stderr); err != nil {
				t.Fatal(err)
			}
			result, err := sdk.client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "reader", QuestionID: sdk.record.ID, Selections: test.sel})
			if err != nil {
				t.Fatal(err)
			}
			a, _, _ := cli.store.Get(cli.record.ID)
			b, _, _ := sdk.store.Get(sdk.record.ID)
			if !reflect.DeepEqual(a.Answers, b.Answers) || a.State != b.State || result.QuestionID != b.ID || out.String() != b.ID+" answered for agent/reader\n" {
				t.Fatalf("answer mismatch %+v %+v %s", a, b, out.String())
			}
		})
	}
}
func TestQuestionSDKRefusalParity(t *testing.T) {
	for _, test := range []struct {
		name  string
		code  projmux.Code
		setup func(*sdkQuestionFixture)
		id    string
		args  []string
		sel   []core.QuestionSelection
	}{
		{name: "missing", code: projmux.CodeQuestionNotFound, id: "question-0000000000000002"},
		{name: "malformed", code: projmux.CodeQuestionNotFound, id: "bad"},
		{name: "answered", code: projmux.CodeQuestionNotPending, setup: func(f *sdkQuestionFixture) {
			_, err := f.store.Answer(f.record.ID, f.record.AgentUID, map[string]string{"Which build tool?": "just", "Which branch?": "dev"})
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "closed", code: projmux.CodeQuestionClosed, setup: func(f *sdkQuestionFixture) { _, _ = f.store.Close(f.record.ID, agentquestion.CloseReasonHookCanceled) }},
		{name: "invalid option", code: projmux.CodeQuestionInvalidAnswer, args: []string{"--option", "1=bad", "--option", "2=dev"}, sel: []core.QuestionSelection{{Question: 1, Option: ptr("bad")}, {Question: 2, Option: ptr("dev")}}},
		{name: "invalid index", code: projmux.CodeQuestionInvalidAnswer, args: []string{"--index", "1=99"}, sel: []core.QuestionSelection{{Question: 1, Index: ptr(99)}}},
		{name: "provider", code: projmux.CodeQuestionProviderUnsupported, setup: func(f *sdkQuestionFixture) {
			f.reg.Agents[0].Spec.Provider = "antigravity"
			raw, _ := json.Marshal(f.reg)
			_ = os.WriteFile(store.PathFor(f.state), raw, 0600)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newSDKQuestionFixture(t, false)
			if test.setup != nil {
				test.setup(&f)
			}
			id := test.id
			if id == "" {
				id = f.record.ID
			}
			sel := test.sel
			args := test.args
			if sel == nil {
				sel = []core.QuestionSelection{{Question: 1, Option: ptr("just")}, {Question: 2, Option: ptr("dev")}}
				args = []string{"--option", "1=just", "--option", "2=dev"}
			}
			var out, stderr bytes.Buffer
			cliErr := f.cmd.runQuestion(append([]string{"answer", "reader", id}, args...), &out, &stderr)
			_, sdkErr := f.client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "reader", QuestionID: id, Selections: sel})
			var refusal *projmux.Refusal
			if !errors.As(sdkErr, &refusal) || refusal.Code != test.code || cliErr == nil || (test.name != "invalid index" && cliErr.Error() != sdkErr.Error() || test.name == "invalid index" && sdkErr.Error() != "AnswerQuestionRequest.Selections: Index for Question 1 must be between 1 and 3") || exitCodeOf(cliErr) != 2 || out.Len() != 0 {
				t.Fatalf("CLI %v SDK %v refusal %+v", cliErr, sdkErr, refusal)
			}
		})
	}
}

type questionDispatchStub struct {
	views      []core.ExactQuestion
	uid, id    string
	selections []core.QuestionSelection
	calls      int
	answerErr  error
	listErr    error
}

func (d *questionDispatchStub) ListQuestions(ctx context.Context, uid string) ([]core.ExactQuestion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.calls++
	d.uid = uid
	return d.views, d.listErr
}
func (d *questionDispatchStub) AnswerQuestion(ctx context.Context, uid, id string, s []core.QuestionSelection) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.uid = uid
	d.id = id
	d.selections = s
	return d.answerErr
}

type internalQuestionStub struct{ stub *questionDispatchStub }

func (d internalQuestionStub) ReadExactProcessQuestions(ctx context.Context, uid string) ([]processquestions.ExactQuestion, error) {
	v, err := d.stub.ListQuestions(ctx, uid)
	raw, _ := json.Marshal(v)
	var out []processquestions.ExactQuestion
	_ = json.Unmarshal(raw, &out)
	return out, err
}
func (d internalQuestionStub) AnswerExactProcessQuestion(ctx context.Context, uid, id string, s map[int]agentquestion.Selection) error {
	var out []core.QuestionSelection
	for i := range 16 {
		v, ok := s[i]
		if !ok {
			continue
		}
		for _, label := range v.Labels {
			out = append(out, core.QuestionSelection{Question: i + 1, Option: ptr(label)})
		}
		if v.HasText {
			out = append(out, core.QuestionSelection{Question: i + 1, Text: ptr(v.Text)})
		}
	}
	return d.stub.AnswerQuestion(ctx, uid, id, out)
}
func TestQuestionSDKProcessParity(t *testing.T) {
	for _, mode := range []string{"option", "index", "text", "secret", "not-pending", "missing", "host-invalid", "host-invalid-wrapped"} {
		t.Run(mode, func(t *testing.T) {
			f := newSDKQuestionFixture(t, true)
			view := core.ExactQuestion{QuestionID: f.record.ID, Policy: "native", Delivery: "process-exact-question", CanAnswer: true, State: core.QuestionWaiting, Prompts: []core.ProviderQuestion{{ID: "p", Question: "A < B?", IsOther: true, Options: []core.ProviderQuestionOption{{Label: "yes"}, {Label: "no"}}}}}
			if mode == "secret" {
				view.Prompts[0].IsSecret = true
			}
			if mode == "not-pending" {
				view.CanAnswer = false
			}
			cliStub := &questionDispatchStub{views: []core.ExactQuestion{view}}
			sdkStub := &questionDispatchStub{views: []core.ExactQuestion{view}}
			if mode == "host-invalid" || mode == "host-invalid-wrapped" {
				cliStub.answerErr = agentquestion.ErrInvalidAnswer
				if mode == "host-invalid-wrapped" {
					cliStub.answerErr = fmt.Errorf("host: %w", agentquestion.ErrInvalidAnswer)
				}
				sdkStub.answerErr = cliStub.answerErr
			}
			if mode == "missing" {
				cliStub.views = nil
				sdkStub.views = nil
			}
			f.cmd.questionDispatcher = internalQuestionStub{cliStub}
			c, err := projmux.NewClient(projmux.Options{StateDir: f.state, ConfigDir: t.TempDir(), QuestionDispatcher: sdkStub})
			if err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			if err = f.cmd.runQuestion([]string{"list", "reader", "-o", "json"}, &out, &stderr); err != nil {
				t.Fatal(err)
			}
			result, err := c.ListQuestions(context.Background(), "reader")
			if err != nil {
				t.Fatal(err)
			}
			if questionJSON(t, result) != out.String() {
				t.Fatalf("process list differs %s vs %s", questionJSON(t, result), out.String())
			}
			args := []string{"--option", "1=yes"}
			selection := core.QuestionSelection{Question: 1, Option: ptr("yes")}
			if mode == "index" {
				args = []string{"--index", "1=2"}
				selection = core.QuestionSelection{Question: 1, Index: ptr(2)}
			}
			if mode == "text" {
				args = []string{"--text", "1=hello"}
				selection = core.QuestionSelection{Question: 1, Text: ptr("hello")}
			}
			out.Reset()
			cliErr := f.cmd.runQuestion(append([]string{"answer", "reader", view.QuestionID}, args...), &out, &stderr)
			_, sdkErr := c.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "reader", QuestionID: view.QuestionID, Selections: []core.QuestionSelection{selection}})
			if cliErr != nil || sdkErr != nil {
				var refusal *projmux.Refusal
				wantExit := 2
				if cliStub.answerErr != nil {
					wantExit = 1
				}
				if cliErr == nil || !errors.As(sdkErr, &refusal) || cliErr.Error() != sdkErr.Error() || exitCodeOf(cliErr) != wantExit || (cliStub.answerErr != nil && refusal.Code != projmux.CodeQuestionInvalidAnswer) {
					t.Fatalf("CLI %v SDK %v", cliErr, sdkErr)
				}
			} else if cliStub.id != sdkStub.id || cliStub.uid != sdkStub.uid || !reflect.DeepEqual(cliStub.selections, sdkStub.selections) {
				t.Fatal("dispatcher calls differ")
			}
			before, _, _ := f.store.Get(f.record.ID)
			if before.State != agentquestion.StateWaiting {
				t.Fatal("process used store answer")
			}
			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			calls := sdkStub.calls
			_, err = c.AnswerQuestion(canceled, projmux.AnswerQuestionRequest{Agent: "reader", QuestionID: view.QuestionID, Selections: []core.QuestionSelection{selection}})
			if !errors.Is(err, context.Canceled) || sdkStub.calls != calls {
				t.Fatal("canceled request dispatched")
			}
		})
	}
}

func TestQuestionSDKTerminalAndSecretRefusals(t *testing.T) {
	for _, mode := range []string{"expired", "answered-elsewhere", "secret", "empty-text", "wrong-owner"} {
		t.Run(mode, func(t *testing.T) {
			f := newSDKQuestionFixture(t, false)
			id := f.record.ID
			if mode == "expired" {
				past := time.Now().Add(-2 * time.Hour)
				record, err := f.store.WithClock(func() time.Time { return past }).Create(agentquestion.Record{ID: "question-0000000000000002", AgentUID: f.record.AgentUID, Questions: json.RawMessage(questionTestQuestions), Deadline: past.Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				id = record.ID
			}
			if mode == "secret" || mode == "answered-elsewhere" {
				raw := json.RawMessage(`[{"id":"choice","question":"Choice?","options":[{"label":"yes"}],"isOther":true}]`)
				if mode == "secret" {
					raw = json.RawMessage(`[{"id":"secret","question":"Password?","options":null,"isSecret":true}]`)
				}
				record, err := f.store.Create(agentquestion.Record{ID: "question-0000000000000002", Provider: "codex", AgentUID: f.record.AgentUID, PaneUID: f.record.PaneUID, SessionID: "fixture-session", RequestID: "opaque-fixture-request", Questions: raw, Deadline: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				id = record.ID
				if mode == "answered-elsewhere" {
					if _, err = f.store.CloseAnsweredElsewhere(record.ID, record.AgentUID, record.SessionID, record.RequestID); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "wrong-owner" {
				record, err := f.store.Create(agentquestion.Record{ID: "question-0000000000000002", AgentUID: "other-agent", Questions: json.RawMessage(questionTestQuestions), Deadline: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				id = record.ID
			}
			sel := []core.QuestionSelection{{Question: 1, Option: ptr("just")}, {Question: 2, Option: ptr("dev")}}
			args := []string{"--option", "1=just", "--option", "2=dev"}
			if mode == "empty-text" {
				sel = []core.QuestionSelection{{Question: 1, Text: ptr("")}}
				args = []string{"--text", "1="}
			}
			var out, stderr bytes.Buffer
			cliErr := f.cmd.runQuestion(append([]string{"answer", "reader", id}, args...), &out, &stderr)
			_, sdkErr := f.client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "reader", QuestionID: id, Selections: sel})
			codes := map[string]projmux.Code{"expired": projmux.CodeQuestionExpired, "answered-elsewhere": projmux.CodeQuestionAnsweredElsewhere, "secret": projmux.CodeQuestionSecretNativeOnly, "empty-text": projmux.CodeQuestionInvalidAnswer, "wrong-owner": projmux.CodeQuestionNotFound}
			var refusal *projmux.Refusal
			if !errors.As(sdkErr, &refusal) || refusal.Code != codes[mode] || cliErr == nil || cliErr.Error() != sdkErr.Error() || exitCodeOf(cliErr) != 2 {
				t.Fatalf("CLI=%v SDK=%v refusal=%+v", cliErr, sdkErr, refusal)
			}
			list, err := f.client.ListQuestions(context.Background(), "reader")
			if err != nil {
				t.Fatal(err)
			}
			encoded := questionJSON(t, list)
			if bytes.Contains([]byte(encoded), []byte("opaque-fixture-request")) {
				t.Fatal("opaque provider id exposed")
			}
		})
	}
}

func TestQuestionSDKSecretRedactionAndCancellation(t *testing.T) {
	f := newSDKQuestionFixture(t, false)
	record, err := f.store.Create(agentquestion.Record{ID: "question-0000000000000002", Provider: "codex", AgentUID: f.record.AgentUID, Questions: json.RawMessage(`[{"id":"secret","question":"Password?","options":null,"isSecret":true}]`), Deadline: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.Answer(record.ID, record.AgentUID, map[string]string{"secret": `["fixture-password"]`}); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if err = f.cmd.runQuestion([]string{"list", "reader", "-o", "json"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	list, err := f.client.ListQuestions(context.Background(), "reader")
	if err != nil {
		t.Fatal(err)
	}
	if questionJSON(t, list) != out.String() || bytes.Contains(out.Bytes(), []byte("fixture-password")) {
		t.Fatal("redacted JSON differs or leaks")
	}
	before, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = f.client.AnswerQuestion(ctx, projmux.AnswerQuestionRequest{Agent: "reader", QuestionID: f.record.ID, Selections: []core.QuestionSelection{{Question: 1, Option: ptr("just")}}})
	after, _ := os.ReadFile(f.store.Path())
	if !errors.Is(err, context.Canceled) || !bytes.Equal(before, after) {
		t.Fatal("canceled answer mutated store")
	}
	other := newSDKQuestionFixture(t, false)
	if _, err = other.client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "reader", QuestionID: other.record.ID, Selections: []core.QuestionSelection{{Question: 1, Option: ptr("just")}, {Question: 2, Option: ptr("dev")}}}); err != nil {
		t.Fatal(err)
	}
	current, _, _ := f.store.Get(f.record.ID)
	if current.State != agentquestion.StateWaiting {
		t.Fatal("client roots share state")
	}
}

func TestQuestionSDKSelectionRefusals(t *testing.T) {
	f := newSDKQuestionFixture(t, false)
	for _, ref := range []string{"absent", "uid:"} {
		var out, stderr bytes.Buffer
		cliErr := f.cmd.runQuestion([]string{"list", ref, "-o", "json"}, &out, &stderr)
		_, sdkErr := f.client.ListQuestions(context.Background(), ref)
		var refusal *projmux.Refusal
		if !errors.As(sdkErr, &refusal) || cliErr == nil || cliErr.Error() != sdkErr.Error() || exitCodeOf(cliErr) != 2 {
			t.Fatalf("ref %s CLI=%v SDK=%v", ref, cliErr, sdkErr)
		}
	}
	for _, selections := range [][]core.QuestionSelection{nil, {{Question: 1}}, {{Question: 1, Option: ptr("just"), Text: ptr("hello")}}} {
		_, err := f.client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "reader", QuestionID: f.record.ID, Selections: selections})
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeQuestionInvalidAnswer {
			t.Fatal(err)
		}
	}
}

// Transport failures do not imply a question's final state and must retain
// their original error identity, text, and CLI runtime exit code.
func TestQuestionSDKProcessRuntimeErrors(t *testing.T) {
	for _, sentinel := range []error{processhost.ErrStale, context.Canceled, context.DeadlineExceeded, io.EOF, os.ErrNotExist} {
		for _, wrapped := range []bool{false, true} {
			for _, action := range []string{"list", "answer"} {
				t.Run(fmt.Sprintf("%s/wrapped=%t/%s", sentinel, wrapped, action), func(t *testing.T) {
					f := newSDKQuestionFixture(t, true)
					hostErr := sentinel
					if wrapped {
						hostErr = fmt.Errorf("host: %w", sentinel)
					}
					view := core.ExactQuestion{QuestionID: f.record.ID, CanAnswer: true, State: core.QuestionWaiting, Prompts: []core.ProviderQuestion{{ID: "p", Question: "Choose", Options: []core.ProviderQuestionOption{{Label: "yes"}}}}}
					stub := &questionDispatchStub{views: []core.ExactQuestion{view}}
					if action == "list" {
						stub.listErr = hostErr
					} else {
						stub.answerErr = hostErr
					}
					f.cmd.questionDispatcher = internalQuestionStub{stub}
					client, err := projmux.NewClient(projmux.Options{StateDir: f.state, ConfigDir: t.TempDir(), QuestionDispatcher: stub})
					if err != nil {
						t.Fatal(err)
					}
					var out, stderr bytes.Buffer
					args := []string{action, "reader"}
					var sdkErr error
					if action == "answer" {
						args = append(args, view.QuestionID, "--option", "1=yes")
						_, sdkErr = client.AnswerQuestion(context.Background(), projmux.AnswerQuestionRequest{Agent: "reader", QuestionID: view.QuestionID, Selections: []core.QuestionSelection{{Question: 1, Option: ptr("yes")}}})
					} else {
						args = append(args, "-o", "json")
						_, sdkErr = client.ListQuestions(context.Background(), "reader")
					}
					cliErr := f.cmd.runQuestion(args, &out, &stderr)
					var refusal *projmux.Refusal
					if !errors.Is(cliErr, sentinel) || !errors.Is(sdkErr, sentinel) || errors.As(sdkErr, &refusal) || cliErr.Error() != sdkErr.Error() || exitCodeOf(cliErr) != 1 || out.Len() != 0 {
						t.Fatalf("CLI %v SDK %v refusal %+v", cliErr, sdkErr, refusal)
					}
				})
			}
		}
	}
}
