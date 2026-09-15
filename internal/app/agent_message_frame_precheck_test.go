package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
)

// precheckPhrase restates the helper's fixed executable phrase independently.
const precheckPhrase = "the configured exact projmux executable"

// precheckToken has the assumed 48-byte length but not the sender's placeholder
// bytes, so an equal size proves the length rather than the value.
var precheckToken = strings.Repeat("9f", 24)

const (
	precheckCodexSourceUID = "agt-precheck-codex-source"
	precheckOriginalRef    = "message-precheck-original"
	precheckReplyAction    = "; retry manually with the same --reply-to and a new --message-ref (or omit --message-ref); original deadline and exact routes must remain valid"
)

type precheckClaudeAdapter struct {
	trace    *[]string
	store    *messagestore.Store
	reason   string
	payloads []string
}

func (a *precheckClaudeAdapter) Submit(_ context.Context, _ string, _ coremetadata.AgentRouteRef, envelope coremessage.Envelope) (agentdelivery.Delivery, error) {
	*a.trace = append(*a.trace, "adapter")
	a.payloads = append(a.payloads, envelope.Payload)
	if a.reason == "" {
		return agentdelivery.Delivery{MessageRef: envelope.MessageRef, State: agentdelivery.StateQueued}, nil
	}
	return agentdelivery.Delivery{MessageRef: envelope.MessageRef, State: agentdelivery.StateFailed, Reason: a.reason}, nil
}

func (a *precheckClaudeAdapter) Status(context.Context, string, coremetadata.AgentRouteRef, string) (agentdelivery.Delivery, error) {
	*a.trace = append(*a.trace, "adapter-status")
	return agentdelivery.Delivery{}, errors.New("status is not part of send")
}

// ExplicitReply creates the reply receipt the way the helper's broker does.
func (a *precheckClaudeAdapter) ExplicitReply(_ context.Context, _ string, _ coremetadata.AgentRouteRef, reply coremessage.Envelope) (string, bool, error) {
	*a.trace = append(*a.trace, "explicit-reply")
	record, created, err := a.store.PutReply(reply.ReplyTo, reply.MessageRef, reply.Payload, reply.Source, reply.Target, reply.AcceptedAt, reply.Deadline)
	if err != nil {
		return "", false, err
	}
	return record.Envelope.MessageRef, created, nil
}

type precheckFixture struct {
	cmd       *agentCommand
	trace     *[]string
	adapter   *precheckClaudeAdapter
	stateDir  string
	route     coremetadata.AgentRouteRef
	claudeUID string
	original  coremessage.Envelope
}

// newPrecheckFixture wires a Claude target, a Claude source and a Codex source
// with one delivered original request to reply to.
func newPrecheckFixture(t *testing.T, executable func() (string, error)) *precheckFixture {
	t.Helper()
	coordination := newClaudeCoordinationTestFixture(t)
	h := newSessionRefHarness(t, aiModeClaude)
	claude, ok := h.registry.Agent(h.agentUID)
	if !ok {
		t.Fatal("claude agent fixture missing")
	}
	codex := claude.Clone()
	codex.Metadata.UID = precheckCodexSourceUID
	codex.Metadata.Name = "precheck-codex-source"
	codex.Spec.Provider = aiModeCodex
	h.registry.Agents = append(h.registry.Agents, codex)
	registry := h.registry.Clone()

	stateDir := t.TempDir()
	base := messagestore.NewStore(stateDir)
	now := time.Now().UTC()
	public := publicMessageRoute(coordination.route)
	original := coremessage.Envelope{Version: coremessage.Version, MessageRef: precheckOriginalRef,
		ConversationRef: "conversation-precheck-original", Source: public, Target: public,
		Authority: coremessage.PeerAuthority(), Payload: "request", AcceptedAt: now, Deadline: now.Add(5 * time.Minute)}
	if _, _, err := base.PutAccepted(original, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := base.MarkHandoff(original.MessageRef); err != nil {
		t.Fatal(err)
	}
	if _, _, err := base.Apply(original.MessageRef, coremessage.Event{Kind: coremessage.EventDeliver, MessageRef: original.MessageRef,
		ConversationRef: original.ConversationRef, Target: original.Target, ObservedAt: now}); err != nil {
		t.Fatal(err)
	}

	var trace []string
	adapter := &precheckClaudeAdapter{trace: &trace, store: base}
	cmd := &agentCommand{
		activeTarget: insideTmux(h.paneUID, "").lookup,
		messagePaths: agentMessagePaths{registryPath: coordination.registryPath, loadRegistry: func() (coremetadata.Registry, error) {
			return registry.Clone(), nil
		}},
		messageStore:  &traceMessageStore{trace: &trace, base: base},
		messageRoute:  &traceMessageRouteResolver{trace: &trace, route: coordination.route},
		messageClaude: adapter,
		messageNow:    messageFixtureNow,
		messageNewRef: func(prefix string) string {
			t.Fatalf("explicit references must not mint %s", prefix)
			return ""
		},
		messageExecutable: executable,
	}
	return &precheckFixture{cmd: cmd, trace: &trace, adapter: adapter, stateDir: stateDir, route: coordination.route,
		claudeUID: h.agentUID, original: original}
}

func (f *precheckFixture) send(t *testing.T, sourceUID, ref string, reply bool, body string) (string, error) {
	t.Helper()
	args := []string{"message", "send", "uid:" + f.claudeUID, "--source", "uid:" + sourceUID, "--message-ref", ref}
	if reply {
		args = append(args, "--reply-to", f.original.MessageRef)
	}
	stdout, _, err := runRoute(t, f.cmd, append(args, "--", body)...)
	return stdout, err
}

// envelope is the independently expected broker envelope for one send.
func (f *precheckFixture) envelope(ref string, reply bool, body string) coremessage.Envelope {
	public := publicMessageRoute(f.route)
	envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: ref, ConversationRef: conversationRefFor(ref),
		Source: public, Target: public, Authority: coremessage.PeerAuthority(), Payload: body}
	if reply {
		envelope.ConversationRef, envelope.ReplyTo = f.original.ConversationRef, f.original.MessageRef
	}
	return envelope
}

func (f *precheckFixture) assertMutationZero(t *testing.T, ref string) {
	t.Helper()
	for _, call := range *f.trace {
		if call != "route" && call != "store-get" {
			t.Fatalf("pre-check refusal reached %s: %v", call, *f.trace)
		}
	}
	if len(f.adapter.payloads) != 0 {
		t.Fatalf("pre-check refusal submitted %d payloads", len(f.adapter.payloads))
	}
	reloaded := messagestore.NewStore(f.stateDir)
	if _, found, err := reloaded.Get(ref); found || (err != nil && !errors.Is(err, messagestore.ErrNotFound)) {
		t.Fatalf("pre-check refusal persisted a receipt: found=%t err=%v", found, err)
	}
	if _, found, err := reloaded.Reply(f.original.MessageRef); found || (err != nil && !errors.Is(err, messagestore.ErrNotFound)) {
		t.Fatalf("pre-check refusal created a reply receipt: found=%t err=%v", found, err)
	}
}

// precheckExpected renders independently of the sender: the helper's content
// renderer for the fixed phrase and every given executable, then a separate
// frame serialization with a 48-byte token. The larger frame and content win.
func precheckExpected(t *testing.T, envelope coremessage.Envelope, executables ...string) (frameBytes, contentBytes int) {
	t.Helper()
	for _, executable := range append([]string{precheckPhrase}, executables...) {
		content, err := providerCoordinationContent(claudeCoordinationEnvelope{BrokerEnvelope: &envelope}, executable)
		if err != nil {
			t.Fatal("coordination content unavailable")
		}
		frameBytes = max(frameBytes, len(serializedClaudeTestFrame(t, precheckToken, content)))
		contentBytes = max(contentBytes, len(content))
	}
	return frameBytes, contentBytes
}

// precheckBoundaryBody returns a body whose rendered frame is exactly
// frameBytes. A quote costs four frame bytes and an "x" one.
func precheckBoundaryBody(t *testing.T, envelope coremessage.Envelope, frameBytes int, executables ...string) string {
	t.Helper()
	envelope.Payload = "x"
	base, _ := precheckExpected(t, envelope, executables...)
	need := frameBytes - (base - 1)
	body := strings.Repeat(`"`, need/4) + strings.Repeat("x", need%4)
	envelope.Payload = body
	if got, _ := precheckExpected(t, envelope, executables...); got != frameBytes || len(body)+1 > coremessage.MaxPayloadBytes {
		t.Fatalf("boundary body renders %d frame bytes with %d payload bytes, want %d", got, len(body), frameBytes)
	}
	return body
}

func staticExecutable(path string) func() (string, error) {
	return func() (string, error) { return path, nil }
}

func TestAgentMessageSendRefusesClaudeBodyWhoseRenderedFrameExceedsBudgetBeforeAcceptance(t *testing.T) {
	const executable = "/home/user/go/bin/projmux"
	body := claudeFrameBudgetSymbolBody()
	for _, test := range []struct {
		name, ref string
		codex     bool
		reply     bool
	}{
		{name: "plain send", ref: "message-precheck-plain"},
		{name: "claude source reply", ref: "message-precheck-claude-reply", reply: true},
		{name: "codex source reply to claude target", ref: "message-precheck-codex-reply", codex: true, reply: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPrecheckFixture(t, staticExecutable(executable))
			sourceUID := f.claudeUID
			if test.codex {
				sourceUID = precheckCodexSourceUID
			}
			frameBytes, _ := precheckExpected(t, f.envelope(test.ref, test.reply, body), executable)
			if frameBytes <= claudeProviderFrameMaxBytes {
				t.Fatalf("fixture frame %d bytes is within the budget", frameBytes)
			}
			stdout, err := f.send(t, sourceUID, test.ref, test.reply, body)
			want := fmt.Sprintf("provider-frame-too-large: frameBytes=%d limitBytes=8192", frameBytes)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want %s", err, want)
			}
			if stdout != "" {
				t.Fatalf("refusal wrote a receipt: %q", stdout)
			}
			for _, secret := range []string{body[:64], precheckToken, strings.Repeat("0", claudeSendAssumedTokenBytes)} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("refusal echoed payload or token bytes")
				}
			}
			f.assertMutationZero(t, test.ref)
		})
	}
}

func TestAgentMessageSendWithinFrameBudgetBodyKeepsAcceptThenPushOrder(t *testing.T) {
	const executable = "/opt/pmx/projmux"
	t.Run("largest body within budget", func(t *testing.T) {
		f := newPrecheckFixture(t, staticExecutable(executable))
		const ref = "message-precheck-within"
		body := precheckBoundaryBody(t, f.envelope(ref, false, ""), claudeProviderFrameMaxBytes, executable)
		stdout, err := f.send(t, f.claudeUID, ref, false, body)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(*f.trace, ","); got != "route,route,store-put,adapter" {
			t.Fatalf("call order = %s", got)
		}
		if stdout != ref+"\taccepted\n" || len(f.adapter.payloads) != 1 || f.adapter.payloads[0] != body {
			t.Fatalf("stdout=%q submitted=%d", stdout, len(f.adapter.payloads))
		}
		if record, found, err := messagestore.NewStore(f.stateDir).Get(ref); err != nil || !found || record.Envelope.Payload != body {
			t.Fatalf("accepted record found=%t err=%v", found, err)
		}
	})
	t.Run("one byte over", func(t *testing.T) {
		f := newPrecheckFixture(t, staticExecutable(executable))
		const ref = "message-precheck-over"
		body := precheckBoundaryBody(t, f.envelope(ref, false, ""), claudeProviderFrameMaxBytes, executable) + "x"
		_, err := f.send(t, f.claudeUID, ref, false, body)
		if err == nil || !strings.Contains(err.Error(), "provider-frame-too-large: frameBytes=8193 limitBytes=8192") {
			t.Fatalf("error = %v", err)
		}
		if got := strings.Join(*f.trace, ","); got != "route,route" {
			t.Fatalf("call order = %s", got)
		}
		f.assertMutationZero(t, ref)
	})
}

func TestAgentMessageSendCodexTargetHasNoClaudeFramePrecheck(t *testing.T) {
	cmd, registryStore, _ := exactControlCLICommand(t)
	var trace []string
	stateDir := t.TempDir()
	route := mustMessageRoute(t, registryStore.registry, "agt-alpha-codex")
	cmd.activeTarget = insideTmux("pan-alpha-codex", "win-alpha-main").lookup
	cmd.messagePaths = agentMessagePaths{loadRegistry: func() (coremetadata.Registry, error) { return registryStore.registry.Clone(), nil }}
	cmd.messageStore = &traceMessageStore{trace: &trace, base: messagestore.NewStore(stateDir)}
	cmd.messageRoute = &traceMessageRouteResolver{trace: &trace, route: route}
	cmd.messageNow = messageFixtureNow
	cmd.messageNewRef = func(prefix string) string {
		t.Fatalf("explicit message ref must not mint %s", prefix)
		return ""
	}
	cmd.messageExecutable = func() (string, error) {
		t.Error("a Codex target rendered a Claude push frame")
		return "", errors.New("unused")
	}
	cmd.controlCall = func(context.Context, string, coremetadata.CodexEndpointRef, codexLifecycleIdentity, agentControlRequest) (agentControlResponse, error) {
		return agentControlResponse{OK: true, ThreadID: "thread-1", TurnID: "turn-1"}, nil
	}
	const ref = "message-codex-over-claude-budget"
	body := strings.Repeat("<", coremessage.MaxPayloadBytes)
	public := publicMessageRoute(route)
	if frameBytes, _ := precheckExpected(t, coremessage.Envelope{MessageRef: ref, ConversationRef: conversationRefFor(ref),
		Source: public, Target: public, Payload: body}); frameBytes <= claudeProviderFrameMaxBytes {
		t.Fatalf("fixture body renders %d Claude frame bytes; it must exceed the budget", frameBytes)
	}
	stdout, _, err := runRoute(t, cmd, "message", "send", "uid:agt-alpha-codex", "--message-ref", ref, "--", body)
	if err != nil || stdout != ref+"\tdelivered\n" {
		t.Fatalf("stdout=%q err=%v", stdout, err)
	}
	if got := strings.Join(trace, ","); !strings.Contains(got, "route,route,store-put") {
		t.Fatalf("call order = %s", got)
	}
	record, found, err := messagestore.NewStore(stateDir).Get(ref)
	if err != nil || !found || record.Envelope.Payload != body || record.Delivery.State != coremessage.StateDelivered {
		t.Fatalf("codex record found=%t err=%v", found, err)
	}
}

func TestAgentMessageSendFramePrecheckUsesLongerExecutableAndAssumedTokenLength(t *testing.T) {
	if claudeSendAssumedTokenBytes != 48 {
		t.Fatalf("assumed token length = %d", claudeSendAssumedTokenBytes)
	}
	// A real messaging token in the environment must never reach the render.
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", strings.Repeat("t", 4096))
	const short = "/p"
	long := "/" + strings.Repeat("l", 300) + "/projmux"
	f := newPrecheckFixture(t, nil)
	envelope := f.envelope("message-precheck-executable", false, "coordination body")
	render := func(executable func() (string, error)) claudeSendRender {
		t.Helper()
		got, err := (&agentCommand{messageExecutable: executable}).claudeSendFrameRender(f.route, envelope)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	phraseFrame, phraseContent := precheckExpected(t, envelope)
	longFrame, longContent := precheckExpected(t, envelope, long)
	if longFrame <= phraseFrame || longContent <= phraseContent {
		t.Fatal("long executable fixture does not lengthen the render")
	}
	for name, executable := range map[string]func() (string, error){
		"shorter executable":     staticExecutable(short),
		"empty executable":       staticExecutable(""),
		"executable unavailable": func() (string, error) { return "", errors.New("no executable") },
	} {
		if got := render(executable); got.frameBytes != phraseFrame || got.contentBytes != phraseContent {
			t.Fatalf("%s: render=%+v, want fixed phrase frame=%d content=%d", name, got, phraseFrame, phraseContent)
		}
	}
	if got := render(staticExecutable(long)); got.frameBytes != longFrame || got.contentBytes != longContent {
		t.Fatalf("longer executable render=%+v, want frame=%d content=%d", got, longFrame, longContent)
	}
	own, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if ownFrame, ownContent := precheckExpected(t, envelope, own); (&agentCommand{}).claudeSendFrameRenderMust(t, f.route, envelope) != (claudeSendRender{ownFrame, ownContent}) {
		t.Fatal("unset seam does not render this process's own executable")
	}
	content, err := providerCoordinationContent(claudeCoordinationEnvelope{BrokerEnvelope: &envelope}, long)
	if err != nil {
		t.Fatal(err)
	}
	for _, tokenBytes := range []int{47, 48, 49} {
		if frame := len(serializedClaudeTestFrame(t, strings.Repeat("a", tokenBytes), content)); (frame == longFrame) != (tokenBytes == 48) {
			t.Fatalf("token %d bytes renders %d frame bytes; sender rendered %d", tokenBytes, frame, longFrame)
		}
	}

	// The same body is within budget for the fixed phrase and over it for a
	// longer sender executable, so the longer rendering decides the refusal.
	const ref = "message-precheck-longer"
	body := precheckBoundaryBody(t, f.envelope(ref, false, ""), claudeProviderFrameMaxBytes)
	accepted := newPrecheckFixture(t, staticExecutable(short))
	if _, err := accepted.send(t, accepted.claudeUID, ref, false, body); err != nil {
		t.Fatalf("phrase-bounded body refused: %v", err)
	}
	if got := strings.Join(*accepted.trace, ","); got != "route,route,store-put,adapter" {
		t.Fatalf("call order = %s", got)
	}
	refused := newPrecheckFixture(t, staticExecutable(long))
	wantFrame, _ := precheckExpected(t, refused.envelope(ref, false, body), long)
	_, err = refused.send(t, refused.claudeUID, ref, false, body)
	if wantFrame <= claudeProviderFrameMaxBytes || err == nil ||
		!strings.Contains(err.Error(), fmt.Sprintf("frameBytes=%d limitBytes=8192", wantFrame)) {
		t.Fatalf("longer executable frame=%d error=%v", wantFrame, err)
	}
	refused.assertMutationZero(t, ref)
}

func (c *agentCommand) claudeSendFrameRenderMust(t *testing.T, route coremetadata.AgentRouteRef, envelope coremessage.Envelope) claudeSendRender {
	t.Helper()
	got, err := c.claudeSendFrameRender(route, envelope)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestAgentMessageSendFramePrecheckCoversEveryPrivateEnvelopeSizeRefusal sweeps
// escape-heavy body families against the private IPC envelope size check that
// Submit runs after acceptance. Every body the pre-check admits must fit that
// check; otherwise a size refusal would still be persisted without a size.
func TestAgentMessageSendFramePrecheckCoversEveryPrivateEnvelopeSizeRefusal(t *testing.T) {
	f := newPrecheckFixture(t, staticExecutable("/p"))
	// The shortest sender executable leaves only the fixed phrase, which is
	// the smallest frame the pre-check can render.
	sender := &agentCommand{messageExecutable: staticExecutable("/p")}
	now := time.Now().UTC()

	// Production-length refs, UIDs, session and times around the body.
	fixtureTarget, ok := claudeTargetForRoute(f.route)
	if !ok {
		t.Fatal("exact Claude target unavailable")
	}
	target := fixtureTarget
	target.AgentUID, target.PaneUID = "agent-01k2v7q9m3x8c4n5b6t0r1y2z7", "pane-01k2v7q9m3x8c4n5b6t0r1y2z8"
	target.Generation = "gen-01k2v7q9m3x8c4n5b6t0r1y2z9"
	target.Authority.SessionID = "0f8b6c2e-6a1d-4c3b-9e7f-2d5a8b1c4e6f"
	target.Authority.RegistrationGeneration = newCoordinationRef("registration")
	production := func(body string, reply bool) coremessage.Envelope {
		envelope := coremessage.Envelope{Version: coremessage.Version, MessageRef: newCoordinationRef("message"),
			ConversationRef: newCoordinationRef("conversation"),
			Source: coremessage.Route{AgentUID: "agent-01k2v7q9m3x8c4n5b6t0r1y2z3", PaneUID: "pane-01k2v7q9m3x8c4n5b6t0r1y2z4",
				ActivationGeneration: "gen-01k2v7q9m3x8c4n5b6t0r1y2z5", Provider: "codex", Incarnation: "incarnation-01k2v7q9m3x8c4n5b6t0r1y2z6"},
			Target: coremessage.Route{AgentUID: target.AgentUID, PaneUID: target.PaneUID, ActivationGeneration: target.Generation,
				Provider: "claude", Incarnation: f.route.Incarnation()},
			Authority: coremessage.PeerAuthority(), Payload: body,
			AcceptedAt: now.Add(123456789 * time.Nanosecond), Deadline: now.Add(10*time.Minute + 987654321*time.Nanosecond)}
		if reply {
			envelope.ReplyTo = newCoordinationRef("message")
		}
		return envelope
	}
	ipcBytes := func(t *testing.T, envelope coremessage.Envelope) int {
		t.Helper()
		payload, err := json.Marshal(claudePrivateCoordinationEnvelope(target, envelope))
		if err != nil {
			t.Fatal(err)
		}
		return len(payload) + 1
	}
	frameBytes := func(t *testing.T, envelope coremessage.Envelope) int {
		t.Helper()
		return sender.claudeSendFrameRenderMust(t, f.route, envelope).frameBytes
	}

	shapes := []struct{ name, unit string }{
		{"less-than", "<"}, {"greater-than", ">"}, {"ampersand", "&"}, {"html-newline", "<>&\n"},
		{"quote", `"`}, {"backslash", `\`}, {"quote-backslash", `"\`}, {"quote-ascii", `"x`},
		{"newline", "\n"}, {"tab", "\t"}, {"escape-control", "\x1b"}, {"line-separator", " "},
		{"korean", "한"}, {"ascii", "y"},
	}
	families := t.Run("families", func(t *testing.T) {
		for _, shape := range shapes {
			for _, reply := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/reply=%t", shape.name, reply), func(t *testing.T) {
					t.Parallel()
					body := func(units int, fill bool) string {
						value := strings.Repeat(shape.unit, units)
						if fill {
							value += strings.Repeat("x", coremessage.MaxPayloadBytes-len(value))
						}
						return value
					}
					maxUnits := coremessage.MaxPayloadBytes / len(shape.unit)
					// A unit JSON leaves unescaped costs the same as the "x" it
					// replaces, so its family has one size and is only sampled.
					step := 1
					if encoded, _ := json.Marshal(shape.unit); len(encoded) == len(shape.unit)+2 {
						step = 97
					}
					counts := make([]int, 0, maxUnits/step+2)
					for units := 0; units < maxUnits; units += step {
						counts = append(counts, units)
					}
					counts = append(counts, maxUnits)
					lastPass, lastPassIPC, lastPassFrame := -1, 0, 0
					firstRefusedIPC, firstRefusedFrame := 0, 0
					for _, units := range counts {
						envelope := production(body(units, true), reply)
						if err := envelope.Validate(); err != nil {
							t.Fatalf("units=%d: public envelope invalid: %v", units, err)
						}
						frame, ipc := frameBytes(t, envelope), ipcBytes(t, envelope)
						if frame > claudeProviderFrameMaxBytes {
							firstRefusedIPC, firstRefusedFrame = ipc, frame
							break
						}
						lastPass, lastPassIPC, lastPassFrame = units, ipc, frame
						if ipc > claudeProviderFrameMaxBytes {
							t.Errorf("ESCAPE units=%d payloadBytes=%d: frame=%d passes the pre-check but private IPC=%d is refused",
								units, len(envelope.Payload), frame, ipc)
						}
					}
					// Above the first refusal, and for unfilled bodies of every
					// length, an IPC size refusal must always be a pre-check refusal.
					refusedIPC := false
					for _, fill := range []bool{true, false} {
						for units := max(lastPass, 1); units <= maxUnits; units += 29 {
							envelope := production(body(units, fill), reply)
							if ipc := ipcBytes(t, envelope); ipc > claudeProviderFrameMaxBytes {
								refusedIPC = true
								if frame := frameBytes(t, envelope); frame <= claudeProviderFrameMaxBytes {
									t.Errorf("ESCAPE units=%d fill=%t: frame=%d ipc=%d", units, fill, frame, ipc)
								}
							}
						}
					}
					t.Logf("last pre-check pass units=%d ipc=%d frame=%d; first refusal ipc=%d frame=%d; ipc-refused-seen=%t",
						lastPass, lastPassIPC, lastPassFrame, firstRefusedIPC, firstRefusedFrame, refusedIPC)
				})
			}
		}
	})
	if !families {
		t.Fatal("a body passed the frame pre-check but fails the private IPC size check")
	}

	// The swept size rule is the real valid() check, and the CLI reports the
	// smallest IPC-refused body of the sharpest family with its frame size.
	route := f.route
	private, _ := claudeTargetForRoute(route)
	const ref = "message-precheck-ipc-boundary"
	for units := 0; units <= coremessage.MaxPayloadBytes; units++ {
		payload := strings.Repeat("<", units) + strings.Repeat("x", coremessage.MaxPayloadBytes-units)
		envelope := f.envelope(ref, false, payload)
		envelope.AcceptedAt, envelope.Deadline = now, now.Add(time.Minute)
		payloadJSON, _ := json.Marshal(claudePrivateCoordinationEnvelope(private, envelope))
		sizeRefused := len(payloadJSON)+1 > claudeProviderFrameMaxBytes
		if claudePrivateCoordinationEnvelope(private, envelope).valid(now, route) == sizeRefused {
			t.Fatalf("units=%d: valid() disagrees with the private envelope size rule", units)
		}
		if !sizeRefused {
			continue
		}
		wantFrame, _ := precheckExpected(t, envelope, "/p")
		_, err := f.send(t, f.claudeUID, ref, false, payload)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("frameBytes=%d limitBytes=8192", wantFrame)) {
			t.Fatalf("smallest IPC-refused body (units=%d ipc=%d) error=%v", units, len(payloadJSON)+1, err)
		}
		f.assertMutationZero(t, ref)
		t.Logf("fixture route: smallest IPC-refused '<' body units=%d ipc=%d refused at frame=%d", units, len(payloadJSON)+1, wantFrame)
		return
	}
	t.Fatal("no body reached the private envelope size refusal")
}

func TestAgentMessageFailureActionNamesRenderedContentBytesForOldHelperInvalidContent(t *testing.T) {
	const executable = "/home/user/go/bin/projmux"
	const invalid = "provider-frame-invalid-content"
	large := strings.Repeat("x", coremessage.MaxPayloadBytes)
	diagnosis := func(contentBytes int) []string {
		return []string{fmt.Sprintf("rendered push content is %d bytes", contentBytes), "4096-byte content limit",
			"re-activate (restart) the target Agent"}
	}
	for _, test := range []struct {
		name, ref, reason, body string
		codex, reply, diagnose  bool
		oldAction               string
	}{
		{name: "plain large invalid content", ref: "message-old-helper-plain", reason: invalid, body: large, diagnose: true},
		{name: "plain small invalid content", ref: "message-old-helper-small", reason: invalid, body: "hello",
			oldAction: "correct message content or configuration before retrying"},
		{name: "plain large other reason", ref: "message-old-helper-write-zero", reason: "provider-write-zero", body: large,
			oldAction: "check provider connection before retrying"},
		{name: "claude source reply large invalid content", ref: "message-old-helper-claude-reply", reason: invalid, body: large,
			reply: true, diagnose: true},
		{name: "codex source reply large invalid content", ref: "message-old-helper-codex-reply", reason: invalid, body: large,
			codex: true, reply: true, diagnose: true},
		{name: "claude source reply small invalid content", ref: "message-old-helper-small-reply", reason: invalid, body: "hello",
			reply: true, oldAction: "correct message content or configuration before retrying" + precheckReplyAction},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPrecheckFixture(t, staticExecutable(executable))
			f.adapter.reason = test.reason
			sourceUID := f.claudeUID
			if test.codex {
				sourceUID = precheckCodexSourceUID
			}
			frameBytes, contentBytes := precheckExpected(t, f.envelope(test.ref, test.reply, test.body), executable)
			if frameBytes > claudeProviderFrameMaxBytes || (contentBytes > coremessage.MaxPayloadBytes) != (test.body == large) {
				t.Fatalf("fixture frame=%d content=%d", frameBytes, contentBytes)
			}
			stdout, err := f.send(t, sourceUID, test.ref, test.reply, test.body)
			// Only the Claude-source reply path already returns its failure;
			// the exit status of other accepted-then-failed sends is unchanged.
			if claudeReply := test.reply && !test.codex; (err != nil) != claudeReply {
				t.Fatalf("error = %v", err)
			}
			prefix := test.ref + "\tfailed\t" + test.reason + "\t"
			if !strings.HasPrefix(stdout, prefix) || !strings.HasSuffix(stdout, "\n") {
				t.Fatalf("receipt = %q", stdout)
			}
			action := strings.TrimSuffix(strings.TrimPrefix(stdout, prefix), "\n")
			if test.diagnose {
				for _, want := range diagnosis(contentBytes) {
					if !strings.Contains(action, want) || (err != nil && !strings.Contains(err.Error(), want)) {
						t.Fatalf("action %q or error %v omits %q", action, err, want)
					}
				}
				if test.reply && !strings.HasSuffix(action, precheckReplyAction) {
					t.Fatalf("reply action lost manual retry guidance: %q", action)
				}
			} else if action != test.oldAction {
				t.Fatalf("action = %q, want unchanged %q", action, test.oldAction)
			}

			reloaded, found, getErr := messagestore.NewStore(f.stateDir).Get(test.ref)
			if getErr != nil || !found || reloaded.Delivery.State != coremessage.StateFailed ||
				reloaded.Delivery.Reason != test.reason || reloaded.Delivery.OutcomeUnknown {
				t.Fatalf("persisted delivery=%+v found=%t err=%v", reloaded.Delivery, found, getErr)
			}
			// The diagnosis belongs to the sender only; status keeps the old
			// action because it has no rendered content.
			var status strings.Builder
			if err := writeAgentMessageReceipt(&status, receiptFor(reloaded), false); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(status.String(), "rendered push content") {
				t.Fatalf("status receipt carried the sender diagnosis: %q", status.String())
			}
		})
	}

	unknown := coremessage.Delivery{State: coremessage.StateFailed, Reason: invalid, OutcomeUnknown: true}
	if got := agentMessageSendFailureAction(unknown, 5000); got != agentMessageFailureAction(unknown) {
		t.Fatalf("unknown outcome action changed: %q", got)
	}
	var codex strings.Builder
	receipt := agentMessageReceipt{MessageRef: "message-codex-invalid", Target: coremessage.Route{Provider: "codex"},
		Delivery: coremessage.Delivery{State: coremessage.StateFailed, Reason: invalid}}
	if err := writeAgentMessageReceiptText(&codex, receipt, 5000); err != nil ||
		codex.String() != "message-codex-invalid\tfailed\t"+invalid+"\tcorrect message content or configuration before retrying\n" {
		t.Fatalf("codex receipt = %q err=%v", codex.String(), err)
	}
}
