package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// All tmux responses are fixture data; no tmux or provider process is started.
// The execution probe deliberately refuses split after observing the fully
// prepared supervisor argv, including the activation's reply-only flag.
type replyOnlyRelaunchRunner struct {
	socket, project, window, shell, session, root string
	launch                                        []string
	calls                                         [][]string
	cause                                         error
	environment                                   map[string]string
}

func (r *replyOnlyRelaunchRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name != "tmux" || len(args) < 3 || args[0] != "-S" || args[1] != r.socket {
		return nil, errors.New("fixture refused non-exact transport")
	}
	r.calls = append(r.calls, slices.Clone(args))
	args = args[2:]
	var out strings.Builder
	for len(args) > 0 {
		n := slices.Index(args, ";")
		if n < 0 {
			n = len(args)
		}
		command := args[:n]
		if n == len(args) {
			args = nil
		} else {
			args = args[n+1:]
		}
		if len(command) == 0 {
			continue
		}
		format := command[len(command)-1]
		rendered := strings.NewReplacer(
			"#{socket_path}", r.socket, "#{pid}", "4242",
			"#{session_id}", "$1", "#{session_name}", r.session,
			"#{window_id}", "@1", "#{pane_id}", "%1",
			"#{"+tmuxopts.ProjectUIDSession+"}", r.project,
			"#{"+tmuxopts.ProjectPathSession+"}", r.root,
			"#{window_name}", "main",
			"#{"+tmuxopts.WindowUID+"}", r.window,
			"#{"+tmuxopts.PaneUID+"}", r.shell,
		).Replace(format)
		switch command[0] {
		case "show-options":
			switch format {
			case tmuxopts.AppGlobal:
				rendered = "1"
			case "@projmux_socket_name":
				rendered = "projmux"
			case "default-shell":
				rendered = "/bin/sh"
			case "default-command":
				rendered = ""
			case tmuxopts.ProjectUIDSession:
				rendered = r.project
			default:
				return nil, errors.New("fixture refused option " + format)
			}
		case "display-message", "list-sessions", "list-windows", "list-panes":
		case "has-session", "set-option", "set-window-option":
			continue
		case "show-environment":
			for key, value := range r.environment {
				out.WriteString(key + "=" + value + "\n")
			}
			continue
		case "set-environment":
			if r.environment == nil {
				r.environment = map[string]string{}
			}
			if slices.Contains(command, "-u") {
				delete(r.environment, format)
			} else {
				r.environment[command[len(command)-2]] = format
			}
			continue
		case "if-shell":
			if !strings.Contains(strings.Join(command, " "), "set-environment -u") {
				return nil, errors.New("fixture refused non-cleanup conditional")
			}
			clear(r.environment)
			continue
		case "split-window":
			r.launch = slices.Clone(command)
			return nil, r.cause
		default:
			return nil, errors.New("fixture refused command " + command[0])
		}
		out.WriteString(rendered)
		out.WriteByte('\n')
	}
	return []byte(out.String()), nil
}

func TestSDKRelaunchPreservesReplyOnlyPlanAndActivation(t *testing.T) {
	client, registry, plan, base := relaunchFailureFixture(t)
	agent, _ := registry.Agent(plan.Target.Metadata.UID)
	if agent.Metadata.Annotations == nil {
		agent.Metadata.Annotations = map[string]string{}
	}
	agent.Metadata.Annotations[metadata.AnnotationAgentDialogueReplyOnly] = metadata.DialogueReplyOnlyOn
	// Ordinary launches reconcile guidance; this fixed launch must not.
	agent.Metadata.Annotations[metadata.AnnotationAgentGuidanceDigest] = "old-guidance"
	persist := func() []byte {
		t.Helper()
		data, err := json.Marshal(registry)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.PathFor(client.paths.StateDir), data, 0600); err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := persist()
	out, err := client.relaunchAgent(context.Background(), core.RelaunchAgentRequest{Ref: "uid:" + agent.Metadata.UID, DryRun: true}, base)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Unchanged || out.Restart || len(out.RelaunchReasons) != 0 {
		t.Fatalf("fixed reply-only settings acquired ordinary prompt changes: %+v", out)
	}
	if after, err := os.ReadFile(store.PathFor(client.paths.StateDir)); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("dry run changed Registry")
	}
	if _, err := store.DefaultMutator().ReleaseAgentPane(&registry, agent.Metadata.UID, metadata.AgentExitNormal, ""); err != nil {
		t.Fatal(err)
	}
	persist()
	runner := &replyOnlyRelaunchRunner{socket: base.socket, project: base.project, window: base.window, shell: base.shell, session: registry.Projects[0].Status.Session.Name, root: registry.Projects[0].Spec.Root, cause: errors.New("fixture split barrier")}
	out, err = client.relaunchAgent(context.Background(), core.RelaunchAgentRequest{Ref: "uid:" + plan.Target.Metadata.UID, Confirm: true}, runner)
	if !errors.Is(err, runner.cause) {
		t.Fatalf("execution did not reach split barrier: %v; cause=%v; calls=%q", err, errors.Unwrap(err), runner.calls)
	}
	if out.Outcome != "resume-failed" {
		t.Fatalf("unexpected probe outcome %q", out.Outcome)
	}
	if !slices.Contains(runner.launch, "--dialogue-reply-only") || !strings.Contains(strings.Join(runner.launch, " "), "claude-dialogue-exec") {
		t.Fatalf("execution lost reply-only launcher or activation flag: %q", runner.launch)
	}
	if slices.Contains(runner.launch, "--append-system-prompt-file") {
		t.Fatal("ordinary guidance leaked into fixed reply-only launch")
	}
}
