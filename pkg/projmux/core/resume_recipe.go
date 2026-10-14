package core

import (
	"fmt"
	"strings"
)

// ResumeRecipeChange describes one existing recorded-recipe guard difference.
// Recorded and Current are scalar singleton lists, or ordered workspace-root
// lists. Empty scalars are [""], empty root lists are [], and unavailable values
// are ["<unknown>"]. Content-bearing settings expose digests, never their bodies.
type ResumeRecipeChange struct {
	Field    string   `json:"field"`
	Reason   string   `json:"reason"`
	Recorded []string `json:"recorded"`
	Current  []string `json:"current"`
}

// ResumeRecipeChangedError is additive detail for a recorded process resume
// refusal. It does not authorize relaunch or enable SDK process resume support.
// Use errors.As through CLI/consumer wrappers; JSON is suitable for UI consumers.
type ResumeRecipeChangedError struct {
	Code            string               `json:"Code"`
	Reason          string               `json:"Reason"`
	AgentUID        string               `json:"agentUID"`
	Changes         []ResumeRecipeChange `json:"changes"`
	RelaunchCommand string               `json:"relaunchCommand"`
	Guidance        string               `json:"guidance"`
}

func (e *ResumeRecipeChangedError) Error() string {
	var details []string
	for _, change := range e.Changes {
		details = append(details, fmt.Sprintf("%s (%s): %q -> %q", change.Field, change.Reason, change.Recorded, change.Current))
	}
	return fmt.Sprintf("agent relaunch: uid:%s recorded resume recipe changed; %s; %s (%s); nothing was changed", e.AgentUID, strings.Join(details, "; "), e.Guidance, e.Reason)
}
