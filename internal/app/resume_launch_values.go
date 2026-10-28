package app

import (
	"fmt"
	"io"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// launchValuesReasonAmbiguous is the reason token of a resume-picker create
// that inherited nothing because the Agents recording its conversation
// disagree about their launch values.
const launchValuesReasonAmbiguous = mutationruntime.ResumeLaunchValuesReasonAmbiguous

func resumeLaunchValueKeysFor(provider string) []string {
	return mutationruntime.ResumeLaunchValueKeysFor(provider)
}

func inheritedResumeLaunchValues(registry *coremetadata.Registry, provider, conversation string) (map[string]string, string) {
	return mutationruntime.InheritedResumeLaunchValues(registry, provider, conversation)
}

// writeIntentAgentNotices discloses what a committed UI Agent create could not
// carry over. Like agent resume's notices, a lost disclosure must not turn a
// committed create into a failure.
//
// Each notice is written without a leading `projmux: `. The intent path's
// consumers add that prefix themselves -- the split funnel shows create's
// stderr on the pressing client as one `projmux: ` line, exactly as it shows
// the split start notice -- so the seam's persona and effort notices, which
// `agent resume` prints with the prefix, would otherwise say it twice.
func writeIntentAgentNotices(stderr io.Writer, notices []string) {
	if stderr == nil {
		return
	}
	for _, notice := range notices {
		_, _ = fmt.Fprintln(stderr, strings.TrimPrefix(notice, "projmux: "))
	}
}
