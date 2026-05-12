package codex

import (
	"errors"
	"reflect"
	"testing"
)

func TestResumeArgs(t *testing.T) {
	t.Parallel()

	got, err := ResumeArgs("  01973f21-abc_DEF.123  ")
	if err != nil {
		t.Fatalf("ResumeArgs() error = %v", err)
	}
	want := []string{"codex", "resume", "01973f21-abc_DEF.123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResumeArgs() = %#v, want %#v", got, want)
	}
}

func TestResumeCommand(t *testing.T) {
	t.Parallel()

	got, err := ResumeCommand("abc'def; rm -rf /")
	if err != nil {
		t.Fatalf("ResumeCommand() error = %v", err)
	}
	if got != "codex resume 'abc'\\''def; rm -rf /'" {
		t.Fatalf("ResumeCommand() = %q, want quoted codex resume command", got)
	}
}

func TestResumeArgsRejectsEmptyOrControlResumeIDs(t *testing.T) {
	t.Parallel()

	tests := []string{
		"",
		"abc\ndef",
		"abc\rdef",
		"abc\tdef",
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
