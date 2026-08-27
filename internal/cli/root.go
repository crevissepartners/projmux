package cli

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"

	"github.com/spf13/cobra"
)

// Handler runs one existing command with raw argv. Phase 0 forwards argv
// untouched so `--`, positional, and unknown-flag semantics stay byte-identical
// to the pre-Cobra dispatch. Leaf flag parsing stays inside the handler.
type Handler func(args []string, stdout, stderr io.Writer) error

// reservedCobraTokens are argv tokens Cobra injects for its own machinery. They
// are not part of the projmux public surface, so the root policy rejects them
// with the historical unknown-command error instead of letting Cobra answer.
var reservedCobraTokens = []string{"__complete", "__completeNoDesc", "completion"}

// rootVersionFlags is both parser input and census input for the public version
// spellings that do not have their own graph node.
var rootVersionFlags = []string{"--version", "-version"}

func rootInvocationBridgeRows() []InvocationCensusRow {
	rows := []InvocationCensusRow{{
		Spelling: "<bare invocation>", Authority: InvocationNatural,
	}}
	for _, name := range helpFlagNames {
		rows = append(rows, InvocationCensusRow{
			Spelling: "<root help flag:" + name + ">", Authority: InvocationFanOut,
		})
	}
	for _, spelling := range rootVersionFlags {
		rows = append(rows, InvocationCensusRow{
			Spelling: spelling, Authority: InvocationFanOut,
		})
	}
	return rows
}

// RootOptions configures the Cobra root factory. Writers are injected, so the
// factory never reaches for os.Stdout/os.Stderr and never calls os.Exit.
type RootOptions struct {
	// Stdout receives command output and every help rendering.
	Stdout io.Writer
	// Stderr receives command diagnostics and the unknown-command listing.
	Stderr io.Writer
	// Version is the string rendered by `version`, `--version`, `-version`.
	Version string
	// Handlers maps a manifest route token to its raw-argv handler. Every
	// manifest route except `help` and `version` must be present; those two
	// are answered by the shared root policy.
	Handlers map[string]Handler
}

// policyOwnedRoutes are manifest routes answered by the root policy itself
// rather than by an injected handler.
var policyOwnedRoutes = map[string]bool{"help": true, "version": true}

// Root is a built Cobra command tree plus the shared root policy: injected
// writers, silenced Cobra output, no suggestions, no injected commands, and one
// common help boundary.
type Root struct {
	cmd     *cobra.Command
	stdout  io.Writer
	stderr  io.Writer
	version string
}

// helpShieldedError hides a handler error from Cobra's own help detection.
//
// Several existing leaf parsers return flag.ErrHelp. Cobra treats a returned
// flag.ErrHelp as "print help and succeed", which would silently rewrite an
// existing non-help exit code. The shield deliberately omits Unwrap so
// errors.Is(err, flag.ErrHelp) is false while the error crosses Cobra, and
// Root.Execute restores the original error for the caller.
type helpShieldedError struct{ err error }

func (e helpShieldedError) Error() string { return e.err.Error() }

// NewRoot builds the Cobra root for one invocation. It is a factory, not a
// global singleton: every call returns an independent tree bound to the
// supplied writers and handlers.
func NewRoot(opts RootOptions) (*Root, error) {
	if opts.Stdout == nil || opts.Stderr == nil {
		return nil, fmt.Errorf("cli: root requires both stdout and stderr writers")
	}
	var missing []string
	for _, route := range routes {
		if policyOwnedRoutes[route.Name] {
			continue
		}
		if _, ok := opts.Handlers[route.Name]; !ok {
			missing = append(missing, route.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("cli: missing handlers for routes %v", missing)
	}

	root := &Root{stdout: opts.Stdout, stderr: opts.Stderr, version: opts.Version}
	cmd := &cobra.Command{
		Use:                "projmux",
		Short:              "projmux",
		SilenceUsage:       true,
		SilenceErrors:      true,
		DisableSuggestions: true,
		// The root forwards raw argv to the historical dispatch, including
		// bare flags such as `--version`, so Cobra must not consume them.
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			return root.runRoot(args)
		},
	}
	cmd.SetOut(opts.Stdout)
	cmd.SetErr(opts.Stderr)
	cmd.CompletionOptions.DisableDefaultCmd = true
	cmd.CompletionOptions.DisableDescriptions = true
	cmd.SetHelpFunc(func(_ *cobra.Command, _ []string) {
		_ = RenderRootHelp(opts.Stdout)
	})
	cmd.SetUsageFunc(func(_ *cobra.Command) error {
		return RenderRootHelp(opts.Stdout)
	})
	// Own the `help` route so Cobra does not inject its own help command with
	// different output. `projmux help [anything]` prints the primary listing.
	cmd.SetHelpCommand(&cobra.Command{
		Use:                "help",
		Short:              helpRouteSummary(),
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return RenderRootHelp(opts.Stdout)
		},
	})

	for _, route := range routes {
		if route.Name == "help" {
			continue
		}
		cmd.AddCommand(newBridgeCommand(root, route, opts))
	}
	root.cmd = cmd
	return root, nil
}

// newBridgeCommand builds one Phase 0 compatibility bridge node. Flag parsing
// is off and argv is forwarded verbatim to the existing handler.
func newBridgeCommand(root *Root, route Route, opts RootOptions) *cobra.Command {
	bridge := &cobra.Command{
		Use:                route.Name,
		Short:              route.Summary,
		Hidden:             route.Hidden,
		SilenceUsage:       true,
		SilenceErrors:      true,
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
	}
	if route.Name == "version" {
		bridge.RunE = func(_ *cobra.Command, _ []string) error {
			return root.printVersion()
		}
		return bridge
	}
	handler := opts.Handlers[route.Name]
	bridge.RunE = func(_ *cobra.Command, args []string) error {
		if err := handler(args, opts.Stdout, opts.Stderr); err != nil {
			return helpShieldedError{err: err}
		}
		return nil
	}
	return bridge
}

func helpRouteSummary() string {
	if route, ok := LookupRoute("help"); ok {
		return route.Summary
	}
	return ""
}

// Execute runs one invocation. Help is answered by the shared boundary before
// Cobra sees argv, so no handler, tmux access, or lifecycle migration runs for
// a help request and every help invocation exits 0.
func (r *Root) Execute(args []string) error {
	if target, ok := RequestedHelp(args); ok {
		return RenderHelp(r.stdout, target)
	}
	if len(args) == 0 {
		return RenderRootHelp(r.stdout)
	}
	if slices.Contains(reservedCobraTokens, args[0]) {
		return r.unknownCommand(args[0])
	}
	r.cmd.SetArgs(args)
	err := r.cmd.Execute()
	var shielded helpShieldedError
	if errors.As(err, &shielded) {
		return shielded.err
	}
	return err
}

// runRoot reproduces the historical top-level default branches for argv that
// does not resolve to a route command.
func (r *Root) runRoot(args []string) error {
	if len(args) == 0 {
		return RenderRootHelp(r.stdout)
	}
	if slices.Contains(rootVersionFlags, args[0]) {
		return r.printVersion()
	}
	switch args[0] {
	case "help", "--help", "-h":
		return RenderRootHelp(r.stdout)
	default:
		return r.unknownCommand(args[0])
	}
}

// unknownCommand keeps the historical contract: the primary listing goes to
// stderr and the returned error is a plain runtime error (exit 1), not a usage
// error (exit 2).
func (r *Root) unknownCommand(token string) error {
	if err := RenderRootHelp(r.stderr); err != nil {
		return err
	}
	return fmt.Errorf("unknown command: %s", token)
}

func (r *Root) printVersion() error {
	_, err := fmt.Fprintf(r.stdout, "projmux %s\n", r.version)
	return err
}
