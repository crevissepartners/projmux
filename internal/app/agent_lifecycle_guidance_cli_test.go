package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// These use only copied CLI binaries and protocol fixtures in the fixture HOME.
// Session/Wait predicates are the existing readiness barriers, never sleeps.
func TestAgentLifecycleGuidanceActualCLI(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
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
				args = append(args, "--", "keep  exact spaces", "--effort")
				cmd := exec.CommandContext(ctx, f.binary, args...)
				input, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				var out, stderr bytes.Buffer
				cmd.Stdout = &out
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
				current := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
					return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != "" && (r.SessionID != "" || r.ThreadID != "")
				})
				_ = input.Close()
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
				if err = json.Unmarshal(out.Bytes(), &wire); err != nil || wire.SettingsApplication == nil {
					t.Fatalf("%s JSON: %v %s", verb, err, &out)
				}
				if verb == "resume" && len(wire.Items) != 1 {
					t.Fatal("lost AgentList Items")
				}
				for _, text := range []string{"--effort is part of the prompt", "runs in the foreground", "--detach"} {
					if !strings.Contains(stderr.String(), text) {
						t.Fatalf("%s missing %s: %s", verb, text, &stderr)
					}
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
				if !bytes.Contains(raw, []byte("keep  exact spaces --effort")) {
					t.Fatal("provider input changed", string(raw))
				}
				old = awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			}
			// none keeps both new warnings out of stdout/stderr, including prompt flags.
			cmd := exec.CommandContext(ctx, f.binary, "agent", "resume", first.ref, "-o", "none", "--", "exact", "--effort")
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != ""
			})
			_ = input.Close()
			if err = cmd.Wait(); err != nil || output.Len() != 0 {
				t.Fatalf("none: %v %s", err, &output)
			}
		})
	}
}

func TestAgentLifecycleGuidanceFrozenActualCLI(t *testing.T) {
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
}
