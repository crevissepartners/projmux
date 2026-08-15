package app

import (
	"io"
	"reflect"
	"strings"
	"testing"
)

// recordingArgv captures the argv a canonical route forwards to the handler
// that already owns the behavior.
type recordingArgv struct {
	calls [][]string
	out   string
	err   error
}

func (r *recordingArgv) Run(args []string, stdout, _ io.Writer) error {
	r.calls = append(r.calls, append([]string(nil), args...))
	if r.out != "" {
		if _, err := io.WriteString(stdout, r.out); err != nil {
			return err
		}
	}
	return r.err
}

func newTestCreateCommand() (*createCommand, *recordingArgv) {
	split := &recordingArgv{}
	return &createCommand{ai: split}, split
}

// TestCreateNormalizesOntoTheExistingSplitWithoutNewComposition is the create
// half of the Agent decomposition table.
//
// Each row proves the canonical spelling is a normalization of the current one:
// an explicit `--provider` or a provider shortcut instead of `--agent`, a named
// `--placement` instead of a bare positional, and `-o pane-id` instead of the
// `--print-pane-id` boolean. The expected argv is written out in full, so a
// change that quietly launches something else fails here.
func TestCreateNormalizesOntoTheExistingSplitWithoutNewComposition(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "agent requires an explicit provider and defaults to the right placement",
			args: []string{"agent", "--provider", "codex"},
			want: []string{"split", "--agent", "codex", "right"},
		},
		{
			name: "placement is a named flag, not a positional",
			args: []string{"agent", "--provider", "claude", "--placement", "down"},
			want: []string{"split", "--agent", "claude", "down"},
		},
		{
			name: "pane-id output replaces the print-pane-id boolean",
			args: []string{"agent", "--provider", "antigravity", "-o", "pane-id"},
			want: []string{"split", "--agent", "antigravity", "--print-pane-id", "right"},
		},
		{
			name: "none output is the explicit quiet spelling of the current default",
			args: []string{"agent", "--provider", "codex", "-o", "none"},
			want: []string{"split", "--agent", "codex", "right"},
		},
		{
			name: "the payload after -- is forwarded untouched",
			args: []string{"agent", "--provider", "codex", "--", "--help", "-h", "안녕"},
			want: []string{"split", "--agent", "codex", "right", "--", "--help", "-h", "안녕"},
		},
		{
			name: "the codex shortcut normalizes onto the same provider",
			args: []string{"codex", "--placement", "down"},
			want: []string{"split", "--agent", "codex", "down"},
		},
		{
			name: "the claude shortcut carries its payload too",
			args: []string{"claude", "--", "hello"},
			want: []string{"split", "--agent", "claude", "right", "--", "hello"},
		},
		{
			name: "the antigravity shortcut supports the pane-id bridge",
			args: []string{"antigravity", "-o", "pane-id"},
			want: []string{"split", "--agent", "antigravity", "--print-pane-id", "right"},
		},
		{
			name: "a shell surface is a Pane route, not an Agent route",
			args: []string{"pane"},
			want: []string{"split", "--agent", "shell", "right"},
		},
		{
			name: "the Pane route keeps the placement and pane-id spellings",
			args: []string{"pane", "--placement", "down", "-o", "pane-id"},
			want: []string{"split", "--agent", "shell", "--print-pane-id", "down"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			create, split := newTestCreateCommand()
			if _, _, err := runRoute(t, create, test.args...); err != nil {
				t.Fatalf("create %v error = %v", test.args, err)
			}
			if len(split.calls) != 1 {
				t.Fatalf("create %v reached the split handler %d times, want 1", test.args, len(split.calls))
			}
			if !reflect.DeepEqual(split.calls[0], test.want) {
				t.Fatalf("create %v forwarded %q, want %q", test.args, split.calls[0], test.want)
			}
		})
	}
}

// TestCreateAgentRefusesEveryNonProviderSpellingWithoutLaunchingAnything is the
// provider/mode/shortcut collision half of the table.
//
// Every row is a usage error (exit 2) that reaches the split handler zero
// times, which is what makes "the saved default is not canonical semantics" and
// "selective is not a provider" observable rather than merely documented.
func TestCreateAgentRefusesEveryNonProviderSpellingWithoutLaunchingAnything(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "a missing provider never falls back to the saved split mode",
			args: []string{"agent"},
			want: "requires --provider",
		},
		{
			name: "an empty provider is not a provider",
			args: []string{"agent", "--provider", ""},
			want: "requires --provider",
		},
		{
			name: "selective is a picker, not a provider",
			args: []string{"agent", "--provider", "selective"},
			want: "interactive picker, not a provider",
		},
		{
			name: "resume is a picker, not a provider",
			args: []string{"agent", "--provider", "resume"},
			want: "interactive picker, not a provider",
		},
		{
			name: "shell is a Pane, not an Agent provider",
			args: []string{"agent", "--provider", "shell"},
			want: "projmux create pane",
		},
		{
			name: "an unknown provider lists the enum",
			args: []string{"agent", "--provider", "gpt"},
			want: "accepted providers: codex, claude, antigravity",
		},
		{
			name: "a shortcut may not respell the provider it already names",
			args: []string{"codex", "--provider", "codex"},
			want: "already names the provider",
		},
		{
			name: "a shortcut may not override its provider either",
			args: []string{"claude", "--provider", "codex"},
			want: "already names the provider",
		},
		{
			name: "the placement enum is closed",
			args: []string{"agent", "--provider", "codex", "--placement", "left"},
			want: "--placement must be one of: right, down",
		},
		{
			name: "the placement positional is not accepted on the canonical route",
			args: []string{"agent", "--provider", "codex", "down"},
			want: "does not accept positional arguments",
		},
		{
			name: "the legacy force flag is not promoted to a canonical flag",
			args: []string{"agent", "--provider", "codex", "--force-agent"},
			want: "flag provided but not defined: -force-agent",
		},
		{
			name: "the legacy agent flag is not a canonical spelling",
			args: []string{"agent", "--agent", "codex"},
			want: "flag provided but not defined: -agent",
		},
		{
			name: "the print-pane-id boolean is not promoted either",
			args: []string{"agent", "--provider", "codex", "--print-pane-id"},
			want: "flag provided but not defined: -print-pane-id",
		},
		{
			name: "an empty payload is refused",
			args: []string{"agent", "--provider", "codex", "--"},
			want: "-- requires a payload",
		},
		{
			name: "the Pane route has no payload yet",
			args: []string{"pane", "--", "htop"},
			want: "does not take a payload yet",
		},
		{
			name: "the Pane route takes no provider",
			args: []string{"pane", "--provider", "codex"},
			want: "flag provided but not defined: -provider",
		},
		{
			name: "a provider is not a resource kind",
			args: []string{"gpt"},
			want: "create gpt is not available",
		},
		{
			name: "selective is not a create shortcut",
			args: []string{"selective"},
			want: "create selective is not available",
		},
		{
			name: "a kind is required",
			args: nil,
			want: "create requires a resource kind",
		},
		{
			name: "cwd is not a create projection",
			args: []string{"agent", "--provider", "codex", "-o", "cwd"},
			want: "invalid --output",
		},
		{
			name: "an unknown output token is refused",
			args: []string{"agent", "--provider", "codex", "-o", "bogus"},
			want: "invalid --output",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			create, split := newTestCreateCommand()
			stdout, _, err := runRoute(t, create, test.args...)
			if err == nil {
				t.Fatalf("create %v succeeded", test.args)
			}
			if !IsUsageError(err) {
				t.Fatalf("create %v error is not a usage error: %v", test.args, err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("create %v error = %q, want it to mention %q", test.args, err, test.want)
			}
			if stdout != "" {
				t.Fatalf("create %v wrote %q to stdout, want 0 bytes", test.args, stdout)
			}
			if len(split.calls) != 0 {
				t.Fatalf("create %v reached the split handler %d times, want 0", test.args, len(split.calls))
			}
		})
	}
}

// TestIdentityProjectionsOnTheCompatibilityBridgeNameTheMissingFlag is the
// replacement for the Phase 4 row that reported these tokens as "not wired yet".
//
// That message is now false: the composition exists, and every one of these
// projections works on the canonical route. What is missing is the flag that
// selects it. A missing flag is something the operator typed, so this is exit 2
// with the fix named in the message rather than exit 1 with an apology -- and it
// must still launch nothing.
func TestIdentityProjectionsOnTheCompatibilityBridgeNameTheMissingFlag(t *testing.T) {
	t.Parallel()

	for _, route := range []struct {
		name string
		args []string
	}{
		{name: "create agent", args: []string{"agent", "--provider", "codex"}},
		{name: "create codex", args: []string{"codex"}},
		// The bridge half of `create pane` has the same gap for the same
		// reason, so it carries the same message rather than a stale one about
		// the Agent composition.
		{name: "create pane", args: []string{"pane"}},
	} {
		for _, mode := range []string{"uid", "name", "ref", "metadata", "json"} {
			t.Run(route.name+" -o "+mode, func(t *testing.T) {
				t.Parallel()
				create, split := newTestCreateCommand()
				args := append(append([]string(nil), route.args...), "-o", mode)
				stdout, _, err := runRoute(t, create, args...)
				if err == nil {
					t.Fatalf("-o %s succeeded", mode)
				}
				if !IsUsageError(err) {
					t.Fatalf("-o %s names a fixable input error, so it must be a usage error: %v", mode, err)
				}
				if !strings.Contains(err.Error(), "--project") {
					t.Fatalf("-o %s error = %q, want it to name --project as the fix", mode, err)
				}
				if strings.Contains(err.Error(), "not wired yet") {
					t.Fatalf("-o %s still claims the composition is missing: %q", mode, err)
				}
				if stdout != "" || len(split.calls) != 0 {
					t.Fatalf("-o %s produced stdout %q and %d split calls", mode, stdout, len(split.calls))
				}
			})
		}
	}
}

// TestCreateRelaysTheSplitHandlerStreamsAndErrors proves the alias is
// transparent: whatever the existing handler writes and returns is what the
// canonical spelling produces.
func TestCreateRelaysTheSplitHandlerStreamsAndErrors(t *testing.T) {
	t.Parallel()

	split := &recordingArgv{out: "%42\n"}
	create := &createCommand{ai: split}
	stdout, _, err := runRoute(t, create, "codex", "-o", "pane-id")
	if err != nil {
		t.Fatalf("create codex error = %v", err)
	}
	if stdout != "%42\n" {
		t.Fatalf("stdout = %q, want the handler's own bytes", stdout)
	}

	failing := &recordingArgv{err: errSplitFailed}
	create = &createCommand{ai: failing}
	if _, _, err := runRoute(t, create, "agent", "--provider", "codex"); err != errSplitFailed {
		t.Fatalf("error = %v, want the handler's own error", err)
	}
}

var errSplitFailed = &UsageError{Message: "split failed"}
