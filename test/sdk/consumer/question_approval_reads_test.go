package consumer_test

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

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

type readControlSpy struct{ calls int }

func (s *readControlSpy) ListApprovals(context.Context, string) ([]core.PendingApproval, error) {
	s.calls++
	return []core.PendingApproval{}, nil
}
func (s *readControlSpy) ReviewApproval(context.Context, string, string, core.OfferedDecision) error {
	panic("read wrote approval")
}

type readQuestionSpy struct {
	calls int
	read  func(context.Context) ([]core.ExactQuestion, error)
}

func (s *readQuestionSpy) ListQuestions(ctx context.Context, _ string) ([]core.ExactQuestion, error) {
	s.calls++
	if s.read != nil {
		return s.read(ctx)
	}
	return []core.ExactQuestion{{QuestionID: "native", ItemID: "host-must-not-supply-item", Delivery: "native", State: core.QuestionWaiting}}, nil
}
func (s *readQuestionSpy) AnswerQuestion(context.Context, string, string, []core.QuestionSelection) error {
	panic("read answered question")
}

type readFixture struct {
	root, state, path string
	agent             core.Agent
	pane              core.Pane
	project           core.Project
	window            core.Window
	control           *readControlSpy
	questions         *readQuestionSpy
}

func newReadFixture(t *testing.T, provider string, runtime core.RuntimeKind) *readFixture {
	t.Helper()
	root := t.TempDir()
	f := &readFixture{root: root, state: filepath.Join(root, "state"), control: &readControlSpy{}, questions: &readQuestionSpy{}}
	f.path = filepath.Join(f.state, "metadata", "registry.json")
	meta := func(uid string, kind core.Kind, owner string) core.ObjectMeta {
		return core.ObjectMeta{UID: uid, Name: uid, OwnerRef: &core.OwnerRef{Kind: kind, UID: owner}}
	}
	f.project = core.Project{APIVersion: "projmux.io/v1alpha1", Kind: core.KindProject, Metadata: core.ObjectMeta{UID: "project", Name: "project"}, Spec: core.ProjectSpec{Root: root, PrimaryWindowRef: "window"}}
	f.window = core.Window{APIVersion: "projmux.io/v1alpha1", Kind: core.KindWindow, Metadata: meta("window", core.KindProject, "project"), Spec: core.WindowSpec{AnchorPaneRef: "pane"}}
	f.agent = core.Agent{APIVersion: "projmux.io/v1alpha1", Kind: core.KindAgent, Metadata: meta("agent", core.KindWindow, "window"), Spec: core.AgentSpec{Provider: provider}, Status: core.AgentStatus{Phase: core.PhaseOffline, PaneRef: "pane"}}
	f.pane = core.Pane{APIVersion: "projmux.io/v1alpha1", Kind: core.KindPane, Metadata: meta("pane", core.KindAgent, "agent"), Spec: core.PaneSpec{Role: core.PaneRoleAgent, Runtime: core.PaneRuntimeSpec{Kind: runtime}}}
	f.save(t)
	return f
}
func (f *readFixture) save(t *testing.T) {
	t.Helper()
	data, e := json.Marshal(map[string]any{"apiVersion": "projmux.io/v1alpha1", "schemaVersion": 5, "projects": []core.Project{f.project}, "windows": []core.Window{f.window}, "agents": []core.Agent{f.agent}, "panes": []core.Pane{f.pane}})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Dir(f.path), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(f.path, data, 0600); e != nil {
		t.Fatal(e)
	}
}
func (f *readFixture) client(t *testing.T) *projmux.Client {
	t.Helper()
	c, e := projmux.NewClient(projmux.Options{HomeDir: f.root, ConfigDir: filepath.Join(f.root, "config"), StateDir: f.state, SocketPath: filepath.Join(f.root, "absent.sock"), ApprovalControl: f.control, QuestionDispatcher: f.questions})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func writeReadJSON(t *testing.T, path string, value any) {
	t.Helper()
	b, e := json.Marshal(value)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
}

type readFileState struct {
	Data     string
	Mode     os.FileMode
	Modified time.Time
}

func readTree(t *testing.T, root string) map[string]readFileState {
	t.Helper()
	out := map[string]readFileState{}
	e := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		v := readFileState{Mode: info.Mode(), Modified: info.ModTime()}
		if !info.IsDir() {
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			v.Data = string(b)
		}
		out[path] = v
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func assertSameJSON(t *testing.T, a, b any) {
	t.Helper()
	left, e := json.Marshal(a)
	if e != nil {
		t.Fatal(e)
	}
	right, e := json.Marshal(b)
	if e != nil || !bytes.Equal(left, right) {
		t.Fatalf("default/zero bytes changed: %s / %s (%v)", left, right, e)
	}
}

func TestExternalQuestionSourceConsumer(t *testing.T) {
	f := newReadFixture(t, "codex", core.RuntimeProcess)
	now := time.Now().UTC()
	records := []map[string]any{
		{"id": "question-0000000000000001", "agentUID": "agent", "provider": "codex", "delivery": "async", "requestID": "opaque/item:001", "generation": "history-generation", "sessionID": "history-thread", "questions": []map[string]any{{"id": "q1", "question": "Choose", "options": []any{map[string]any{"label": "yes"}}}}, "state": "waiting", "createdAt": now, "updatedAt": now, "deadline": now.Add(time.Hour)},
		{"id": "question-0000000000000002", "agentUID": "other", "provider": "codex", "delivery": "async", "requestID": "other-agent-item", "questions": []map[string]any{{"id": "q2", "question": "Other", "options": []any{map[string]any{"label": "yes"}}}}, "state": "waiting", "createdAt": now, "updatedAt": now, "deadline": now.Add(time.Hour)},
	}
	writeReadJSON(t, filepath.Join(f.state, "agent-questions", ".async", "questions.json"), map[string]any{"version": 1, "records": records})
	c := f.client(t)
	ctx := context.Background()
	before := readTree(t, f.root)
	list, e := c.ListQuestionsWithOptions(ctx, "uid:agent", core.QuestionListOptions{IncludeSource: true})
	if e != nil || list.Provider != "codex" || list.Runtime != core.RuntimeProcess || list.Phase != core.PhaseOffline || list.Session != "" || len(list.Questions) != 1 || list.Questions[0].ItemID != "opaque/item:001" || list.Questions[0].CanAnswer {
		t.Fatalf("source consumer %+v %v", list, e)
	}
	if list.Questions[0].State != core.QuestionWaiting || list.Questions[0].Disposition != "" {
		t.Fatal("opt-in closed an unanswered saved history", list)
	}
	old, e := c.ListQuestions(ctx, "uid:agent")
	if e != nil {
		t.Fatal(e)
	}
	zero, e := c.ListQuestionsWithOptions(ctx, "uid:agent", core.QuestionListOptions{})
	if e != nil {
		t.Fatal(e)
	}
	assertSameJSON(t, old, zero)
	if old.Questions[0].State != core.QuestionClosed || old.Questions[0].Disposition != "process-stale" {
		t.Fatal("default/zero stale projection changed", old, zero)
	}
	if old.Provider != "" || old.Runtime != "" || old.Phase != "" || old.Session != "" || old.Questions[0].ItemID != "" {
		t.Fatal("default populated source metadata", old)
	}
	if f.questions.calls != 0 || !reflect.DeepEqual(before, readTree(t, f.root)) {
		t.Fatal("stopped source read had effects")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := c.ListQuestionsWithOptions(canceled, "uid:agent", core.QuestionListOptions{IncludeSource: true}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	_, oldErr := c.ListQuestions(ctx, "uid:missing")
	_, zeroErr := c.ListQuestionsWithOptions(ctx, "uid:missing", core.QuestionListOptions{})
	if oldErr.Error() != zeroErr.Error() {
		t.Fatal("zero error changed", oldErr, zeroErr)
	}
}

func TestExternalFileOnlyApprovalConsumer(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "antigravity"} {
		for _, runtime := range []core.RuntimeKind{core.RuntimeTmux, core.RuntimeProcess} {
			t.Run(provider+"/"+string(runtime), func(t *testing.T) {
				f := newReadFixture(t, provider, runtime)
				now := time.Now().UTC()
				path := filepath.Join(f.state, "agent-approvals", "requests.json")
				record := map[string]any{"id": "permission-0000000000000001", "agentUID": "agent", "toolName": "Bash", "toolInput": map[string]any{"command": "display fixture"}, "state": "waiting", "createdAt": now, "updatedAt": now, "deadline": now.Add(time.Hour)}
				writeReadJSON(t, path, map[string]any{"version": 1, "records": []any{record}})
				commands := filepath.Join(f.root, "command-spies")
				if e := os.MkdirAll(commands, 0700); e != nil {
					t.Fatal(e)
				}
				log := filepath.Join(f.root, "forbidden-command.log")
				for _, name := range []string{"tmux", "claude", "codex"} {
					if e := os.WriteFile(filepath.Join(commands, name), []byte("#!/bin/sh\nprintf called >> \"$READ_COMMAND_LOG\"\nexit 42\n"), 0700); e != nil {
						t.Fatal(e)
					}
				}
				t.Setenv("READ_COMMAND_LOG", log)
				t.Setenv("PATH", commands+string(os.PathListSeparator)+os.Getenv("PATH"))
				c := f.client(t)
				before := readTree(t, f.root)
				list, e := c.ListApprovalsWithOptions(context.Background(), "uid:agent", core.ApprovalListOptions{FileOnly: true})
				mode := core.ApprovalReadModeSingleRead
				if provider == "claude" {
					mode = core.ApprovalReadModeStored
				}
				if e != nil || list.Provider != provider || list.AgentUID != "agent" || list.ReadMode != mode || list.Requests == nil {
					t.Fatal(list, e)
				}
				if provider == "claude" {
					if len(list.Requests) != 1 || string(list.Requests[0].ToolInput) != `{"command":"display fixture"}` {
						t.Fatal(list)
					}
				} else if len(list.Requests) != 0 {
					t.Fatal("single read masqueraded as saved Claude", list)
				}
				if f.control.calls != 0 || f.questions.calls != 0 || !reflect.DeepEqual(before, readTree(t, f.root)) {
					t.Fatal("file-only caused RPC, lock or file effect")
				}
				// The resource observer is the positive control for the command
				// trap: file-only must never enter that observation path.
				_, _ = c.GetResource(context.Background(), core.Ref{Kind: core.KindAgent, Value: "uid:agent"})
				called, e := os.ReadFile(log)
				if e != nil || !strings.Contains(string(called), "called") {
					t.Fatal("runtime command trap was not exercised", e)
				}
				old, oldErr := c.ListApprovals(context.Background(), "uid:agent")
				zero, zeroErr := c.ListApprovalsWithOptions(context.Background(), "uid:agent", core.ApprovalListOptions{})
				if (oldErr == nil) != (zeroErr == nil) {
					t.Fatal(oldErr, zeroErr)
				}
				if oldErr != nil {
					if oldErr.Error() != zeroErr.Error() {
						t.Fatal(oldErr, zeroErr)
					}
				} else {
					assertSameJSON(t, old, zero)
					if old.ReadMode != "" {
						t.Fatal(old)
					}
				}
				if provider == "codex" && runtime == core.RuntimeTmux && f.control.calls != 2 {
					t.Fatal("positive default live control spy", f.control.calls)
				}
			})
		}
	}
}

func (f *readFixture) running(t *testing.T) {
	t.Helper()
	b := core.ProcessBinding{HostInstanceID: "host", ProjectUID: "project", WindowUID: "window", AgentUID: "agent", PaneUID: "pane", Generation: "generation", OperationID: "operation"}
	f.agent.Status.Phase = core.PhaseRunning
	f.agent.Status.SessionRef = &core.AgentSessionRef{Provider: f.agent.Spec.Provider, ObservedAt: time.Now().UTC()}
	record := &core.ProcessSessionRecord{Provider: f.agent.Spec.Provider, Binding: b, ConnectionID: "operation", ResumeState: core.ProcessResumeUnknown}
	if f.agent.Spec.Provider == "codex" {
		record.ThreadID = "conversation"
		f.agent.Status.SessionRef.Codex = &core.CodexSessionRef{ThreadID: "conversation"}
	} else {
		record.SessionID = "conversation"
		f.agent.Status.SessionRef.Claude = &core.ClaudeSessionRef{SessionID: "conversation"}
	}
	f.pane.Status.ProcessSession = record
	f.pane.Status.Activation = core.PaneActivation{Kind: core.RuntimeProcess, Generation: b.Generation, AgentUID: b.AgentUID, OperationID: b.OperationID, Process: &core.ProcessActivation{Binding: b, HostProcess: core.ProcessIdentity{PID: 2000000000, OwnerUID: uint32(os.Getuid()), Start: "fixture-host"}, Child: core.ProcessIdentity{PID: 2000000001, OwnerUID: uint32(os.Getuid()), Start: "fixture-child"}}}
	f.save(t)
}
func TestExternalQuestionCapturedConversationAndHostIDAbsence(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := newReadFixture(t, provider, core.RuntimeProcess)
			f.running(t)
			c := f.client(t)
			source, e := c.ListQuestionsWithOptions(context.Background(), "uid:agent", core.QuestionListOptions{IncludeSource: true})
			if e != nil || source.Provider != provider || source.Session != "conversation" || source.Phase != core.PhaseRunning || len(source.ExactQuestions) != 1 || source.ExactQuestions[0].ItemID != "" {
				t.Fatal(source, e)
			}
			plain, e := c.ListQuestions(context.Background(), "uid:agent")
			if e != nil {
				t.Fatal(e)
			}
			zero, e := c.ListQuestionsWithOptions(context.Background(), "uid:agent", core.QuestionListOptions{})
			if e != nil {
				t.Fatal(e)
			}
			assertSameJSON(t, plain, zero)
			if plain.ExactQuestions[0].ItemID != "" || plain.Session != "" {
				t.Fatal("host injected default source", plain)
			}
			f.questions.read = func(context.Context) ([]core.ExactQuestion, error) {
				f.pane.Status.ProcessSession.ConnectionID = "replacement"
				f.save(t)
				return []core.ExactQuestion{{QuestionID: "native", ItemID: "untrusted-host-id"}}, nil
			}
			source, e = c.ListQuestionsWithOptions(context.Background(), "uid:agent", core.QuestionListOptions{IncludeSource: true})
			var refusal *projmux.Refusal
			if !errors.As(e, &refusal) || refusal.Code != projmux.CodeQuestionStale || source.AgentUID != "" {
				t.Fatalf("typed stale %+v %v", source, e)
			}
		})
	}
}
