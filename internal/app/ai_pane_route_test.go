package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// Existing semantic fixtures model a known, reachable Pane. Give their fake
// app server the containment answer shared writes now require. Route-denial
// tests use their own reader and never opt into this successful probe.
func testAIPaneRouteProbe(name string, args []string) ([]byte, bool) {
	if name == "tmux" && len(args) == 8 && args[0] == "-L" && args[1] == defaultAppSocket &&
		args[2] == "display-message" && args[3] == "-p" && args[4] == "-t" &&
		args[6] == "-F" && args[7] == aiPaneOptionRouteFormat {
		return codexHookDeliveryRouteRow(args[5], "test-pane-uid"), true
	}
	return nil, false
}

func TestSharedPaneRoutingSetClearRecord(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		t.Run(map[bool]string{false: "detached", true: "inherited"}[inherited], func(t *testing.T) {
			cmd := codexHookDeliveryTestCommand(t, "%7")
			prefix := []string{"-L", defaultAppSocket}
			if inherited {
				base := cmd.lookupEnv
				cmd.lookupEnv = func(name string) string {
					if name == "TMUX" {
						return "/tmp/test-inherited/socket,42,0"
					}
					return base(name)
				}
				prefix = []string{"-S", "/tmp/test-inherited/socket"}
				cmd.readCommand = func(context.Context, string, ...string) ([]byte, error) {
					t.Fatal("inherited bootstrap must not require existing marker or UID")
					return nil, os.ErrNotExist
				}
			}
			if err := cmd.setAIPaneOption("%7", aiPaneStateOption, "thinking"); err != nil {
				t.Fatal(err)
			}
			if err := cmd.clearAIPaneOption("%7", attentionAckOption); err != nil {
				t.Fatal(err)
			}
			cmd.recordAIPaneOption("%7", aiPaneHookActiveOption, "1")
			want := [][]string{
				{"set-option", "-p", "-t", "%7", aiPaneStateOption, "thinking"},
				{"set-option", "-p", "-u", "-t", "%7", attentionAckOption},
				{"set-option", "-p", "-t", "%7", aiPaneHookActiveOption, "1"},
			}
			commands := cmdRecorder(cmd).commands
			if len(commands) != len(want) {
				t.Fatalf("writes = %#v", commands)
			}
			for i, args := range want {
				if !reflect.DeepEqual(commands[i].args, append(slices.Clone(prefix), args...)) {
					t.Fatalf("write %d = %q, want exact route %q and option %q", i, commands[i].args, prefix, args)
				}
			}
			if reason := cmd.recordedAIPaneWriteFailure(); reason != "" {
				t.Fatalf("marker failed: %s", reason)
			}
		})
	}
}

func TestSharedPaneRoutingDenialIsBoundedAndWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  []byte
		err  error
	}{
		{"missing server", []byte("no server running on /tmp/private/socket"), os.ErrNotExist},
		{"missing pane", nil, errors.New("can't find pane: %7")},
		{"empty answer", []byte("\n"), nil},
		{"foreign server", []byte(strings.Join([]string{"", "%7", "pane-7"}, tmuxRowSep)), nil},
		{"different pane", codexHookDeliveryRouteRow("%8", "pane-8"), nil},
		{"unmanaged pane", codexHookDeliveryRouteRow("%7", ""), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := codexHookDeliveryTestCommand(t, "%7")
			cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
				want := routedAppSocketArgs("display-message", "-p", "-t", "%7", "-F", aiPaneOptionRouteFormat)
				if name != "tmux" || !reflect.DeepEqual(args, want) {
					t.Fatalf("unexpected probe %s %q", name, args)
				}
				return tc.row, tc.err
			}
			for _, err := range []error{cmd.setAIPaneOption("%7", aiPaneStateOption, "thinking"), cmd.clearAIPaneOption("%7", attentionAckOption)} {
				if !errors.Is(err, errAIPaneWriteUnavailable) {
					t.Fatalf("refusal = %v", err)
				}
			}
			cmd.recordAIPaneOption("%7", aiPaneHookActiveOption, "1")
			if got := cmdRecorder(cmd).commands; len(got) != 0 {
				t.Fatalf("refusal wrote %q", got)
			}
			entry := cmd.honestAIIngestResult(aiIngestLogEntry{Source: "claude-hook", Result: "state"})
			if entry.Result != "error" || entry.Reason != aiPaneWriteReasonMarkerUnavailable {
				t.Fatalf("dishonest record: %+v", entry)
			}
		})
	}
}

func TestSharedPaneRoutingProviderConsumers(t *testing.T) {
	for _, tc := range []struct {
		source, payload, result string
		args                    []string
	}{
		{"codex-hook", `{"hook_event_name":"UserPromptSubmit","session_id":"session","cwd":"/repo"}`, "state", nil},
		{"claude-hook", `{"hook_event_name":"UserPromptSubmit","session_id":"session","cwd":"/repo"}`, "state", nil},
		{"antigravity-hook", `{"conversationId":"session","workspacePaths":["/repo"]}`, "state", []string{"--event", "pre_invocation"}},
		{"bell", `{}`, "notify", []string{"--pane", "%7"}},
	} {
		t.Run(tc.source, func(t *testing.T) {
			cmd := codexHookDeliveryTestCommand(t, "%7")
			baseEnv := cmd.lookupEnv
			cmd.lookupEnv = func(name string) string {
				if name == "TMUX_PANE" {
					return "%7"
				}
				return baseEnv(name)
			}
			baseRead := cmd.readCommand
			cmd.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "-t", "%7", aiBellPaneFormat}) {
					return []byte(strings.Join([]string{"project", "@1", "window", "%7", "title", "sh", "/tmp/test/socket"}, tmuxRowSep)), nil
				}
				return baseRead(ctx, name, args...)
			}
			cmd.notifyStore = &stubNotifyStore{}
			cmd.stdin = strings.NewReader(tc.payload)
			cmd.now = func() time.Time { return time.Unix(100, 0) }
			if err := cmd.Run(append([]string{"ingest", tc.source}, tc.args...), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			writes := cmdRecorder(cmd).commands
			seen := map[string]bool{}
			for _, command := range writes {
				plain := stripRecordedTmuxRoute(command.args)
				if command.name != "tmux" || len(plain) == 0 || plain[0] != "set-option" {
					continue
				}
				if !reflect.DeepEqual(command.args[:len(command.args)-len(plain)], []string{"-L", defaultAppSocket}) {
					t.Fatalf("provider bypass: %q", command.args)
				}
				if len(plain) == 6 && plain[2] == "-t" {
					seen[plain[4]] = true
				}
			}
			if tc.source == "bell" {
				if !seen[aiBellDedupeOption] {
					t.Fatal("bell marker missing")
				}
			} else if !seen[aiPaneHookActiveOption] || !seen[aiPaneStateOption] || !seen[aiPaneResumeIDOption] {
				t.Fatalf("provider marker/status/resume writes missing: %v", seen)
			}
			path, _ := cmd.aiIngestLogPath()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var entry aiIngestLogEntry
			if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
				t.Fatal(err)
			}
			if entry.Result != tc.result {
				t.Fatalf("record = %+v", entry)
			}
		})
	}
}

func TestSharedPaneRoutingLaunchBeforeFirstMarker(t *testing.T) {
	for _, provider := range []string{aiModeCodex, aiModeClaude, aiModeAntigravity} {
		t.Run(provider, func(t *testing.T) {
			cmd := testAICommand(t.TempDir())
			// Canonical detached materialization already owns an exact runner.
			// Both resource UID and managed marker are absent before binding.
			options := map[string]string{}
			runner := phase0RTmuxRunner(func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "tmux" || !slices.Equal(args[:2], []string{"-S", "/tmp/materialization/socket"}) {
					t.Fatalf("materialization route escaped: %s %q", name, args)
				}
				args = args[2:]
				switch args[0] {
				case "show-options":
					return []byte(options[args[len(args)-1]]), nil
				case "set-option":
					if slices.Contains(args, "-u") {
						delete(options, args[len(args)-1])
					} else {
						options[args[len(args)-2]] = args[len(args)-1]
					}
				}
				return nil, nil
			})
			exact := explicitTmuxRunner{runner: runner, target: tmuxTransport{Kind: tmuxSocketPath, Value: "/tmp/materialization/socket", Source: tmuxSocketPathSource}}
			if err := cmd.BindAgentPaneOnRoute(context.Background(), exact, agentPaneBinding{PaneID: "%7", Provider: provider, ContextDir: "/repo", Title: "topic", ConversationID: "session"}); err != nil {
				t.Fatal(err)
			}
			if options[aiPaneManagedOption] != "1" || options[aiPaneAgentOption] != provider || options[aiPaneResumeIDOption] != "session" || options[tmuxopts.PaneUID] != "" {
				t.Fatalf("pre-UID materialization failed: %v", options)
			}
		})
	}
}

// Semantic command assertions can omit the route; an expectation that names
// -L or -S still compares the entire exact argv. Dedicated routing assertions
// below always require their route explicitly.
func sameRecordedAICommandArgs(name string, got, want []string) bool {
	if name == "tmux" && len(want) > 0 && want[0] != "-L" && want[0] != "-S" {
		got = stripRecordedTmuxRoute(got)
	}
	return reflect.DeepEqual(got, want)
}

func sameRecordedAICommands(got, want []recordedAICommand) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].name != want[i].name || !sameRecordedAICommandArgs(got[i].name, got[i].args, want[i].args) {
			return false
		}
	}
	return true
}

// This fixture drives the two caller paths in the preserved failure cohort.
// The only injected fault is tmux's rejection of an unprefixed write when the
// hook has no inherited server. Attribution and the app route both succeed.
func TestSharedPaneRoutingCodexIngest(t *testing.T) {
	for _, tc := range []struct{ name, event, result, option, value string }{
		{"marker", "PreToolUse", "quiet", aiPaneHookActiveOption, "1"},
		{"ordinary", "UserPromptSubmit", "state", aiPaneStateOption, "thinking"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := codexHookDeliveryTestCommand(t, "%7")
			cmd.stdin = strings.NewReader(`{"hook_event_name":"` + tc.event + `","session_id":"codex-session","cwd":"/repo/projmux"}`)
			landed := map[string]string{}
			cmd.runCommand = func(_ context.Context, name string, args ...string) error {
				plain := stripRecordedTmuxRoute(args)
				if name != "tmux" || len(plain) == 0 || plain[0] != "set-option" {
					return nil
				}
				if !reflect.DeepEqual(args[:len(args)-len(plain)], []string{"-L", defaultAppSocket}) {
					return errors.New("no server running on /tmp/isolated/default")
				}
				if len(plain) == 6 && plain[1] == "-p" && plain[2] == "-t" {
					landed[plain[4]] = plain[5]
				}
				return nil
			}
			err := cmd.Run([]string{"ingest", "codex-hook"}, &bytes.Buffer{}, &bytes.Buffer{})
			path, pathErr := cmd.aiIngestLogPath()
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			lines := nonEmptyLines(string(data))
			var record aiIngestLogEntry
			if len(lines) != 1 {
				t.Fatalf("records = %d, want 1", len(lines))
			}
			if decodeErr := json.Unmarshal([]byte(lines[0]), &record); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if err != nil || record.Result != tc.result || landed[tc.option] != tc.value {
				t.Fatalf("%s caller did not reach its proven route: err=%v result=%q reason=%q %s=%q; want result=%q %s=%q", tc.name, err, record.Result, record.Reason, tc.option, landed[tc.option], tc.result, tc.option, tc.value)
			}
		})
	}
}
