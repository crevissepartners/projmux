package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/web"
	"github.com/crevissepartners/projmux/internal/web/question"
)

type questionTmux struct {
	owner  string
	screen string
	keys   []string
}

func (q *questionTmux) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) < 3 || args[0] != "-L" || args[1] != defaultAppSocket {
		return nil, errors.New("not the app socket")
	}
	switch args[2] {
	case "display-message":
		return []byte(q.owner), nil
	case "capture-pane":
		return []byte(q.screen), nil
	case "send-keys":
		q.keys = append(q.keys, strings.Join(args[5:], " "))
		return nil, nil
	}
	return nil, errors.New("unexpected " + args[2])
}

const pendingQuestion = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"AskUserQuestion","input":{"questions":[{"question":"Which colour?","multiSelect":false,"options":[{"label":"red"},{"label":"blue"}]}]}}]}}`

func questionHarness(t *testing.T, live bool) (*webBackend, *questionTmux) {
	t.Helper()
	backend, _ := webClientFixture(t, pendingQuestion)
	if live {
		backend.observe = func(context.Context, resourcegraph.Transport) resourcegraph.Inventory {
			return resourcegraph.Inventory{
				HostMode: resourcegraph.HostModeAppOwned,
				Sessions: []resourcegraph.Session{{ID: "$1", Name: "alpha"}},
				Windows:  []resourcegraph.Window{{ID: "@1", SessionID: "$1", UID: "win-alpha-main"}},
				Panes:    []resourcegraph.Pane{{ID: "%9", WindowID: "@1", UID: "pan-alpha-codex", AgentProvider: "claude"}},
			}
		}
	}
	tmux := &questionTmux{owner: "pan-alpha-codex", screen: "Which colour?\n 1. red\n 2. blue\nEnter to select"}
	saved := questionRunner
	questionRunner = tmux
	t.Cleanup(func() { questionRunner = saved })
	return backend, tmux
}

func TestWebAnswerQuestionPressesTheOptionOnTheAgentsPane(t *testing.T) {
	backend, tmux := questionHarness(t, true)
	s, err := backend.snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if runtime, err := s.livePane("pan-alpha-codex"); err != nil || runtime != "%9" {
		t.Fatalf("fixture inventory does not resolve the pane live (%v %q)", err, runtime)
	}
	handler := web.New(backend, nil).Handler()
	code, body := webSend(t, handler, "POST", "/api/v1/web/agents/agt-alpha-codex/question",
		`{"toolId":"toolu_1","answers":[{"picks":[1]}]}`)
	if code != 200 || body["ok"] != true {
		t.Fatalf("answer = %d %v", code, body)
	}
	if strings.Join(tmux.keys, "|") != "-l -- 2" {
		t.Fatalf("keys = %q", tmux.keys)
	}
}

func TestWebAnswerQuestionRefusals(t *testing.T) {
	cases := []struct {
		name string
		live bool
		body string
		edit func(*questionTmux)
		code string
	}{
		{"another question on screen", true, `{"toolId":"toolu_0","answers":[{"picks":[1]}]}`, nil, "question-changed"},
		{"pane not live", false, `{"toolId":"toolu_1","answers":[{"picks":[1]}]}`, nil, web.CodeNotLive},
		{"bad answer", true, `{"toolId":"toolu_1","answers":[{"picks":[0,1]}]}`, nil, question.CodeInvalid},
		{"widget gone", true, `{"toolId":"toolu_1","answers":[{"picks":[1]}]}`, func(q *questionTmux) { q.screen = "$ " }, question.CodeNotOnScreen},
		{"pane reused", true, `{"toolId":"toolu_1","answers":[{"picks":[1]}]}`, func(q *questionTmux) { q.owner = "pane-other" }, question.CodeWrongPane},
		{"no tool id", true, `{"answers":[{"picks":[1]}]}`, nil, web.CodeInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend, tmux := questionHarness(t, tc.live)
			if tc.edit != nil {
				tc.edit(tmux)
			}
			code, body := webSend(t, web.New(backend, nil).Handler(), "POST", "/api/v1/web/agents/agt-alpha-codex/question", tc.body)
			if errorCode(body) != tc.code || code < 400 {
				t.Fatalf("= %d %v, want %s", code, body, tc.code)
			}
			if len(tmux.keys) != 0 {
				t.Fatalf("keys were sent: %q", tmux.keys)
			}
		})
	}
	// A question already answered is not pending.
	backend, _ := webClientFixture(t, pendingQuestion,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"red"}]}}`)
	_, body := webSend(t, web.New(backend, nil).Handler(), "POST", "/api/v1/web/agents/agt-alpha-codex/question", `{"toolId":"toolu_1","answers":[{"picks":[0]}]}`)
	if errorCode(body) != question.CodeAnswered {
		t.Fatalf("answered = %v", body)
	}
}
