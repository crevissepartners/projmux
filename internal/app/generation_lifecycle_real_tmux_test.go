package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

func TestGenerationLifecycleProjectionUsesIsolatedRealTmuxAndExactCleanup(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("", "p1tmux-")
	if err != nil {
		t.Fatal(err)
	}
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "TMUX" || key == "TMUX_PANE" || key == "TMUX_TMPDIR" {
			continue
		}
		environment = append(environment, entry)
	}
	environment = append(environment, "TMUX_TMPDIR="+root)
	runner := shellTmuxExecRunner{env: func() []string { return environment }}
	socketCandidate := filepath.Join(root, fmt.Sprintf("phase1-%d-%x.sock", os.Getpid(), uint32(time.Now().UnixNano())))
	out, err := runner.Run(ctx, "tmux", "-S", socketCandidate, "-f", "/dev/null", "new-session", "-d",
		"-s", "phase1-lifecycle", "-P", "-F", "#{pane_id}\t#{socket_path}", "tail", "-f", "/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(strings.TrimSpace(string(out)), "\t")
	if len(fields) != 2 || !strings.HasPrefix(fields[0], "%") {
		t.Fatalf("new-session exact Pane/socket receipt=%q", out)
	}
	paneID := fields[0]
	socketPath := filepath.Clean(fields[1])
	if socketPath != filepath.Clean(socketCandidate) {
		t.Fatalf("new-session reported socket %q, want exact candidate %q", socketPath, socketCandidate)
	}
	rel, relErr := filepath.Rel(root, socketPath)
	if relErr != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("isolated tmux socket %q escaped root %q", socketPath, root)
	}
	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		observed, observeErr := runner.Run(cleanupCtx, "tmux", "-S", socketPath, "display-message", "-p", "-F", "#{socket_path}")
		if observeErr != nil || filepath.Clean(strings.TrimSpace(string(observed))) != socketPath {
			t.Errorf("refuse cleanup without exact socket proof: observed=%q err=%v", observed, observeErr)
			return
		}
		if _, killErr := runner.Run(cleanupCtx, "tmux", "-S", socketPath, "kill-server"); killErr != nil {
			t.Errorf("kill isolated tmux: %v", killErr)
			return
		}
		if _, liveErr := runner.Run(cleanupCtx, "tmux", "-S", socketPath, "has-session"); liveErr == nil {
			t.Errorf("isolated tmux still answers after kill-server")
			return
		}
		if removeErr := os.Remove(socketPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			t.Errorf("remove exact isolated tmux socket: %v", removeErr)
			return
		}
		closed = true
		if _, statErr := os.Stat(socketPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("isolated tmux socket remains after exact cleanup: %v", statErr)
		}
		if removeErr := os.RemoveAll(root); removeErr != nil {
			t.Errorf("remove exact isolated tmux root: %v", removeErr)
		}
	})
	route := explicitTmuxRunner{runner: runner, target: tmuxTransport{Kind: tmuxSocketPath, Value: socketPath, Source: tmuxSocketPathSource}}
	if _, err := route.Run(ctx, "tmux", "set-option", "-p", "-t", paneID, tmuxopts.PaneUID, "pan-alpha-codex"); err != nil {
		t.Fatal(err)
	}
	endpoint := &coremetadata.CodexEndpointRef{StateDomainID: "real-tmux-domain", EndpointGenerationID: "real-tmux-generation"}
	operation := &coremetadata.CodexGenerationOperationRef{ID: "real-tmux-recovery", Endpoint: *endpoint}
	stored := &coremetadata.CodexAuthorityRef{
		StateDomainID: endpoint.StateDomainID, EndpointGenerationID: endpoint.EndpointGenerationID,
		BrokerRuntimeID: "real-broker", ConnectionEpoch: 3, BindingEpoch: 5,
	}
	registry := resourceFixtureRegistry(t)
	agent, _ := registry.Agent("agt-alpha-codex")
	agent.Status.Interaction = coremetadata.AgentInteraction{Kind: coremetadata.InteractionIdle, ObservedAt: resourceFixtureClock}
	agent.Status.SessionRef = &coremetadata.AgentSessionRef{
		Provider: aiModeCodex, ObservedAt: resourceFixtureClock,
		Codex: &coremetadata.CodexSessionRef{
			ThreadID: "real-tmux-thread", Endpoint: endpoint,
			Lifecycle: &coremetadata.CodexGenerationLifecycleRef{State: coremetadata.CodexGenerationRecovering, Operation: operation},
		},
	}
	registryPane, _ := registry.Pane("pan-alpha-codex")
	registryPane.Status.Activation = coremetadata.PaneActivation{
		Generation: "real-materialization", RuntimeID: paneID, AgentUID: agent.Metadata.UID,
		Codex: &coremetadata.CodexActivationBinding{ThreadID: "real-tmux-thread", Authority: stored},
	}
	if err := registry.Validate(); err != nil {
		t.Fatal(err)
	}
	first := newResourcePlanTmuxRunner(route)
	if err := planResourceAgentProjections(ctx, first, registry, resourceFixtureClock); err != nil {
		t.Fatal(err)
	}
	if len(first.writes) != 2 {
		t.Fatalf("real tmux first reconcile writes=%#v, want state and badge", first.writes)
	}
	for _, write := range first.writes {
		if _, err := route.Run(ctx, "tmux", write.args...); err != nil {
			t.Fatalf("execute real tmux write %v: %v", write.args, err)
		}
	}
	second := newResourcePlanTmuxRunner(route)
	if err := planResourceAgentProjections(ctx, second, registry, resourceFixtureClock); err != nil {
		t.Fatal(err)
	}
	if len(second.writes) != 0 {
		t.Fatalf("real tmux second reconcile writes=%#v, want zero", second.writes)
	}
	for field, want := range map[string]string{
		aiPaneStateOption:     codexgeneration.LifecycleStateRecovering,
		aiPaneBadgeKindOption: codexgeneration.LifecycleBadgeRecovering,
		attentionStateOption:  "",
	} {
		observed, err := route.Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F", "#{"+field+"}")
		if err != nil || strings.TrimSpace(string(observed)) != want {
			t.Fatalf("real tmux %s=%q err=%v, want %q", field, observed, err, want)
		}
	}

	foreign := *stored
	foreign.EndpointGenerationID = "foreign-generation"
	registryWrites, tmuxWrites := 0, 0
	decision := codexgeneration.ApplyRuntimeMutation(codexgeneration.RuntimeMutationInput{
		DurableEndpoint: endpoint, StoredAuthority: stored, PresentedAuthority: &foreign,
		TargetRuntimeID: paneID, EventRuntimeID: paneID,
	}, func() { registryWrites++ }, func() {
		tmuxWrites++
		_, _ = route.Run(ctx, "tmux", "set-option", "-p", "-t", paneID, "@projmux_phase1_authority_probe", "mutated")
	})
	if decision.Class.Effect != codexgeneration.MutationZeroWrite || registryWrites != 0 || tmuxWrites != 0 {
		t.Fatalf("foreign authority decision=%#v Registry=%d tmux=%d", decision, registryWrites, tmuxWrites)
	}
	observed, err := route.Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F", "#{@projmux_phase1_authority_probe}")
	if err != nil || strings.TrimSpace(string(observed)) != "" {
		t.Fatalf("foreign authority changed real tmux probe=%q err=%v", observed, err)
	}
}
