package app

import (
	"bytes"
	"flag"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestResourceQueryScopeShortOptionsShareTheLongOptionSinks(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		kind    coremetadata.Kind
		long    []string
		short   []string
		project []string
		windows []string
	}{
		{name: "Project", kind: coremetadata.KindProject, long: []string{"--project", "alpha"}, short: []string{"-p", "alpha"}, project: []string{"alpha"}},
		{name: "Window", kind: coremetadata.KindWindow, long: []string{"--project", "alpha", "--window", "main", "--window", "review"}, short: []string{"-p", "alpha", "-w", "main", "-w", "review"}, project: []string{"alpha"}, windows: []string{"main", "review"}},
		{name: "Pane mixed", kind: coremetadata.KindPane, long: []string{"--project", "alpha", "--window", "main", "--window", "review"}, short: []string{"-p", "alpha", "--window", "main", "-w", "review"}, project: []string{"alpha"}, windows: []string{"main", "review"}},
		{name: "Agent", kind: coremetadata.KindAgent, long: []string{"--project=alpha", "--window=main", "--window=review"}, short: []string{"-p=alpha", "-w=main", "-w=review"}, project: []string{"alpha"}, windows: []string{"main", "review"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			parse := func(args []string) (resourceQueryFlags, string, error) {
				var stderr bytes.Buffer
				fs := flag.NewFlagSet("probe", flag.ContinueOnError)
				fs.SetOutput(&stderr)
				got := resourceQueryFlags{kind: test.kind}
				got.register(fs)
				err := fs.Parse(args)
				return got, stderr.String(), err
			}
			long, longStderr, longErr := parse(test.long)
			short, shortStderr, shortErr := parse(test.short)
			if longErr != nil || shortErr != nil || longStderr != shortStderr {
				t.Fatalf("long=(%v,%q) short=(%v,%q)", longErr, longStderr, shortErr, shortStderr)
			}
			if !reflect.DeepEqual(long.projects, short.projects) || !reflect.DeepEqual(long.windows, short.windows) ||
				!reflect.DeepEqual(short.projects, repeatedFlag(test.project)) || !reflect.DeepEqual(short.windows, repeatedFlag(test.windows)) {
				t.Fatalf("long project/window=%v/%v short=%v/%v", long.projects, long.windows, short.projects, short.windows)
			}
		})
	}
}

func TestSharedResourceRouteInventoryReceivesOnlyItsKindScopeAliases(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		spelling string
		kind     coremetadata.Kind
		window   bool
	}{
		{spelling: "get projects/project", kind: coremetadata.KindProject},
		{spelling: "get windows/window", kind: coremetadata.KindWindow, window: true},
		{spelling: "get panes", kind: coremetadata.KindPane, window: true},
		{spelling: "get agents/agent", kind: coremetadata.KindAgent, window: true},
		{spelling: "describe project/projects", kind: coremetadata.KindProject},
		{spelling: "describe window/windows", kind: coremetadata.KindWindow, window: true},
		{spelling: "describe pane/panes", kind: coremetadata.KindPane, window: true},
		{spelling: "describe agent/agents", kind: coremetadata.KindAgent, window: true},
		{spelling: "delete window/windows", kind: coremetadata.KindWindow, window: true},
		{spelling: "delete pane/panes", kind: coremetadata.KindPane, window: true},
		{spelling: "delete agent/agents", kind: coremetadata.KindAgent, window: true},
		{spelling: "rename project/projects", kind: coremetadata.KindProject},
		{spelling: "rename window/windows", kind: coremetadata.KindWindow, window: true},
		{spelling: "rename pane/panes", kind: coremetadata.KindPane, window: true},
		{spelling: "rename agent/agents", kind: coremetadata.KindAgent, window: true},
		{spelling: "rebind project", kind: coremetadata.KindProject},
		{spelling: "agent resume", kind: coremetadata.KindAgent, window: true},
	} {
		t.Run(test.spelling, func(t *testing.T) {
			t.Parallel()
			flags := resourceQueryFlags{kind: test.kind}
			fs := flag.NewFlagSet(test.spelling, flag.ContinueOnError)
			flags.register(fs)
			if fs.Lookup("project") == nil || fs.Lookup("p") == nil {
				t.Fatal("Project long/short scope is incomplete")
			}
			if (fs.Lookup("window") != nil) != test.window || (fs.Lookup("w") != nil) != test.window {
				t.Fatalf("Window long/short registration = %v/%v, want both %v", fs.Lookup("window") != nil, fs.Lookup("w") != nil, test.window)
			}
			if fs.Lookup("A") != nil {
				t.Fatal("shared selector registered plural-read-only -A")
			}
		})
	}
}

func TestResourceScopeShortOptionsPreserveReadSelectionAndErrors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		long  []string
		short []string
	}{
		{name: "plural repeated Window order", long: []string{"panes", "--project", "alpha", "--window", "review", "--window", "main", "-o", "ref"}, short: []string{"panes", "-p", "alpha", "-w", "review", "--window", "main", "-o", "ref"}},
		{name: "standalone Pane exact one", long: []string{"pane", "--project", "alpha", "--window", "main", "--pane", "log", "-o", "ref"}, short: []string{"pane", "-p", "alpha", "-w", "main", "--pane", "log", "-o", "ref"}},
		{name: "Project exact-one duplicate", long: []string{"panes", "--project", "alpha", "--project", "beta", "-o", "uid"}, short: []string{"panes", "-p", "alpha", "--project", "beta", "-o", "uid"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			longOut, longStderr, longErr := runRoute(t, newTestListGetCommand(t, newFakeResourceStore(t)), test.long...)
			shortOut, shortStderr, shortErr := runRoute(t, newTestListGetCommand(t, newFakeResourceStore(t)), test.short...)
			if longOut != shortOut || longStderr != shortStderr || exitCodeOf(longErr) != exitCodeOf(shortErr) || errorText(longErr) != errorText(shortErr) {
				t.Fatalf("long=(%q,%q,%v) short=(%q,%q,%v)", longOut, longStderr, longErr, shortOut, shortStderr, shortErr)
			}
		})
	}
}

func TestAllProjectsShortOptionIsExactAndConflictsWithEitherProjectSpelling(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"windows", "panes", "agents"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			longOut, longStderr, longErr := runRoute(t, newTestListGetCommand(t, newFakeResourceStore(t)), kind, "--all-projects", "-o", "uid")
			shortOut, shortStderr, shortErr := runRoute(t, newTestListGetCommand(t, newFakeResourceStore(t)), kind, "-A", "-o", "uid")
			if longOut != shortOut || longStderr != shortStderr || errorText(longErr) != errorText(shortErr) {
				t.Fatalf("long=(%q,%q,%v) short=(%q,%q,%v)", longOut, longStderr, longErr, shortOut, shortStderr, shortErr)
			}
		})
	}

	for _, project := range []string{"--project", "-p"} {
		_, _, err := runRoute(t, newTestListGetCommand(t, newFakeResourceStore(t)), "panes", "-A", project, "alpha")
		if err == nil || !IsUsageError(err) || err.Error() != "get panes: --all-projects cannot be combined with --project" {
			t.Fatalf("-A %s error = %v", project, err)
		}
	}
}

func TestResourceCreateShortOptionsPreserveDispatchOrderAndOpaquePayload(t *testing.T) {
	t.Parallel()

	if !hasProjectFlag([]string{"-p", "alpha", "--", "-p", "payload"}) {
		t.Fatal("-p before -- did not select resource-backed create")
	}
	if hasProjectFlag([]string{"--", "-p", "payload"}) {
		t.Fatal("-p after -- was reinterpreted as a Projmux scope")
	}

	longArgs := []string{"--project", "alpha", "--window", "review", "--window", "main", "-o", "pane-id", "--", "tool", "-p", "payload-project", "-w", "payload-window", "--project", "opaque", "--window", "opaque"}
	shortArgs := []string{"-p", "alpha", "-w", "review", "--window", "main", "-o", "pane-id", "--", "tool", "-p", "payload-project", "-w", "payload-window", "--project", "opaque", "--window", "opaque"}
	long, err := parseResourceCreateFlags("create pane", longArgs, &bytes.Buffer{}, resourceCreateShape{split: true})
	if err != nil {
		t.Fatal(err)
	}
	short, err := parseResourceCreateFlags("create pane", shortArgs, &bytes.Buffer{}, resourceCreateShape{split: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(long, short) {
		t.Fatalf("long=%#v\nshort=%#v", long, short)
	}
	wantPayload := []string{"tool", "-p", "payload-project", "-w", "payload-window", "--project", "opaque", "--window", "opaque"}
	if !reflect.DeepEqual(short.payload, wantPayload) {
		t.Fatalf("payload=%q want=%q", short.payload, wantPayload)
	}
}

func TestResourceCreateShortOptionsPreserveMutationAndTmuxPlan(t *testing.T) {
	t.Parallel()

	run := func(args []string) (string, string, string, [][]string, error) {
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		cmd, _ := newTestResourceCreateCommand(t, store, tmux)
		stdout, stderr, err := runRoute(t, cmd, args...)
		return stdout, stderr, store.snapshot(), tmux.calls, err
	}
	longOut, longStderr, longRegistry, longCalls, longErr := run([]string{"pane", "--project", "alpha", "--window", "review", "--window", "main", "-o", "none", "--", "tool", "-p", "opaque", "-w", "opaque"})
	shortOut, shortStderr, shortRegistry, shortCalls, shortErr := run([]string{"pane", "-p", "alpha", "-w", "review", "--window", "main", "-o", "none", "--", "tool", "-p", "opaque", "-w", "opaque"})
	if longOut != shortOut || longStderr != shortStderr || errorText(longErr) != errorText(shortErr) ||
		longRegistry != shortRegistry || !reflect.DeepEqual(longCalls, shortCalls) {
		t.Fatalf("long=(%q,%q,%v,\n%s\n%#v) short=(%q,%q,%v,\n%s\n%#v)",
			longOut, longStderr, longErr, longRegistry, longCalls,
			shortOut, shortStderr, shortErr, shortRegistry, shortCalls)
	}
}

func TestCanonicalFocusShortOptionsPreserveInterleaveAndPlan(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		long  []string
		short []string
	}{
		{name: "window ref before scope", long: []string{"window", "review", "--project", "alpha"}, short: []string{"window", "review", "-p", "alpha"}},
		{name: "pane mixed interleave", long: []string{"pane", "--project", "alpha", "log", "--window", "main"}, short: []string{"pane", "-p", "alpha", "log", "-w", "main"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			longRunner := newLiveFocusRunner(defaultLiveInventory())
			shortRunner := newLiveFocusRunner(defaultLiveInventory())
			longOut, longStderr, longErr := runRoute(t, newFocusTestCommand(longRunner, nil, nil), test.long...)
			shortOut, shortStderr, shortErr := runRoute(t, newFocusTestCommand(shortRunner, nil, nil), test.short...)
			if longOut != shortOut || longStderr != shortStderr || errorText(longErr) != errorText(shortErr) || !reflect.DeepEqual(longRunner.calls, shortRunner.calls) {
				t.Fatalf("long=(%q,%q,%v,%#v) short=(%q,%q,%v,%#v)", longOut, longStderr, longErr, longRunner.calls, shortOut, shortStderr, shortErr, shortRunner.calls)
			}
		})
	}
}

func TestResourceScopeShortOptionNegatives(t *testing.T) {
	t.Parallel()

	flags := resourceQueryFlags{kind: coremetadata.KindPane}
	fs := flag.NewFlagSet("pane", flag.ContinueOnError)
	fs.SetOutput(&bytes.Buffer{})
	flags.register(fs)
	if fs.Lookup("p") == nil || fs.Lookup("p").Value != &flags.projects {
		t.Fatal("-p is not the Project value sink")
	}
	if fs.Lookup("P") != nil {
		t.Fatal("Pane acquired an undeclared -P shortcut")
	}
	if err := fs.Parse([]string{"-A"}); err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("shared Pane selector accepted -A: %v", err)
	}

	for _, kind := range []string{"projects", "pane"} {
		_, _, err := runRoute(t, newTestListGetCommand(t, newFakeResourceStore(t)), kind, "-A")
		if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Fatalf("get %s accepted -A: %v", kind, err)
		}
	}

	var stdout, stderr bytes.Buffer
	if err := usagecmd.New(nil).Run([]string{"-w", "5h"}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("agent usage parser accepted resource -w: %v", err)
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
