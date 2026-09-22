package app

import (
	"context"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// appRouteFixture answers the reads both invocation policies issue against one
// app-owned server reached through an inherited TMUX receipt.
func appRouteFixture(path, pid string, markers ...string) *recordingTmuxRunner {
	appMarker, logical := "1", defaultAppSocket
	if len(markers) > 0 {
		appMarker = markers[0]
	}
	if len(markers) > 1 {
		logical = markers[1]
	}
	return &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-S", path, "display-message", "-p", "-F", "#{socket_path}"):             path + "\n",
		recordedTmuxCallKey("tmux", "-S", path, "show-options", "-gqv", tmuxopts.AppGlobal):                  appMarker + "\n",
		recordedTmuxCallKey("tmux", "-S", path, "show-options", "-gqv", runtimeMutationSocketNameOption):     logical + "\n",
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "display-message", "-p", "-F", "#{socket_path}"): path + "\n",
		recordedTmuxCallKey("tmux", "-S", path, "display-message", "-p", "-F", "#{pid}"):                     pid + "\n",
	}}
}

// TestResumeRoutePolicyDropsTheAnchorRequirement is the route half of this
// Task's acceptance: the same environment that refuses the anchored policy --
// an inherited TMUX_PANE naming a Pane that no longer exists, which is what a
// caller reviving a dead Agent is left holding -- binds under the
// selector-authoritative policy `agent resume` now selects, and the refusal the
// anchored policy gives names absence rather than drift.
func TestResumeRoutePolicyDropsTheAnchorRequirement(t *testing.T) {
	t.Parallel()
	const (
		path = "/tmp/projmux-route/app.sock"
		pid  = "4242"
		dead = "%999"
	)
	env := func(key string) string {
		switch key {
		case "TMUX":
			return path + "," + pid + ",0"
		case "TMUX_PANE":
			return dead
		default:
			return ""
		}
	}
	deadRow := recordedTmuxCallKey("tmux", "-S", path, "display-message", "-p", "-t", dead, "-F", anchorRowFormat())

	anchored := appRouteFixture(path, pid)
	// tmux exits 0 for a Pane that is gone and blanks $/@/% only.
	anchored.outputs[deadRow] = anchorRow(path, pid, "", "", "")
	if _, err := resolveInvocationRuntimeMutationRoute(context.Background(), anchored, env); err == nil {
		t.Fatal("anchored policy bound a route on a Pane that no longer exists")
	} else if !strings.Contains(err.Error(), "anchor pane "+dead+" no longer exists") {
		t.Fatalf("anchored refusal = %q, want it to name the absent anchor Pane", err)
	} else if strings.Contains(err.Error(), "containment drifted") {
		t.Fatalf("anchored refusal still calls an absent Pane drift: %q", err)
	}

	exact := appRouteFixture(path, pid)
	exact.outputs[deadRow] = anchorRow(path, pid, "", "", "")
	route, err := resolveExactObjectRuntimeMutationRoute(context.Background(), exact, env)
	if err != nil {
		t.Fatalf("exact-object policy refused a live app server over a dead ambient anchor: %v", err)
	}
	if route.authority == nil || route.authority.Class != runtimeMutationRouteApp || route.authority.ServerPID != pid {
		t.Fatalf("exact-object route authority = %#v", route.authority)
	}
	if route.authority.PaneID != "" {
		t.Fatalf("exact-object route adopted an ambient anchor Pane %q", route.authority.PaneID)
	}
	for _, call := range exact.calls {
		if len(call.args) > 4 && call.args[len(call.args)-4] == "-t" {
			t.Fatalf("exact-object route still reobserved an anchor Pane: %#v", call.args)
		}
	}
}

// TestResumeRoutePolicyKeepsStandaloneReceiptRequired is the change-freeze
// boundary in the other direction. The policy `agent resume` selects grants
// PID-only authority to an *app-owned* server only. A standalone server -- both
// ownership markers blank -- still has to prove the inherited $/@/% receipt,
// and an absent Pane there is still a refusal, now by its own name.
func TestResumeRoutePolicyKeepsStandaloneReceiptRequired(t *testing.T) {
	t.Parallel()
	const (
		path = "/tmp/projmux-route/standalone.sock"
		pid  = "7171"
		dead = "%999"
	)
	env := func(key string) string {
		switch key {
		case "TMUX":
			return path + "," + pid + ",0"
		case "TMUX_PANE":
			return dead
		default:
			return ""
		}
	}
	runner := appRouteFixture(path, pid, "", "")
	runner.outputs[recordedTmuxCallKey("tmux", "-S", path, "display-message", "-p", "-t", dead, "-F", anchorRowFormat())] =
		anchorRow(path, pid, "", "", "")
	_, err := resolveExactObjectRuntimeMutationRoute(context.Background(), runner, env)
	if err == nil {
		t.Fatal("exact-object policy granted standalone authority without a receipt")
	}
	if !strings.Contains(err.Error(), "anchor pane "+dead+" no longer exists") {
		t.Fatalf("standalone refusal = %q, want it to name the absent anchor Pane", err)
	}
}

// TestAgentResumeSelectsTheSelectorAuthoritativeRouteBinder pins which binder
// the resume verb actually runs. The route policies above are only reachable
// from `agent resume` if the rebind selects the explicit one, and nothing else
// in the resume path says so out loud.
func TestAgentResumeSelectsTheSelectorAuthoritativeRouteBinder(t *testing.T) {
	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
	tmux := newFakeTmux()
	command, launcher, _, _ := newTestAgentResumeCommand(t, store, tmux)
	enablePinnedNativeResumeFixture(t, command, store, "agt-beta-codex", launcher)

	rebinder := command.rebind
	bound := ""
	rebinder.create.bindRuntime = func(context.Context) error { bound = "anchored"; return nil }
	rebinder.create.bindExplicitRuntime = func(context.Context) error { bound = "explicit"; return nil }

	if _, _, err := runRoute(t, command, "resume", "uid:agt-beta-codex"); err != nil {
		t.Fatal(err)
	}
	if bound != "explicit" {
		t.Fatalf("resume bound the %q route binder, want the selector-authoritative one", bound)
	}
	if !rebinder.create.explicitRuntimeAuthority {
		t.Fatal("resume left the create command on natural anchor authority")
	}
}
