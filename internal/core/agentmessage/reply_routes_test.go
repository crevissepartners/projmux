package agentmessage

import (
	"errors"
	"strings"
	"testing"
)

// TestReplyRoutesFollowsTheAgentAndItsConversation pins C-1: a reply
// correlates by Agent, provider and conversation. A relaunch into the same
// conversation moves either Agent to a new Pane and activation generation and
// still correlates; another conversation of the same Agents is named as such;
// another Agent or provider stays a correlation mismatch.
func TestReplyRoutesFollowsTheAgentAndItsConversation(t *testing.T) {
	t.Parallel()
	relaunch := func(r *Route) {
		r.PaneUID, r.ActivationGeneration = r.PaneUID+"-relaunched", r.ActivationGeneration+"-relaunched"
	}
	for _, test := range []struct {
		name   string
		mutate func(reply *Envelope)
		want   error
	}{
		{"exact reverse", func(*Envelope) {}, nil},
		{"original sender relaunched", func(e *Envelope) { relaunch(&e.Target) }, nil},
		{"replying receiver relaunched", func(e *Envelope) { relaunch(&e.Source) }, nil},
		{"both relaunched", func(e *Envelope) { relaunch(&e.Source); relaunch(&e.Target) }, nil},
		{"original sender in another conversation", func(e *Envelope) { relaunch(&e.Target); e.Target.Incarnation = "route-other" }, ErrReplyConversationChanged},
		{"replying receiver in another conversation", func(e *Envelope) { e.Source.Incarnation = "route-other" }, ErrReplyConversationChanged},
		{"to another Agent", func(e *Envelope) { e.Target.AgentUID = "agent-other" }, ErrInvalidEnvelope},
		{"from another Agent", func(e *Envelope) { e.Source.AgentUID = "agent-other" }, ErrInvalidEnvelope},
		{"to another provider", func(e *Envelope) { e.Target.Provider = "claude" }, ErrInvalidEnvelope},
		{"from another provider", func(e *Envelope) { e.Source.Provider = "codex" }, ErrInvalidEnvelope},
		{"another Agent in another conversation", func(e *Envelope) { e.Target.AgentUID, e.Target.Incarnation = "agent-other", "route-other" }, ErrInvalidEnvelope},
		{"not reversed", func(e *Envelope) { e.Source, e.Target = e.Target, e.Source }, ErrInvalidEnvelope},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			original := messageEnvelopeFixture()
			reply := messageEnvelopeFixture()
			reply.MessageRef, reply.ReplyTo = "message-reply", original.MessageRef
			reply.Source, reply.Target = original.Target, original.Source
			test.mutate(&reply)
			for name, err := range map[string]error{"ReplyRoutes": ReplyRoutes(original, reply), "ValidateReply": ValidateReply(original, reply)} {
				switch {
				case test.want == nil && err != nil:
					t.Fatalf("%s = %v, want correlation", name, err)
				case test.want == nil:
				case !errors.Is(err, test.want):
					t.Fatalf("%s = %v, want %v", name, err, test.want)
				case test.want == ErrInvalidEnvelope && (errors.Is(err, ErrReplyConversationChanged) ||
					!strings.Contains(err.Error(), ReasonCorrelationInvalid)):
					t.Fatalf("%s = %v, want a correlation mismatch, not a conversation change", name, err)
				}
			}
		})
	}
}
