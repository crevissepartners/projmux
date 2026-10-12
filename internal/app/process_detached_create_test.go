package app

import (
	"bytes"
	"context"
	"errors"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestDetachedCreateRefusesUnsupportedHostBeforeWrites(t *testing.T) {
	for _, args := range [][]string{{"--detach"}, {"--detach", "--host", "tmux"}} {
		var stderr bytes.Buffer
		_, err := parseResourceCreateFlags("create agent", args, &stderr, resourceCreateShape{split: true, provider: true, host: true})
		if err == nil {
			t.Fatalf("unsupported flags accepted: %v", args)
		}
	}
	var stderr bytes.Buffer
	if _, err := parseResourceCreateFlags("create window", []string{"--host", "process", "--detach"}, &stderr, resourceCreateShape{initialProvider: true, host: true}); err == nil {
		t.Fatal("direct window detach accepted")
	}
	flags, err := parseResourceCreateFlags("create agent", []string{"--host", "process", "--detach", "--project", "alpha", "--window", "beta", "--create-window"}, &stderr, resourceCreateShape{split: true, provider: true, host: true})
	if err != nil || !flags.detach || !flags.createWindow {
		t.Fatalf("process create-window refused: %+v %v", flags, err)
	}
}

func TestDetachedOwnerLogBoundAndPermissions(t *testing.T) {
	root := t.TempDir()
	log, err := newProcessDetachedLog(filepath.Join(root, "registry", "registry.json"), processhost.Binding{Agent: "agent-probe", Generation: "gen-probe"})
	if err != nil {
		t.Fatal(err)
	}
	defer log.file.Close()
	for range 5000 {
		payload := bytes.Repeat([]byte("x"), processDetachedLogLimit/3)
		n, err := log.Write(payload)
		if err != nil || n != len(payload) {
			t.Fatalf("log: %d %v", n, err)
		}
	}
	payload := bytes.Repeat([]byte("y"), processDetachedLogLimit*2)
	if n, err := log.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("oversized log: %d %v", n, err)
	}
	info, err := log.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > processDetachedLogLimit || info.Mode().Perm() != 0600 {
		t.Fatalf("log not private and bounded: %v", info)
	}
	raw, err := os.ReadFile(log.file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || bytes.Contains(raw, []byte("yyyy")) || bytes.Contains(raw, []byte("xxxx")) {
		t.Fatal("diagnostic log leaked free-form content or lost all lifecycle markers")
	}
}

func TestDetachedIdentityHandshakeTimeoutHasNoReservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "silent-launcher")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 20\n"), 0700); err != nil {
		t.Fatal(err)
	}
	command := &createCommand{store: &resourceStore{snapshot: func() (coremetadata.Registry, error) {
		t.Fatal("startup timeout reached Registry")
		return coremetadata.Registry{}, nil
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var output bytes.Buffer
	err := launchProcessDetached(ctx, path, processDetachedRequest{}, command, &output, &output)
	if err == nil || !strings.Contains(err.Error(), "identity-handshake-failed") || !errors.Is(err, context.DeadlineExceeded) || output.Len() != 0 {
		t.Fatalf("silent startup: %v %s", err, output.String())
	}
}

func TestDetachedStopRefusesTmuxBeforeControl(t *testing.T) {
	_, reg, _ := processCreatorFixture(t)
	var selected *coremetadata.Agent
	for i := range reg.Agents {
		if reg.Agents[i].Status.PaneRef != "" {
			selected = &reg.Agents[i]
			break
		}
	}
	if selected == nil {
		t.Fatal("fixture Agent absent")
	}
	pane, _ := reg.Pane(selected.Status.PaneRef)
	pane.Spec.Runtime.Kind = coremetadata.RuntimeTmux
	before := reg.Clone()
	command := &agentCommand{loadRegistry: func() (coremetadata.Registry, error) { return reg, nil }}
	var stdout, stderr bytes.Buffer
	err := command.runStop([]string{"uid:" + selected.Metadata.UID}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "tmux Agents are unsupported") || stdout.Len() != 0 || !reflect.DeepEqual(before, reg) {
		t.Fatalf("tmux stop reached effects: %v %s", err, stdout.String())
	}
}

func TestDetachedStopRejectsChangedGeneration(t *testing.T) {
	_, reg, _ := processCreatorFixture(t)
	var pane *coremetadata.Pane
	for i := range reg.Panes {
		if reg.Panes[i].Status.Activation.Process != nil {
			pane = &reg.Panes[i]
			break
		}
	}
	if pane == nil {
		t.Fatal("fixture process Pane absent")
	}
	agent, _ := reg.Agent(pane.Metadata.OwnerUID())
	binding := pane.Status.ProcessSession.Binding
	code := 0
	receipt := &coremetadata.TerminationEvidence{Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.TerminationNormal, ObservedAt: time.Now(), AgentUID: agent.Metadata.UID, PaneUID: pane.Metadata.UID, Generation: binding.Generation, OperationID: binding.OperationID, ExitCode: &code}
	agent.Status.Phase = coremetadata.PhaseOffline
	agent.Status.LastTermination = receipt
	pane.Status.LastTermination = receipt
	latest := reg.Clone()
	replacement, _ := latest.Pane(pane.Metadata.UID)
	replacement.Status.ProcessSession.Binding.Generation = "gen-replacement"
	before := latest.Clone()
	command := &agentCommand{loadRegistry: func() (coremetadata.Registry, error) { return latest, nil }, processIdentity: func(int) (coremetadata.ProcessIdentity, int, error) {
		t.Fatal("stale stop inspected runtime")
		return coremetadata.ProcessIdentity{}, 0, nil
	}}
	err := command.stopExactProcessAgent(context.Background(), reg, agent.Clone(), pane.Clone())
	if !errors.Is(err, processhost.ErrStale) || !reflect.DeepEqual(latest, before) {
		t.Fatalf("stale stop borrowed another generation's Wait: %v", err)
	}
}
