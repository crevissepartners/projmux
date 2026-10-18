package projmux

import (
	"context"

	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	store "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ReadMessageArchive reads retained live messages (including their full Payload)
// followed by both history generations from the Client's captured StateDir.
//
// Payload access: 권한: 같은 사용자의 StateDir 읽기, 비밀 처리 없음.
// Bodies are exposed without redaction or secret filtering; callers must decide
// where they may display or forward them.
//
// This shares the store reader and validation with CORE. It creates nothing,
// takes no lock, and observes no Registry, provider or tmux state. Missing files
// are empty. Store read/validation and history read errors retain their original
// text and wrapped causes and return no partial archive. Invalid history lines
// count toward Skipped. Order, duplicates and nil/empty slices are preserved;
// callers own the returned slices. A store read sees one complete renamed store,
// but the store and sequential history reads are not an atomic snapshot.
//
// Cancellation is checked before reading and after a successful synchronous
// read. It cannot interrupt an in-progress file read; a reader error takes
// precedence over cancellation during that read.
func (c *Client) ReadMessageArchive(ctx context.Context) (core.MessageArchive, error) {
	if err := ctx.Err(); err != nil {
		return core.MessageArchive{}, err
	}
	if c.pathsErr != nil {
		return core.MessageArchive{}, c.pathsErr
	}
	archive, err := store.ReadArchive(c.paths.StateDir)
	if err != nil {
		return core.MessageArchive{}, err
	}
	if err := ctx.Err(); err != nil {
		return core.MessageArchive{}, err
	}
	out := core.MessageArchive{Skipped: archive.Skipped}
	if archive.Records != nil {
		out.Records = make([]core.MessageArchiveRecord, len(archive.Records))
	}
	for i, record := range archive.Records {
		e, d := record.Envelope, record.Delivery
		out.Records[i] = core.MessageArchiveRecord{
			MessageRef: e.MessageRef, ConversationRef: e.ConversationRef, ReplyTo: e.ReplyTo,
			Origin: core.MessageOrigin{Kind: e.Origin.Kind, Client: e.Origin.Client},
			Source: archiveRoute(e.Source), Target: archiveRoute(e.Target),
			Delivery: core.MessageDelivery{MessageRef: d.MessageRef, ConversationRef: d.ConversationRef,
				State: core.MessageState(d.State), Reason: d.Reason, OutcomeUnknown: d.OutcomeUnknown, AcceptedAt: d.AcceptedAt, TerminalAt: d.TerminalAt},
			Payload: e.Payload, PayloadBytes: len(e.Payload), Operator: e.Operator(),
		}
	}
	if archive.History != nil {
		out.History = make([]core.MessageHistoryEntry, len(archive.History))
	}
	for i, h := range archive.History {
		out.History[i] = core.MessageHistoryEntry{SchemaVersion: h.SchemaVersion,
			MessageRef: h.MessageRef, ConversationRef: h.ConversationRef, ReplyTo: h.ReplyTo,
			State: core.MessageState(h.State), AcceptedAt: h.AcceptedAt, PayloadBytes: h.PayloadBytes, PayloadSHA256: h.PayloadSHA256,
			Origin: core.MessageOrigin{Kind: h.Origin.Kind, Client: h.Origin.Client}, Source: archiveRoute(h.Source), Target: archiveRoute(h.Target), Operator: h.Origin.Operator(),
		}
	}
	return out, nil
}

// MessageArchiveFiles returns the three paths ReadMessageArchive reads, in
// order: StateDir/agent-messages/messages.json, history.jsonl, history.jsonl.1.
// StateDir is captured at Client construction, including any explicit override.
// It opens and stats nothing, even if archive contents are corrupt or missing,
// and returns a caller-owned slice. Cancellation is checked before and after
// projecting the paths. Callers may stat them for size/mtime change detection;
// no cross-file atomic snapshot or replacement for file timestamps is supplied.
func (c *Client) MessageArchiveFiles(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.pathsErr != nil {
		return nil, c.pathsErr
	}
	files := store.ArchiveFiles(c.paths.StateDir)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return files, nil
}

func archiveRoute(r message.Route) core.MessageRoute {
	return core.MessageRoute{AgentUID: r.AgentUID, PaneUID: r.PaneUID, ActivationGeneration: r.ActivationGeneration, Provider: r.Provider, Incarnation: r.Incarnation}
}
