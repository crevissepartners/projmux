package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

func TestClaudeReplyToolRequestedCaptureFailureRefusesAndSecretsAreAbsent(t *testing.T) {
	getenv := func(key string) string {
		switch key {
		case internalClaudeReplyGuardEnv:
			return "1"
		case "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_CODE_MESSAGING_SOCKET":
			return "private-fixture-value"
		case "HOME":
			return "/owned/home"
		}
		return ""
	}
	good := func() (string, error) { return "/owned", nil }
	bad := func() (string, error) { return "", errors.New("unavailable") }
	for _, callbacks := range [][2]func() (string, error){{bad, good}, {good, bad}} {
		if policy, err := captureClaudeReplyToolPolicyFrom(getenv, callbacks[0], callbacks[1]); err == nil || policy != nil {
			t.Fatal("requested guard capture failure became unguarded bootstrap")
		}
	}
	policy, err := captureClaudeReplyToolPolicyFrom(getenv, good, good)
	if err != nil || policy == nil || strings.Contains(strings.Join(policy.Environment, "\n"), "private-fixture-value") || len(policy.Environment) != 1 {
		t.Fatal("capture copied provider credentials or lost fixed environment")
	}
}

func TestClaudeReplyToolLiteralCommandAndOpaqueCarrier(t *testing.T) {
	executable := "/owned/projmux"
	valid := executable + " agent message send uid:codex-agent --reply-to message-a -- 'literal $(touch /outside); text'"
	argv, err := parseClaudeReplyCommand(valid, executable)
	if err != nil || len(argv) != 9 || argv[8] != "literal $(touch /outside); text" {
		t.Fatalf("literal data was not preserved: %v", err)
	}
	for _, command := range []string{
		"cat /outside", "ENV=1 " + valid, valid + " ; touch /outside", valid + " extra",
		strings.Replace(valid, executable, "/other/projmux", 1),
		strings.Replace(valid, "'literal $(touch /outside); text'", "$(touch /outside)", 1),
		strings.Replace(valid, "'literal $(touch /outside); text'", "`touch /outside`", 1),
		strings.Replace(valid, "--reply-to", "--conversation", 1), valid + "\n", valid + "\x00",
	} {
		if _, err := parseClaudeReplyCommand(command, executable); err == nil {
			t.Fatal("nonliteral or nonallowlisted command accepted")
		}
	}
	marker := "pmx-reply-ticket-" + strings.Repeat("a", 16) + "-" + strings.Repeat("b", 48)
	// The surrounding text is opaque. Even apparently executable bytes cannot
	// add effects: the result is only a memory ticket, never a shell program.
	for _, carrier := range []string{marker, "opaque -- '" + marker + "' ; $(touch /outside)", "\n" + marker + "\n"} {
		if got, err := claudeReplyCarrierMarker(carrier); err != nil || got != marker {
			t.Fatalf("single ticket rejected: %v", err)
		}
	}
	for _, carrier := range []string{"cat /outside", marker + " " + marker, "x" + marker, marker + "a", marker + " pmx-reply-ticket-forged", marker + "\x00", strings.Repeat(" ", localipc.MaxFrameBytes) + marker} {
		if _, err := claudeReplyCarrierMarker(carrier); err == nil {
			t.Fatal("missing, partial, multiple or malformed carrier accepted")
		}
	}
}

func TestClaudeReplyToolRejectsMalformedAndOtherOfficialTools(t *testing.T) {
	base := map[string]any{"hook_event_name": "PreToolUse", "session_id": "session-owned", "cwd": "/owned", "tool_name": "Bash", "tool_use_id": "tool-owned", "tool_input": map[string]any{"command": "literal", "timeout": 5000, "run_in_background": false}}
	data, _ := json.Marshal(base)
	if _, _, err := readClaudeReplyToolInput(strings.NewReader(string(data))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"other tool", func(m map[string]any) { m["tool_name"] = "Read" }},
		{"other hook", func(m map[string]any) { m["hook_event_name"] = "Stop" }},
		{"subagent", func(m map[string]any) { m["agent_id"] = "foreign" }},
		{"missing tool id", func(m map[string]any) { delete(m, "tool_use_id") }},
		{"background", func(m map[string]any) { m["tool_input"].(map[string]any)["run_in_background"] = true }},
		{"unknown option", func(m map[string]any) { m["tool_input"].(map[string]any)["dangerouslyDisableSandbox"] = true }},
		{"unbounded timeout", func(m map[string]any) { m["tool_input"].(map[string]any)["timeout"] = 30001 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value map[string]any
			_ = json.Unmarshal(data, &value)
			tc.edit(value)
			input, _ := json.Marshal(value)
			if _, _, err := readClaudeReplyToolInput(strings.NewReader(string(input))); err == nil {
				t.Fatal("unapproved tool action accepted")
			}
		})
	}
	for _, input := range []string{"", "{", string(data) + "{}"} {
		if _, _, err := readClaudeReplyToolInput(strings.NewReader(input)); err == nil {
			t.Fatal("malformed hook accepted")
		}
	}
}

func newClaudeReplyToolTestGate(t *testing.T) (*claudeReplyToolGate, coremetadata.ProcessIdentity) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("owned executable proof requires Linux /proc")
	}
	image, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "candidate")
	if err := os.Link(image, path); err != nil {
		t.Skip("owned executable hardlink unavailable: " + err.Error())
	}
	// go test's disposable image may be group-writable under the runner umask.
	// Pin its owned permissions for this nonparallel test, then restore them.
	info, err := os.Stat(image)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, info.Mode().Perm() & ^os.FileMode(0o022)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(image, info.Mode().Perm()) })
	gate, err := newClaudeReplyToolGate(claudeReplyToolPolicy{Executable: path, Directory: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gate.close)
	peer, _, err := localipc.Process(os.Getpid())
	if err != nil || !gate.currentExecutable(peer) {
		t.Fatal("test caller is not the pinned owned candidate")
	}
	return gate, peer
}

func toolTestInput(gate *claudeReplyToolGate, id, ref, payload string) claudeReplyToolInput {
	return claudeReplyToolInput{ToolUseID: id, Directory: gate.policy.Directory,
		Command: "'" + gate.policy.Executable + "' agent message send uid:codex-agent --reply-to " + ref + " -- '" + payload + "'"}
}

func TestClaudeReplyToolTicketsChooseExactActionAndConsumeOnce(t *testing.T) {
	gate, peer := newClaudeReplyToolTestGate(t)
	fixture := newClaudeCoordinationTestFixture(t)
	now := time.Now().UTC()
	hub := qualifiedPushHub(now)
	broker := &failingClaudeDialogueBroker{}
	poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
	a, b := dialogueForRoute("message-a", fixture.route, now), dialogueForRoute("message-b", fixture.route, now)
	for _, envelope := range []claudeCoordinationEnvelope{a, b} {
		if got := hub.submitPush(envelope, broker, poster); got.State != agentdelivery.StateDelivered {
			t.Fatal("fixture request unavailable")
		}
	}
	first, err := gate.prepare(toolTestInput(gate, "tool-a", "message-a", "reply-a"), peer, fixture.route, hub, broker)
	if err != nil {
		t.Fatal(err)
	}
	second, err := gate.prepare(toolTestInput(gate, "tool-b", "message-b", "reply-b"), peer, fixture.route, hub, broker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.prepare(toolTestInput(gate, "tool-a", "message-b", "reply-b"), peer, fixture.route, hub, broker); err == nil {
		t.Fatal("same tool id was authorized twice")
	}
	forged := strings.Replace(first.Marker, "pmx-reply-ticket-", "pmx-reply-ticket-f", 1)
	if _, err := gate.consume(forged, peer, fixture.route, hub, broker); err == nil {
		t.Fatal("forged ticket accepted")
	}
	// B may complete before A. No FIFO/latest-message selection is involved.
	result, err := gate.consume(second.Marker, peer, fixture.route, hub, broker)
	if err != nil || result.Argv[6] != "message-b" || result.Argv[8] != "reply-b" {
		t.Fatalf("wrong parallel action selected: %v", err)
	}
	if _, err := gate.consume(second.Marker, peer, fixture.route, hub, broker); err == nil {
		t.Fatal("ticket replayed")
	}
	stale := peer
	stale.Start += "replaced"
	if gate.authorizeCommit(stale, explicitTestReply(*b.BrokerEnvelope, "reply-b")) || !gate.authorizeCommit(peer, explicitTestReply(*b.BrokerEnvelope, "reply-b")) || gate.authorizeCommit(peer, explicitTestReply(*b.BrokerEnvelope, "reply-b")) {
		t.Fatal("process birth or commit-once witness failed")
	}
	result, err = gate.consume(first.Marker, peer, fixture.route, hub, broker)
	if err != nil || result.Argv[6] != "message-a" {
		t.Fatalf("A was lost or substituted: %v", err)
	}
	if gate.authorizeCommit(peer, explicitTestReply(*b.BrokerEnvelope, "reply-b")) || gate.authorizeCommit(peer, explicitTestReply(*a.BrokerEnvelope, "reply-a")) {
		t.Fatal("wrong action committed or failed witness was reused")
	}
}

func TestClaudeReplyToolForeignStaleExpiredAndReplacedExecutableExecuteZero(t *testing.T) {
	for _, kind := range []string{"stale birth", "foreign provider", "expired", "source lease", "replaced executable", "changed cwd", "missing hook"} {
		t.Run(kind, func(t *testing.T) {
			gate, peer := newClaudeReplyToolTestGate(t)
			fixture := newClaudeCoordinationTestFixture(t)
			now := time.Now().UTC()
			hub := qualifiedPushHub(now)
			broker := &failingClaudeDialogueBroker{}
			poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
			hub.submitPush(dialogueForRoute("message-a", fixture.route, now), broker, poster)
			permit, err := gate.prepare(toolTestInput(gate, "tool-a", "message-a", "reply-a"), peer, fixture.route, hub, broker)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "stale birth":
				peer.Start += "old"
			case "foreign provider":
				peer = fixture.route.Authority().(coremetadata.ClaudeAuthorityRef).Process
			case "expired":
				value := gate.permits[permit.Marker]
				value.expires = time.Now().Add(-time.Second)
				gate.permits[permit.Marker] = value
			case "source lease":
				broker.current = func() bool { return false }
			case "replaced executable":
				if err := os.Rename(gate.policy.Executable, gate.policy.Executable+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(gate.policy.Executable, []byte("replacement"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "changed cwd":
				gate.policy.Directory = t.TempDir()
			case "missing hook":
				permit.Marker = "cat /outside"
			}
			if _, err := gate.consume(permit.Marker, peer, fixture.route, hub, broker); err == nil || len(gate.executing) != 0 || broker.replies != 0 {
				t.Fatal("unproved action gained an execution witness")
			}
		})
	}
}

func TestClaudeExplicitReplyUnsupportedTargetCannotCorruptStore(t *testing.T) {
	root := t.TempDir()
	store := messagestore.NewStore(root)
	original := dialogueEnvelope("message-original", time.Now().Add(time.Minute)).BrokerEnvelope
	original.Source.Provider = "claude"
	if _, _, err := store.PutAccepted(*original, "claude-coordination"); err != nil {
		t.Fatal(err)
	}
	contents := map[string]string{}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			data, _ := os.ReadFile(path)
			contents[path] = string(data)
		}
		return err
	})
	broker := &liveClaudeDialogueBroker{store: store}
	if broker.CommitReply(*original, explicitTestReply(*original, "reply")) == nil {
		t.Fatal("unsupported target accepted")
	}
	for path, before := range contents {
		after, err := os.ReadFile(path)
		if err != nil || string(after) != before {
			t.Fatal("rejected reply changed durable store")
		}
	}
	if _, found, err := store.Get(original.MessageRef); err != nil || !found {
		t.Fatal("store became malformed")
	}
}

func TestClaudeQualificationRequiresCurrentPinnedMemoryGuard(t *testing.T) {
	for _, kind := range []string{"missing guard", "replaced image", "writable image"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newClaudeCoordinationTestFixture(t)
			if kind != "missing guard" {
				gate, _ := newClaudeReplyToolTestGate(t)
				fixture.server.tool = gate
				if kind == "replaced image" {
					if err := os.Rename(gate.policy.Executable, gate.policy.Executable+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(gate.policy.Executable, []byte("replacement"), 0o700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Chmod(gate.policy.Executable, 0o777); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now().UTC()
			broker := &failingClaudeDialogueBroker{}
			poster := &qualificationPosterRecorder{outcome: claudeProviderPostOutcome{FullFrameWritten: true, WroteAny: true}}
			fixture.server.broker, fixture.server.poster = broker, poster
			evidence := exactQualificationEvidence(fixture.route, now) // Asserted boolean is deliberately true.
			challenge := qualificationTestEnvelope(fixture.route, now)
			response := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion, Operation: "qualify", Target: fixture.target,
				Qualification: &evidence, Envelope: &challenge, ExplicitOptIn: true})
			if response.Kind != "qualification-refused" || response.Reason != "pinned-reply-execution-required" || poster.calls != 0 || broker.handoffs != 0 {
				t.Fatal("asserted evidence gained authority without a current pinned guard")
			}
			// Delivery admission no longer reads the qualification, so an
			// obsolete local value cannot be inherited into it. What the guard
			// still owns is the qualification itself: the pinned reply
			// execution stays required no matter what that value says.
			fixture.server.hub.qualifiedVersion = claudeFrozenFrameProviderVersion // Simulate an obsolete local qualification.
			repeat := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion, Operation: "qualify", Target: fixture.target,
				Qualification: &evidence, Envelope: &challenge, ExplicitOptIn: true})
			if repeat.Kind != "qualification-refused" || repeat.Reason != "pinned-reply-execution-required" || poster.calls != 0 || broker.handoffs != 0 {
				t.Fatalf("obsolete qualification granted authority: %+v", repeat)
			}
		})
	}
}
