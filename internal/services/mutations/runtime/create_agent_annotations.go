package runtime

import (
"bytes"
"encoding/json"
"errors"
"fmt"
"github.com/crevissepartners/projmux/internal/core/persona"
"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
"io"
"maps"
"os"
"path/filepath"
"regexp"
"runtime"
"slices"
"strings"
"syscall"
coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// withEffortAnnotation adds the effort a new Agent is created with to
// base, the Agent annotations the create already records. Without an effort
// it returns base itself, so a create without --effort stores exactly what it
// stored before the effort was recorded -- nil included. Otherwise it returns
// a new map and never writes into base. The create preflight has already
// refused unsupported providers and invalid values.
func withEffortAnnotation(effort string, base map[string]string) map[string]string {
	if effort == "" {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, 1)
	}
	out[coremetadata.AnnotationAgentEffort] = effort
	return out
}

// withModelAnnotation adds the model a new Agent is created with to base, the
// Agent annotations the create already records, with the same contract as
// withEffortAnnotation: without a model it returns base itself (nil included),
// otherwise a new map, and it never writes into base.
func withModelAnnotation(model string, base map[string]string) map[string]string {
	if model == "" {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, 1)
	}
	out[coremetadata.AnnotationAgentModel] = model
	return out
}

// withDialogueReplyOnlyAnnotation adds the reply-only record to base, the
// Agent annotations a create already records. Without the mode it returns
// base itself (nil included), so an ordinary create records nothing new.
func withDialogueReplyOnlyAnnotation(enabled bool, base map[string]string) map[string]string {
	if !enabled {
		return base
	}
	out := maps.Clone(base)
	if out == nil {
		out = make(map[string]string, 1)
	}
	out[coremetadata.AnnotationAgentDialogueReplyOnly] = coremetadata.DialogueReplyOnlyOn
	return out
}

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
