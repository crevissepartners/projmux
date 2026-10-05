package usagecmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
)

func TestUsageHistoryReadsOnlyWithoutManagerOrCollection(t *testing.T) {
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	store := usage.NewStore(dir)
	if err := store.AppendHistory([]usage.MetricPoint{
		{Name: "usage.percent", Value: 12, ObservedAt: now, Provider: "claude", Window: "5h"},
		{Name: "agent.live.count", Value: 2, ObservedAt: now, Provider: "codex"},
	}, now); err != nil {
		t.Fatal(err)
	}
	c := New(func() time.Time { return now })
	c.lookupEnv = func(key string) string {
		if key == StateDirEnvVar {
			return dir
		}
		return ""
	}
	c.managerFn = func([]string) (*usage.Manager, error) {
		t.Fatal("history constructed collection manager")
		return nil, nil
	}
	var out, stderr bytes.Buffer
	if err := c.Run([]string{"--history", "--json", "--model", "claude", "--window", "5h", "--metric", "usage.percent"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, `"provider":"claude"`) || strings.Contains(got, `"codex"`) {
		t.Fatalf("filtered JSON: %s", got)
	}
	out.Reset()
	stderr.Reset()
	if err := c.Run([]string{"--history", "--force"}, &out, &stderr); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("force not refused: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("force wrote output: %s", out.String())
	}
}
