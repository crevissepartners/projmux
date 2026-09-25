package app

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

const (
	leaseClearOurMarker   = "v1:4243:1700000001:op-8899aabbccddeeff"
	leaseClearOtherMarker = "v1:4244:1700000002:op-0011223344556677"
)

// newLeaseClearMaterializer is the app-routed materializer the create routes
// hand clearCreateOperations, over one fake server.
func newLeaseClearMaterializer(tmux *fakeTmux, warn *bytes.Buffer) *materializer {
	return &materializer{
		runner: tmux, mirror: intmetadata.NewMirror(tmux), sessions: &fakeSessionMaterializer{tmux: tmux}, warn: warn,
		target:             tmuxTransport{Kind: tmuxSocketPath, Value: tmux.socketPath, Source: tmuxSocketPathSource},
		expectedSocketPath: tmux.socketPath,
		socketName:         defaultAppSocket,
		routeAuthority:     &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: tmux.serverPID},
	}
}

// ownerCheckedLeaseClearOf reports whether argv is the owner-checked clear of
// variable: one if-shell whose body unsets it. An empty variable matches the
// clear of either create-operation lease.
func ownerCheckedLeaseClearOf(argv []string, variable string) bool {
	if len(argv) == 0 || argv[0] != "if-shell" {
		return false
	}
	body := argv[len(argv)-1]
	return strings.HasPrefix(body, "set-environment -u ") && (variable == "" || strings.HasSuffix(body, " "+variable))
}

// leaseClearWrites is every command that can unset a lease variable.
func leaseClearWrites(tmux *fakeTmux) [][]string {
	var writes [][]string
	for _, call := range tmux.calls {
		argv := tmuxCommandArgv(call)
		if len(argv) > 0 && (argv[0] == "if-shell" || (argv[0] == "set-environment" && slices.Contains(argv, "-u"))) {
			writes = append(writes, argv)
		}
	}
	return writes
}

// TestClearCreateOperationsKeepsAnotherOperationsLeaseWrittenAfterItsObservation
// pins the post-lock clear against the next create. The clear runs after the
// Registry lock is released, so the next create may write its own marker into
// the same session variable after the clear observed ours. The injection lands
// exactly there: after every read of the clear, immediately before the tmux
// command that removes the create lease. That command must leave the other
// operation's marker in place, and the clear must not warn about a race that
// is part of normal operation.
func TestClearCreateOperationsKeepsAnotherOperationsLeaseWrittenAfterItsObservation(t *testing.T) {
	t.Parallel()
	tmux := newFakeTmux()
	session := tmux.addSession("alpha")
	session.env[createOperationEnvironment] = leaseClearOurMarker
	session.env[finalizeOperationEnvironment] = leaseClearOurMarker
	injected := false
	tmux.beforeDispatch = func(f *fakeTmux, args []string) {
		// The old form is `set-environment -u -t $N <var>`; the owner-checked
		// form carries the same unset as the body of one if-shell.
		command := strings.Join(args, " ")
		deletesCreateLease := (args[0] == "set-environment" || args[0] == "if-shell") &&
			strings.Contains(command, "set-environment -u") && strings.HasSuffix(command, createOperationEnvironment)
		if injected || !deletesCreateLease {
			return
		}
		injected = true
		f.session(session.id).env[createOperationEnvironment] = leaseClearOtherMarker
	}
	var warnings bytes.Buffer
	runtime := newLeaseClearMaterializer(tmux, &warnings)
	ledger := &runtimeLedger{operationMarker: leaseClearOurMarker}
	ledger.markSession(session.id)

	runtime.clearCreateOperations(context.Background(), ledger)

	if !injected {
		t.Fatalf("the create-lease deletion never ran; writes=%q", leaseClearWrites(tmux))
	}
	if got := session.env[createOperationEnvironment]; got != leaseClearOtherMarker {
		t.Fatalf("create lease = %q after clear, want the other operation's marker %q kept; writes=%q",
			got, leaseClearOtherMarker, leaseClearWrites(tmux))
	}
	if got, ok := session.env[finalizeOperationEnvironment]; ok {
		t.Fatalf("finalize lease = %q after clear, want our marker removed", got)
	}
	if warnings.Len() != 0 {
		t.Fatalf("clear warned about a normal lease race: %q", warnings.String())
	}
}

// TestClearCreateOperationsRemovesOnlyItsOwnLease is the ordinary outcome: with
// only our marker present both lease variables go, and each write is the
// owner-checked form carrying our marker.
func TestClearCreateOperationsRemovesOnlyItsOwnLease(t *testing.T) {
	t.Parallel()
	tmux := newFakeTmux()
	session := tmux.addSession("alpha")
	session.env[createOperationEnvironment] = leaseClearOurMarker
	session.env[finalizeOperationEnvironment] = leaseClearOurMarker
	session.env["KEEP"] = "1"
	var warnings bytes.Buffer
	runtime := newLeaseClearMaterializer(tmux, &warnings)
	ledger := &runtimeLedger{operationMarker: leaseClearOurMarker}
	ledger.markSession(session.id)

	runtime.clearCreateOperations(context.Background(), ledger)

	for _, name := range []string{createOperationEnvironment, finalizeOperationEnvironment} {
		if got, ok := session.env[name]; ok {
			t.Fatalf("%s = %q after clear, want it removed", name, got)
		}
	}
	if session.env["KEEP"] != "1" {
		t.Fatal("clear removed an unrelated session variable")
	}
	writes := leaseClearWrites(tmux)
	if len(writes) != 2 {
		t.Fatalf("writes = %q, want one owner-checked clear per lease", writes)
	}
	for _, argv := range writes {
		if argv[0] != "if-shell" || !strings.Contains(argv[4], ","+leaseClearOurMarker+"}") {
			t.Fatalf("write %q is not the owner-checked clear for our marker", argv)
		}
	}
	if warnings.Len() != 0 {
		t.Fatalf("clear warned: %q", warnings.String())
	}
}

// TestClearCreateOperationsIssuesNoWriteWithoutItsLease keeps the initial read:
// a session that is gone, carries no lease, or carries another operation's
// lease gets no write at all.
func TestClearCreateOperationsIssuesNoWriteWithoutItsLease(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		lease string
		gone  bool
	}{
		{name: "missing session", lease: leaseClearOurMarker, gone: true},
		{name: "no lease"},
		{name: "another operation's lease", lease: leaseClearOtherMarker},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmux := newFakeTmux()
			session := tmux.addSession("alpha")
			if tt.lease != "" {
				session.env[createOperationEnvironment] = tt.lease
				session.env[finalizeOperationEnvironment] = tt.lease
			}
			ledger := &runtimeLedger{operationMarker: leaseClearOurMarker}
			ledger.markSession(session.id)
			if tt.gone {
				tmux.sessions = nil
			}
			var warnings bytes.Buffer
			newLeaseClearMaterializer(tmux, &warnings).clearCreateOperations(context.Background(), ledger)
			if writes := leaseClearWrites(tmux); len(writes) != 0 {
				t.Fatalf("writes = %q, want none", writes)
			}
			if tt.lease != "" && !tt.gone && session.env[createOperationEnvironment] != tt.lease {
				t.Fatalf("create lease = %q, want %q kept", session.env[createOperationEnvironment], tt.lease)
			}
			if warnings.Len() != 0 {
				t.Fatalf("clear warned: %q", warnings.String())
			}
		})
	}
}

// ownerCheckedLeaseClear is one valid owner-checked clear-lease declaration.
func ownerCheckedLeaseClear(variable string) plannedRuntimeMutation {
	action := newRuntimeMutation(1, mutationClearLease, runtimeMutationTarget{
		Socket: "-L=projmux", PhysicalSocket: "/tmp/lease-clear", RouteAuthority: "app:pid=4242/session=/window=/pane=",
		Kind: "session", ID: "$3", UID: "session:$3",
	})
	action.Operands = []string{"-u", "-t", "$3", variable}
	action.LeaseMarker = leaseClearOurMarker
	return action
}

// TestRuntimeMutationArgvOwnerCheckedLeaseClear pins the one-command form and
// its fail-closed declaration: an exact $N target bound to the printable
// target, one of the two create-lease variables, and a marker without any
// character a tmux format or command parser would read.
func TestRuntimeMutationArgvOwnerCheckedLeaseClear(t *testing.T) {
	t.Parallel()
	for _, variable := range []string{createOperationEnvironment, finalizeOperationEnvironment} {
		argv, err := runtimeMutationArgv(ownerCheckedLeaseClear(variable))
		if err != nil {
			t.Fatalf("%s: %v", variable, err)
		}
		want := []string{"if-shell", "-F", "-t", "$3",
			"#{==:#{E:" + variable + "}," + leaseClearOurMarker + "}",
			"set-environment -u -t '$3' " + variable}
		if !slices.Equal(argv, want) {
			t.Fatalf("argv = %q, want %q", argv, want)
		}
	}

	plain := ownerCheckedLeaseClear(createOperationEnvironment)
	plain.LeaseMarker = ""
	if argv, err := runtimeMutationArgv(plain); err != nil || !slices.Equal(argv, []string{"set-environment", "-u", "-t", "$3", createOperationEnvironment}) {
		t.Fatalf("unconditional clear argv = %q, %v; want the unchanged set-environment -u", argv, err)
	}

	for _, tt := range []struct {
		name   string
		mutate func(*plannedRuntimeMutation)
	}{
		{name: "marker with a format character", mutate: func(a *plannedRuntimeMutation) { a.LeaseMarker = "v1:1:2:op}#{pid" }},
		{name: "marker with a comma", mutate: func(a *plannedRuntimeMutation) { a.LeaseMarker = "v1:1:2:op,x" }},
		{name: "marker with a quote", mutate: func(a *plannedRuntimeMutation) { a.LeaseMarker = "v1:1:2:op'x" }},
		{name: "marker with a space", mutate: func(a *plannedRuntimeMutation) { a.LeaseMarker = "v1:1:2:op x" }},
		{name: "marker with a dollar", mutate: func(a *plannedRuntimeMutation) { a.LeaseMarker = "v1:1:2:$op" }},
		{name: "unknown variable", mutate: func(a *plannedRuntimeMutation) { a.Operands[3] = "PATH" }},
		{name: "session name target", mutate: func(a *plannedRuntimeMutation) { a.Target.ID, a.Operands[2] = "alpha", "alpha" }},
		{name: "operand target disagrees", mutate: func(a *plannedRuntimeMutation) { a.Operands[2] = "$4" }},
		{name: "missing -u", mutate: func(a *plannedRuntimeMutation) { a.Operands = []string{"-t", "$3", createOperationEnvironment} }},
		{name: "extra operand", mutate: func(a *plannedRuntimeMutation) { a.Operands = append(a.Operands, "value") }},
		{name: "global scope", mutate: func(a *plannedRuntimeMutation) {
			a.Operands = []string{"-gu", "-t", "$3", createOperationEnvironment}
		}},
	} {
		action := ownerCheckedLeaseClear(createOperationEnvironment)
		tt.mutate(&action)
		if argv, err := runtimeMutationArgv(action); err == nil {
			t.Fatalf("%s: argv = %q, want a refusal", tt.name, argv)
		}
	}

	write := newRuntimeMutation(1, mutationWriteLease, ownerCheckedLeaseClear(createOperationEnvironment).Target)
	write.Operands = []string{"-t", "$3", createOperationEnvironment, leaseClearOurMarker}
	write.LeaseMarker = leaseClearOurMarker
	if err := validateRuntimeMutationActionShape(write); err == nil {
		t.Fatal("a lease marker on a non-clear action was accepted")
	}
}
