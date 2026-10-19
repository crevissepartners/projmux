package projmux

import (
	"context"

	"github.com/crevissepartners/projmux/internal/services/agentsessions"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ListRecordedSessions reads the complete recorded session history from the
// Client's frozen StateDir, including deleted Agents and missing Projects.
// Agents appear in file first-appearance order; each Agent's rows use CORE
// time ordering, stable ties and duplicate merging. CorruptLines counts skipped
// nonempty lines across the whole file. Missing history returns an empty array.
// It never reads the Registry, transcripts, tmux or the network, writes state,
// or adds current sessions. Cancellation before admission performs no reads;
// path resolution errors are returned and all results are caller-owned.
func (c *Client) ListRecordedSessions(ctx context.Context) (core.RecordedSessionsList, error) {
	if err := ctx.Err(); err != nil {
		return core.RecordedSessionsList{}, err
	}
	service := agentsessions.Service{StateDir: func() (string, error) { return c.paths.StateDir, c.pathsErr }}
	listed, err := service.ListRecorded(ctx)
	if err != nil {
		return core.RecordedSessionsList{}, sessionReadRefusal(err)
	}
	result := core.RecordedSessionsList{Sessions: make([]core.AgentSession, len(listed.Sessions)), CorruptLines: listed.CorruptLines}
	for i, row := range listed.Sessions {
		result.Sessions[i] = core.AgentSession{AgentUID: row.AgentUID, Provider: row.Provider, SessionID: row.SessionID, TranscriptPath: row.TranscriptPath, ObservedAt: row.ObservedAt, Source: core.SessionSource(row.Source), ProjectUID: row.ProjectUID, WindowUID: row.WindowUID, AgentName: row.AgentName, AffiliationBasis: core.SessionAffiliationBasis(row.AffiliationBasis)}
		if row.LastRecordAt != nil {
			last := *row.LastRecordAt
			result.Sessions[i].LastRecordAt = &last
		}
	}
	return result, nil
}
