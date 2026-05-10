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
