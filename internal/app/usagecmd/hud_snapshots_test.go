package usagecmd

import (
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/usage"
)

func TestHUDSnapshotsFollowSettingsVisibilityAndOneRowPerWindow(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home}
	c := &Command{now: time.Now, lookupEnv: func(key string) string { return env[key] }}
	paths, err := c.hudVisibilityConfigPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveStatusbarVisibilityFile(paths.StatusbarAgentUsageProviderVisibilityFile("antigravity"), config.StatusbarVisibilityOff); err != nil {
		t.Fatal(err)
	}
	reset := time.Now().Add(time.Hour)
	snaps := []usage.Snapshot{
		{Model: "codex", Window: usage.WindowWeekly, Bucket: "other", Pct: 0, ResetsAt: reset},
		{Model: "codex", Window: usage.WindowWeekly, Bucket: "codex", Pct: 40, ResetsAt: reset},
		{Model: "codex", Window: usage.Window5h, Bucket: "codex", Pct: 10, ResetsAt: reset},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 96, ResetsAt: reset},
		{Model: "claude", Window: usage.Window5h, Pct: 75, ResetsAt: reset},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "gemini-weekly", Pct: 5, ResetsAt: reset},
	}
	got := c.HUDSnapshots(snaps)
	want := []struct {
		model  string
		window usage.Window
		pct    float64
	}{
		{"claude", usage.Window5h, 75},
		{"claude", usage.WindowWeekly, 96},
		{"codex", usage.WindowWeekly, 40},
	}
	if len(got) != len(want) {
		t.Fatalf("HUDSnapshots = %+v, want %d rows", got, len(want))
	}
	for i, w := range want {
		if got[i].Model != w.model || got[i].Window != w.window || got[i].Pct != w.pct {
			t.Fatalf("row %d = %s %s %v, want %+v", i, got[i].Model, got[i].Window, got[i].Pct, w)
		}
	}
}
