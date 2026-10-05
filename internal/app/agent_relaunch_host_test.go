package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestRelaunchHostGrammar(t *testing.T) {
	for _, host := range []string{"tmux", "process"} {
		request, err := parseAgentRelaunchArgs([]string{"uid:agent-test", "--host", host, "--", "first input"}, io.Discard)
		if err != nil || request.host != host || !reflect.DeepEqual(request.prompt, []string{"first input"}) {
			t.Fatalf("%+v %v", request, err)
		}
	}
	for _, host := range []string{"", "headless", "other"} {
		if _, err := parseAgentRelaunchArgs([]string{"uid:agent-test", "--host", host}, io.Discard); err == nil {
			t.Fatalf("accepted host %q", host)
		}
	}
}

func TestClaudeHostRelaunchEarlyRefusalsWriteNothing(t *testing.T) {
	for _, tc := range []struct {
		name, host, prompt, want string
		reply                    bool
	}{
		{"missing prompt", "process", "", "requires -- <prompt>", false},
		{"blank prompt", "process", " ", "requires -- <prompt>", false},
		{"reply-only", "process", "continue", replyOnlyReasonLaunchFixed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRelaunchFixture(t)
			if tc.reply {
				a, _ := f.store.registry.Agent(personaAttachAgent)
				a.Metadata.Annotations[coremetadata.AnnotationAgentDialogueReplyOnly] = coremetadata.DialogueReplyOnlyOn
			}
			before := f.store.registry.Clone()
			args := []string{"relaunch", "uid:" + personaAttachAgent, "--host", tc.host, "--dry-run"}
			if tc.prompt != "" {
				args = append(args, "--", tc.prompt)
			}
			stdout, _, err := runRoute(t, f.command, args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) || stdout != "" || f.store.writes != 0 || f.store.transactions != 0 || !reflect.DeepEqual(before, f.store.registry) {
				t.Fatalf("err=%v output=%q writes=%d", err, stdout, f.store.writes)
			}
		})
	}
}

func hostCLIOutput(t *testing.T, f processCreateCLI, args ...string) []byte {
	t.Helper()
	if _, err := os.Stat(os.Getenv("TMUX_TMPDIR")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(f.binary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("CLI %q: %v %s", args, err, stderr.String())
	}
	return out
}

func TestClaudeHostMoveActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	f, agent, oldUID, socket := hostMoveCLIFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	ref := "uid:" + agent.Metadata.UID
	before, _ := f.store.LoadReadOnly()
	var preview agentRelaunchResult
	if err := json.Unmarshal(hostCLIOutput(t, f, "agent", "relaunch", ref, "--host", "process", "--socket-path", socket, "--dry-run", "-o", "json", "--", "continue"), &preview); err != nil {
		t.Fatal(err)
	}
	afterPreview, _ := f.store.LoadReadOnly()
	if preview.CurrentHost != "tmux" || preview.TargetHost != "process" || !slices.Contains(preview.RelaunchReasons, "host-changed") || !reflect.DeepEqual(before, afterPreview) {
		t.Fatalf("preview=%+v writes=%t", preview, !reflect.DeepEqual(before, afterPreview))
	}
	owner, moved := startProcessRelaunchCLI(t, ctx, f, ref, "--host", "process", "--socket-path", socket, "--yes", "--", "continue")
	if moved.NewPaneUID == oldUID || moved.AgentUID != agent.Metadata.UID || moved.CurrentHost != "tmux" || moved.TargetHost != "process" {
		t.Fatalf("move=%+v", moved)
	}
	record := awaitProcessResumeRecord(t, ctx, f, ref, func(r *coremetadata.ProcessSessionRecord) bool {
		return r.SessionID == "process-session" && r.TurnID == ""
	})
	reg, _ := f.store.LoadReadOnly()
	if _, ok := reg.Pane(oldUID); ok {
		t.Fatal("old tmux Pane retained")
	}
	currentPane, _ := reg.Pane(record.Binding.PaneUID)
	hostSocket := processClaudeHostSocket(f.store.Path(), record.Binding.PaneUID, record.Binding.Generation)
	socketIdentity, err := localipc.InspectOwnedSocket(hostSocket)
	if err != nil {
		t.Fatal(err)
	}
	staleBinding := processSchemaBinding(record.Binding)
	staleBinding.Generation = "retired-tmux-generation"
	wireBefore, _ := os.ReadFile(f.trace)
	stale, err := callProcessForeground(ctx, hostSocket, socketIdentity, currentPane.Status.Activation.Process.HostProcess, claudeProcessCheck{Foreground: &processForegroundRequest{Authority: processhost.Authority{Binding: staleBinding, Connection: record.ConnectionID, Session: record.SessionID}, Action: "turn", Operation: "stale-transfer-input", Prompt: "must-not-write"}})
	wireAfter, _ := os.ReadFile(f.trace)
	if (err == nil && stale.Accepted) || !bytes.Equal(wireBefore, wireAfter) {
		t.Fatal("retired generation wrote provider wire")
	}
	var unchanged agentRelaunchResult
	if err := json.Unmarshal(hostCLIOutput(t, f, "agent", "relaunch", ref, "--dry-run", "-o", "json"), &unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged.CurrentHost != "process" || unchanged.TargetHost != "process" || slices.Contains(unchanged.RelaunchReasons, "host-changed") {
		t.Fatalf("omitted host changed target: %+v", unchanged)
	}
	var back agentRelaunchResult
	if err := json.Unmarshal(hostCLIOutput(t, f, "agent", "relaunch", ref, "--host", "tmux", "--yes", "-o", "json"), &back); err != nil {
		t.Fatal(err)
	}
	if back.AgentUID != agent.Metadata.UID || back.NewPaneUID == record.Binding.PaneUID || back.CurrentHost != "process" || back.TargetHost != "tmux" {
		t.Fatalf("back=%+v", back)
	}
	_ = owner.input.Close()
	if err := owner.cmd.Wait(); err != nil && !strings.Contains(owner.stderr.String(), "process control closed") {
		t.Fatalf("retired foreground owner: %v %s", err, owner.stderr.String())
	}
	owner.done = true
	reg, _ = f.store.LoadReadOnly()
	current, ok := reg.Agent(agent.Metadata.UID)
	if !ok || current.Status.SessionRef.ConversationID() != "process-session" {
		t.Fatal("conversation changed")
	}
	if _, ok := reg.Pane(record.Binding.PaneUID); ok {
		t.Fatal("old process Pane retained")
	}
}

func hostMoveCLIFixture(t *testing.T) (processCreateCLI, coremetadata.Agent, string, string) {
	t.Helper()
	f := processResumeCLIFixture(t, aiModeClaude)
	tmuxDir := filepath.Join(f.root, "tmux")
	if err := os.MkdirAll(tmuxDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", tmuxDir)
	socket := filepath.Join(tmuxDir, fmt.Sprintf("tmux-%d", os.Getuid()), "projmux")
	t.Cleanup(func() {
		if _, err := os.Stat(tmuxDir); err == nil {
			cmd := exec.Command("tmux", "-S", socket, "display-message", "-p", "#{socket_path}")
			if out, err := cmd.Output(); err == nil && strings.TrimSpace(string(out)) == socket {
				_ = exec.Command("tmux", "-S", socket, "kill-server").Run()
			}
		}
	})
	// The interactive fixture is a persistent process, while the process branch
	// uses the existing stream-json protocol fixture. No actual provider runs.
	script := "#!/bin/sh\nif [ -n \"$PMX_INTERNAL_CLAUDE_PROCESS_BINDING\" ]; then exec python3 -u " + fmt.Sprintf("%q", filepath.Join(f.root, "provider.py")) + " \"$@\"; fi\nexec sleep 300\n"
	if err := os.WriteFile(filepath.Join(f.root, "claude"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	hostCLIOutput(t, f, "config", "apply")
	hostCLIOutput(t, f, "start", "project", "uid:"+f.project)
	hostCLIOutput(t, f, "create", "agent", "--provider", "claude", "--profile", "none", "--project", "uid:"+f.project, "--window", "uid:"+f.window, "--name", "host-source")
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Agents) != 1 {
		t.Fatalf("agents=%d", len(reg.Agents))
	}
	agent := reg.Agents[0].Clone()
	oldPane, ok := reg.Pane(agent.Status.PaneRef)
	if !ok {
		t.Fatal("source Pane missing")
	}
	oldUID := oldPane.Metadata.UID
	pidOutput, err := exec.Command("tmux", "-S", socket, "display-message", "-p", "-t", oldPane.Status.Activation.RuntimeID, "#{pane_pid}").Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidOutput)))
	if err != nil {
		t.Fatal(err)
	}
	identity, _, err := localipc.Process(pid)
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		mut := intmetadata.DefaultMutator()
		currentPane, _ := reg.Pane(oldUID)
		if currentPane.Status.Activation.Claude == nil {
			if err := mut.RecordClaudeProcess(reg, oldUID, agent.Metadata.UID, currentPane.Status.Activation.Generation, identity); err != nil {
				return err
			}
		}
		_, _, err := mut.RecordAgentSessionRef(reg, agent.Metadata.UID, coremetadata.AgentSessionObservation{Provider: aiModeClaude, SessionID: "process-session"})
		if err != nil {
			return err
		}
		_, err = mut.SetAgentActivation(reg, agent.Metadata.UID, coremetadata.ActivationAcknowledged, "provider-hook", "")
		if err != nil {
			return err
		}
		_, err = mut.SetAgentInteraction(reg, agent.Metadata.UID, coremetadata.InteractionIdle, "provider-hook")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	reg, _ = f.store.LoadReadOnly()
	currentAgent, _ := reg.Agent(agent.Metadata.UID)
	return f, currentAgent.Clone(), oldUID, socket
}

func TestProcessHostRetirementCASRetainsEvidenceOnMismatch(t *testing.T) {
	for _, change := range []string{"recipe", "session", "history", "wait", "phase", "binding", "none"} {
		t.Run(change, func(t *testing.T) {
			reg, agentUID, paneUID := processDeleteFixture(t, aiModeClaude)
			recordFixtureWait(t, reg, paneUID)
			agent, _ := reg.Agent(agentUID)
			pane, _ := reg.Pane(paneUID)
			old := processResumeCandidate{Agent: agent.Clone(), Pane: pane.Clone(), Record: *pane.Status.ProcessSession.Clone()}
			switch change {
			case "recipe":
				agent.Spec.Workspace.CWD = "/changed"
			case "session":
				agent.Status.SessionRef = &coremetadata.AgentSessionRef{Provider: aiModeClaude, Claude: &coremetadata.ClaudeSessionRef{SessionID: "foreign"}}
			case "history":
				pane.Status.ProcessSession.ConnectionID = "foreign"
			case "wait":
				pane.Status.LastTermination = nil
			case "phase":
				agent.Status.Phase = coremetadata.PhasePending
			case "binding":
				pane.Status.ProcessSession.Binding.Generation = "foreign"
			}
			before := reg.Clone()
			err := retireProcessPaneForTmux(reg, intmetadata.DefaultMutator(), old)
			if change == "none" {
				if err != nil {
					t.Fatal(err)
				}
				if _, exists := reg.Pane(paneUID); exists {
					t.Fatal("retired Pane retained")
				}
			} else if err == nil || !reflect.DeepEqual(before, *reg) {
				t.Fatalf("err=%v evidence changed=%t", err, !reflect.DeepEqual(before, *reg))
			}
		})
	}
}

func TestClaudeHostTransferFailedInitRestoresRecipeActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	f, agent, _, socket := hostMoveCLIFixture(t)
	scriptPath := filepath.Join(f.root, "provider.py")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	script = bytes.ReplaceAll(script, []byte("process-session"), []byte("wrong-session"))
	if err = os.WriteFile(scriptPath, script, 0600); err != nil {
		t.Fatal(err)
	}
	ref := "uid:" + agent.Metadata.UID
	cmd := exec.Command(f.binary, "agent", "relaunch", ref, "--host", "process", "--model", "new-model", "--socket-path", socket, "--yes", "--", "continue")
	out, err := cmd.CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("resume refused")) || !bytes.Contains(out, []byte("previous recipe/conversation retained")) {
		t.Fatalf("err=%v output=%s", err, out)
	}
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	after, _ := reg.Agent(agent.Metadata.UID)
	if !sameHostTransferSpec(after.Spec, agent.Spec) || !reflect.DeepEqual(after.Metadata.Annotations, agent.Metadata.Annotations) || !after.Status.SessionRef.SameConversation(agent.Status.SessionRef) || after.Status.Phase != coremetadata.PhaseOffline || after.Status.PaneRef != "" {
		t.Fatalf("old recipe not restored: %+v", after)
	}
	for _, pane := range reg.Panes {
		if pane.Metadata.OwnerUID() == agent.Metadata.UID {
			t.Fatal("failed target Pane retained")
		}
	}
	// The previous host recovery is executable without pretending there is a
	// retired tmux source available for a second transfer.
	hostCLIOutput(t, f, "agent", "relaunch", ref, "--socket-path", socket, "--yes")
}

func TestHostTransferRollbackCASPreservesConcurrentRecipe(t *testing.T) {
	for _, change := range []string{"recipe", "annotations", "session", "pane", "writer", "none"} {
		t.Run(change, func(t *testing.T) {
			reg, agentUID, paneUID := processDeleteFixture(t, aiModeClaude)
			recordFixtureWait(t, reg, paneUID)
			agent, _ := reg.Agent(agentUID)
			pane, _ := reg.Pane(paneUID)
			expected := agent.Clone()
			retired := agent.Clone()
			retired.Status.PaneRef = ""
			binding := processSchemaBinding(pane.Status.ProcessSession.Binding)
			switch change {
			case "recipe":
				agent.Spec.Workspace.CWD = "/new-cwd"
			case "annotations":
				if agent.Metadata.Annotations == nil {
					agent.Metadata.Annotations = map[string]string{}
				}
				agent.Metadata.Annotations[coremetadata.AnnotationAgentModel] = "new-model"
			case "session":
				agent.Status.SessionRef = &coremetadata.AgentSessionRef{Provider: aiModeClaude, Claude: &coremetadata.ClaudeSessionRef{SessionID: "foreign"}}
			case "pane":
				agent.Status.PaneRef = "other"
			case "writer":
				pane.Status.Activation = coremetadata.PaneActivation{Generation: "live"}
			}
			before := reg.Clone()
			err := restoreTmuxTransferReservation(reg, intmetadata.DefaultMutator(), binding, expected, retired)
			if change == "none" {
				if err != nil {
					t.Fatal(err)
				}
				if _, exists := reg.Pane(paneUID); exists {
					t.Fatal("failed target retained")
				}
			} else if !errors.Is(err, processhost.ErrStale) || !reflect.DeepEqual(before, *reg) {
				t.Fatalf("err=%v concurrent state overwritten", err)
			}
		})
	}
}

func TestTmuxTransferRequiresBothMirrorAndProviderRetirement(t *testing.T) {
	identity, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	request := agentRelaunchRequest{socket: deleteSocketFlags{socketPath: "/absolute/private/socket"}}
	command := &agentCommand{lookupEnv: os.Getenv, managedPaneLive: func(tmuxTransport, string) (bool, error) { return false, nil }}
	if err := command.verifyTmuxTransferRetirement("retired-pane", identity, request); err == nil {
		t.Fatal("missing Pane inferred live provider retired")
	}
	command.managedPaneLive = func(tmuxTransport, string) (bool, error) { return false, errors.New("inventory unknown") }
	if err := command.verifyTmuxTransferRetirement("retired-pane", identity, request); err == nil {
		t.Fatal("unknown inventory admitted transfer")
	}
	command.managedPaneLive = func(tmuxTransport, string) (bool, error) { return true, nil }
	if err := command.verifyTmuxTransferRetirement("retired-pane", identity, request); err == nil {
		t.Fatal("live mirror admitted transfer")
	}
}

func TestClaudeHostMoveDeferredClaimActualCLI(t *testing.T) {
	for _, mode := range []string{"closed", "stale"} {
		t.Run(mode, func(t *testing.T) {
			f, source, _, socket := hostMoveCLIFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
			defer cancel()
			ref := "uid:" + source.Metadata.UID
			owner, _ := startProcessRelaunchCLI(t, ctx, f, ref, "--host", "process", "--socket-path", socket, "--yes", "--", "continue")
			owner.shutdown(t)
			old := awaitProcessResumeRecord(t, ctx, f, ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			c := &agentCommand{loadRegistry: f.store.LoadReadOnly, messagePaths: agentMessagePaths{registryPath: f.store.Path()}}
			options := processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: source.Metadata.UID}}
			// Establish a candidate, then acquire a competing claim without any Registry mutation.
			if _, err := c.processResumeCandidate(processAgentResumeRequest{options: options}); err != nil {
				t.Fatal(err)
			}
			claim, err := c.claimDeferredProcessAgent(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = claim.Close() })
			before, _ := f.store.LoadReadOnly()
			inventory, err := exec.Command("tmux", "-S", socket, "list-panes", "-a", "-F", "#{pane_id}").Output()
			if err != nil {
				t.Fatal(err)
			}
			wire, _ := os.ReadFile(f.trace)
			args := []string{"agent", "relaunch", ref, "--host", "tmux", "--yes", "-o", "json"}
			output, err := exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
			if err == nil || !bytes.Contains(output, []byte(processResumeOwned)) {
				t.Fatalf("live claim bypassed: %v %s", err, output)
			}
			after, _ := f.store.LoadReadOnly()
			nextInventory, err := exec.Command("tmux", "-S", socket, "list-panes", "-a", "-F", "#{pane_id}").Output()
			nextWire, _ := os.ReadFile(f.trace)
			currentClaim, readErr := readDeferredClaim(claim.path)
			if err != nil || readErr != nil || currentClaim != claim.record || !reflect.DeepEqual(before, after) || !bytes.Equal(inventory, nextInventory) || !bytes.Equal(wire, nextWire) {
				t.Fatal("refused host move mutated target, provider, or claim")
			}
			if mode == "closed" {
				if err := claim.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				stale := claim.record
				stale.Process.Start += "-different-birth"
				if err := writeDeferredClaim(claim.path, stale); err != nil {
					t.Fatal(err)
				}
			}
			var moved agentRelaunchResult
			if err := json.Unmarshal(hostCLIOutput(t, f, args...), &moved); err != nil {
				t.Fatal(err)
			}
			final, _ := f.store.LoadReadOnly()
			agent, _ := final.Agent(source.Metadata.UID)
			if moved.AgentUID != source.Metadata.UID || moved.NewPaneUID == old.Binding.PaneUID || agent.Status.SessionRef.ConversationID() != old.SessionID {
				t.Fatal("released/stale claim migration lost identity")
			}
			if _, found := final.Pane(old.Binding.PaneUID); found {
				t.Fatal("old process Pane retained")
			}
		})
	}
}

func TestClaudeHostMoveFinalFenceAfterCandidateClaim(t *testing.T) {
	c, options := deferredClaimFixture(t)
	old, err := c.processResumeCandidate(processAgentResumeRequest{options: options})
	if err != nil {
		t.Fatal(err)
	}
	// The claim lands after the caller captured its retired candidate. No Registry value changes.
	claim, err := c.claimDeferredProcessAgent(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = claim.Close() })
	before, _ := c.loadRegistry()
	plan := agentResumePlan{agentUID: old.Agent.Metadata.UID, retiredProcess: &old}
	err = c.rebindRetiredProcessToTmux(plan, io.Discard, io.Discard)
	if !errors.Is(err, processhost.ErrResumeRefused) || !strings.HasPrefix(err.Error(), processResumeOwned+":") {
		t.Fatal("final migration fence bypassed competing claim", err)
	}
	after, _ := c.loadRegistry()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("final fence changed source")
	}
	// Both refusal and rebind-error paths must release the sidecar lock.
	if err := claim.Close(); err != nil {
		t.Fatal("refusal retained sidecar lock", err)
	}
	err = c.rebindRetiredProcessToTmux(plan, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "materialization seam is not configured") {
		t.Fatal("closed claim did not reach rebind", err)
	}
	unlock, err := lockDeferredClaim(claim.path)
	if err != nil {
		t.Fatal("rebind error retained sidecar lock", err)
	}
	unlock()
}
