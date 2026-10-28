package app

import (
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// withCreateSettingSources adds to base, the annotations a create records,
// the source of every launch setting that create records a value for: the
// profile it applied (flag or role), and the instructions, model, and effort
// (profile when the profile filled them in, flag when a create flag gave
// them). It reads the same fields the value annotations are written from, so
// a value is recorded with its source or not at all.
//
// A create that records none of the four values returns base itself -- nil
// included -- so it stores exactly what it stored before sources were
// recorded. Otherwise it returns a new map and never writes into base.
func withCreateSettingSources(flags resourceCreateFlags, base map[string]string) map[string]string {
	return mutationruntime.WithCreateSettingSources(flags.sharedAgentSettingsFlags(), base)
}

func withInheritedSettingSources(inherited, base map[string]string) map[string]string {
	return mutationruntime.WithInheritedSettingSources(inherited, base)
}
