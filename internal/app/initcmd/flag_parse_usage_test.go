package initcmd

import (
	"bytes"
	"flag"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// TestInitCommandFlagParseErrorsAreUsageErrors pins that a rejected flag on
// `setup terminal` carries the usage marker (exit 2) with the flag package's
// message unchanged, while --help stays flag.ErrHelp.
func TestInitCommandFlagParseErrorsAreUsageErrors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown flag", args: []string{"--zz-bogus-flag"}, want: "flag provided but not defined: -zz-bogus-flag"},
		{name: "config without value", args: []string{"ghostty", "--config"}, want: "flag needs an argument: -config"},
		{name: "apply bad value", args: []string{"--apply=maybe"}, want: `invalid boolean value "maybe" for -apply`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := New().Run(test.args, &bytes.Buffer{}, &bytes.Buffer{})
			if !coremetadata.IsUsageError(err) {
				t.Fatalf("Run(%q) error = %v, want a usage error", test.args, err)
			}
			if !strings.HasPrefix(err.Error(), test.want) {
				t.Fatalf("Run(%q) error = %q, want prefix %q", test.args, err.Error(), test.want)
			}
		})
	}

	t.Run("help", func(t *testing.T) {
		t.Parallel()
		err := New().Run([]string{"--help"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err != flag.ErrHelp { // identity: help must be returned unwrapped
			t.Fatalf("Run(--help) error = %v, want flag.ErrHelp unchanged", err)
		}
		if coremetadata.IsUsageError(err) {
			t.Fatal("Run(--help) error is a usage error")
		}
	})
}
