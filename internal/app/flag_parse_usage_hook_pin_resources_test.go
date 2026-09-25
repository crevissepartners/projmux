package app

import (
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
)

// flagParseUsageRoute drives one public route whose flag parsing fails before
// the handler touches any dependency, so zero-value commands are enough and no
// HOME, tmux server, or network is reached.
type flagParseUsageRoute struct {
	name string
	run  func(args []string) error
}

func hookPinResourceFlagParseRoutes() map[string]flagParseUsageRoute {
	return map[string]flagParseUsageRoute{
		"hook": {name: "hook", run: func(args []string) error {
			return (&hookCommand{}).Run(args, io.Discard, io.Discard)
		}},
		"pin": {name: "pin", run: func(args []string) error {
			return (&pinCommand{}).Run(args, io.Discard, io.Discard)
		}},
		"attention": {name: "attention", run: func(args []string) error {
			return (&attentionCommand{}).Run(args, io.Discard, io.Discard)
		}},
		"create": {name: "create", run: func(args []string) error {
			return (&createCommand{}).Run(args, io.Discard, io.Discard)
		}},
		"resources": {name: "resources", run: func(args []string) error {
			return (&resourceCommand{}).Run(args, io.Discard, io.Discard)
		}},
		"reconcile": {name: "reconcile", run: func(args []string) error {
			cmd := &resourceReconcileCommand{registry: &registryRecoveryCommand{}}
			return cmd.Run(args, io.Discard, io.Discard)
		}},
	}
}

func isolateFlagParseUsageEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("XDG_STATE_HOME", home+"/.local/state")
	t.Setenv("XDG_DATA_HOME", home+"/.local/share")
	t.Setenv("TMUX", "")
}

func TestHookPinResourceRoutesClassifyFlagParseErrorsAsUsage(t *testing.T) {
	isolateFlagParseUsageEnv(t)
	routes := hookPinResourceFlagParseRoutes()

	const unknown = "flag provided but not defined: -zz-bogus-flag"
	const attentionPrefix = "parse attention list flags: "

	tests := []struct {
		route string
		args  []string
		want  string
	}{
		// Unknown flag on every route.
		{"hook", []string{"--zz-bogus-flag"}, unknown},
		{"hook", []string{"list", "--zz-bogus-flag"}, unknown},
		{"hook", []string{"edit", "--zz-bogus-flag"}, unknown},
		{"hook", []string{"validate", "--zz-bogus-flag"}, unknown},
		{"pin", []string{"--zz-bogus-flag"}, unknown},
		{"pin", []string{"project", "--zz-bogus-flag"}, unknown},
		{"pin", []string{"list", "--zz-bogus-flag"}, unknown},
		{"pin", []string{"project", "list", "--zz-bogus-flag"}, unknown},
		{"pin", []string{"migrate", "--zz-bogus-flag"}, unknown},
		{"pin", []string{"project", "migrate", "--zz-bogus-flag"}, unknown},
		{"attention", []string{"list", "--zz-bogus-flag"}, attentionPrefix + unknown},
		{"create", []string{"project", "--zz-bogus-flag"}, unknown},
		{"resources", []string{"--zz-bogus-flag"}, unknown},
		{"reconcile", []string{"resources", "--zz-bogus-flag"}, unknown},
		{"reconcile", []string{"registry", "--zz-bogus-flag"}, unknown},

		// A value flag with its value missing.
		{"pin", []string{"list", "--kind"}, "flag needs an argument: -kind"},
		{"create", []string{"project", "--root"}, "flag needs an argument: -root"},
		{"reconcile", []string{"resources", "--output"}, "flag needs an argument: -output"},
		{"reconcile", []string{"registry", "--source"}, "flag needs an argument: -source"},

		// A malformed bool value.
		{"hook", []string{"list", "--global=maybe"}, `invalid boolean value "maybe" for -global: parse error`},
		{"hook", []string{"edit", "--editor=maybe"}, `invalid boolean value "maybe" for -editor: parse error`},
		{"attention", []string{"list", "--json=maybe"}, attentionPrefix + `invalid boolean value "maybe" for -json: parse error`},
		{"pin", []string{"migrate", "--dry-run=maybe"}, `invalid boolean value "maybe" for -dry-run: parse error`},
		{"reconcile", []string{"resources", "--dry-run=maybe"}, `invalid boolean value "maybe" for -dry-run: parse error`},
		{"reconcile", []string{"registry", "--dry-run=maybe"}, `invalid boolean value "maybe" for -dry-run: parse error`},
	}

	for _, tt := range tests {
		route := routes[tt.route]
		var name strings.Builder
		name.WriteString(route.name)
		for _, arg := range tt.args {
			name.WriteString(" " + arg)
		}
		t.Run(name.String(), func(t *testing.T) {
			err := route.run(tt.args)
			if err == nil {
				t.Fatalf("expected an error")
			}
			if !IsUsageError(err) {
				t.Fatalf("IsUsageError(%q) = false, want true", err)
			}
			if errors.Is(err, flag.ErrHelp) {
				t.Fatalf("parse failure must not read as flag.ErrHelp: %v", err)
			}
			if got := err.Error(); got != tt.want {
				t.Fatalf("error text = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHookPinResourceRoutesKeepHelpOutOfUsageErrors(t *testing.T) {
	isolateFlagParseUsageEnv(t)
	routes := hookPinResourceFlagParseRoutes()

	tests := []struct {
		route   string
		args    []string
		wantNil bool // the route answers --help itself and returns nil
	}{
		{"hook", []string{"--help"}, false},
		{"hook", []string{"list", "--help"}, false},
		{"hook", []string{"edit", "--help"}, false},
		{"hook", []string{"validate", "--help"}, false},
		{"pin", []string{"--help"}, false},
		{"pin", []string{"project", "--help"}, false},
		{"pin", []string{"list", "--help"}, false},
		{"pin", []string{"migrate", "--help"}, false},
		{"attention", []string{"list", "--help"}, true},
		{"create", []string{"project", "--help"}, false},
		{"resources", []string{"--help"}, true},
		{"reconcile", []string{"resources", "--help"}, false},
		{"reconcile", []string{"registry", "--help"}, false},
	}

	for _, tt := range tests {
		route := routes[tt.route]
		var name strings.Builder
		name.WriteString(route.name)
		for _, arg := range tt.args {
			name.WriteString(" " + arg)
		}
		t.Run(name.String(), func(t *testing.T) {
			err := route.run(tt.args)
			if tt.wantNil {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
				return
			}
			if !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("expected flag.ErrHelp, got %v", err)
			}
			if IsUsageError(err) {
				t.Fatalf("--help must not be classified as a usage error: %v", err)
			}
		})
	}
}
