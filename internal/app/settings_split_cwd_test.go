package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/i18n"
)

// splitCWDSettingsCommand wires a Settings command whose config seams point at
// an isolated config home, and whose Project context is the given root, so the
// project tier is exercised without touching the developer's own config.
func splitCWDSettingsCommand(t *testing.T, configHome, projectRoot string) *settingsCommand {
	t.Helper()

	return &settingsCommand{
		ai:      testAICommand(configHome),
		homeDir: func() (string, error) { return configHome, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_CONFIG_HOME":
				return configHome
			case "PROJMUX_CWD":
				return projectRoot
			default:
				return ""
			}
		},
	}
}

func writeSplitCWDGlobalConfig(t *testing.T, cmd *settingsCommand, body string) {
	t.Helper()

	path, err := cmd.globalConfigPath()
	if err != nil {
		t.Fatalf("resolve global config path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create global config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}
}

func writeSplitCWDProjectConfig(t *testing.T, root, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Join(root, ".projmux"), 0o755); err != nil {
		t.Fatalf("create project config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".projmux", "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write project config: %v", err)
	}
}

// TestSettingsSplitCWDFromRowTracksTheUIResolver is the display half: the row
// summary and the chooser's active toggle must always be what the UI split
// resolver returns for the same command, including when a project-tier config
// overrides the global one. The expected value is recomputed from the resolver
// rather than written out, so a row that drifts away from the resolver fails
// here instead of lying in the UI.
func TestSettingsSplitCWDFromRowTracksTheUIResolver(t *testing.T) {
	t.Parallel()

	configHome := t.TempDir()
	root := t.TempDir()
	cmd := splitCWDSettingsCommand(t, configHome, root)

	assertRowMatchesResolver := func(stage string, wantSource splitCWDSource, wantOrigin splitCWDOrigin) {
		t.Helper()

		resolved := resolveUISplitCWDSource("", root, cmd.homeDir, cmd.lookupEnv)
		if resolved.Source != wantSource || resolved.Origin != wantOrigin {
			t.Fatalf("%s: resolver = %+v, want %s/%s", stage, resolved, wantSource, wantOrigin)
		}
		if got := cmd.currentSplitCWDFrom(); got != resolved {
			t.Fatalf("%s: Settings resolution = %+v, want the UI resolver's %+v", stage, got, resolved)
		}
		wantSummary := splitCWDSourceLabelLocale(cmd.locale(), resolved.Source) + " - " + string(resolved.Origin)
		if got := cmd.splitCWDFromSummary(); got != wantSummary {
			t.Fatalf("%s: row summary = %q, want %q", stage, got, wantSummary)
		}

		rows := cmd.aiRootEntries()
		if !hasEntryValue(rows, settingsAISplitCWDFrom) {
			t.Fatalf("%s: AI root entries = %#v, want the split start row", stage, rows)
		}
		if !hasEntryLabelContaining(rows, wantSummary) {
			t.Fatalf("%s: AI root entries = %#v, want the row to show %q", stage, rows, wantSummary)
		}

		// The chooser toggles exactly the resolved source, so the drill-in and
		// the summary can never disagree.
		active := ""
		for _, entry := range cmd.aiSplitCWDFromEntries() {
			if !strings.HasPrefix(entry.Value, settingsActionPrefixAISplitCWD) {
				continue
			}
			if strings.Contains(entry.Label, settingsGlyphToggle) {
				active = strings.TrimPrefix(entry.Value, settingsActionPrefixAISplitCWD)
			}
		}
		if active != string(resolved.Source) {
			t.Fatalf("%s: chooser toggled %q, want the resolved %q", stage, active, resolved.Source)
		}
	}

	assertRowMatchesResolver("no config", splitCWDFromProject, splitCWDOriginDefault)

	writeSplitCWDGlobalConfig(t, cmd, "[ai]\nsplit_cwd_from = \"pane\"\n")
	assertRowMatchesResolver("global pane", splitCWDFromPane, splitCWDOriginGlobal)

	// A project-tier value outranks the global one, and the row has to say so.
	writeSplitCWDProjectConfig(t, root, "[ai]\nsplit_cwd_from = \"project\"\n")
	assertRowMatchesResolver("project override", splitCWDFromProject, splitCWDOriginProject)
}

// TestSettingsSplitCWDFromWritesOnlyTheGlobalKeyAndMovesTheNextUISplit is the
// write half. Choosing a value writes exactly `[ai] split_cwd_from` in the
// global config, leaves every other key it round-trips untouched, and the very
// next UI split starts in the active Pane directory.
func TestSettingsSplitCWDFromWritesOnlyTheGlobalKeyAndMovesTheNextUISplit(t *testing.T) {
	t.Parallel()

	fx := newSplitCWDIntentFixture(t)
	cmd := splitCWDSettingsCommand(t, fx.configHome, "")
	// Everything a global config can hold that this row must not touch.
	const before = "[hooks.post-create]\nrun = \"echo hi\"\n\n[env]\nFOO = \"bar\"\n\n[ui]\nlocale = \"en-US\"\n\n[ai]\nresume_picker_limit = 50\nresume_scan_depth = 2\n"
	writeSplitCWDGlobalConfig(t, cmd, before)

	var stdout bytes.Buffer
	if err := cmd.setSplitCWDFrom(splitCWDFromPane, &stdout); err != nil {
		t.Fatalf("setSplitCWDFrom error = %v", err)
	}
	if got, want := stdout.String(), "New splits start in: pane\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}

	path, err := cmd.globalConfigPath()
	if err != nil {
		t.Fatalf("resolve global config path: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read global config: %v", err)
	}
	after := string(raw)
	if after != before+"split_cwd_from = \"pane\"\n" {
		t.Fatalf("global config after the write:\n%s\nwant exactly the previous bytes plus one split_cwd_from line:\n%s", after, before)
	}
	if got := cmd.currentSplitCWDFrom(); got.Source != splitCWDFromPane || got.Origin != splitCWDOriginGlobal {
		t.Fatalf("resolution after the write = %+v, want pane/global", got)
	}

	// The next UI split really starts there. The fixture and the Settings
	// command share one config home, so this is the value just written.
	sub := fx.subdir("services", "api")
	fx.runner.cwds[fx.anchorID] = sub
	var out, errOut bytes.Buffer
	if err := fx.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerDirectShell, placement: "right", anchorPaneID: fx.anchorID,
	}, &out, &errOut); err != nil {
		t.Fatalf("UI intent create failed: %v (stderr %q)", err, errOut.String())
	}
	if got := splitArgvCWD(t, fx.tmux.calls); got != sub {
		t.Fatalf("UI split -c = %q, want the active Pane directory %q", got, sub)
	}
}

// TestSettingsSplitCWDFromCopyIsExactInBothLocales pins the shipped copy. Both
// locales carry the owner Project root rule *and* the CLI boundary, because the
// setting is half-true without the second half: a script keeps starting in the
// Project root whatever is chosen here.
func TestSettingsSplitCWDFromCopyIsExactInBothLocales(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		locale      i18n.Locale
		row         string
		project     string
		pane        string
		description string
		boundary    string
	}{
		{
			locale:      i18n.FallbackLocale,
			row:         "New splits start in",
			project:     "Project root",
			pane:        "Current Pane directory",
			description: "Pane directory is used only inside the Project root; the CLI does not follow this setting",
			boundary:    "the CLI does not follow this setting",
		},
		{
			locale:      i18n.Locale("ko-KR"),
			row:         "새 분할 시작 위치",
			project:     "프로젝트 루트",
			pane:        "현재 Pane 디렉터리",
			description: "Pane 디렉터리가 프로젝트 루트 안일 때만 사용; CLI는 이 설정을 따르지 않음",
			boundary:    "CLI는 이 설정을 따르지 않음",
		},
	} {
		t.Run(string(test.locale), func(t *testing.T) {
			t.Parallel()

			if got := settingsNavLabelLocale(test.locale, settingsNavAISplitCWD); got != test.row {
				t.Fatalf("row label = %q, want %q", got, test.row)
			}
			if got := splitCWDSourceLabelLocale(test.locale, splitCWDFromProject); got != test.project {
				t.Fatalf("project value label = %q, want %q", got, test.project)
			}
			if got := splitCWDSourceLabelLocale(test.locale, splitCWDFromPane); got != test.pane {
				t.Fatalf("pane value label = %q, want %q", got, test.pane)
			}
			rendered := localizeUIText(test.locale, splitCWDFromRowDescription)
			if rendered != test.description {
				t.Fatalf("row description = %q, want %q", rendered, test.description)
			}
			if !strings.Contains(rendered, test.boundary) {
				t.Fatalf("row description = %q, want it to carry the CLI boundary %q", rendered, test.boundary)
			}

			// The rendered chooser rows really carry that description, in this
			// locale, for both values.
			configHome := t.TempDir()
			cmd := splitCWDSettingsCommand(t, configHome, "")
			writeSplitCWDGlobalConfig(t, cmd, "[ui]\nlocale = "+quoteLocale(test.locale)+"\n")
			entries := cmd.aiSplitCWDFromEntries()
			for _, want := range []string{test.project, test.pane} {
				if !hasEntryLabelContainingAll(entries, want, test.boundary) {
					t.Fatalf("chooser entries = %#v, want a %q row carrying %q", entries, want, test.boundary)
				}
			}
		})
	}
}

func quoteLocale(locale i18n.Locale) string {
	return "\"" + string(locale) + "\""
}
