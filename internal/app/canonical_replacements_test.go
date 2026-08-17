package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
)

type parityForwardProbe struct {
	calls  [][]string
	stdout string
	stderr string
	err    error
}

func (p *parityForwardProbe) Run(args []string, stdout, stderr io.Writer) error {
	p.calls = append(p.calls, append([]string(nil), args...))
	_, _ = io.WriteString(stdout, p.stdout)
	_, _ = io.WriteString(stderr, p.stderr)
	return p.err
}

func TestCanonicalReplacementRoutesForwardRawArgvStreamsAndErrors(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("sentinel exit")
	tests := []struct {
		name string
		args []string
		want []string
		new  func(*parityForwardProbe) rawArgvCommand
	}{
		{"config edit get", []string{"edit", "--get"}, []string{"settings", "--get"}, func(p *parityForwardProbe) rawArgvCommand { return &configCommand{ai: p} }},
		{"config edit set", []string{"edit", "--set", "codex"}, []string{"settings", "--set", "codex"}, func(p *parityForwardProbe) rawArgvCommand { return &configCommand{ai: p} }},
		{"config edit unknown flag", []string{"edit", "--unknown", "안녕"}, []string{"settings", "--unknown", "안녕"}, func(p *parityForwardProbe) rawArgvCommand { return &configCommand{ai: p} }},
		{"create notification", []string{"notification", "--text", "hello", "--bogus"}, []string{"push", "--text", "hello", "--bogus"}, func(p *parityForwardProbe) rawArgvCommand { return &createCommand{notify: p} }},
		{"create snapshot", []string{"snapshot", "--", "payload"}, []string{"save", "--", "payload"}, func(p *parityForwardProbe) rawArgvCommand { return &createCommand{snapshots: p} }},
		{"notification ack", []string{"ack", "--all", "--bogus"}, []string{"ack", "--all", "--bogus"}, func(p *parityForwardProbe) rawArgvCommand { return &notificationCommand{notify: p} }},
		{"notification reconcile", []string{"reconcile", "--json", "--", "tail"}, []string{"reconcile", "--json", "--", "tail"}, func(p *parityForwardProbe) rawArgvCommand { return &notificationCommand{notify: p} }},
		{"diagnostics agent-hook", []string{"agent-hook", "--tail", "7", "--json"}, []string{"ingest", "log", "--tail", "7", "--json"}, func(p *parityForwardProbe) rawArgvCommand { return &diagnosticsCommand{ai: p} }},
		{"internal focus", []string{"focus", "--target", "alpha:1.0", "--source", "toast", "--bogus"}, []string{"--target", "alpha:1.0", "--source", "toast", "--bogus"}, func(p *parityForwardProbe) rawArgvCommand { return &internalCommand{focus: p} }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			probe := &parityForwardProbe{stdout: "stdout\n", stderr: "stderr\n", err: sentinel}
			var stdout, stderr bytes.Buffer
			err := test.new(probe).Run(test.args, &stdout, &stderr)
			if !errors.Is(err, sentinel) || err != sentinel {
				t.Fatalf("error = %v, want the unchanged sentinel instance", err)
			}
			if stdout.String() != probe.stdout || stderr.String() != probe.stderr {
				t.Fatalf("streams = (%q, %q), want (%q, %q)", stdout.String(), stderr.String(), probe.stdout, probe.stderr)
			}
			if len(probe.calls) != 1 || !reflect.DeepEqual(probe.calls[0], test.want) {
				t.Fatalf("forwarded calls = %#v, want %#v", probe.calls, test.want)
			}
		})
	}
}

func TestCanonicalReplacementGraphSharesExistingHandlerInstances(t *testing.T) {
	t.Parallel()

	application := New()
	for name, pair := range map[string][2]rawArgvCommand{
		"config edit":            {application.config.ai, application.ai},
		"create notification":    {application.create.notify, application.notify},
		"create snapshot":        {application.create.snapshots, application.sessionState},
		"notification ack":       {application.notification.notify, application.notify},
		"notification reconcile": {application.notification.notify, application.notify},
		"diagnostics agent-hook": {application.diagnostics.ai, application.ai},
		"internal focus":         {application.internal.focus, application.focus},
	} {
		if pair[0] == nil || pair[1] == nil || pair[0] != pair[1] {
			t.Errorf("%s does not share its existing handler instance", name)
		}
	}
}

func TestConfigEditMatchesAISettingsGetSetAndErrors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "get", args: []string{"--get"}},
		{name: "set", args: []string{"--set", "codex"}},
		{name: "unknown flag", args: []string{"--unknown"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			legacy := testAICommand(t.TempDir())
			canonical := testAICommand(t.TempDir())
			var legacyOut, legacyErr, canonicalOut, canonicalErr bytes.Buffer
			legacyRunErr := legacy.Run(append([]string{"settings"}, test.args...), &legacyOut, &legacyErr)
			canonicalRunErr := (&configCommand{ai: canonical}).Run(append([]string{"edit"}, test.args...), &canonicalOut, &canonicalErr)

			if legacyOut.String() != canonicalOut.String() || legacyErr.String() != canonicalErr.String() {
				t.Fatalf("streams differ: legacy=(%q, %q) canonical=(%q, %q)", legacyOut.String(), legacyErr.String(), canonicalOut.String(), canonicalErr.String())
			}
			if fmt.Sprint(legacyRunErr) != fmt.Sprint(canonicalRunErr) {
				t.Fatalf("errors differ: legacy=%v canonical=%v", legacyRunErr, canonicalRunErr)
			}

			// A successful --set must leave the same durable mode observable through
			// the shared --get parser, not merely return the same immediate streams.
			if test.name == "set" {
				legacyOut.Reset()
				canonicalOut.Reset()
				if err := legacy.Run([]string{"settings", "--get"}, &legacyOut, io.Discard); err != nil {
					t.Fatalf("legacy get after set: %v", err)
				}
				if err := (&configCommand{ai: canonical}).Run([]string{"edit", "--get"}, &canonicalOut, io.Discard); err != nil {
					t.Fatalf("canonical get after set: %v", err)
				}
				if legacyOut.String() != canonicalOut.String() || canonicalOut.String() != "codex\n" {
					t.Fatalf("durable modes differ: legacy=%q canonical=%q", legacyOut.String(), canonicalOut.String())
				}
			}
		})
	}
}

func TestAgentHookReadersStayReadOnlyBeforeDispatch(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"diagnostics", "agent-hook", "--tail", "5"},
		{"ai", "ingest", "log", "--tail", "5"},
	} {
		if shouldRunLegacyHookMigrations(args) {
			t.Errorf("%v would run a filesystem migration before its read-only handler", args)
		}
	}
	if !shouldRunLegacyHookMigrations([]string{"internal", "agent-hook", "ingest", "codex-hook"}) {
		t.Fatal("machine ingest unexpectedly skipped the existing pre-dispatch migration path")
	}
}
