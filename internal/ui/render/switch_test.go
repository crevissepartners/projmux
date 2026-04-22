package render

import "testing"

func TestBuildSwitchRowsFormatsSessionModeAndPath(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/home/tester/dotfiles",
		SessionName: "dotfiles",
		ModeLabel:   "existing",
	}})

	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if got, want := rows[0].Label, "dotfiles  [existing]  /home/tester/dotfiles"; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
	if got, want := rows[0].Value, "/home/tester/dotfiles"; got != want {
		t.Fatalf("value = %q, want %q", got, want)
	}
}

func TestBuildSwitchRowsOmitsBlankMode(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/tmp/app",
		SessionName: "tmp-app",
	}})

	if got, want := rows[0].Label, "tmp-app  /tmp/app"; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
}

func TestBuildSwitchRowsSanitizesTabsAndNewlines(t *testing.T) {
	t.Parallel()

	rows := BuildSwitchRows([]SwitchCandidate{{
		Path:        "/tmp/app\tone",
		SessionName: "tmp\napp",
		ModeLabel:   "new\tstate",
	}})

	if got, want := rows[0].Label, "tmp app  [new state]  /tmp/app one"; got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
}
