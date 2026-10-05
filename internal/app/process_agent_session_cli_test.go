package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
)

func TestProcessSessionBindingActualCLICreateResumeAndHistory(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := processResumeCLIFixture(t, provider)
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(f.root, ".claude"))
			// Provider fixtures own their transcript files, just as real providers do.
			scriptPath := filepath.Join(f.root, "provider.py")
			conversation := "process-session"
			transcript := filepath.Join(f.root, ".claude", "projects", aisessions.EncodeClaudeProjectPath(f.root), conversation+".jsonl")
			content := "{\"type\":\"assistant\",\"message\":{\"content\":\"fixture response\"}}\n"
			if provider == aiModeCodex {
				scriptPath = filepath.Join(f.root, "codex-provider.py")
				conversation = "process-thread"
				transcript = filepath.Join(f.root, ".codex", "sessions", "rollout-"+conversation+".jsonl")
				meta, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": conversation, "cwd": f.root, "timestamp": "2026-10-05T00:00:00Z"}})
				content = string(meta) + "\n" + "{\"type\":\"event_msg\",\"payload\":{\"type\":\"agent_message\",\"message\":\"fixture response\"}}\n"
			}
			script, err := os.ReadFile(scriptPath)
			if err != nil {
				t.Fatal(err)
			}
			pyPath, _ := json.Marshal(transcript)
			pyContent, _ := json.Marshal(content)
			preamble := "import os\nos.makedirs(os.path.dirname(" + string(pyPath) + "),exist_ok=True)\nopen(" + string(pyPath) + ",'w').write(" + string(pyContent) + ")\n"
			if err := os.WriteFile(scriptPath, append([]byte(preamble), script...), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "first task"))
			old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.ConnectionID != "" && (r.SessionID != "" || r.ThreadID != "")
			})
			getRef := func() *coremetadata.AgentSessionRef {
				t.Helper()
				raw, err := exec.CommandContext(ctx, f.binary, "get", "agents", "--project", "uid:"+f.project, "-o", "json").CombinedOutput()
				if err != nil {
					t.Fatalf("get agent: %v %s", err, raw)
				}
				var result struct {
					Items []coremetadata.Agent `json:"items"`
				}
				if err := json.Unmarshal(raw, &result); err != nil || len(result.Items) != 1 {
					t.Fatalf("get JSON: %v %s", err, raw)
				}
				return result.Items[0].Status.SessionRef
			}
			waitCodexCreate(t, ctx, func() bool { return getRef() != nil })
			ref := getRef()
			if ref == nil || ref.Provider != provider {
				t.Fatalf("missing CLI sessionRef: %+v", ref)
			}
			conversation = old.SessionID
			if provider == aiModeClaude {
				if ref.Claude == nil || ref.Claude.SessionID != conversation {
					t.Fatal("wrong Claude session", ref)
				}
				expected := filepath.Join(f.root, ".claude", "projects", aisessions.EncodeClaudeProjectPath(f.root), conversation+".jsonl")
				if ref.Claude.TranscriptPath != expected {
					t.Fatalf("transcript = %q, want %q", ref.Claude.TranscriptPath, expected)
				}
				reg, err := f.store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				agent, _ := reg.Agent(strings.TrimPrefix(first.ref, "uid:"))
				path := claudeAgentTranscriptPath(*agent)
				tail, _, err := readClaudeTranscriptTail(path)
				if path != transcript || err != nil || !bytes.Contains(tail, []byte("fixture response")) {
					t.Fatalf("bound transcript response: %q %s %v", path, tail, err)
				}
			} else {
				conversation = old.ThreadID
				domain, err := defaultCodexStateDomainID(os.Getenv, os.UserHomeDir)
				if err != nil || ref.Codex == nil || ref.Codex.ThreadID != conversation || ref.Codex.Endpoint == nil || ref.Codex.Endpoint.StateDomainID != domain || ref.Codex.Endpoint.EndpointGenerationID != "codex-0.160.0" {
					t.Fatalf("Codex tmux endpoint shape: %+v %v", ref, err)
				}
				sessions := filepath.Join(f.root, ".codex", "sessions")
				path := transcript
				rows, err := aisessions.DiscoverProviderContext(ctx, provider, f.root, aisessions.DiscoverOptions{HomeDir: f.root, CodexSessionsDir: sessions, ClaudeProjectsDir: filepath.Join(f.root, ".claude", "projects")}, 10)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, row := range rows.Sessions {
					if row.Agent == provider && row.ResumeID == ref.Codex.ThreadID {
						preview, err := aisessions.ReadPreview(ctx, row, nil)
						if err != nil || preview.Assistant != "fixture response" {
							t.Fatalf("rollout response: %+v %v", preview, err)
						}
						found = true
					}
				}
				if !found {
					t.Fatalf("bound thread did not identify rollout %s: %+v", path, rows)
				}
			}
			sessionsCLI := func() {
				raw, err := exec.CommandContext(ctx, f.binary, "agent", "sessions", "list", first.ref, "-o", "json").CombinedOutput()
				if err != nil || !bytes.Contains(raw, []byte(conversation)) {
					t.Fatalf("agent sessions: %v %s", err, raw)
				}
			}
			sessionsCLI()
			state := filepath.Dir(filepath.Dir(f.store.Path()))
			history, err := sessionhistory.Read(state, strings.TrimPrefix(first.ref, "uid:"))
			if err != nil || len(history.Records) != 1 || history.Records[0].SessionID != conversation {
				t.Fatal("first history", history, err)
			}
			historyBytes, _ := os.ReadFile(sessionhistory.Path(state))
			first.shutdown(t)
			second := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", "resume task"})
			awaitProcessResumeRecord(t, ctx, f, second.ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != ""
			})
			if !reflect.DeepEqual(getRef(), ref) {
				t.Fatalf("resume changed same conversation ref: %+v %+v", getRef(), ref)
			}
			sessionsCLI()
			second.shutdown(t)
			after, _ := os.ReadFile(sessionhistory.Path(state))
			if !bytes.Equal(historyBytes, after) {
				t.Fatal("same conversation resume appended history")
			}
		})
	}
}

func TestProcessSessionStartingDeleteActualCLITenTimes(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for i := range 10 {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			f := newProcessCreateCLI(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			owner := startResumeCLIInvocation(t, ctx, f, f.args())
			old := awaitProcessResumeRecord(t, ctx, f, owner.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.SessionID != "" })
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			a, _ := reg.Agent(old.Binding.AgentUID)
			if a.Status.SessionRef != nil {
				t.Fatal("SessionStart reservation was recorded as initialized conversation")
			}
			state := filepath.Dir(filepath.Dir(f.store.Path()))
			if _, err := os.Stat(sessionhistory.Path(state)); !os.IsNotExist(err) {
				t.Fatal("uninitialized history", err)
			}
			raw, err := exec.CommandContext(ctx, f.binary, "delete", "agent", owner.ref, "--yes").CombinedOutput()
			if err != nil {
				t.Fatalf("starting delete: %v %s", err, raw)
			}
			_ = owner.input.Close()
			err = owner.cmd.Wait()
			owner.done = true
			if err != nil {
				t.Fatalf("owner after delete: %v %s", err, owner.stderr.String())
			}
			reg, err = f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := reg.Agent(old.Binding.AgentUID); ok {
				t.Fatal("starting Agent retained")
			}
			if _, ok := reg.Pane(old.Binding.PaneUID); ok {
				t.Fatal("starting Pane retained")
			}
			// Wait before init must not backfill the reserved session either.
			history, err := sessionhistory.Read(state, old.Binding.AgentUID)
			if err != nil || len(history.Records) != 0 {
				t.Fatal("starting Wait fabricated history", history, err)
			}
		})
	}
}

func TestProcessSessionWaitBeforeInitNeverBecomesResumeBackfillSource(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	f := newProcessCreateCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	owner := startResumeCLIInvocation(t, ctx, f, f.args())
	old := awaitProcessResumeRecord(t, ctx, f, owner.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.SessionID != "" })
	owner.shutdown(t)
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Agent(old.Binding.AgentUID)
	p, _ := reg.Pane(old.Binding.PaneUID)
	if a.Status.SessionRef != nil || p.Status.ProcessSession.SessionID != "" || p.Status.ProcessSession.ResumeState == coremetadata.ProcessResumable {
		t.Fatalf("uninitialized reservation survived Wait: %+v %+v", a.Status.SessionRef, p.Status.ProcessSession)
	}
}

func TestProcessSessionLegacyAgentBackfilledByActualCLIWait(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := processResumeCLIFixture(t, provider)
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(f.root, ".claude"))
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			owner := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--", "legacy task"))
			old := awaitProcessResumeRecord(t, ctx, f, owner.ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.ConnectionID != "" && (r.SessionID != "" || r.ThreadID != "")
			})
			waitCodexCreate(t, ctx, func() bool {
				reg, err := f.store.LoadReadOnly()
				if err != nil {
					return false
				}
				a, _ := reg.Agent(old.Binding.AgentUID)
				return a.Status.SessionRef != nil
			})
			state := filepath.Dir(filepath.Dir(f.store.Path()))
			// Reproduce an already-created process Agent from before this fix. The
			// owner is idle; its next durable mutation is its actual child Wait.
			_, _, err := f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
				a, _ := reg.Agent(old.Binding.AgentUID)
				a.Status.SessionRef = nil
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(sessionhistory.Path(state)); err != nil {
				t.Fatal(err)
			}
			owner.shutdown(t)
			raw, err := exec.CommandContext(ctx, f.binary, "get", "agents", "--project", "uid:"+f.project, "-o", "json").CombinedOutput()
			if err != nil {
				t.Fatalf("get legacy Agent: %v %s", err, raw)
			}
			var projection struct {
				Items []coremetadata.Agent `json:"items"`
			}
			if json.Unmarshal(raw, &projection) != nil || len(projection.Items) != 1 {
				t.Fatalf("legacy JSON: %s", raw)
			}
			ref := projection.Items[0].Status.SessionRef
			conversation := old.SessionID
			if provider == aiModeCodex {
				conversation = old.ThreadID
			}
			if ref == nil || ref.Provider != provider || (provider == aiModeClaude && (ref.Claude == nil || ref.Claude.SessionID != conversation)) || (provider == aiModeCodex && (ref.Codex == nil || ref.Codex.ThreadID != conversation)) {
				t.Fatalf("Wait did not backfill legacy Agent: %+v", ref)
			}
			history, err := sessionhistory.Read(state, old.Binding.AgentUID)
			if err != nil || len(history.Records) != 1 || history.Records[0].SessionID != conversation {
				t.Fatal("legacy Wait history", history, err)
			}
			raw, err = exec.CommandContext(ctx, f.binary, "agent", "sessions", "list", owner.ref, "-o", "json").CombinedOutput()
			if err != nil || !bytes.Contains(raw, []byte(conversation)) {
				t.Fatalf("legacy agent sessions: %v %s", err, raw)
			}
		})
	}
}

func TestProcessSessionRelaunchPreservesConversationActualCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	f := processResumeCLIFixture(t, aiModeClaude)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(f.root, ".claude"))
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--model", "stub-model", "--", "first task"))
	old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
		return r.SessionID != "" && r.ConnectionID != ""
	})
	var ref *coremetadata.AgentSessionRef
	waitCodexCreate(t, ctx, func() bool {
		reg, err := f.store.LoadReadOnly()
		if err != nil {
			return false
		}
		agent, _ := reg.Agent(old.Binding.AgentUID)
		ref = agent.Status.SessionRef.Clone()
		return ref != nil
	})
	state := filepath.Dir(filepath.Dir(f.store.Path()))
	var before []byte
	waitCodexCreate(t, ctx, func() bool {
		var err error
		before, err = os.ReadFile(sessionhistory.Path(state))
		return err == nil && bytes.Contains(before, []byte(old.SessionID))
	})
	second, result := startProcessRelaunchCLI(t, ctx, f, first.ref, "--model", "relaunch-model", "--yes", "--", "relaunch task")
	if result.AgentUID != old.Binding.AgentUID || result.NewPaneUID != old.Binding.PaneUID {
		t.Fatalf("relaunch changed resource identity: %+v", result)
	}
	current := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
		return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != "" && r.SessionID == old.SessionID
	})
	if current.History == nil || current.History.Binding != old.Binding {
		t.Fatal("missing relaunch generation history")
	}
	_ = first.input.Close()
	if err := first.cmd.Wait(); err != nil && !strings.Contains(first.stderr.String(), "process control closed") {
		t.Fatalf("retired owner: %v %s", err, first.stderr.String())
	}
	first.done = true
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent(old.Binding.AgentUID)
	if !reflect.DeepEqual(ref, agent.Status.SessionRef) {
		t.Fatalf("relaunch rewrote ref: %+v => %+v", ref, agent.Status.SessionRef)
	}
	raw, err := exec.CommandContext(ctx, f.binary, "agent", "sessions", "list", first.ref, "-o", "json").CombinedOutput()
	if err != nil || !bytes.Contains(raw, []byte(old.SessionID)) {
		t.Fatalf("relaunch sessions: %v %s", err, raw)
	}
	second.shutdown(t)
	after, err := os.ReadFile(sessionhistory.Path(state))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("relaunch changed conversation history: %v %s", err, after)
	}
}
