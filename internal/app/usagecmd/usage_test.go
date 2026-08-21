package usagecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/usage"
	"github.com/crevissepartners/projmux/internal/theme"
	intrender "github.com/crevissepartners/projmux/internal/ui/render"
)

// installedCacheFixture mirrors the sanitized shape of the installed cache:
// Claude 5h+weekly, weekly-only Codex, and Antigravity context plus its two
// exact named quota buckets. All rows are percent-only (Tokens/Limit zero).
func installedCacheFixture(now time.Time) []usage.Snapshot {
	modelID := "model-redacted-id"
	return []usage.Snapshot{
		{Model: "antigravity", Window: usage.WindowContext, Pct: 37, UpdatedAt: now},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "3p-weekly", Pct: 24, ResetsAt: now.Add(4 * 24 * time.Hour), UpdatedAt: now},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "gemini-weekly", Pct: 61, ResetsAt: now.Add(5 * 24 * time.Hour), UpdatedAt: now},
		{Model: "claude", Window: usage.Window5h, Pct: 18, ResetsAt: now.Add(3 * time.Hour), UpdatedAt: now},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 42, ResetsAt: now.Add(6 * 24 * time.Hour), UpdatedAt: now},
		{Model: "claude", Window: usage.WindowQuota, Bucket: "group-redacted-model", Pct: 73, ResetsAt: now.Add(2 * 24 * time.Hour), UpdatedAt: now, NamedQuota: &usage.NamedQuota{Kind: "kind-redacted", Group: "group-redacted-model", Severity: "severity-redacted", IsActive: true, Scope: &usage.NamedQuotaScope{Model: &usage.NamedQuotaModel{ID: &modelID, DisplayName: "Model Redacted Alpha"}}}},
		{Model: "codex", Window: usage.WindowWeekly, Pct: 12, ResetsAt: now.Add(6 * 24 * time.Hour), UpdatedAt: now},
	}
}

func TestCodexNativeUsageJSONTableAndHUDShareValueSourceAndReasons(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	label := "General Codex"
	limitID := "codex"
	cadence := int64(300)
	rows := []usage.Snapshot{
		{
			Model: "codex", Window: usage.Window5h, Bucket: "alternate", Pct: 88,
			ResetsAt: time.Unix(1787380200, 0).UTC(), UpdatedAt: now,
			Source: usage.SourceAppServer,
			RateLimit: &usage.RateLimitMetadata{
				BucketKey: "alternate", LimitID: &limitID, Label: &label,
				Slot: "primary", CadenceMinutes: &cadence,
			},
		},
		{
			Model: "codex", Window: usage.Window5h, Bucket: "codex", Pct: 12,
			ResetsAt: time.Unix(1787380300, 0).UTC(), UpdatedAt: now,
			Source: usage.SourceAppServer,
			RateLimit: &usage.RateLimitMetadata{
				BucketKey: "codex", LimitID: &limitID, Label: &label,
				Slot: "primary", CadenceMinutes: &cadence,
			},
		},
	}

	var jsonOut bytes.Buffer
	if err := writeUsageJSON(&jsonOut, rows, usage.State{}, now); err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(jsonOut.Bytes(), &decoded); err != nil {
		t.Fatalf("public JSON shape changed from array: %v\n%s", err, jsonOut.String())
	}
	if len(decoded) != 2 || decoded[0]["source"] != "app-server" {
		t.Fatalf("JSON provenance = %#v", decoded)
	}
	rateLimit, ok := decoded[0]["rate_limit"].(map[string]any)
	if !ok || rateLimit["label"] != label {
		t.Fatalf("JSON label metadata = %#v", decoded[0]["rate_limit"])
	}

	var table bytes.Buffer
	if err := writeUsageTable(&table, rows, now); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SOURCE", "REASON", "app-server", "5h/codex · General Codex", "12%"} {
		if !strings.Contains(table.String(), want) {
			t.Fatalf("usage table missing %q:\n%s", want, table.String())
		}
	}

	hud := formatStatusUsage(rows, 0, now)
	for _, want := range []string{"Codex[app-server]", "12%"} {
		if !strings.Contains(hud, want) {
			t.Fatalf("HUD missing %q: %q", want, hud)
		}
	}
	if strings.Contains(hud, "88%") {
		t.Fatalf("HUD selected non-canonical bucket: %q", hud)
	}
}

func TestCodexFallbackAndLastKnownGoodReasonsRemainVisible(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	fallback := usage.Snapshot{
		Model: "codex", Window: usage.Window5h, Pct: 17,
		UpdatedAt: now, Source: usage.SourceRollout,
		FallbackReason: usage.ReasonAppServerUnsupported,
	}
	if got := formatStatusUsage([]usage.Snapshot{fallback}, 0, now); !strings.Contains(got, "rollout/fallback:app-server-unsupported") {
		t.Fatalf("fallback HUD = %q", got)
	}
	stale := fallback
	stale.StaleReason = usage.ReasonAppServerDisconnected
	if got := formatStatusUsage([]usage.Snapshot{stale}, 0, now); !strings.Contains(got, "rollout/stale:app-server-disconnected") {
		t.Fatalf("last-known-good HUD = %q", got)
	}

	var table bytes.Buffer
	if err := writeUsageTable(&table, []usage.Snapshot{stale}, now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table.String(), "app-server-disconnected") {
		t.Fatalf("last-known-good table = %q", table.String())
	}
}

func TestCommandCodexJSONAndStatusHUDSelectSameCanonicalNativeRow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	native := &stubAdapter{name: "codex", snaps: []usage.Snapshot{
		{
			Model: "codex", Window: usage.Window5h, Bucket: "alternate", Pct: 88,
			UpdatedAt: now, Source: usage.SourceAppServer,
		},
		{
			Model: "codex", Window: usage.Window5h, Bucket: "codex", Pct: 12,
			UpdatedAt: now, Source: usage.SourceAppServer,
		},
	}}
	registry := usage.NewRegistry()
	if err := registry.Register(native); err != nil {
		t.Fatal(err)
	}
	manager := usage.NewManager(registry, usage.NewStore(t.TempDir()), func() time.Time { return now })
	command := New(func() time.Time { return now })
	command.managerFn = func([]string) (*usage.Manager, error) { return manager, nil }
	command.enabledAgentsFn = func() ([]config.AIAgentProvider, error) {
		return []config.AIAgentProvider{config.AIAgentCodex}, nil
	}
	isolatedHome := t.TempDir()
	command.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return isolatedHome
		case "XDG_CONFIG_HOME":
			return filepath.Join(isolatedHome, "config")
		case "XDG_STATE_HOME":
			return filepath.Join(isolatedHome, "state")
		default:
			return ""
		}
	}

	var jsonOut, jsonErr bytes.Buffer
	if err := command.Run([]string{"--model", "codex", "--json"}, &jsonOut, &jsonErr); err != nil {
		t.Fatal(err)
	}
	var rows []usage.Snapshot
	if err := json.Unmarshal(jsonOut.Bytes(), &rows); err != nil {
		t.Fatalf("command JSON no longer decodes as snapshot array: %v\n%s", err, jsonOut.String())
	}
	var canonical usage.Snapshot
	for _, row := range rows {
		if row.Bucket == "codex" && row.Window == usage.Window5h {
			canonical = row
			break
		}
	}
	if canonical.Pct != 12 || canonical.Source != usage.SourceAppServer {
		t.Fatalf("canonical CLI row = %#v", canonical)
	}

	var hudOut, hudErr bytes.Buffer
	if err := command.RunStatus(nil, &hudOut, &hudErr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hudOut.String(), "Codex[app-server]") || !strings.Contains(hudOut.String(), "12%") {
		t.Fatalf("status HUD disagrees with CLI canonical row: %q", hudOut.String())
	}
	if strings.Contains(hudOut.String(), "88%") {
		t.Fatalf("status HUD selected another bucket: %q", hudOut.String())
	}
	if native.collectCalls != 1 {
		t.Fatalf("HUD bypassed Manager throttle: collect calls = %d", native.collectCalls)
	}
}

// The tests below drive the renderer through usageSegmentPlan values rather
// than through named tiers: after the move to element-priority degradation
// there is no whole-segment tier to call, only a plan with some optional
// elements switched off. These four constructors name the plans the old tier
// functions used to be, so the assertions stay readable.

// usagePlanCompactAge drops every age TEXT, leaving the `~` / `~~` marker.
func usagePlanCompactAge(models []modelDisplay) usageSegmentPlan {
	plan := newUsageSegmentPlan(models)
	for i := range models {
		plan.ageText[i] = false
	}
	return plan
}

// usagePlanOfficialOnly additionally drops every provider's second window, so
// only the official window bar (5h, otherwise weekly) is left.
func usagePlanOfficialOnly(models []modelDisplay) usageSegmentPlan {
	plan := usagePlanCompactAge(models)
	for i := range models {
		plan.secondary[i] = false
	}
	return plan
}

// usagePlanTextLong drops the bars, keeping the long labels.
func usagePlanTextLong(models []modelDisplay) usageSegmentPlan {
	plan := usagePlanOfficialOnly(models)
	plan.bars = false
	return plan
}

// usagePlanTextShort additionally drops the long labels.
func usagePlanTextShort(models []modelDisplay) usageSegmentPlan {
	plan := usagePlanTextLong(models)
	plan.longLabels = false
	return plan
}

func TestFormatStatusUsageRendersBothModelsHUD(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 42.0, ResetsAt: now.Add(5 * time.Hour), UpdatedAt: now},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 18.0, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now},
		{Model: "codex", Window: usage.Window5h, Pct: 71.0, ResetsAt: now.Add(5 * time.Hour), UpdatedAt: now},
		{Model: "codex", Window: usage.WindowWeekly, Pct: 55.0, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now},
	}
	got := formatStatusUsage(snaps, 0, now)

	if !strings.Contains(got, "Claude") || !strings.Contains(got, "Codex") {
		t.Fatalf("missing model labels: %q", got)
	}
	if !strings.Contains(got, "5h ") || !strings.Contains(got, "weekly ") {
		t.Fatalf("missing window labels: %q", got)
	}
	if !strings.Contains(got, "█") || !strings.Contains(got, "░") {
		t.Fatalf("missing bar runes: %q", got)
	}
	for _, want := range []string{"42%", "18%", "71%", "55%"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if !strings.Contains(got, "#[fg="+theme.TmuxAccentAIFg+",bold]") {
		t.Fatalf("missing AI label color: %q", got)
	}
	if !strings.HasSuffix(got, "#[default]") {
		t.Fatalf("must end with #[default]: %q", got)
	}
}

func TestProjectStatusSnapshotsOfficialWindowsOnly(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 10, UpdatedAt: now},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 20, UpdatedAt: now},
		{Model: "codex", Window: usage.WindowWeekly, Pct: 30, UpdatedAt: now},
		{Model: "antigravity", Window: usage.WindowContext, Pct: 40, UpdatedAt: now},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "3p-weekly", Pct: 50, UpdatedAt: now},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "gemini-weekly", Pct: 60, UpdatedAt: now},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "future-bucket", Pct: 70, UpdatedAt: now},
	}
	got := projectStatusSnapshots(snaps)
	want := []struct {
		model  string
		window usage.Window
		pct    float64
	}{
		{"antigravity", usage.WindowWeekly, 60},
		{"claude", usage.Window5h, 10},
		{"claude", usage.WindowWeekly, 20},
		{"codex", usage.WindowWeekly, 30},
	}
	if len(got) != len(want) {
		t.Fatalf("projection = %#v, want %d rows", got, len(want))
	}
	for i := range want {
		if got[i].Model != want[i].model || got[i].Window != want[i].window || got[i].Pct != want[i].pct || got[i].Bucket != "" {
			t.Fatalf("projection[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
	// Projection must not rewrite the lossless source snapshot identity.
	if snaps[5].Window != usage.WindowQuota || snaps[5].Bucket != "gemini-weekly" {
		t.Fatalf("source identity mutated: %#v", snaps[5])
	}
}

func TestFormatStatusUsageCurrentCacheExcludesNonOfficialRows(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC)
	got := intrender.StripTmuxEscapes(formatStatusUsage(installedCacheFixture(now), 120, now))
	for _, want := range []string{"Claude", "Codex", "Antigravity", "weekly", "61%"} {
		if !strings.Contains(got, want) {
			t.Fatalf("HUD = %q, missing %q", got, want)
		}
	}
	for _, excluded := range []string{"ctx", "quota/", "3p-weekly", "gemini-weekly", "future-bucket", "group-redacted-model", "Model Redacted Alpha", "73%"} {
		if strings.Contains(got, excluded) {
			t.Fatalf("HUD = %q, leaked %q", got, excluded)
		}
	}
}

func TestBucketDisplayIDEscapesWithoutLabelCollision(t *testing.T) {
	t.Parallel()
	ids := []string{"#", `\x23`, "\n", `\n`, "\x1b", `\x1b`, "\t", `\t`}
	seen := map[string]string{}
	for _, id := range ids {
		label := BucketDisplayID(id)
		if prior, ok := seen[label]; ok {
			t.Fatalf("IDs %q and %q collide at label %q", prior, id, label)
		}
		seen[label] = id
		if strings.ContainsAny(label, "\n\r\t\x1b") || strings.Contains(label, "#") {
			t.Fatalf("unsafe label for %q: %q", id, label)
		}
	}
}

func TestSnapshotWindowLabelDistinguishesNamedModelAndBoundsDisplayOnly(t *testing.T) {
	t.Parallel()
	rawGroup := strings.Repeat("group-redacted-", 8) + "\n#[unsafe]"
	rawDisplay := strings.Repeat("Model Redacted ", 8) + "\x1b[31m"
	snapshot := usage.Snapshot{
		Model: "claude", Window: usage.WindowQuota, Bucket: rawGroup,
		NamedQuota: &usage.NamedQuota{
			Kind: "kind-redacted", Group: rawGroup, Severity: "severity-redacted", IsActive: false,
			Scope: &usage.NamedQuotaScope{Model: &usage.NamedQuotaModel{DisplayName: rawDisplay}},
		},
	}
	label := SnapshotWindowLabel(snapshot)
	if len([]rune(label)) > 72 {
		t.Fatalf("label length = %d, want <= 72: %q", len([]rune(label)), label)
	}
	if strings.ContainsAny(label, "\n\r\t\x1b") || strings.Contains(label, "#") {
		t.Fatalf("unsafe display label: %q", label)
	}
	if !strings.Contains(label, "quota/group-redacted") || !strings.Contains(label, "Model Redacted") || !strings.Contains(label, "[inactive]") {
		t.Fatalf("named/model/inactive distinction missing: %q", label)
	}
	if snapshot.Bucket != rawGroup || snapshot.NamedQuota.Group != rawGroup || snapshot.NamedQuota.Scope.Model.DisplayName != rawDisplay {
		t.Fatalf("display bounding mutated stored opaque identity: %#v", snapshot)
	}
}

func TestNamedQuotaTextAndJSONPreserveIdentityResetFreshnessWithoutCounts(t *testing.T) {
	t.Parallel()
	now := time.Date(2031, 2, 3, 4, 5, 6, 0, time.UTC)
	reset := now.Add(2 * time.Hour)
	modelID := "model-redacted-id"
	snapshot := usage.Snapshot{
		Model: "claude", Window: usage.WindowQuota, Bucket: "group-redacted-model", Pct: 37.5,
		ResetsAt: reset, UpdatedAt: now.Add(-11 * time.Minute),
		NamedQuota: &usage.NamedQuota{
			Kind: "kind-redacted", Group: "group-redacted-model", Severity: "severity-redacted", IsActive: false,
			Scope: &usage.NamedQuotaScope{Model: &usage.NamedQuotaModel{ID: &modelID, DisplayName: "Model Redacted Alpha"}},
		},
	}
	var table bytes.Buffer
	if err := writeUsageTable(&table, []usage.Snapshot{snapshot}, now); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"quota/group-redacted-model · Model Redacted Alpha [inactive]", "38%", reset.Local().Format(time.RFC3339), "*"} {
		if !strings.Contains(table.String(), want) {
			t.Fatalf("table = %q, missing %q", table.String(), want)
		}
	}
	if strings.Contains(table.String(), "Tokens") || strings.Contains(table.String(), "Limit") {
		t.Fatalf("percent-only table synthesized counts: %q", table.String())
	}
	var output bytes.Buffer
	if err := writeUsageJSON(&output, []usage.Snapshot{snapshot}, usage.State{}, now); err != nil {
		t.Fatal(err)
	}
	jsonText := output.String()
	for _, want := range []string{`"bucket": "group-redacted-model"`, `"group": "group-redacted-model"`, `"is_active": false`, `"id": "model-redacted-id"`, `"display_name": "Model Redacted Alpha"`, `"surface": null`, `"stale": true`} {
		if !strings.Contains(jsonText, want) {
			t.Fatalf("JSON = %s, missing %s", jsonText, want)
		}
	}
	for _, absent := range []string{`"tokens"`, `"limit"`} {
		if strings.Contains(jsonText, absent) {
			t.Fatalf("percent-only JSON synthesized %s: %s", absent, jsonText)
		}
	}
}

func TestUsageTablePreservesQuotaResetShapes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	zero := int64(0)
	var out bytes.Buffer
	if err := writeUsageTable(&out, []usage.Snapshot{
		{Model: "antigravity", Window: usage.WindowContext, Pct: 12, UpdatedAt: now},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "context", Pct: 0, ResetsAt: reset, ResetInSeconds: &zero, UpdatedAt: now},
	}, now); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"quota/context", reset.Local().Format(time.RFC3339), "0s", "RESET_IN"} {
		if !strings.Contains(got, want) {
			t.Fatalf("table = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "antigravity  context ") {
		t.Fatalf("legacy context row leaked into table: %q", got)
	}
}

// TestFormatStatusUsageCanonicalOrder locks the HUD ordering: Claude,
// Codex, then Antigravity, regardless of snapshot input order. This also
// guards claude/codex against regression when a context-only model is
// present.
func TestFormatStatusUsageCanonicalOrder(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "gemini-weekly", Pct: 42, UpdatedAt: now},
		{Model: "codex", Window: usage.Window5h, Pct: 71, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		{Model: "claude", Window: usage.Window5h, Pct: 12, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}
	got := formatStatusUsage(snaps, 0, now)

	iClaude := strings.Index(got, "Claude")
	iCodex := strings.Index(got, "Codex")
	iAgy := strings.Index(got, "Antigravity")
	if iClaude < 0 || iCodex < 0 || iAgy < 0 {
		t.Fatalf("missing a model label: %q", got)
	}
	if !(iClaude < iCodex && iCodex < iAgy) {
		t.Fatalf("canonical order Claude<Codex<Antigravity not held: claude=%d codex=%d agy=%d in %q", iClaude, iCodex, iAgy, got)
	}
}

func TestFormatStatusUsageOmitsPlaceholderRows(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	// Claude rows here have Pct=0 AND ResetsAt zero AND Limit=0 → treated
	// as "no data" placeholders that the HUD must skip. Codex has real
	// percentages and must still appear.
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 0, UpdatedAt: now},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 0, UpdatedAt: now},
		{Model: "codex", Window: usage.Window5h, Pct: 50, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		{Model: "codex", Window: usage.WindowWeekly, Pct: 25, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now},
	}
	got := formatStatusUsage(snaps, 0, now)
	if strings.Contains(got, "Claude") {
		t.Fatalf("claude has no data but appears in output: %q", got)
	}
	if !strings.Contains(got, "Codex") {
		t.Fatalf("codex must appear: %q", got)
	}
	if !strings.Contains(got, "50%") || !strings.Contains(got, "25%") {
		t.Fatalf("missing codex percentages: %q", got)
	}
}

func TestFormatStatusUsageRendersGenuineZeroWithResetTime(t *testing.T) {
	t.Parallel()

	// A genuine 0% from a healthy account (Pct=0 but ResetsAt is real)
	// must still render — that's a real measurement, not a placeholder.
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 0, ResetsAt: now.Add(5 * time.Hour), UpdatedAt: now},
	}
	got := formatStatusUsage(snaps, 0, now)
	if !strings.Contains(got, "Claude") {
		t.Fatalf("genuine 0%% must still render label: %q", got)
	}
	if !strings.Contains(got, "0%") {
		t.Fatalf("missing 0%% text: %q", got)
	}
}

func TestFormatStatusUsageAllEmpty(t *testing.T) {
	t.Parallel()

	if got := formatStatusUsage(nil, 0, time.Time{}); got != "" {
		t.Fatalf("formatStatusUsage(nil) = %q, want empty", got)
	}
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h},
	}
	if got := formatStatusUsage(snaps, 0, time.Time{}); got != "" {
		t.Fatalf("formatStatusUsage(no data) = %q, want empty", got)
	}
}

func TestFormatStatusUsageWidthTiers(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 42, ResetsAt: now.Add(time.Hour)},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 18, ResetsAt: now.Add(7 * 24 * time.Hour)},
		{Model: "codex", Window: usage.Window5h, Pct: 71, ResetsAt: now.Add(time.Hour)},
		{Model: "codex", Window: usage.WindowWeekly, Pct: 55, ResetsAt: now.Add(7 * 24 * time.Hour)},
	}

	// Widest tier: long HUD with both bars per model.
	long := formatStatusUsage(snaps, 200, now)
	if !strings.Contains(long, "Claude") || !strings.Contains(long, "weekly ") {
		t.Fatalf("tier1 long HUD missing weekly bar: %q", long)
	}
	if intrender.VisualLen(long) > 200 {
		t.Fatalf("tier1 visualLen=%d > 200", intrender.VisualLen(long))
	}

	// Primary-bar tier: drop weekly bars (label + 5h only).
	tier2 := formatStatusUsage(snaps, 60, now)
	if intrender.VisualLen(tier2) > 60 {
		t.Fatalf("tier2 visualLen=%d > 60: %q", intrender.VisualLen(tier2), tier2)
	}
	if !strings.Contains(tier2, "Claude") || !strings.Contains(tier2, "Codex") {
		t.Fatalf("tier2 missing labels: %q", tier2)
	}
	if !strings.Contains(tier2, "5h ") {
		t.Fatalf("tier2 must keep 5h bar: %q", tier2)
	}
	if strings.Contains(tier2, "weekly ") {
		t.Fatalf("tier2 must drop weekly bar: %q", tier2)
	}

	// Long-text tier: drop bars, keep long labels.
	tier3 := formatStatusUsage(snaps, 50, now)
	if intrender.VisualLen(tier3) > 50 {
		t.Fatalf("tier3 visualLen=%d > 50: %q", intrender.VisualLen(tier3), tier3)
	}
	if !strings.Contains(tier3, "Claude 5h:42% weekly:18%") {
		t.Fatalf("tier3 long-label form missing: %q", tier3)
	}
	if strings.Contains(tier3, "█") || strings.Contains(tier3, "░") {
		t.Fatalf("tier3 must not contain bar runes: %q", tier3)
	}

	// Short-text tier: single-letter labels.
	tier4 := formatStatusUsage(snaps, 45, now)
	if intrender.VisualLen(tier4) > 45 {
		t.Fatalf("tier4 visualLen=%d > 45: %q", intrender.VisualLen(tier4), tier4)
	}
	if !strings.Contains(tier4, "C 5h:42%") || !strings.Contains(tier4, "X 5h:71%") {
		t.Fatalf("tier4 short-label form missing: %q", tier4)
	}
	if strings.Contains(tier4, "Claude") {
		t.Fatalf("tier4 must drop long labels: %q", tier4)
	}

	// Last resort: hard truncate with ellipsis.
	tier5 := formatStatusUsage(snaps, 15, now)
	if intrender.VisualLen(tier5) > 15 {
		t.Fatalf("tier5 visualLen=%d > 15: %q", intrender.VisualLen(tier5), tier5)
	}
	if !strings.HasSuffix(tier5, "…") {
		t.Fatalf("tier5 must end with ellipsis: %q", tier5)
	}
}

func TestCurrentCacheWidthTiersPreserveWeeklyOnlyProvider(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC)
	models := buildModelDisplays(projectStatusSnapshots(installedCacheFixture(now)))

	for name, out := range map[string]string{
		"long":        renderUsageSegment(models, now, usagePlanCompactAge(models)),
		"primary-bar": renderUsageSegment(models, now, usagePlanOfficialOnly(models)),
		"text":        renderUsageSegment(models, now, usagePlanTextLong(models)),
	} {
		for _, provider := range []string{"Claude", "Codex", "Antigravity"} {
			if !strings.Contains(out, provider) {
				t.Fatalf("%s tier dropped %s: %q", name, provider, out)
			}
		}
		if !strings.Contains(out, "Codex") || !strings.Contains(out, "weekly") {
			t.Fatalf("%s tier did not preserve weekly-only Codex: %q", name, out)
		}
	}
	short := renderUsageSegment(models, now, usagePlanTextShort(models))
	for _, provider := range []string{"C ", "X weekly:12%", "A weekly:61%"} {
		if !strings.Contains(short, provider) {
			t.Fatalf("short text tier dropped %q: %q", provider, short)
		}
	}
	hard := formatStatusUsage(installedCacheFixture(now), 15, now)
	if intrender.VisualLen(hard) > 15 || !strings.HasSuffix(hard, "…") {
		t.Fatalf("hard truncation = %q (%d cells)", hard, intrender.VisualLen(hard))
	}
}

func TestUnknownQuotaDoesNotChangeStatusProjection(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC)
	base := installedCacheFixture(now)
	want := formatStatusUsage(base, 120, now)
	withUnknown := append(append([]usage.Snapshot(nil), base...), usage.Snapshot{
		Model: "antigravity", Window: usage.WindowQuota, Bucket: "future-valid-bucket", Pct: 99, UpdatedAt: now,
	})
	if got := formatStatusUsage(withUnknown, 120, now); got != want {
		t.Fatalf("unknown quota changed status width/output:\n got %q\nwant %q", got, want)
	}
	var table bytes.Buffer
	if err := writeUsageTable(&table, withUnknown, now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(table.String(), "quota/future-valid-bucket") || !strings.Contains(table.String(), "99%") {
		t.Fatalf("unknown valid quota lost from CLI table: %q", table.String())
	}
}

func TestUsageJSONSuppressesLegacyContextAndPreservesNamedQuota(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	if err := writeUsageJSON(&out, installedCacheFixture(now), usage.State{}, now); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, `"window": "context"`) || strings.Contains(got, `"window":"context"`) {
		t.Fatalf("legacy context row leaked into JSON: %s", got)
	}
	for _, bucket := range []string{"3p-weekly", "gemini-weekly"} {
		if !strings.Contains(got, bucket) {
			t.Fatalf("named quota %q missing from JSON: %s", bucket, got)
		}
	}
}

func TestFormatStatusUsageOverLimitShowsActualPercent(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 319, ResetsAt: now.Add(time.Hour)},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 110, ResetsAt: now.Add(7 * 24 * time.Hour)},
	}
	got := formatStatusUsage(snaps, 0, now)
	if !strings.Contains(got, "319%") {
		t.Fatalf("missing actual over-limit percent: %q", got)
	}
	if !strings.Contains(got, theme.TmuxStateCriticalFg+",bold") {
		t.Fatalf("over-limit must use critical color: %q", got)
	}
}

func TestFilterSnapshotsByModelAndWindow(t *testing.T) {
	t.Parallel()

	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h},
		{Model: "claude", Window: usage.WindowWeekly},
		{Model: "codex", Window: usage.Window5h},
		{Model: "codex", Window: usage.WindowWeekly},
	}
	got := filterSnapshots(snaps, "claude", "all")
	if len(got) != 2 {
		t.Fatalf("model=claude got %d, want 2", len(got))
	}
	got = filterSnapshots(snaps, "all", "5h")
	if len(got) != 2 {
		t.Fatalf("window=5h got %d, want 2", len(got))
	}
	got = filterSnapshots(snaps, "codex", "weekly")
	if len(got) != 1 || got[0].Model != "codex" || got[0].Window != usage.WindowWeekly {
		t.Fatalf("codex+weekly got %+v, want one codex weekly", got)
	}
}

func TestFilterSnapshotsQuotaDoesNotAliasFixedWindows(t *testing.T) {
	t.Parallel()
	snaps := []usage.Snapshot{
		{Model: "antigravity", Window: usage.WindowContext},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "context"},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "weekly"},
		{Model: "claude", Window: usage.WindowWeekly},
	}
	got := filterSnapshots(snaps, "antigravity", "quota")
	if len(got) != 2 || got[0].Bucket != "context" || got[1].Bucket != "weekly" {
		t.Fatalf("quota filter = %#v", got)
	}
	got = filterSnapshots(snaps, "all", "weekly")
	if len(got) != 1 || got[0].Model != "claude" || got[0].Window != usage.WindowWeekly {
		t.Fatalf("weekly filter aliased opaque bucket: %#v", got)
	}
}

func TestUsageRunAllScopesToEnabledClaudeOnly(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	store := usage.NewStore(t.TempDir())
	if err := store.SaveState(usage.State{
		Snapshots: []usage.Snapshot{
			{Model: "claude", Window: usage.Window5h, Pct: 12, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
			{Model: "codex", Window: usage.Window5h, Pct: 88, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		},
		Backoff: map[string]usage.BackoffState{
			"codex": {Until: now.Add(5 * time.Minute), Consecutive: 1},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	claudeAd := &stubAdapter{name: "claude", snaps: []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 13, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}}
	codexAd := &stubAdapter{name: "codex", snaps: []usage.Snapshot{
		{Model: "codex", Window: usage.Window5h, Pct: 89, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}}

	c := New(func() time.Time { return now })
	c.enabledAgentsFn = func() ([]config.AIAgentProvider, error) {
		return []config.AIAgentProvider{config.AIAgentClaude}, nil
	}
	c.managerFn = scopedUsageManagerFactory(t, store, now, map[string]*stubAdapter{
		"claude": claudeAd,
		"codex":  codexAd,
	})

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.Run([]string{"--model", "all"}, stdout, stderr); err != nil {
		t.Fatalf("Run: %v stderr=%s", err, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "claude") || !strings.Contains(out, "13%") {
		t.Fatalf("expected enabled claude row: %q", out)
	}
	if strings.Contains(out, "codex") {
		t.Fatalf("disabled codex leaked into all-model output: %q", out)
	}
	if strings.Contains(out, "codex is in backoff") {
		t.Fatalf("disabled codex backoff leaked into all-model output: %q", out)
	}
	if claudeAd.collectCalls != 1 {
		t.Fatalf("claude collect calls = %d, want 1", claudeAd.collectCalls)
	}
	if codexAd.collectCalls != 0 {
		t.Fatalf("codex collect calls = %d, want 0 for disabled ambient scope", codexAd.collectCalls)
	}
}

func TestUsageStatusScopesToEnabledCodexOnly(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	store := usage.NewStore(t.TempDir())
	if err := store.SaveState(usage.State{
		Snapshots: []usage.Snapshot{
			{Model: "claude", Window: usage.Window5h, Pct: 44, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
			{Model: "codex", Window: usage.Window5h, Pct: 22, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	claudeAd := &stubAdapter{name: "claude", snaps: []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 45, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}}
	codexAd := &stubAdapter{name: "codex", snaps: []usage.Snapshot{
		{Model: "codex", Window: usage.Window5h, Pct: 23, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}}

	c := New(func() time.Time { return now })
	c.enabledAgentsFn = func() ([]config.AIAgentProvider, error) {
		return []config.AIAgentProvider{config.AIAgentCodex}, nil
	}
	c.managerFn = scopedUsageManagerFactory(t, store, now, map[string]*stubAdapter{
		"claude": claudeAd,
		"codex":  codexAd,
	})

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.RunStatus(nil, stdout, stderr); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "Codex") || !strings.Contains(out, "23%") {
		t.Fatalf("expected enabled codex HUD: %q", out)
	}
	if strings.Contains(out, "Claude") {
		t.Fatalf("disabled claude leaked into status HUD: %q", out)
	}
	if claudeAd.collectCalls != 0 {
		t.Fatalf("claude collect calls = %d, want 0 for disabled ambient scope", claudeAd.collectCalls)
	}
	if codexAd.collectCalls != 1 {
		t.Fatalf("codex collect calls = %d, want 1", codexAd.collectCalls)
	}
}

func TestUsageStatusProjectsAntigravityGeminiWeekly(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	store := usage.NewStore(t.TempDir())
	agyAd := &stubAdapter{name: "antigravity", snaps: []usage.Snapshot{
		{Model: "antigravity", Window: usage.WindowContext, Pct: 63, UpdatedAt: now},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "3p-weekly", Pct: 45, UpdatedAt: now},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "gemini-weekly", Pct: 22, UpdatedAt: now},
	}}

	c := New(func() time.Time { return now })
	c.enabledAgentsFn = func() ([]config.AIAgentProvider, error) {
		return []config.AIAgentProvider{config.AIAgentAntigravity}, nil
	}
	c.managerFn = scopedUsageManagerFactory(t, store, now, map[string]*stubAdapter{
		"antigravity": agyAd,
	})

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.RunStatus(nil, stdout, stderr); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "Antigravity") || !strings.Contains(out, "weekly") || !strings.Contains(out, "22%") {
		t.Fatalf("expected Antigravity weekly projection: %q", out)
	}
	for _, excluded := range []string{"ctx", "63%", "3p-weekly", "gemini-weekly", "quota/"} {
		if strings.Contains(out, excluded) {
			t.Fatalf("status leaked %q: %q", excluded, out)
		}
	}
	if agyAd.collectCalls != 1 {
		t.Fatalf("antigravity collect calls = %d, want 1", agyAd.collectCalls)
	}
}

func TestUsageRunWindowContextCompatibilityReturnsNoRows(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC)
	store := usage.NewStore(t.TempDir())
	agyAd := &stubAdapter{name: "antigravity", snaps: []usage.Snapshot{
		{Model: "antigravity", Window: usage.WindowContext, Pct: 55, UpdatedAt: now},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "gemini-weekly", Pct: 31, UpdatedAt: now},
	}}
	c := New(func() time.Time { return now })
	c.managerFn = scopedUsageManagerFactory(t, store, now, map[string]*stubAdapter{
		"antigravity": agyAd,
	})

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.Run([]string{"--model", "antigravity", "--window", "context"}, stdout, stderr); err != nil {
		t.Fatalf("Run compatibility context filter: %v stderr=%q", err, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "MODEL") || strings.Contains(output, "antigravity") || strings.Contains(output, "55%") {
		t.Fatalf("context compatibility output = %q, want empty usage table", output)
	}
	if agyAd.collectCalls != 1 {
		t.Fatalf("antigravity collect calls = %d, want 1", agyAd.collectCalls)
	}
}

func TestUsageRunSuppressesContextAndKeepsNamedQuota(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	store := usage.NewStore(t.TempDir())
	agyAd := &stubAdapter{name: "antigravity", snaps: []usage.Snapshot{
		{Model: "antigravity", Window: usage.WindowContext, Pct: 55, UpdatedAt: now},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "3p-weekly", Pct: 44, UpdatedAt: now},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "gemini-weekly", Pct: 77, UpdatedAt: now},
	}}
	c := New(func() time.Time { return now })
	c.enabledAgentsFn = func() ([]config.AIAgentProvider, error) {
		return []config.AIAgentProvider{config.AIAgentAntigravity}, nil
	}
	c.managerFn = scopedUsageManagerFactory(t, store, now, map[string]*stubAdapter{
		"antigravity": agyAd,
	})

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.Run([]string{"--model", "all"}, stdout, stderr); err != nil {
		t.Fatalf("Run: %v stderr=%s", err, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "antigravity") || !strings.Contains(output, "quota/3p-weekly") || !strings.Contains(output, "quota/gemini-weekly") {
		t.Fatalf("output = %q, want lossless named quota rows", output)
	}
	if strings.Contains(output, "  context") || strings.Contains(output, "55%") {
		t.Fatalf("output = %q, legacy context row leaked", output)
	}
	if strings.Contains(output, "unsupported") {
		t.Fatalf("output = %q, Antigravity usage should no longer be unsupported", output)
	}
	if strings.Contains(output, "enable Claude or Codex") {
		t.Fatalf("output = %q, should not imply Antigravity is not an enabled agent", output)
	}
	if agyAd.collectCalls != 1 {
		t.Fatalf("antigravity collect calls = %d, want 1", agyAd.collectCalls)
	}
}

func TestUsageRunExplicitAntigravityWorksWhenDisabled(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	store := usage.NewStore(t.TempDir())
	agyAd := &stubAdapter{name: "antigravity", snaps: []usage.Snapshot{
		{Model: "antigravity", Window: usage.WindowContext, Pct: 77, UpdatedAt: now},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "gemini-weekly", Pct: 31, UpdatedAt: now},
	}}
	c := New(func() time.Time { return now })
	c.enabledAgentsFn = func() ([]config.AIAgentProvider, error) {
		return []config.AIAgentProvider{config.AIAgentClaude}, nil
	}
	c.managerFn = scopedUsageManagerFactory(t, store, now, map[string]*stubAdapter{
		"antigravity": agyAd,
	})

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.Run([]string{"--model", "antigravity"}, stdout, stderr); err != nil {
		t.Fatalf("Run: %v stderr=%s", err, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "antigravity") || !strings.Contains(output, "quota/gemini-weekly") || !strings.Contains(output, "31%") {
		t.Fatalf("output = %q, want explicit antigravity named quota row", output)
	}
	if strings.Contains(output, "  context") || strings.Contains(output, "77%") {
		t.Fatalf("output = %q, legacy context row leaked", output)
	}
	if agyAd.collectCalls != 1 {
		t.Fatalf("antigravity collect calls = %d, want 1 for explicit model", agyAd.collectCalls)
	}
}

func TestUsageAllWithNoEnabledAgentsSkipsCollectAndShowsFallback(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	store := usage.NewStore(t.TempDir())
	if err := store.SaveState(usage.State{
		Snapshots: []usage.Snapshot{
			{Model: "claude", Window: usage.Window5h, Pct: 44, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
			{Model: "codex", Window: usage.Window5h, Pct: 22, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	claudeAd := &stubAdapter{name: "claude"}
	codexAd := &stubAdapter{name: "codex"}

	c := New(func() time.Time { return now })
	c.enabledAgentsFn = func() ([]config.AIAgentProvider, error) {
		return nil, nil
	}
	c.managerFn = scopedUsageManagerFactory(t, store, now, map[string]*stubAdapter{
		"claude": claudeAd,
		"codex":  codexAd,
	})

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.Run([]string{"--model", "all"}, stdout, stderr); err != nil {
		t.Fatalf("Run: %v stderr=%s", err, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "no AI usage providers enabled") {
		t.Fatalf("missing no-enabled-agents fallback: %q", out)
	}
	if strings.Contains(out, "claude") || strings.Contains(out, "codex") {
		t.Fatalf("disabled cached rows leaked with no enabled agents: %q", out)
	}
	if claudeAd.collectCalls != 0 || codexAd.collectCalls != 0 {
		t.Fatalf("collect calls with no enabled agents = claude:%d codex:%d, want 0/0", claudeAd.collectCalls, codexAd.collectCalls)
	}

	stdout.Reset()
	if err := c.RunStatus(nil, stdout, stderr); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("status output with no enabled agents = %q, want empty", stdout.String())
	}
}

func TestUsageExplicitClaudeWorksWhenDisabled(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	store := usage.NewStore(t.TempDir())
	claudeAd := &stubAdapter{name: "claude", snaps: []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 31, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}}
	codexAd := &stubAdapter{name: "codex", snaps: []usage.Snapshot{
		{Model: "codex", Window: usage.Window5h, Pct: 91, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}}

	c := New(func() time.Time { return now })
	c.enabledAgentsFn = func() ([]config.AIAgentProvider, error) {
		return []config.AIAgentProvider{config.AIAgentCodex}, nil
	}
	c.managerFn = scopedUsageManagerFactory(t, store, now, map[string]*stubAdapter{
		"claude": claudeAd,
		"codex":  codexAd,
	})

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.Run([]string{"--model", "claude"}, stdout, stderr); err != nil {
		t.Fatalf("Run: %v stderr=%s", err, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "claude") || !strings.Contains(out, "31%") {
		t.Fatalf("explicit disabled claude should render read-only data: %q", out)
	}
	if strings.Contains(out, "codex") {
		t.Fatalf("explicit claude output should not include codex: %q", out)
	}
	if claudeAd.collectCalls != 1 {
		t.Fatalf("claude collect calls = %d, want 1 for explicit disabled model", claudeAd.collectCalls)
	}
	if codexAd.collectCalls != 0 {
		t.Fatalf("codex collect calls = %d, want 0 for explicit claude scope", codexAd.collectCalls)
	}
}

func TestUsageExplicitCodexWorksWhenDisabled(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	store := usage.NewStore(t.TempDir())
	claudeAd := &stubAdapter{name: "claude", snaps: []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 31, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}}
	codexAd := &stubAdapter{name: "codex", snaps: []usage.Snapshot{
		{Model: "codex", Window: usage.Window5h, Pct: 91, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}}

	c := New(func() time.Time { return now })
	c.enabledAgentsFn = func() ([]config.AIAgentProvider, error) {
		return []config.AIAgentProvider{config.AIAgentClaude}, nil
	}
	c.managerFn = scopedUsageManagerFactory(t, store, now, map[string]*stubAdapter{
		"claude": claudeAd,
		"codex":  codexAd,
	})

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.Run([]string{"--model", "codex"}, stdout, stderr); err != nil {
		t.Fatalf("Run: %v stderr=%s", err, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "codex") || !strings.Contains(out, "91%") {
		t.Fatalf("explicit disabled codex should render read-only data: %q", out)
	}
	if strings.Contains(out, "claude") {
		t.Fatalf("explicit codex output should not include claude: %q", out)
	}
	if codexAd.collectCalls != 1 {
		t.Fatalf("codex collect calls = %d, want 1 for explicit disabled model", codexAd.collectCalls)
	}
	if claudeAd.collectCalls != 0 {
		t.Fatalf("claude collect calls = %d, want 0 for explicit codex scope", claudeAd.collectCalls)
	}
}

// stubAdapter emits Snapshots directly under the v2 contract.
type stubAdapter struct {
	name         string
	snaps        []usage.Snapshot
	err          error
	collectCalls int
}

func (s *stubAdapter) Name() string { return s.name }
func (s *stubAdapter) Collect(ctx context.Context) ([]usage.Snapshot, error) {
	s.collectCalls++
	return s.snaps, s.err
}

func newStubManager(t *testing.T, adapters []*stubAdapter) *usage.Manager {
	t.Helper()
	dir := t.TempDir()
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	registry := usage.NewRegistry()
	for _, a := range adapters {
		_ = registry.Replace(a)
	}
	store := usage.NewStore(dir)
	return usage.NewManager(registry, store, func() time.Time { return now })
}

func scopedUsageManagerFactory(t *testing.T, store *usage.Store, now time.Time, adapters map[string]*stubAdapter) func([]string) (*usage.Manager, error) {
	t.Helper()
	return func(scope []string) (*usage.Manager, error) {
		registry := usage.NewRegistry()
		for _, model := range normalizeUsageModelScope(scope) {
			adapter, ok := adapters[model]
			if !ok {
				continue
			}
			if err := registry.Replace(adapter); err != nil {
				return nil, err
			}
		}
		return usage.NewManager(registry, store, func() time.Time { return now }), nil
	}
}

func TestUsageRunJSONEmptyCacheReturnsArray(t *testing.T) {
	t.Parallel()

	c := New(nil)
	c.managerFn = func([]string) (*usage.Manager, error) {
		dir := t.TempDir()
		registry := usage.NewRegistry()
		_ = registry.Register(&stubAdapter{name: "claude"})
		store := usage.NewStore(dir)
		return usage.NewManager(registry, store, func() time.Time {
			return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
		}), nil
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.Run([]string{"--json"}, stdout, stderr); err != nil {
		t.Fatalf("Run: %v err=%s", err, stderr.String())
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout.String()), "[") {
		t.Fatalf("stdout = %q, want JSON array", stdout.String())
	}
}

func TestUsageStatusEmitsFormattedSegment(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	c := New(nil)
	mgr := newStubManager(t, []*stubAdapter{
		{name: "claude", snaps: []usage.Snapshot{
			{Model: "claude", Window: usage.Window5h, Pct: 30, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		}},
		{name: "codex", snaps: []usage.Snapshot{
			{Model: "codex", Window: usage.Window5h, Pct: 70, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		}},
	})
	if _, err := mgr.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	c.managerFn = func([]string) (*usage.Manager, error) { return mgr, nil }

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.RunStatus(nil, stdout, stderr); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "Claude") || !strings.Contains(out, "Codex") {
		t.Fatalf("status output = %q, want HUD model labels", out)
	}
}

func TestUsageStatusManagerErrorIsSilent(t *testing.T) {
	t.Parallel()

	c := New(nil)
	c.managerFn = func([]string) (*usage.Manager, error) {
		return nil, errors.New("boom")
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.RunStatus(nil, stdout, stderr); err != nil {
		t.Fatalf("RunStatus must swallow error, got %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestUsageStatusMaybeCollectThrottledOnSecondCall(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	c := New(nil)
	mgr := newStubManager(t, []*stubAdapter{
		{name: "claude", snaps: []usage.Snapshot{
			{Model: "claude", Window: usage.Window5h, Pct: 5, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		}},
		{name: "codex", snaps: []usage.Snapshot{
			{Model: "codex", Window: usage.Window5h, Pct: 10, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		}},
	})
	c.managerFn = func([]string) (*usage.Manager, error) { return mgr, nil }

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.RunStatus(nil, stdout, stderr); err != nil {
		t.Fatalf("first RunStatus: %v", err)
	}
	first := stdout.String()
	stdout.Reset()
	if err := c.RunStatus(nil, stdout, stderr); err != nil {
		t.Fatalf("second RunStatus: %v", err)
	}
	second := stdout.String()
	if first == "" || second == "" {
		t.Fatalf("expected non-empty status output, got %q / %q", first, second)
	}
}

func TestUsageMaybeCollectManualRefreshThrottlesAllAdapters(t *testing.T) {
	t.Parallel()

	claude := &stubAdapter{
		name: "claude",
		snaps: []usage.Snapshot{
			{Model: "claude", Window: usage.Window5h, Pct: 5},
		},
	}
	codex := &stubAdapter{
		name: "codex",
		snaps: []usage.Snapshot{
			{Model: "codex", Window: usage.Window5h, Pct: 10},
		},
	}
	c := New(nil)
	mgr := newStubManager(t, []*stubAdapter{claude, codex})
	c.managerFn = func([]string) (*usage.Manager, error) {
		return mgr, nil
	}

	ran, err := c.MaybeCollect(context.Background())
	if err != nil {
		t.Fatalf("first MaybeCollect: %v", err)
	}
	if !ran {
		t.Fatal("first MaybeCollect ran = false, want true")
	}
	if claude.collectCalls != 1 || codex.collectCalls != 1 {
		t.Fatalf("first collect calls: claude=%d codex=%d, want 1/1", claude.collectCalls, codex.collectCalls)
	}

	ran, err = c.MaybeCollect(context.Background())
	if err != nil {
		t.Fatalf("second MaybeCollect: %v", err)
	}
	if ran {
		t.Fatal("second MaybeCollect ran = true within cooldown, want false")
	}
	if claude.collectCalls != 1 || codex.collectCalls != 1 {
		t.Fatalf("second collect calls: claude=%d codex=%d, want throttle no-op at 1/1", claude.collectCalls, codex.collectCalls)
	}
}

func TestUsageStatusSwallowsAdapterErrorByDefault(t *testing.T) {
	t.Parallel()

	c := New(nil)
	dir := t.TempDir()
	registry := usage.NewRegistry()
	_ = registry.Replace(&stubAdapter{
		name: "claude",
		err:  errors.New("network down"),
	})
	store := usage.NewStore(dir)
	mgr := usage.NewManager(registry, store, func() time.Time {
		return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	})
	c.managerFn = func([]string) (*usage.Manager, error) { return mgr, nil }

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.RunStatus(nil, stdout, stderr); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty (adapter failures must be silent)", stderr.String())
	}
}

func TestUsageStatusEchoesAdapterErrorWithDebugEnv(t *testing.T) {
	t.Parallel()

	c := New(nil)
	c.lookupEnv = func(name string) string {
		if name == usageDebugEnvVar {
			return "1"
		}
		return ""
	}
	dir := t.TempDir()
	registry := usage.NewRegistry()
	_ = registry.Replace(&stubAdapter{
		name: "claude",
		err:  errors.New("network down"),
	})
	store := usage.NewStore(dir)
	mgr := usage.NewManager(registry, store, func() time.Time {
		return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	})
	c.managerFn = func([]string) (*usage.Manager, error) { return mgr, nil }

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.RunStatus(nil, stdout, stderr); err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if !strings.Contains(stderr.String(), "network down") {
		t.Fatalf("stderr = %q, want adapter error surfaced under PROJMUX_USAGE_DEBUG", stderr.String())
	}
}

func TestFormatStatusUsageHUDConfinesTildeToAgeIndicator(t *testing.T) {
	t.Parallel()

	// At an unconstrained width the `~` / `~~` stale vocabulary lives INSIDE
	// the age indicator: the per-window pairs stay marker-free, and a model
	// that opted out of the indicator (Codex) never carries one at all.
	// Narrower widths move the marker onto the label as the compact form —
	// see TestFormatStatusUsageStalenessSurvivesStatusbarWidths — but it never
	// reaches the window pairs at any width.
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-15 * time.Minute)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 18, ResetsAt: now.Add(time.Hour), UpdatedAt: stale},
		{Model: "codex", Window: usage.Window5h, Pct: 50, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}
	got := formatStatusUsage(snaps, 0, now)
	if !strings.Contains(got, "(15m~)") {
		t.Fatalf("stale claude age indicator missing its `~` marker: %q", got)
	}
	if strings.Count(got, "~") != 1 {
		t.Fatalf("`~` escaped the age indicator: %q", got)
	}
	claudeBlock, codexBlock, ok := strings.Cut(got, "Codex")
	if !ok {
		t.Fatalf("codex block missing: %q", got)
	}
	if strings.Contains(codexBlock, "~") || strings.Contains(codexBlock, "(") {
		t.Fatalf("codex must not carry an age indicator: %q", codexBlock)
	}
	// The marker sits in the indicator, not on the 5h/weekly pair.
	if strings.Contains(claudeBlock[strings.Index(claudeBlock, "5h"):], "~") {
		t.Fatalf("window pair carried a stale marker: %q", claudeBlock)
	}
}

func TestFormatStatusUsageTextTiersCarryPlainStaleMarker(t *testing.T) {
	t.Parallel()

	// The colorless text tiers carry staleness as the plain `~` / `~~`
	// marker glued to the label. Dropping it here was the whole defect:
	// these tiers are what a narrow terminal actually renders, so a tier
	// that cannot express staleness makes the marker width-dependent.
	// Fresh models stay marker-free, which is what keeps the historical
	// bytes intact on a healthy install.
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-15 * time.Minute)
	veryStale := now.Add(-90 * time.Minute)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 42, ResetsAt: now.Add(time.Hour), UpdatedAt: veryStale},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 18, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: stale},
		{Model: "codex", Window: usage.Window5h, Pct: 71, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		{Model: "codex", Window: usage.WindowWeekly, Pct: 55, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now},
	}
	// Claude's lastSync is max(5h, weekly) = -15m, i.e. level 1 → `~`.
	// The two tiers are exercised directly: their selection depends on cell
	// budgets that shift whenever a bar or a label changes, and this test is
	// about the marker's SHAPE, not about which budget selects which tier
	// (TestFormatStatusUsageStalenessSurvivesStatusbarWidths owns that).
	models := buildModelDisplays(projectStatusSnapshots(snaps))
	for want, got := range map[string]string{
		"Claude~ 5h:42% weekly:18%": renderUsageSegment(models, now, usagePlanTextLong(models)),
		"C~ 5h:42% weekly:18%":      renderUsageSegment(models, now, usagePlanTextShort(models)),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("text tier = %q, want it to contain %q", got, want)
		}
		// Plain tiers stay colorless: the marker must not drag tmux escapes in.
		if strings.Contains(got, "#[") {
			t.Fatalf("text tier emitted a color escape: %q", got)
		}
		// Codex opted out of the age signal, so it never carries a marker.
		if _, codex, ok := strings.Cut(got, "5h:71%"); ok && strings.Contains(codex, "~") {
			t.Fatalf("codex carried a stale marker: %q", got)
		}
		if strings.Count(got, "~") != 1 {
			t.Fatalf("text tier marker count = %d, want 1: %q", strings.Count(got, "~"), got)
		}
	}
	// Level 2 doubles the marker in exactly the same position.
	veryStaleOnly := buildModelDisplays(projectStatusSnapshots([]usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 42, ResetsAt: now.Add(time.Hour), UpdatedAt: veryStale},
		{Model: "codex", Window: usage.Window5h, Pct: 71, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}))
	if got := renderUsageSegment(veryStaleOnly, now, usagePlanTextShort(veryStaleOnly)); !strings.Contains(got, "C~~ 5h:42%") {
		t.Fatalf("very-stale text tier = %q, want `C~~ 5h:42%%`", got)
	}
	// A fresh model keeps the historical marker-free bytes.
	fresh := buildModelDisplays(projectStatusSnapshots([]usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 42, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}))
	if got := renderUsageSegment(fresh, now, usagePlanTextLong(fresh)); got != "Claude 5h:42%" {
		t.Fatalf("fresh text tier = %q, want %q", got, "Claude 5h:42%")
	}
}

func TestUsageTableShowsStaleColumn(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-30 * time.Minute)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 18, ResetsAt: now.Add(time.Hour), UpdatedAt: stale},
		{Model: "codex", Window: usage.Window5h, Pct: 50, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}
	out := &bytes.Buffer{}
	if err := writeUsageTable(out, snaps, now); err != nil {
		t.Fatalf("writeUsageTable: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, "STALE") {
		t.Fatalf("table header missing STALE column: %q", body)
	}
	// Claude row must have a `*` in the STALE column; codex row must not.
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("table too short: %q", body)
	}
	var claudeLine, codexLine string
	for _, l := range lines[1:] {
		if strings.Contains(l, "claude") {
			claudeLine = l
		}
		if strings.Contains(l, "codex") {
			codexLine = l
		}
	}
	if !strings.Contains(claudeLine, "*") {
		t.Fatalf("claude row missing stale marker: %q", claudeLine)
	}
	if strings.Contains(codexLine, "*") {
		t.Fatalf("codex row should be fresh, not stale-marked: %q", codexLine)
	}
}

func TestUsageJSONIncludesStaleAndBackoff(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 18, ResetsAt: now.Add(time.Hour), UpdatedAt: now.Add(-30 * time.Minute)},
		{Model: "codex", Window: usage.Window5h, Pct: 50, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}
	state := usage.State{
		Backoff: map[string]usage.BackoffState{
			"claude": {Until: now.Add(5 * time.Minute), Consecutive: 1},
		},
	}
	out := &bytes.Buffer{}
	if err := writeUsageJSON(out, snaps, state, now); err != nil {
		t.Fatalf("writeUsageJSON: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, `"stale": true`) {
		t.Fatalf("missing stale=true for claude row: %s", body)
	}
	if !strings.Contains(body, `"stale": false`) {
		t.Fatalf("missing stale=false for codex row: %s", body)
	}
	if !strings.Contains(body, `"backoff"`) {
		t.Fatalf("missing backoff block: %s", body)
	}
	if !strings.Contains(body, `"claude"`) {
		t.Fatalf("backoff block missing claude entry: %s", body)
	}
}

func TestUsageJSONHealthyOmitsBackoff(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "codex", Window: usage.Window5h, Pct: 50, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
	}
	out := &bytes.Buffer{}
	if err := writeUsageJSON(out, snaps, usage.State{}, now); err != nil {
		t.Fatalf("writeUsageJSON: %v", err)
	}
	body := strings.TrimSpace(out.String())
	if !strings.HasPrefix(body, "[") {
		t.Fatalf("healthy --json should emit bare array: %s", body)
	}
	if !strings.Contains(body, `"stale": false`) {
		t.Fatalf("missing per-row stale field: %s", body)
	}
}

// forceTrackingAdapter records whether ForceCollect was the entry
// point: `--force` clears the persisted backoff via the Manager, so
// the adapter sees an empty BackoffState even when one was on disk.
// Save echoes whatever was loaded so the on-disk state round-trips
// (mirroring how the real claude adapter preserves backoff during a
// short-circuit).
type forceTrackingAdapter struct {
	stubAdapter
	loadedBackoff usage.BackoffState
	collectCalls  int
}

func (a *forceTrackingAdapter) LoadBackoff(state usage.BackoffState) {
	a.loadedBackoff = state
}
func (a *forceTrackingAdapter) SaveBackoff() usage.BackoffState {
	return a.loadedBackoff
}
func (a *forceTrackingAdapter) Collect(ctx context.Context) ([]usage.Snapshot, error) {
	a.collectCalls++
	return a.snaps, a.err
}

func TestUsageRunForceBypassesBackoffAndThrottle(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	store := usage.NewStore(dir)
	// Seed: claude in active backoff.
	if err := store.SaveState(usage.State{
		Backoff: map[string]usage.BackoffState{
			"claude": {Until: now.Add(30 * time.Minute), Consecutive: 4},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	registry := usage.NewRegistry()
	claudeAd := &forceTrackingAdapter{
		stubAdapter: stubAdapter{
			name: "claude",
			snaps: []usage.Snapshot{
				{Model: "claude", Window: usage.Window5h, Pct: 9.0, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
			},
		},
	}
	if err := registry.Replace(claudeAd); err != nil {
		t.Fatalf("register: %v", err)
	}
	mgr := usage.NewManager(registry, store, func() time.Time { return now })

	c := New(nil)
	c.managerFn = func([]string) (*usage.Manager, error) { return mgr, nil }
	c.now = func() time.Time { return now }

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.Run([]string{"--force"}, stdout, stderr); err != nil {
		t.Fatalf("Run: %v stderr=%s", err, stderr.String())
	}
	if claudeAd.collectCalls != 1 {
		t.Fatalf("collect calls = %d, want 1 (--force must invoke despite backoff)", claudeAd.collectCalls)
	}
	if !claudeAd.loadedBackoff.Until.IsZero() {
		t.Fatalf("LoadBackoff received Until=%v, want zero (--force clears)", claudeAd.loadedBackoff.Until)
	}
	if claudeAd.loadedBackoff.Consecutive != 0 {
		t.Fatalf("LoadBackoff received Consecutive=%d, want 0 (--force clears)", claudeAd.loadedBackoff.Consecutive)
	}
	out := stdout.String()
	if !strings.Contains(out, "claude") || !strings.Contains(out, "9%") {
		t.Fatalf("expected refreshed claude row in output: %q", out)
	}
}

func TestUsageRunDefaultRespectsBackoff(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	store := usage.NewStore(dir)
	prior := usage.Snapshot{Model: "claude", Window: usage.Window5h, Pct: 18.0, ResetsAt: now.Add(time.Hour), UpdatedAt: now.Add(-time.Minute)}
	if err := store.SaveState(usage.State{
		Snapshots: []usage.Snapshot{prior},
		Backoff: map[string]usage.BackoffState{
			"claude": {Until: now.Add(30 * time.Minute), Consecutive: 1},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	registry := usage.NewRegistry()
	claudeAd := &forceTrackingAdapter{
		stubAdapter: stubAdapter{name: "claude"},
	}
	if err := registry.Replace(claudeAd); err != nil {
		t.Fatalf("register: %v", err)
	}
	mgr := usage.NewManager(registry, store, func() time.Time { return now })

	c := New(nil)
	c.managerFn = func([]string) (*usage.Manager, error) { return mgr, nil }
	c.now = func() time.Time { return now }

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if err := c.Run(nil, stdout, stderr); err != nil {
		t.Fatalf("Run: %v stderr=%s", err, stderr.String())
	}
	if !claudeAd.loadedBackoff.Until.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("LoadBackoff Until = %v, want preserved 30m (no --force)", claudeAd.loadedBackoff.Until)
	}
	out := stdout.String()
	// Output should still show prior 18% (preserved during backoff
	// short-circuit) AND a backoff note pointing at --force.
	if !strings.Contains(out, "18%") {
		t.Fatalf("expected preserved 18%% in output: %q", out)
	}
	if !strings.Contains(out, "claude is in backoff") {
		t.Fatalf("expected backoff note in output: %q", out)
	}
	if !strings.Contains(out, "--force") {
		t.Fatalf("backoff note must mention --force: %q", out)
	}
}

func TestWriteBackoffNoteEmitsWhenActive(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	state := usage.State{
		Backoff: map[string]usage.BackoffState{
			"claude": {Until: now.Add(12 * time.Minute), Consecutive: 2},
		},
	}
	out := &bytes.Buffer{}
	writeBackoffNote(out, state, now)
	got := out.String()
	if !strings.Contains(got, "claude is in backoff") {
		t.Fatalf("missing backoff note: %q", got)
	}
	if !strings.Contains(got, "12m") {
		t.Fatalf("missing remaining duration: %q", got)
	}
	if !strings.Contains(got, "--force") {
		t.Fatalf("must point at --force: %q", got)
	}
}

func TestWriteBackoffNoteSilentWhenHealthy(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	state := usage.State{Backoff: map[string]usage.BackoffState{}}
	out := &bytes.Buffer{}
	writeBackoffNote(out, state, now)
	if out.Len() != 0 {
		t.Fatalf("expected silent on healthy state, got %q", out.String())
	}

	// Expired backoff (Until in the past) is also no-op.
	state = usage.State{Backoff: map[string]usage.BackoffState{
		"claude": {Until: now.Add(-time.Minute), Consecutive: 1},
	}}
	out = &bytes.Buffer{}
	writeBackoffNote(out, state, now)
	if out.Len() != 0 {
		t.Fatalf("expected silent on expired backoff, got %q", out.String())
	}
}

func TestFormatBackoffDurationShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "1s"},
		{45 * time.Second, "45s"},
		{2 * time.Minute, "2m"},
		{12 * time.Minute, "12m"},
		{60 * time.Minute, "1h"},
		{75 * time.Minute, "1h15m"},
	}
	for _, tc := range cases {
		if got := FormatBackoffDuration(tc.in); got != tc.want {
			t.Fatalf("FormatBackoffDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFormatStatusUsageAgeFreshOmitsIndicator covers the `now` case
// from the spec: an age below 1 minute keeps the bar tight by
// suppressing the `(<age>)` block entirely.
func TestFormatStatusUsageAgeFreshOmitsIndicator(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 18, ResetsAt: now.Add(time.Hour), UpdatedAt: now.Add(-30 * time.Second)},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 9, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now.Add(-30 * time.Second)},
	}
	got := formatStatusUsage(snaps, 0, now)
	if strings.Contains(got, "(") {
		t.Fatalf("fresh data must not render an age indicator: %q", got)
	}
	// Sanity: the label and bar still render.
	if !strings.Contains(got, "Claude") {
		t.Fatalf("missing Claude label: %q", got)
	}
}

// TestFormatStatusUsageAgeMinutesGrey covers the spec's `(3m)`
// scenario — minute-scale age renders in dim grey.
func TestFormatStatusUsageAgeMinutesGrey(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 18, ResetsAt: now.Add(time.Hour), UpdatedAt: now.Add(-3 * time.Minute)},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 9, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now.Add(-3 * time.Minute)},
	}
	got := formatStatusUsage(snaps, 0, now)
	if !strings.Contains(got, "#[fg=colour245](3m)#[default]") {
		t.Fatalf("missing dim-grey (3m) age indicator: %q", got)
	}
}

// TestFormatStatusUsageAgeStaleBandMarksStale covers the level-1 band
// (>staleAfter, <=veryStaleAfter). A value the table already calls stale must
// read as stale in the HUD too: single `~`, muted, never a warning color.
func TestFormatStatusUsageAgeStaleBandMarksStale(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 18, ResetsAt: now.Add(time.Hour), UpdatedAt: now.Add(-15 * time.Minute)},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 9, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now.Add(-15 * time.Minute)},
	}
	got := formatStatusUsage(snaps, 0, now)
	if !strings.Contains(got, "#[fg=colour244](15m~)#[default]") {
		t.Fatalf("missing muted (15m~) stale age indicator: %q", got)
	}
}

// TestFormatStatusUsageAgeWarnMuted covers the >1h band — the marker doubles
// but the indicator stays muted so warning/critical colors remain reserved
// for usage thresholds.
func TestFormatStatusUsageAgeWarnMuted(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 18, ResetsAt: now.Add(time.Hour), UpdatedAt: now.Add(-90 * time.Minute)},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 9, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now.Add(-90 * time.Minute)},
	}
	got := formatStatusUsage(snaps, 0, now)
	if !strings.Contains(got, "#[fg=colour244](1h~~)#[default]") {
		t.Fatalf("missing muted (1h~~) age indicator: %q", got)
	}
	for _, reserved := range []string{"colour214", "colour160"} {
		if strings.Contains(strings.SplitN(got, "5h", 2)[0], reserved) {
			t.Fatalf("staleness borrowed a warning/critical color: %q", got)
		}
	}
}

// TestFormatStatusUsageAgeVeryStaleStaysMuted covers the >=6h band. The unit
// stays the actual hours value, but the color remains muted rather than
// escalating to critical red.
func TestFormatStatusUsageAgeVeryStaleStaysMuted(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 18, ResetsAt: now.Add(time.Hour), UpdatedAt: now.Add(-8 * time.Hour)},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 9, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now.Add(-8 * time.Hour)},
	}
	got := formatStatusUsage(snaps, 0, now)
	if !strings.Contains(got, "#[fg=colour244](8h~~)#[default]") {
		t.Fatalf("missing muted (8h~~) age indicator: %q", got)
	}
}

// TestFormatStatusUsageCodexNoAgeIndicator confirms the Codex block
// never carries the age indicator (its rate_limits payload is sourced
// from the latest rollout file every call — always near-current).
func TestFormatStatusUsageCodexNoAgeIndicator(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	// Even a deliberately ancient Codex UpdatedAt must not produce
	// an age block.
	snaps := []usage.Snapshot{
		{Model: "codex", Window: usage.Window5h, Pct: 50, ResetsAt: now.Add(time.Hour), UpdatedAt: now.Add(-12 * time.Hour)},
		{Model: "codex", Window: usage.WindowWeekly, Pct: 25, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now.Add(-12 * time.Hour)},
	}
	got := formatStatusUsage(snaps, 0, now)
	if strings.Contains(got, "(") {
		t.Fatalf("codex rendered an age indicator: %q", got)
	}
}

// TestFormatStatusUsageCosmeticAgeDropsBeforeBars verifies that when the
// budget can't fit the full-age tier, the renderer falls back to a long form
// WITHOUT the age block — rather than jumping straight to the bar-less text
// tier. The fixture here is entirely level 0, so the age block is purely
// cosmetic and is exactly what the ladder is supposed to shed first; a stale
// fixture instead keeps its marker (see
// TestFormatStatusUsageStalenessSurvivesStatusbarWidths).
func TestFormatStatusUsageCosmeticAgeDropsBeforeBars(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-3 * time.Minute)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 42, ResetsAt: now.Add(time.Hour), UpdatedAt: stale},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 18, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: stale},
		{Model: "codex", Window: usage.Window5h, Pct: 71, ResetsAt: now.Add(time.Hour), UpdatedAt: now},
		{Model: "codex", Window: usage.WindowWeekly, Pct: 55, ResetsAt: now.Add(7 * 24 * time.Hour), UpdatedAt: now},
	}
	// Tier 1 (with age) renders to width 70.
	tier1 := formatStatusUsage(snaps, 200, now)
	if !strings.Contains(tier1, "(3m)") {
		t.Fatalf("tier1 missing age indicator at unconstrained width: %q", tier1)
	}
	tier1Width := intrender.VisualLen(tier1)
	// Pick a budget that fits tier 2 but not tier 1.
	budget := tier1Width - 1
	tier2 := formatStatusUsage(snaps, budget, now)
	if strings.Contains(tier2, "(3m)") {
		t.Fatalf("tier2 must drop age indicator: %q", tier2)
	}
	if !strings.Contains(tier2, "weekly ") {
		t.Fatalf("tier2 must keep the weekly bar: %q", tier2)
	}
	if intrender.VisualLen(tier2) > budget {
		t.Fatalf("tier2 visualLen=%d > budget=%d: %q", intrender.VisualLen(tier2), budget, tier2)
	}
}

// TestFormatLastSyncAgeUnits covers the formatLastSyncAge unit
// ladder: <1m → "" (omit), 1m..1h → minutes, 1h..24h → hours, >=24h
// → days.
func TestFormatLastSyncAgeUnits(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, ""},
		{59 * time.Second, ""},
		{60 * time.Second, "1m"},
		{3 * time.Minute, "3m"},
		{59 * time.Minute, "59m"},
		{60 * time.Minute, "1h"},
		{8 * time.Hour, "8h"},
		{23 * time.Hour, "23h"},
		{24 * time.Hour, "1d"},
		{72 * time.Hour, "3d"},
	}
	for _, tc := range cases {
		if got := formatLastSyncAge(tc.in); got != tc.want {
			t.Fatalf("formatLastSyncAge(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStaleLevelCorrectness(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		age  time.Duration
		want int
	}{
		{"fresh", 1 * time.Minute, 0},
		{"just under stale", 9 * time.Minute, 0},
		{"stale", 30 * time.Minute, 1},
		{"just under very stale", 59 * time.Minute, 1},
		{"very stale", 2 * time.Hour, 2},
	}
	for _, tc := range cases {
		s := usage.Snapshot{UpdatedAt: now.Add(-tc.age)}
		if got := staleLevel(s, now); got != tc.want {
			t.Fatalf("staleLevel(%s) = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestUsageHelpDocumentsForceFlag(t *testing.T) {
	t.Parallel()

	out := &bytes.Buffer{}
	printUsageHelp(out)
	body := out.String()
	if !strings.Contains(body, "--force") {
		t.Fatalf("help missing --force flag: %s", body)
	}
	if !strings.Contains(body, "-f") {
		t.Fatalf("help missing -f shorthand: %s", body)
	}
}

func TestResolveStateDirHonoursEnvOverride(t *testing.T) {
	t.Parallel()

	c := New(nil)
	want := "/tmp/projmux-shared-usage"
	c.lookupEnv = func(name string) string {
		if name == StateDirEnvVar {
			return want
		}
		return ""
	}
	got, err := c.resolveStateDir()
	if err != nil {
		t.Fatalf("resolveStateDir: %v", err)
	}
	if got != want {
		t.Fatalf("resolveStateDir = %q, want %q (env override)", got, want)
	}
}

func TestHUDProviderCapabilityMatrixFollowsUsageCatalogAndRejectsFabrication(t *testing.T) {
	t.Parallel()

	capabilities := HUDProviderCapabilities()
	if got, want := len(capabilities), len(aiprovider.UsageSupported()); got != want {
		t.Fatalf("capability providers = %d, want every usage-supported provider (%d)", got, want)
	}
	want := []struct {
		id      aiprovider.ID
		windows []string
	}{
		{aiprovider.Claude, []string{"5h", "weekly"}},
		{aiprovider.Codex, []string{"5h", "weekly"}},
		{aiprovider.Antigravity, []string{"weekly"}},
	}
	for i, expected := range want {
		if capabilities[i].ID != expected.id {
			t.Fatalf("capability[%d] provider = %q, want declared order %q", i, capabilities[i].ID, expected.id)
		}
		var keys []string
		for _, window := range capabilities[i].Windows {
			keys = append(keys, window.Key)
		}
		if !reflect.DeepEqual(keys, expected.windows) {
			t.Fatalf("%s windows = %v, want %v", expected.id, keys, expected.windows)
		}
	}

	projected := projectStatusSnapshots([]usage.Snapshot{
		{Model: "future", Window: usage.Window5h, Pct: 99},
		{Model: "antigravity", Window: usage.Window5h, Pct: 88},
		{Model: "antigravity", Window: usage.WindowWeekly, Pct: 77},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "5h", Pct: 66},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "gemini-weekly", Pct: 55},
	})
	if got, want := projected, []usage.Snapshot{{Model: "antigravity", Window: usage.WindowWeekly, Pct: 55}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit projection = %#v, want only exact Antigravity gemini-weekly %#v", got, want)
	}
}

func TestHUDVisibilityFilterRecomputesWeeklyAsOfficialAcrossWidthSweep(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	projected := projectStatusSnapshots([]usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 42, UpdatedAt: now},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 18, UpdatedAt: now},
	})
	prefs := hudVisibilityPreferences{
		providers: map[string]bool{"claude": true},
		windows:   map[string]map[usage.Window]bool{"claude": {usage.Window5h: false, usage.WindowWeekly: true}},
	}
	filtered := filterStatusProjectionByVisibility(projected, prefs)
	models := buildModelDisplays(filtered)
	if len(models) != 1 || models[0].hasFive || !models[0].hasWeek {
		t.Fatalf("weekly-only model = %#v", models)
	}
	plan := newUsageSegmentPlan(models)
	steps := usageShedSteps(models, now)
	for _, step := range steps {
		if usageShedOrder[step.rule].name == "secondary window bar" {
			t.Fatalf("weekly-only official window became a secondary shed candidate: %#v", step)
		}
		step.apply(&plan)
	}
	minimum := intrender.VisualLen(renderUsageSegment(models, now, plan))
	for width := minimum; width <= 200; width++ {
		plain := intrender.StripTmuxEscapes(formatProjectedStatusUsage(filtered, width, now))
		if !strings.Contains(plain, "weekly") || strings.Contains(plain, "5h") {
			t.Fatalf("width %d weekly official drift: %q", width, plain)
		}
	}
	prefs.windows["claude"][usage.Window5h] = true
	prefs.windows["claude"][usage.WindowWeekly] = false
	fiveOnly := filterStatusProjectionByVisibility(projected, prefs)
	fiveModels := buildModelDisplays(fiveOnly)
	if len(fiveModels) != 1 || !fiveModels[0].hasFive || fiveModels[0].hasWeek {
		t.Fatalf("5h-only model = %#v", fiveModels)
	}
	plain := intrender.StripTmuxEscapes(formatProjectedStatusUsage(fiveOnly, 200, now))
	if !strings.Contains(plain, "5h") || strings.Contains(plain, "weekly") {
		t.Fatalf("weekly-off 5h-on projection drift: %q", plain)
	}
}

func TestHUDVisibilityFilterLeavesNoProviderWindowSeparatorOrStalenessResidue(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	projected := projectStatusSnapshots([]usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 42, UpdatedAt: now.Add(-3 * time.Hour)},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 18, UpdatedAt: now.Add(-3 * time.Hour)},
		{Model: "codex", Window: usage.Window5h, Pct: 71, UpdatedAt: now},
		{Model: "codex", Window: usage.WindowWeekly, Pct: 55, UpdatedAt: now},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "gemini-weekly", Pct: 38, UpdatedAt: now},
	})
	prefs := hudVisibilityPreferences{
		providers: map[string]bool{"claude": false, "codex": true, "antigravity": true},
		windows: map[string]map[usage.Window]bool{
			"claude":      {usage.Window5h: true, usage.WindowWeekly: true},
			"codex":       {usage.Window5h: false, usage.WindowWeekly: false},
			"antigravity": {usage.WindowWeekly: true},
		},
	}
	plain := intrender.StripTmuxEscapes(formatProjectedStatusUsage(filterStatusProjectionByVisibility(projected, prefs), 0, now))
	if !strings.HasPrefix(plain, "Antigravity") || strings.Contains(plain, "Claude") || strings.Contains(plain, "Codex") || strings.Contains(plain, "~~") || strings.Contains(plain, "   ") {
		t.Fatalf("filtered output retained provider/window/separator/staleness residue: %q", plain)
	}
	prefs.windows["antigravity"][usage.WindowWeekly] = false
	if got := formatProjectedStatusUsage(filterStatusProjectionByVisibility(projected, prefs), 0, now); got != "" {
		t.Fatalf("all provider windows off = %q, want empty ambient text", got)
	}
}

func TestHUDVisibilityProviderTogglePreservesOtherProviderBytesAndOrder(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	snaps := []usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 42, UpdatedAt: now},
		{Model: "codex", Window: usage.Window5h, Pct: 71, UpdatedAt: now},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "gemini-weekly", Pct: 38, UpdatedAt: now},
	}
	projected := projectStatusSnapshots(snaps)
	prefs := hudVisibilityPreferences{
		providers: map[string]bool{"claude": true, "codex": true, "antigravity": true},
		windows: map[string]map[usage.Window]bool{
			"claude":      {usage.Window5h: true, usage.WindowWeekly: true},
			"codex":       {usage.Window5h: true, usage.WindowWeekly: true},
			"antigravity": {usage.WindowWeekly: true},
		},
	}
	for _, hidden := range []string{"claude", "codex", "antigravity"} {
		prefs.providers[hidden] = false
		got := formatProjectedStatusUsage(filterStatusProjectionByVisibility(projected, prefs), 0, now)
		var survivors []usage.Snapshot
		for _, snapshot := range snaps {
			if snapshot.Model != hidden {
				survivors = append(survivors, snapshot)
			}
		}
		want := formatProjectedStatusUsage(projectStatusSnapshots(survivors), 0, now)
		if got != want {
			t.Fatalf("%s-off changed surviving provider bytes/order:\n got %q\nwant %q", hidden, got, want)
		}
		prefs.providers[hidden] = true
	}
	if got, want := formatProjectedStatusUsage(filterStatusProjectionByVisibility(projected, prefs), 0, now), formatProjectedStatusUsage(projected, 0, now); got != want {
		t.Fatalf("provider on restore drifted all-on bytes:\n got %q\nwant %q", got, want)
	}
}

func TestHUDVisibilityWindowTogglesAreIndependentAcrossCapabilityMatrix(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	projected := projectStatusSnapshots([]usage.Snapshot{
		{Model: "claude", Window: usage.Window5h, Pct: 42, UpdatedAt: now},
		{Model: "claude", Window: usage.WindowWeekly, Pct: 18, UpdatedAt: now},
		{Model: "codex", Window: usage.Window5h, Pct: 71, UpdatedAt: now},
		{Model: "codex", Window: usage.WindowWeekly, Pct: 55, UpdatedAt: now},
		{Model: "antigravity", Window: usage.WindowQuota, Bucket: "gemini-weekly", Pct: 38, UpdatedAt: now},
	})
	prefs := hudVisibilityPreferences{providers: map[string]bool{}, windows: map[string]map[usage.Window]bool{}}
	for _, capability := range HUDProviderCapabilities() {
		model := capability.Model
		prefs.providers[model] = true
		prefs.windows[model] = map[usage.Window]bool{}
		for _, window := range capability.Windows {
			prefs.windows[model][window.Window] = true
		}
	}
	for _, capability := range HUDProviderCapabilities() {
		for _, window := range capability.Windows {
			prefs.windows[capability.Model][window.Window] = false
			got := filterStatusProjectionByVisibility(projected, prefs)
			if len(got) != len(projected)-1 {
				t.Fatalf("%s/%s off kept %d rows, want %d: %#v", capability.ID, window.Key, len(got), len(projected)-1, got)
			}
			for _, snapshot := range got {
				if snapshot.Model == capability.Model && snapshot.Window == window.Window {
					t.Fatalf("%s/%s off retained its snapshot: %#v", capability.ID, window.Key, got)
				}
			}
			prefs.windows[capability.Model][window.Window] = true
		}
	}
	if got := filterStatusProjectionByVisibility(projected, prefs); !reflect.DeepEqual(got, projected) {
		t.Fatalf("window on restore changed all-on projection:\n got %#v\nwant %#v", got, projected)
	}
}

func TestUsageStatusVisibilityFiltersAfterCollectionAndLeavesExplicitCLIStateLossless(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	home := t.TempDir()
	configHome := filepath.Join(home, "config")
	paths, err := config.Homes{HomeDir: home, ConfigHome: configHome}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"claude", "codex", "antigravity"} {
		if err := config.SaveStatusbarVisibilityFile(paths.StatusbarAgentUsageProviderVisibilityFile(provider), config.StatusbarVisibilityOff); err != nil {
			t.Fatal(err)
		}
	}
	store := usage.NewStore(filepath.Join(home, "usage"))
	claude := &stubAdapter{name: "claude", snaps: []usage.Snapshot{{Model: "claude", Window: usage.Window5h, Pct: 42, UpdatedAt: now}}}
	c := New(func() time.Time { return now })
	c.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "XDG_CONFIG_HOME":
			return configHome
		}
		return ""
	}
	c.enabledAgentsFn = func() ([]config.AIAgentProvider, error) { return []config.AIAgentProvider{config.AIAgentClaude}, nil }
	c.managerFn = scopedUsageManagerFactory(t, store, now, map[string]*stubAdapter{"claude": claude})

	statusOut := &bytes.Buffer{}
	if err := c.RunStatus(nil, statusOut, io.Discard); err != nil {
		t.Fatal(err)
	}
	if statusOut.Len() != 0 {
		t.Fatalf("all providers off status = %q, want empty", statusOut.String())
	}
	if claude.collectCalls != 1 {
		t.Fatalf("hidden provider collect calls = %d, want 1", claude.collectCalls)
	}
	popupState, _, _, err := c.CachedState()
	if err != nil || len(popupState.Snapshots) != 1 || popupState.Snapshots[0].Model != "claude" {
		t.Fatalf("CachedState popup input lost hidden provider: %#v, %v", popupState, err)
	}
	explicitTable := &bytes.Buffer{}
	if err := c.Run([]string{"--model", "claude"}, explicitTable, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explicitTable.String(), "claude") || !strings.Contains(explicitTable.String(), "5h") || !strings.Contains(explicitTable.String(), "42%") {
		t.Fatalf("explicit table lost hidden row: %s", explicitTable.String())
	}

	explicitOut := &bytes.Buffer{}
	if err := c.Run([]string{"--model", "claude", "--json"}, explicitOut, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explicitOut.String(), `"model": "claude"`) || !strings.Contains(explicitOut.String(), `"window": "5h"`) {
		t.Fatalf("explicit JSON lost hidden row: %s", explicitOut.String())
	}
	state, err := store.LoadState()
	if err != nil || len(state.Snapshots) != 1 {
		t.Fatalf("cache after visibility = %#v, %v", state, err)
	}
}
