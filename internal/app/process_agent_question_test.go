package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestExactQuestionProductionConstructors(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, annotation := range []string{"", coremetadata.QuestionChannelOn, "off"} {
			for _, mode := range []config.AgentQuestionAnswering{config.AgentQuestionAnsweringClaude, config.AgentQuestionAnsweringProjmux} {
				t.Run(provider+"/"+annotation+"/"+string(mode), func(t *testing.T) {
					var binding processhost.Binding
					var path, root string
					var result processAgentCreateResult
					var start func()
					var wait func()
					if provider == aiModeClaude {
						f := newProcessClaudeFixture(t, nil)
						binding, path, root = f.binding, f.path, f.root
						result = processAgentCreateResult{Binding: binding, Handle: f.handle, Provider: provider, registryPath: path}
						start = func() { f.turn(t, "first", "question") }
						wait = func() { f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 1 }) }
					}
					if provider == aiModeCodex {
						f := newProcessCodexFixture(t, nil)
						binding, path, root = f.endpoint.binding, f.path, f.root
						result = processAgentCreateResult{Binding: binding, Provider: provider, Handle: f.endpoint.handle, codexEndpoint: f.endpoint, registryPath: path}
						start = func() { f.turn(t, "first", "controls") }
						wait = func() { f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 }) }
					}
					d := ProcessQuestionDispatcher{RegistryPath: path}
					paths := config.DefaultPaths(filepath.Join(root, "policy"), filepath.Dir(filepath.Dir(path)))
					if err := config.SaveAgentQuestionAnsweringFile(paths.AgentQuestionAnsweringFile(), mode); err != nil {
						t.Fatal(err)
					}
					command := &createCommand{homeDir: func() (string, error) { return root, nil }, lookupEnv: func(key string) string {
						if key == "XDG_CONFIG_HOME" {
							return filepath.Dir(paths.ConfigDir)
						}
						return ""
					}}
					if _, err := command.newProcessCreateControl(result); err != nil {
						t.Fatal(err)
					}
					// The annotation is deliberately unrelated to the global setting.
					store := intmetadata.NewStore(path)
					if _, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error {
						a, _ := reg.Agent(binding.Agent)
						if a.Metadata.Annotations == nil {
							a.Metadata.Annotations = map[string]string{}
						}
						if annotation == "" {
							delete(a.Metadata.Annotations, coremetadata.AnnotationAgentQuestionChannel)
						} else {
							a.Metadata.Annotations[coremetadata.AnnotationAgentQuestionChannel] = annotation
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					start()
					wait()
					snapshot, err := result.Handle.Observe(binding)
					if err != nil {
						t.Fatal(err)
					}
					if err = result.recordProcessSnapshot(snapshot); err != nil {
						t.Fatal(err)
					}
					views, err := d.ReadExactProcessQuestions(context.Background(), binding.Agent)
					if err != nil || len(views) != 1 {
						t.Fatal(views, err)
					}
					v := views[0]
					want := "process-exact-question"
					if mode == config.AgentQuestionAnsweringProjmux {
						want = "held-question"
					}
					if v.Delivery != want || (v.Deadline == nil) != (want == "process-exact-question") {
						t.Fatal(v)
					}
					changed := config.AgentQuestionAnsweringProjmux
					if mode == changed {
						changed = config.AgentQuestionAnsweringClaude
					}
					if err = config.SaveAgentQuestionAnsweringFile(paths.AgentQuestionAnsweringFile(), changed); err != nil {
						t.Fatal(err)
					}
					same, err := d.ReadExactProcessQuestions(context.Background(), binding.Agent)
					if err != nil || len(same) != 1 || !reflect.DeepEqual(v, same[0]) {
						t.Fatal("pending changed", err)
					}
					for _, id := range []string{"unknown", v.QuestionID + "-stale"} {
						if err = d.AnswerExactProcessQuestion(context.Background(), binding.Agent, id, map[int]agentquestion.Selection{0: {Labels: []string{"blue"}}}); err == nil {
							t.Fatal("unknown accepted")
						}
					}
					original, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					for _, field := range []string{"session", "connection"} {
						if _, _, err = store.UpdateConvergent(func(reg *coremetadata.Registry) error {
							p, _ := reg.Pane(binding.Pane)
							if field == "session" {
								if provider == aiModeClaude {
									p.Status.ProcessSession.SessionID += "-stale"
								} else {
									p.Status.ProcessSession.ThreadID += "-stale"
								}
								for i := range p.Status.ProcessSession.Pending {
									p.Status.ProcessSession.Pending[i].SessionID += "-stale"
								}
							} else {
								p.Status.ProcessSession.ConnectionID += "-stale"
								for i := range p.Status.ProcessSession.Pending {
									p.Status.ProcessSession.Pending[i].ConnectionID += "-stale"
								}
							}
							return nil
						}); err != nil {
							t.Fatal(err)
						}
						if err = d.AnswerExactProcessQuestion(context.Background(), binding.Agent, v.QuestionID, map[int]agentquestion.Selection{0: {Labels: []string{"blue"}}}); !errors.Is(err, processhost.ErrStale) {
							t.Fatal("stale conversation accepted", err)
						}
						if err = os.WriteFile(path, original, 0600); err != nil {
							t.Fatal(err)
						}
					}
					callback, _ := processQuestionCallbacks.Load(binding)
					for _, field := range []string{"pane", "generation", "operation"} {
						bad := binding
						if field == "pane" {
							bad.Pane += "-stale"
						}
						if field == "generation" {
							bad.Generation += "-stale"
						}
						if field == "operation" {
							bad.Operation += "-stale"
						}
						if _, err = callback.(processQuestionCallback)(context.Background(), processQuestionRequest{Binding: bad, QuestionID: v.QuestionID, Selections: map[int]agentquestion.Selection{0: {Labels: []string{"blue"}}}}); !errors.Is(err, processhost.ErrStale) {
							t.Fatal("stale binding accepted", err)
						}
					}

					reg, _ := store.LoadReadOnly()
					pane, _ := reg.Pane(binding.Pane)
					activation := pane.Status.Activation.Process
					socket := processHostSocket(provider, path, binding.Pane, binding.Generation)
					inode, err := localipc.InspectOwnedSocket(socket)
					if err != nil {
						t.Fatal(err)
					}
					q := &processQuestionRequest{Binding: binding, QuestionID: v.QuestionID, Selections: map[int]agentquestion.Selection{0: {Labels: []string{"blue"}}}}
					var mixed []any
					if provider == aiModeClaude {
						mixed = []any{claudeProcessCheck{Questions: q, Observe: &binding}, claudeProcessCheck{Questions: q, Foreground: &processForegroundRequest{Action: "respond"}}, claudeProcessCheck{Questions: q, Register: true}}
					} else {
						mixed = []any{codexProcessExchange{Questions: q, Observe: &binding}, codexProcessExchange{Questions: q, Foreground: &processForegroundRequest{Action: "approval"}}, codexProcessExchange{Questions: q, MessageRef: "mixed"}}
					}
					for _, request := range mixed {
						result, err := callProcessForeground(context.Background(), socket, inode, activation.HostProcess, request)
						if err == nil && result.Accepted {
							t.Fatal("mixed admitted")
						}
					}
					unchanged, err := d.ReadExactProcessQuestions(context.Background(), binding.Agent)
					if err != nil || len(unchanged) != 1 || !reflect.DeepEqual(v, unchanged[0]) {
						t.Fatal("rejected request changed pending", err)
					}
					if err = d.AnswerExactProcessQuestion(context.Background(), binding.Agent, v.QuestionID, map[int]agentquestion.Selection{9: {Text: "invalid", HasText: true}}); !errors.Is(err, agentquestion.ErrInvalidAnswer) {
						t.Fatal("invalid typed answer", err)
					}
					if err = d.AnswerExactProcessQuestion(context.Background(), binding.Agent, v.QuestionID, map[int]agentquestion.Selection{0: {Labels: []string{"blue"}}}); err != nil {
						t.Fatal(err)
					}
					if err = d.AnswerExactProcessQuestion(context.Background(), binding.Agent, v.QuestionID, map[int]agentquestion.Selection{0: {Labels: []string{"red"}}}); err == nil {
						t.Fatal("duplicate accepted")
					}
				})
			}
		}
	}
}

func TestExactQuestionDTOHasNoProviderAuthority(t *testing.T) {
	request := processhost.Request{Kind: "question", Input: json.RawMessage(`{"questions":[{"question":"Color?","options":[{"label":"blue"},{"label":"red"}]}]}`)}
	views, err := exactQuestionViews(aiModeClaude, map[string]processhost.Request{"opaque": request}, map[string]bool{"opaque": true}, nil)
	if err != nil || len(views) != 1 || views[0].Deadline != nil {
		t.Fatal(views, err)
	}
	raw, _ := json.Marshal(views)
	var fields []map[string]any
	_ = json.Unmarshal(raw, &fields)
	for _, key := range []string{"Input", "Token", "Authority", "Request", "Selections", "deadline"} {
		if _, ok := fields[0][key]; ok {
			t.Fatal("private data projected", key)
		}
	}
}

func TestExactQuestionHeldExpiryCannotWrite(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	now := time.Now()
	f.control.questions = f.control.questions.WithClock(func() time.Time { return now })
	f.control.now = func() time.Time { return now }
	f.turn(t, "expiry", "controls")
	f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
	if err := f.control.syncControls(context.Background()); err != nil {
		t.Fatal(err)
	}
	views, err := f.control.exactQuestions(context.Background(), processQuestionRequest{Binding: f.endpoint.binding})
	if err != nil || len(views) != 1 {
		t.Fatal(views, err)
	}
	now = now.Add(2 * time.Minute)
	if _, err = f.control.exactQuestions(context.Background(), processQuestionRequest{Binding: f.endpoint.binding, QuestionID: views[0].QuestionID, Selections: map[int]agentquestion.Selection{0: {Labels: []string{"blue"}}}}); err == nil {
		t.Fatal("expired answer admitted")
	}
	for _, n := range f.wire(t) {
		if len(n["result"]) > 0 {
			t.Fatal("expired response reached wire")
		}
	}
}

func TestOfflineProcessQuestionHistoryRemainsReadable(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	b := f.endpoint.binding
	f.turn(t, "offline-history", "controls")
	snap := f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
	result := processAgentCreateResult{Binding: b, Provider: aiModeCodex, Handle: f.endpoint.handle, codexEndpoint: f.endpoint, registryPath: f.path}
	if err := result.recordProcessSnapshot(snap); err != nil {
		t.Fatal(err)
	}
	if err := f.control.syncControls(context.Background()); err != nil {
		t.Fatal(err)
	}
	views, err := f.control.exactQuestions(context.Background(), processQuestionRequest{Binding: b})
	if err != nil || len(views) != 1 {
		t.Fatal(views, err)
	}
	id := views[0].QuestionID
	record, found, err := f.control.questions.Get(id)
	if err != nil || !found {
		t.Fatal(record, found, err)
	}
	if _, err = f.control.questions.Close(id, agentquestion.CloseReasonHookCanceled); err != nil {
		t.Fatal(err)
	}
	record.ID, record.Disposition = "question-0000000000000001", ""
	if _, err = f.control.questions.Create(record); err != nil {
		t.Fatal(err)
	}
	if err = f.endpoint.handle.Stop(b); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snap, err = f.endpoint.handle.Wait(ctx, b)
	if err != nil || snap.Exit == nil {
		t.Fatal(snap, err)
	}
	if err = result.persistProcessWait(snap); err != nil {
		t.Fatal(err)
	}
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent(b.Agent)
	if agent.Status.Phase != coremetadata.PhaseOffline {
		t.Fatal(agent.Status.Phase)
	}
	command := &agentCommand{loadRegistry: f.store.LoadDegradedReadOnly, messagePaths: agentMessagePaths{registryPath: f.path}, questionStore: func() (*agentquestion.Store, error) { return f.control.questions, nil }}
	for _, asJSON := range []bool{false, true} {
		var output bytes.Buffer
		if err = command.listQuestions(agentQuestionRequest{spelling: "agent question list", json: asJSON}, *agent, &output); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), id) {
			t.Fatal(output.String())
		}
		if asJSON {
			var listed agentQuestionList
			if err = json.Unmarshal(output.Bytes(), &listed); err != nil || len(listed.Questions) != 2 || len(listed.ExactQuestions) != 0 {
				t.Fatal(output.String(), err)
			}
			for _, q := range listed.Questions {
				if q.CanAnswer {
					t.Fatal("offline question admitted", q.ID)
				}
			}
		}
	}
	d := ProcessQuestionDispatcher{RegistryPath: f.path}
	if _, err = d.ReadExactProcessQuestions(ctx, b.Agent); !errors.Is(err, processhost.ErrStale) {
		t.Fatal(err)
	}
	if err = d.AnswerExactProcessQuestion(ctx, b.Agent, id, map[int]agentquestion.Selection{0: {Labels: []string{"blue"}}}); !errors.Is(err, processhost.ErrStale) {
		t.Fatal(err)
	}
	for _, row := range f.wire(t) {
		if len(row["result"]) > 0 {
			t.Fatal("retired answer reached wire")
		}
	}
}

func TestHeldDirectAdmissionSurvivesDeadlineCrossing(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	b := f.endpoint.binding
	f.turn(t, "crossing", "controls")
	f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
	if err := f.control.syncControls(context.Background()); err != nil {
		t.Fatal(err)
	}
	views, err := f.control.exactQuestions(context.Background(), processQuestionRequest{Binding: b})
	if err != nil || len(views) != 1 || views[0].Deadline == nil {
		t.Fatal(views, err)
	}
	deadline := *views[0].Deadline
	calls := 0
	f.control.questions = f.control.questions.WithClock(func() time.Time {
		calls++
		if calls <= 2 {
			return deadline.Add(-time.Nanosecond)
		}
		return deadline.Add(time.Nanosecond)
	})
	_, err = f.control.exactQuestions(context.Background(), processQuestionRequest{Binding: b, QuestionID: views[0].QuestionID, Selections: map[int]agentquestion.Selection{0: {Labels: []string{"blue"}}}})
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := f.control.questions.Get(views[0].QuestionID)
	if err != nil || !found || record.State != agentquestion.StateClosed || record.Disposition != string(agentquestion.CloseReasonAnsweredDirect) || len(record.Answers) != 0 {
		t.Fatal(record, found, err)
	}
	wrote := false
	f.wait(t, func(processhost.Snapshot) bool {
		for _, row := range f.pollWire(t) {
			if bytes.Contains(row["result"], []byte("blue")) {
				wrote = true
			}
		}
		return wrote
	})
	if _, err = f.control.exactQuestions(context.Background(), processQuestionRequest{Binding: b, QuestionID: views[0].QuestionID, Selections: map[int]agentquestion.Selection{0: {Labels: []string{"blue"}}}}); err == nil {
		t.Fatal("duplicate admission")
	}
}

func TestExactQuestionErrorPreservesCapturedMaps(t *testing.T) {
	for _, native := range []bool{false, true} {
		f := newProcessCodexFixture(t, nil)
		f.control.questionAnswering = func() config.AgentQuestionAnswering {
			if native {
				return config.AgentQuestionAnsweringClaude
			}
			return config.AgentQuestionAnsweringProjmux
		}
		b := f.endpoint.binding
		f.turn(t, "error-maps", "controls")
		f.wait(t, func(s processhost.Snapshot) bool { return len(s.Pending) == 2 })
		views, err := f.control.exactQuestions(context.Background(), processQuestionRequest{Binding: b})
		if err != nil || len(views) != 1 {
			t.Fatal(views, err)
		}
		id := views[0].QuestionID
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if native {
			cancel()
		} else {
			calls := 0
			f.control.questions = f.control.questions.WithClock(func() time.Time {
				calls++
				if calls == 2 {
					cancel()
				}
				return time.Now()
			})
		}
		_, err = f.control.exactQuestions(ctx, processQuestionRequest{Binding: b, QuestionID: id, Selections: map[int]agentquestion.Selection{0: {Labels: []string{"blue"}}}})
		if err == nil {
			t.Fatal("canceled write accepted")
		}
		if _, ok := f.control.records[id]; !ok || f.control.nativeQuestions[id] != native {
			t.Fatal("error recaptured policy or deleted request")
		}
		if !native {
			record, found, err := f.control.questions.Get(id)
			if err != nil || !found || record.Disposition != "direct-answer-uncertain" || len(record.Answers) != 0 {
				t.Fatal(record, found, err)
			}
		}
	}
}
