package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	"github.com/crevissepartners/projmux/internal/core/aibadge"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func deferredRelaunchFixture(t *testing.T) processCreateCLI {
	t.Helper()
	product := os.Getenv("PMX_TEST_CLI")
	if product == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	root, err := os.MkdirTemp(filepath.Dir(product), "f-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var before claudeReadinessRoot
		if t.Failed() {
			before = claudeReadinessRootFor(root)
		}
		started := time.Now()
		err := os.RemoveAll(root)
		claudeReadinessRootRemoved(t, before, started, err, root)
	})
	for _, key := range []string{"TMUX", "TMUX_PANE"} {
		t.Setenv(key, "")
	}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "PROJMUX_") || strings.HasPrefix(key, "__PROJMUX_") {
			t.Setenv(key, "")
		}
	}
	for key, dir := range map[string]string{"HOME": root, "XDG_STATE_HOME": root + "/state", "XDG_CONFIG_HOME": root + "/config", "XDG_CACHE_HOME": root + "/cache"} {
		t.Setenv(key, dir)
	}
	binary := filepath.Join(root, "projmux")
	raw, err := os.ReadFile(product)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(binary, raw, 0700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "provider.py")
	trace := filepath.Join(root, "wire.jsonl")
	provider := strings.Replace(processClaudeProviderFixture, " frame=json.loads(line)", " open("+fmt.Sprintf("%q", trace)+",'a').write(line)\n frame=json.loads(line)", 1)
	provider = strings.Replace(provider, "emit({'type':'assistant','session_id':'process-session','message_echo':m});c.close()", "open(os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'messages.jsonl'),'a').write(json.dumps(m)+'\\n');emit({'type':'assistant','session_id':'process-session','message_echo':m});c.close()", 1)
	provider = strings.Replace(provider, "elif prompt=='register-again':", "elif prompt=='send-message':\n   emit({'type':'result','subtype':'success','session_id':'process-session'})\n   b=json.loads(os.environ['PMX_INTERNAL_CLAUDE_PROCESS_BINDING'])\n   subprocess.Popen([os.environ['PMX_TEST_PROCESS_BINARY'],'agent','message','send','uid:'+b['Agent'],'--source','uid:'+b['Agent'],'--','peer payload'],stdout=open(os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'message-receipt'),'w'),stderr=subprocess.STDOUT)\n  elif prompt=='register-again':", 1)
	provider = strings.Replace(provider, "path=os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'provider.sock')", "open(os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'argv.jsonl'),'a').write(json.dumps(sys.argv)+'\\n')\npath=os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'provider-%s.sock'%os.getpid())", 1)
	provider = strings.Replace(provider, "  inp={'questions'", "  emit({'type':'result','subtype':'success','session_id':'process-session'})\n  continue\n  inp={'questions'", 1)
	if err = os.WriteFile(script, []byte(provider), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "claude"), []byte("#!/bin/sh\n"+processFixtureExports(root)+"exec python3 -u "+fmt.Sprintf("%q", script)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "tmux"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PMX_TEST_PROCESS_ROOT", root)
	t.Setenv("PMX_TEST_PROCESS_BINARY", binary)
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	store := intmetadata.NewStore(intmetadata.PathFor(paths.StateDir))
	var project, window string
	_, _, err = store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		p, err := intmetadata.DefaultMutator().RegisterProject(reg, coremetadata.RegisterProjectOptions{Root: root, DefaultShell: "/bin/sh", OperationID: "fixture-create"})
		if err == nil {
			project, window = p.Project.Metadata.UID, p.Windows[0].Metadata.UID
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	return processCreateCLI{root, binary, trace, project, window, store}
}

func deferredRelaunchCLI(t *testing.T, ctx context.Context, f processCreateCLI, ref string, flags ...string) *deferredCLIClaim {
	t.Helper()
	run := &deferredCLIClaim{cmd: exec.CommandContext(ctx, f.binary, append([]string{"agent", "relaunch", ref}, flags...)...)}
	run.cmd.Stderr = &run.stderr
	var err error
	run.input, err = run.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := run.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	run.output = bufio.NewReader(out)
	if err = run.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		run.cleanup.before(t, f, ref, run.cmd)
		started := time.Now()
		closeErr := run.input.Close()
		run.cleanup.Close = append(run.cleanup.Close, claudeReadinessCallResult(started, closeErr))
		if !run.done {
			started = time.Now()
			killErr := run.cmd.Process.Kill()
			run.cleanup.Kill = append(run.cleanup.Kill, claudeReadinessCallResult(started, killErr))
			started = time.Now()
			waitErr := run.cmd.Wait()
			run.cleanup.CLIWait = append(run.cleanup.CLIWait, claudeReadinessCallResult(started, waitErr))
		}
		run.cleanup.after(t, f, ref)
	})
	barrier := filepath.Join(f.root, "hold-init")
	if _, barrierErr := os.Stat(barrier); barrierErr == nil {
		c := deferredFixtureCommand(f)
		uid := strings.TrimPrefix(ref, "uid:")
		for {
			intent, e := c.readDeferredLaunch(uid)
			if e != nil {
				t.Fatal(e)
			}
			if intent != nil && intent.Previous != nil {
				reg := mustRegistry(t, f)
				agent, _ := reg.Agent(uid)
				wire := deferredWireTexts(t, f)
				activation, provider, current := reg.CurrentProcessActivation(intent.Attempt.Target)
				child, _, childErr := localipc.Process(activation.Child.PID)
				host, _, hostErr := localipc.Process(activation.HostProcess.PID)
				if current && childErr == nil && hostErr == nil && child == activation.Child && host == activation.HostProcess && provider == aiModeClaude && agent.Status.Phase == coremetadata.PhaseRunning && len(wire) > 0 && wire[len(wire)-1] == "prompt first" {
					before, _ := os.ReadFile(f.store.Path())
					lbefore, _ := os.ReadFile(c.deferredStatePath("deferred-launches", uid))
					out, e := exec.CommandContext(ctx, f.binary, "agent", "resume", ref, "--wait-for-peer").CombinedOutput()
					after, _ := os.ReadFile(f.store.Path())
					lafter, _ := os.ReadFile(c.deferredStatePath("deferred-launches", uid))
					if e == nil || !bytes.Contains(out, []byte("process-resume-owned")) || !bytes.Equal(before, after) || !bytes.Equal(lbefore, lafter) {
						t.Fatalf("pre-init typed refusal/state invariant mismatch: %v %s", e, out)
					}
					if e = writeDeferredState(filepath.Join(f.root, "captured-intent"), intent); e != nil {
						t.Fatal(e)
					}
					if e = os.Remove(barrier); e != nil {
						t.Fatal(e)
					}
					break
				}
			}
			select {
			case <-ctx.Done():
				raw, _ := os.ReadFile(f.store.Path())
				launch, _ := os.ReadFile(c.deferredStatePath("deferred-launches", uid))
				_ = writeDeferredState(filepath.Join(filepath.Dir(filepath.Dir(f.binary)), "init-barrier-failure.json"), map[string]any{"stage": "pre-init barrier", "registry": json.RawMessage(raw), "launch": json.RawMessage(launch), "wire": deferredWireTexts(t, f), "stderr": run.stderr.String()})
				t.Fatal("intent barrier not ready")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	line, err := run.output.ReadString('\n')
	delimiter := slices.Index(flags, "--")
	prompt := delimiter >= 0 && strings.TrimSpace(strings.Join(flags[delimiter+1:], " ")) != ""
	if err != nil || (!prompt && line != "agent "+ref+" pane uid:"+deferredCandidate(t, f, ref).Record.Binding.PaneUID+" runtime=process foreground=claimed\n") || (prompt && !strings.Contains(line, "relaunch")) {
		t.Fatalf("claimed %q %v %s", line, err, run.stderr.String())
	}
	return run
}

func deferredCandidate(t *testing.T, f processCreateCLI, ref string) processResumeCandidate {
	t.Helper()
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	uid := strings.TrimPrefix(ref, "uid:")
	for _, candidate := range listResumableProcessAgents(reg, processResumeFilter{}) {
		if candidate.Agent.Metadata.UID == uid {
			return candidate
		}
	}
	t.Fatal("not resumable", uid)
	return processResumeCandidate{}
}

// SessionStart can write a session before stream init. Require the owned host
// to report ready and its exact endpoint to qualify before Stop or peer use.
func deferredReady(t *testing.T, ctx context.Context, f processCreateCLI, ref string, beforeObserve ...func(coremetadata.Registry)) {
	t.Helper()
	uid := strings.TrimPrefix(ref, "uid:")
	d := newClaudeReadinessTrace()
	for {
		d.Iteration++
		reg := mustRegistry(t, f)
		for _, before := range beforeObserve {
			before(reg)
		}
		pane, _ := processResumePane(reg, uid)
		if d.check("pane-present", pane != nil) && d.check("session-record-present", pane.Status.ProcessSession != nil) {
			s := pane.Status.ProcessSession
			if d.check("session-present", s.SessionID != "") && d.check("connection-present", s.ConnectionID != "") && d.check("turn-empty", s.TurnID == "") {
				b := processSchemaBinding(s.Binding)
				observer := remoteProcessObserver{ctx: ctx, registry: reg, registryPath: f.store.Path(), binding: b}
				view, err := observer.Observe(b)
				d.result("host-observe", err)
				proof, ok := discoverProcessClaudeProof(f.store.Path(), reg, uid)
				d.resultBool("proof-discovery-aggregate", ok)
				rows, attentionErr := newProcessAttentionStore(filepath.Dir(filepath.Dir(f.store.Path()))).read()
				d.result("attention-read", attentionErr)
				completed := rows[b.Pane]
				if d.check("observe-ok", err == nil) && d.check("host-ready", view.State == "ready") && d.check("proof-guard", ok) && d.check("attention-guard", attentionErr == nil) && d.check("attention-binding-matches", completed.Binding == b) && d.check("attention-provider-claude", completed.Provider == aiModeClaude) && d.check("attention-sequence-positive", completed.Sequence > 0) && d.check("attention-response-complete", completed.Badge == aibadge.ResponseComplete) {
					_, err = lookupClaudeProcessRegistration(proof)
					if d.check("registration-lookup-ok", err == nil) {
						return
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			d.failure(t, f, ref)
			t.Fatal("verified provider init/endpoint not ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func deferredFixtureCommand(f processCreateCLI) *agentCommand {
	state := filepath.Dir(filepath.Dir(f.store.Path()))
	return &agentCommand{loadRegistry: f.store.LoadReadOnly, messagePaths: agentMessagePaths{registryPath: f.store.Path()}, messageStore: messagestore.NewStore(state)}
}

func deferredArgv(t *testing.T, f processCreateCLI) [][]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, "argv.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for line := range bytes.SplitSeq(bytes.TrimSpace(raw), []byte("\n")) {
		var args []string
		if err = json.Unmarshal(line, &args); err != nil {
			t.Fatal(err)
		}
		out = append(out, args)
	}
	return out
}

func deferredPeer(t *testing.T, ctx context.Context, f processCreateCLI, source, target, ref, text string) messagestore.Record {
	t.Helper()
	out, err := exec.CommandContext(ctx, f.binary, "agent", "message", "send", target, "--source", source, "--message-ref", ref, "--", text).CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("held\ttarget-awaiting-resume")) {
		t.Fatalf("peer: %v %s", err, out)
	}
	record, found, err := deferredFixtureCommand(f).messageStore.Get(ref)
	if err != nil || !found {
		t.Fatal("held record", err)
	}
	return record
}

func deferredWireTexts(t *testing.T, f processCreateCLI) []string {
	t.Helper()
	raw, err := os.ReadFile(f.trace)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for line := range bytes.SplitSeq(bytes.TrimSpace(raw), []byte("\n")) {
		var frame struct {
			Type    string `json:"type"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if err = json.Unmarshal(line, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type == "user" {
			texts = append(texts, frame.Message.Content)
		}
	}
	return texts
}

func TestDeferredClaudeRelaunchWaitsWithoutChildActualCLI(t *testing.T) {
	for _, flags := range [][]string{{"--yes"}, {"--yes", "--model", "new-model", "--effort", "high"}, {"--yes", "--", " "}} {
		t.Run(strings.Join(flags, "-"), func(t *testing.T) {
			f := deferredRelaunchFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
			deferredReady(t, ctx, f, first.ref)
			run := deferredRelaunchCLI(t, ctx, f, first.ref, flags...)
			_ = first.cmd.Wait()
			first.done = true
			if len(deferredArgv(t, f)) != 1 {
				t.Fatal("empty relaunch started a child")
			}
			candidate := deferredCandidate(t, f, first.ref)
			c := deferredFixtureCommand(f)
			claim, err := readDeferredClaim(c.deferredClaimPath(candidate.Agent.Metadata.UID))
			if err != nil || !deferredClaimLive(claim) || !deferredClaimMatches(claim, mustRegistry(t, f), claim.Agent) {
				t.Fatal("claim proof", err)
			}
			launch, err := c.readDeferredLaunch(claim.Agent)
			if err != nil || launch == nil || launch.Command.Env != nil || !launch.matches(candidate) {
				t.Fatal("durable configuration", err)
			}
			path := c.deferredStatePath("deferred-launches", claim.Agent)
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("launch permissions", err)
			}
			before, _ := os.ReadFile(f.store.Path())
			out, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", first.ref, "--yes", "--model", "forbidden").CombinedOutput()
			if err == nil || !bytes.Contains(out, []byte("process-resume-owned")) {
				t.Fatalf("ordinary relaunch bypassed claim: %v %s", err, out)
			}
			after, _ := os.ReadFile(f.store.Path())
			currentClaim, _ := readDeferredClaim(c.deferredClaimPath(claim.Agent))
			if !bytes.Equal(before, after) || currentClaim.Nonce != claim.Nonce {
				t.Fatal("owned refusal changed Registry or nonce")
			}
			run.finish(t)
			if _, err = os.Stat(path); err != nil {
				t.Fatal("EOF lost launch", err)
			}
			if _, err = os.Stat(c.deferredClaimPath(claim.Agent)); !os.IsNotExist(err) {
				t.Fatal("EOF retained live claim", err)
			}
			// Simulate interruption after launch fsync but before Registry CAS.
			_, _, err = f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
				a, _ := reg.Agent(claim.Agent)
				a.Spec, a.Metadata.Annotations = launch.OldSpec, launch.OldAnnotations
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			recovered := startDeferredCLIClaim(t, ctx, f, first.ref)
			if !launch.matches(deferredCandidate(t, f, first.ref)) || len(deferredArgv(t, f)) != 1 {
				t.Fatal("prepared recipe CAS recovery failed")
			}
			recovered.finish(t)
		})
	}
}

func TestDeferredClaudeProjectGuidanceStaysFrozenUntilFirstInputActualCLI(t *testing.T) {
	f := deferredRelaunchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "initial"))
	deferredReady(t, ctx, f, first.ref)
	first.shutdown(t)
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	store := agentguidance.NewDefaultProjectStore(paths)
	prepared := []byte("Project instruction captured when preparing the recipe.\n")
	if err = store.Save(f.project, prepared); err != nil {
		t.Fatal(err)
	}
	run := deferredRelaunchCLI(t, ctx, f, first.ref)
	uid := strings.TrimPrefix(first.ref, "uid:")
	frozen, err := deferredFixtureCommand(f).readDeferredLaunch(uid)
	if err != nil || frozen == nil {
		t.Fatal("prepared recipe missing", err)
	}
	index := slices.Index(frozen.Command.Args, "--append-system-prompt-file")
	if index < 0 || index+1 >= len(frozen.Command.Args) {
		t.Fatal("prepared instructions missing", frozen.Command.Args)
	}
	before, err := os.ReadFile(frozen.Command.Args[index+1])
	if err != nil || !bytes.Contains(before, prepared) {
		t.Fatal("prepared Project instructions missing", err)
	}
	run.finish(t) // Release the claimant; the prepared recipe remains durable.
	changed := []byte("Project instruction changed after preparation.\n")
	if err = store.Save(f.project, changed); err != nil {
		t.Fatal(err)
	}
	resumed := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", "first input"})
	deferredReady(t, ctx, f, first.ref)
	resumed.shutdown(t)
	argv := deferredArgv(t, f)
	last := argv[len(argv)-1][1:]
	want := append(append([]string{}, frozen.Command.Args...), "--resume", frozen.Retired.SessionID)
	if !reflect.DeepEqual(last, want) {
		t.Fatalf("first input replaced prepared command: got %q want %q", last, want)
	}
	after, err := os.ReadFile(last[index+1])
	if err != nil || !bytes.Equal(before, after) || bytes.Contains(after, changed) {
		t.Fatal("first input replaced prepared instruction bytes", err)
	}
	reg := mustRegistry(t, f)
	agent, _ := reg.Agent(uid)
	if agent.Metadata.Annotations[coremetadata.AnnotationAgentProjectGuidanceDigest] != agentguidance.Digest(prepared) {
		t.Fatal("first input replaced prepared Project digest")
	}
	texts := deferredWireTexts(t, f)
	if texts[len(texts)-1] != "first input" {
		t.Fatal("first input was not launched", texts)
	}
}

func TestDeferredClaudeRelaunchChangedSnapshotRefusedActualCLI(t *testing.T) {
	f := deferredRelaunchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "initial"))
	deferredReady(t, ctx, f, first.ref)
	first.shutdown(t)
	run := deferredRelaunchCLI(t, ctx, f, first.ref, "--model", "new-model")
	uid := strings.TrimPrefix(first.ref, "uid:")
	launch, err := deferredFixtureCommand(f).readDeferredLaunch(uid)
	if err != nil || launch == nil || len(launch.Files) == 0 {
		t.Fatal("snapshot absent", err)
	}
	run.finish(t)
	before, _ := os.ReadFile(f.store.Path())
	for path := range launch.Files {
		if err = os.WriteFile(path, []byte("changed snapshot"), 0600); err != nil {
			t.Fatal(err)
		}
		break
	}
	out, err := exec.CommandContext(ctx, f.binary, "agent", "resume", first.ref, "--wait-for-peer").CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("process-resume-refused")) {
		t.Fatalf("tampered snapshot accepted %v %s", err, out)
	}
	after, _ := os.ReadFile(f.store.Path())
	retained, err := deferredFixtureCommand(f).readDeferredLaunch(uid)
	if err != nil || deferredLaunchDigest(retained) != deferredLaunchDigest(launch) || !bytes.Equal(before, after) || len(deferredArgv(t, f)) != 1 {
		t.Fatal("refusal lost recipe or changed Registry/child", err)
	}
}

func mustRegistry(t *testing.T, f processCreateCLI) coremetadata.Registry {
	t.Helper()
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestDeferredClaudeRelaunchPeerUsesNewRecipeActualCLI(t *testing.T) {
	for _, killed := range []bool{false, true} {
		t.Run(fmt.Sprint("kill=", killed), func(t *testing.T) {
			f := deferredRelaunchFixture(t)
			paths, err := config.DefaultPathsFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			profiles := profile.NewDefaultStore(paths)
			writeCodexProfile(t, profiles, "delayed", "model = \"new-model\"\neffort = \"high\"\n[permissions]\nallow = [\"Read\"]\n")
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
			deferredReady(t, ctx, f, first.ref)
			first.shutdown(t)
			old := deferredCandidate(t, f, first.ref)
			var run *deferredCLIClaim
			var primaryAt, cleanupAt time.Time
			journal := filepath.Join(filepath.Dir(filepath.Dir(f.store.Path())), "logs", "operations.jsonl")
			var journalBefore os.FileInfo
			if !killed {
				journalBefore, _ = os.Lstat(journal)
				defer func() { cleanupAt = time.Now() }()
				// Registered before the existing relaunch cleanup: LIFO makes
				// its Close/Kill/Wait finish before inspecting the owned buffer.
				t.Cleanup(func() {
					if primaryAt.IsZero() || run == nil {
						return
					}
					if run.cmd.ProcessState == nil {
						t.Log("wake evidence post-cleanup=wait-unconfirmed")
					} else {
						logClaudeFailureBuffer(t, "post-cleanup", run.stderr.Bytes())
						t.Logf("wake evidence cleanup-intervened=true cli-wait-exit=%d provider-wait=unproven", run.cmd.ProcessState.ExitCode())
					}
					logClaudeFailureRegistration(t, journal, journalBefore, old.Agent.Metadata.UID, old.Record.Binding.PaneUID, primaryAt, cleanupAt)
				})
			}
			relaunchStarted := time.Now()
			run = deferredRelaunchCLI(t, ctx, f, first.ref, "--profile", "delayed")
			claimReadyAt := time.Now()
			frozen, err := deferredFixtureCommand(f).readDeferredLaunch(old.Agent.Metadata.UID)
			if err != nil || frozen == nil {
				t.Fatal(err)
			}
			if len(frozen.Files) < 1 || permissionModes(frozen.Command.Args) != "auto" {
				t.Fatal("permissions snapshot proof missing")
			}
			// Changes to named profiles must not replace the committed recipe.
			writeCodexProfileFile(t, profiles, "delayed", "model = \"changed-model\"\neffort = \"low\"\n[permissions]\nallow = [\"Bash\"]\n")
			source := startResumeCLIInvocation(t, ctx, f, f.args("--name", "source", "--profile", "none", "--", "source"))
			deferredReady(t, ctx, f, source.ref)
			claimantIdentitySHA := deferredPauseClaimant(t, ctx, f, first.ref, run)
			record := deferredPeer(t, ctx, f, source.ref, first.ref, "delayed-peer", "peer exact\ncontent")
			expected, err := deferredPeerText(record)
			if err != nil {
				t.Fatal(err)
			}
			wakeStarted := time.Now()
			if killed {
				_ = run.cmd.Process.Kill()
				_ = run.cmd.Wait()
				run.done = true
				run = startDeferredCLIClaim(t, ctx, f, first.ref)
			} else if err = run.cmd.Process.Signal(syscall.SIGCONT); err != nil {
				t.Fatal(err)
			}
			line, err := run.output.ReadString('\n')
			if err != nil {
				cached := run.stderr.String()
				if !killed {
					primaryAt = time.Now()
					logClaudeFailureBuffer(t, "primary-cache-copy-unsettled", []byte(cached))
					t.Logf("wake evidence relaunch-start=%s claim-ready=%s continue-start=%s primary-at=%s identity-subject=claimant-cli identity-sha256=%x producer-pid=unproven", relaunchStarted.UTC().Format(time.RFC3339Nano), claimReadyAt.UTC().Format(time.RFC3339Nano), wakeStarted.UTC().Format(time.RFC3339Nano), primaryAt.UTC().Format(time.RFC3339Nano), claimantIdentitySHA)
				}
				t.Fatalf("wake %v %s", err, cached)
			}
			if killed {
				if !strings.Contains(line, "foreground=owned") {
					t.Fatal(line)
				}
			} else if !strings.Contains(line, "relaunch") {
				t.Fatal(line)
			}
			deferredCLIStatus(t, ctx, f, "delayed-peer", "delivered")
			current := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.Binding.Generation != old.Record.Binding.Generation && r.SessionID != ""
			})
			assertProcessCLIForeground(t, f, current)
			if current.SessionID != old.Record.SessionID || current.Binding.PaneUID != old.Record.Binding.PaneUID {
				t.Fatal("conversation changed")
			}
			run.finish(t)
			source.shutdown(t)
			argv := deferredArgv(t, f)
			last := argv[len(argv)-1][1:]
			want := append(append([]string{}, frozen.Command.Args...), "--resume", old.Record.SessionID)
			if !reflect.DeepEqual(last, want) || permissionModes(last) != "auto" {
				t.Fatalf("frozen argv\ngot %q\nwant%q", last, want)
			}
			texts := deferredWireTexts(t, f)
			if texts[len(texts)-1] != expected {
				t.Fatalf("first peer bytes %q", texts)
			}
			if launch, err := deferredFixtureCommand(f).readDeferredLaunch(old.Agent.Metadata.UID); err != nil || launch != nil {
				t.Fatal("successful launch not consumed", err)
			}
		})
	}
}

func TestDeferredClaudeRelaunchFailedInitRetainsRecipeActualCLI(t *testing.T) {
	f := deferredRelaunchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "initial"))
	deferredReady(t, ctx, f, first.ref)
	first.shutdown(t)
	run := deferredRelaunchCLI(t, ctx, f, first.ref, "--model", "new-model", "--effort", "high")
	uid := strings.TrimPrefix(first.ref, "uid:")
	c := deferredFixtureCommand(f)
	frozen, err := c.readDeferredLaunch(uid)
	if err != nil || frozen == nil {
		t.Fatal("recipe", err)
	}
	source := startResumeCLIInvocation(t, ctx, f, f.args("--name", "source", "--profile", "none", "--", "source"))
	deferredReady(t, ctx, f, source.ref)
	script := filepath.Join(f.root, "provider.py")
	original, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	broken := bytes.ReplaceAll(original, []byte("emit({'type':'system','subtype':'init','session_id':'process-session'})"), []byte("emit({'type':'system','subtype':'init','session_id':'wrong-session'})"))
	if err = os.WriteFile(script, broken, 0600); err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		ref := fmt.Sprintf("failed-init-%d", attempt)
		deferredPeer(t, ctx, f, source.ref, first.ref, ref, "first peer")
		err = run.cmd.Wait()
		run.done = true
		if err == nil {
			t.Fatal("mismatched init succeeded")
		}
		_ = run.input.Close()
		deferredCLIStatus(t, ctx, f, ref, "failed")
		retained, err := c.readDeferredLaunch(uid)
		candidate := deferredCandidate(t, f, first.ref)
		if err != nil || retained == nil || retained.Attempt == nil || !retained.matches(candidate) || retained.Retired.SessionID != frozen.Retired.SessionID || !reflect.DeepEqual(retained.Command, frozen.Command) {
			t.Fatalf("failed startup recipe: %v %s", err, run.stderr.String())
		}
		attention := newProcessAttentionStore(filepath.Dir(filepath.Dir(f.store.Path())))
		// Same generation is insufficient: refuse a different complete binding.
		if err = attention.update(func(rows map[string]processAttentionRecord) error {
			r := rows[retained.Attempt.Target.PaneUID]
			r.Binding.Host += "-foreign"
			rows[r.Binding.Pane] = r
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(f.store.Path())
		launchBefore, _ := os.ReadFile(c.deferredStatePath("deferred-launches", uid))
		out, err := exec.CommandContext(ctx, f.binary, "agent", "resume", first.ref, "--wait-for-peer").CombinedOutput()
		if err == nil || !bytes.Contains(out, []byte("attention source differs")) {
			t.Fatalf("foreign attention accepted %v %s", err, out)
		}
		after, _ := os.ReadFile(f.store.Path())
		launchAfter, _ := os.ReadFile(c.deferredStatePath("deferred-launches", uid))
		if !bytes.Equal(before, after) || !bytes.Equal(launchBefore, launchAfter) {
			t.Fatal("refusal overwrote Registry/launch")
		}
		if err = attention.update(func(rows map[string]processAttentionRecord) error {
			b := processSchemaBinding(retained.Attempt.Target)
			if attempt == 1 {
				b = processSchemaBinding(retained.Attempt.Source.Binding)
			}
			rows[b.Pane] = processAttentionRecord{Binding: b, Provider: aiModeClaude, Sequence: 99, NoticeSequence: 12, Pending: map[string]processAttentionPending{"old": {Kind: "permission"}}}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// Crash after attention CAS/before L, then L-new/attention-old.
		if attempt == 0 {
			retained.Retired = *retained.Attempt.Source.Clone()
			if err = writeDeferredState(c.deferredStatePath("deferred-launches", uid), retained); err != nil {
				t.Fatal(err)
			}
		}
		if attempt == 0 {
			run = startDeferredCLIClaim(t, ctx, f, first.ref)
		}
	}
	if err = os.WriteFile(script, original, 0600); err != nil {
		t.Fatal(err)
	}
	reclaimed := startDeferredCLIClaim(t, ctx, f, first.ref)
	deferredPeer(t, ctx, f, source.ref, first.ref, "after-failed-init", "new peer")
	if line, err := reclaimed.output.ReadString('\n'); err != nil || !strings.Contains(line, "foreground=owned") {
		t.Fatal("reclaim startup", line, err, reclaimed.stderr.String())
	}
	deferredCLIStatus(t, ctx, f, "after-failed-init", "delivered")
	for attempt := range 2 {
		deferredCLIStatus(t, ctx, f, fmt.Sprintf("failed-init-%d", attempt), "failed")
	}
	argv := deferredArgv(t, f)
	want := append(append([]string{}, frozen.Command.Args...), "--resume", frozen.Retired.SessionID)
	if !reflect.DeepEqual(argv[len(argv)-1][1:], want) {
		t.Fatal("recovery replaced frozen argv")
	}
	reclaimed.finish(t)
	source.shutdown(t)
	if launch, err := c.readDeferredLaunch(uid); err != nil || launch != nil {
		t.Fatal("recovery did not consume recipe", err)
	}
}

func TestDeferredClaudeRelaunchUserUsesRawFirstFrameActualCLI(t *testing.T) {
	// Execution is subject to the operator/fixture permission decision. This
	// test is always selected by the copied-CLI gate; it is never skipped there.
	for _, relaunch := range []bool{true, false} {
		t.Run(fmt.Sprint("recipe=", relaunch), func(t *testing.T) {
			f := deferredRelaunchFixture(t)
			diagnostic := newClaudeAuthorityFailureDiagnostics(t)
			diagnostic.paths = []string{f.store.Path()}
			defer diagnostic.beforeCleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "initial"))
			deferredReady(t, ctx, f, first.ref)
			first.shutdown(t)
			var run *deferredCLIClaim
			if relaunch {
				run = deferredRelaunchCLI(t, ctx, f, first.ref, "--model", "new-model", "--effort", "high")
			} else {
				run = startDeferredCLIClaim(t, ctx, f, first.ref)
			}
			literal := "raw user input\nwith a literal newline"
			out, err := exec.CommandContext(ctx, f.binary, "agent", "turn", "start", first.ref, "--", literal).CombinedOutput()
			if err != nil || !bytes.Contains(out, []byte("runtime=process")) {
				t.Fatalf("user %v %s", err, out)
			}
			texts := deferredWireTexts(t, f)
			if texts[len(texts)-1] != literal || strings.Contains(texts[len(texts)-1], "projmux-coordination") {
				t.Fatalf("raw user %q", texts)
			}
			run.finish(t)
			input, err := deferredFixtureCommand(f).readDeferredInput(strings.TrimPrefix(first.ref, "uid:"))
			if err != nil || input == nil || input.Phase != "settled" || !input.Success || input.Text != "" {
				t.Fatal("user acknowledgement/redaction", err)
			}
		})
	}
}

func TestDeferredClaudeRelaunchUserCompetitionAndCancelActualCLI(t *testing.T) {
	f := deferredRelaunchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "initial"))
	deferredReady(t, ctx, f, first.ref)
	first.shutdown(t)
	run := deferredRelaunchCLI(t, ctx, f, first.ref, "--model", "new-model")
	deferredPauseClaimant(t, ctx, f, first.ref, run)
	uid := strings.TrimPrefix(first.ref, "uid:")
	c := deferredFixtureCommand(f)
	caller := exec.CommandContext(ctx, f.binary, "agent", "turn", "start", first.ref, "--", "cancelled raw input")
	var output bytes.Buffer
	caller.Stdout, caller.Stderr = &output, &output
	if err := caller.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = caller.Process.Kill() })
	for {
		input, err := c.readDeferredInput(uid)
		if err != nil {
			t.Fatal(err)
		}
		if input != nil && input.Phase == "pending" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("pending input not accepted")
		case <-time.After(10 * time.Millisecond):
		}
	}
	deferredAssertBusyUnchanged(t, ctx, f, first.ref)
	if err := caller.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := caller.Wait(); err == nil {
		t.Fatal("cancelled input reported success", output.String())
	}
	input, err := c.readDeferredInput(uid)
	if err != nil || input == nil || input.Phase != "settled" || input.Text != "" || input.Success || input.Unknown || input.Reason != "caller-cancelled" {
		t.Fatal("pending cancellation proof", input, err)
	}
	if err = run.cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	run.finish(t)
	if len(deferredArgv(t, f)) != 1 {
		t.Fatal("cancelled or losing input started a child")
	}
}

// A refused losing input must preserve the winner's exact durable authority.
func deferredAssertBusyUnchanged(t *testing.T, ctx context.Context, f processCreateCLI, ref string) {
	t.Helper()
	c, uid := deferredFixtureCommand(f), strings.TrimPrefix(ref, "uid:")
	paths := []string{f.store.Path(), c.deferredClaimPath(uid), c.deferredStatePath("deferred-launches", uid), c.deferredStatePath("deferred-inputs", uid), filepath.Join(f.root, "argv.jsonl")}
	read := func() [][]byte {
		var result [][]byte
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			result = append(result, raw)
		}
		return result
	}
	before := read()
	out, err := exec.CommandContext(ctx, f.binary, "agent", "turn", "start", ref, "--", "losing input").CombinedOutput()
	if err == nil || strings.TrimSpace(string(out)) != processhost.ErrBusy.Error() {
		t.Fatalf("losing input refusal: %v %s", err, out)
	}
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("busy refusal changed Registry, claim nonce, recipe, input or child")
	}
}

func TestDeferredClaudeRelaunchPeerFirstRejectsUserActualCLI(t *testing.T) {
	f := deferredRelaunchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "initial"))
	deferredReady(t, ctx, f, first.ref)
	first.shutdown(t)
	run := deferredRelaunchCLI(t, ctx, f, first.ref, "--model", "new-model")
	source := startResumeCLIInvocation(t, ctx, f, f.args("--name", "source", "--profile", "none", "--", "source"))
	deferredReady(t, ctx, f, source.ref)
	deferredPauseClaimant(t, ctx, f, first.ref, run)
	record := deferredPeer(t, ctx, f, source.ref, first.ref, "peer-first", "peer first\nexact content")
	expected, err := deferredPeerText(record)
	if err != nil {
		t.Fatal(err)
	}
	deferredAssertBusyUnchanged(t, ctx, f, first.ref)
	if err = run.cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	if _, err = run.output.ReadString('\n'); err != nil {
		t.Fatal(err, run.stderr.String())
	}
	deferredCLIStatus(t, ctx, f, "peer-first", "delivered")
	run.finish(t)
	source.shutdown(t)
	texts := deferredWireTexts(t, f)
	if texts[len(texts)-1] != expected || len(deferredArgv(t, f)) != 3 {
		t.Fatal("peer-first frame bytes or child count", texts)
	}
}

func TestDeferredClaudeRelaunchGoUserWinsPendingSlotActualCLI(t *testing.T) {
	for _, recipe := range []bool{false, true} {
		t.Run(fmt.Sprint("recipe=", recipe), func(t *testing.T) {
			f := deferredRelaunchFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "initial"))
			deferredReady(t, ctx, f, first.ref)
			first.shutdown(t)
			if recipe {
				run := deferredRelaunchCLI(t, ctx, f, first.ref, "--model", "new-model")
				run.finish(t)
			}
			source := startResumeCLIInvocation(t, ctx, f, f.args("--name", "source", "--profile", "none", "--", "source"))
			deferredReady(t, ctx, f, source.ref)
			t.Setenv("PMX_TEST_DEFERRED_INTERNAL", "1")
			c, uid := New().agent, strings.TrimPrefix(first.ref, "uid:")
			claim, err := c.claimDeferredProcessAgent(ctx, processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: uid}})
			if err != nil {
				t.Fatal(err)
			}
			defer claim.Close()
			unlock, err := lockDeferredClaim(claim.path)
			if err != nil {
				t.Fatal(err)
			}
			pending, handled, err := c.acceptDeferredUserTurn(uid, "pending CLI loser")
			unlock()
			if err != nil || !handled || pending == nil {
				t.Fatal("pending admission", handled, err)
			}
			record := deferredPeer(t, ctx, f, source.ref, first.ref, "go-user-held", "queued peer")
			expected, err := deferredPeerText(record)
			if err != nil {
				t.Fatal(err)
			}
			literal := "Go user first\nexact newline"
			result, err := claim.Resume(ctx, processResumeFirstFrame{Kind: "user", Text: literal})
			if err != nil {
				t.Fatal("Go user first refused", err)
			}
			// Peers after the first frame use the existing messaging socket.
			// Prove provider read and result before ending the owned process.
			processCLIUntil(t, ctx, func() bool {
				raw, _ := os.ReadFile(filepath.Join(f.root, "messages.jsonl"))
				var pushed struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				}
				if json.Unmarshal(bytes.TrimSpace(raw), &pushed) != nil || pushed.Message.Content != expected {
					return false
				}
				events, snapshot, err := result.Handle.Events(result.Binding, 0)
				if err != nil {
					t.Fatal(err)
				}
				var echo uint64
				for _, event := range events {
					var frame struct {
						Echo struct {
							Message struct {
								Content string `json:"content"`
							} `json:"message"`
						} `json:"message_echo"`
					}
					if json.Unmarshal(event.Raw, &frame) == nil && frame.Echo.Message.Content == expected {
						echo = event.Sequence
					}
					if echo > 0 && event.Sequence > echo && event.Kind == "turn-result" && snapshot.Turn == "" && snapshot.Session == claim.record.Session && snapshot.Connection != "" {
						return true
					}
				}
				return false
			})
			end, stop := context.WithCancel(ctx)
			stop()
			if _, err = result.owner.waitProcessAgent(end, nil); err != nil {
				t.Fatal(err)
			}
			input, err := c.readDeferredInput(uid)
			if err != nil || input == nil || input.Operation != pending.Operation || input.Phase != "settled" || input.Success || input.Unknown || input.Text != "" || input.Reason != "busy" {
				t.Fatal("Go winner failed slot settlement", input, err)
			}
			deferredCLIStatus(t, ctx, f, "go-user-held", "delivered")
			source.shutdown(t)
			texts := deferredWireTexts(t, f)
			if texts[len(texts)-1] != literal || len(deferredArgv(t, f)) != 3 {
				t.Fatal("Go raw first/held drain bytes or child count", texts)
			}
		})
	}
}

// Pause only after excluding every claim critical section, and prove stopped
// before releasing the guard so a live claimant never retains it while parked.
func deferredPauseClaimant(t *testing.T, ctx context.Context, f processCreateCLI, ref string, run *deferredCLIClaim) [32]byte {
	t.Helper()
	uid := strings.TrimPrefix(ref, "uid:")
	path := deferredFixtureCommand(f).deferredClaimPath(uid)
	unlock, err := lockDeferredClaim(path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	record, err := readDeferredClaim(path)
	if err != nil || record.Process.PID != run.cmd.Process.Pid || !deferredClaimLive(record) || !deferredClaimMatches(record, mustRegistry(t, f), uid) {
		t.Fatal("claimant pause authority", record, err)
	}
	if err = run.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	processCLIUntil(t, ctx, func() bool {
		out, err := exec.CommandContext(ctx, "/bin/ps", "-o", "state=", "-p", fmt.Sprint(record.Process.PID)).CombinedOutput()
		if err != nil {
			t.Fatal("owned claimant stopped proof", err, string(out))
		}
		return strings.HasPrefix(strings.TrimSpace(string(out)), "T")
	})
	// Use the identity this existing authority check already read; do not
	// probe the process again or expose its PID/birth value in diagnostics.
	identity, _ := json.Marshal(record.Process)
	return sha256.Sum256(identity)
}

func TestDeferredClaudeExplicitReplacementActualCLI(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint("missing=", missing), func(t *testing.T) {
			f := deferredRelaunchFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "initial"))
			deferredReady(t, ctx, f, first.ref)
			first.shutdown(t)
			paths, err := config.DefaultPathsFromEnv()
			if err != nil {
				t.Fatal(err)
			}
			profiles := profile.NewDefaultStore(paths)
			writeCodexProfile(t, profiles, "previous", "model = \"old-model\"\n[permissions]\nallow = [\"Read\"]\n")
			writeCodexProfile(t, profiles, "replacement", "model = \"next-model\"\n[permissions]\nallow = [\"Bash\"]\n")
			run := deferredRelaunchCLI(t, ctx, f, first.ref, "--profile", "previous")
			run.finish(t)
			c := deferredFixtureCommand(f)
			uid := strings.TrimPrefix(first.ref, "uid:")
			old, err := c.readDeferredLaunch(uid)
			if err != nil || old == nil {
				t.Fatal(err)
			}
			// Explicit replacement can recover an older recipe with no mode,
			// even when its settings snapshot is also unavailable.
			modeIndex := slices.Index(old.Command.Args, "--permission-mode")
			if modeIndex < 0 {
				t.Fatal("auto missing from new prepared launch")
			}
			old.Command.Args = append(slices.Clone(old.Command.Args[:modeIndex]), old.Command.Args[modeIndex+2:]...)
			if err = writeDeferredState(c.deferredStatePath("deferred-launches", uid), old); err != nil {
				t.Fatal(err)
			}
			index := slices.Index(old.Command.Args, "--settings")
			if index < 0 {
				t.Fatal("settings snapshot absent")
			}
			oldPath := old.Command.Args[index+1]
			if missing {
				err = os.Remove(oldPath)
			} else {
				err = os.WriteFile(oldPath, []byte("changed"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			run = deferredRelaunchCLI(t, ctx, f, first.ref, "--profile", "replacement")
			next, err := c.readDeferredLaunch(uid)
			if err != nil || next == nil || next.Model != "next-model" || next.validateFiles() != nil {
				t.Fatal("replacement", err)
			}
			if _, reused := next.Files[oldPath]; reused {
				t.Fatal("old settings snapshot repaired/reused")
			}
			if raw, e := os.ReadFile(oldPath); (!missing && (e != nil || string(raw) != "changed")) || (missing && !os.IsNotExist(e)) {
				t.Fatal("planner repaired old snapshot")
			}
			// Live claim replacement remains typed owned, with every durable byte intact.
			before, _ := os.ReadFile(f.store.Path())
			lbefore, _ := os.ReadFile(c.deferredStatePath("deferred-launches", uid))
			claimbefore, _ := os.ReadFile(c.deferredClaimPath(uid))
			out, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", first.ref, "--model", "foreign").CombinedOutput()
			after, _ := os.ReadFile(f.store.Path())
			lafter, _ := os.ReadFile(c.deferredStatePath("deferred-launches", uid))
			claimafter, _ := os.ReadFile(c.deferredClaimPath(uid))
			if err == nil || !bytes.Contains(out, []byte("process-resume-owned")) || !bytes.Equal(before, after) || !bytes.Equal(lbefore, lafter) || !bytes.Equal(claimbefore, claimafter) {
				t.Fatalf("live replacement changed state: %v %s", err, out)
			}
			run.finish(t)
			resumed := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", "new first"})
			deferredReady(t, ctx, f, first.ref)
			resumed.shutdown(t)
			argv := deferredArgv(t, f)
			want := append(append([]string{}, next.Command.Args...), "--resume", next.Retired.SessionID)
			if !reflect.DeepEqual(argv[len(argv)-1][1:], want) || permissionModes(argv[len(argv)-1]) != "auto" {
				t.Fatal("replacement argv")
			}
		})
	}
}

func TestDeferredClaudePromptRelaunchTransitionActualCLI(t *testing.T) {
	for _, scenario := range []struct{ failed, unknown bool }{{}, {failed: true}, {unknown: true}} {
		failed, unknown := scenario.failed, scenario.unknown
		t.Run(fmt.Sprint("failed=", failed, "/unknown=", unknown), func(t *testing.T) {
			f := deferredRelaunchFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "initial"))
			deferredReady(t, ctx, f, first.ref)
			first.shutdown(t)
			run := deferredRelaunchCLI(t, ctx, f, first.ref, "--model", "frozen-model")
			abandoned, err := readDeferredClaim(deferredFixtureCommand(f).deferredClaimPath(strings.TrimPrefix(first.ref, "uid:")))
			if err != nil {
				t.Fatal(err)
			}
			run.finish(t)
			c := deferredFixtureCommand(f)
			uid := strings.TrimPrefix(first.ref, "uid:")
			prior, err := c.readDeferredLaunch(uid)
			if err != nil || prior == nil {
				t.Fatal(err)
			}
			// Durable prompt intent before reservation CAS recovers only the exact source.
			intent, priorCopy := *prior, *prior
			intent.OldSpec, intent.OldAnnotations = prior.NewSpec, prior.NewAnnotations
			target := prior.Retired.Binding
			target.HostInstanceID += "-intent"
			target.Generation += "-intent"
			target.OperationID += "-intent"
			intent.Previous, intent.Attempt = &priorCopy, &deferredLaunchAttempt{Source: *prior.Retired.Clone(), Target: target}
			if err = writeDeferredState(c.deferredStatePath("deferred-launches", uid), &intent); err != nil {
				t.Fatal(err)
			}
			sourceAgent := deferredCandidate(t, f, first.ref).Agent.Clone()
			// Unrelated source record/config cannot use a valid intent as authority.
			for _, foreignRecord := range []bool{false, true} {
				_, _, err = f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
					a, _ := reg.Agent(uid)
					pane, _ := processResumePane(*reg, uid)
					if foreignRecord {
						pane.Status.ProcessSession.ConnectionID += "-foreign"
					} else {
						a.Metadata.Annotations[coremetadata.AnnotationAgentModel] = "foreign-model"
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				before, _ := os.ReadFile(f.store.Path())
				launchBefore, _ := os.ReadFile(c.deferredStatePath("deferred-launches", uid))
				out, e := exec.CommandContext(ctx, f.binary, "agent", "resume", first.ref, "--wait-for-peer").CombinedOutput()
				after, _ := os.ReadFile(f.store.Path())
				launchAfter, _ := os.ReadFile(c.deferredStatePath("deferred-launches", uid))
				if e == nil || !bytes.Contains(out, []byte("process-resume-refused")) || !bytes.Equal(before, after) || !bytes.Equal(launchBefore, launchAfter) || len(deferredArgv(t, f)) != 1 {
					t.Fatalf("foreign source changed bytes: %v %s", e, out)
				}
				_, _, err = f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
					a, _ := reg.Agent(uid)
					pane, _ := processResumePane(*reg, uid)
					a.Metadata.Annotations = sourceAgent.Clone().Metadata.Annotations
					pane.Status.ProcessSession = prior.Retired.Clone()
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			recovery := startDeferredCLIClaim(t, ctx, f, first.ref)
			recovery.finish(t)
			restoredSource, err := c.readDeferredLaunch(uid)
			if err != nil || deferredLaunchDigest(restoredSource) != deferredLaunchDigest(prior) {
				t.Fatal("intent/source crash", err)
			}
			slot := &deferredUserInput{Version: 1, Operation: "abandoned-b1-user", Nonce: abandoned.Nonce, Binding: abandoned.Binding, Session: abandoned.Session, Process: abandoned.Process, Recipe: deferredLaunchDigest(prior), Deadline: time.Now().Add(deferredUserDeadline), Phase: "pending", Text: "never replay"}
			if unknown {
				slot.Phase = "inflight"
			}
			if err = c.writeDeferredInput(slot); err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(f.root, "provider.py")
			original, err := os.ReadFile(script)
			if err != nil {
				t.Fatal(err)
			}
			if failed {
				broken := bytes.ReplaceAll(original, []byte("emit({'type':'system','subtype':'init','session_id':'process-session'})"), []byte("emit({'type':'system','subtype':'init','session_id':'wrong-session'})"))
				if err = os.WriteFile(script, broken, 0600); err != nil {
					t.Fatal(err)
				}
				out, err := exec.CommandContext(ctx, f.binary, "agent", "relaunch", first.ref, "--model", "prompt-model", "--", "prompt first").CombinedOutput()
				if err == nil {
					t.Fatalf("wrong init accepted %s", out)
				}
				restored, err := c.readDeferredLaunch(uid)
				if err != nil || restored == nil || restored.Previous != nil || !restored.matches(deferredCandidate(t, f, first.ref)) || !reflect.DeepEqual(restored.Command, prior.Command) {
					t.Fatalf("prior not restored: %v %s", err, out)
				}
				// Recreate reserved-target/L-source crash and attention-old after exact failed Wait.
				intent.Retired, intent.Attempt = *restored.Attempt.Source.Clone(), restored.Attempt
				for _, projection := range []bool{false, true} {
					if projection {
						intent.Retired = restored.Retired
					}
					if err = writeDeferredState(c.deferredStatePath("deferred-launches", uid), &intent); err != nil {
						t.Fatal(err)
					}
					attention := newProcessAttentionStore(filepath.Dir(filepath.Dir(f.store.Path())))
					if err = attention.update(func(rows map[string]processAttentionRecord) error {
						b := processSchemaBinding(intent.Attempt.Source.Binding)
						rows[b.Pane] = processAttentionRecord{Binding: b, Provider: aiModeClaude, Terminal: true, Pending: map[string]processAttentionPending{}}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					recovery = startDeferredCLIClaim(t, ctx, f, first.ref)
					recovery.finish(t)
					again, err := c.readDeferredLaunch(uid)
					if err != nil || again.Previous != nil || !again.matches(deferredCandidate(t, f, first.ref)) || !reflect.DeepEqual(again.Command, prior.Command) {
						t.Fatal("reserved/attention crash", err)
					}
				}
				if err = os.WriteFile(script, original, 0600); err != nil {
					t.Fatal(err)
				}
				claim := startDeferredCLIClaim(t, ctx, f, first.ref)
				claim.finish(t)
			} else {
				if unknown {
					barrier := filepath.Join(f.root, "hold-init")
					if err = os.WriteFile(barrier, nil, 0600); err != nil {
						t.Fatal(err)
					}
					delayed := bytes.ReplaceAll(original, []byte("  emit({'type':'system','subtype':'init','session_id':'process-session'})"), []byte("  while os.path.exists(os.path.join(os.environ['PMX_TEST_PROCESS_ROOT'],'hold-init')): __import__('time').sleep(.01)\n  emit({'type':'system','subtype':'init','session_id':'process-session'})"))
					if err = os.WriteFile(script, delayed, 0600); err != nil {
						t.Fatal(err)
					}
				}
				run = deferredRelaunchCLI(t, ctx, f, first.ref, "--model", "prompt-model", "--", "prompt first")
				deferredReady(t, ctx, f, first.ref)
				run.finish(t)
				if retained, err := c.readDeferredLaunch(uid); err != nil || retained != nil {
					t.Fatal("B1 intent not consumed", err)
				}
			}
			settled, err := c.readDeferredInput(uid)
			if err != nil || settled == nil || settled.Phase != "settled" || settled.Text != "" || settled.Success || settled.Unknown != unknown || slices.Contains(deferredWireTexts(t, f), "never replay") {
				t.Fatal("B1 raw slot settlement", err)
			}
			if unknown {
				var captured deferredLaunchRecord
				if found, err := readDeferredState(filepath.Join(f.root, "captured-intent"), &captured); err != nil || !found {
					t.Fatal("intent not captured", err)
				}
				if err = writeDeferredState(c.deferredStatePath("deferred-launches", uid), &captured); err != nil {
					t.Fatal(err)
				}
				wire := deferredWireTexts(t, f)
				children := len(deferredArgv(t, f))
				recovery = startDeferredCLIClaim(t, ctx, f, first.ref)
				recovery.finish(t)
				if !reflect.DeepEqual(wire, deferredWireTexts(t, f)) || children != len(deferredArgv(t, f)) {
					t.Fatal("unknown intent replayed first frame")
				}
				if err = os.WriteFile(script, original, 0600); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"agent", "resume", first.ref, "--", "ordinary first"}
			if !failed && !unknown {
				args = []string{"agent", "resume", first.ref, "--model", "ordinary-model", "--", "ordinary first"}
			}
			resumed := startResumeCLIInvocation(t, ctx, f, args)
			deferredReady(t, ctx, f, first.ref)
			resumed.shutdown(t)
			run = deferredRelaunchCLI(t, ctx, f, first.ref, "--model", "final-model")
			run.finish(t)
			if retained, err := c.readDeferredLaunch(uid); err != nil || retained == nil || retained.Model != "final-model" {
				t.Fatal("subsequent empty relaunch", err)
			}
		})
	}
}

func TestDeferredClaudeAbsentLaunchReservationRaceActualCLI(t *testing.T) {
	testDeferredClaudeAbsentLaunchReservationRaceActualCLI(t, false)
}

func TestDeferredClaudeAbsentLaunchReservationRaceHostReadDeadlineActualCLI(t *testing.T) {
	product := os.Getenv("PMX_TEST_CLAUDE_REGRESSION_CLI")
	if product == "" {
		if os.Getenv("PMX_TEST_CLI") != "" {
			t.Fatal("missing tagged regression CLI; refusing ordinary CLI fallback")
		}
		t.Skip("set PMX_TEST_CLAUDE_REGRESSION_CLI to the tagged copied CLI")
	}
	t.Setenv("PMX_TEST_CLI", product)
	testDeferredClaudeAbsentLaunchReservationRaceActualCLI(t, true)
}

func testDeferredClaudeAbsentLaunchReservationRaceActualCLI(t *testing.T, regression bool) {
	f := deferredRelaunchFixture(t)
	var control string
	if regression {
		control = filepath.Join(f.root, "regression")
		if err := os.Mkdir(control, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PMX_TEST_CLAUDE_REGRESSION_CONTROL", control)
		// Provider environments drop PMX_ variables; export the private control
		// from this fixture wrapper, as in the existing detached regression.
		wrapper := filepath.Join(f.root, "claude")
		raw, err := os.ReadFile(wrapper)
		if err != nil {
			t.Fatal(err)
		}
		raw = bytes.Replace(raw, []byte("#!/bin/sh\n"), fmt.Appendf(nil, "#!/bin/sh\nexport PMX_TEST_CLAUDE_REGRESSION_CONTROL=%q\n", control), 1)
		if err := os.WriteFile(wrapper, raw, 0700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "initial"))
	deferredReady(t, ctx, f, first.ref)
	first.shutdown(t)
	c := deferredFixtureCommand(f)
	uid := strings.TrimPrefix(first.ref, "uid:")
	candidate := deferredCandidate(t, f, first.ref)
	_, observed, err := c.prepareDeferredLaunch(ctx, candidate, processAgentResumeOptions{Agent: selector.Ref{Kind: coremetadata.KindAgent, UID: uid}})
	if err != nil || observed != nil {
		t.Fatal("initial launch was not absent", err)
	}
	// The other empty relaunch commits the same recipe, then releases its claim.
	run := deferredRelaunchCLI(t, ctx, f, first.ref)
	run.finish(t)
	current := deferredCandidate(t, f, first.ref)
	if !processResumeRecordEqual(&current.Record, &candidate.Record) || !reflect.DeepEqual(current.Agent.Spec, candidate.Agent.Spec) || !reflect.DeepEqual(current.Agent.Metadata.Annotations, candidate.Agent.Metadata.Annotations) {
		t.Fatal("fixture did not retain the exact source and recipe")
	}
	launchPath := c.deferredStatePath("deferred-launches", uid)
	registryBefore, err := os.ReadFile(f.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	launchBefore, err := os.ReadFile(launchPath)
	if err != nil {
		t.Fatal(err)
	}
	inputPath := c.deferredStatePath("deferred-inputs", uid)
	if _, err = os.Stat(inputPath); !os.IsNotExist(err) {
		t.Fatal("unexpected input before reservation", err)
	}
	children := len(deferredArgv(t, f))
	state := filepath.Dir(filepath.Dir(f.store.Path()))
	c.rebind = &agentRebinder{create: &createCommand{store: &resourceStore{stateDir: func() (string, error) { return state, nil }, mutator: intmetadata.DefaultMutator, update: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		reg, _, err := f.store.UpdateConvergent(fn)
		return reg, err
	}}}}
	binding := processSchemaBinding(candidate.Record.Binding)
	binding.Host, binding.Generation, binding.Operation = "race-host", "race-generation", "race-operation"
	for _, resume := range []bool{false, true} {
		for _, explicitNil := range []bool{false, true} {
			var prepared []*deferredLaunchRecord
			if explicitNil {
				prepared = append(prepared, nil)
			}
			if resume {
				err = c.reserveProcessResume(ctx, candidate, agentSettingsLaunch{}, binding, nil, prepared...)
			} else {
				err = c.reserveProcessRelaunch(ctx, candidate, agentSettingsLaunch{}, agentGuidanceLaunch{}, projectLinksLaunch{}, binding, prepared...)
			}
			if !errors.Is(err, processhost.ErrResumeRefused) || !strings.HasPrefix(err.Error(), processResumeRefused+":") {
				t.Fatalf("nil launch reservation accepted resume=%t explicitNil=%t: %v", resume, explicitNil, err)
			}
			registryAfter, e := os.ReadFile(f.store.Path())
			if e != nil {
				t.Fatal(e)
			}
			launchAfter, e := os.ReadFile(launchPath)
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(registryBefore, registryAfter) || !bytes.Equal(launchBefore, launchAfter) || len(deferredArgv(t, f)) != children {
				t.Fatal("nil launch refusal changed state or child count")
			}
			if _, e = os.Stat(inputPath); !os.IsNotExist(e) {
				t.Fatal("refusal created an input slot", e)
			}
		}
	}
	frozen, err := c.readDeferredLaunch(uid)
	if err != nil || frozen == nil {
		t.Fatal("saved launch missing after refusal", err)
	}
	resumed := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", "race recovery"})
	var beforeObserve []func(coremetadata.Registry)
	armed := false
	if regression {
		beforeObserve = append(beforeObserve, func(reg coremetadata.Registry) {
			if armed {
				return
			}
			pane, ambiguous := processResumePane(reg, uid)
			if ambiguous || pane == nil || pane.Status.Activation.Process == nil || pane.Status.ProcessSession == nil {
				return
			}
			activation := pane.Status.Activation.Process
			current, provider, valid := reg.CurrentProcessActivation(activation.Binding)
			if !valid || provider != aiModeClaude || pane.Status.ProcessSession.Binding != activation.Binding || pane.Status.ProcessSession.SessionID != candidate.Record.SessionID {
				return
			}
			// Reuse this iteration's Registry snapshot: no extra read, RPC,
			// poll, or timeout. The existing host gate proves Ready selection.
			proof := claudeProcessProof{Binding: processSchemaBinding(activation.Binding), Process: current.Child, HostProcess: current.HostProcess, Session: pane.Status.ProcessSession.SessionID}
			exerciseClaudeHostReadDeadlineCLI(t, ctx, f, proof, control)
			armed = true
		})
	}
	deferredReady(t, ctx, f, first.ref, beforeObserve...)
	if regression && !armed {
		t.Fatal("native host read deadline barrier was not completed")
	}
	resumed.shutdown(t)
	if retained, err := c.readDeferredLaunch(uid); err != nil || retained != nil {
		t.Fatal("race refusal left an unusable deferred launch", err)
	}
	argv := deferredArgv(t, f)
	want := append(append([]string{}, frozen.Command.Args...), "--resume", candidate.Record.SessionID)
	texts := deferredWireTexts(t, f)
	if !reflect.DeepEqual(argv[len(argv)-1][1:], want) || texts[len(texts)-1] != "race recovery" {
		t.Fatal("post-refusal frozen arguments or raw input changed")
	}
}

func TestDeferredClaudePermissionModeValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		valid bool
	}{
		{"auto", []string{"--model", "m", "--permission-mode", "auto", "--print"}, true},
		{"equals-auto", []string{"--permission-mode=auto", "--print"}, true},
		{"unset", []string{"--print"}, false},
		{"other", []string{"--permission-mode", "default"}, false},
		{"duplicate", []string{"--permission-mode", "auto", "--permission-mode=auto"}, false},
		{"invalid", []string{"--permission-mode=invalid"}, false},
		{"empty", []string{"--permission-mode="}, false},
		{"missing-value", []string{"--print", "--permission-mode"}, false},
		{"flag-as-value", []string{"--permission-mode", "--print"}, false},
		{"after-delimiter", []string{"--", "--permission-mode", "auto"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.args)
			err := validateDeferredClaudePermissionMode(tc.args)
			if (err == nil) != tc.valid || !reflect.DeepEqual(before, tc.args) {
				t.Fatalf("validation=%v argv=%q", err, tc.args)
			}
			if !tc.valid && (!errors.Is(err, processhost.ErrResumeRefused) || !strings.Contains(err.Error(), "use agent relaunch")) {
				t.Fatalf("typed recovery absent: %v", err)
			}
		})
	}
}

func TestDeferredClaudeLegacyModeRefusalAndReplacementActualCLI(t *testing.T) {
	for _, mode := range [][]string{nil, {"--permission-mode", "default"}, {"--permission-mode", "auto", "--permission-mode=auto"}, {"--permission-mode=invalid"}, {"--permission-mode"}} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			f := deferredRelaunchFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "initial"))
			deferredReady(t, ctx, f, first.ref)
			first.shutdown(t)
			run := deferredRelaunchCLI(t, ctx, f, first.ref, "--model", "prepared-model")
			run.finish(t)
			c := deferredFixtureCommand(f)
			uid := strings.TrimPrefix(first.ref, "uid:")
			legacy, err := c.readDeferredLaunch(uid)
			if err != nil || legacy == nil {
				t.Fatal("prepared launch", err)
			}
			index := slices.Index(legacy.Command.Args, "--permission-mode")
			if index < 0 {
				t.Fatal("new recipe has no auto")
			}
			legacy.Command.Args = append(slices.Clone(legacy.Command.Args[:index]), legacy.Command.Args[index+2:]...)
			legacy.Command.Args = append(legacy.Command.Args, mode...)
			launchPath := c.deferredStatePath("deferred-launches", uid)
			if err = writeDeferredState(launchPath, legacy); err != nil {
				t.Fatal(err)
			}
			// Leave the old recipe unapplied: refusal must precede recovery CAS.
			_, _, err = f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
				a, _ := reg.Agent(uid)
				a.Spec, a.Metadata.Annotations = legacy.OldSpec, legacy.OldAnnotations
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			identity, _, err := localipc.Process(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			input := &deferredUserInput{Version: 1, Operation: "legacy-pending", Nonce: "old-nonce", Session: legacy.Retired.SessionID, Recipe: deferredLaunchDigest(legacy), Phase: "pending", Text: "preserve pending", Binding: legacy.Retired.Binding, Process: identity, Deadline: time.Now().Add(time.Minute)}
			if err = c.writeDeferredInput(input); err != nil {
				t.Fatal(err)
			}
			inputPath := c.deferredStatePath("deferred-inputs", uid)
			beforeRegistry, _ := os.ReadFile(f.store.Path())
			beforeLaunch, _ := os.ReadFile(launchPath)
			beforeInput, _ := os.ReadFile(inputPath)
			for _, flags := range [][]string{{"--wait-for-peer"}, {"--", "must not write"}} {
				out, err := exec.CommandContext(ctx, f.binary, append([]string{"agent", "resume", first.ref}, flags...)...).CombinedOutput()
				afterRegistry, _ := os.ReadFile(f.store.Path())
				afterLaunch, _ := os.ReadFile(launchPath)
				afterInput, _ := os.ReadFile(inputPath)
				if err == nil || !bytes.Contains(out, []byte("process-resume-refused")) || !bytes.Contains(out, []byte("use agent relaunch")) || !bytes.Equal(beforeRegistry, afterRegistry) || !bytes.Equal(beforeLaunch, afterLaunch) || !bytes.Equal(beforeInput, afterInput) || len(deferredArgv(t, f)) != 1 {
					t.Fatalf("legacy refusal mutated state/spawned: %v %s", err, out)
				}
				if _, err = os.Stat(c.deferredClaimPath(uid)); !os.IsNotExist(err) {
					t.Fatal("refusal wrote claim", err)
				}
			}
			// Restore the applied fixture source before testing explicit replacement;
			// the refusals above deliberately used an unapplied crash projection.
			_, _, err = f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
				a, _ := reg.Agent(uid)
				a.Spec, a.Metadata.Annotations = legacy.NewSpec, legacy.NewAnnotations
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			// A new-auto intent can still restore a legacy Previous after a crash.
			// Ordinary replay must refuse before that rollback writes any state.
			priorCopy, intent := *legacy, *legacy
			intent.Command.Args = append(slices.Clone(legacy.Command.Args[:len(legacy.Command.Args)-len(mode)]), "--permission-mode", "auto")
			intent.OldSpec, intent.OldAnnotations = legacy.NewSpec, legacy.NewAnnotations
			target := legacy.Retired.Binding
			target.HostInstanceID, target.Generation, target.OperationID = "intent-host", "intent-generation", "intent-operation"
			intent.Previous, intent.Attempt = &priorCopy, &deferredLaunchAttempt{Source: *legacy.Retired.Clone(), Target: target}
			if err = writeDeferredState(launchPath, &intent); err != nil {
				t.Fatal(err)
			}
			beforeRegistry, _ = os.ReadFile(f.store.Path())
			beforeLaunch, _ = os.ReadFile(launchPath)
			for _, flags := range [][]string{{"--wait-for-peer"}, {"--", "must not recover legacy"}} {
				out, err := exec.CommandContext(ctx, f.binary, append([]string{"agent", "resume", first.ref}, flags...)...).CombinedOutput()
				afterRegistry, _ := os.ReadFile(f.store.Path())
				afterLaunch, _ := os.ReadFile(launchPath)
				afterInput, _ := os.ReadFile(inputPath)
				if err == nil || !bytes.Contains(out, []byte("use agent relaunch")) || !bytes.Equal(beforeRegistry, afterRegistry) || !bytes.Equal(beforeLaunch, afterLaunch) || !bytes.Equal(beforeInput, afterInput) || len(deferredArgv(t, f)) != 1 {
					t.Fatalf("Previous legacy recovery wrote state/spawned: %v %s", err, out)
				}
				if _, err = os.Stat(c.deferredClaimPath(uid)); !os.IsNotExist(err) {
					t.Fatal("intent refusal wrote claim", err)
				}
			}
			// Explicit replacement alone may recover the intent and replace the
			// exact prepared recipe through the existing Previous/digest/CAS path.
			run = deferredRelaunchCLI(t, ctx, f, first.ref, "--model", "replacement-model")
			next, err := c.readDeferredLaunch(uid)
			if err != nil || next == nil || next.Model != "replacement-model" || permissionModes(next.Command.Args) != "auto" || deferredLaunchDigest(next) == deferredLaunchDigest(legacy) {
				t.Fatal("explicit replacement", err)
			}
			run.finish(t)
			resumed := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", "replacement first"})
			deferredReady(t, ctx, f, first.ref)
			resumed.shutdown(t)
			argv := deferredArgv(t, f)
			want := append(slices.Clone(next.Command.Args), "--resume", legacy.Retired.SessionID)
			if len(argv) != 2 || !reflect.DeepEqual(argv[1][1:], want) || permissionModes(argv[1]) != "auto" {
				t.Fatal("replacement did not replay auto recipe", argv)
			}
			if retained, err := c.readDeferredLaunch(uid); err != nil || retained != nil {
				t.Fatal("replacement not consumed", err)
			}
		})
	}
}

// These diagnostics only run for the two explicitly selected failure branches.
// They do not identify the writer PID or promote cleanup observations to causes.
func logClaudeFailureBuffer(t *testing.T, phase string, data []byte) {
	t.Helper()
	class := "unclassified"
	if len(data) > 64*1024 {
		class = "over-limit"
	} else {
		for _, entry := range []struct{ text, class string }{
			{"Waiting for first input; the provider has not started.", "initial-wait-notice"},
			{"claude registration lease is stale or unavailable", "registration-lease-refusal"},
			{"process-resume-owned", "resume-owned-refusal"},
		} {
			if bytes.Contains(data, []byte(entry.text)) {
				class = entry.class
			}
		}
	}
	t.Logf("claude failure buffer phase=%s bytes=%d sha256=%x class=%s", phase, len(data), sha256.Sum256(data), class)
}

func logClaudeFailureRegistration(t *testing.T, path string, before os.FileInfo, agent, pane string, primaryAt, cleanupAt time.Time) {
	t.Helper()
	closed := func() { t.Log("claude registration evidence state=closed-unconfirmed actor-pid=unproven best-effort=true") }
	pre, err := os.Lstat(path)
	private := func(info os.FileInfo) bool {
		if info == nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		return ok && stat.Uid == uint32(os.Getuid())
	}
	if err != nil || !private(before) || !private(pre) || !os.SameFile(before, pre) {
		closed()
		return
	}
	data, ok := readSDKResumeRefusalJournal(path, before)
	post, err := os.Lstat(path)
	if !ok || err != nil || !private(post) || !os.SameFile(pre, post) || pre.Size() != int64(len(data)) || post.Size() != pre.Size() || !pre.ModTime().Equal(post.ModTime()) {
		closed()
		return
	}
	if len(data) != 0 && data[len(data)-1] != '\n' {
		closed()
		return
	}
	// Closed recorder source/reason pairs, including refusals. No event text
	// or unknown field value can become diagnostic output.
	allowed := map[string]bool{
		"hook:hook-arguments-present":         true,
		"hook:registry-path-invalid":          true,
		"hook:registry-unreadable":            true,
		"helper:registry-unreadable":          true,
		"hook:hook-input-unreadable":          true,
		"hook:payload-not-session-start":      true,
		"hook:pane-binding-mismatch":          true,
		"hook:agent-mismatch":                 true,
		"hook:provider-process-mismatch":      true,
		"hook:messaging-credential-invalid":   true,
		"hook:session-id-embeds-credential":   true,
		"hook:messaging-socket-unavailable":   true,
		"helper:messaging-socket-unavailable": true,
		"hook:nonce-unavailable":              true,
		"hook:authority-invalid":              true,
		"helper:authority-invalid":            true,
		"hook:hook-identity-unavailable":      true,
		"hook:reply-tool-policy-unavailable":  true,
		"hook:helper-executable-unavailable":  true,
		"hook:helper-bootstrap-unavailable":   true,
		"hook:helper-ack-pipe-unavailable":    true,
		"hook:helper-start-failed":            true,
		"hook:helper-admission-unconfirmed":   true,
		"helper:helper-arguments-invalid":     true,
		"helper:helper-ack-missing":           true,
		"helper:helper-ack-not-pipe":          true,
		"helper:helper-input-unreadable":      true,
		"helper:producer-mismatch":            true,
		"helper:bootstrap-invalid":            true,
		"helper:helper-identity-unavailable":  true,
		"helper:lease-unavailable":            true,
		"helper:coordination-unavailable":     true,
		"helper:lease-owner-unavailable":      true,
		"helper:provider-process-gone":        true,
		"helper:claim-refused-activation":     true,
		"helper:claim-refused-competing":      true,
		"helper:claim-refused-newer":          true,
		"helper:lock-timeout":                 true,
		"helper:lock-acquire-failed":          true,
		"helper:registry-degraded":            true,
		"helper:registry-write-failed":        true,
		"helper:route-mismatch":               true,
		"helper:dialogue-broker-unavailable":  true,
		"helper:reply-tool-gate-unavailable":  true,
		"helper:stale-before-ack":             true,
		"helper:ready":                        true,
		"helper:ended-context-done":           true,
		"helper:ended-not-current":            true,
		"helper:ended-accept-failed":          true,
	}
	type observation struct {
		at time.Time
		source, code, result, run string
	}
	var observations []observation
	count := 0
	for _, line := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		if len(data) == 0 {
			break
		}
		// Validate complete objects and reject duplicate/null fields before
		// decoding typed values. Parser errors and original values stay private.
		keys := map[string]bool{}
		fields := json.NewDecoder(bytes.NewReader(line))
		opening, err := fields.Token()
		if err != nil || opening != json.Delim('{') {
			closed()
			return
		}
		for fields.More() {
			key, err := fields.Token()
			name, named := key.(string)
			var value json.RawMessage
			if err != nil || !named || keys[name] || fields.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				closed()
				return
			}
			keys[name] = true
		}
		ending, err := fields.Token()
		if err != nil || ending != json.Delim('}') || fields.Decode(new(any)) != io.EOF {
			closed()
			return
		}
		var event diagnostics.Event
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&event) != nil || decoder.Decode(new(any)) != io.EOF {
			closed()
			return
		}
		count++
		if event.Event != "agent.claude.registration" || event.AgentUID != agent || event.PaneUID != pane {
			continue
		}
		for key := range keys {
			switch key {
			case "at", "level", "component", "event", "result", "duration_ms", "run_id", "version", "mux_backend", "source", "code", "agent_uid", "pane_uid", "kind":
			default:
				closed()
				return
			}
		}
		for _, key := range []string{"at", "level", "component", "event", "result", "duration_ms", "run_id", "version", "mux_backend", "source", "code", "agent_uid", "pane_uid"} {
			if !keys[key] {
				closed()
				return
			}
		}
		reason, prefix := strings.CutPrefix(event.Code, "claude.registration.")
		at, err := time.Parse(time.RFC3339Nano, event.At)
		refused := diagnostics.ClaudeRegistrationReason(reason).Refusal()
		shape := event.Level == "info" && event.Result == "success" && event.Kind == ""
		if refused {
			shape = event.Level == "error" && event.Result == "error" && event.Kind == "runtime"
		}
		if !prefix || !allowed[event.Source+":"+reason] || err != nil || !shape || event.Component != "agent" || event.DurationMS < 0 || event.Command != "" || event.Subcommand != "" || event.Message != "" {
			closed()
			return
		}
		observations = append(observations, observation{at, event.Source, event.Code, event.Result, fmt.Sprintf("%x", sha256.Sum256([]byte(event.RunID)))})
	}
	t.Logf("claude registration evidence state=complete records=%d matched=%d actor-pid=unproven best-effort=true primary-at=%s cleanup-at=%s", count, len(observations), primaryAt.UTC().Format(time.RFC3339Nano), cleanupAt.UTC().Format(time.RFC3339Nano))
	for _, event := range observations {
		phase := "before-primary"
		if !event.at.Before(primaryAt) {
			phase = "after-primary-before-cleanup"
		}
		if !cleanupAt.IsZero() && !event.at.Before(cleanupAt) {
			phase = "cleanup-or-later"
		}
		t.Logf("claude registration evidence at=%s source=%s code=%s result=%s run-sha256=%s phase=%s", event.at.UTC().Format(time.RFC3339Nano), event.source, event.code, event.result, event.run, phase)
	}
}
