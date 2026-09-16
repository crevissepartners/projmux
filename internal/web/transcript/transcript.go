// Package transcript reads the conversation files AI providers write and
// turns them into one provider-neutral shape the web client can render.
//
// Claude, Codex and Antigravity each keep an append-only jsonl log in their own
// format. Nothing here writes to those files or feeds what it reads back into
// the Registry: the web client is a viewer, and the logs are read only so a
// person can see what an Agent said without attaching to its Pane.
//
// Everything this package emits is structure rather than prose. Labels a person
// reads ("thinking", "clipped", "empty conversation") are left to the client,
// which owns localization; the server hands over booleans and stable tokens.
package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

// Turn is one rendered entry in an agent conversation.
//
// The three providers write three unrelated transcript formats, so this is a
// lowest common denominator on purpose: who spoke, what text, when — plus the
// tool calls the turn made, which are what most of a working session actually
// consists of.
type Turn struct {
	Role string `json:"role"` // user | assistant | system | tool | peer
	Text string `json:"text"`
	At   string `json:"at,omitempty"`
	Kind string `json:"kind,omitempty"` // provider-native record type
	// Thinking is true when the turn carried a reasoning block. The reasoning
	// itself is not exported (providers store it redacted or signed), but a
	// turn that only thought still happened, and dropping it would make the
	// assistant look idle.
	Thinking bool `json:"thinking,omitempty"`
	// From names the peer Agent a coordination message came from. It is nil
	// for everything else, including a self-anchored coordination frame,
	// which is this viewer's own composer rather than a peer.
	From *Sender `json:"from,omitempty"`
	// MessageRef is the coordination frame's message reference, so a client
	// can match a message it sent against the turn that recorded it.
	MessageRef string `json:"messageRef,omitempty"`
	// Via names the client a coordination message came through, when it said
	// so. Empty for anything typed at the terminal.
	Via   string     `json:"via,omitempty"`
	Tools []ToolCall `json:"tools,omitempty"`
	// Model and Effort are what the provider ran this turn with, when its
	// record says. A Codex turn_context record is a turn of kind "context"
	// that carries only these.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Task is set on a task-notification turn, Report on a subagent's report.
	Task   *Task   `json:"task,omitempty"`
	Report *Report `json:"report,omitempty"`
	// Images counts pictures attached to the message. They are not served;
	// the count keeps an image-only message from vanishing.
	Images int `json:"images,omitempty"`
}

// Sender identifies the peer a coordination message came from.
type Sender struct {
	AgentUID string `json:"agentUID"`
	Provider string `json:"provider,omitempty"`
}

// ToolCall is one tool invocation, with its result linked back in when the
// transcript carries it.
//
// Call and result are separate records in every format — the result arrives in
// a later entry keyed by id — so they are stitched back together after the file
// is read rather than being shown as two unrelated lines.
type ToolCall struct {
	ID string `json:"id,omitempty"`
	// Name is the tool, Summary the one argument worth reading in a list
	// (a command, a path, a pattern), and Input the full arguments.
	Name    string `json:"name"`
	Summary string `json:"summary,omitempty"`
	Input   string `json:"input,omitempty"`
	Result  string `json:"result,omitempty"`
	Error   bool   `json:"error,omitempty"`
	// Clipped is true when Input or Result was cut to the display limit, so
	// the client can say so instead of presenting a partial blob as whole.
	Clipped bool `json:"clipped,omitempty"`
}

// Note tokens explain an empty Transcript. They are stable identifiers, not
// prose: the client maps them to localized text.
const (
	// NoteEmpty: the file was read but held no renderable turn yet — only
	// bookkeeping records, or nothing at all.
	NoteEmpty = "empty"
	// NoteNoTranscript: the Agent has no transcript to read (the provider has
	// not reported one yet, or the file is gone). Set by callers when Path or
	// the open fails, so the client can tell "nothing said" from "nothing to
	// read".
	NoteNoTranscript = "no-transcript"
)

// Transcript is a bounded tail of one agent conversation.
type Transcript struct {
	Provider string `json:"provider"`
	Path     string `json:"path,omitempty"`
	Turns    []Turn `json:"turns"`
	// Truncated is true when older entries were dropped to honour the limit.
	Truncated bool `json:"truncated"`
	// Note explains an empty result instead of leaving the panel blank. It is
	// one of the Note* tokens above.
	Note string `json:"note,omitempty"`
	// Offset is the byte position after the last complete line read. A
	// follower started there misses nothing written after this read.
	Offset int64 `json:"offset"`
}

// toolTextLimit bounds an argument blob or a tool result. A single result can
// be megabytes; the panel wants enough to recognize it, not all of it.
const toolTextLimit = 1200

// summaryLimit bounds the one-line summary of a call.
const summaryLimit = 200

// clip trims text and cuts it to limit bytes, reporting whether it cut.
func clip(text string, limit int) (string, bool) {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text, false
	}
	// Cut on a rune boundary so the JSON stays valid UTF-8.
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut], true
}

// clipSummary cuts a one-line summary. A summary has no flag of its own to
// carry the cut, and it is a list label rather than content, so an ellipsis
// is enough to show that it continues.
func clipSummary(text string) string {
	out, clipped := clip(text, summaryLimit)
	if clipped {
		out += "…"
	}
	return out
}

// toolSummary picks the one argument that identifies the call. The names are
// the ones the three providers actually use; anything else falls back to the
// first short string argument so an unknown tool is still readable.
func toolSummary(input map[string]any) string {
	for _, key := range []string{
		"command", "cmd", "file_path", "path", "pattern", "query", "url",
		"prompt", "description", "notebook_path", "skill",
	} {
		if value := strings.TrimSpace(stringOf(input[key])); value != "" {
			return clipSummary(value)
		}
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if value := strings.TrimSpace(stringOf(input[key])); value != "" && len(value) < summaryLimit {
			return value
		}
	}
	return ""
}

// defaultLimit is the number of spoken turns ReadTranscript keeps when the
// caller does not say.
const defaultLimit = 200

// ReadTranscript parses the last `limit` spoken turns from path.
func ReadTranscript(provider, path string, limit int) (*Transcript, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	file, err := os.Open(path) // #nosec G304 -- read-only open of the transcript path the provider hook reported.
	if err != nil {
		return nil, err
	}
	defer file.Close()

	out := &Transcript{Provider: provider, Path: path}
	scanner := bufio.NewScanner(file)
	// Transcript lines carry whole tool outputs and can be very long; the
	// default 64KB token limit drops them silently.
	scanner.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)
	// Offset counts only complete lines. A trailing line with no newline yet
	// is a record the provider is still writing; a follower that starts here
	// reads it whole once it is finished.
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		advance, token, err := bufio.ScanLines(data, atEOF)
		if advance > 0 && data[advance-1] == '\n' {
			out.Offset += int64(advance)
		}
		return advance, token, err
	})

	parse := parserFor(provider)
	total := 0
	speaking := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] != '{' {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue // a partially written tail line is normal on a live file
		}
		turn, ok := parse(raw)
		if !ok {
			continue
		}
		total++
		out.Turns = append(out.Turns, turn)
		if speaks(turn) {
			speaking++
		}
		// The limit counts what was *said*, not what was recorded.
		//
		// Counting every record made the window useless on a busy session:
		// once tool calls became their own turns, a tail of 200 records was
		// 199 tool calls and one message, and the peer and user turns — the
		// thing the panel exists to show — had already scrolled out. The hard
		// cap is only there so a session that is nothing but tool calls cannot
		// grow this slice without bound.
		for len(out.Turns) > 0 && (speaking > limit || len(out.Turns) > hardTurnCap) {
			if speaks(out.Turns[0]) {
				speaking--
			}
			out.Turns = out.Turns[1:]
			out.Truncated = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	out.Turns = linkTools(out.Turns)
	if total == 0 {
		out.Note = NoteEmpty
	}
	return out, nil
}

// linkTools merges each tool result into the call it answers and then drops
// the turns that carried nothing else.
//
// A result that arrives before its call was read — the tail was truncated, or
// the call is older than the window — has nowhere to go, so it is kept as a
// nameless entry rather than thrown away, which would make a visible gap.
func linkTools(turns []Turn) []Turn {
	type slot struct{ turn, tool int }
	byID := map[string]slot{}
	for ti := range turns {
		for ci, call := range turns[ti].Tools {
			if call.Name != "" && call.ID != "" {
				byID[call.ID] = slot{ti, ci}
			}
		}
	}

	// Pass one writes every result onto its call. It has to finish before
	// anything is rewritten, because pass two replaces the Tools slices and a
	// write aimed at the old slice would then be lost.
	orphaned := make(map[int][]ToolCall)
	for ti := range turns {
		for _, call := range turns[ti].Tools {
			if call.Name != "" {
				continue // this is the call itself, not a result
			}
			if at, ok := byID[call.ID]; ok {
				target := &turns[at.turn].Tools[at.tool]
				target.Result = call.Result
				target.Error = call.Error
				// The call's own flag may already say its input was cut; a
				// clipped result must not clear that.
				target.Clipped = target.Clipped || call.Clipped
				continue
			}
			// A result whose call is older than the window has nowhere to go.
			// Keeping it beats dropping it, which would leave a visible gap.
			if call.Result != "" {
				orphaned[ti] = append(orphaned[ti], call)
			}
		}
	}

	kept := make([]Turn, 0, len(turns))
	for ti := range turns {
		turn := turns[ti]
		turn.Tools = append(filterCalls(turn.Tools), orphaned[ti]...)
		if !hasContent(turn) {
			continue
		}
		kept = append(kept, turn)
	}
	return kept
}

// hasContent reports whether a turn still shows anything once its tool
// results have been moved onto their calls.
//
// A coordination turn is kept even with an empty payload: a message was
// delivered, and its sender and reference are what the client shows. A
// thinking-only turn is kept for the same reason the marker exists at all, and
// a task notification or an image-only message carries its content outside
// Text.
func hasContent(turn Turn) bool {
	return turn.Text != "" || len(turn.Tools) > 0 || turn.Thinking || isCoordinationKind(turn.Kind) ||
		turn.Task != nil || turn.Images > 0 || turn.Kind == KindContext
}

// filterCalls keeps only the entries that are real calls.
func filterCalls(tools []ToolCall) []ToolCall {
	out := make([]ToolCall, 0, len(tools))
	for _, call := range tools {
		if call.Name != "" {
			out = append(out, call)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// hardTurnCap bounds the slice when a session is almost entirely tool calls.
const hardTurnCap = 4000

// speaks reports whether a turn is something someone said, as opposed to a
// record of a tool the assistant used.
//
// A thinking-only assistant turn counts: it is rendered as its own row, the
// same as it was when the marker was carried in the text.
func speaks(turn Turn) bool {
	if turn.Kind == KindContext {
		return false
	}
	switch turn.Role {
	case "user", "peer", "system":
		return true
	}
	return strings.TrimSpace(turn.Text) != "" || turn.Thinking
}

type lineParser func(map[string]any) (Turn, bool)

func parserFor(provider string) lineParser {
	switch strings.ToLower(provider) {
	case "claude":
		return parseClaudeLine
	case "codex":
		return parseCodexLine
	case "antigravity":
		return parseAntigravityLine
	}
	return func(map[string]any) (Turn, bool) { return Turn{}, false }
}

func stringOf(value any) string {
	text, _ := value.(string)
	return text
}
