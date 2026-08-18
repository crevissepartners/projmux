package app

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/recentwindows"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/i18n"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// The secondary primary surfaces -- recent sessions and recent windows -- with
// the Registry deciding which rows are theirs.
//
// Attribution is by tmux's own id in both cases: `$N` for a session and `@N` for
// a window. That is the point of the fixtures below. A row is managed because
// the resolved graph bound its exact id to a resource, never because a name
// looked familiar.

// sessionsRegistryFixture wires a recent-session command over the navigation
// fixture's exact host.
func sessionsRegistryFixture(t *testing.T, inherited string) *sessionsCommand {
	t.Helper()
	reader, _, _, _ := navigationFixtureReader(t, "1", inherited)
	return &sessionsCommand{navigation: reader}
}

// fixtureSessionSummaries mirrors the fake server's sessions: the managed
// Project session, the Home control session, and a scratch session.
func fixtureSessionSummaries() []inttmux.RecentSessionSummary {
	return []inttmux.RecentSessionSummary{
		{ID: "$1", Name: "alpha", Attached: true, WindowCount: 3, Activity: 30},
		{ID: "$8", Name: "Home", WindowCount: 1, Activity: 20},
		{ID: "$11", Name: "scratch", WindowCount: 1, Activity: 10},
	}
}

// TestSessionsWithholdRuntimeOnlySessions is acceptance (3) on the recent-session
// surface: Home and a scratch session are not managed rows, and the row that
// leads to them names what it leads to.
func TestSessionsWithholdRuntimeOnlySessions(t *testing.T) {
	t.Parallel()

	command := sessionsRegistryFixture(t, "/tmp/fake-tmux/primary,0,0")
	attribution := command.attributeSessions(context.Background(), fixtureSessionSummaries())

	names := make([]string, 0, len(attribution.managed))
	for _, summary := range attribution.managed {
		names = append(names, summary.Name)
	}
	if !reflect.DeepEqual(names, []string{"alpha"}) {
		t.Fatalf("managed sessions = %#v, want the Project session alone", names)
	}
	if got, want := attribution.withheld, (registryview.RuntimeCounts{Control: 1, Ephemeral: 1}); got != want {
		t.Fatalf("withheld tally = %+v, want %+v", got, want)
	}
	entry, ok := attribution.runtimeLinkEntry()
	if !ok {
		t.Fatal("no Runtime link was offered for the withheld sessions")
	}
	if entry.Value != sessionsRuntimeSentinel {
		t.Fatalf("runtime link value = %q, want the sentinel", entry.Value)
	}
	for _, want := range []string{"control 1", "ephemeral 1"} {
		if !strings.Contains(entry.Label, want) {
			t.Fatalf("runtime link label %q does not name %q", entry.Label, want)
		}
	}
}

// TestSessionsCarryTheManagedResourceName pins "prefer managed resource
// identity": the row keeps the exact tmux handle its actions need and gains the
// Registry name that says whose session it is.
func TestSessionsCarryTheManagedResourceName(t *testing.T) {
	t.Parallel()

	command := sessionsRegistryFixture(t, "/tmp/fake-tmux/primary,0,0")
	attribution := command.attributeSessions(context.Background(), fixtureSessionSummaries())
	rows, err := command.buildRows(attribution.managed, attribution, i18n.FallbackLocale)
	if err != nil {
		t.Fatalf("buildRows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want one managed row", rows)
	}
	if rows[0].Value != "alpha" {
		t.Fatalf("row value = %q, want the exact tmux session name the opener targets", rows[0].Value)
	}
	if !strings.Contains(rows[0].Label, "alpha") {
		t.Fatalf("row label = %q, want the Registry Project name", rows[0].Label)
	}
	if got := attribution.byID["$1"].ResourceUID; got != runtimeFixtureProject {
		t.Fatalf("managed session resource = %q, want %q", got, runtimeFixtureProject)
	}
}

// TestSessionsKeepEverythingWhenAttributionIsUnavailable is the degradation
// rule: a read that could not classify anything must not hide rows an operator
// can see on their own screen.
func TestSessionsKeepEverythingWhenAttributionIsUnavailable(t *testing.T) {
	t.Parallel()

	command := &sessionsCommand{}
	attribution := command.attributeSessions(context.Background(), fixtureSessionSummaries())
	if len(attribution.managed) != 3 {
		t.Fatalf("managed sessions = %#v, want every observed session", attribution.managed)
	}
	if _, ok := attribution.runtimeLinkEntry(); ok {
		t.Fatal("an unresolved read offered a Runtime link it cannot populate")
	}
}

// TestSessionsWithoutATransportWithholdNothing pins the outside-tmux case: there
// is no observation to classify against, so no row is withheld on the strength
// of one that was never taken.
func TestSessionsWithoutATransportWithholdNothing(t *testing.T) {
	t.Parallel()

	command := sessionsRegistryFixture(t, "")
	attribution := command.attributeSessions(context.Background(), fixtureSessionSummaries())
	if len(attribution.managed) != 3 {
		t.Fatalf("managed sessions = %#v, want every summary kept with no observation", attribution.managed)
	}
	if attribution.withheld.Total() != 0 {
		t.Fatalf("withheld tally = %+v, want nothing withheld without an observation", attribution.withheld)
	}
}

// recentWindowRegistryFixture wires a recent-window command over the same host.
func recentWindowRegistryFixture(t *testing.T, inherited string) *recentWindowCommand {
	t.Helper()
	reader, _, _, _ := navigationFixtureReader(t, "1", inherited)
	return &recentWindowCommand{navigation: reader}
}

func fixtureRecentWindows() []recentwindows.Candidate {
	return []recentwindows.Candidate{
		{Snapshot: recentwindows.Snapshot{WindowID: "@2", Session: "alpha", WindowName: "editor"}},
		{Snapshot: recentwindows.Snapshot{WindowID: "@4", Session: "alpha", WindowName: "notes"}},
		{Snapshot: recentwindows.Snapshot{WindowID: "@6", Session: "alpha", WindowName: "ghost"}},
		{Snapshot: recentwindows.Snapshot{WindowID: "@9", Session: "Home", WindowName: "shell"}, IsCurrent: true},
	}
}

// TestRecentWindowsWithholdUnmanagedWindows is acceptance (3) on the
// recent-window surface, plus the one exception: the window an operator is
// standing in always stays, because the picker's own stay-here row is it.
func TestRecentWindowsWithholdUnmanagedWindows(t *testing.T) {
	t.Parallel()

	command := recentWindowRegistryFixture(t, "/tmp/fake-tmux/primary,0,0")
	attribution := command.attributeRecentWindows(context.Background(), fixtureRecentWindows())

	ids := make([]string, 0, len(attribution.managed))
	for _, candidate := range attribution.managed {
		ids = append(ids, candidate.WindowID)
	}
	if !reflect.DeepEqual(ids, []string{"@2", "@9"}) {
		t.Fatalf("managed windows = %#v, want the Registry Window and the current window", ids)
	}
	if got, want := attribution.withheld, (registryview.RuntimeCounts{Unattributed: 1, Recoverable: 1}); got != want {
		t.Fatalf("withheld tally = %+v, want %+v", got, want)
	}
	if got := attribution.resourceName(attribution.managed[0]); got != "editor" {
		t.Fatalf("managed window resource name = %q, want the Registry Window name", got)
	}
	item, ok := attribution.runtimeLinkItem()
	if !ok {
		t.Fatal("no Runtime link was offered for the withheld windows")
	}
	if item.Value != recentWindowRuntimeValue {
		t.Fatalf("runtime link value = %q, want the sentinel", item.Value)
	}
}

// TestRecentWindowsAnnotateManagedRowsWithTheRegistryName pins the badge: the
// title stays what an operator recognizes the window by, and the Registry name
// is added rather than substituted.
func TestRecentWindowsAnnotateManagedRowsWithTheRegistryName(t *testing.T) {
	t.Parallel()

	command := recentWindowRegistryFixture(t, "/tmp/fake-tmux/primary,0,0")
	attribution := command.attributeRecentWindows(context.Background(), fixtureRecentWindows())
	items, _, _ := recentWindowPickerItems(attribution.managed, command.currentTime(), "dot")
	items = attribution.annotate(items, attribution.managed)

	if len(items) != len(attribution.managed) {
		t.Fatalf("items = %d, want one per managed candidate", len(items))
	}
	if !contains(items[0].Badges, "editor") {
		t.Fatalf("managed window badges = %#v, want the Registry Window name", items[0].Badges)
	}
	if contains(items[1].Badges, "editor") {
		t.Fatalf("the current unmanaged window gained a Registry name: %#v", items[1].Badges)
	}
}

func contains(values []string, want string) bool {
	return slices.Contains(values, want)
}
