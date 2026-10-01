package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/app/updatecmd"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// judgedReleaseChannel answers the question the acceptance criteria ask —
// which channel the next update judgment runs on — by going through the real
// resolution order rather than reading the seam's raw string. The seam is
// deliberately unnormalized (an empty answer means "nothing here, keep
// falling back"); normalization belongs to the judgment.
func judgedReleaseChannel(lookupEnv func(string) string, homeDir func() (string, error)) string {
	cmd := &updatecmd.Command{
		Getenv:               lookupEnv,
		ReleaseChannelSource: updatecmd.NewReleaseChannelSource(lookupEnv, homeDir),
	}
	return cmd.ReleaseChannel()
}

// TestAboutUpdatesTogglesTheReleaseChannelOptIn is the round trip the user
// actually performs: open About > Updates, press the release channel row, and
// have the next judgment ask on the other channel. It asserts both halves —
// what the row says and what the update command resolves — because a toggle
// that only repaints itself would satisfy neither half of C-3.
func TestAboutUpdatesTogglesTheReleaseChannelOptIn(t *testing.T) {
	home := t.TempDir()
	var opened, afterOptIn, afterOptOut intpickercompat.Options
	runner, native := scriptedPicker(t, []pickerStep{
		{
			observe: func(options intpickercompat.Options) { opened = options },
			reply:   intpickercompat.Result{Key: "enter", Value: settingsUpdateReleaseChannel},
		},
		{
			observe: func(options intpickercompat.Options) { afterOptIn = options },
			reply:   intpickercompat.Result{Key: "enter", Value: settingsUpdateReleaseChannel},
		},
		{
			observe: func(options intpickercompat.Options) { afterOptOut = options },
			reply:   intpickercompat.Result{Key: "enter", Value: settingsBackValue},
		},
	})
	lookupEnv := func(string) string { return "" }
	homeDir := func() (string, error) { return home, nil }
	cmd := &settingsCommand{
		homeDir:      homeDir,
		lookupEnv:    lookupEnv,
		runner:       runner,
		nativePicker: native,
	}
	if got := judgedReleaseChannel(lookupEnv, homeDir); got != updatecmd.ReleaseChannelStable {
		t.Fatalf("release channel before the toggle = %q, want %q", got, updatecmd.ReleaseChannelStable)
	}
	if err := cmd.runAboutUpdatesSection(&bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runAboutUpdatesSection() error = %v", err)
	}

	if !hasEntryLabelContainingAll(opened.Entries, "Release channel", "stable", "never offered") {
		t.Fatalf("opened entries = %#v, want the release channel row defaulting to stable", opened.Entries)
	}
	if !hasEntryLabelContainingAll(afterOptIn.Entries, "Release channel", "rc", "prereleases included") {
		t.Fatalf("entries after opt-in = %#v, want the release channel row on rc", afterOptIn.Entries)
	}
	if !hasEntryLabelContainingAll(afterOptOut.Entries, "Release channel", "stable", "never offered") {
		t.Fatalf("entries after opt-out = %#v, want the release channel row back on stable", afterOptOut.Entries)
	}
	if got := judgedReleaseChannel(lookupEnv, homeDir); got != updatecmd.ReleaseChannelStable {
		t.Fatalf("release channel after opting back out = %q, want %q", got, updatecmd.ReleaseChannelStable)
	}
	configToml := readFile(t, filepath.Join(home, ".config", "projmux", "config.toml"))
	if !strings.Contains(configToml, "[update]\nrelease_channel = \"stable\"") {
		t.Fatalf("config.toml = %q, want the explicit stable opt-out persisted", configToml)
	}
}

// TestReleaseChannelOptInIsVisibleToTheNextJudgment closes the other half of
// acceptance 1: the stored rc value is what the update command judges on, and
// it survives being read by a freshly built resolver rather than living in the
// Settings process only.
func TestReleaseChannelOptInIsVisibleToTheNextJudgment(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	lookupEnv := func(string) string { return "" }
	homeDir := func() (string, error) { return home, nil }
	cmd := &settingsCommand{homeDir: homeDir, lookupEnv: lookupEnv}

	if err := cmd.setReleaseChannelSetting(updatecmd.ReleaseChannelRC); err != nil {
		t.Fatalf("setReleaseChannelSetting(rc) error = %v", err)
	}
	if got := judgedReleaseChannel(lookupEnv, homeDir); got != updatecmd.ReleaseChannelRC {
		t.Fatalf("resolved release channel = %q, want %q", got, updatecmd.ReleaseChannelRC)
	}

	update := &updatecmd.Command{
		Getenv:               lookupEnv,
		ReleaseChannelSource: updatecmd.NewReleaseChannelSource(lookupEnv, homeDir),
	}
	if got := update.ReleaseChannel(); got != updatecmd.ReleaseChannelRC {
		t.Fatalf("Command.ReleaseChannel() = %q, want %q", got, updatecmd.ReleaseChannelRC)
	}

	if err := cmd.setReleaseChannelSetting(updatecmd.ReleaseChannelStable); err != nil {
		t.Fatalf("setReleaseChannelSetting(stable) error = %v", err)
	}
	if got := update.ReleaseChannel(); got != updatecmd.ReleaseChannelStable {
		t.Fatalf("Command.ReleaseChannel() after opt-out = %q, want %q", got, updatecmd.ReleaseChannelStable)
	}
}

// TestReleaseChannelDefaultsToOffOnAnInstallThatNeverConfiguredIt is
// acceptance 3. The install that has never opened Settings is the one that
// must not be surprised, so the assertion is deliberately about an untouched
// home directory rather than about a config file written with a stable value.
func TestReleaseChannelDefaultsToOffOnAnInstallThatNeverConfiguredIt(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	lookupEnv := func(string) string { return "" }
	homeDir := func() (string, error) { return home, nil }

	if _, stored := updatecmd.StoredReleaseChannel(lookupEnv, homeDir); stored {
		t.Fatal("updatecmd.StoredReleaseChannel() reported a stored setting on an untouched home")
	}
	if got := judgedReleaseChannel(lookupEnv, homeDir); got != updatecmd.ReleaseChannelStable {
		t.Fatalf("resolved release channel = %q, want %q", got, updatecmd.ReleaseChannelStable)
	}

	// Reading the row must not be a write. If merely rendering Settings
	// created the key, an install driven by PROJMUX_RELEASE_CHANNEL would
	// lose its opt-in the first time somebody looked at the screen.
	cmd := &settingsCommand{homeDir: homeDir, lookupEnv: lookupEnv}
	entry := cmd.releaseChannelEntry(cmd.locale())
	if entry.Value != settingsUpdateReleaseChannel {
		t.Fatalf("release channel entry value = %q, want %q", entry.Value, settingsUpdateReleaseChannel)
	}
	configPath := filepath.Join(home, ".config", "projmux", "config.toml")
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config stat error = %v, want the file still missing after rendering the row", err)
	}
}

// TestReleaseChannelSettingOutranksTheEnvironmentOnlyOnceStored fixes the
// resolution order this Phase introduces. The stored setting wins, and the
// environment keeps answering until a setting exists — which is what stops a
// user who never touched the toggle from silently losing an env opt-in.
func TestReleaseChannelSettingOutranksTheEnvironmentOnlyOnceStored(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	lookupEnv := func(name string) string {
		if name == updatecmd.ReleaseChannelEnv {
			return updatecmd.ReleaseChannelRC
		}
		return ""
	}
	homeDir := func() (string, error) { return home, nil }
	if got := judgedReleaseChannel(lookupEnv, homeDir); got != updatecmd.ReleaseChannelRC {
		t.Fatalf("resolved release channel = %q, want the %s fallback", got, updatecmd.ReleaseChannelEnv)
	}

	cmd := &settingsCommand{homeDir: homeDir, lookupEnv: lookupEnv}
	if err := cmd.setReleaseChannelSetting(updatecmd.ReleaseChannelStable); err != nil {
		t.Fatalf("setReleaseChannelSetting(stable) error = %v", err)
	}
	if got := judgedReleaseChannel(lookupEnv, homeDir); got != updatecmd.ReleaseChannelStable {
		t.Fatalf("resolved release channel = %q, want the stored opt-out to beat %s", got, updatecmd.ReleaseChannelEnv)
	}

	channel, stored, err := cmd.currentReleaseChannelSetting()
	if err != nil {
		t.Fatalf("currentReleaseChannelSetting() error = %v", err)
	}
	if channel != updatecmd.ReleaseChannelStable || !stored {
		t.Fatalf("currentReleaseChannelSetting() = %q, %t; want %q, true", channel, stored, updatecmd.ReleaseChannelStable)
	}
}

// TestStoredReleaseChannelIsFailClosedForUnknownValues keeps the stored side
// as strict as the axis itself: a value this binary does not recognise is
// stable, and it still counts as stored so it does not fall back to an
// environment the user has already overridden from Settings.
func TestStoredReleaseChannelIsFailClosedForUnknownValues(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"), "[update]\nrelease_channel = \"beta\"\n")
	lookupEnv := func(name string) string {
		if name == updatecmd.ReleaseChannelEnv {
			return updatecmd.ReleaseChannelRC
		}
		return ""
	}
	homeDir := func() (string, error) { return home, nil }

	raw, stored := updatecmd.StoredReleaseChannel(lookupEnv, homeDir)
	if raw != "beta" || !stored {
		t.Fatalf("updatecmd.StoredReleaseChannel() = %q, %t; want %q, true", raw, stored, "beta")
	}
	if got := judgedReleaseChannel(lookupEnv, homeDir); got != updatecmd.ReleaseChannelStable {
		t.Fatalf("resolved release channel = %q, want the unknown value judged as %q", got, updatecmd.ReleaseChannelStable)
	}

	cmd := &settingsCommand{homeDir: homeDir, lookupEnv: lookupEnv}
	if !hasEntryLabelContainingAll([]intpickercompat.Entry{cmd.releaseChannelEntry(cmd.locale())}, "Release channel", "stable") {
		t.Fatal("release channel row did not report an unrecognised stored value as stable")
	}
}

// TestReleaseChannelRowStaysInAboutAndNotInLabs is the positive companion to
// the Labs negative guard: the row this Phase adds is owned by About, so the
// retired container is not resurrected by the back door of a new toggle.
func TestReleaseChannelRowStaysInAboutAndNotInLabs(t *testing.T) {
	t.Parallel()

	meta, ok := settingsEntryMetaForValue(settingsUpdateReleaseChannel)
	if !ok {
		t.Fatalf("%s has no owner contract", settingsUpdateReleaseChannel)
	}
	if got, want := settingsEntryOwnerName(meta.Owner), "about"; got != want {
		t.Fatalf("release channel owner = %q, want %q", got, want)
	}
	node, ok := settingsNavByValue(settingsUpdateReleaseChannel)
	if !ok {
		t.Fatalf("%s has no navigation node", settingsUpdateReleaseChannel)
	}
	if got, want := node.Parent, settingsNavAbout+".updates"; got != want {
		t.Fatalf("release channel parent = %q, want %q", got, want)
	}
	if got, want := node.Kind, settingsNavToggle; got != want {
		t.Fatalf("release channel nav kind = %q, want %q", got, want)
	}
}

// TestReleaseChannelToggleDoesNotReportItselfAsAnUpdate keeps the toggle's
// feedback distinguishable from the apply action it sits next to. The row
// shares the `update:` action prefix with check and apply, so the generic
// prefix label would announce "Update complete" one row below "Update now" —
// wording a user is entitled to read as "the binary was just replaced".
func TestReleaseChannelToggleDoesNotReportItselfAsAnUpdate(t *testing.T) {
	t.Parallel()

	label, mutation := settingsMutationLabel(settingsUpdateReleaseChannel)
	if !mutation {
		t.Fatalf("settingsMutationLabel(%q) reported no mutation", settingsUpdateReleaseChannel)
	}
	if got, want := label, "Release channel"; got != want {
		t.Fatalf("release channel feedback label = %q, want %q", got, want)
	}
	for _, sibling := range []string{settingsUpdateCheck, settingsUpdateApply} {
		if got, _ := settingsMutationLabel(sibling); got != "Update" {
			t.Fatalf("settingsMutationLabel(%q) = %q, want the sibling actions to keep %q", sibling, got, "Update")
		}
	}

	home := t.TempDir()
	cmd := &settingsCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}
	if err := cmd.executeWithFeedback(settingsUpdateReleaseChannel, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("executeWithFeedback() error = %v", err)
	}
	if cmd.feedback == nil {
		t.Fatal("toggling the release channel produced no Settings feedback")
	}
	if got, want := cmd.feedback.Summary, "Release channel complete"; got != want {
		t.Fatalf("feedback summary = %q, want %q", got, want)
	}
	channel, stored, err := cmd.currentReleaseChannelSetting()
	if err != nil {
		t.Fatalf("currentReleaseChannelSetting() error = %v", err)
	}
	if channel != updatecmd.ReleaseChannelRC || !stored {
		t.Fatalf("currentReleaseChannelSetting() = %q, %t; want %q, true", channel, stored, updatecmd.ReleaseChannelRC)
	}
}
