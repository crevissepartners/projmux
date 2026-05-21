package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// TestProjectTrustEntryAbsentWhenConfigMissing confirms the Trust row on
// the Project tab reports the absent state (no config.toml on disk) and
// still routes Enter into the Trust subsection so the user can read the
// hint instead of losing it on a no-op row.
func TestProjectTrustEntryAbsentWhenConfigMissing(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	stateHome := t.TempDir()
	repo := t.TempDir()

	cmd := trustTestSettings(t, home, configHome, stateHome, repo, nil)
	entry := cmd.projectTrustEntry(cmd.resolveSettingsProjectContext())

	if entry.Value != settingsSectionProjectTrust {
		t.Fatalf("entry.Value = %q, want %q", entry.Value, settingsSectionProjectTrust)
	}
	if !strings.Contains(entry.Label, "Trust") || !strings.Contains(entry.Label, "no .projmux/config.toml") {
		t.Fatalf("entry.Label = %q, want absent summary", entry.Label)
	}
}

// TestProjectTrustEntryUntrustedAfterCreatingConfig verifies the badge
// flips to "untrusted" tone once a config.toml is on disk but no trust
// store entry exists yet.
func TestProjectTrustEntryUntrustedAfterCreatingConfig(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	stateHome := t.TempDir()
	repo := t.TempDir()
	mkdirAll(t, filepath.Join(repo, ".projmux"))
	writeFile(t, filepath.Join(repo, ".projmux", "config.toml"), `
[startup]
run = "echo ready"
`)

	cmd := trustTestSettings(t, home, configHome, stateHome, repo, nil)
	entry := cmd.projectTrustEntry(cmd.resolveSettingsProjectContext())

	if !strings.Contains(entry.Label, "untrusted") || !strings.Contains(entry.Label, "registration required") {
		t.Fatalf("entry.Label = %q, want untrusted summary", entry.Label)
	}
	if !strings.Contains(entry.Label, settingsColorTrustUntrusted) {
		t.Fatalf("entry.Label = %q, want trust-untrusted tone (%q)", entry.Label, settingsColorTrustUntrusted)
	}
	if strings.Contains(entry.Label, settingsColorRemove) {
		t.Fatalf("entry.Label = %q, untrusted state must not reuse destructive color %q", entry.Label, settingsColorRemove)
	}
}

// TestProjectTrustEntryTrustedAfterTrust verifies the badge picks up
// the bold/active tone and "hash matches" summary once the trust store
// records the config hash.
func TestProjectTrustEntryTrustedAfterTrust(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	stateHome := t.TempDir()
	repo := t.TempDir()
	mkdirAll(t, filepath.Join(repo, ".projmux"))
	writeFile(t, filepath.Join(repo, ".projmux", "config.toml"), `
[startup]
run = "echo ready"
`)
	cmd := trustTestSettings(t, home, configHome, stateHome, repo, nil)

	trustPath, err := cmd.projectConfigTrustStorePath()
	if err != nil {
		t.Fatalf("projectConfigTrustStorePath: %v", err)
	}
	if _, err := hooks.TrustProjectConfig(repo, trustPath); err != nil {
		t.Fatalf("TrustProjectConfig: %v", err)
	}

	entry := cmd.projectTrustEntry(cmd.resolveSettingsProjectContext())
	if !strings.Contains(entry.Label, "trusted") || !strings.Contains(entry.Label, "hash matches") {
		t.Fatalf("entry.Label = %q, want trusted summary", entry.Label)
	}
	if !strings.Contains(entry.Label, settingsColorTrustTrusted) {
		t.Fatalf("entry.Label = %q, want trust-trusted tone (%q)", entry.Label, settingsColorTrustTrusted)
	}
	if strings.Contains(entry.Label, settingsColorAdd) {
		t.Fatalf("entry.Label = %q, trusted state must not reuse action color %q", entry.Label, settingsColorAdd)
	}
}

// TestProjectTrustEntryStaleAfterEdit verifies the badge moves to a
// warning amber tone once the on-disk config hash diverges from the
// stored hash.
func TestProjectTrustEntryStaleAfterEdit(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	stateHome := t.TempDir()
	repo := t.TempDir()
	mkdirAll(t, filepath.Join(repo, ".projmux"))
	writeFile(t, filepath.Join(repo, ".projmux", "config.toml"), `
[startup]
run = "echo ready"
`)
	cmd := trustTestSettings(t, home, configHome, stateHome, repo, nil)
	trustPath, err := cmd.projectConfigTrustStorePath()
	if err != nil {
		t.Fatalf("projectConfigTrustStorePath: %v", err)
	}
	if _, err := hooks.TrustProjectConfig(repo, trustPath); err != nil {
		t.Fatalf("TrustProjectConfig: %v", err)
	}

	// Mutate the on-disk config so its hash diverges from the stored one.
	writeFile(t, filepath.Join(repo, ".projmux", "config.toml"), `
[startup]
run = "echo updated"
`)

	entry := cmd.projectTrustEntry(cmd.resolveSettingsProjectContext())
	if !strings.Contains(entry.Label, "stale") || !strings.Contains(entry.Label, "file changed") {
		t.Fatalf("entry.Label = %q, want stale summary", entry.Label)
	}
	if !strings.Contains(entry.Label, settingsColorTrustStale) {
		t.Fatalf("entry.Label = %q, want stale tone (%q)", entry.Label, settingsColorTrustStale)
	}
	if strings.Contains(entry.Label, settingsColorRemove) || strings.Contains(entry.Label, settingsColorAdd) {
		t.Fatalf("entry.Label = %q, stale trust state must not reuse danger/action colors", entry.Label)
	}
	if settingsColorTrustStale == settingsColorDim {
		t.Fatalf("stale trust color must not alias muted text color")
	}
}

// TestProjectTrustEntriesShowActionsForEachState verifies the picker
// rows on the Trust subsection match the current state: untrusted → Trust
// row, stale → Refresh + Untrust rows, trusted → Untrust row, absent →
// no actionable row.
func TestProjectTrustEntriesShowActionsForEachState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		setup      func(t *testing.T, repo string, cmd *settingsCommand)
		wantValues []string
		wantAbsent []string
	}{
		{
			name:       "absent",
			setup:      func(t *testing.T, repo string, cmd *settingsCommand) {},
			wantValues: nil,
			wantAbsent: []string{settingsTrustApply, settingsTrustRefresh, settingsTrustUntrust},
		},
		{
			name: "untrusted",
			setup: func(t *testing.T, repo string, cmd *settingsCommand) {
				mkdirAll(t, filepath.Join(repo, ".projmux"))
				writeFile(t, filepath.Join(repo, ".projmux", "config.toml"), `
[startup]
run = "echo ready"
`)
			},
			wantValues: []string{settingsTrustApply},
			wantAbsent: []string{settingsTrustRefresh, settingsTrustUntrust},
		},
		{
			name: "trusted",
			setup: func(t *testing.T, repo string, cmd *settingsCommand) {
				mkdirAll(t, filepath.Join(repo, ".projmux"))
				writeFile(t, filepath.Join(repo, ".projmux", "config.toml"), `
[startup]
run = "echo ready"
`)
				trustPath, err := cmd.projectConfigTrustStorePath()
				if err != nil {
					t.Fatalf("projectConfigTrustStorePath: %v", err)
				}
				if _, err := hooks.TrustProjectConfig(repo, trustPath); err != nil {
					t.Fatalf("TrustProjectConfig: %v", err)
				}
			},
			wantValues: []string{settingsTrustUntrust},
			wantAbsent: []string{settingsTrustApply, settingsTrustRefresh},
		},
		{
			name: "stale",
			setup: func(t *testing.T, repo string, cmd *settingsCommand) {
				mkdirAll(t, filepath.Join(repo, ".projmux"))
				writeFile(t, filepath.Join(repo, ".projmux", "config.toml"), `
[startup]
run = "echo ready"
`)
				trustPath, err := cmd.projectConfigTrustStorePath()
				if err != nil {
					t.Fatalf("projectConfigTrustStorePath: %v", err)
				}
				if _, err := hooks.TrustProjectConfig(repo, trustPath); err != nil {
					t.Fatalf("TrustProjectConfig: %v", err)
				}
				writeFile(t, filepath.Join(repo, ".projmux", "config.toml"), `
[startup]
run = "echo updated"
`)
			},
			wantValues: []string{settingsTrustRefresh, settingsTrustUntrust},
			wantAbsent: []string{settingsTrustApply},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			configHome := t.TempDir()
			stateHome := t.TempDir()
			repo := t.TempDir()
			cmd := trustTestSettings(t, home, configHome, stateHome, repo, nil)
			tc.setup(t, repo, cmd)

			entries := cmd.projectTrustEntries(cmd.resolveSettingsProjectContext())
			if !hasEntryValue(entries, settingsBackValue) {
				t.Fatalf("entries missing Back row: %#v", entries)
			}
			for _, value := range tc.wantValues {
				if !hasEntryValue(entries, value) {
					t.Fatalf("entries missing expected action %q: %#v", value, entries)
				}
			}
			for _, value := range tc.wantAbsent {
				if hasEntryValue(entries, value) {
					t.Fatalf("entries should not include %q for state %s: %#v", value, tc.name, entries)
				}
			}
		})
	}
}

// TestGlobalTabExcludesTrust enforces the spec exemption: the Trust
// surface only exists on the Project tab — the Global tab must not
// surface it (trust policy is project-scoped).
func TestGlobalTabExcludesTrust(t *testing.T) {
	t.Parallel()

	cmd := &settingsCommand{lookupEnv: func(string) string { return "" }}
	entries := cmd.rootEntriesForAxis(settingsAxisGlobal)
	for _, entry := range entries {
		if strings.Contains(stripAnsi(entry.Label), "Trust") {
			t.Fatalf("global tab leaked Trust row: %q", entry.Label)
		}
		if entry.Value == settingsSectionProjectTrust {
			t.Fatalf("global tab leaked Trust section value")
		}
	}
}

// TestRunSectionDispatchesProjectTrust verifies the new section value is
// wired through runSection — guards against the dispatch table drifting
// from the catalog.
func TestRunSectionDispatchesProjectTrust(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	stateHome := t.TempDir()
	repo := t.TempDir()

	var calls int
	runner := switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		if calls == 1 {
			if got, want := options.UI, "settings-project-trust"; got != want {
				t.Fatalf("first picker UI = %q, want %q", got, want)
			}
		}
		return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
	})

	cmd := trustTestSettings(t, home, configHome, stateHome, repo, runner)
	var stdout, stderr bytes.Buffer
	if err := cmd.runSection(settingsSectionProjectTrust, &stdout, &stderr); err != nil {
		t.Fatalf("runSection: %v", err)
	}
	if calls < 1 {
		t.Fatalf("runSection did not invoke picker (calls = %d)", calls)
	}
}

// TestProjectTrustApplyWritesTrustStore drives the trust:apply action
// through the subsection runner and confirms a fresh trust-store entry
// is written. It exercises the same code path the user invokes when they
// press Enter on the "Trust this config" row.
func TestProjectTrustApplyWritesTrustStore(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	stateHome := t.TempDir()
	repo := t.TempDir()
	mkdirAll(t, filepath.Join(repo, ".projmux"))
	writeFile(t, filepath.Join(repo, ".projmux", "config.toml"), `
[startup]
run = "echo ready"
`)

	var calls int
	runner := switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsTrustApply}, nil
		case 2:
			// After applying, the page should rerender and surface the
			// trusted state — Back out so the loop terminates.
			if !hasEntryLabelContaining(options.Entries, "trusted") {
				t.Fatalf("refreshed entries missing trusted state: %#v", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		default:
			t.Fatalf("unexpected runner call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})

	cmd := trustTestSettings(t, home, configHome, stateHome, repo, runner)
	var stdout, stderr bytes.Buffer
	if err := cmd.runProjectTrustSection(&stdout, &stderr); err != nil {
		t.Fatalf("runProjectTrustSection: %v", err)
	}

	trustStorePath := filepath.Join(stateHome, "projmux", "trusted-projects.json")
	contents, err := os.ReadFile(trustStorePath)
	if err != nil {
		t.Fatalf("trust store not written: %v", err)
	}
	if !strings.Contains(string(contents), ".projmux/config.toml") {
		t.Fatalf("trust store = %s, want config.toml entry", contents)
	}
}

// TestProjectTrustUntrustRequiresConfirmation confirms the destructive
// Untrust row goes through a Yes/No confirmation page — a single Enter
// on the Untrust row plus a Cancel must NOT mutate the trust store.
func TestProjectTrustUntrustRequiresConfirmation(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	stateHome := t.TempDir()
	repo := t.TempDir()
	mkdirAll(t, filepath.Join(repo, ".projmux"))
	writeFile(t, filepath.Join(repo, ".projmux", "config.toml"), `
[startup]
run = "echo ready"
`)

	// Pre-trust so the page surfaces an Untrust row.
	tmpCmd := trustTestSettings(t, home, configHome, stateHome, repo, nil)
	trustPath, err := tmpCmd.projectConfigTrustStorePath()
	if err != nil {
		t.Fatalf("projectConfigTrustStorePath: %v", err)
	}
	if _, err := hooks.TrustProjectConfig(repo, trustPath); err != nil {
		t.Fatalf("TrustProjectConfig: %v", err)
	}
	storeBefore, err := os.ReadFile(trustPath)
	if err != nil {
		t.Fatalf("read trust store before: %v", err)
	}

	var calls int
	runner := switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsTrustUntrust}, nil
		case 2:
			// Confirmation page — pick Cancel.
			if got, want := options.UI, "settings-project-trust-confirm"; got != want {
				t.Fatalf("confirmation UI = %q, want %q", got, want)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsTrustConfirmNo}, nil
		case 3:
			// Back to the page — exit.
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		default:
			t.Fatalf("unexpected runner call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})

	cmd := trustTestSettings(t, home, configHome, stateHome, repo, runner)
	var stdout, stderr bytes.Buffer
	if err := cmd.runProjectTrustSection(&stdout, &stderr); err != nil {
		t.Fatalf("runProjectTrustSection: %v", err)
	}

	storeAfter, err := os.ReadFile(trustPath)
	if err != nil {
		t.Fatalf("read trust store after: %v", err)
	}
	if string(storeBefore) != string(storeAfter) {
		t.Fatalf("trust store mutated after Cancel\nbefore=%s\nafter=%s", storeBefore, storeAfter)
	}
}

// TestProjectTrustUntrustConfirmedRemovesEntry drives the Untrust flow
// with a Yes confirmation and verifies the trust store entry is
// removed.
func TestProjectTrustUntrustConfirmedRemovesEntry(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	stateHome := t.TempDir()
	repo := t.TempDir()
	mkdirAll(t, filepath.Join(repo, ".projmux"))
	writeFile(t, filepath.Join(repo, ".projmux", "config.toml"), `
[startup]
run = "echo ready"
`)

	tmpCmd := trustTestSettings(t, home, configHome, stateHome, repo, nil)
	trustPath, err := tmpCmd.projectConfigTrustStorePath()
	if err != nil {
		t.Fatalf("projectConfigTrustStorePath: %v", err)
	}
	if _, err := hooks.TrustProjectConfig(repo, trustPath); err != nil {
		t.Fatalf("TrustProjectConfig: %v", err)
	}

	var calls int
	runner := switchRunnerFunc(func(options intpickercompat.Options) (intpickercompat.Result, error) {
		calls++
		switch calls {
		case 1:
			return intpickercompat.Result{Key: "enter", Value: settingsTrustUntrust}, nil
		case 2:
			return intpickercompat.Result{Key: "enter", Value: settingsTrustConfirmYes}, nil
		case 3:
			if !hasEntryLabelContaining(options.Entries, "untrusted") {
				t.Fatalf("refreshed entries missing untrusted state: %#v", options.Entries)
			}
			return intpickercompat.Result{Key: "enter", Value: settingsBackValue}, nil
		default:
			t.Fatalf("unexpected runner call %d", calls)
			return intpickercompat.Result{}, nil
		}
	})

	cmd := trustTestSettings(t, home, configHome, stateHome, repo, runner)
	var stdout, stderr bytes.Buffer
	if err := cmd.runProjectTrustSection(&stdout, &stderr); err != nil {
		t.Fatalf("runProjectTrustSection: %v", err)
	}

	report, err := hooks.InspectProjectConfigTrust(repo, trustPath)
	if err != nil {
		t.Fatalf("InspectProjectConfigTrust: %v", err)
	}
	if report.State != hooks.ProjectConfigTrustUntrusted {
		t.Fatalf("State = %q, want %q after Untrust confirmation", report.State, hooks.ProjectConfigTrustUntrusted)
	}
}

// trustTestSettings builds a settingsCommand wired so it can drive the
// Trust subsection using a scripted runner. Passing a nil runner returns
// a command that is only valid for read-only entry-building helpers.
func trustTestSettings(t *testing.T, home, configHome, stateHome, repo string, runner switchRunnerFunc) *settingsCommand {
	t.Helper()
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "PROJMUX_CWD":
				return repo
			case "XDG_CONFIG_HOME":
				return configHome
			case "XDG_STATE_HOME":
				return stateHome
			default:
				return ""
			}
		},
	}
	if runner != nil {
		cmd.runner = runner
		cmd.nativePicker = nativePickerFromCompatRunner(runner)
	}
	return cmd
}
