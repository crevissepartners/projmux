package app

import (
	"context"
	"net/http"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/i18n"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/web"
	"github.com/crevissepartners/projmux/internal/web/question"
	"github.com/crevissepartners/projmux/internal/web/termview"
	"github.com/crevissepartners/projmux/internal/web/transcript"
)

// The browser client's own reads: its message catalog, agent transcripts, and
// what tmux shows. These are views of the terminal rather than of the
// Registry, which is why they sit behind web.ClientBackend and not the core
// Backend.

var _ web.ClientBackend = (*webBackend)(nil)

const webMessagePrefix = "web."

func (b *webBackend) Messages(context.Context) (any, error) {
	locale := appLocale(os.UserHomeDir, os.Getenv)
	catalog := i18n.DefaultCatalog()
	localizer := i18n.NewLocalizerWithCatalog(catalog, locale)
	messages := map[string]string{}
	for _, key := range catalog.LocaleKeys(i18n.FallbackLocale) {
		if !strings.HasPrefix(string(key), webMessagePrefix) {
			continue
		}
		if text, err := localizer.Text(key); err == nil {
			messages[string(key)] = text.String()
		}
	}
	return map[string]any{"locale": string(locale), "messages": messages}, nil
}

// webSurface is how the client may write to an agent. It follows from the
// provider: Codex takes native turns, Claude takes broker messages anchored
// on a source Agent under the broker's frame limit, and Antigravity has no
// input path.
type webSurface struct {
	Mode           string `json:"mode"`
	CanStop        bool   `json:"canStop,omitempty"`
	SourceRequired bool   `json:"sourceRequired,omitempty"`
	MaxBytes       int    `json:"maxBytes,omitempty"`
}

// webClaudeMessageBytes is the body the composer allows. The broker frames
// the body in an envelope of about 1.2KB and the smallest helper still in
// service refuses frames over 4KB, so this is the floor that always fits.
const webClaudeMessageBytes = 2800

func surfaceFor(provider string) webSurface {
	switch strings.ToLower(provider) {
	case "codex":
		return webSurface{Mode: "turn", CanStop: true}
	case "claude":
		return webSurface{Mode: "message", SourceRequired: true, MaxBytes: webClaudeMessageBytes}
	}
	return webSurface{Mode: "none"}
}

func (s webSnapshot) agent(uid string) (coremetadata.Agent, error) {
	projected, _, ok := resourceFor(s.registry, coremetadata.KindAgent, uid)
	if !ok {
		return coremetadata.Agent{}, web.NotFound("no agent " + uid)
	}
	return projected.(coremetadata.Agent), nil
}

func (b *webBackend) Transcript(ctx context.Context, agentUID string, limit int) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	agent, err := s.agent(agentUID)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"surface":    surfaceFor(agent.Spec.Provider),
		"repository": transcript.RepoFor(ctx, agent.Spec.Workspace.CWD),
	}
	provider := strings.ToLower(agent.Spec.Provider)
	home, _ := os.UserHomeDir()
	// A missing transcript is the normal state of a just-started agent, so it
	// is a note on the result rather than a failed request.
	path, err := transcript.Path(agent, home)
	if err != nil {
		body["transcript"] = transcript.Transcript{Provider: provider, Turns: []transcript.Turn{}, Note: transcript.NoteNoTranscript}
		return body, nil
	}
	read, err := transcript.ReadTranscript(provider, path, limit)
	if err != nil {
		body["transcript"] = transcript.Transcript{Provider: provider, Turns: []transcript.Turn{}, Note: transcript.NoteNoTranscript}
		return body, nil
	}
	if read.Turns == nil {
		read.Turns = []transcript.Turn{}
	}
	read.Path = ""
	body["transcript"] = read
	return body, nil
}

type turnFollower struct{ tailer *transcript.Tailer }

func (f turnFollower) Offset() int64 { return f.tailer.Offset() }

func (f turnFollower) Next() ([]any, error) {
	turns, err := f.tailer.Next()
	if err != nil {
		return nil, err
	}
	out := make([]any, len(turns))
	for i := range turns {
		out[i] = turns[i]
	}
	return out, nil
}

func (b *webBackend) FollowTranscript(ctx context.Context, agentUID string, offset int64) (web.Follower, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	agent, err := s.agent(agentUID)
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	path, err := transcript.Path(agent, home)
	if err != nil {
		return nil, web.NewError(http.StatusConflict, transcript.NoteNoTranscript, err.Error())
	}
	provider := strings.ToLower(agent.Spec.Provider)
	if offset >= 0 {
		return turnFollower{tailer: transcript.NewTailerAt(provider, path, offset)}, nil
	}
	tailer, err := transcript.NewTailer(provider, path, false)
	if err != nil {
		return nil, web.NewError(http.StatusConflict, transcript.NoteNoTranscript, err.Error())
	}
	return turnFollower{tailer: tailer}, nil
}

// liveWindow is the tmux id the app server has for a window, from this
// request's observation rather than the stored hint.
func (s webSnapshot) liveWindow(uid string) (string, error) {
	for _, node := range s.graph.Windows {
		if node.Window.Metadata.UID != uid {
			continue
		}
		if !node.Live || node.Runtime == nil || node.Runtime.ID == "" {
			return "", web.NewError(http.StatusConflict, web.CodeNotLive, "window "+uid+" has no live runtime")
		}
		return node.Runtime.ID, nil
	}
	return "", web.NotFound("no window " + uid)
}

func (s webSnapshot) livePane(uid string) (string, error) {
	for _, node := range s.graph.Panes {
		if node.Pane.Metadata.UID != uid {
			continue
		}
		if node.Status != resourcegraph.StatusLive || node.Runtime == nil || node.Runtime.ID == "" {
			return "", web.NewError(http.StatusConflict, web.CodeNotLive, "pane "+uid+" has no live runtime")
		}
		return node.Runtime.ID, nil
	}
	return "", web.NotFound("no pane " + uid)
}

func (b *webBackend) Layout(ctx context.Context, window string, contents bool) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	runtime, err := s.liveWindow(window)
	if err != nil {
		return nil, err
	}
	layout, err := termview.CaptureWindowLayout(ctx, inttmux.ExecRunner{}, b.transport.Args(), runtime, contents)
	if err != nil {
		return nil, web.NewError(http.StatusConflict, web.CodeNotLive, err.Error())
	}
	return layout, nil
}

func (b *webBackend) Screen(ctx context.Context, pane string) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	runtime, err := s.livePane(pane)
	if err != nil {
		return nil, err
	}
	screen, err := termview.CapturePane(ctx, inttmux.ExecRunner{}, b.transport.Args(), runtime)
	if err != nil {
		return nil, web.NewError(http.StatusConflict, web.CodeNotLive, err.Error())
	}
	return screen, nil
}

// webResumeCandidate is one Offline agent with enough of its conversation to
// recognize it. A name and a provider do not tell two sessions in the same
// repository apart; what it was asked and where it got to do.
type webResumeCandidate struct {
	UID      string `json:"uid"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Phase    string `json:"phase"`
	Opening  string `json:"opening,omitempty"`
	Last     string `json:"last,omitempty"`
	At       string `json:"at,omitempty"`
	Turns    int    `json:"turns"`
	Note     string `json:"note,omitempty"`
}

func (b *webBackend) ResumeCandidates(ctx context.Context, window string) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := s.registry.Window(window); !ok {
		return nil, web.NotFound("no window " + window)
	}
	bound := map[string]bool{}
	for _, node := range s.graph.Panes {
		if node.WindowUID == window && node.AgentUID != "" && node.Status == resourcegraph.StatusLive {
			bound[node.AgentUID] = true
		}
	}
	home, _ := os.UserHomeDir()
	rows := []webResumeCandidate{}
	for i := range s.registry.Agents {
		stored := s.registry.Agents[i]
		if stored.Metadata.OwnerUID() != window || bound[stored.Metadata.UID] {
			continue
		}
		agent, err := s.agent(stored.Metadata.UID)
		if err != nil {
			continue
		}
		row := webResumeCandidate{
			UID: agent.Metadata.UID, Name: agent.Metadata.Name,
			Provider: agent.Spec.Provider, Phase: string(agent.Status.Phase),
		}
		fillResumePreview(&row, agent, home)
		rows = append(rows, row)
	}
	// A session that can be recognized comes first, newest first. The rest are
	// still resumable, but on a long-lived window most are Offline rows whose
	// transcript is gone, and burying the useful ones makes the list useless.
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if (a.Turns > 0) != (b.Turns > 0) {
			return a.Turns > 0
		}
		return a.At > b.At
	})
	return map[string]any{"items": rows}, nil
}

func fillResumePreview(row *webResumeCandidate, agent coremetadata.Agent, home string) {
	path, err := transcript.Path(agent, home)
	if err != nil {
		row.Note = err.Error()
		return
	}
	read, err := transcript.ReadTranscript(strings.ToLower(agent.Spec.Provider), path, 400)
	if err != nil {
		row.Note = err.Error()
		return
	}
	for _, turn := range read.Turns {
		if turn.Text == "" {
			continue
		}
		row.Turns++
		if row.Opening == "" && (turn.Role == "user" || turn.Role == "peer") {
			row.Opening = clipLine(turn.Text)
		}
		row.Last = clipLine(turn.Text)
		if turn.At != "" {
			row.At = turn.At
		}
	}
	if row.Turns == 0 {
		row.Note = transcript.NoteEmpty
	}
}

// clipLine flattens a turn to the one line a picker row holds.
func clipLine(text string) string {
	line := strings.Join(strings.Fields(text), " ")
	if len(line) <= 160 {
		return line
	}
	cut := 160
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return line[:cut] + "…"
}

// questionRunner runs the tmux calls that answer a question; tests replace it.
var questionRunner question.Runner = inttmux.ExecRunner{}

// AnswerQuestion answers the agent's pending question with the widget's own
// keys. The question's shape comes from the transcript, the pane from this
// request's observation of the app server, and the pane must still carry the
// agent's pane uid before anything is pressed.
func (b *webBackend) AnswerQuestion(ctx context.Context, agentUID string, req web.QuestionAnswer) (any, error) {
	s, err := b.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	agent, err := s.agent(agentUID)
	if err != nil {
		return nil, err
	}
	if strings.ToLower(agent.Spec.Provider) != "claude" {
		return nil, web.NewError(http.StatusBadRequest, web.CodeUnsupported, "only a Claude agent asks questions this way")
	}
	home, _ := os.UserHomeDir()
	path, err := transcript.Path(agent, home)
	if err != nil {
		return nil, web.NewError(http.StatusConflict, transcript.NoteNoTranscript, err.Error())
	}
	read, err := transcript.ReadTranscript("claude", path, 40)
	if err != nil {
		return nil, web.NewError(http.StatusConflict, transcript.NoteNoTranscript, err.Error())
	}
	id, questions, err := question.Pending(read.Turns)
	if err != nil {
		return nil, questionError(err)
	}
	if req.ToolID != id {
		return nil, web.NewError(http.StatusConflict, "question-changed", "the question on screen is not the one pending; reload")
	}
	paneUID := agent.Status.PaneRef
	runtime, err := s.livePane(paneUID)
	if err != nil {
		return nil, err
	}
	answers := make([]question.Answer, len(req.Answers))
	for i, a := range req.Answers {
		answers[i] = question.Answer{Picks: a.Picks, Other: a.Other}
	}
	b.mutations.Lock()
	defer b.mutations.Unlock()
	pane := question.Pane{Runner: questionRunner, Server: b.transport.Args(), ID: runtime, UID: paneUID}
	if err := pane.Answer(ctx, questions, answers); err != nil {
		return nil, questionError(err)
	}
	return map[string]any{"ok": true, "toolId": id}, nil
}

func questionError(err error) error {
	if refusal, ok := question.IsRefusal(err); ok {
		status := http.StatusConflict
		if refusal.Code == question.CodeInvalid || refusal.Code == question.CodeUnmeasured {
			status = http.StatusBadRequest
		}
		return web.NewError(status, refusal.Code, refusal.Message)
	}
	return err
}
