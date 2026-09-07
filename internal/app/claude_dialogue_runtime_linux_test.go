package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"golang.org/x/sys/unix"
)

func dialogueRuntimeProfile(t *testing.T) (superviseSpec, string, string) {
	t.Helper()
	imageGate, _ := newClaudeReplyToolTestGate(t)
	candidate := imageGate.policy.Executable
	spec := superviseSpec{PaneUID: "pane-owned", AgentUID: "agent-owned", Generation: "gen-owned", RegistryPath: intmetadata.PathFor(filepath.Join(t.TempDir(), "state")), DialogueReplyOnly: true}
	profile, err := createClaudeDialogueProfile(spec, candidate)
	if err != nil {
		t.Fatal(err)
	}
	return spec, profile, candidate
}

func TestClaudeDialogueProfilePinsFilesAliasAndInvocationMode(t *testing.T) {
	spec, root, candidate := dialogueRuntimeProfile(t)
	profile, err := readClaudeDialogueProfile(root, candidate)
	if err != nil || profile.Generation != spec.Generation {
		t.Fatal("profile read failed", err)
	}
	args, selected := claudeDialoguePrefixArgs(filepath.Join(root, claudeDialoguePrefixName), root, []string{"opaque marker carrier"})
	if !selected || len(args) != 4 || args[1] != "claude-reply-tool" || args[3] != "opaque marker carrier" {
		t.Fatalf("prefix dispatch: %v", args)
	}
	args, selected = claudeDialoguePrefixArgs(filepath.Join(root, claudeDialoguePrefixName), root, []string{"one", "two"})
	if !selected || len(args) != 3 {
		t.Fatal("malformed alias escaped guarded route")
	}
	files, err := profile.files()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(files["settings.json"], []byte("claude-message-reply")) || !bytes.Contains(files["settings.json"], []byte("agent-hook")) {
		t.Fatal("profile lost lifecycle or enabled Stop auto reply")
	}
	for _, name := range []string{"settings.json", "mcp.json", "profile.json"} {
		path := filepath.Join(root, name)
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(original, ' '), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readClaudeDialogueProfile(root, candidate); err == nil {
			t.Fatal("modified profile admitted")
		}
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(root, "mcp.json")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "mcp.json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readClaudeDialogueProfile(root, candidate); err == nil {
		t.Fatal("FIFO profile admitted")
	}
}

func TestClaudeDialogueOrdinaryEnvironmentKeepsOfficialPrefixWithoutInheritingOptIn(t *testing.T) {
	env := []string{"CLAUDE_CODE_SHELL_PREFIX=ordinary-prefix", internalClaudeReplyGuardEnv + "=1", internalClaudeDialogueProfileEnv + "=/old", "KEEP=value"}
	got := withoutClaudeDialoguePolicy(env)
	if strings.Join(got, ";") != "CLAUDE_CODE_SHELL_PREFIX=ordinary-prefix;KEEP=value" {
		t.Fatalf("ordinary environment changed: %v", got)
	}
	guarded := claudeDialogueNativeEnvironment(append(env, "CLAUDE_CODE_MESSAGING_TOKEN=fixture", "CLAUDE_CODE_MESSAGING_SOCKET=/fixture"), "/owned/profile")
	all := strings.Join(guarded, ";")
	if strings.Count(all, "CLAUDE_CODE_SHELL_PREFIX=") != 1 || strings.Contains(all, "ordinary-prefix") || strings.Contains(all, "CLAUDE_CODE_MESSAGING") || !strings.Contains(all, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1") {
		t.Fatal("native policy was not isolated")
	}
}

func ownedDialogueWriter(t *testing.T) (*exec.Cmd, io.WriteCloser, coremetadata.ProcessIdentity) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "printf R; read -r release")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = cmd.Wait() })
	var ready [1]byte
	if _, err := io.ReadFull(output, ready[:]); err != nil {
		t.Fatal(err)
	}
	birth, err := claudeDialogueProcessBirth(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return cmd, input, birth
}

func TestClaudeDialogueCleanupWaitsExactWriterAndHandlesZombieWithoutSignals(t *testing.T) {
	spec, root, _ := dialogueRuntimeProfile(t)
	cmd, input, birth := ownedDialogueWriter(t)
	raw, _ := json.Marshal(birth)
	if err := os.WriteFile(filepath.Join(root, "observer.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := waitClaudeDialogueObserverExit(birth, time.Millisecond); err == nil {
		t.Fatal("live writer reported exited")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("root removed before writer exit")
	}
	replaced := birth
	replaced.Start += "-replaced"
	if err := waitClaudeDialogueObserverExit(replaced, time.Second); err == nil {
		t.Fatal("replaced birth admitted")
	}
	foreign := birth
	foreign.OwnerUID++
	if err := waitClaudeDialogueObserverExit(foreign, time.Second); err == nil {
		t.Fatal("foreign owner admitted")
	}
	if _, err := io.WriteString(input, "release\n"); err != nil {
		t.Fatal(err)
	}
	var status unix.Siginfo
	if err := unix.Waitid(unix.P_PID, cmd.Process.Pid, &status, unix.WEXITED|unix.WNOWAIT, nil); err != nil {
		t.Fatal(err)
	}
	if err := waitClaudeDialogueObserverExit(birth, time.Second); err != nil {
		t.Fatal("zombie exit not proven", err)
	}
	if err := cleanupClaudeDialogueProfile(spec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("owned profile not automatically removed")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeDialoguePartialSetupAndUnknownFilesRetainExactRoot(t *testing.T) {
	_, root, _ := dialogueRuntimeProfile(t)
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	// Before observer Start/descriptor publication no writer exists; setup owns
	// the exact inode and can remove its finite files without an observer receipt.
	if err := removeClaudeDialogueProfileFiles(root, info, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("partial setup leaked profile")
	}
	_, root, _ = dialogueRuntimeProfile(t)
	info, _ = os.Lstat(root)
	if err := os.WriteFile(filepath.Join(root, "unknown-evidence"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeClaudeDialogueProfileFiles(root, info, true); err == nil {
		t.Fatal("unknown profile removed")
	}
	if _, err := os.Stat(filepath.Join(root, "unknown-evidence")); err != nil {
		t.Fatal("unknown evidence lost")
	}
}

func TestClaudeDialoguePipeEOFClosesInputButWaitsCurrentTurn(t *testing.T) {
	output, writer, _ := os.Pipe()
	diagnostics, stderrWriter, _ := os.Pipe()
	terminal, terminalWriter, _ := os.Pipe()
	input, keepalive, _ := os.Pipe()
	for _, file := range []*os.File{output, writer, diagnostics, stderrWriter, terminal, terminalWriter, input, keepalive} {
		defer file.Close()
	}
	observed := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- consumeClaudeDialoguePipes(int32(output.Fd()), int32(diagnostics.Fd()), int32(terminal.Fd()), keepalive, func(line []byte) error { observed <- string(line); return nil })
	}()
	if _, err := terminalWriter.Write([]byte("NEVER MODEL INPUT\n")); err != nil {
		t.Fatal(err)
	}
	_ = terminalWriter.Close()
	if data, err := io.ReadAll(input); err != nil || len(data) != 0 {
		t.Fatal("terminal content forwarded")
	}
	select {
	case err := <-done:
		t.Fatal("observer exited before current turn", err)
	default:
	}
	if _, err := writer.Write([]byte("current-turn-complete\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-observed:
		if got != "current-turn-complete" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("current turn lost")
	}
	_ = writer.Close()
	_ = stderrWriter.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("EOF did not stop observer")
	}
}

func TestClaudeDialoguePipeDiagnosticLossFailsClosedWithoutBusyLoop(t *testing.T) {
	for _, problem := range []string{"stderr-eof", "stderr-bytes", "invalid-fd"} {
		t.Run(problem, func(t *testing.T) {
			output, writer, _ := os.Pipe()
			diagnostics, stderrWriter, _ := os.Pipe()
			terminal, terminalWriter, _ := os.Pipe()
			unusedInput, keepalive, _ := os.Pipe()
			for _, file := range []*os.File{output, writer, diagnostics, stderrWriter, terminal, terminalWriter, unusedInput, keepalive} {
				defer file.Close()
			}
			diagnosticFD := int32(diagnostics.Fd())
			switch problem {
			case "stderr-eof":
				_ = stderrWriter.Close()
			case "stderr-bytes":
				_, _ = stderrWriter.Write([]byte("not retained"))
			case "invalid-fd":
				_ = diagnostics.Close()
			}
			done := make(chan error, 1)
			go func() {
				done <- consumeClaudeDialoguePipes(int32(output.Fd()), diagnosticFD, int32(terminal.Fd()), keepalive, func([]byte) error { t.Error("unsafe output published"); return nil })
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("lost diagnostic stream admitted")
				}
			case <-time.After(time.Second):
				t.Fatal("diagnostic loss busy loop")
			}
		})
	}
}

func currentDialogueProfileFixture(t *testing.T) (*claudeCoordinationTestFixture, *claudeReplyToolGate, string) {
	t.Helper()
	fixture := newClaudeCoordinationTestFixture(t)
	imageGate, _ := newClaudeReplyToolTestGate(t)
	candidate := imageGate.policy.Executable
	spec := superviseSpec{AgentUID: fixture.route.AgentUID, PaneUID: fixture.route.PaneUID, Generation: fixture.route.Generation, RegistryPath: fixture.registryPath, DialogueReplyOnly: true}
	root, err := createClaudeDialogueProfile(spec, candidate)
	if err != nil {
		t.Fatal(err)
	}
	peer, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(peer)
	if err := os.WriteFile(filepath.Join(root, "observer.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	gate, err := newClaudeReplyToolGate(claudeReplyToolPolicy{Executable: candidate, Directory: cwd, ProfileDir: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	fixture.server.tool = gate
	fixture.server.profile = &claudeDialogueObservedState{peer: peer}
	for _, observation := range []*claudeDialogueObservation{{Kind: "init", SessionID: fixture.sessionID, ProviderVersion: claudeFrozenFrameProviderVersion, Tools: []string{"Bash"}, MCPServers: []string{}, Plugins: []string{}}, {Kind: "ready", SessionID: fixture.sessionID}} {
		response := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion, Target: fixture.target, Operation: "profile-observe", Observation: observation})
		if response.Kind != "profile-observed" {
			t.Fatalf("observation: %s", response.Kind)
		}
	}
	evidence, err := readCurrentClaudeDialogueEvidence(context.Background(), fixture.registryPath, fixture.route)
	if err != nil || !evidence.validExplicit(time.Now(), fixture.route) {
		t.Fatal("public optional evidence UDS path unavailable", err)
	}
	return fixture, gate, root
}

func TestClaudeDialogueCurrentHelperEvidenceAndObserverLossPrecludeEffects(t *testing.T) {
	for _, stage := range []string{"tool-prepare", "tool-consume", "explicit-reply", "submit"} {
		t.Run(stage, func(t *testing.T) {
			fixture, gate, root := currentDialogueProfileFixture(t)
			now := time.Now()
			fixture.server.hub.qualifiedVersion = claudeFrozenFrameProviderVersion
			broker := &failingClaudeDialogueBroker{}
			poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
			fixture.server.broker = broker
			fixture.server.poster = poster
			call := func(request claudeCoordinationRequest) claudeCoordinationResponse {
				request.Version = claudeCoordinationVersion
				request.Target = fixture.target
				request.SessionID = fixture.sessionID
				return fixture.call(t, request)
			}
			submit := func(ref string) claudeCoordinationEnvelope {
				envelope := dialogueForRoute(ref, fixture.route, now)
				if got := call(claudeCoordinationRequest{Operation: "submit", Envelope: &envelope}); got.Kind != "delivered" {
					t.Fatalf("baseline submit: %s", got.Kind)
				}
				return envelope
			}
			prepare := func(ref, id string) string {
				input := toolTestInput(gate, id, ref, "reply")
				got := call(claudeCoordinationRequest{Operation: "tool-prepare", ToolInput: &input})
				if got.Kind != "tool-permitted" || got.ToolResult == nil {
					t.Fatalf("baseline prepare: %s", got.Kind)
				}
				return got.ToolResult.Marker
			}
			consume := func(marker string) {
				if got := call(claudeCoordinationRequest{Operation: "tool-consume", ToolMarker: marker}); got.Kind != "tool-permitted" {
					t.Fatalf("baseline consume: %s", got.Kind)
				}
			}
			a := submit("message-baseline")
			consume(prepare("message-baseline", "tool-baseline"))
			reply := explicitTestReply(*a.BrokerEnvelope, "reply")
			if got := call(claudeCoordinationRequest{Operation: "explicit-reply", ReplyEnvelope: &reply}); got.Kind != "reply-accepted" || broker.replies != 1 {
				t.Fatalf("baseline commit: %s", got.Kind)
			}
			b := dialogueForRoute("message-lost", fixture.route, now)
			request := claudeCoordinationRequest{Operation: stage}
			if stage == "submit" {
				request.Envelope = &b
			} else {
				b = submit("message-lost")
				input := toolTestInput(gate, "tool-lost", "message-lost", "reply")
				if stage == "tool-prepare" {
					request.ToolInput = &input
				} else {
					marker := prepare("message-lost", "tool-lost")
					if stage == "tool-consume" {
						request.ToolMarker = marker
					} else {
						consume(marker)
						reply := explicitTestReply(*b.BrokerEnvelope, "reply")
						request.ReplyEnvelope = &reply
					}
				}
			}
			writes, commits := poster.calls, broker.replies
			peer := gate.profileObserver
			peer.Start += "-replaced"
			raw, _ := json.Marshal(peer)
			if err := os.WriteFile(filepath.Join(root, "observer.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			response := call(request)
			if response.Kind == "tool-permitted" || response.Kind == "reply-accepted" || response.Kind == "delivered" || poster.calls != writes || broker.replies != commits {
				t.Fatalf("observer loss crossed %s: %s", stage, response.Kind)
			}
			// Restoring the descriptor cannot restore an observer already invalidated.
			raw, _ = json.Marshal(gate.profileObserver)
			if err := os.WriteFile(filepath.Join(root, "observer.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if fixture.server.dialogueProfileCurrent() {
				t.Fatal("observer invalidation was not sticky")
			}
		})
	}
}

func TestClaudeDialogueNativeArgsPreserveSameConversationAndRestrictEffects(t *testing.T) {
	original := []string{"/owned/claude", "--resume", "session-same"}
	args := claudeDialogueNativeArgs(original, "/owned/profile")
	for _, required := range []string{"--resume", "session-same", "--restricted", "--no-chrome", "--disable-slash-commands", "--prompt-suggestions", "--strict-mcp-config", "/owned/profile/settings.json", "/owned/profile/mcp.json"} {
		if !slices.Contains(args, required) {
			t.Fatalf("missing launch restriction: %s", required)
		}
	}
	if slices.Contains(args, "--no-session-persistence") || len(original) != 3 {
		t.Fatal("same conversation persistence changed")
	}
	for key, value := range map[string]string{"--tools": "Bash", "--permission-mode": "dontAsk", "--setting-sources": ""} {
		index := slices.Index(args, key)
		if index < 0 || args[index+1] != value {
			t.Fatalf("isolation flag %s incorrect", key)
		}
	}
}
