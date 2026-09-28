package app

import (
	"maps"

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
	fromProfile := func(filled bool) string {
		if filled {
			return coremetadata.SettingSourceProfile
		}
		return coremetadata.SettingSourceFlag
	}
	sources := map[string]string{}
	if flags.profileLaunch.active() {
		sources[coremetadata.AnnotationAgentProfileSource] = flags.profileLaunch.source
	}
	if flags.personaLaunch.name != "" {
		sources[coremetadata.AnnotationAgentInstructionsSource] = fromProfile(flags.personaOption == "profile")
	}
	if flags.model != "" {
		sources[coremetadata.AnnotationAgentModelSource] = fromProfile(flags.profileLaunch.filledModel)
	}
	if flags.effort != "" {
		sources[coremetadata.AnnotationAgentEffortSource] = fromProfile(flags.profileLaunch.filledEffort)
	}
	return withSettingSources(base, sources)
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
	if len(sources) == 0 {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, len(sources))
	}
	maps.Copy(out, sources)
	return out
}
