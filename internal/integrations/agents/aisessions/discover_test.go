package aisessions

import (
	"context"
	"sort"
)

// Discover preserves the historical all-provider convenience for discovery
// fixtures. Production uses provider-isolated discovery so one slow or failed
// provider cannot block the picker or its peers.
func Discover(cwd string, opts DiscoverOptions) ([]SessionMeta, error) {
	var sessions []SessionMeta
	for _, provider := range []string{AgentClaude, AgentCodex, AgentAntigravity} {
		discovery, err := DiscoverProviderContext(context.Background(), provider, cwd, opts, 0)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, discovery.Sessions...)
	}
	sessions = dedupeByResumeID(sessions)
	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].LastModified.Equal(sessions[j].LastModified) {
			if sessions[i].Agent == sessions[j].Agent {
				return sessions[i].ResumeID < sessions[j].ResumeID
			}
			return sessions[i].Agent < sessions[j].Agent
		}
		return sessions[i].LastModified.After(sessions[j].LastModified)
	})
	return sessions, nil
}
