package hooks

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMergeEffectiveProjectWinsConflicts(t *testing.T) {
	t.Parallel()

	global := ProjectConfig{
		StartupRun: "global-cmd",
		Env: map[string]string{
			"EDITOR":       "vim",
			"DATABASE_URL": "postgres://global",
		},
		Kube: KubeConfig{
			Context:   "global-cluster",
			Namespace: "default",
		},
	}
	project := ProjectConfig{
		StartupRun: "claude",
		Env: map[string]string{
			"DATABASE_URL": "postgres://project",
			"GH_TOKEN":     "ghp_abc",
		},
		Kube: KubeConfig{
			Context: "dev-cluster",
		},
	}

	got := MergeEffective(global, project)

	// [env] — DATABASE_URL conflict → project wins; EDITOR only in global;
	// GH_TOKEN only in project. Section label = merged.
	wantEnv := []EffectiveEntry{
		{Key: "DATABASE_URL", Value: "postgres://project", Source: EffectiveSourceProject},
		{Key: "EDITOR", Value: "vim", Source: EffectiveSourceGlobal},
		{Key: "GH_TOKEN", Value: "ghp_abc", Source: EffectiveSourceProject},
	}
	if !reflect.DeepEqual(got.Env.Entries, wantEnv) {
		t.Fatalf("env entries = %+v, want %+v", got.Env.Entries, wantEnv)
	}
	if got.Env.Source != EffectiveSourceMerged {
		t.Fatalf("env section source = %q, want merged", got.Env.Source)
	}

	// [kube] — context: project wins (project value); namespace: only in global.
	wantKube := []EffectiveEntry{
		{Key: "context", Value: "dev-cluster", Source: EffectiveSourceProject},
		{Key: "namespace", Value: "default", Source: EffectiveSourceGlobal},
	}
	if !reflect.DeepEqual(got.Kube.Entries, wantKube) {
		t.Fatalf("kube entries = %+v, want %+v", got.Kube.Entries, wantKube)
	}
	if got.Kube.Source != EffectiveSourceMerged {
		t.Fatalf("kube section source = %q, want merged", got.Kube.Source)
	}

	// [startup] — single scalar, project wins.
	wantStartup := []EffectiveEntry{
		{Key: "run", Value: "claude", Source: EffectiveSourceProject},
	}
	if !reflect.DeepEqual(got.Startup.Entries, wantStartup) {
		t.Fatalf("startup entries = %+v, want %+v", got.Startup.Entries, wantStartup)
	}
	if got.Startup.Source != EffectiveSourceProject {
		t.Fatalf("startup section source = %q, want project", got.Startup.Source)
	}
}

func TestMergeEffectiveDefaultsWhenBothEmpty(t *testing.T) {
	t.Parallel()

	got := MergeEffective(ProjectConfig{}, ProjectConfig{})

	if len(got.Env.Entries) != 0 {
		t.Fatalf("env entries = %+v, want empty", got.Env.Entries)
	}
	if got.Env.Source != EffectiveSourceDefault {
		t.Fatalf("env source on empty merge = %q, want default", got.Env.Source)
	}
	// kube / startup always emit scalar rows (so the UI keeps stable
	// row positions); when unset they label as default.
	for _, entry := range got.Kube.Entries {
		if entry.Source != EffectiveSourceDefault {
			t.Fatalf("kube entry %q source = %q, want default", entry.Key, entry.Source)
		}
	}
	if got.Kube.Source != EffectiveSourceDefault {
		t.Fatalf("kube section source = %q, want default", got.Kube.Source)
	}
	for _, entry := range got.Startup.Entries {
		if entry.Source != EffectiveSourceDefault {
			t.Fatalf("startup entry %q source = %q, want default", entry.Key, entry.Source)
		}
	}
}

func TestMergeEffectiveGlobalOnlySectionLabel(t *testing.T) {
	t.Parallel()

	global := ProjectConfig{
		Env: map[string]string{"EDITOR": "vim"},
		Kube: KubeConfig{
			Namespace: "team-foo",
		},
	}
	got := MergeEffective(global, ProjectConfig{})

	if got.Env.Source != EffectiveSourceGlobal {
		t.Fatalf("env section source = %q, want global (only global entries)", got.Env.Source)
	}
	if got.Kube.Source != EffectiveSourceGlobal {
		t.Fatalf("kube section source = %q, want global (only global namespace defined)", got.Kube.Source)
	}
}

func TestMergeEffectiveProjectOnlySectionLabel(t *testing.T) {
	t.Parallel()

	project := ProjectConfig{
		StartupRun: "claude",
		Env:        map[string]string{"GH_TOKEN": "ghp_xxx"},
	}
	got := MergeEffective(ProjectConfig{}, project)

	if got.Env.Source != EffectiveSourceProject {
		t.Fatalf("env section source = %q, want project", got.Env.Source)
	}
	if got.Startup.Source != EffectiveSourceProject {
		t.Fatalf("startup section source = %q, want project", got.Startup.Source)
	}
}

func TestLoadProjectConfigFileMissingReturnsEmpty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg, err := LoadProjectConfigFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatalf("LoadProjectConfigFile() missing path error = %v", err)
	}
	if !reflect.DeepEqual(cfg, (ProjectConfig{})) {
		t.Fatalf("missing config = %+v, want zero ProjectConfig", cfg)
	}
}

func TestLoadProjectConfigFileParsesWhenPresent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[env]\nEDITOR = \"vim\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadProjectConfigFile(path)
	if err != nil {
		t.Fatalf("LoadProjectConfigFile() error = %v", err)
	}
	if got, want := cfg.Env["EDITOR"], "vim"; got != want {
		t.Fatalf("env EDITOR = %q, want %q", got, want)
	}
}

func TestIsSensitiveEnvKey(t *testing.T) {
	t.Parallel()

	sensitive := []string{
		"GH_TOKEN",
		"OPENAI_API_KEY",
		"DATABASE_PASSWORD",
		"MY_SECRET",
		"AWS_CREDENTIAL_FILE",
		"some_password_x", // case-insensitive
	}
	for _, key := range sensitive {
		if !IsSensitiveEnvKey(key) {
			t.Fatalf("IsSensitiveEnvKey(%q) = false, want true", key)
		}
	}
	nonSensitive := []string{
		"EDITOR",
		"DATABASE_URL",
		"PATH",
		"",
	}
	for _, key := range nonSensitive {
		if IsSensitiveEnvKey(key) {
			t.Fatalf("IsSensitiveEnvKey(%q) = true, want false", key)
		}
	}
}

func TestDisplayEnvValueRedactsSensitive(t *testing.T) {
	t.Parallel()

	if got := DisplayEnvValue("GH_TOKEN", "ghp_abc123"); got != SensitiveRedaction {
		t.Fatalf("DisplayEnvValue sensitive = %q, want %q", got, SensitiveRedaction)
	}
	if got := DisplayEnvValue("EDITOR", "vim"); got != "vim" {
		t.Fatalf("DisplayEnvValue plain = %q, want vim", got)
	}
	if got := DisplayEnvValue("EDITOR", ""); got != "(unset)" {
		t.Fatalf("DisplayEnvValue empty = %q, want (unset)", got)
	}
}

// TestMergeEffectiveHooksProjectWinsOnConflict covers the Phase 4 merge
// engine: when both axes define a [hooks.<event>] entry for the same event,
// the project value wins and the row is labelled project.
func TestMergeEffectiveHooksProjectWinsOnConflict(t *testing.T) {
	t.Parallel()

	global := ProjectConfig{
		Hooks: map[Event]string{
			EventPostCreate: "echo global-post",
		},
	}
	project := ProjectConfig{
		Hooks: map[Event]string{
			EventPostCreate: "echo project-post",
		},
	}
	got := MergeEffective(global, project).Hooks
	want := []EffectiveEntry{
		{Key: string(EventPostCreate), Value: "echo project-post", Source: EffectiveSourceProject},
	}
	if !reflect.DeepEqual(got.Entries, want) {
		t.Fatalf("hooks entries = %+v, want %+v", got.Entries, want)
	}
	if got.Source != EffectiveSourceProject {
		t.Fatalf("hooks section source = %q, want project", got.Source)
	}
}

// TestMergeEffectiveHooksGlobalOnlySourceLabel covers the case where a hook
// is defined only in the global config — the row reports the global axis and
// the section header summarizes the same single axis.
func TestMergeEffectiveHooksGlobalOnlySourceLabel(t *testing.T) {
	t.Parallel()

	global := ProjectConfig{
		Hooks: map[Event]string{
			EventPreCreate: "echo global-pre",
		},
	}
	got := MergeEffective(global, ProjectConfig{}).Hooks
	want := []EffectiveEntry{
		{Key: string(EventPreCreate), Value: "echo global-pre", Source: EffectiveSourceGlobal},
	}
	if !reflect.DeepEqual(got.Entries, want) {
		t.Fatalf("hooks entries = %+v, want %+v", got.Entries, want)
	}
	if got.Source != EffectiveSourceGlobal {
		t.Fatalf("hooks section source = %q, want global", got.Source)
	}
}

// TestMergeEffectiveHooksProjectOnlySourceLabel covers the symmetric case:
// only the project config defines a hook, so both row and section label
// resolve to project.
func TestMergeEffectiveHooksProjectOnlySourceLabel(t *testing.T) {
	t.Parallel()

	project := ProjectConfig{
		Hooks: map[Event]string{
			EventPostAttach: "echo project-attach",
		},
	}
	got := MergeEffective(ProjectConfig{}, project).Hooks
	want := []EffectiveEntry{
		{Key: string(EventPostAttach), Value: "echo project-attach", Source: EffectiveSourceProject},
	}
	if !reflect.DeepEqual(got.Entries, want) {
		t.Fatalf("hooks entries = %+v, want %+v", got.Entries, want)
	}
	if got.Source != EffectiveSourceProject {
		t.Fatalf("hooks section source = %q, want project", got.Source)
	}
}

// TestMergeEffectiveHooksMixedAxesSectionLabel covers the case where each
// axis contributes a hook for a *different* event — neither row alone is
// ambiguous, but the section header should summarize as merged because both
// axes contributed entries.
func TestMergeEffectiveHooksMixedAxesSectionLabel(t *testing.T) {
	t.Parallel()

	global := ProjectConfig{
		Hooks: map[Event]string{
			EventPreCreate: "echo global-pre",
		},
	}
	project := ProjectConfig{
		Hooks: map[Event]string{
			EventPostCreate: "echo project-post",
		},
	}
	got := MergeEffective(global, project).Hooks
	if got.Source != EffectiveSourceMerged {
		t.Fatalf("hooks section source = %q, want merged (project + global contributed)", got.Source)
	}
	// Stable display order follows SupportedEvents (pre-create before
	// post-create), so the per-axis order is global-then-project here.
	if len(got.Entries) != 2 {
		t.Fatalf("hooks entries len = %d, want 2: %+v", len(got.Entries), got.Entries)
	}
	if got.Entries[0].Key != string(EventPreCreate) || got.Entries[0].Source != EffectiveSourceGlobal {
		t.Fatalf("entries[0] = %+v, want pre-create/global", got.Entries[0])
	}
	if got.Entries[1].Key != string(EventPostCreate) || got.Entries[1].Source != EffectiveSourceProject {
		t.Fatalf("entries[1] = %+v, want post-create/project", got.Entries[1])
	}
}

// TestMergeEffectiveHooksOmitsUndefinedEvents documents the Phase 4 display
// decision: events that are unset on both axes are not emitted as rows. The
// section is allowed to be entirely empty, which keeps the popup uncluttered
// when no lifecycle is wired up.
func TestMergeEffectiveHooksOmitsUndefinedEvents(t *testing.T) {
	t.Parallel()

	got := MergeEffective(ProjectConfig{}, ProjectConfig{}).Hooks
	if len(got.Entries) != 0 {
		t.Fatalf("hooks entries = %+v, want empty (no events defined)", got.Entries)
	}
	if got.Source != EffectiveSourceDefault {
		t.Fatalf("hooks section source = %q, want default when both axes empty", got.Source)
	}
}

// TestMergeEffectiveHooksRowOrderFollowsSupportedEvents pins the row order so
// the popup renders lifecycle events in the same order the hooks catalog
// defines them — pre-create, post-create, post-attach, send-noti.
func TestMergeEffectiveHooksRowOrderFollowsSupportedEvents(t *testing.T) {
	t.Parallel()

	project := ProjectConfig{
		Hooks: map[Event]string{
			EventPostAttach: "echo attach",
			EventPreCreate:  "echo pre",
			EventPostCreate: "echo post",
			EventSendNoti:   "echo send-noti",
		},
	}
	got := MergeEffective(ProjectConfig{}, project).Hooks
	wantOrder := []Event{EventPreCreate, EventPostCreate, EventPostAttach, EventSendNoti}
	if len(got.Entries) != len(wantOrder) {
		t.Fatalf("hooks entries len = %d, want %d: %+v", len(got.Entries), len(wantOrder), got.Entries)
	}
	for i, want := range wantOrder {
		if got.Entries[i].Key != string(want) {
			t.Fatalf("entries[%d].Key = %q, want %q", i, got.Entries[i].Key, want)
		}
	}
}

// TestEffectiveConfigSectionsIncludesHooks pins the Sections() display order:
// env, kube, startup, then hooks. The settings popup depends on this order
// to lay out the page.
func TestEffectiveConfigSectionsIncludesHooks(t *testing.T) {
	t.Parallel()

	got := MergeEffective(ProjectConfig{}, ProjectConfig{}).Sections()
	wantNames := []string{"env", "kube", "startup", "hooks"}
	if len(got) != len(wantNames) {
		t.Fatalf("Sections() len = %d, want %d: %+v", len(got), len(wantNames), got)
	}
	for i, name := range wantNames {
		if got[i].Name != name {
			t.Fatalf("Sections()[%d].Name = %q, want %q", i, got[i].Name, name)
		}
	}
}
