package app

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
)

// fakeProcessTree is a process table for the source lineage check: pid to
// identity and parent. A pid that is absent cannot be read.
type fakeProcessTree map[int]struct {
	identity coremetadata.ProcessIdentity
	parent   int
}

func (tree fakeProcessTree) read(pid int) (coremetadata.ProcessIdentity, int, error) {
	entry, ok := tree[pid]
	if !ok {
		return coremetadata.ProcessIdentity{}, 0, errors.New("fixture process unreadable")
	}
	return entry.identity, entry.parent, nil
}

func (tree fakeProcessTree) add(pid, parent int) fakeProcessTree {
	tree[pid] = struct {
		identity coremetadata.ProcessIdentity
		parent   int
	}{identityOf(pid), parent}
	return tree
}

func (tree fakeProcessTree) without(pid int) fakeProcessTree {
	delete(tree, pid)
	return tree
}

func identityOf(pid int) coremetadata.ProcessIdentity {
	return coremetadata.ProcessIdentity{PID: pid, OwnerUID: 1000, Start: "linux:boot-fixture:" + strconv.Itoa(pid)}
}

const (
	lineageProviderPID      = 500
	lineageDescendantCaller = 700
	lineageForeignCaller    = 800
	lineageRegisteredID     = "session-registered"
	lineageEnvSessionID     = "session-registered"
)

// lineageTree is the incident's table: the registered Claude (500) runs a
// shell (600) whose child (700) is a legitimate sender, and a second Claude
// (750, reparented to init like a background session) runs the foreign
// sender (800).
func lineageTree() fakeProcessTree {
	return fakeProcessTree{}.add(400, 1).add(lineageProviderPID, 400).add(600, lineageProviderPID).
		add(lineageDescendantCaller, 600).add(750, 1).add(lineageForeignCaller, 750)
}

type lineageSendResult struct {
	stdout, stderr string
	err            error
	delivery       coremessage.Delivery
	submits        int
	events         []diagnostics.Event
	logOutput      string
	sourceUID      string
}

type lineageSendCase struct {
	sourceUID  func(*precheckFixture) string
	caller     int
	tree       fakeProcessTree
	unbound    bool
	reply      bool
	envSession string
}

// runLineageSend runs one send on a fresh delivered-Claude fixture with the
// source Pane bound to the registered provider, a fake process tree, and a
// real operations journal, and reads the journal back through the public
// `diagnostics log` command.
func runLineageSend(t *testing.T, test lineageSendCase) lineageSendResult {
	t.Helper()
	f, adapter := newScriptedClaudeSendFixture(t, agentdelivery.Delivery{State: agentdelivery.StateDelivered, Reason: "provider-pipe-full-frame"}, nil)
	registry, err := f.cmd.messagePaths.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	pane, ok := registry.Pane(f.route.PaneUID)
	if !ok {
		t.Fatalf("source Pane %s missing from fixture registry", f.route.PaneUID)
	}
	pane.Status.Activation.Claude = nil
	if !test.unbound {
		pane.Status.Activation.Claude = &coremetadata.ClaudeActivationBinding{Process: identityOf(lineageProviderPID),
			RegistrationSessionID: lineageRegisteredID}
	}
	f.cmd.messagePaths.loadRegistry = func() (coremetadata.Registry, error) { return registry.Clone(), nil }
	f.cmd.messageProcess = test.tree.read
	f.cmd.messageCallerPID = func() int { return test.caller }
	f.cmd.lookupEnv = func(key string) string {
		if key == claudeSourceSessionEnv {
			return test.envSession
		}
		return ""
	}
	stateHome := t.TempDir()
	lookup := func(key string) string {
		if key == "XDG_STATE_HOME" {
			return stateHome
		}
		return ""
	}
	path, err := diagnostics.DefaultPath(lookup, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.cmd.messageDiagnostics = diagnostics.NewLifecycleRecorder(diagnostics.NewStore(path), "run-lineage", "1.0.0-test", diagnostics.MuxBackend()).AgentMessage()

	sourceUID := f.claudeUID
	if test.sourceUID != nil {
		sourceUID = test.sourceUID(f)
	}
	const ref = "message-lineage"
	args := []string{"message", "send", "uid:" + f.claudeUID, "--source", "uid:" + sourceUID, "--message-ref", ref}
	if test.reply {
		args = append(args, "--reply-to", f.original.MessageRef)
	}
	stdout, stderr, sendErr := runRoute(t, f.cmd, append(args, "--", "coordination body")...)
	events, err := diagnostics.NewStore(path).Read()
	if err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	logCmd := &diagnosticsCommand{lookupEnv: lookup, homeDir: func() (string, error) { return stateHome, nil }}
	if err := logCmd.Run([]string{"log", "--component", "agent"}, &log, &log); err != nil {
		t.Fatal(err)
	}
	return lineageSendResult{stdout: stdout, stderr: stderr, err: sendErr,
		delivery: persistedDelivery(t, messagestore.NewStore(f.stateDir), ref).Delivery,
		submits:  adapter.submits, events: events, logOutput: log.String(), sourceUID: sourceUID}
}

// assertSendUnchanged compares everything a sender observes except stderr and
// the journal against the same send from a descendant caller.
func assertSendUnchanged(t *testing.T, got, baseline lineageSendResult) {
	t.Helper()
	// The fixture clock is wall time, so the two sends differ only in when.
	for _, delivery := range []*coremessage.Delivery{&got.delivery, &baseline.delivery} {
		delivery.AcceptedAt, delivery.TerminalAt = time.Time{}, time.Time{}
	}
	if got.stdout != baseline.stdout || got.delivery != baseline.delivery || got.submits != baseline.submits ||
		(got.err == nil) != (baseline.err == nil) || (got.err != nil && got.err.Error() != baseline.err.Error()) {
		t.Fatalf("send changed:\n got stdout=%q err=%v delivery=%+v submits=%d\nwant stdout=%q err=%v delivery=%+v submits=%d",
			got.stdout, got.err, got.delivery, got.submits, baseline.stdout, baseline.err, baseline.delivery, baseline.submits)
	}
}

func assertNoForeignSourceReport(t *testing.T, got lineageSendResult) {
	t.Helper()
	if got.stderr != "" || len(got.events) != 0 || got.logOutput != "" {
		t.Fatalf("stderr=%q events=%+v log=%q, want no warning and no journal event", got.stderr, got.events, got.logOutput)
	}
}

func TestAgentMessageSendWarnsOnceWhenCallerIsNotDescendantOfClaudeSource(t *testing.T) {
	baseline := runLineageSend(t, lineageSendCase{caller: lineageDescendantCaller, tree: lineageTree(), envSession: lineageEnvSessionID})
	got := runLineageSend(t, lineageSendCase{caller: lineageForeignCaller, tree: lineageTree(), envSession: lineageEnvSessionID})
	if baseline.err != nil || baseline.delivery.State != coremessage.StateDelivered || baseline.submits != 1 {
		t.Fatalf("baseline send = err %v delivery %+v submits %d, want one delivered push", baseline.err, baseline.delivery, baseline.submits)
	}
	assertSendUnchanged(t, got, baseline)

	if strings.Count(got.stderr, "\n") != 1 || !strings.HasSuffix(got.stderr, "\n") {
		t.Fatalf("stderr = %q, want exactly one warning line", got.stderr)
	}
	agentUID := got.sourceUID
	for _, want := range []string{
		"agent message send: warning:",
		"source Agent uid:" + agentUID + " ",
		"Claude session " + lineageRegisteredID,
		"pid 800",
		`CLAUDE_CODE_SESSION_ID="` + lineageEnvSessionID + `"`,
		"claude --resume",
		"Move to background",
		"end that session, or send from the registered session",
	} {
		if !strings.Contains(got.stderr, want) {
			t.Fatalf("warning %q lacks %q", got.stderr, want)
		}
	}

	if len(got.events) != 1 {
		t.Fatalf("journal events = %+v, want exactly one", got.events)
	}
	event := got.events[0]
	if event.Component != "agent" || event.Event != "agent.message.foreign-source" || event.Level != "info" || event.Result != "success" ||
		event.AgentUID != agentUID || !strings.HasPrefix(event.PaneUID, "pane-") || event.Message != "" {
		t.Fatalf("journal event = %+v", event)
	}
	if strings.Count(got.logOutput, "\n") != 1 || !strings.Contains(got.logOutput, "agent.message.foreign-source") ||
		!strings.Contains(got.logOutput, "agent_uid="+agentUID) || !strings.Contains(got.logOutput, "pane_uid="+event.PaneUID) {
		t.Fatalf("diagnostics log = %q, want the one foreign-source record", got.logOutput)
	}
	// The provider session, pids, and environment stay out of the journal.
	for _, secret := range []string{lineageRegisteredID, "800", "CLAUDE_CODE_SESSION_ID"} {
		if strings.Contains(got.logOutput, secret) {
			t.Fatalf("diagnostics log %q carries %q", got.logOutput, secret)
		}
	}
}

func TestAgentMessageSendWarningNamesAnUnsetClaudeSessionEnvironment(t *testing.T) {
	got := runLineageSend(t, lineageSendCase{caller: lineageForeignCaller, tree: lineageTree()})
	if !strings.Contains(got.stderr, "CLAUDE_CODE_SESSION_ID=unset") || len(got.events) != 1 {
		t.Fatalf("stderr=%q events=%d, want the unset environment named and one event", got.stderr, len(got.events))
	}
}

func TestAgentMessageSendFromClaudeSourceDescendantDoesNotWarn(t *testing.T) {
	got := runLineageSend(t, lineageSendCase{caller: lineageDescendantCaller, tree: lineageTree(), envSession: lineageEnvSessionID})
	if got.err != nil || got.delivery.State != coremessage.StateDelivered {
		t.Fatalf("send = err %v delivery %+v", got.err, got.delivery)
	}
	assertNoForeignSourceReport(t, got)
}

func TestAgentMessageSendForeignSourceWarningSkipsUnjudgedSends(t *testing.T) {
	codexSource := func(*precheckFixture) string { return precheckCodexSourceUID }
	for _, test := range []struct {
		name     string
		foreign  lineageSendCase
		baseline lineageSendCase
	}{
		{name: "codex source",
			foreign:  lineageSendCase{sourceUID: codexSource, caller: lineageForeignCaller, tree: lineageTree()},
			baseline: lineageSendCase{sourceUID: codexSource, caller: lineageDescendantCaller, tree: lineageTree()}},
		{name: "claude source without a registered process",
			foreign:  lineageSendCase{unbound: true, caller: lineageForeignCaller, tree: lineageTree()},
			baseline: lineageSendCase{caller: lineageDescendantCaller, tree: lineageTree()}},
		{name: "own process unreadable",
			foreign:  lineageSendCase{caller: lineageForeignCaller, tree: lineageTree().without(lineageForeignCaller)},
			baseline: lineageSendCase{caller: lineageDescendantCaller, tree: lineageTree()}},
		{name: "registered provider exited",
			foreign:  lineageSendCase{caller: lineageForeignCaller, tree: lineageTree().without(lineageProviderPID)},
			baseline: lineageSendCase{caller: lineageDescendantCaller, tree: lineageTree()}},
		{name: "registered provider pid reused",
			foreign:  lineageSendCase{caller: lineageForeignCaller, tree: lineageTree().add(lineageProviderPID, 400).reborn(lineageProviderPID)},
			baseline: lineageSendCase{caller: lineageDescendantCaller, tree: lineageTree()}},
		{name: "ancestor unreadable mid walk",
			foreign:  lineageSendCase{caller: lineageForeignCaller, tree: lineageTree().without(750)},
			baseline: lineageSendCase{caller: lineageDescendantCaller, tree: lineageTree()}},
		// A Claude-source reply is judged by its coordination server, which
		// refuses a caller outside the provider before any receipt exists.
		{name: "explicit reply",
			foreign:  lineageSendCase{reply: true, caller: lineageForeignCaller, tree: lineageTree()},
			baseline: lineageSendCase{reply: true, caller: lineageDescendantCaller, tree: lineageTree()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := runLineageSend(t, test.foreign)
			assertSendUnchanged(t, got, runLineageSend(t, test.baseline))
			assertNoForeignSourceReport(t, got)
		})
	}
}

func (tree fakeProcessTree) reborn(pid int) fakeProcessTree {
	entry := tree[pid]
	entry.identity.Start += "-reused"
	tree[pid] = entry
	return tree
}

func TestClaudeProviderLineageSeparatesNotDescendantFromUnknown(t *testing.T) {
	provider := identityOf(lineageProviderPID)
	for _, test := range []struct {
		name       string
		tree       fakeProcessTree
		peer       int
		descendant bool
		unknown    bool
	}{
		{name: "descendant", tree: lineageTree(), peer: lineageDescendantCaller, descendant: true},
		{name: "reparented to init", tree: lineageTree(), peer: lineageForeignCaller},
		{name: "provider itself", tree: lineageTree(), peer: lineageProviderPID},
		{name: "other owner", tree: lineageTree().add(900, 1).owned(900, 0), peer: 900},
		{name: "peer unreadable", tree: lineageTree().without(lineageForeignCaller), peer: lineageForeignCaller, unknown: true},
		{name: "parent unreadable", tree: lineageTree().without(600), peer: lineageDescendantCaller, unknown: true},
		{name: "peer changed identity", tree: lineageTree().reborn(lineageDescendantCaller), peer: lineageDescendantCaller, unknown: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			peer := identityOf(test.peer)
			if test.name == "other owner" {
				peer.OwnerUID = 0
			}
			got, err := claudeProviderLineage(test.tree.read, peer, provider)
			if got != test.descendant || (err != nil) != test.unknown {
				t.Fatalf("lineage = %t, %v; want %t, unknown %t", got, err, test.descendant, test.unknown)
			}
		})
	}
}

func (tree fakeProcessTree) owned(pid int, owner uint32) fakeProcessTree {
	entry := tree[pid]
	entry.identity.OwnerUID = owner
	tree[pid] = entry
	return tree
}
