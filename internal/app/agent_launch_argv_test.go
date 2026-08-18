package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// The tests in this file exist because the Phase 6 workspace test asserted that
// the argv *contained* the payload token, and a token absorbed by a variadic
// option is still contained in the argv. So substring containment cannot
// distinguish "the provider received a prompt" from "the provider received one
// more directory", which is precisely the installed regression this repairs.
//
// What replaces it is a model of the provider's own option grammar: the argv is
// parsed back the way the provider parses it, and the recovered workspace and
// prompt are compared with what the launch was asked to deliver.

// parsedProviderArgv is what a provider's parser would recover from one argv
// tail.
type parsedProviderArgv struct {
	workingRoot string
	roots       []string
	operands    []string
	// unknownOptions is every option-looking token the grammar does not
	// declare. It exists so a payload that leaks into option position fails
	// loudly instead of quietly landing in operands.
	unknownOptions []string
}

// parseProviderArgvLikeProvider replays one provider's documented option
// grammar over an argv tail.
//
// It models the three rules that actually decide this Phase's outcome: a
// one-value option consumes exactly one operand, a variadic option keeps
// consuming operands until an option-looking token or the terminator stops it,
// and the terminator makes everything after it an operand. Every one of those is
// measured behavior of the installed CLIs, recorded in
// providerLaunchGrammarFor's doc comment.
func parseProviderArgvLikeProvider(grammar providerLaunchGrammar, args []string) parsedProviderArgv {
	var out parsedProviderArgv
	for i := 0; i < len(args); i++ {
		token := args[i]
		switch {
		case grammar.payloadTerminator != "" && token == grammar.payloadTerminator:
			out.operands = append(out.operands, args[i+1:]...)
			return out
		case grammar.workingRootFlag != "" && token == grammar.workingRootFlag:
			if i+1 < len(args) {
				out.workingRoot = args[i+1]
				i++
			}
		case grammar.additionalRootFlag != "" && token == grammar.additionalRootFlag:
			if grammar.additionalRootArity == optionVariadicValues {
				for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					out.roots = append(out.roots, args[i+1])
					i++
				}
				continue
			}
			if i+1 < len(args) {
				out.roots = append(out.roots, args[i+1])
				i++
			}
		case strings.HasPrefix(token, "-"):
			out.unknownOptions = append(out.unknownOptions, token)
		default:
			out.operands = append(out.operands, token)
		}
	}
	return out
}

// TestProviderLaunchArgvSurvivesProviderOptionCardinality is the acceptance
// test: for every provider, root count and payload shape, the argv the launch
// produces is parsed back by the provider's own grammar into exactly the
// workspace and prompt it was given.
func TestProviderLaunchArgvSurvivesProviderOptionCardinality(t *testing.T) {
	t.Parallel()
	const cwd = "/work/owner"
	roots := map[string][]string{
		"zero":     nil,
		"one":      {"/work/extra-a"},
		"multiple": {"/work/extra-a", "/work/extra-b", "/work/extra-c"},
	}
	payloads := map[string][]string{
		"empty":       nil,
		"one token":   {"summarize the release notes"},
		"many tokens": {"summarize", "the", "release", "notes"},
		// A prompt that begins with `-` is an operand only for a provider whose
		// parser has a terminator to put in front of it. Codex has none in this
		// grammar by choice -- see TestCodexPayloadBeginningWithDashIsAKnownProviderLimit.
		"leading dash": {"--not-an-option"},
	}
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		grammar := providerLaunchGrammarFor(provider)
		for rootName, wantRoots := range roots {
			for payloadName, payload := range payloads {
				if payloadName == "leading dash" && grammar.payloadTerminator == "" {
					continue
				}
				t.Run(provider+"/"+rootName+"/"+payloadName, func(t *testing.T) {
					t.Parallel()
					args, err := providerLaunchArgs(provider, coremetadata.AgentWorkspace{
						CWD:                     cwd,
						AdditionalWritableRoots: wantRoots,
					}, payload)
					if err != nil {
						t.Fatalf("providerLaunchArgs: %v", err)
					}
					got := parseProviderArgvLikeProvider(grammar, args)
					if !slices.Equal(got.roots, wantRoots) {
						t.Errorf("argv %q: provider recovers roots %q, want %q", args, got.roots, wantRoots)
					}
					if !slices.Equal(got.operands, payload) {
						t.Errorf("argv %q: provider recovers prompt %q, want %q", args, got.operands, payload)
					}
					if len(got.unknownOptions) != 0 {
						t.Errorf("argv %q: provider sees undeclared options %q", args, got.unknownOptions)
					}
					// The working root is in argv only for the provider that
					// needs it there; the others take it from the pane's cwd.
					wantWorkingRoot := ""
					if grammar.workingRootFlag != "" {
						wantWorkingRoot = cwd
					}
					if got.workingRoot != wantWorkingRoot {
						t.Errorf("argv %q: provider recovers working root %q, want %q", args, got.workingRoot, wantWorkingRoot)
					}
				})
			}
		}
	}
}

// TestProviderArgvModelDetectsTheAbsorbedPayload proves the model above is not
// vacuous. It replays the pre-fix concatenation -- workspace options followed
// directly by the payload -- and asserts the provider loses the prompt, which is
// the installed failure that was observed as an activation timeout with no
// transcript.
func TestProviderArgvModelDetectsTheAbsorbedPayload(t *testing.T) {
	t.Parallel()
	grammar := providerLaunchGrammarFor(aiModeClaude)
	absorbed := parseProviderArgvLikeProvider(grammar, []string{
		"--add-dir", "/work/extra-a", "summarize the release notes",
	})
	if len(absorbed.operands) != 0 {
		t.Fatalf("the model recovered a prompt %q from the absorbing argv, so it cannot detect the regression", absorbed.operands)
	}
	if !slices.Contains(absorbed.roots, "summarize the release notes") {
		t.Fatalf("the model recovered roots %q, want the payload absorbed as a directory", absorbed.roots)
	}
	repaired, err := providerLaunchArgs(aiModeClaude, coremetadata.AgentWorkspace{
		AdditionalWritableRoots: []string{"/work/extra-a"},
	}, []string{"summarize the release notes"})
	if err != nil {
		t.Fatalf("providerLaunchArgs: %v", err)
	}
	if got := parseProviderArgvLikeProvider(grammar, repaired); !slices.Equal(got.operands, []string{"summarize the release notes"}) {
		t.Fatalf("repaired argv %q yields prompt %q, want the payload preserved", repaired, got.operands)
	}
}

// TestCodexPayloadBeginningWithDashIsAKnownProviderLimit records the one payload
// shape Codex cannot carry, so it is a stated limit rather than a gap someone
// discovers later.
//
// Codex's `--add-dir <DIR>` takes exactly one value, so no payload is ever
// absorbed and this Phase has nothing to repair in its argv. Its parser would
// accept `--`, but adding one would change the shipped Codex argv, and the
// corrective's contract is to leave Codex byte-identical. The consequence is
// that a Codex prompt beginning with `-` still reaches the provider in option
// position, exactly as it does on the pre-corrective build.
func TestCodexPayloadBeginningWithDashIsAKnownProviderLimit(t *testing.T) {
	t.Parallel()
	args, err := providerLaunchArgs(aiModeCodex, coremetadata.AgentWorkspace{CWD: "/work/owner"}, []string{"--not-an-option"})
	if err != nil {
		t.Fatalf("providerLaunchArgs: %v", err)
	}
	if !slices.Equal(args, []string{"-C", "/work/owner", "--not-an-option"}) {
		t.Fatalf("codex argv = %q, want the shipped concatenation preserved", args)
	}
	parsed := parseProviderArgvLikeProvider(providerLaunchGrammarFor(aiModeCodex), args)
	if len(parsed.operands) != 0 || !slices.Equal(parsed.unknownOptions, []string{"--not-an-option"}) {
		t.Fatalf("codex recovers prompt %q and options %q; this test pins the unchanged limit, so update it deliberately",
			parsed.operands, parsed.unknownOptions)
	}
	// Claude, which does declare a terminator, delivers the same payload.
	claudeArgs, err := providerLaunchArgs(aiModeClaude, coremetadata.AgentWorkspace{CWD: "/work/owner"}, []string{"--not-an-option"})
	if err != nil {
		t.Fatalf("providerLaunchArgs claude: %v", err)
	}
	claudeParsed := parseProviderArgvLikeProvider(providerLaunchGrammarFor(aiModeClaude), claudeArgs)
	if !slices.Equal(claudeParsed.operands, []string{"--not-an-option"}) {
		t.Fatalf("claude recovers prompt %q, want the dash-leading payload delivered", claudeParsed.operands)
	}
}

// TestProviderLaunchArgvExactShapes pins the literal argv per provider, so a
// later change to the grammar has to state which provider argv it is changing.
func TestProviderLaunchArgvExactShapes(t *testing.T) {
	t.Parallel()
	workspace := coremetadata.AgentWorkspace{
		CWD:                     "/work/owner",
		AdditionalWritableRoots: []string{"/work/extra-a", "/work/extra-b"},
	}
	tests := []struct {
		name      string
		provider  string
		workspace coremetadata.AgentWorkspace
		payload   []string
		want      []string
	}{
		{
			name:      "claude carries every root in one variadic occurrence and terminates before the prompt",
			provider:  aiModeClaude,
			workspace: workspace,
			payload:   []string{"do the thing"},
			want:      []string{"--add-dir", "/work/extra-a", "/work/extra-b", "--", "do the thing"},
		},
		{
			name:      "claude with no roots still terminates before the prompt",
			provider:  aiModeClaude,
			workspace: coremetadata.AgentWorkspace{CWD: "/work/owner"},
			payload:   []string{"do the thing"},
			want:      []string{"--", "do the thing"},
		},
		{
			name:      "claude with an empty payload adds nothing after the roots",
			provider:  aiModeClaude,
			workspace: workspace,
			want:      []string{"--add-dir", "/work/extra-a", "/work/extra-b"},
		},
		{
			name:      "claude interactive launch is a bare argv",
			provider:  aiModeClaude,
			workspace: coremetadata.AgentWorkspace{CWD: "/work/owner"},
			want:      []string{},
		},
		{
			name:      "codex repeats its one-value root option and needs no terminator",
			provider:  aiModeCodex,
			workspace: workspace,
			payload:   []string{"do the thing"},
			want:      []string{"-C", "/work/owner", "--add-dir", "/work/extra-a", "--add-dir", "/work/extra-b", "do the thing"},
		},
		{
			name:      "codex with an empty payload keeps the workspace options only",
			provider:  aiModeCodex,
			workspace: workspace,
			want:      []string{"-C", "/work/owner", "--add-dir", "/work/extra-a", "--add-dir", "/work/extra-b"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := providerLaunchArgs(test.provider, test.workspace, test.payload)
			if err != nil {
				t.Fatalf("providerLaunchArgs: %v", err)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("providerLaunchArgs = %q, want %q", got, test.want)
			}
		})
	}
}

// TestProviderLaunchArgsRefusesRootsForProviderWithoutRootFlag keeps the refusal
// the workspace resolver already performs from degrading into an invented flag
// if a stored workspace ever outlives that gate.
func TestProviderLaunchArgsRefusesRootsForProviderWithoutRootFlag(t *testing.T) {
	t.Parallel()
	args, err := providerLaunchArgs(aiModeAntigravity, coremetadata.AgentWorkspace{
		CWD:                     "/work/owner",
		AdditionalWritableRoots: []string{"/work/extra-a"},
	}, []string{"do the thing"})
	if err == nil {
		t.Fatalf("providerLaunchArgs = %q, want a refusal for a provider with no additional-root option", args)
	}
	if !strings.Contains(err.Error(), "additional writable roots") {
		t.Fatalf("refusal = %v, want it to name the unsupported additional roots", err)
	}
	// Without roots the provider is untouched: nothing is invented, and the
	// payload is still delivered as an operand.
	plain, err := providerLaunchArgs(aiModeAntigravity, coremetadata.AgentWorkspace{CWD: "/work/owner"}, []string{"do the thing"})
	if err != nil {
		t.Fatalf("providerLaunchArgs without roots: %v", err)
	}
	if !slices.Equal(plain, []string{"do the thing"}) {
		t.Fatalf("providerLaunchArgs without roots = %q, want the payload alone", plain)
	}
}

// TestPlanAgentLaunchAndResumeShareOneProviderGrammar runs both verbs through
// the real seams and compares the exec argv the pane will run. It is the test
// that would have caught the installed regression: the assertion is on the
// exact exec tail, not on containment.
func TestPlanAgentLaunchAndResumeShareOneProviderGrammar(t *testing.T) {
	t.Parallel()
	cmd := agentLaunchArgvTestCommand(t)
	workspace := coremetadata.AgentWorkspace{
		CWD:                     "/work/owner",
		AdditionalWritableRoots: []string{"/work/extra-a", "/work/extra-b"},
	}
	tests := []struct {
		name     string
		provider string
		want     []string
	}{
		{
			name:     "claude create keeps the prompt after every root",
			provider: aiModeClaude,
			want:     []string{"--add-dir", "/work/extra-a", "/work/extra-b", "--", "do the thing"},
		},
		{
			name:     "codex create keeps its shipped one-value spelling",
			provider: aiModeCodex,
			want:     []string{"-C", "/work/owner", "--add-dir", "/work/extra-a", "--add-dir", "/work/extra-b", "do the thing"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, argv, err := cmd.PlanAgentLaunch(test.provider, workspace, []string{"do the thing"})
			if err != nil {
				t.Fatalf("PlanAgentLaunch: %v", err)
			}
			gotArgs := execArgvTail(t, argv, test.provider)
			if !slices.Equal(gotArgs, test.want) {
				t.Fatalf("%s exec argv tail = %q, want %q", test.provider, gotArgs, test.want)
			}
			parsed := parseProviderArgvLikeProvider(providerLaunchGrammarFor(test.provider), gotArgs)
			if !slices.Equal(parsed.operands, []string{"do the thing"}) {
				t.Fatalf("%s pane prompt = %q, want the task payload", test.provider, parsed.operands)
			}
			if !slices.Equal(parsed.roots, workspace.AdditionalWritableRoots) {
				t.Fatalf("%s pane roots = %q, want %q", test.provider, parsed.roots, workspace.AdditionalWritableRoots)
			}
		})
	}
	// Resume carries the same workspace grammar with no payload, so the
	// conversation option -- not a terminator -- is what stops the variadic
	// root option. A terminator here would make the resume id an operand.
	t.Run("claude resume shares the grammar and adds no terminator", func(t *testing.T) {
		t.Parallel()
		_, argv, err := cmd.PlanAgentResume(aiModeClaude, workspace, "11111111-2222-3333-4444-555555555555")
		if err != nil {
			t.Fatalf("PlanAgentResume: %v", err)
		}
		gotArgs := execArgvTail(t, argv, aiModeClaude)
		want := []string{"--add-dir", "/work/extra-a", "/work/extra-b", "--resume", "11111111-2222-3333-4444-555555555555"}
		if !slices.Equal(gotArgs, want) {
			t.Fatalf("claude resume exec argv tail = %q, want %q", gotArgs, want)
		}
		if slices.Contains(gotArgs, "--") {
			t.Fatalf("claude resume argv %q carries a payload terminator with no payload", gotArgs)
		}
	})
}

// agentLaunchArgvTestCommand returns an aiCommand whose provider binaries all
// resolve inside a temp dir, so the launch seams can be planned without any
// provider installed on the machine running the test.
func agentLaunchArgvTestCommand(t *testing.T) *aiCommand {
	t.Helper()
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	binaries := map[string]string{}
	for _, provider := range []string{aiModeCodex, aiModeClaude, aiModeAntigravity} {
		binaries[provider] = writeExecutable(t, filepath.Join(binDir, provider))
	}
	// `agy` is the antigravity binary name the resolver looks for.
	binaries["agy"] = writeExecutable(t, filepath.Join(binDir, "agy"))
	cmd := testAICommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "command" && len(args) == 2 && args[0] == "-v" {
			if path, ok := binaries[args[1]]; ok {
				return []byte(path + "\n"), nil
			}
		}
		return nil, os.ErrNotExist
	}
	return cmd
}

// execArgvTail recovers the provider argv from the shell command the pane runs.
//
// The launch seams return `[shell, -lc, script]` where the script ends in an
// `exec` of the shell-quoted provider argv. Unquoting that tail keeps the
// assertion on the exact argv rather than on a substring of the script.
func execArgvTail(t *testing.T, argv []string, provider string) []string {
	t.Helper()
	if len(argv) != 3 || argv[1] != "-lc" {
		t.Fatalf("launch argv = %q, want a [shell -lc script] wrapper", argv)
	}
	script := argv[2]
	marker := " && exec "
	index := strings.LastIndex(script, marker)
	if index < 0 {
		t.Fatalf("launch script %q carries no exec tail", script)
	}
	words := splitShellQuotedWords(t, script[index+len(marker):])
	if len(words) == 0 {
		t.Fatalf("launch script %q carries an empty exec tail", script)
	}
	if got := filepath.Base(words[0]); got != providerBinaryName(provider) {
		t.Fatalf("exec runs %q, want the %s binary", words[0], provider)
	}
	return words[1:]
}

// providerBinaryName is the executable one provider is launched through.
func providerBinaryName(provider string) string {
	if provider == aiModeAntigravity {
		return "agy"
	}
	return provider
}

// splitShellQuotedWords reverses shellQuote over a whole command tail.
//
// agentLaunchCommandForArgv single-quotes every argument, so the tail is a
// space-separated list of single-quoted words in which an embedded quote appears
// as the `'\”` escape. Walking it quote-aware is what lets a multi-word prompt
// be compared as the one argument the provider receives.
func splitShellQuotedWords(t *testing.T, tail string) []string {
	t.Helper()
	var words []string
	var current strings.Builder
	inWord, quoted := false, false
	for i := 0; i < len(tail); i++ {
		switch c := tail[i]; {
		case c == '\'':
			quoted = !quoted
			inWord = true
		case c == ' ' && !quoted:
			if inWord {
				words = append(words, current.String())
				current.Reset()
				inWord = false
			}
		default:
			current.WriteByte(c)
			inWord = true
		}
	}
	if quoted {
		t.Fatalf("exec tail %q ends inside a quoted word", tail)
	}
	if inWord {
		words = append(words, current.String())
	}
	return words
}
