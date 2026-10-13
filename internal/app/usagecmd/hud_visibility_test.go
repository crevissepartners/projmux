package usagecmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/usage"
)

func TestHUDSnapshotsWithVisibilityMatchesSettingsForEveryToggle(t *testing.T) {
	caps := HUDProviderCapabilities()
	type query struct {
		provider aiprovider.ID
		window   string
	}
	var queries []query
	for _, cap := range caps {
		queries = append(queries, query{cap.ID, ""})
		for _, window := range cap.Windows {
			queries = append(queries, query{cap.ID, window.Key})
		}
	}
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	snaps := installedCacheFixture(now)
	snaps = append(snaps,
		usage.Snapshot{Model: "codex", Window: usage.Window5h, Bucket: "other", Pct: 90, Source: usage.SourceRollout},
		usage.Snapshot{Model: "codex", Window: usage.Window5h, Bucket: "codex", Pct: 10, Source: usage.SourceAppServer},
	)
	before, err := json.Marshal(snaps)
	if err != nil {
		t.Fatal(err)
	}
	for mask := 0; mask < 1<<len(queries); mask++ {
		t.Run(fmt.Sprint(mask), func(t *testing.T) {
			c := &Command{}
			isolateUsageCommandEnv(t, c)
			paths, err := c.hudVisibilityConfigPaths()
			if err != nil {
				t.Fatal(err)
			}
			values := make(map[query]bool)
			for i, q := range queries {
				values[q] = mask&(1<<i) != 0
				value := config.StatusbarVisibilityOff
				if values[q] {
					value = config.StatusbarVisibilityOn
				}
				path := paths.StatusbarAgentUsageProviderVisibilityFile(string(q.provider))
				if q.window != "" {
					path = paths.StatusbarAgentUsageWindowVisibilityFile(string(q.provider), q.window)
				}
				if err := config.SaveStatusbarVisibilityFile(path, value); err != nil {
					t.Fatal(err)
				}
			}
			var calls []query
			got := HUDSnapshotsWithVisibility(snaps, func(provider aiprovider.ID, window string) bool {
				q := query{provider, window}
				calls = append(calls, q)
				return values[q]
			})
			if !reflect.DeepEqual(calls, queries) {
				t.Fatalf("callback calls = %v, want %v", calls, queries)
			}
			if want := c.HUDSnapshots(snaps); !reflect.DeepEqual(got, want) {
				t.Fatalf("projection = %+v, settings = %+v", got, want)
			}
			for _, row := range got {
				if !values[query{aiprovider.ID(row.Model), ""}] || !values[query{aiprovider.ID(row.Model), string(row.Window)}] {
					t.Fatalf("hidden provider/window returned: %+v", row)
				}
			}
			after, err := json.Marshal(snaps)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("projection changed input")
			}
		})
	}
}

func TestHUDSnapshotsWithVisibilityDropsPlaceholdersAndReadsNoSettings(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	t.Setenv("HOME", missing)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(missing, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(missing, "state"))
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h},
		{Model: "claude", Window: usage.WindowWeekly, Limit: 100},
		{Model: "codex", Window: usage.Window5h, ResetsAt: time.Unix(1, 0), Source: usage.SourceRollout},
		{Model: "codex", Window: usage.WindowWeekly},
		{Model: "codex", Window: usage.WindowContext, Pct: 20},
		{Model: "unknown", Window: usage.WindowWeekly, Pct: 20},
	}
	visible := func(aiprovider.ID, string) bool { return true }
	got := HUDSnapshotsWithVisibility(snaps, visible)
	want := []usage.Snapshot{snaps[1], snaps[2]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projection = %+v, want %+v", got, want)
	}
	if !FallbackProvenance(got[1]) || FallbackProvenance(got[0]) {
		t.Fatal("provider provenance differs")
	}
	if got := HUDSnapshotsWithVisibility(nil, visible); len(got) != 0 {
		t.Fatalf("empty input = %+v", got)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("projection created %v (%v)", entries, err)
	}
}

func TestFallbackProvenanceMatchesHUDClassificationWithoutChangingInput(t *testing.T) {
	models := []string{"codex", " CODEX ", "claude", "unknown", ""}
	sources := []usage.SnapshotSource{usage.SourceAppServer, usage.SourceRollout, "future", ""}
	for _, model := range models {
		for _, source := range sources {
			for _, fallback := range []usage.SnapshotReason{"", usage.ReasonAppServerUnsupported, "future"} {
				for _, stale := range []usage.SnapshotReason{"", usage.ReasonAppServerDisconnected, "future"} {
					snap := usage.Snapshot{Model: model, Source: source, FallbackReason: fallback, StaleReason: stale}
					before := snap
					want := compactModelFallbackProvenance(snap)
					if got := FallbackProvenance(snap); got != want {
						t.Fatalf("provenance %+v = %v, HUD = %v", snap, got, want)
					}
					if !reflect.DeepEqual(snap, before) {
						t.Fatal("provenance changed input")
					}
				}
			}
		}
	}
}
