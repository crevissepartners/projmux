package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/web"
)

// These tests are the mutation contract the prototype's server tests pinned,
// carried over: what each route refuses before anything runs, and the exact
// CLI argv it runs when it does not. The handler call is stubbed, so nothing
// touches a Registry or tmux, and a route that runs when it should have
// refused shows up as a recorded call.

type webCLIRecorder struct {
	calls []string
	reply func(argv []string) (string, error)
}

func (r *webCLIRecorder) run(argv []string) (string, error) {
	r.calls = append(r.calls, strings.Join(argv, " "))
	if r.reply != nil {
		return r.reply(argv)
	}
	return "", nil
}

func webMutationHarness(t *testing.T) (http.Handler, *webCLIRecorder) {
	t.Helper()
	backend, _ := webFixtureBackend(t)
	recorder := &webCLIRecorder{}
	backend.runCLI = recorder.run
	backend.socketPath = func(context.Context) (string, error) { return "/tmp/tmux-1000/projmux", nil }
	return web.New(backend, nil).Handler(), recorder
}

func webSend(t *testing.T, handler http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var decoded map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
	return rec.Code, decoded
}

func errorCode(body map[string]any) string {
	envelope, _ := body["error"].(map[string]any)
	code, _ := envelope["code"].(string)
	return code
}

const webWindowAlpha = "/api/v1/projects/prj-alpha/windows/win-alpha-main"

// A destructive or quota-spending mutation without confirm is refused before
// anything runs. The flag is not a UI confirmation; it makes a bare POST inert.
func TestWebMutationsRefuseABareRequest(t *testing.T) {
	handler, recorder := webMutationHarness(t)
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/v1/projects/prj-alpha/windows"},
		{"POST", webWindowAlpha + "/agents"},
		{"DELETE", webWindowAlpha},
		{"DELETE", webWindowAlpha + "/panes/pan-alpha-log"},
		{"DELETE", "/api/v1/agents/agt-alpha-codex"},
		{"POST", "/api/v1/agents/agt-beta-codex/resume"},
		{"POST", "/api/v1/projects/prj-alpha/stop"},
	} {
		for _, body := range []string{``, `{}`, `{"confirm":false}`} {
			code, reply := webSend(t, handler, tc.method, tc.path, body)
			if code != http.StatusBadRequest || errorCode(reply) != web.CodeConfirmRequired {
				t.Errorf("%s %s %q = %d %v, want confirm-required", tc.method, tc.path, body, code, reply)
			}
		}
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("an unconfirmed mutation ran: %v", recorder.calls)
	}
}

// A body cannot name a ref the route does not read.
func TestWebMutationsRefuseUnknownBodyFields(t *testing.T) {
	handler, recorder := webMutationHarness(t)
	for _, tc := range []struct{ method, path, body string }{
		{"POST", webWindowAlpha + "/agents", `{"provider":"claude","confirm":true,"window":"win-beta-main"}`},
		{"POST", webWindowAlpha + "/agents", `{"provider":"claude","confirm":true,"placement":"down"}`},
		{"DELETE", webWindowAlpha + "/panes/pan-alpha-log", `{"confirm":true,"pane":"pan-beta-zsh"}`},
		{"DELETE", "/api/v1/agents/agt-alpha-codex", `{"confirm":true,"agent":"agt-beta-codex"}`},
		{"DELETE", "/api/v1/agents/agt-alpha-codex?dryRun=true", `{"window":"win-beta-main"}`},
		{"POST", "/api/v1/projects/prj-alpha/stop", `{"confirm":true,"window":"x"}`},
		{"POST", "/api/v1/projects/prj-alpha/stop?dryRun=true", `{"project":"prj-beta"}`},
		{"PATCH", webWindowAlpha, `{"name":"x","uid":"win-beta-main"}`},
		{"POST", "/api/v1/agents/agt-alpha-codex/messages", `{"body":"hi","source":"uid:agt-alpha-codex","target":"x"}`},
		{"POST", webWindowAlpha + "/agents", `{"provider":"claude","confirm":true}{"x":1}`},
		{"POST", webWindowAlpha + "/agents", `not json`},
	} {
		code, reply := webSend(t, handler, tc.method, tc.path, tc.body)
		if code != http.StatusBadRequest || errorCode(reply) != web.CodeInvalidRequest {
			t.Errorf("%s %s %s = %d %v, want invalid-request", tc.method, tc.path, tc.body, code, reply)
		}
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("a malformed request ran: %v", recorder.calls)
	}
}

func TestWebMutationsRefuseAChildOfAnotherParent(t *testing.T) {
	handler, recorder := webMutationHarness(t)
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/v1/projects/prj-beta/windows/win-alpha-main/agents", `{"provider":"claude","confirm":true}`},
		{"POST", webWindowAlpha + "/agents", `{"provider":"claude","anchorPane":"pan-beta-zsh","confirm":true}`},
		{"DELETE", webWindowAlpha + "/panes/pan-alpha-review", `{"confirm":true}`},
		{"DELETE", "/api/v1/projects/prj-beta/windows/win-alpha-main", `{"confirm":true}`},
		{"PATCH", webWindowAlpha + "/panes/pan-beta-zsh", `{"name":"x"}`},
		{"POST", webWindowAlpha + "/panes/pan-beta-zsh/focus", `{}`},
		{"POST", "/api/v1/projects/prj-missing/windows", `{"confirm":true}`},
		{"POST", "/api/v1/agents/agt-missing/resume", `{"confirm":true}`},
		{"DELETE", "/api/v1/agents/agt-missing", `{"confirm":true}`},
		{"DELETE", "/api/v1/agents/agt-missing?dryRun=true", ``},
		{"POST", "/api/v1/projects/prj-missing/stop", `{"confirm":true}`},
		{"POST", "/api/v1/projects/prj-missing/stop?dryRun=true", ``},
		{"POST", "/api/v1/agents/agt-alpha-codex/messages", `{"body":"hi","source":"uid:agt-missing"}`},
	} {
		code, reply := webSend(t, handler, tc.method, tc.path, tc.body)
		if code != http.StatusNotFound || errorCode(reply) != web.CodeNotFound {
			t.Errorf("%s %s = %d %v, want not-found", tc.method, tc.path, code, reply)
		}
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("a refused request ran: %v", recorder.calls)
	}
}

// Each confirmed mutation runs its own argv, with every ref taken from the
// path's Registry entries.
func TestWebMutationsRunTheirOwnArgv(t *testing.T) {
	agentList := func(uid string) string {
		return fmt.Sprintf(`{"apiVersion":"projmux.io/v1alpha1","kind":"AgentList","items":[{"metadata":{"uid":%q}}]}`, uid)
	}
	cases := []struct {
		name, method, path, body string
		reply                    string
		want                     []string
	}{
		{
			"create agent beside a pane", "POST", webWindowAlpha + "/agents",
			`{"provider":"Claude","anchorPane":"pan-alpha-log","payload":" hi ","confirm":true}`, agentList("agt-alpha-codex"),
			[]string{"create agent --provider claude --project uid:prj-alpha --window uid:win-alpha-main --pane uid:pan-alpha-log --placement right --cwd-from project -o json -- hi"},
		},
		{
			"create agent without an anchor uses the project directory", "POST", webWindowAlpha + "/agents",
			`{"provider":"codex","confirm":true}`, agentList("agt-alpha-codex"),
			[]string{"create agent --provider codex --project uid:prj-alpha --window uid:win-alpha-main --placement right --cwd-from project -o json"},
		},
		{
			"delete pane", "DELETE", webWindowAlpha + "/panes/pan-alpha-codex", `{"confirm":true}`, "",
			[]string{"delete pane uid:pan-alpha-codex --project uid:prj-alpha --window uid:win-alpha-main --yes --socket projmux"},
		},
		{
			"delete pane dry run needs no confirm", "DELETE", webWindowAlpha + "/panes/pan-alpha-codex?dryRun=true", ``, "",
			[]string{"delete pane uid:pan-alpha-codex --project uid:prj-alpha --window uid:win-alpha-main --dry-run --socket projmux"},
		},
		{
			"delete window", "DELETE", webWindowAlpha, `{"confirm":true}`, "",
			[]string{"delete window uid:win-alpha-main --project uid:prj-alpha --socket projmux --yes"},
		},
		{
			"delete window dry run needs no confirm", "DELETE", webWindowAlpha + "?dryRun=true", ``, "",
			[]string{"delete window uid:win-alpha-main --project uid:prj-alpha --socket projmux --dry-run"},
		},
		{
			"rename window", "PATCH", webWindowAlpha, `{"name":" n "}`, "",
			[]string{"rename window uid:win-alpha-main --name n --project uid:prj-alpha"},
		},
		{
			"rename pane", "PATCH", webWindowAlpha + "/panes/pan-alpha-log", `{"name":"n"}`, "",
			[]string{"rename pane uid:pan-alpha-log --name n --project uid:prj-alpha --window uid:win-alpha-main"},
		},
		{
			"rename agent", "PATCH", "/api/v1/agents/agt-alpha-codex", `{"name":"n"}`, "",
			[]string{"rename agent uid:agt-alpha-codex --name n --project uid:prj-alpha --window uid:win-alpha-main"},
		},
		{
			"delete agent", "DELETE", "/api/v1/agents/agt-alpha-codex", `{"confirm":true}`, "",
			[]string{"delete agent uid:agt-alpha-codex --project uid:prj-alpha --window uid:win-alpha-main --yes --socket projmux"},
		},
		{
			"delete agent dry run needs no confirm", "DELETE", "/api/v1/agents/agt-alpha-codex?dryRun=true", ``, "",
			[]string{"delete agent uid:agt-alpha-codex --project uid:prj-alpha --window uid:win-alpha-main --dry-run --socket projmux"},
		},
		{
			"delete an agent in another project", "DELETE", "/api/v1/agents/agt-beta-codex", `{"confirm":true}`, "",
			[]string{"delete agent uid:agt-beta-codex --project uid:prj-beta --window uid:win-beta-main --yes --socket projmux"},
		},
		{
			"resume", "POST", "/api/v1/agents/agt-beta-codex/resume", `{"confirm":true}`, "",
			[]string{"agent resume uid:agt-beta-codex --project uid:prj-beta --window uid:win-beta-main"},
		},
		{
			"stop project", "POST", "/api/v1/projects/prj-alpha/stop", `{"confirm":true}`, "",
			[]string{"stop project uid:prj-alpha"},
		},
		{
			"start a codex turn", "POST", "/api/v1/agents/agt-alpha-codex/turns", `{"text":"hi"}`, "",
			[]string{"agent turn start uid:agt-alpha-codex -- hi"},
		},
		{
			"steer a codex turn", "POST", "/api/v1/agents/agt-alpha-codex/turns/current/steer", `{"text":"hi"}`, "",
			[]string{"agent turn steer uid:agt-alpha-codex -- hi"},
		},
		{
			"interrupt a codex turn", "DELETE", "/api/v1/agents/agt-alpha-codex/turns/current", ``, "",
			[]string{"agent turn interrupt uid:agt-alpha-codex"},
		},
		{
			"send a message with a given ref", "POST", "/api/v1/agents/agt-alpha-codex/messages",
			`{"body":" hi ","source":"agt-beta-codex","messageRef":"projmux-web-1","replyTo":"message-9","ttl":"5m"}`, "projmux-web-1\taccepted\n",
			[]string{"agent message send uid:agt-alpha-codex --source uid:agt-beta-codex --message-ref projmux-web-1 --reply-to message-9 --ttl 5m -- hi"},
		},
		{
			"capabilities", "GET", "/api/v1/agents/agt-alpha-codex/capabilities", ``, `{"provider":"codex","capabilities":[]}`,
			[]string{"agent capabilities uid:agt-alpha-codex --json"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, recorder := webMutationHarness(t)
			recorder.reply = func([]string) (string, error) { return tc.reply, nil }
			code, body := webSend(t, handler, tc.method, tc.path, tc.body)
			if code >= 300 {
				t.Fatalf("%s %s = %d %v", tc.method, tc.path, code, body)
			}
			if strings.Join(recorder.calls, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("calls = %q\nwant   %q", recorder.calls, tc.want)
			}
		})
	}
}

// A dry run names the Running Agents the delete would stop, so the client can
// ask before it closes one, and deletes nothing.
func TestWebDeleteDryRunNamesTheRunningAgents(t *testing.T) {
	codex := []any{map[string]any{"uid": "agt-alpha-codex", "name": "codex"}}
	cases := []struct {
		name, path string
		offline    bool
		want       []any
	}{
		{"the agent's pane", webWindowAlpha + "/panes/pan-alpha-codex", false, codex},
		{"a window holding the agent", webWindowAlpha, false, codex},
		{"a pane without an agent", webWindowAlpha + "/panes/pan-alpha-log", false, []any{}},
		{"a window without an agent", "/api/v1/projects/prj-alpha/windows/win-alpha-review", false, []any{}},
		{"a window whose agent is offline", "/api/v1/projects/prj-beta/windows/win-beta-main", false, []any{}},
		{"the pane of an agent that is not running", webWindowAlpha + "/panes/pan-alpha-codex", true, []any{}},
		{"a window whose agent is not running", webWindowAlpha, true, []any{}},
		{"the agent itself", "/api/v1/agents/agt-alpha-codex", false, codex},
		{"an agent that is not running", "/api/v1/agents/agt-alpha-codex", true, []any{}},
		{"an offline agent", "/api/v1/agents/agt-beta-codex", false, []any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend, _ := webFixtureBackend(t)
			if tc.offline {
				registry, err := backend.loadRegistry()
				if err != nil {
					t.Fatal(err)
				}
				for i := range registry.Agents {
					registry.Agents[i].Status.Phase = coremetadata.PhaseOffline
				}
				backend.loadRegistry = func() (coremetadata.Registry, error) { return registry.Clone(), nil }
			}
			recorder := &webCLIRecorder{reply: func([]string) (string, error) { return "plan text\n", nil }}
			backend.runCLI = recorder.run
			code, body := webSend(t, web.New(backend, nil).Handler(), "DELETE", tc.path+"?dryRun=true", ``)
			if code != http.StatusOK {
				t.Fatalf("dry run = %d %v", code, body)
			}
			if body["dryRun"] != true || body["plan"] != "plan text" {
				t.Errorf("dry run body = %v, want dryRun and the plan", body)
			}
			if got := fmt.Sprint(body["runningAgents"]); got != fmt.Sprint(tc.want) || body["runningAgents"] == nil {
				t.Errorf("runningAgents = %v, want %v", body["runningAgents"], tc.want)
			}
			if len(recorder.calls) != 1 || !strings.Contains(recorder.calls[0], " --dry-run") || strings.Contains(recorder.calls[0], "--yes") {
				t.Fatalf("calls = %q, want one dry run and no --yes", recorder.calls)
			}
		})
	}

	// Without dryRun a pane delete still needs confirm, and its result carries
	// no Agent list.
	handler, recorder := webMutationHarness(t)
	for _, path := range []string{webWindowAlpha + "/panes/pan-alpha-codex", webWindowAlpha + "/panes/pan-alpha-codex?dryRun=false"} {
		if code, reply := webSend(t, handler, "DELETE", path, `{}`); code != http.StatusBadRequest || errorCode(reply) != web.CodeConfirmRequired {
			t.Errorf("DELETE %s = %d %v, want confirm-required", path, code, reply)
		}
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("an unconfirmed pane delete ran: %v", recorder.calls)
	}
	code, body := webSend(t, handler, "DELETE", webWindowAlpha+"/panes/pan-alpha-codex", `{"confirm":true}`)
	if code != http.StatusOK {
		t.Fatalf("confirmed pane delete = %d %v", code, body)
	}
	if _, ok := body["runningAgents"]; ok {
		t.Errorf("a real delete carries runningAgents: %v", body)
	}
}

// A Project stop dry run runs nothing, because `stop project` has none, and
// names the Running Agents of the Project's Windows from the same Registry
// read; a stop without dryRun needs confirm.
func TestWebStopProjectDryRunRunsNothing(t *testing.T) {
	codex := []any{map[string]any{"uid": "agt-alpha-codex", "name": "codex"}}
	cases := []struct {
		name, project string
		offline       bool
		want          []any
	}{
		{"a project with a running agent", "prj-alpha", false, codex},
		{"a project whose agents are not running", "prj-alpha", true, []any{}},
		{"a project whose agent is offline", "prj-beta", false, []any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend, _ := webFixtureBackend(t)
			if tc.offline {
				registry, err := backend.loadRegistry()
				if err != nil {
					t.Fatal(err)
				}
				for i := range registry.Agents {
					registry.Agents[i].Status.Phase = coremetadata.PhaseOffline
				}
				backend.loadRegistry = func() (coremetadata.Registry, error) { return registry.Clone(), nil }
			}
			recorder := &webCLIRecorder{}
			backend.runCLI = recorder.run
			for _, body := range []string{``, `{}`, `{"confirm":false}`} {
				code, reply := webSend(t, web.New(backend, nil).Handler(), "POST", "/api/v1/projects/"+tc.project+"/stop?dryRun=true", body)
				if code != http.StatusOK {
					t.Fatalf("dry run %q = %d %v", body, code, reply)
				}
				wantPlan := "stop project uid:" + tc.project + ": ends the Project's tmux session; the Project, its Windows and Agents stay registered"
				if reply["uid"] != tc.project || reply["dryRun"] != true || reply["plan"] != wantPlan {
					t.Errorf("dry run body = %v, want uid, dryRun and the fixed plan", reply)
				}
				if got := fmt.Sprint(reply["runningAgents"]); got != fmt.Sprint(tc.want) || reply["runningAgents"] == nil {
					t.Errorf("runningAgents = %v, want %v", reply["runningAgents"], tc.want)
				}
			}
			if len(recorder.calls) != 0 {
				t.Fatalf("a stop dry run ran %q", recorder.calls)
			}
		})
	}
}

// The project path segment is an exact Project uid. A name or a selector
// spelling is not-found and nothing runs; a confirmed stop runs one
// `stop project` and returns its receipt line as the plan.
func TestWebStopProjectTakesOnlyAnExactProjectUID(t *testing.T) {
	handler, recorder := webMutationHarness(t)
	for _, path := range []string{"/api/v1/projects/prj-alpha/stop", "/api/v1/projects/prj-alpha/stop?dryRun=false"} {
		if code, reply := webSend(t, handler, "POST", path, `{}`); code != http.StatusBadRequest || errorCode(reply) != web.CodeConfirmRequired {
			t.Errorf("POST %s = %d %v, want confirm-required", path, code, reply)
		}
	}
	for _, ref := range []string{"alpha", "uid:prj-alpha", "project%2Falpha", "win-alpha-main", "PRJ-ALPHA"} {
		for _, suffix := range []string{"", "?dryRun=true"} {
			path := "/api/v1/projects/" + ref + "/stop" + suffix
			if code, reply := webSend(t, handler, "POST", path, `{"confirm":true}`); code != http.StatusNotFound || errorCode(reply) != web.CodeNotFound {
				t.Errorf("POST %s = %d %v, want not-found", path, code, reply)
			}
		}
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("a refused stop ran: %v", recorder.calls)
	}

	recorder.reply = func([]string) (string, error) { return "stop.project project/alpha runtime=stopped\n", nil }
	code, body := webSend(t, handler, "POST", "/api/v1/projects/prj-alpha/stop", `{"confirm":true}`)
	if code != http.StatusOK || body["uid"] != "prj-alpha" || body["plan"] != "stop.project project/alpha runtime=stopped" {
		t.Fatalf("confirmed stop = %d %v", code, body)
	}
	for _, key := range []string{"runningAgents", "dryRun"} {
		if _, ok := body[key]; ok {
			t.Errorf("a real stop carries %s: %v", key, body)
		}
	}
	if len(recorder.calls) != 1 || recorder.calls[0] != "stop project uid:prj-alpha" {
		t.Fatalf("calls = %q, want one exact-uid stop project", recorder.calls)
	}
}

// A Project with no live session is the CLI's usage refusal, which the web
// error mapping makes invalid-request; it is never a success.
func TestWebStopProjectOfANonLiveProjectIsRefused(t *testing.T) {
	handler, recorder := webMutationHarness(t)
	recorder.reply = func([]string) (string, error) {
		return "", webCLIError(usageError("stop project: project/beta has no live persistent session; nothing was changed"), "")
	}
	code, body := webSend(t, handler, "POST", "/api/v1/projects/prj-beta/stop", `{"confirm":true}`)
	if code != http.StatusBadRequest || errorCode(body) != web.CodeInvalidRequest {
		t.Fatalf("stop of a non-live project = %d %v, want 400 invalid-request", code, body)
	}
	message, _ := body["error"].(map[string]any)["message"].(string)
	if !strings.Contains(message, "no live persistent session") {
		t.Errorf("refusal message = %q, want the CLI's text", message)
	}
	if len(recorder.calls) != 1 || recorder.calls[0] != "stop project uid:prj-beta" {
		t.Fatalf("calls = %q", recorder.calls)
	}
}

// The agent path segment is an exact Agent uid, the same one the graph
// carries. A selector spelling, an Agent name, or another kind's uid is not an
// Agent uid in the Registry, so it is not-found and nothing runs; a confirmed
// delete runs one `delete agent` and its result carries no Agent list.
func TestWebDeleteAgentTakesOnlyAnExactAgentUID(t *testing.T) {
	handler, recorder := webMutationHarness(t)
	for _, path := range []string{"/api/v1/agents/agt-alpha-codex", "/api/v1/agents/agt-alpha-codex?dryRun=false"} {
		for _, body := range []string{``, `{}`, `{"confirm":false}`} {
			if code, reply := webSend(t, handler, "DELETE", path, body); code != http.StatusBadRequest || errorCode(reply) != web.CodeConfirmRequired {
				t.Errorf("DELETE %s %q = %d %v, want confirm-required", path, body, code, reply)
			}
		}
	}
	for _, ref := range []string{
		"uid:agt-alpha-codex",
		"codex",
		"agent%2Fcodex",
		"pan-alpha-codex",
		"win-alpha-main",
		"prj-alpha",
		"AGT-ALPHA-CODEX",
	} {
		for _, suffix := range []string{"", "?dryRun=true"} {
			path := "/api/v1/agents/" + ref + suffix
			if code, reply := webSend(t, handler, "DELETE", path, `{"confirm":true}`); code != http.StatusNotFound || errorCode(reply) != web.CodeNotFound {
				t.Errorf("DELETE %s = %d %v, want not-found", path, code, reply)
			}
		}
	}
	for _, path := range []string{"/api/v1/agents/", "/api/v1/agents"} {
		if code, reply := webSend(t, handler, "DELETE", path, `{"confirm":true}`); code < 300 {
			t.Errorf("DELETE %s = %d %v, want a refusal", path, code, reply)
		}
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("a refused agent delete ran: %v", recorder.calls)
	}

	recorder.reply = func([]string) (string, error) { return "deleted\n", nil }
	code, body := webSend(t, handler, "DELETE", "/api/v1/agents/agt-alpha-codex", `{"confirm":true}`)
	if code != http.StatusOK || body["uid"] != "agt-alpha-codex" || body["plan"] != "deleted" {
		t.Fatalf("confirmed agent delete = %d %v", code, body)
	}
	if _, ok := body["runningAgents"]; ok {
		t.Errorf("a real delete carries runningAgents: %v", body)
	}
	if len(recorder.calls) != 1 || !strings.HasPrefix(recorder.calls[0], "delete agent uid:agt-alpha-codex ") {
		t.Fatalf("calls = %q, want one exact-uid delete agent", recorder.calls)
	}
}

func TestWebMutationResultsCarryTheResource(t *testing.T) {
	handler, recorder := webMutationHarness(t)
	recorder.reply = func([]string) (string, error) {
		return `{"kind":"AgentList","items":[{"metadata":{"uid":"agt-alpha-codex"}}]}`, nil
	}
	code, body := webSend(t, handler, "POST", webWindowAlpha+"/agents", `{"provider":"codex","confirm":true}`)
	if code != http.StatusCreated {
		t.Fatalf("create agent = %d %v", code, body)
	}
	agent, _ := body["agent"].(map[string]any)
	pane, _ := body["pane"].(map[string]any)
	if agent == nil || agent["metadata"].(map[string]any)["uid"] != "agt-alpha-codex" {
		t.Errorf("agent = %v", body["agent"])
	}
	if pane == nil || pane["metadata"].(map[string]any)["uid"] != "pan-alpha-codex" {
		t.Errorf("pane = %v, want the agent's pane", body["pane"])
	}

	code, body = webSend(t, handler, "POST", "/api/v1/agents/agt-alpha-codex/messages", `{"body":"hi","source":"agt-alpha-codex"}`)
	if code != http.StatusOK {
		t.Fatalf("send = %d %v", code, body)
	}
	last := recorder.calls[len(recorder.calls)-1]
	if !strings.Contains(last, "--message-ref "+webMessageRefPrefix) {
		t.Errorf("an unsigned send: %s", last)
	}
}

func TestWebMutationsRefuseWhatTheProviderCannotDo(t *testing.T) {
	handler, recorder := webMutationHarness(t)
	cases := []struct {
		method, path, body, code string
	}{
		{"POST", webWindowAlpha + "/agents", `{"provider":"shell","confirm":true}`, web.CodeInvalidRequest},
		{"POST", webWindowAlpha + "/agents", `{"provider":"claude","cwdFrom":"root","confirm":true}`, web.CodeInvalidRequest},
		{"POST", "/api/v1/agents/agt-alpha-codex/turns", `{"text":"  "}`, web.CodeInvalidRequest},
		{"POST", "/api/v1/agents/agt-alpha-codex/messages", `{"body":"  ","source":"agt-alpha-codex"}`, web.CodeInvalidRequest},
		{"POST", "/api/v1/agents/agt-alpha-codex/messages", `{"body":"hi"}`, web.CodeInvalidRequest},
		{"PATCH", webWindowAlpha, `{"name":"  "}`, web.CodeInvalidRequest},
		{"POST", "/api/v1/notifications/a%20b/ack", ``, web.CodeInvalidRequest},
	}
	for _, tc := range cases {
		code, reply := webSend(t, handler, tc.method, tc.path, tc.body)
		if errorCode(reply) != tc.code || code < 400 {
			t.Errorf("%s %s %s = %d %v, want %s", tc.method, tc.path, tc.body, code, reply, tc.code)
		}
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("a refused request ran: %v", recorder.calls)
	}
}

func TestWebTurnOnANonCodexAgentIsUnsupported(t *testing.T) {
	backend, _ := webFixtureBackend(t)
	registry := resourceFixtureRegistry(t)
	agent, _ := registry.Agent("agt-alpha-codex")
	agent.Spec.Provider = "claude"
	backend.loadRegistry = func() (coremetadata.Registry, error) { return registry.Clone(), nil }
	recorder := &webCLIRecorder{}
	backend.runCLI = recorder.run
	code, body := webSend(t, web.New(backend, nil).Handler(), "POST", "/api/v1/agents/agt-alpha-codex/turns", `{"text":"hi"}`)
	if code != http.StatusBadRequest || errorCode(body) != web.CodeUnsupported {
		t.Fatalf("claude turn = %d %v", code, body)
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("a refused turn ran: %v", recorder.calls)
	}
}

func TestWebFocusBuildsTheRegistryCoordinate(t *testing.T) {
	backend, _ := webFixtureBackend(t)
	registry := resourceFixtureRegistry(t)
	window, _ := registry.Window("win-alpha-main")
	window.Status.RuntimeID = "@7"
	pane, _ := registry.Pane("pan-alpha-log")
	pane.Status.Activation.RuntimeID = "%12"
	backend.loadRegistry = func() (coremetadata.Registry, error) { return registry.Clone(), nil }
	backend.socketPath = func(context.Context) (string, error) { return "/tmp/tmux-1000/projmux", nil }
	recorder := &webCLIRecorder{reply: func([]string) (string, error) { return `{"ok":true}`, nil }}
	backend.runCLI = recorder.run
	handler := web.New(backend, nil).Handler()

	code, body := webSend(t, handler, "POST", webWindowAlpha+"/panes/pan-alpha-log/focus", ``)
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("focus = %d %v", code, body)
	}
	want := "internal focus --target alpha:@7.%12 --source projmux-web --kind pane-click --socket /tmp/tmux-1000/projmux --json"
	if len(recorder.calls) != 1 || recorder.calls[0] != want {
		t.Fatalf("calls = %q\nwant   %q", recorder.calls, want)
	}

	// A pane with no live runtime has no coordinate to focus.
	code, body = webSend(t, handler, "POST", webWindowAlpha+"/panes/pan-alpha-zsh/focus", ``)
	if code != http.StatusConflict || errorCode(body) != web.CodeNotLive {
		t.Fatalf("focus without a runtime = %d %v", code, body)
	}

	// A focus refusal keeps projmux's own reason token.
	recorder.reply = func([]string) (string, error) {
		return `{"ok":false,"reason":"no-attached-client"}`, errors.New("focus did not move")
	}
	code, body = webSend(t, handler, "POST", webWindowAlpha+"/panes/pan-alpha-log/focus", ``)
	if code != http.StatusConflict || errorCode(body) != "no-attached-client" {
		t.Fatalf("refused focus = %d %v", code, body)
	}
}

// A refusal keeps projmux's own token as the code.
func TestWebCLIErrorKeepsRefusalTokens(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{addOpenCodexBindingRecovery(agentControlResponse{Code: "turn-in-progress", Message: "exact thread already has a turn in progress"}.Error(), exactAgentControlBinding{}), 409, "turn-in-progress"},
		{fmt.Errorf("agent turn start: %w", &exactAgentControlBindingError{Reason: "stale"}), 409, "unavailable"},
		{fmt.Errorf("rename: %w", coremetadata.ErrNameConflict), 409, web.CodeNameConflict},
		{fmt.Errorf("get: %w", coremetadata.ErrNotFound), 404, web.CodeNotFound},
		{usageError("bad flag"), 400, web.CodeInvalidRequest},
		{usageError("stop project: project/alpha has no live persistent session; nothing was changed"), 400, web.CodeInvalidRequest},
		{errors.New("cascade plan changed; nothing was deleted"), 409, web.CodeRefused},
	}
	for _, tc := range cases {
		got := web.AsError(webCLIError(tc.err, ""))
		if got.Status != tc.status || got.Code != tc.code {
			t.Errorf("%v -> %d %s, want %d %s", tc.err, got.Status, got.Code, tc.status, tc.code)
		}
	}
	// The CLI text of a control refusal is unchanged by the typed error.
	text := agentControlResponse{Code: "timeout"}.Error().Error()
	if text != "native Codex control unavailable (timeout)" {
		t.Errorf("refusal text = %q", text)
	}
}

func TestWebMessageRefusalCarriesTheDeliveryReason(t *testing.T) {
	handler, recorder := webMutationHarness(t)
	recorder.reply = func([]string) (string, error) {
		return "projmux-web-1\tfailed\tbroker-handoff-persist-failed\tcheck message status\n",
			errors.New("agent message send: message not delivered")
	}
	code, body := webSend(t, handler, "POST", "/api/v1/agents/agt-alpha-codex/messages", `{"body":"hi","source":"agt-alpha-codex"}`)
	if code != http.StatusConflict || errorCode(body) != "broker-handoff-persist-failed" {
		t.Fatalf("undelivered send = %d %v", code, body)
	}
	details, _ := body["error"].(map[string]any)["details"].(map[string]any)
	delivery, _ := details["delivery"].(map[string]any)
	if delivery["state"] != "failed" || delivery["messageRef"] != "projmux-web-1" {
		t.Fatalf("delivery details = %v", details)
	}
}
