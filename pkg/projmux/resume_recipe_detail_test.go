package projmux

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// The recorded-recipe detail in a failed owner reply must reach the caller of
// every resume entry that reads that reply. A refusal that drops it fails here.
func TestRecipeDetailSurvivesPreparedResumeRefusal(t *testing.T) {
	recipe := core.ResumeRecipeChangedError{
		Code: "process-resume-refused", Reason: "relaunch-no-conversation", AgentUID: "agent-01",
		Changes:         []core.ResumeRecipeChange{{Field: "model", Reason: "changed", Recorded: []string{"a"}, Current: []string{"b"}}},
		RelaunchCommand: "projmux agent relaunch uid:agent-01", Guidance: "relaunch",
	}
	good, err := json.Marshal(recipe)
	if err != nil {
		t.Fatal(err)
	}
	other := recipe
	other.Code = "something-else"
	otherJSON, _ := json.Marshal(other)

	failure := func() error {
		return &mutationruntime.ProcessResumeFailure{Code: "process-resume-refused", Cause: errors.New("refused")}
	}
	for name, tc := range map[string]struct {
		detail json.RawMessage
		want   *core.ResumeRecipeChangedError
	}{
		"recipe changed": {good, &recipe},
		"other code":     {otherJSON, nil},
		"empty":          {nil, nil},
		"damaged":        {json.RawMessage(`{"Code":`), nil},
	} {
		t.Run(name, func(t *testing.T) {
			for entry, got := range map[string]error{
				"prepare/validate": preparationRefusal(failure(), "agent-01", tc.detail),
				"ResumeAgent":      restoreRecipeDetail(failure(), tc.detail),
			} {
				var found *core.ResumeRecipeChangedError
				if ok := errors.As(got, &found); ok != (tc.want != nil) {
					t.Fatalf("%s: errors.As=%v, want found=%v (%v)", entry, ok, tc.want != nil, got)
				}
				if tc.want != nil && !reflect.DeepEqual(found, tc.want) {
					t.Fatalf("%s: detail %+v, want %+v", entry, found, tc.want)
				}
				var refusal *Refusal
				if !errors.As(got, &refusal) && entry == "prepare/validate" {
					t.Fatalf("%s: not a Refusal: %v", entry, got)
				}
				if tc.want == nil && entry == "prepare/validate" && refusal.Code != "process-resume-refused" {
					t.Fatalf("code changed: %v", refusal.Code)
				}
			}
		})
	}
}
