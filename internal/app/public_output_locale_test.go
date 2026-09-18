package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/i18n"
)

// TestPublicRouteOutputIsLocaleIndependent is the D5 en-US boundary, stated as
// a byte comparison: representative public routes produce identical stdout,
// stderr, and error text under PROJMUX_LOCALE=ko-KR and under the default
// locale. Interactive pickers and tmux surfaces keep translating; the test
// proves that too, so the comparison cannot pass because ko-KR never resolved.
//
// It is not parallel. Some of the surfaces it covers used to read the process
// environment rather than an injected lookup, so both legs set the process
// environment as well as the injected one, under an isolated HOME.
func TestPublicRouteOutputIsLocaleIndependent(t *testing.T) {
	outputs := map[string]string{}
	for _, locale := range []string{"", "ko-KR"} {
		name := locale
		if name == "" {
			name = "default"
		}
		t.Run(name, func(t *testing.T) {
			outputs[name] = capturePublicRouteOutputs(t, locale)
		})
	}
	if outputs["default"] == "" || outputs["default"] != outputs["ko-KR"] {
		t.Fatalf("public route output depends on the locale\n--- default ---\n%s\n--- ko-KR ---\n%s", outputs["default"], outputs["ko-KR"])
	}
}

func capturePublicRouteOutputs(t *testing.T, locale string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv(i18n.LocaleEnvName, locale)
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		t.Setenv(name, "")
	}
	homeDir := func() (string, error) { return home, nil }
	lookupEnv := os.Getenv

	// Non-vacuity: the ko-KR leg really resolves ko-KR, and the TUI surfaces
	// that sit next to each fixed output still translate.
	if locale != "" {
		if got := appLocale(homeDir, lookupEnv); got != i18n.Locale(locale) {
			t.Fatalf("appLocale = %q, want %q; the comparison would be vacuous", got, locale)
		}
		if got := (&agentCommand{}).agentActionPickerText(agentActionInterruptTurn); got == agentActionInterruptTurn {
			t.Fatalf("approval picker text = %q, want the ko-KR translation", got)
		}
		counts := diagnostics.TopologyCounts{Resumed: 1, Skipped: 2}
		if topologyRecoverySummary(settingsLocale(), diagnostics.LifecycleSuccess, counts) == topologyRecoverySummary(i18n.FallbackLocale, diagnostics.LifecycleSuccess, counts) {
			t.Fatal("the startup-screen recovery summary no longer translates under ko-KR")
		}
	}

	var out strings.Builder
	record := func(name, stdout, stderr string, err error) {
		fmt.Fprintf(&out, "== %s\nstdout=%q\nstderr=%q\nerr=%v\n", name, stdout, stderr, err)
	}

	// `agent turn interrupt` and `agent turn steer`: agentActionText output.
	for _, turn := range [][]string{
		{"turn", "interrupt", "uid:agt-alpha-codex"},
		{"turn", "steer", "uid:agt-alpha-codex", "--", "exact steer"},
	} {
		cmd, _, _ := exactControlCLICommand(t)
		cmd.controlCall = func(_ context.Context, _ string, _ coremetadata.CodexEndpointRef, _ codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
			response := agentControlResponse{OK: true, ThreadID: "thread-1", TurnID: "turn-1"}
			if request.Operation == agentControlOpSteer {
				response.Acceptance, response.Delivery = agentControlAcceptanceProvider, agentControlDeliveryUnconfirmed
			}
			return response, nil
		}
		stdout, stderr, err := runRoute(t, cmd, turn...)
		record("agent "+strings.Join(turn, " "), stdout, stderr, err)
	}
	focusErr := (&agentCommand{}).focusExactCodex(exactAgentControlBinding{}, &bytes.Buffer{}, &bytes.Buffer{})
	record("agent approval review: Open Codex without a focus route", "", "", focusErr)

	// `config providers`: the list and a mutation.
	config := &configCommand{homeDir: homeDir, lookupEnv: lookupEnv}
	for _, args := range [][]string{{"providers"}, {"providers", "--disable", "codex"}, {"providers"}, {"providers", "--enable", "nope"}} {
		stdout, stderr, err := runRoute(t, config, args...)
		record("config "+strings.Join(args, " "), stdout, stderr, err)
	}

	// The disabled-provider refusal the launch gate returns to `create agent`,
	// `create window --provider`, and `agent resume`.
	ai := testAICommand(home)
	ai.homeDir, ai.lookupEnv = homeDir, lookupEnv
	record("create agent --provider codex: refusal", "", "", ai.RequireAgentEnabled("codex"))
	for _, path := range []aiSplitLaunchPath{aiSplitLaunchDefault, aiSplitLaunchPicker, aiSplitLaunchCanonical, "direct"} {
		message, _ := ai.aiAgentDisabledLaunchMessage("codex", path)
		record("refusal variant "+string(path), "", message, nil)
	}

	// `resources`: the non-interactive usage paths.
	for _, args := range [][]string{{"help"}, {"extra"}} {
		resources := &resourceCommand{homeDir: homeDir, lookupEnv: lookupEnv}
		stdout, stderr, err := runRoute(t, resources, args...)
		record("resources "+strings.Join(args, " "), stdout, stderr, err)
	}

	// `get notifications` forwards to this handler: table and --json.
	now := time.Date(2026, time.May, 6, 12, 0, 0, 0, time.UTC)
	for _, args := range [][]string{{"list"}, {"list", "--json"}} {
		store := &stubNotifyStore{listEntries: []notify.Notification{{
			ID: "abc", Text: "deploy ok", Severity: "warn", Source: "ai",
			Session: "main", Window: "1", Pane: "0",
			CreatedAt: now.Add(-90 * time.Second), ExpiresAt: now.Add(time.Hour),
		}}}
		notifyCmd := newCmd(store)
		notifyCmd.homeDir, notifyCmd.lookupEnv = homeDir, lookupEnv
		stdout, stderr, err := runRoute(t, notifyCmd, args...)
		record("get notifications via notify "+strings.Join(args, " "), stdout, stderr, err)
	}

	// `reconcile resources --materialize-project` writes the recovery totals
	// to its own stderr.
	var recovery bytes.Buffer
	reportTopologyRecovery(&recovery, diagnostics.LifecycleSuccess, diagnostics.TopologyCounts{Resumed: 1, Skipped: 2})
	record("reconcile resources --materialize-project: recovery totals", "", recovery.String(), nil)

	return out.String()
}
