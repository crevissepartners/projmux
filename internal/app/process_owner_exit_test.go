package app

import (
	"errors"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestProcessOwnerEndNoticeNeverPointsAtAContinuedAgent(t *testing.T) {
	closed := errors.Join(processhost.ErrClosed, processhost.ErrStale)
	for name, tc := range map[string]struct {
		prepare      func(*testing.T, *coremetadata.Registry, string, string)
		waitRecorded bool
		cause        error
		want         string
	}{
		"deleted elsewhere": {prepare: func(t *testing.T, reg *coremetadata.Registry, agent, pane string) {
			if err := intmetadata.DefaultMutator().DeleteAgent(reg, agent); err != nil {
				t.Fatal(err)
			}
		}, cause: closed, want: "was deleted by another process; nothing to clean up"},
		"continued as another generation": {prepare: func(_ *testing.T, reg *coremetadata.Registry, _, pane string) {
			p, _ := reg.Pane(pane)
			p.Status.ProcessSession.Binding.Generation = "gen-two"
		}, cause: errors.New("unrelated failure"), want: "continued as generation gen-two in another process; this owner's generation gen-one ended"},
		"stopped elsewhere with recorded Wait": {prepare: func(t *testing.T, reg *coremetadata.Registry, agent, pane string) {
			recordFixtureWait(t, reg, pane)
		}, waitRecorded: true, cause: closed, want: "generation gen-one was stopped by another process; it is offline with its recorded Wait"},
		"recorded Wait with a real failure keeps guidance": {prepare: func(t *testing.T, reg *coremetadata.Registry, agent, pane string) {
			recordFixtureWait(t, reg, pane)
		}, waitRecorded: true, cause: errors.Join(processhost.ErrClosed, errors.New("attention store failed"))},
		"unrecorded Wait keeps guidance": {prepare: func(*testing.T, *coremetadata.Registry, string, string) {}, cause: closed},
	} {
		t.Run(name, func(t *testing.T) {
			reg, agent, pane := processDeleteFixture(t, aiModeClaude)
			p, _ := reg.Pane(pane)
			binding := processSchemaBinding(p.Status.ProcessSession.Binding)
			tc.prepare(t, reg, agent, pane)
			notice, ok := processOwnerEndNotice(*reg, binding, tc.waitRecorded, tc.cause)
			if tc.want == "" {
				if ok {
					t.Fatalf("guidance suppressed: %q", notice)
				}
				return
			}
			if !ok || !strings.Contains(notice, tc.want) || strings.Contains(notice, "delete agent") {
				t.Fatalf("notice = %q, %v", notice, ok)
			}
		})
	}
}
