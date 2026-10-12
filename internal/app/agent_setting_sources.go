package app

import (
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
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

// withInheritedSettingSources adds to base the source of every launch setting
// a resume-picker create inherited from the Agents that already recorded its
// conversation: inherited is the launch-value bundle and profile pair it
// inherited, and each of the profile, the instructions, and the effort found
// there is recorded as coremetadata.SettingSourceInherited. The source keys
// are never part of the bundle itself, so holders that differ only in where
// their values came from still agree.
//
// Nothing inherited returns base itself; otherwise it returns a new map and
// never writes into base.
func withInheritedSettingSources(inherited, base map[string]string) map[string]string {
	sources := map[string]string{}
	for valueKey, sourceKey := range map[string]string{
		coremetadata.AnnotationAgentProfile: coremetadata.AnnotationAgentProfileSource,
		coremetadata.AnnotationAgentPersona: coremetadata.AnnotationAgentInstructionsSource,
		coremetadata.AnnotationAgentEffort:  coremetadata.AnnotationAgentEffortSource,
	} {
		if inherited[valueKey] != "" {
			sources[sourceKey] = coremetadata.SettingSourceInherited
		}
	}
	return withSettingSources(base, sources)
}

func withSettingSources(base, sources map[string]string) map[string]string {
	return mutationruntime.WithSettingSources(base, sources)
}
