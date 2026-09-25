package usagecmd

import (
	"bytes"
	"io"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// flagParseTestCommand has no manager or adapter hooks: a parse failure must
// return before any of them is consulted.
func flagParseTestCommand() *Command {
	return New(func() time.Time { return time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC) })
}

func TestAgentUsageFlagParseErrorsAreUsageErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args     []string
		wantText string
	}{
		{[]string{"--zz-bogus-flag"}, "flag provided but not defined: -zz-bogus-flag"},
		{[]string{"--model"}, "flag needs an argument: -model"},
		{[]string{"--window"}, "flag needs an argument: -window"},
		{[]string{"--json=maybe"}, `invalid boolean value "maybe" for -json: parse error`},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			t.Parallel()
			err := flagParseTestCommand().Run(tc.args, io.Discard, io.Discard)
			if err == nil {
				t.Fatalf("Run(%q) error = nil, want a flag parse error", tc.args)
			}
			if !coremetadata.IsUsageError(err) {
				t.Fatalf("Run(%q) error = %v (%T), want a usage error", tc.args, err, err)
			}
			if got := err.Error(); got != tc.wantText {
				t.Fatalf("Run(%q) error text = %q, want %q", tc.args, got, tc.wantText)
			}
		})
	}
}

func TestAgentUsageHelpStillSucceeds(t *testing.T) {
	t.Parallel()
	for _, arg := range []string{"--help", "-h"} {
		if err := flagParseTestCommand().Run([]string{arg}, io.Discard, io.Discard); err != nil {
			t.Fatalf("Run(%q) error = %v, want nil", arg, err)
		}
	}
}

// The hidden `internal status usage` FlagSet is tmux plumbing and keeps
// returning the bare flag error.
func TestStatusUsageFlagParseErrorStaysBare(t *testing.T) {
	t.Parallel()
	err := flagParseTestCommand().RunStatus([]string{"--zz-bogus-flag"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("RunStatus error = nil, want a flag parse error")
	}
	if coremetadata.IsUsageError(err) {
		t.Fatalf("RunStatus error = %v, want a non-usage error", err)
	}
	if got, want := err.Error(), "flag provided but not defined: -zz-bogus-flag"; got != want {
		t.Fatalf("RunStatus error text = %q, want %q", got, want)
	}
}

// A leftover positional argument is a usage error (exit 2) with its message
// unchanged; the usage text still goes to stderr first.
func TestAgentUsagePositionalArgumentIsUsageError(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"x"}, {"x", "--zz"}, {"--json", "x"}} {
		var stderr bytes.Buffer
		err := flagParseTestCommand().Run(args, io.Discard, &stderr)
		if err == nil {
			t.Fatalf("Run(%q) error = nil, want a usage error", args)
		}
		if !coremetadata.IsUsageError(err) {
			t.Fatalf("Run(%q) error = %v (%T), want a usage error", args, err, err)
		}
		if got, want := err.Error(), "usage does not accept positional arguments"; got != want {
			t.Fatalf("Run(%q) error text = %q, want %q", args, got, want)
		}
		if stderr.Len() == 0 {
			t.Fatalf("Run(%q) printed no usage text on stderr", args)
		}
	}
}

// The hidden `internal status usage` positional refusal stays a plain error.
func TestStatusUsagePositionalArgumentStaysBare(t *testing.T) {
	t.Parallel()
	err := flagParseTestCommand().RunStatus([]string{"x"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("RunStatus error = nil, want a positional refusal")
	}
	if coremetadata.IsUsageError(err) {
		t.Fatalf("RunStatus error = %v, want a non-usage error", err)
	}
}
