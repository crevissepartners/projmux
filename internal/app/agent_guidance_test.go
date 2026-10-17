package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/agentguidance"
)

func TestAgentGuidanceCLIShowAndPolicyPreserveFiles(t *testing.T) {
	home := t.TempDir()
	cmd := &agentCommand{lookupEnv: func(key string) string {
		if key == "XDG_CONFIG_HOME" {
			return filepath.Join(home, "config")
		}
		if key == "XDG_STATE_HOME" {
			return filepath.Join(home, "state")
		}
		return ""
	}}
	paths, err := configPaths(nil, cmd.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	store := agentguidance.NewDefaultStore(paths)
	user := []byte("# User decisions\r\nKeep user content.\n")
	if err := store.Save(user); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(paths.ConfigDir, agentguidance.FragmentsDirName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	fragment := filepath.Join(dir, "20-example.md")
	if err := os.WriteFile(fragment, []byte("FRAGMENT"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, string) {
		t.Helper()
		var out, diagnostic bytes.Buffer
		if err := cmd.Run(append([]string{"guidance"}, args...), &out, &diagnostic); err != nil {
			t.Fatal(err)
		}
		return out.String(), diagnostic.String()
	}
	out, diagnostic := run("show", "-o", "json")
	var view agentguidance.Inspection
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatal(err)
	}
	want := string(agentguidance.Default()) + "\n\nFRAGMENT\n\n" + string(user)
	if view.Text != want || len(view.Parts) != 3 || view.Parts[0].Source != "default" || view.Parts[1].Name != "20-example.md" || view.Parts[2].Source != "user" || !strings.Contains(diagnostic, "Retained user text") {
		t.Fatalf("show: %+v stderr=%q", view, diagnostic)
	}
	_, diagnostic = run("show")
	if diagnostic != "" {
		t.Fatal("repeated migration notice")
	}
	run("disable")
	run("disable", "20-example.md")
	settings, _, err := store.LoadSettings()
	if err != nil || !settings.Disabled || !slices.Contains(settings.DisabledFragments, "20-example.md") {
		t.Fatal("fragment disable changed whole off", err)
	}
	run("enable", "20-example.md")
	settings, _, _ = store.LoadSettings()
	if !settings.Disabled || len(settings.DisabledFragments) != 0 {
		t.Fatal("fragment enable changed whole off")
	}
	run("disable", "20-example.md")
	run("enable")
	out, _ = run("show", "-o", "json")
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatal(err)
	}
	if view.Disabled || view.Parts[1].Enabled || view.Text != string(agentguidance.Default())+"\n\n"+string(user) {
		t.Fatal("whole enable lost fragment mask or user content")
	}
	for path, want := range map[string]string{store.Path(): string(user), fragment: "FRAGMENT"} {
		content, err := os.ReadFile(path)
		if err != nil || string(content) != want {
			t.Fatal("policy control changed input file", path, err)
		}
	}
	run("enable", "20-example.md")
	out, _ = run("show", "--process", "-o", "json")
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatal(err)
	}
	if !(strings.Index(view.Text, "# Process host") < strings.Index(view.Text, "FRAGMENT")) {
		t.Fatal("process order")
	}
}

func TestAgentGuidanceCLIInvalidRequestsHaveNoEffects(t *testing.T) {
	for _, args := range [][]string{{}, {"bad"}, {"show", "extra"}, {"show", "-o", "yaml"}, {"enable", "a.md", "b.md"}, {"disable", "../outside.md"}, {"enable", ""}, {"disable", "--project", "x"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := t.TempDir()
			cmd := &agentCommand{lookupEnv: func(key string) string {
				if key == "XDG_CONFIG_HOME" {
					return filepath.Join(home, "config")
				}
				if key == "XDG_STATE_HOME" {
					return filepath.Join(home, "state")
				}
				return ""
			}}
			var out, diagnostic bytes.Buffer
			err := cmd.Run(append([]string{"guidance"}, args...), &out, &diagnostic)
			if err == nil || !IsUsageError(err) || exitCodeOf(err) != 2 || out.Len() != 0 {
				t.Fatalf("invalid request err=%v output=%q", err, out.String())
			}
			entries, _ := os.ReadDir(home)
			if len(entries) != 0 {
				t.Fatal("invalid request wrote files")
			}
		})
	}
}

func TestMigrationNoticeReachesLaunchWithoutChangingDigest(t *testing.T) {
	fixture := newAgentGuidanceCreate(t)
	writeAgentGuidance(t, fixture.paths, []byte("USER"))
	plan := fixture.planner.PlanCodexAgentGuidance()
	if !strings.Contains(plan.notice("test"), "User guidance now appends") {
		t.Fatal("launch lost migration notice")
	}
	if plan.digest != agentguidance.Digest(plan.text) {
		t.Fatal("notice entered digest")
	}
	if fixture.planner.PlanCodexAgentGuidance().notice("test") != "" {
		t.Fatal("launch repeated migration notice")
	}
}

func TestAgentGuidanceCLIControlsBeforeShowPreserveLegacyBytes(t *testing.T) {
	home := t.TempDir()
	cmd := &agentCommand{lookupEnv: func(key string) string {
		switch key {
		case "XDG_CONFIG_HOME":
			return filepath.Join(home, "config")
		case "XDG_STATE_HOME":
			return filepath.Join(home, "state")
		}
		return ""
	}}
	paths, err := configPaths(nil, cmd.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	store := agentguidance.NewDefaultStore(paths)
	original := append(agentguidance.Default(), []byte("\nUSER\n")...)
	if err := store.Save(original); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"disable"}, {"disable", "app.md"}, {"enable"}, {"enable", "app.md"}} {
		var out, diagnostic bytes.Buffer
		if err := cmd.Run(append([]string{"guidance"}, args...), &out, &diagnostic); err != nil {
			t.Fatal(err)
		}
		if diagnostic.Len() != 0 {
			t.Fatal("policy control emitted a migration notice")
		}
		current, err := os.ReadFile(store.Path())
		if err != nil || !bytes.Equal(current, original) {
			t.Fatal("CLI control changed legacy user bytes", err)
		}
		if _, err := os.Stat(store.Path() + ".bak"); !os.IsNotExist(err) {
			t.Fatal("CLI control migrated before show")
		}
	}
	var out, diagnostic bytes.Buffer
	if err := cmd.Run([]string{"guidance", "show", "-o", "json"}, &out, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diagnostic.String(), "removed historical default prefix=true") {
		t.Fatal("show did not migrate legacy content")
	}
	current, _ := os.ReadFile(store.Path())
	if string(current) != "\nUSER\n" {
		t.Fatal("show damaged user bytes")
	}
}
