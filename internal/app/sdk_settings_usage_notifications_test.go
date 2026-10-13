package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/core/usage"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func settingsClient(t *testing.T, root string) (*projmux.Client, *configCommand, config.Paths) {
	t.Helper()
	home := func() (string, error) { return root, nil }
	env := func(string) string { return "" }
	paths, err := configPaths(home, env)
	if err != nil {
		t.Fatal(err)
	}
	client, err := projmux.NewClient(projmux.Options{ConfigDir: paths.ConfigDir, StateDir: paths.StateDir})
	if err != nil {
		t.Fatal(err)
	}
	return client, &configCommand{homeDir: home, lookupEnv: env}, paths
}
func settingsCLI(c *configCommand, key core.ConfigKey, args ...string) (string, error) {
	var out, stderr bytes.Buffer
	var err error
	switch key {
	case core.ConfigProviders:
		err = c.runProviders(args, &out, &stderr)
	case core.ConfigLocale:
		err = c.runLocale(args, &out, &stderr)
	case core.ConfigAgentQuestions:
		err = c.runAgentQuestions(args, &out, &stderr)
	case core.ConfigAgentHost:
		err = c.runAgentHost(args, &out, &stderr)
	case core.ConfigAgentApprovals:
		err = c.runAgentApprovals(args, &out, &stderr)
	}
	return out.String(), err
}
func settingsFiles(t *testing.T, p config.Paths) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(p.ConfigDir, func(path string, d os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !d.IsDir() {
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			rel, _ := filepath.Rel(p.ConfigDir, path)
			out[rel] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func settingLine(v core.ConfigValue, write bool, provider string) string {
	switch v.Key {
	case core.ConfigProviders:
		var b strings.Builder
		for _, p := range v.Providers {
			if provider != "" && p.ID != provider {
				continue
			}
			state := "disabled"
			if p.Enabled {
				state = "enabled"
			}
			fmt.Fprintf(&b, "%s %s\n", p.ID, state)
		}
		return b.String()
	case core.ConfigAgentHost:
		return "default " + v.DefaultHost + "\n"
	case core.ConfigLocale:
		if write {
			return "locale " + v.Locale + "\n"
		}
		return "locale " + v.Locale + " from " + v.Source + "\n"
	default:
		return fmt.Sprintf("answering %s window %s\n", v.Answering, v.Window)
	}
}
func TestSettingsSDKCLIParity(t *testing.T) {
	ctx := context.Background()
	c, _, sp := settingsClient(t, t.TempDir())
	_, cmd, cp := settingsClient(t, t.TempDir())
	for _, key := range []core.ConfigKey{core.ConfigProviders, core.ConfigLocale, core.ConfigAgentQuestions, core.ConfigAgentApprovals, core.ConfigAgentHost} {
		v, err := c.GetConfig(ctx, key)
		out, ce := settingsCLI(cmd, key)
		want := strings.ReplaceAll(settingLine(v, false, ""), sp.ConfigDir, cp.ConfigDir)
		if err != nil || ce != nil || out != want {
			t.Fatalf("read %s %q %q %v %v", key, out, want, err, ce)
		}
	}
	str := func(s string) *string { return &s }
	for _, tc := range []struct {
		u    core.ConfigUpdate
		args []string
	}{
		{core.ConfigUpdate{Key: core.ConfigProviders, Provider: "claude", Enabled: false}, []string{"--disable", "claude"}},
		{core.ConfigUpdate{Key: core.ConfigProviders, Provider: "claude", Enabled: true}, []string{"--enable", "claude"}},
		{core.ConfigUpdate{Key: core.ConfigProviders, Provider: "claude", Enabled: true}, []string{"--enable", "claude"}},
		{core.ConfigUpdate{Key: core.ConfigAgentHost, DefaultHost: str(" Process ")}, []string{"--default", " Process "}},
		{core.ConfigUpdate{Key: core.ConfigAgentHost, DefaultHost: str("tmux")}, []string{"--default", "tmux"}},
		{core.ConfigUpdate{Key: core.ConfigLocale, Locale: "ko-KR"}, []string{"--set", "ko-KR"}},
		{core.ConfigUpdate{Key: core.ConfigLocale, Locale: "auto"}, []string{"--set", "auto"}},
		{core.ConfigUpdate{Key: core.ConfigAgentQuestions, Answering: str("projmux"), Window: str("unlimited")}, []string{"--answering", "projmux", "--window", "unlimited"}},
		{core.ConfigUpdate{Key: core.ConfigAgentQuestions, Window: str("60")}, []string{"--window", "60"}},
		{core.ConfigUpdate{Key: core.ConfigAgentApprovals, Answering: str("projmux"), Window: str("60")}, []string{"--answering", "projmux", "--window", "60"}},
	} {
		v, err := c.SetConfig(ctx, tc.u)
		out, ce := settingsCLI(cmd, tc.u.Key, tc.args...)
		if err != nil || ce != nil || out != settingLine(v, true, tc.u.Provider) {
			t.Fatalf("set %+v %q %+v %v %v", tc.u, out, v, err, ce)
		}
		if !reflect.DeepEqual(settingsFiles(t, sp), settingsFiles(t, cp)) {
			t.Fatal("stored bytes differ")
		}
	}
	for _, key := range []core.ConfigKey{core.ConfigProviders, core.ConfigLocale, core.ConfigAgentQuestions, core.ConfigAgentApprovals, core.ConfigAgentHost} {
		v, _ := c.GetConfig(ctx, key)
		out, err := settingsCLI(cmd, key)
		if err != nil || out != strings.ReplaceAll(settingLine(v, false, ""), sp.ConfigDir, cp.ConfigDir) {
			t.Fatalf("read after set %s", key)
		}
	}
	// All providers disabled remains authoritative rather than reverting to defaults.
	for _, p := range config.KnownAIAgentProviders() {
		if _, err := c.SetConfig(ctx, core.ConfigUpdate{Key: core.ConfigProviders, Provider: string(p)}); err != nil {
			t.Fatal(err)
		}
		if _, err := settingsCLI(cmd, core.ConfigProviders, "--disable", string(p)); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(settingsFiles(t, sp), settingsFiles(t, cp)) {
		t.Fatal("all-disabled bytes differ")
	}
}
func TestSettingsSDKRefusalParity(t *testing.T) {
	c, cmd, p := settingsClient(t, t.TempDir())
	str := func(s string) *string { return &s }
	for _, tc := range []struct {
		u    core.ConfigUpdate
		args []string
	}{
		{core.ConfigUpdate{Key: core.ConfigProviders, Provider: "unknown"}, []string{"--disable", "unknown"}},
		{core.ConfigUpdate{Key: core.ConfigProviders, Provider: "", Enabled: true}, []string{"--enable", ""}},
		{core.ConfigUpdate{Key: core.ConfigAgentHost, DefaultHost: str("bad")}, []string{"--default", "bad"}},
		{core.ConfigUpdate{Key: core.ConfigAgentHost, DefaultHost: str("")}, []string{"--default", ""}},
		{core.ConfigUpdate{Key: core.ConfigLocale, Locale: "invalid"}, []string{"--set", "invalid"}},
		{core.ConfigUpdate{Key: core.ConfigLocale}, []string{"--set", ""}},
		{core.ConfigUpdate{Key: core.ConfigAgentQuestions, Answering: str("bad")}, []string{"--answering", "bad"}},
		{core.ConfigUpdate{Key: core.ConfigAgentQuestions, Window: str("")}, []string{"--window", ""}},
		{core.ConfigUpdate{Key: core.ConfigAgentQuestions, Answering: str("projmux"), Window: str("0")}, []string{"--answering", "projmux", "--window", "0"}},
		{core.ConfigUpdate{Key: core.ConfigAgentApprovals, Answering: str("bad")}, []string{"--answering", "bad"}},
		{core.ConfigUpdate{Key: core.ConfigAgentApprovals, Answering: str("projmux"), Window: str("unlimited")}, []string{"--answering", "projmux", "--window", "unlimited"}},
	} {
		before := settingsFiles(t, p)
		_, err := c.SetConfig(context.Background(), tc.u)
		_, ce := settingsCLI(cmd, tc.u.Key, tc.args...)
		var r *projmux.Refusal
		if !errors.As(err, &r) || r.Code != projmux.CodeConfigValueInvalid || ce == nil || sdkSettingMessageMismatch(err, ce) || !IsUsageError(ce) {
			t.Fatalf("refusal %+v %v %v", tc.u, err, ce)
		}
		if !reflect.DeepEqual(before, settingsFiles(t, p)) {
			t.Fatal("refused value wrote a file")
		}
	}
	_, err := c.GetConfig(context.Background(), "missing")
	var r *projmux.Refusal
	if !errors.As(err, &r) || r.Code != projmux.CodeConfigKeyInvalid {
		t.Fatal(err)
	}
}
func encodedJSON(t *testing.T, v any) string {
	t.Helper()
	var out bytes.Buffer
	e := json.NewEncoder(&out)
	e.SetIndent("", "  ")
	if err := e.Encode(v); err != nil {
		t.Fatal(err)
	}
	return out.String()
}
func TestStoredUsageSDKParity(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("PROJMUX_USAGE_STATE_DIR", "")
	c, _, p := settingsClient(t, root)
	now := time.Now().UTC().Truncate(time.Second)
	dir := filepath.Join(p.StateDir, "usage")
	store := usage.NewStore(dir)
	reset := int64(25)
	id := "quota-id"
	surface := "cli"
	state := usage.State{Snapshots: []usage.Snapshot{
		{Model: "claude", Window: usage.WindowWeekly, Pct: 40, UpdatedAt: now},
		{Model: "codex", Window: usage.WindowQuota, Bucket: "named", Pct: 23, ResetInSeconds: &reset, UpdatedAt: now, RateLimit: &usage.RateLimitMetadata{LimitID: &id, CadenceMinutes: &reset}, NamedQuota: &usage.NamedQuota{Kind: "quota", Group: "named", Scope: &usage.NamedQuotaScope{Surface: &surface}}},
		{Model: "antigravity", Window: usage.WindowContext, Pct: 88, UpdatedAt: now},
	}, LastCollect: map[string]time.Time{"claude": now, "antigravity": now}, Backoff: map[string]usage.BackoffState{"claude": {Until: now.Add(time.Hour), Consecutive: 2}}}
	if err := store.SaveState(state); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(store.FilePath())
	info, _ := os.Stat(store.FilePath())
	cmd := usagecmd.New(func() time.Time { return now })
	raw, unsupported, mtime, err := cmd.CachedState()
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.GetUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bar := statusbarUsageStateFromCache(raw, mtime)
	if encodedJSON(t, got.Snapshots) != encodedJSON(t, bar.Snapshots) || encodedJSON(t, got.LastCollect) != encodedJSON(t, raw.LastCollect) || encodedJSON(t, got.Backoff) != encodedJSON(t, raw.Backoff) || encodedJSON(t, got.Unsupported) != encodedJSON(t, unsupported) || !got.CacheModTime.Equal(mtime) {
		t.Fatalf("cache parity %+v %+v", got, bar)
	}
	got.Snapshots[0].Pct = 99
	got.LastCollect["claude"] = time.Time{}
	got.Backoff["claude"] = core.BackoffState{}
	for i := range got.Snapshots {
		if got.Snapshots[i].RateLimit != nil {
			*got.Snapshots[i].RateLimit.LimitID = "changed"
			*got.Snapshots[i].NamedQuota.Scope.Surface = "changed"
		}
	}
	again, _ := c.GetUsage(context.Background())
	if encodedJSON(t, again.Snapshots) != encodedJSON(t, bar.Snapshots) {
		t.Fatal("cache aliases returned pointers")
	}
	after, _ := os.ReadFile(store.FilePath())
	afterInfo, _ := os.Stat(store.FilePath())
	if !bytes.Equal(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("cache contents or collection metadata changed")
	}
	points := []usage.MetricPoint{{Name: "usage.percent", Value: 40, ObservedAt: now.Add(-time.Minute), Provider: "claude", Window: "weekly", ResetsAt: &now}, {Name: "usage.percent", Value: 23, ObservedAt: now, Provider: "codex", Window: "quota", Bucket: "named"}}
	if err := store.AppendHistory(points, now); err != nil {
		t.Fatal(err)
	}
	for _, f := range []core.UsageHistoryFilter{{Now: now}, {Provider: "claude", Now: now}, {Provider: "codex", Window: "quota", Metric: "usage.percent", Now: now}, {Metric: "absent", Now: now}} {
		args := []string{"--history", "--json"}
		if f.Provider != "" {
			args = append(args, "--model", f.Provider)
		}
		if f.Window != "" {
			args = append(args, "--window", f.Window)
		}
		if f.Metric != "" {
			args = append(args, "--metric", f.Metric)
		}
		var out, stderr bytes.Buffer
		if err := cmd.Run(args, &out, &stderr); err != nil {
			t.Fatal(err)
		}
		history, err := c.GetUsageHistory(context.Background(), f)
		var compact bytes.Buffer
		encoder := json.NewEncoder(&compact)
		if encodeErr := encoder.Encode(history); encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if err != nil || compact.String() != out.String() {
			t.Fatalf("history parity %v %s %s", err, compact.String(), out.String())
		}
	}
	// SDK's explicit root must win over a subsequently changed process override.
	t.Setenv("PROJMUX_USAGE_STATE_DIR", t.TempDir())
	again, _ = c.GetUsage(context.Background())
	if len(again.Snapshots) != 2 {
		t.Fatal("captured root changed")
	}
}
func TestNotificationsSDKCLIParity(t *testing.T) {
	c, _, p := settingsClient(t, t.TempDir())
	store := notify.NewDefaultStore(p)
	for i, v := range []struct{ sev, src string }{{"info", "ai"}, {"critical", "git"}, {"warn", "external"}} {
		_, _, err := store.Push(notify.PushInput{ID: fmt.Sprintf("n%d", i), Text: "message", Severity: v.sev, Source: v.src, Target: notify.Target{Session: "saved-session"}, Metadata: map[string]string{"custom": "value"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	cmd := &notifyCommand{store: store, homeDir: func() (string, error) { return filepath.Dir(filepath.Dir(p.ConfigDir)), nil }, lookupEnv: func(string) string { return "" }}
	for _, f := range []core.NotificationFilter{{}, {Severities: []string{"info", "critical"}, Sources: []string{"ai", "git"}}, {Limit: 1}, {Sources: []string{"k8s"}}} {
		args := []string{"--json", "--limit", fmt.Sprint(f.Limit)}
		for _, v := range f.Severities {
			args = append(args, "--severity", v)
		}
		for _, v := range f.Sources {
			args = append(args, "--source", v)
		}
		var out, stderr bytes.Buffer
		if err := cmd.runList(args, &out, &stderr); err != nil {
			t.Fatal(err)
		}
		got, err := c.ListNotifications(context.Background(), f)
		if err != nil || out.String() != encodedJSON(t, got) {
			t.Fatalf("notification list %v %s %s", err, out.String(), encodedJSON(t, got))
		}
		if len(got) > 0 {
			got[0].Metadata["custom"] = "changed"
			again, _ := c.ListNotifications(context.Background(), f)
			if again[0].Metadata["custom"] != "value" {
				t.Fatal("queue aliases returned map")
			}
		}
	}
	for _, f := range []core.NotificationFilter{{Limit: -1}, {Severities: []string{"bad"}}, {Sources: []string{"bad"}}} {
		args := []string{"--limit", fmt.Sprint(f.Limit)}
		for _, v := range f.Severities {
			args = append(args, "--severity", v)
		}
		for _, v := range f.Sources {
			args = append(args, "--source", v)
		}
		var out, stderr bytes.Buffer
		ce := cmd.runList(args, &out, &stderr)
		_, err := c.ListNotifications(context.Background(), f)
		var r *projmux.Refusal
		if !errors.As(err, &r) || ce == nil || (r.Code != projmux.CodeNotificationLimitInvalid && err.Error() != ce.Error() || r.Code == projmux.CodeNotificationLimitInvalid && err.Error() != "NotificationFilter.Limit must be >= 0") || !IsUsageError(ce) {
			t.Fatalf("filter refusal %v %v", err, ce)
		}
	}
	// Clone exact queue bytes to compare acknowledgement state and messages.
	queue, _ := os.ReadFile(store.Path())
	_, _, other := settingsClient(t, t.TempDir())
	cliStore := notify.NewDefaultStore(other)
	if err := os.MkdirAll(filepath.Dir(cliStore.Path()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cliStore.Path(), queue, 0600); err != nil {
		t.Fatal(err)
	}
	cmd.store = cliStore
	for _, a := range []core.NotificationAck{{ID: "n1"}, {ID: "missing"}, {All: true}, {All: true}} {
		args := []string{a.ID}
		if a.All {
			args = []string{"--all"}
		}
		var out, stderr bytes.Buffer
		ce := cmd.runAck("notification ack", args, &out, &stderr)
		n, err := c.AckNotifications(context.Background(), a)
		if a.ID == "missing" {
			var r *projmux.Refusal
			if !errors.As(err, &r) || r.Code != projmux.CodeNotificationNotFound || ce == nil || err.Error() != ce.Error() {
				t.Fatalf("missing ack %v %v", err, ce)
			}
		} else {
			want := "ack " + a.ID + "\n"
			if a.All {
				want = fmt.Sprintf("ack %d notification(s)\n", n)
			}
			if err != nil || ce != nil || out.String() != want {
				t.Fatalf("ack %v %v %q", err, ce, out.String())
			}
		}
		sdkBytes, _ := os.ReadFile(store.Path())
		cliBytes, _ := os.ReadFile(cliStore.Path())
		if !bytes.Equal(sdkBytes, cliBytes) {
			t.Fatal("ack stored state differs")
		}
	}
}
func TestSettingsUsageNotificationsSDKIsolationAndCancellation(t *testing.T) {
	t.Setenv("PROJMUX_USAGE_STATE_DIR", t.TempDir())
	beforeEnv := os.Environ()
	a, _, ap := settingsClient(t, t.TempDir())
	b, _, bp := settingsClient(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.SetConfig(ctx, core.ConfigUpdate{Key: core.ConfigLocale, Locale: "ko-KR"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := a.GetConfig(ctx, core.ConfigProviders); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := a.GetUsage(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := a.GetUsageHistory(ctx, core.UsageHistoryFilter{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := a.ListNotifications(ctx, core.NotificationFilter{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := a.AckNotifications(ctx, core.NotificationAck{All: true}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(settingsFiles(t, ap)) != 0 {
		t.Fatal("cancelled config wrote")
	}
	if _, err := a.SetConfig(context.Background(), core.ConfigUpdate{Key: core.ConfigLocale, Locale: "ko-KR"}); err != nil {
		t.Fatal(err)
	}
	v, _ := b.GetConfig(context.Background(), core.ConfigLocale)
	if v.Locale != "auto" || len(settingsFiles(t, bp)) != 0 {
		t.Fatal("clients share settings")
	}
	empty, err := b.GetUsage(context.Background())
	if err != nil || len(empty.Snapshots) != 0 {
		t.Fatal("missing usage", err)
	}
	history, err := b.GetUsageHistory(context.Background(), core.UsageHistoryFilter{})
	if err != nil || history == nil || len(history) != 0 {
		t.Fatal("empty history", err)
	}
	if !reflect.DeepEqual(beforeEnv, os.Environ()) {
		t.Fatal("SDK changed environment")
	}
}

func TestNotificationsSDKAckAllStorageErrorParity(t *testing.T) {
	client, _, paths := settingsClient(t, t.TempDir())
	store := notify.NewDefaultStore(paths)
	// A directory at the queue file makes both readers fail before any write.
	if err := os.MkdirAll(store.Path(), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := newCmd(store)
	cliErr := cmd.Run([]string{"ack", "--all"}, &bytes.Buffer{}, &bytes.Buffer{})
	_, sdkErr := client.AckNotifications(context.Background(), core.NotificationAck{All: true})
	if cliErr == nil || sdkErr == nil || cliErr.Error() != sdkErr.Error() || !strings.HasPrefix(cliErr.Error(), "ack all notifications: ") {
		t.Fatalf("CLI = %v SDK = %v", cliErr, sdkErr)
	}
}
