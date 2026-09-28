package app

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// unreadableAppServer answers every app-socket query with a failure that is not
// the server-absent signature, so the inventory marks its scopes unavailable
// instead of reading an empty machine.
func unreadableAppServer() resourceRunnerFunc {
	return resourceRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		return nil, errors.New("exit status 1: error connecting to /tmp/view-fixture/projmux (Permission denied)")
	})
}

// TestRegistryViewsReportUnobservedRowsAsUnknown is C-2 and C-3 on the read
// routes: when the app server could not be read, every runtime-bearing row --
// the Project included -- says unknown and offers only delete, and describe
// says the same thing.
func TestRegistryViewsReportUnobservedRowsAsUnknown(t *testing.T) {
	t.Parallel()
	get, describe := viewReadCommands(t, unreadableAppServer(), viewTMUXEnv("", false))

	for _, test := range []struct {
		args []string
		name string
	}{
		{args: []string{"projects", "-o", "wide"}, name: "alpha"},
		{args: []string{"windows", "-p", "uid:prj-alpha", "-o", "wide"}, name: "main"},
		{args: []string{"panes", "-p", "uid:prj-alpha", "-o", "wide"}, name: "zsh"},
		{args: []string{"agents", "-p", "uid:prj-alpha", "-o", "wide"}, name: "codex"},
	} {
		out := viewRun(t, get, test.args...)
		row := " " + strings.Join(viewRow(t, out, test.name), " ") + " "
		if !strings.Contains(row, " unknown ") {
			t.Errorf("get %v row %q = %q, want STATUS unknown", test.args, test.name, row)
		}
		if !strings.Contains(row, " delete ") {
			t.Errorf("get %v row %q = %q, want ACTIONS delete only", test.args, test.name, row)
		}
		for _, forbidden := range []string{"start", "resume", "open"} {
			if strings.Contains(row, forbidden) {
				t.Errorf("get %v row %q = %q offers %s for an unobserved row", test.args, test.name, row, forbidden)
			}
		}
	}

	out := viewRun(t, describe, "window", "uid:win-alpha-main")
	if !strings.Contains(out, "unknown") || strings.Contains(out, "offline") {
		t.Errorf("describe window with an unreadable app server:\n%s\nwant status unknown, not offline", out)
	}
}

// TestRegistryViewsKeepOfflineAndStartWhenObservedEmpty is the C-2 boundary: a
// readable machine with nothing on it is still offline with start, and the
// Project row judges its session from that same observation even though the
// stored projection says live.
func TestRegistryViewsKeepOfflineAndStartWhenObservedEmpty(t *testing.T) {
	t.Parallel()
	runner := resourceRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		return nil, errors.New("exit status 1: no server running on /tmp/view-fixture/" + defaultAppSocket)
	})
	get, _ := viewReadCommands(t, runner, viewTMUXEnv("", false))

	for _, test := range []struct {
		args []string
		name string
	}{
		{args: []string{"projects", "-o", "wide"}, name: "alpha"},
		{args: []string{"windows", "-p", "uid:prj-alpha", "-o", "wide"}, name: "main"},
	} {
		row := " " + strings.Join(viewRow(t, viewRun(t, get, test.args...), test.name), " ") + " "
		if !strings.Contains(row, " offline ") || !strings.Contains(row, "start,delete") {
			t.Errorf("get %v row %q = %q, want offline and start,delete", test.args, test.name, row)
		}
	}
}
