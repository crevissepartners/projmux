package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type bindingFailureRunner struct {
	targetFlag, targetValue string
	title                   string
	options                 map[string]string
	writes                  int
	failWrite               int
	failed                  bool
	failureCommand          int
	commands                [][]string
}

func (r *bindingFailureRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "tmux" || len(args) < 3 || args[0] != r.targetFlag || args[1] != r.targetValue {
		return nil, fmt.Errorf("unexpected route %s %q", name, args)
	}
	r.commands = append(r.commands, append([]string(nil), args...))
	args = args[2:]
	switch args[0] {
	case "display-message":
		return []byte(r.title + "\n"), nil
	case "show-options":
		return []byte(r.options[args[len(args)-1]] + "\n"), nil
	case "select-pane":
		r.writes++
		if r.failWrite == r.writes && !r.failed {
			r.failed = true
			r.failureCommand = len(r.commands) - 1
			return nil, errors.New("injected title write failure")
		}
		r.title = args[2]
		return nil, nil
	case "set-option":
		r.writes++
		if r.failWrite == r.writes && !r.failed {
			r.failed = true
			r.failureCommand = len(r.commands) - 1
			return nil, errors.New("injected option write failure")
		}
		option := args[len(args)-2]
		if slices.Contains(args, "-u") {
			option = args[len(args)-1]
			delete(r.options, option)
		} else {
			r.options[option] = args[len(args)-1]
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected tmux operation %q", args)
	}
}

func TestAgentPaneBinderRequiredOptionsAndFailureCompensationOnExactRoutes(t *testing.T) {
	fixedAt := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)
	common := func(provider string) map[string]string {
		return map[string]string{
			aiPaneManagedOption: "1", aiPaneAgentOption: provider, aiPaneLaunchAuthorshipOption: "1",
			aiPaneContextOption: "/work", aiPaneStateOption: "idle",
		}
	}
	type bindingShape struct {
		name     string
		binding  agentPaneBinding
		expected map[string]string
	}
	createExpected := common(aiModeCodex)
	createExpected[aiPaneCodexAuthorityOption] = codexAuthorityHook
	createExpected[aiPaneCodexReasonOption] = "native-fallback"
	replayExpected := common(aiModeCodex)
	maps.Copy(replayExpected, map[string]string{
		aiPaneTopicOption: "stored replay", aiPaneTopicManualOption: "on",
		aiPaneSessionIDOption: "thread-replay", aiPaneResumeIDOption: "thread-replay",
		aiPaneResumeUpdatedAtOption: fixedAt.Format(time.RFC3339), aiPaneCodexAuthorityOption: codexAuthorityHook,
		aiPaneCodexReasonOption: "native-fallback",
	})
	resumeExpected := common(aiModeClaude)
	maps.Copy(resumeExpected, map[string]string{
		aiPaneTopicOption: "stored resume", aiPaneTopicManualOption: "on",
		aiPaneSessionIDOption: "conversation-resume", aiPaneResumeIDOption: "conversation-resume",
		aiPaneResumeSourceOption: "cli", aiPaneResumeUpdatedAtOption: fixedAt.Format(time.RFC3339),
	})
	nativeExpected := common(aiModeCodex)
	maps.Copy(nativeExpected, map[string]string{
		aiPaneSessionIDOption: "thread-native", aiPaneResumeIDOption: "thread-native",
		aiPaneResumeSourceOption: "app-server", aiPaneResumeUpdatedAtOption: fixedAt.Format(time.RFC3339),
		aiPaneThreadIDOption: "thread-native", aiPaneCodexAuthorityOption: codexAuthorityHook,
		aiPaneCodexReasonOption: "observer-starting",
	})
	shapes := []bindingShape{
		{name: "create", binding: agentPaneBinding{PaneID: "%9", Provider: aiModeCodex, ContextDir: "/work", Title: "codex:create"}, expected: createExpected},
		{name: "topology-replay", binding: agentPaneBinding{
			PaneID: "%9", Provider: aiModeCodex, ContextDir: "/work", Title: "codex:replay", Topic: "stored replay", TopicManual: true,
			ConversationID: "thread-replay",
		}, expected: replayExpected},
		{name: "resume", binding: agentPaneBinding{
			PaneID: "%9", Provider: aiModeClaude, ContextDir: "/work", Title: "claude:resume", Topic: "stored resume", TopicManual: true,
			ConversationID: "conversation-resume", ResumeSource: "cli",
		}, expected: resumeExpected},
		{name: "native", binding: agentPaneBinding{
			PaneID: "%9", Provider: aiModeCodex, ContextDir: "/work", Title: "codex:native", ConversationID: "thread-native",
			ResumeSource: "app-server", ThreadID: "thread-native", NativeCodex: true,
		}, expected: nativeExpected},
	}
	routes := []tmuxTransport{{Kind: tmuxSocketName, Value: "phase2", Source: tmuxSocketNameSource}, {Kind: tmuxSocketPath, Value: "/tmp/phase2.sock", Source: tmuxSocketPathSource}}
	for _, shape := range shapes {
		for _, route := range routes {
			for _, failure := range []struct {
				name  string
				write int
			}{{name: "success"}, {name: "first", write: 2}, {name: "middle", write: 9}, {name: "last", write: 16}} {
				t.Run(shape.name+"/"+strings.TrimPrefix(route.Flag(), "-")+"/"+failure.name, func(t *testing.T) {
					before := map[string]string{
						aiPaneManagedOption: "old-managed", aiPaneContextOption: "/old", aiPaneCodexEpochOption: "old-epoch", "@sibling": "keep",
					}
					raw := &bindingFailureRunner{
						targetFlag: route.Flag(), targetValue: route.Value, title: "old-title", options: map[string]string{},
						failWrite: failure.write, failureCommand: -1,
					}
					maps.Copy(raw.options, before)
					runner := explicitTmuxRunner{runner: raw, target: route}
					binder := &aiCommand{now: func() time.Time { return fixedAt }}
					err := binder.BindAgentPaneOnRoute(context.Background(), runner, shape.binding)
					if failure.write != 0 {
						var typed *agentPaneBindError
						if !errors.As(err, &typed) {
							t.Fatalf("error = %T %v, want typed bind failure", err, err)
						}
						if raw.title != "old-title" || !reflect.DeepEqual(raw.options, before) {
							t.Fatalf("partial binding remained: title=%q options=%#v want title old-title options %#v", raw.title, raw.options, before)
						}
						assertBindingCompensationOrder(t, raw, failure.write)
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					expected := maps.Clone(shape.expected)
					expected["@sibling"] = "keep"
					if raw.title != shape.binding.Title || !reflect.DeepEqual(raw.options, expected) {
						t.Fatalf("binding = title %q options %#v, want title %q options %#v", raw.title, raw.options, shape.binding.Title, expected)
					}
				})
			}
		}
	}
}

var agentPaneBindingOptionOrder = []string{
	aiPaneManagedOption, aiPaneAgentOption, aiPaneLaunchAuthorshipOption, aiPaneContextOption,
	aiPaneTopicOption, aiPaneTopicManualOption, aiPaneStateOption, aiPaneSessionIDOption,
	aiPaneResumeIDOption, aiPaneResumeSourceOption, aiPaneResumeUpdatedAtOption, aiPaneThreadIDOption,
	aiPaneCodexAuthorityOption, aiPaneCodexEpochOption, aiPaneCodexReasonOption,
}

func assertBindingCompensationOrder(t *testing.T, runner *bindingFailureRunner, failWrite int) {
	t.Helper()
	if runner.failureCommand < 0 {
		t.Fatal("failure command was not recorded")
	}
	restores := runner.commands[runner.failureCommand+1:]
	appliedOptions := failWrite - 2 // title is write 1; failWrite itself did not apply.
	if len(restores) != appliedOptions+1 {
		t.Fatalf("compensation commands = %q, want %d option restores plus title", restores, appliedOptions)
	}
	for index := range appliedOptions {
		argv := restores[index][2:]
		option := argv[len(argv)-2]
		if slices.Contains(argv, "-u") {
			option = argv[len(argv)-1]
		}
		want := agentPaneBindingOptionOrder[appliedOptions-1-index]
		if option != want {
			t.Fatalf("compensation[%d] option = %q, want reverse-order %q; commands=%q", index, option, want, restores)
		}
	}
	if argv := restores[len(restores)-1][2:]; len(argv) == 0 || argv[0] != "select-pane" {
		t.Fatalf("final compensation = %q, want title restore", restores[len(restores)-1])
	}
}
