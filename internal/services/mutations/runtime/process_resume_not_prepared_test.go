package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// The marker is written only for the not-prepared sentinel, and read back only
// as that exact marker.
func TestResumeNotPreparedDetailMarksOnlyTheSentinel(t *testing.T) {
	if ErrResumeNotPrepared.Error() != "process-resume-not-prepared" || ResumeNotPrepared != "process-resume-not-prepared" {
		t.Fatalf("sentinel text %q const %q", ErrResumeNotPrepared.Error(), ResumeNotPrepared)
	}
	for name, err := range map[string]error{
		"bare":    ErrResumeNotPrepared,
		"wrapped": fmt.Errorf("outer: %w", ErrResumeNotPrepared),
	} {
		detail := ResumeNotPreparedDetail(err)
		if string(detail) != `{"Code":"process-resume-not-prepared"}` || !IsResumeNotPreparedDetail(detail) {
			t.Fatalf("%s: detail %s", name, detail)
		}
	}
	for name, err := range map[string]error{
		"nil":        nil,
		"other":      errors.New("resume-execution-failed"),
		"same text":  errors.New("process-resume-not-prepared"),
		"stale":      ErrResumePreparationStale,
		"stale text": ResumePreparationStaleError("process-resume-not-prepared"),
	} {
		if detail := ResumeNotPreparedDetail(err); detail != nil {
			t.Fatalf("%s: marked %s", name, detail)
		}
	}
	for name, detail := range map[string]json.RawMessage{
		"nil":            nil,
		"empty":          json.RawMessage(``),
		"damaged":        json.RawMessage(`{"Code":`),
		"other code":     json.RawMessage(`{"Code":"process-resume-refused"}`),
		"no code":        json.RawMessage(`{}`),
		"recipe changed": json.RawMessage(`{"Code":"process-resume-refused","Reason":"relaunch-no-conversation"}`),
	} {
		if IsResumeNotPreparedDetail(detail) {
			t.Fatalf("%s: accepted", name)
		}
	}
}
