package claude

import (
	"errors"
	"reflect"
	"testing"
)

func TestResumeArgs(t *testing.T) {
	t.Parallel()

	got, err := ResumeArgs("  018f4c2d-abc_DEF.123  ")
	if err != nil {
		t.Fatalf("ResumeArgs() error = %v", err)
	}
	want := []string{"claude", "--resume", "018f4c2d-abc_DEF.123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResumeArgs() = %#v, want %#v", got, want)
	}
}

func TestResumeCommand(t *testing.T) {
	t.Parallel()

	got, err := ResumeCommand("abc'def-1234")
	if err != nil {
		t.Fatalf("ResumeCommand() error = %v", err)
	}
	if got != "claude --resume 'abc'\\''def-1234'" {
		t.Fatalf("ResumeCommand() = %q, want claude resume command", got)
	}
}

func TestResumeArgsRejectsEmptyOrControlResumeIDs(t *testing.T) {
	t.Parallel()

	tests := []string{
		"",
		"abc\ndef",
		"abc\rdef",
	}
	for _, in := range tests {
		t.Run(in, func(t *testing.T) {
			t.Parallel()

			_, err := ResumeArgs(in)
			if !errors.Is(err, ErrInvalidResumeID) {
				t.Fatalf("ResumeArgs(%q) error = %v, want %v", in, err, ErrInvalidResumeID)
			}
		})
	}
}
