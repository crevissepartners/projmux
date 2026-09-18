package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/web"
)

// Mutations run the CLI's own handlers in this process.
//
// A handler is the unit that holds a verb's whole contract -- flag parsing,
// scope resolution, the Registry transaction, the tmux side, the receipt --
// and the web API promises the same behaviour as the CLI. Calling it with an
// argv built here gives exactly that, with no second implementation to keep in
// step and no process to spawn. The argv shapes are fixed below; every ref in
// them was resolved from the Registry, never taken from a request body.
//
// Handlers are built fresh for every call, because command objects cache
// observations for their own lifetime, and calls are serialized, because
// several of them share process-wide state.

// webHandler runs one CLI verb and returns its stdout.
func (b *webBackend) cli(argv ...string) (string, error) {
	b.mutations.Lock()
	defer b.mutations.Unlock()
	if b.runCLI != nil {
		return b.runCLI(argv)
	}
	handler, ok := webCLIApp().routeHandlers()[argv[0]]
	if !ok {
		return "", fmt.Errorf("web: no handler for %q", argv[0])
	}
	var stdout, stderr bytes.Buffer
	err := handler(argv[1:], &stdout, &stderr)
	if err != nil {
		return stdout.String(), webCLIError(err, stderr.String())
	}
	return stdout.String(), nil
}

// webCLIApp is the in-process app a web mutation runs on. A create it runs
// inherits the web server's environment and parent chain, which say nothing
// about who asked, so creator provenance is withdrawn.
func webCLIApp() *App {
	app := NewWithLifecycleDiagnostics(nil)
	app.create.withoutCreatorProvenance()
	return app
}

// webCLIError classifies a handler's error into the API's envelope.
func webCLIError(err error, stderr string) error {
	message := err.Error()
	if detail := strings.TrimSpace(stderr); detail != "" && !strings.Contains(message, detail) {
		message += ": " + detail
	}
	var refusal *agentControlRefusal
	if errors.As(err, &refusal) {
		return web.NewError(http.StatusConflict, refusal.Code, message)
	}
	var binding *exactAgentControlBindingError
	if errors.As(err, &binding) {
		return web.NewError(http.StatusConflict, "unavailable", message)
	}
	switch {
	case errors.Is(err, coremetadata.ErrNotFound):
		return web.NewError(http.StatusNotFound, web.CodeNotFound, message)
	case errors.Is(err, coremetadata.ErrNameConflict):
		return web.NewError(http.StatusConflict, web.CodeNameConflict, message)
	case errors.Is(err, coremetadata.ErrInvalidName):
		return web.NewError(http.StatusBadRequest, web.CodeInvalidName, message)
	case IsUsageError(err):
		return web.NewError(http.StatusBadRequest, web.CodeInvalidRequest, message)
	}
	return web.NewError(http.StatusConflict, web.CodeRefused, message)
}

// createdUID reads the uid out of a `-o json` list envelope.
func createdUID(stdout string) (string, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				UID string `json:"uid"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(stdout), &list); err != nil || len(list.Items) == 0 || list.Items[0].Metadata.UID == "" {
		return "", fmt.Errorf("web: create returned no resource: %q", strings.TrimSpace(stdout))
	}
	return list.Items[len(list.Items)-1].Metadata.UID, nil
}

func (b *webBackend) beginWindowCreate(project string) bool {
	b.creatingMu.Lock()
	defer b.creatingMu.Unlock()
	if b.creatingWindows[project] {
		return false
	}
	if b.creatingWindows == nil {
		b.creatingWindows = map[string]bool{}
	}
	b.creatingWindows[project] = true
	return true
}

func (b *webBackend) endWindowCreate(project string) {
	b.creatingMu.Lock()
	defer b.creatingMu.Unlock()
	delete(b.creatingWindows, project)
}

func (b *webBackend) CreateWindow(ctx context.Context, project string, req web.CreateWindowRequest) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := s.registry.Project(project); !ok {
		return nil, web.NotFound("no project " + project)
	}
	if !b.beginWindowCreate(project) {
		return nil, web.NewError(http.StatusConflict, web.CodeInProgress, "a window is already being created in project "+project)
	}
	defer b.endWindowCreate(project)
	argv := []string{"create", "window", "--project", "uid:" + project, "-o", "json"}
	if name := strings.TrimSpace(req.Name); name != "" {
		argv = append(argv, "--name", name)
	}
	out, err := b.cli(argv...)
	if err != nil {
		return nil, err
	}
	window, err := createdUID(out)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	// The Window exists from here on. A refused agent or focus is reported on
	// the result rather than failing a request whose main effect happened.
	if req.Agent != nil {
		agent, agentErr := b.CreateAgent(ctx, project, window, web.CreateAgentRequest{
			Provider: req.Agent.Provider, Payload: req.Agent.Payload, CwdFrom: "project", Confirm: true,
			Model: req.Agent.Model, Effort: req.Agent.Effort,
		})
		if agentErr != nil {
			result["agentError"] = web.AsError(agentErr)
		} else {
			maps.Copy(result, agent.(map[string]any))
		}
	}
	if req.Focus {
		if _, focusErr := b.focus(ctx, project, window, ""); focusErr != nil {
			result["focusError"] = web.AsError(focusErr)
		}
	}
	after, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	result["window"], _ = after.item(coremetadata.KindWindow, window)
	return result, nil
}

// createAgentArgv is the one builder of a create-agent call. The preview
// route renders it and CreateAgent runs it, so what the operator approves is
// what runs.
// webSplitCWDFrom is where a web split starts: the request's value when it
// names one, otherwise the web's tiered `[ai] split_cwd_from` setting: the
// Project's config, then web.toml, then the global config.
func (b *webBackend) splitCWDFrom(s webSnapshot, project, requested string) (string, error) {
	if requested != "" {
		source, ok := parseSplitCWDSource(requested)
		if !ok {
			return "", web.InvalidRequest(fmt.Sprintf("cwdFrom %q is not pane or project", requested))
		}
		return string(source), nil
	}
	root := ""
	if found, ok := s.registry.Project(project); ok {
		root = found.Spec.Root
	}
	home, env := b.home, b.env
	if home == nil {
		home = os.UserHomeDir
	}
	if env == nil {
		env = webSettingsEnv
	}
	resolved, err := resolveWebSplitCWDSource("", root, home, env)
	if err != nil {
		return "", err
	}
	return string(resolved.Source), nil
}

func (b *webBackend) createAgentArgv(s webSnapshot, project, window string, req web.CreateAgentRequest) ([]string, error) {
	if _, err := s.window(project, window); err != nil {
		return nil, err
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	switch provider {
	case "claude", "codex", "antigravity":
	default:
		return nil, web.NewError(http.StatusBadRequest, web.CodeInvalidRequest, fmt.Sprintf("provider %q cannot be created", req.Provider))
	}
	cwdFrom, err := b.splitCWDFrom(s, project, req.CwdFrom)
	if err != nil {
		return nil, err
	}
	argv := []string{"create", "agent", "--provider", provider, "--project", "uid:" + project, "--window", "uid:" + window}
	if anchor := strings.TrimSpace(req.AnchorPane); anchor != "" {
		if _, ok := s.registry.PaneInWindow(window, anchor); !ok {
			return nil, web.NotFound("no pane " + anchor + " in window " + window)
		}
		argv = append(argv, "--pane", "uid:"+anchor)
	} else if cwdFrom == "pane" {
		cwdFrom = "project" // there is no anchor to inherit a directory from
	}
	// Placement is fixed. `down` is excluded on purpose, not left to callers.
	argv = append(argv, "--placement", "right", "--cwd-from", cwdFrom, "-o", "json")
	if model := strings.TrimSpace(req.Model); model != "" {
		argv = append(argv, "--model", model)
	}
	if effort := strings.TrimSpace(req.Effort); effort != "" {
		argv = append(argv, "--effort", effort)
	}
	if payload := strings.TrimSpace(req.Payload); payload != "" {
		argv = append(argv, "--", payload)
	}
	return argv, nil
}

func (b *webBackend) PreviewAgent(ctx context.Context, project, window string, req web.CreateAgentRequest) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	argv, err := b.createAgentArgv(s, project, window, req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"argv": append([]string{"projmux"}, argv...)}, nil
}

func (b *webBackend) CreateAgent(ctx context.Context, project, window string, req web.CreateAgentRequest) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	argv, err := b.createAgentArgv(s, project, window, req)
	if err != nil {
		return nil, err
	}
	out, err := b.cli(argv...)
	if err != nil {
		return nil, err
	}
	agent, err := createdUID(out)
	if err != nil {
		return nil, err
	}
	after, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	result["agent"], _ = after.item(coremetadata.KindAgent, agent)
	if created, ok := after.registry.Agent(agent); ok && created.Status.PaneRef != "" {
		result["pane"], _ = after.item(coremetadata.KindPane, created.Status.PaneRef)
	}
	return result, nil
}

// CreatePane splits a plain shell to the right of anchorPane, the web's
// counterpart of the launcher's shell row.
func (b *webBackend) CreatePane(ctx context.Context, project, window string, req web.CreatePaneRequest) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.window(project, window); err != nil {
		return nil, err
	}
	anchor := strings.TrimSpace(req.AnchorPane)
	if anchor == "" {
		return nil, web.InvalidRequest("anchorPane is required")
	}
	if _, ok := s.registry.PaneInWindow(window, anchor); !ok {
		return nil, web.NotFound("no pane " + anchor + " in window " + window)
	}
	cwdFrom, err := b.splitCWDFrom(s, project, req.CwdFrom)
	if err != nil {
		return nil, err
	}
	out, err := b.cli("create", "pane", "--project", "uid:"+project, "--window", "uid:"+window,
		"--pane", "uid:"+anchor, "--placement", "right", "--cwd-from", cwdFrom, "-o", "json")
	if err != nil {
		return nil, err
	}
	pane, err := createdUID(out)
	if err != nil {
		return nil, err
	}
	after, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	item, _ := after.item(coremetadata.KindPane, pane)
	return map[string]any{"pane": item}, nil
}

func (b *webBackend) RenameWindow(ctx context.Context, project, window, name string) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.window(project, window); err != nil {
		return nil, err
	}
	if _, err := b.cli("rename", "window", "uid:"+window, "--name", name, "--project", "uid:"+project); err != nil {
		return nil, err
	}
	return b.Window(ctx, project, window)
}

func (b *webBackend) RenamePane(ctx context.Context, project, window, pane, name string) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.window(project, window); err != nil {
		return nil, err
	}
	if _, ok := s.registry.PaneInWindow(window, pane); !ok {
		return nil, web.NotFound("no pane " + pane + " in window " + window)
	}
	if _, err := b.cli("rename", "pane", "uid:"+pane, "--name", name, "--project", "uid:"+project, "--window", "uid:"+window); err != nil {
		return nil, err
	}
	return b.Pane(ctx, project, window, pane)
}

func (b *webBackend) RenameAgent(ctx context.Context, agent, name string) (any, error) {
	project, window, err := b.agentScope(ctx, agent)
	if err != nil {
		return nil, err
	}
	if _, err := b.cli("rename", "agent", "uid:"+agent, "--name", name, "--project", "uid:"+project, "--window", "uid:"+window); err != nil {
		return nil, err
	}
	return b.Agent(ctx, agent)
}

// agentScope resolves the Project and Window an Agent belongs to.
func (b *webBackend) agentScope(ctx context.Context, agent string) (project, window string, err error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return "", "", err
	}
	return s.agentScope(agent)
}

// agentScope resolves the Project and Window an Agent belongs to from this
// snapshot. agent must be an exact Agent uid.
func (s webSnapshot) agentScope(agent string) (project, window string, err error) {
	found, ok := s.registry.Agent(agent)
	if !ok {
		return "", "", web.NotFound("no agent " + agent)
	}
	owner, ok := s.registry.Window(found.Metadata.OwnerUID())
	if !ok {
		return "", "", web.NotFound("agent " + agent + " has no window")
	}
	return owner.Metadata.OwnerUID(), owner.Metadata.UID, nil
}

// StopProject ends the Project's persistent tmux session through the same
// `stop project` the CLI runs; the Project, its Windows and Agents, and its
// root stay. The Project is resolved from one snapshot by exact uid, so a name
// or an unknown uid is not-found before anything runs. `stop project` has no
// dry run, so a dry run runs nothing: its plan is fixed text and its
// runningAgents are the Running Agents of the Project's Windows, from the same
// Registry read.
func (b *webBackend) StopProject(ctx context.Context, project string, dryRun bool) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := s.registry.Project(project); !ok {
		return nil, web.NotFound("no project " + project)
	}
	if dryRun {
		return map[string]any{"uid": project, "dryRun": true,
			"plan": "stop project uid:" + project + ": ends the Project's tmux session; the Project, its Windows and Agents stay registered",
			"runningAgents": s.runningAgents(func(agent coremetadata.Agent) bool {
				owner, _ := agentProject(&s.registry, agent.Metadata.UID)
				return owner == project
			}),
		}, nil
	}
	out, err := b.cli("stop", "project", "uid:"+project)
	if err != nil {
		return nil, err
	}
	return map[string]any{"uid": project, "plan": strings.TrimSpace(out)}, nil
}

func (b *webBackend) DeleteWindow(ctx context.Context, project, window string, dryRun bool) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.window(project, window); err != nil {
		return nil, err
	}
	argv := []string{"delete", "window", "uid:" + window, "--project", "uid:" + project, "--socket", defaultAppSocket}
	if dryRun {
		argv = append(argv, "--dry-run")
	} else {
		argv = append(argv, "--yes")
	}
	out, err := b.cli(argv...)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"uid": window, "dryRun": dryRun, "plan": strings.TrimSpace(out)}
	if dryRun {
		result["runningAgents"] = s.runningAgents(func(agent coremetadata.Agent) bool {
			return agent.Metadata.OwnerUID() == window
		})
	}
	return result, nil
}

func (b *webBackend) DeletePane(ctx context.Context, project, window, pane string, dryRun bool) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.window(project, window); err != nil {
		return nil, err
	}
	found, ok := s.registry.PaneInWindow(window, pane)
	if !ok {
		return nil, web.NotFound("no pane " + pane + " in window " + window)
	}
	argv := []string{"delete", "pane", "uid:" + pane, "--project", "uid:" + project, "--window", "uid:" + window}
	if dryRun {
		argv = append(argv, "--dry-run")
	} else {
		argv = append(argv, "--yes")
	}
	argv = append(argv, "--socket", defaultAppSocket)
	out, err := b.cli(argv...)
	if err != nil {
		return nil, err
	}
	if !dryRun {
		return map[string]any{"uid": pane, "plan": strings.TrimSpace(out)}, nil
	}
	paneOwner := ""
	if owner := found.Metadata.OwnerRef; owner != nil && owner.Kind == coremetadata.KindAgent {
		paneOwner = owner.UID
	}
	return map[string]any{"uid": pane, "dryRun": true, "plan": strings.TrimSpace(out),
		"runningAgents": s.runningAgents(func(agent coremetadata.Agent) bool {
			return agent.Status.PaneRef == pane || agent.Metadata.UID == paneOwner
		}),
	}, nil
}

// DeleteAgent deletes one Agent, and with it its managed Pane, through the
// same `delete agent` the CLI runs. The Agent and its scope are resolved from
// one snapshot by exact uid, so a name, a selector, or an unknown uid is
// not-found before anything runs, and the dry run's runningAgents comes from
// the Registry read the delete was checked against.
func (b *webBackend) DeleteAgent(ctx context.Context, agent string, dryRun bool) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	project, window, err := s.agentScope(agent)
	if err != nil {
		return nil, err
	}
	argv := []string{"delete", "agent", "uid:" + agent, "--project", "uid:" + project, "--window", "uid:" + window}
	if dryRun {
		argv = append(argv, "--dry-run")
	} else {
		argv = append(argv, "--yes")
	}
	argv = append(argv, "--socket", defaultAppSocket)
	out, err := b.cli(argv...)
	if err != nil {
		return nil, err
	}
	if !dryRun {
		return map[string]any{"uid": agent, "plan": strings.TrimSpace(out)}, nil
	}
	return map[string]any{"uid": agent, "dryRun": true, "plan": strings.TrimSpace(out),
		"runningAgents": s.runningAgents(func(a coremetadata.Agent) bool {
			return a.Metadata.UID == agent
		}),
	}, nil
}

// webRunningAgent names an Agent a delete would stop.
type webRunningAgent struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
}

// runningAgents lists the Running Agents a delete would take with it, from the
// same Registry read the delete was checked against, so a client can ask
// before it stops work in progress. Running is the stored phase, the same one
// the graph carries and the client reads. The list is never nil, so it
// encodes as [] when nothing is running.
func (s webSnapshot) runningAgents(affected func(coremetadata.Agent) bool) []webRunningAgent {
	out := []webRunningAgent{}
	for _, agent := range s.registry.Agents {
		if agent.Status.Phase == coremetadata.PhaseRunning && affected(agent) {
			out = append(out, webRunningAgent{UID: agent.Metadata.UID, Name: agent.Metadata.Name})
		}
	}
	return out
}

func (b *webBackend) ResumeAgent(ctx context.Context, agent string) (any, error) {
	project, window, err := b.agentScope(ctx, agent)
	if err != nil {
		return nil, err
	}
	if _, err := b.cli("agent", "resume", "uid:"+agent, "--project", "uid:"+project, "--window", "uid:"+window); err != nil {
		return nil, err
	}
	after, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	result := map[string]any{}
	result["agent"], _ = after.item(coremetadata.KindAgent, agent)
	if resumed, ok := after.registry.Agent(agent); ok && resumed.Status.PaneRef != "" {
		result["pane"], _ = after.item(coremetadata.KindPane, resumed.Status.PaneRef)
	}
	return result, nil
}

func (b *webBackend) FocusPane(ctx context.Context, project, window, pane string) (any, error) {
	return b.focus(ctx, project, window, pane)
}

// focus moves the operator's attached client. The coordinate is built from
// the Registry entries the path names, and the server socket is the app
// socket's own path, since `focus --socket` takes a path.
func (b *webBackend) focus(ctx context.Context, project, window, pane string) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	w, err := s.window(project, window)
	if err != nil {
		return nil, err
	}
	p, _ := s.registry.Project(project)
	session := ""
	if p.Status.Session != nil {
		session = p.Status.Session.Name
	}
	target := session + ":" + w.Status.RuntimeID
	if pane != "" {
		found, ok := s.registry.PaneInWindow(window, pane)
		if !ok {
			return nil, web.NotFound("no pane " + pane + " in window " + window)
		}
		if found.Status.Activation.RuntimeID == "" {
			return nil, web.NewError(http.StatusConflict, web.CodeNotLive, "pane "+pane+" has no live runtime")
		}
		target += "." + found.Status.Activation.RuntimeID
	}
	if session == "" || w.Status.RuntimeID == "" {
		return nil, web.NewError(http.StatusConflict, web.CodeNotLive, "window "+window+" has no live runtime")
	}
	socket, err := b.appSocketPath(ctx)
	if err != nil {
		return nil, err
	}
	out, err := b.cli("internal", "focus", "--target", target, "--source", "projmux-web", "--kind", "pane-click", "--socket", socket, "--json")
	var result map[string]any
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); jsonErr == nil {
		if reason, _ := result["reason"].(string); err != nil && reason != "" {
			e := web.NewError(http.StatusConflict, reason, fmt.Sprintf("focus %s: %s", target, reason))
			e.Details = result
			return nil, e
		}
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (b *webBackend) appSocketPath(ctx context.Context) (string, error) {
	if b.socketPath != nil {
		return b.socketPath(ctx)
	}
	// The diagnostics reader already asks the observed server for its own
	// socket path; it is the one read that turns `-L projmux` into the path
	// `focus --socket` takes.
	path, ok := newRuntimeDiagnosticsReader(inttmux.ExecRunner{}).socketPath(ctx, b.transport)
	if !ok {
		return "", web.NewError(http.StatusConflict, web.CodeNotLive, "the projmux tmux server is not running")
	}
	return path, nil
}

func (b *webBackend) Capabilities(ctx context.Context, agent string) (any, error) {
	if _, _, err := b.agentScope(ctx, agent); err != nil {
		return nil, err
	}
	out, err := b.cli("agent", "capabilities", "uid:"+agent, "--json")
	if err != nil {
		return nil, err
	}
	return json.RawMessage(strings.TrimSpace(out)), nil
}

func (b *webBackend) StartTurn(ctx context.Context, agent, text string) (any, error) {
	return b.turn(ctx, "start", agent, text)
}

func (b *webBackend) SteerTurn(ctx context.Context, agent, text string) (any, error) {
	return b.turn(ctx, "steer", agent, text)
}

func (b *webBackend) InterruptTurn(ctx context.Context, agent string) (any, error) {
	return b.turn(ctx, "interrupt", agent, "")
}

// turn runs one Codex turn operation. Starting never falls back to steering
// here: a client that gets turn-in-progress decides, so one request is never
// two different operations.
func (b *webBackend) turn(ctx context.Context, op, agent, text string) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	found, ok := s.registry.Agent(agent)
	if !ok {
		return nil, web.NotFound("no agent " + agent)
	}
	if found.Spec.Provider != "codex" {
		return nil, web.NewError(http.StatusBadRequest, web.CodeUnsupported, fmt.Sprintf("a %s agent has no turn control", found.Spec.Provider))
	}
	argv := []string{"agent", "turn", op, "uid:" + agent}
	if op != "interrupt" {
		if strings.TrimSpace(text) == "" {
			return nil, web.NewError(http.StatusBadRequest, web.CodeInvalidRequest, "text is empty")
		}
		argv = append(argv, "--", text)
	}
	out, err := b.cli(argv...)
	if err != nil {
		return nil, err
	}
	return map[string]any{"operation": "turn." + op, "receipt": strings.TrimSpace(out)}, nil
}

// webMessageRefPrefix marks a message this server sent. The ref is the only
// field on the broker path a sender chooses, and the broker echoes it to the
// receiver, so it is where the web origin of a message stays visible.
const webMessageRefPrefix = "projmux-web-"

func (b *webBackend) SendMessage(ctx context.Context, agent string, req web.MessageRequest) (any, error) {
	if _, _, err := b.agentScope(ctx, agent); err != nil {
		return nil, err
	}
	body := strings.TrimSpace(req.Body)
	if body == "" {
		return nil, web.NewError(http.StatusBadRequest, web.CodeInvalidRequest, "body is empty")
	}
	source := strings.TrimPrefix(strings.TrimSpace(req.Source), "uid:")
	if source == "" {
		return nil, web.NewError(http.StatusBadRequest, web.CodeInvalidRequest, "source is required: the browser has no pane to send from")
	}
	if _, _, err := b.agentScope(ctx, source); err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(req.MessageRef)
	if ref == "" {
		ref = newWebMessageRef()
	}
	argv := []string{"agent", "message", "send", "uid:" + agent, "--source", "uid:" + source, "--message-ref", ref}
	if reply := strings.TrimSpace(req.ReplyTo); reply != "" {
		argv = append(argv, "--reply-to", reply)
	}
	if ttl := strings.TrimSpace(req.TTL); ttl != "" {
		argv = append(argv, "--ttl", ttl)
	}
	argv = append(argv, "--", body)
	out, err := b.cli(argv...)
	receipt := parseMessageReceipt(out)
	if err != nil {
		if receipt["reason"] != "" {
			e := web.NewError(http.StatusConflict, receipt["reason"], err.Error())
			e.Details = map[string]any{"delivery": receipt}
			return nil, e
		}
		return nil, err
	}
	return map[string]any{"delivery": receipt}, nil
}

func parseMessageReceipt(stdout string) map[string]string {
	line := strings.TrimSpace(stdout)
	if i := strings.LastIndex(line, "\n"); i >= 0 {
		line = line[i+1:]
	}
	fields := strings.Split(line, "\t")
	receipt := map[string]string{}
	for i, key := range []string{"messageRef", "state", "reason", "action"} {
		if i < len(fields) {
			receipt[key] = fields[i]
		}
	}
	return receipt
}

func newWebMessageRef() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	return webMessageRefPrefix + hex.EncodeToString(raw[:])
}
