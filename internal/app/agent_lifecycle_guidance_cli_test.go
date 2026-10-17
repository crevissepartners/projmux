package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// These use only copied CLI binaries and protocol fixtures in the fixture HOME.
// Session/Wait predicates are the existing readiness barriers, never sleeps.
func TestAgentLifecycleGuidanceActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			if provider == aiModeCodex {
				// This scenario changes the thread model. Use the same settings
				// protocol as processCodexRelaunchFixture in this isolated fixture.
				path := filepath.Join(f.root, "codex-provider.py")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Count(string(raw), "turn=0") != 1 || strings.Count(string(raw), "elif method in ('thread/start','thread/resume'):") != 1 {
					t.Fatal("settings protocol fixture anchors changed")
				}
				source := strings.Replace(string(raw), "turn=0", "model='stub-model'\neffort='low'\nsandbox={'type':'readOnly'}\napproval='on-request'\nturn=0", 1)
				source = strings.Replace(source, "elif method in ('thread/start','thread/resume'):", `elif method=='thread/resume':
  reply({'thread':{'id':'process-thread'},'model':model,'reasoningEffort':effort,'sandbox':sandbox,'approvalPolicy':approval})
 elif method=='thread/settings/update':
  p=n['params'];model=p.get('model',model);effort=p.get('effort',effort);sandbox=p.get('sandboxPolicy',sandbox);approval=p.get('approvalPolicy',approval);reply({})
 elif method=='thread/start':`, 1)
				if err = os.WriteFile(path, []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "first dialogue"))
			awaitDetachedRelaunchInitialConversation(t, ctx, f, first.ref)
			first.shutdown(t)
			old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			paths, err := config.DefaultPathsFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			if err = config.SaveAgentDefaultHostFile(paths.AgentDefaultHostFile(), config.AgentHostProcess); err != nil {
				t.Fatal(err)
			}
			// Resume and relaunch keep the same conversation and exact input. Closing
			// this invocation's stdin still waits for and retires its actual child.
			for _, verb := range []string{"resume", "relaunch"} {
				args := []string{"agent", verb, first.ref, "-o", "json"}
				if verb == "relaunch" {
					args = append(args, "--yes", "--model", "changed-model")
				}
				args = append(args, "--", "keep  exact spaces", "--effort=fixture-private-canary")
				cmd := exec.CommandContext(ctx, f.binary, args...)
				input, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				output, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				decoder := json.NewDecoder(output)
				var rawOutput json.RawMessage
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				if err = cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = input.Close()
					if cmd.ProcessState == nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
					}
				})
				// Successful result output is committed only after preparation.
				// A reservation already has the old conversation and a new
				// connection, so it is deliberately not the EOF barrier.
				if err = decoder.Decode(&rawOutput); err != nil {
					_ = input.Close()
					_ = cmd.Wait()
					t.Fatalf("%s success output: %v %s", verb, err, &stderr)
				}
				current := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
					return r.Binding.Generation != old.Binding.Generation
				})
				_ = input.Close()
				var extra json.RawMessage
				if extraErr := decoder.Decode(&extra); extraErr != io.EOF {
					_ = cmd.Wait()
					t.Fatalf("extra JSON stdout: %v %s", extraErr, extra)
				}
				if err = cmd.Wait(); err != nil {
					t.Fatalf("%s Wait: %v %s", verb, err, &stderr)
				}
				if current.Binding.AgentUID != old.Binding.AgentUID || current.Binding.PaneUID != old.Binding.PaneUID || processRelaunchConversation(&current) != processRelaunchConversation(&old) {
					t.Fatal("identity/conversation changed")
				}
				var wire struct {
					Items               []json.RawMessage         `json:"items"`
					SettingsApplication *agentSettingsApplication `json:"settingsApplication"`
				}
				if err = json.Unmarshal(rawOutput, &wire); err != nil || wire.SettingsApplication == nil {
					t.Fatalf("%s JSON: %v %s", verb, err, rawOutput)
				}
				if verb == "resume" && len(wire.Items) != 1 {
					t.Fatal("lost AgentList Items")
				}
				for _, text := range []string{"--effort is part of the prompt", "runs in the foreground", "--detach"} {
					if !strings.Contains(stderr.String(), text) {
						t.Fatalf("%s missing %s: %s", verb, text, &stderr)
					}
				}
				if bytes.Contains(rawOutput, []byte("fixture-private-canary")) || strings.Contains(stderr.String(), "fixture-private-canary") {
					t.Fatal("prompt value leaked into result/notice")
				}
				model := "stub-model"
				if verb == "relaunch" {
					model = "changed-model"
				}
				e := lifecycleEntry(t, wire.SettingsApplication, "model")
				if e.Status != "delivered" || e.Value == nil || *e.Value != model {
					t.Fatal(e)
				}
				raw, _ := os.ReadFile(filepath.Join(f.root, "wire.jsonl"))
				if !bytes.Contains(raw, []byte("keep  exact spaces --effort=fixture-private-canary")) {
					t.Fatal("provider input changed", string(raw))
				}
				old = awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			}
			// none adds no lifecycle guidance, including prompt flag warnings.
			// Existing owner-stop diagnostics retain their original policy.
			cmd := exec.CommandContext(ctx, f.binary, "agent", "resume", first.ref, "-o", "none", "--", "exact", "--effort")
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			var output, diagnostics bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &diagnostics
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			var noneBinding coremetadata.ProcessBinding
			processCLIUntil(t, ctx, func() bool {
				reg, err := f.store.LoadReadOnly()
				if err != nil {
					return false
				}
				pane, ambiguous := processResumePane(reg, strings.TrimPrefix(first.ref, "uid:"))
				if ambiguous || pane == nil || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding.Generation == old.Binding.Generation {
					return false
				}
				view, ok := observeProcessHostOwner(reg, f.store.Path(), *pane)
				ready := ok && view.State == "ready" && view.Binding.Generation == pane.Status.ProcessSession.Binding.Generation && view.Child.Valid()
				if ready {
					noneBinding = pane.Status.ProcessSession.Binding
				}
				return ready
			})
			_ = input.Close()
			if err = cmd.Wait(); err != nil || output.Len() != 0 {
				t.Fatalf("none: %v stdout=%s stderr=%s", err, &output, &diagnostics)
			}
			wantDiagnostic := fmt.Sprintf("agent owner stop: reason=stdin-eof agent=%s pane=uid:%s generation=%s owner_pid=%d owner_ppid=%d parent_comm=%s\n", first.ref, noneBinding.PaneUID, noneBinding.Generation, cmd.Process.Pid, os.Getpid(), processOwnerParentComm(os.Getpid()))
			if text := diagnostics.String(); text != "" && text != wantDiagnostic {
				t.Fatalf("none added guidance or changed the existing owner diagnostic: %s", text)
			}
		})
	}
}

func TestAgentLifecycleGuidanceFrozenActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	f := deferredResumeCLIFixture(t, aiModeClaude)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", "claude", "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
	awaitDetachedRelaunchInitialConversation(t, ctx, f, first.ref)
	first.shutdown(t)
	cleanupDetachedResume(t, f, first.ref)
	cmd := exec.CommandContext(ctx, f.binary, "agent", "relaunch", first.ref, "--detach", "--yes", "--model", "frozen-model", "-o", "json")
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v %s", err, &stderr)
	}
	var result agentRelaunchResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.SettingsApplication == nil {
		t.Fatalf("%v %s", err, &out)
	}
	e := lifecycleEntry(t, result.SettingsApplication, "model")
	if e.Status != "pending" || e.Reason != "first-input-required" || *e.Value != "frozen-model" || result.SettingsApplication.NotAppliedCount != 0 {
		t.Fatal(e)
	}
	reg := mustRegistry(t, f)
	pane, _ := processResumePane(reg, strings.TrimPrefix(first.ref, "uid:"))
	if pane == nil || !pane.Status.Activation.IsZero() {
		t.Fatal("frozen recipe invented a provider")
	}
	if strings.Contains(stderr.String(), "runs in the foreground") {
		t.Fatal(stderr.String())
	}
	// Stop releases the exact child-free claim without an invented provider Wait.
	stop := exec.CommandContext(ctx, f.binary, "agent", "stop", first.ref)
	stop.Stdout = io.Discard
	if err := stop.Run(); err != nil {
		t.Fatal(err)
	}
	// A committed child-free recipe explains pending application on the same
	// default result line; no additional notice line or wire field is introduced.
	out.Reset()
	stderr.Reset()
	command := exec.CommandContext(ctx, f.binary, "agent", "relaunch", first.ref, "--detach", "--yes")
	command.Stdout = &out
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("default claim: %v %s", err, &stderr)
	}
	if strings.Count(out.String(), "\n") != 1 || !strings.Contains(out.String(), "waiting for first input; settings are recorded and not yet applied") {
		t.Fatal(out.String())
	}
	stop = exec.CommandContext(ctx, f.binary, "agent", "stop", first.ref)
	if raw, err := stop.CombinedOutput(); err != nil {
		t.Fatalf("claim stop: %v %s", err, raw)
	}
	// Ordinary resume consumes the frozen recipe. Its successful default output
	// must no longer claim that provider application is waiting for first input.
	out.Reset()
	stderr.Reset()
	command = exec.CommandContext(ctx, f.binary, "agent", "resume", first.ref, "--detach", "--", "consume frozen")
	command.Stdout = &out
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("consume frozen: %v %s", err, &stderr)
	}
	if strings.Contains(out.String(), "waiting for first input") || strings.Contains(stderr.String(), "settings are recorded and not yet applied") {
		t.Fatal(out.String(), stderr.String())
	}
	awaitDetachedRelaunchInitialConversation(t, ctx, f, first.ref)
	raw, err := os.ReadFile(filepath.Join(f.root, "argv.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	rows := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	if !bytes.Contains(rows[len(rows)-1], []byte("frozen-model")) {
		t.Fatalf("frozen model not delivered: %s", raw)
	}
	stop = exec.CommandContext(ctx, f.binary, "agent", "stop", first.ref)
	if raw, err := stop.CombinedOutput(); err != nil {
		t.Fatalf("provider stop: %v %s", err, raw)
	}
}

func TestAgentLifecycleGuidanceClaimModesActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	f := deferredResumeCLIFixture(t, aiModeClaude)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", "claude", "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
	awaitDetachedRelaunchInitialConversation(t, ctx, f, first.ref)
	first.shutdown(t)
	old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
	command := &agentCommand{messagePaths: agentMessagePaths{registryPath: f.store.Path()}}
	for _, mode := range []string{"default", "json", "receipt", "metadata", "uid", "none"} {
		args := []string{"agent", "resume", first.ref, "--wait-for-peer"}
		if mode != "default" {
			args = append(args, "-o", mode)
		}
		cmd := exec.CommandContext(ctx, f.binary, args...)
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = input.Close()
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		// A child-free claim is the success fact here, not a reservation mistaken
		// for provider readiness. EOF is admitted only after the real claim exists.
		processCLIUntil(t, ctx, func() bool {
			claim, err := readDeferredClaim(command.deferredClaimPath(old.Binding.AgentUID))
			return err == nil && claim.Agent == old.Binding.AgentUID && claim.Process.PID == cmd.Process.Pid && deferredClaimLive(claim)
		})
		_ = input.Close()
		if err = cmd.Wait(); err != nil {
			t.Fatalf("%s: %v %s", mode, err, &stderr)
		}
		const waiting = "Waiting for first input; the provider has not started."
		if mode == "default" {
			if len(strings.Fields(stdout.String())) != 6 || strings.Count(stdout.String(), "\n") != 1 || !strings.Contains(stdout.String(), "foreground=claimed") {
				t.Fatal("identity line changed", stdout.String())
			}
			if stderr.String() != waiting+"\n" {
				t.Fatal("default lifetime notice", stderr.String())
			}
		} else {
			if stdout.Len() != 0 || strings.Contains(stderr.String(), waiting) {
				t.Fatalf("%s gained pre-input result: %s %s", mode, &stdout, &stderr)
			}
			if mode == "none" && stderr.Len() != 0 {
				t.Fatal("none leaked", stderr.String())
			}
			if mode != "none" && len(strings.Fields(stderr.String())) != 6 {
				t.Fatalf("%s ownership changed: %s", mode, &stderr)
			}
		}
		reg := mustRegistry(t, f)
		pane, _ := processResumePane(reg, old.Binding.AgentUID)
		if pane == nil || !pane.Status.Activation.IsZero() || pane.Status.ProcessSession.Binding != old.Binding {
			t.Fatal("claim fabricated provider or generation")
		}
	}
}

// This observes a copied product CLI with isolated protocol providers, not
// collaboration Agents or real provider accounts.
func TestGuidanceFragmentsSnapshotActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			paths, err := config.DefaultPathsFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(paths.ConfigDir, agentguidance.FragmentsDirName)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			for name, content := range map[string]string{"20-second.md": "SECOND-FRAGMENT\r\n", "10-first.md": "FIRST-FRAGMENT\n"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			store := agentguidance.NewDefaultStore(paths)
			user := []byte("# Decisions and approvals\nUSER-GUIDANCE\n")
			if err := store.Save(user); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			run := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "observe fragments"))
			awaitDetachedRelaunchInitialConversation(t, ctx, f, run.ref)
			reg := mustRegistry(t, f)
			agent, ok := reg.Agent(strings.TrimPrefix(run.ref, "uid:"))
			if !ok {
				t.Fatal("created Agent missing")
			}
			digest := agent.Metadata.Annotations[coremetadata.AnnotationAgentGuidanceDigest]
			path, err := store.RecordedSnapshotPath(digest)
			if err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			suffix := []byte("\n\nFIRST-FRAGMENT\n\n\nSECOND-FRAGMENT\r\n\n\n" + string(user))
			if !bytes.HasPrefix(content, agentguidance.Default()) || !bytes.HasSuffix(content, suffix) || agentguidance.Digest(content) != digest {
				t.Fatal("CLI snapshot did not preserve ordered guidance parts and digest")
			}
			baseEnd := len(agentguidance.Default())
			first := bytes.Index(content, []byte("\n\nFIRST-FRAGMENT"))
			process := bytes.Index(content, []byte("\n# Process host\n\n"))
			if process < baseEnd || process >= first {
				t.Fatal("process context is not between built-in guidance and fragments")
			}
			for _, part := range [][]byte{agentguidance.Default(), []byte("# Process host\n"), []byte("FIRST-FRAGMENT"), []byte("SECOND-FRAGMENT"), user} {
				if bytes.Count(content, part) != 1 {
					t.Fatal("snapshot repeated or omitted a guidance part")
				}
			}
			run.shutdown(t)
			t.Logf("copied CLI %s: built-in → process context → first fragment → second fragment → user; recorded digest matches snapshot", provider)
		})
	}
}
