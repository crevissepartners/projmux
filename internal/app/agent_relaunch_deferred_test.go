package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
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
	t.Cleanup(func() { _ = os.RemoveAll(root) })
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
	if err = os.WriteFile(filepath.Join(root, "claude"), []byte("#!/bin/sh\nexec python3 -u "+fmt.Sprintf("%q", script)+" \"$@\"\n"), 0700); err != nil {
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
		_ = run.input.Close()
		if !run.done {
			_ = run.cmd.Process.Kill()
			_ = run.cmd.Wait()
		}
	})
	line, err := run.output.ReadString('\n')
	if err != nil || line != "agent "+ref+" pane uid:"+deferredCandidate(t, f, ref).Record.Binding.PaneUID+" runtime=process foreground=claimed\n" {
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
			awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.SessionID != "" && r.TurnID == "" })
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

func TestDeferredClaudeRelaunchChangedSnapshotRefusedActualCLI(t *testing.T) {
	f := deferredRelaunchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "initial"))
	awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.SessionID != "" && r.TurnID == "" })
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
			awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.SessionID != "" && r.TurnID == "" })
			first.shutdown(t)
			old := deferredCandidate(t, f, first.ref)
			run := deferredRelaunchCLI(t, ctx, f, first.ref, "--profile", "delayed")
			frozen, err := deferredFixtureCommand(f).readDeferredLaunch(old.Agent.Metadata.UID)
			if err != nil || frozen == nil {
				t.Fatal(err)
			}
			if len(frozen.Files) < 1 {
				t.Fatal("permissions snapshot proof missing")
			}
			// Changes to named profiles must not replace the committed recipe.
			writeCodexProfileFile(t, profiles, "delayed", "model = \"changed-model\"\neffort = \"low\"\n[permissions]\nallow = [\"Bash\"]\n")
			source := startResumeCLIInvocation(t, ctx, f, f.args("--name", "source", "--profile", "none", "--", "source"))
			awaitProcessResumeRecord(t, ctx, f, source.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.TurnID == "" && r.SessionID != "" })
			if err = run.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
				t.Fatal(err)
			}
			record := deferredPeer(t, ctx, f, source.ref, first.ref, "delayed-peer", "peer exact\ncontent")
			expected, err := deferredPeerText(record)
			if err != nil {
				t.Fatal(err)
			}
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
				t.Fatalf("wake %v %s", err, run.stderr.String())
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
			if current.SessionID != old.Record.SessionID || current.Binding.PaneUID != old.Record.Binding.PaneUID {
				t.Fatal("conversation changed")
			}
			run.finish(t)
			source.shutdown(t)
			argv := deferredArgv(t, f)
			last := argv[len(argv)-1][1:]
			want := append(append([]string{}, frozen.Command.Args...), "--resume", old.Record.SessionID)
			if !reflect.DeepEqual(last, want) {
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
	awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.SessionID != "" && r.TurnID == "" })
	first.shutdown(t)
	run := deferredRelaunchCLI(t, ctx, f, first.ref, "--model", "new-model", "--effort", "high")
	uid := strings.TrimPrefix(first.ref, "uid:")
	c := deferredFixtureCommand(f)
	frozen, err := c.readDeferredLaunch(uid)
	if err != nil || frozen == nil {
		t.Fatal("recipe", err)
	}
	source := startResumeCLIInvocation(t, ctx, f, f.args("--name", "source", "--profile", "none", "--", "source"))
	awaitProcessResumeRecord(t, ctx, f, source.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.SessionID != "" && r.TurnID == "" })
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
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--profile", "none", "--", "initial"))
			awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.SessionID != "" && r.TurnID == "" })
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
	awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.SessionID != "" && r.TurnID == "" })
	first.shutdown(t)
	run := deferredRelaunchCLI(t, ctx, f, first.ref, "--model", "new-model")
	if err := run.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
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
	out, err := exec.CommandContext(ctx, f.binary, "agent", "turn", "start", first.ref, "--", "losing input").CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("busy")) {
		t.Fatalf("second input accepted %v %s", err, out)
	}
	if err = caller.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err = caller.Wait(); err == nil {
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
