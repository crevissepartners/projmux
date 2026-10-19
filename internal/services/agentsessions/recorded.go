package agentsessions

import (
	"context"

	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
)

// RecordedResult contains only persisted history, without Registry enrichment.
type RecordedResult struct {
	Sessions     []sessionhistory.Record `json:"sessions"`
	CorruptLines int                     `json:"corruptLines"`
}

// ListRecorded reads all history and merges each Agent in first-appearance order.
// It never calls LoadRegistry or supplies a current session to Merge.
func (s Service) ListRecorded(ctx context.Context) (RecordedResult, error) {
	if err := ctx.Err(); err != nil {
		return RecordedResult{}, err
	}
	stateDir, err := s.stateDir("recorded sessions")
	if err != nil {
		return RecordedResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return RecordedResult{}, err
	}
	read, err := sessionhistory.Read(stateDir, "")
	if err != nil {
		return RecordedResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return RecordedResult{}, err
	}
	groups := make(map[string][]sessionhistory.Record)
	order := []string{}
	for _, row := range read.Records {
		if _, exists := groups[row.AgentUID]; !exists {
			order = append(order, row.AgentUID)
		}
		groups[row.AgentUID] = append(groups[row.AgentUID], row)
	}
	result := RecordedResult{Sessions: []sessionhistory.Record{}, CorruptLines: read.Corrupt}
	for _, uid := range order {
		if err := ctx.Err(); err != nil {
			return RecordedResult{}, err
		}
		result.Sessions = append(result.Sessions, sessionhistory.Merge(groups[uid], nil)...)
	}
	return result, nil
}
