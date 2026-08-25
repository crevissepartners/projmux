package app

import (
	"errors"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestProjectLifecycleAppClassifiesAllThreeDesiredStatesWithoutRuntimeEvidence(t *testing.T) {
	t.Parallel()
	store := freshStartFixtureStore(t)
	mutator := store.mutator()
	for _, window := range store.registry.WindowsOf("prj-gone") {
		if err := mutator.DeleteWindow(&store.registry, window.Metadata.UID); err != nil {
			t.Fatal(err)
		}
	}
	inputs := []struct {
		root  string
		state coremetadata.ProjectLifecycleState
		uid   string
	}{
		{root: "/srv/alpha", state: coremetadata.ProjectLifecycleRetainedWindows, uid: "prj-alpha"},
		{root: "/srv/gone", state: coremetadata.ProjectLifecycleZeroWindows, uid: "prj-gone"},
		{root: "/srv/deleted", state: coremetadata.ProjectLifecycleDeleted},
	}
	for _, input := range inputs {
		state, uid := projectLifecycleStateFor(store.registry, input.root)
		if state != input.state || uid != input.uid {
			t.Fatalf("root=%q app state=(%q,%q), want (%q,%q)", input.root, state, uid, input.state, input.uid)
		}
	}
}

func TestProjectLifecycleFailuresExposeActionStageAndOldNewUIDs(t *testing.T) {
	t.Parallel()
	for _, action := range []coremetadata.ProjectLifecycleAction{
		coremetadata.ProjectLifecycleStop,
		coremetadata.ProjectLifecycleContinue,
		coremetadata.ProjectLifecycleFresh,
		coremetadata.ProjectLifecycleDeleteProject,
	} {
		err := wrapProjectLifecycleError(action, "commit-stage", "proj-old", "proj-new", errors.New("injected failure"))
		message := err.Error()
		for _, want := range []string{"action=" + string(action), "stage=commit-stage", "old_uid=proj-old", "new_uid=proj-new", "injected failure"} {
			if !strings.Contains(message, want) {
				t.Fatalf("action=%q error=%q, want %q", action, message, want)
			}
		}
	}
}
