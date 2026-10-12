package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
)

// withEffortAnnotation adds the effort a new Agent is created with to
// base, the Agent annotations the create already records. Without an effort
// it returns base itself, so a create without --effort stores exactly what it
// stored before the effort was recorded -- nil included. Otherwise it returns
// a new map and never writes into base. The create preflight has already
// refused unsupported providers and invalid values.
func WithEffortAnnotation(effort string, base map[string]string) map[string]string {
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
func WithModelAnnotation(model string, base map[string]string) map[string]string {
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
func WithDialogueReplyOnlyAnnotation(enabled bool, base map[string]string) map[string]string {
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
func WithCreateSettingSources(flags AgentSettingsFlags, base map[string]string) map[string]string {
	fromProfile := func(filled bool) string {
		if filled {
			return coremetadata.SettingSourceProfile
		}
		return coremetadata.SettingSourceFlag
	}
	sources := map[string]string{}
	if flags.ProfileLaunch.active() {
		sources[coremetadata.AnnotationAgentProfileSource] = flags.ProfileLaunch.Source
	}
	if flags.personaLaunch.name != "" {
		sources[coremetadata.AnnotationAgentInstructionsSource] = fromProfile(flags.InstructionsOption == "profile")
	}
	if flags.Model != "" {
		sources[coremetadata.AnnotationAgentModelSource] = fromProfile(flags.ProfileLaunch.FilledModel)
	}
	if flags.Effort != "" {
		sources[coremetadata.AnnotationAgentEffortSource] = fromProfile(flags.ProfileLaunch.FilledEffort)
	}
	return WithSettingSources(base, sources)
}

func WithSettingSources(base, sources map[string]string) map[string]string {
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
