package render

import "testing"

func TestBuildSwitchRowsFormatsSessionModeAndPath(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/home/tester/workspace",
		DisplayPath: "~/workspace",
		SessionName: "workspace",
		ModeLabel:   "existing",
		GitBranch:   "main",
		UI:          "popup",
	}})

	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if got, want := rows[0].Label, "[ ]     \x1b[32m[Existing]\x1b[0m  workspace  ~/workspace"; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
	if got, want := rows[0].Value, "/home/tester/workspace"; got != want {
		t.Fatalf("value = %q, want %q", got, want)
	}
	if got, want := rows[0].Item.Title, "workspace"; got != want {
		t.Fatalf("item title = %q, want %q", got, want)
	}
	if got, want := rows[0].Item.EffectiveSearchText(), "workspace"; got != want {
		t.Fatalf("item search text = %q, want %q", got, want)
	}
	if got, want := rows[0].Item.MetaLines, []string{"~/workspace [main]"}; !equalStringSlices(got, want) {
		t.Fatalf("item meta lines = %q, want %q", got, want)
	}
}

func TestBuildSwitchRowsOmitsBlankMode(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/tmp/app",
		SessionName: "tmp-app",
		UI:          "popup",
	}})

	if got, want := rows[0].Label, "[ ]     tmp-app  /tmp/app"; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
}

func TestPrettyPathPrefersRepoRootAlias(t *testing.T) {
	t.Parallel()

	if got, want := PrettyPath("/home/tester/source/repos/app", "/home/tester", "/home/tester/source/repos"), "~rp/app"; got != want {
		t.Fatalf("PrettyPath() = %q, want %q", got, want)
	}
}

func TestPrettyPathFallsBackToHomeAlias(t *testing.T) {
	t.Parallel()

	if got, want := PrettyPath("/home/tester/workspace", "/home/tester", "/repo"), "~/workspace"; got != want {
		t.Fatalf("PrettyPath() = %q, want %q", got, want)
	}
}

func TestBuildSwitchRowsSanitizesTabsAndNewlines(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/tmp/app\tone",
		SessionName: "tmp\napp",
		ModeLabel:   "new\tstate",
		UI:          "popup",
	}})

	if got, want := rows[0].Label, "[ ]     [new state]  tmp app  /tmp/app one"; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
}

func TestBuildSwitchRowsSidebarUsesAnsiStylingForModeAndToggles(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/home/tester/source/repos/app",
		DisplayPath: "~rp/app",
		SessionName: "app",
		ModeLabel:   "existing",
		UI:          "sidebar",
		Pinned:      true,
		Tagged:      true,
	}})

	const want = "  \x1b[31mx\x1b[0m \x1b[33m*\x1b[0m \x1b[1m\x1b[32mapp\x1b[0m \x1b[2m~rp/app\x1b[0m"
	if got := rows[0].Label; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
}

func TestBuildSwitchRowsSidebarLeavesNewSessionNameUncolored(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/home/tester/source/repos/app",
		DisplayPath: "~rp/app",
		SessionName: "app",
		ModeLabel:   "new",
		UI:          "sidebar",
	}})

	const want = "  app \x1b[2m~rp/app\x1b[0m"
	if got := rows[0].Label; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
}

func TestBuildSwitchRowsSidebarShowsAttentionBadge(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:          "/home/tester/source/repos/app",
		DisplayPath:   "~rp/app",
		SessionName:   "app",
		ModeLabel:     "existing",
		UI:            "sidebar",
		AttentionRank: 2,
	}})

	const want = "\x1b[33m●\x1b[0m \x1b[1m\x1b[32mapp\x1b[0m \x1b[2m~rp/app\x1b[0m"
	if got := rows[0].Label; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
	if got, want := rows[0].Item.Badges, []string{"needs review"}; !equalStringSlices(got, want) {
		t.Fatalf("item badges = %q, want %q", got, want)
	}
}

func TestBuildSwitchRowsSidebarFormatsSettingsRow(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "__projmux_settings__",
		DisplayPath: "Settings",
		UI:          "sidebar",
	}})

	const want = "  \x1b[1m\x1b[36mSettings\x1b[0m  \x1b[2mmanage pinned directories\x1b[0m"
	if got := rows[0].Label; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
	if got, want := rows[0].Item.Title, "Settings"; got != want {
		t.Fatalf("item title = %q, want %q", got, want)
	}
	if got, want := rows[0].Item.Value, "__projmux_settings__"; got != want {
		t.Fatalf("item value = %q, want %q", got, want)
	}
}

func TestBuildSwitchPickerItemsReturnsBackendNeutralRows(t *testing.T) {
	t.Parallel()

	items := BuildSwitchPickerItems([]SwitchCandidate{{
		Path:        "/home/tester/source/repos/app",
		DisplayPath: "~rp/app",
		DisplayName: "app",
		SessionName: "repos-app",
		ModeLabel:   "new",
		GitBranch:   "topic",
		Pinned:      true,
		Tagged:      true,
	}})

	if len(items) != 1 {
		t.Fatalf("item count = %d, want 1", len(items))
	}
	item := items[0]
	if got, want := item.Title, "app"; got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
	if got, want := item.Value, "/home/tester/source/repos/app"; got != want {
		t.Fatalf("value = %q, want %q", got, want)
	}
	if got, want := item.MetaLines, []string{"~rp/app [topic]"}; !equalStringSlices(got, want) {
		t.Fatalf("meta lines = %q, want %q", got, want)
	}
	if got, want := item.Badges, []string{"tagged", "pinned"}; !equalStringSlices(got, want) {
		t.Fatalf("badges = %q, want %q", got, want)
	}
}

func TestFormatSwitchCardLabelShowsMultilineContext(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:          "/home/tester/source/repos/app",
		DisplayPath:   "~rp/app",
		DisplayName:   "app",
		SessionName:   "repos-app",
		ModeLabel:     "existing",
		GitBranch:     "main",
		WindowNames:   []string{"shell", "server", "tests"},
		AttentionRank: 1,
		Pinned:        true,
	}})

	got := FormatSwitchCardLabel(rows[0].Item)
	const want = "\x1b[1m\x1b[32mapp\x1b[0m \x1b[32m●\x1b[0m\n\x1b[2m  ~rp/app [main]\x1b[0m\n\x1b[2m  \x1b[38;5;16;48;5;114m shell \x1b[0m \x1b[38;5;252;48;5;238m server \x1b[0m \x1b[38;5;252;48;5;238m tests \x1b[0m\x1b[0m"
	if got != want {
		t.Fatalf("card label = %q, want %q", got, want)
	}
}

func equalStringSlices(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
