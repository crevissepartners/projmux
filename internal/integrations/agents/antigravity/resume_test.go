package antigravity

import (
	"errors"
	"reflect"
	"testing"
)

func TestResumeArgsUsesConversationFlag(t *testing.T) {
	t.Parallel()

	got, err := ResumeArgs("  123e4567-e89b-12d3-a456-426614174000  ")
	if err != nil {
		t.Fatalf("ResumeArgs() error = %v", err)
	}
	want := []string{"agy", "--conversation", "123e4567-e89b-12d3-a456-426614174000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResumeArgs() = %#v, want %#v", got, want)
	}
}

func TestResumeCommandQuotesConversationID(t *testing.T) {
	t.Parallel()

	got, err := ResumeCommand("123e4567-e89b-12d3-a456-426614174000")
	if err != nil {
		t.Fatalf("ResumeCommand() error = %v", err)
	}
	if got != "agy --conversation 123e4567-e89b-12d3-a456-426614174000" {
		t.Fatalf("ResumeCommand() = %q", got)
	}
}

func TestNormalizeResumeIDRejectsInvalidIDs(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"", "ag-conv-123", "123e4567-e89b-12d3-a456-426614174\n000"} {
		if _, err := NormalizeResumeID(id); !errors.Is(err, ErrInvalidResumeID) {
			t.Fatalf("NormalizeResumeID(%q) error = %v, want ErrInvalidResumeID", id, err)
		}
	}
}
