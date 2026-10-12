package app

import (
	"maps"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)



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


