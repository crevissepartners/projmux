package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

// TestAIModeSetMatchesTheCentralNewWindowModeSet keeps normalizeAIMode and
// config.AINewWindowModes from drifting: getMode trusts
// config.ValidAINewWindowMode for the TUI file.
func TestAIModeSetMatchesTheCentralNewWindowModeSet(t *testing.T) {
	appModes := []string{aiModeClaude, aiModeCodex, aiModeAntigravity, aiModeSelective, aiModeResume, aiModeShell}
	if len(appModes) != len(config.AINewWindowModes) {
		t.Fatalf("app modes %v, config modes %v", appModes, config.AINewWindowModes)
	}
	for _, mode := range appModes {
		if got, ok := config.ValidAINewWindowMode(mode); !ok || got != mode {
			t.Errorf("config.ValidAINewWindowMode(%q) = %q, %v", mode, got, ok)
		}
	}
	for _, mode := range config.AINewWindowModes {
		if got := normalizeAIMode(mode); got != mode {
			t.Errorf("normalizeAIMode(%q) = %q", mode, got)
		}
	}
}

// newAIModeTestCommand is an aiCommand whose config home is a temp
// XDG_CONFIG_HOME, returned with its projmux config directory.
func newAIModeTestCommand(t *testing.T) (*aiCommand, string) {
	t.Helper()
	configHome := t.TempDir()
	home := t.TempDir()
	c := &aiCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "XDG_CONFIG_HOME" {
				return configHome
			}
			return ""
		},
	}
	return c, filepath.Join(configHome, config.AppName)
}

func writeAIModeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAIGetModeFallsBackFromTUIToCentralToSelective(t *testing.T) {
	const unset = "\x00unset"
	for _, tc := range []struct {
		name, tui, central, want string
	}{
		{name: "neither set", tui: unset, central: unset, want: aiModeSelective},
		{name: "central only", tui: unset, central: "codex\n", want: aiModeCodex},
		{name: "TUI wins over central", tui: "claude\n", central: "codex\n", want: aiModeClaude},
		{name: "invalid TUI falls through", tui: "bogus\n", central: "shell\n", want: aiModeShell},
		{name: "empty TUI falls through", tui: "", central: "resume\n", want: aiModeResume},
		{name: "invalid TUI and no central", tui: "bogus\n", central: unset, want: aiModeSelective},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, dir := newAIModeTestCommand(t)
			if tc.tui != unset {
				writeAIModeTestFile(t, dir, config.TmuxAISplitModeFileName, tc.tui)
			}
			if tc.central != unset {
				writeAIModeTestFile(t, dir, config.AINewWindowModeFileName, tc.central)
			}
			if got := c.getMode(); got != tc.want {
				t.Fatalf("getMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCentralAINewWindowModeLoadAndSave(t *testing.T) {
	c, _ := newAIModeTestCommand(t)
	if mode, saved, err := loadCentralAINewWindowMode(c.homeDir, c.lookupEnv); err != nil || saved || mode != "" {
		t.Fatalf("load before save = %q, %v, %v; want unset", mode, saved, err)
	}
	if err := saveCentralAINewWindowMode(c.homeDir, c.lookupEnv, "bogus"); err == nil {
		t.Fatal("saveCentralAINewWindowMode(bogus) error = nil, want refusal")
	}
	if mode, saved, err := loadCentralAINewWindowMode(c.homeDir, c.lookupEnv); err != nil || saved || mode != "" {
		t.Fatalf("load after refused save = %q, %v, %v; want unset", mode, saved, err)
	}
	if err := saveCentralAINewWindowMode(c.homeDir, c.lookupEnv, aiModeAntigravity); err != nil {
		t.Fatalf("saveCentralAINewWindowMode(antigravity) error = %v", err)
	}
	if mode, saved, err := loadCentralAINewWindowMode(c.homeDir, c.lookupEnv); err != nil || !saved || mode != aiModeAntigravity {
		t.Fatalf("load after save = %q, %v, %v; want antigravity", mode, saved, err)
	}
	if got := c.getMode(); got != aiModeAntigravity {
		t.Fatalf("getMode() = %q, want the central antigravity", got)
	}
}

func TestAIConfigHomeMatchesTheTUIModeFile(t *testing.T) {
	failing := func() (string, error) { return "", os.ErrNotExist }
	for _, tc := range []struct {
		name      string
		homeDir   func() (string, error)
		lookupEnv func(string) string
		want      string
	}{
		{name: "no resolvers", want: ".config"},
		{name: "home error", homeDir: failing, want: ".config"},
		{name: "home", homeDir: func() (string, error) { return "/h", nil }, want: "/h/.config"},
		{name: "XDG wins", homeDir: func() (string, error) { return "/h", nil }, lookupEnv: func(string) string { return " /x " }, want: "/x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := aiConfigHome(tc.homeDir, tc.lookupEnv); got != tc.want {
				t.Fatalf("aiConfigHome() = %q, want %q", got, tc.want)
			}
			c := &aiCommand{homeDir: tc.homeDir, lookupEnv: tc.lookupEnv}
			central := config.DefaultPaths(tc.want, "").AINewWindowModeFile()
			if filepath.Dir(c.configFile()) != filepath.Dir(central) {
				t.Fatalf("configFile() %q and central %q differ in directory", c.configFile(), central)
			}
		})
	}
}

func TestCentralAINewWindowModeIsNotAFrontRead(t *testing.T) {
	c, dir := newAIModeTestCommand(t)
	writeAIModeTestFile(t, dir, config.AINewWindowModeFileName, "codex\n")

	var reads []config.FrontRead
	restore := config.ObserveFrontReads(func(read config.FrontRead) { reads = append(reads, read) })
	defer restore()

	if _, _, err := loadCentralAINewWindowMode(c.homeDir, c.lookupEnv); err != nil {
		t.Fatal(err)
	}
	if err := saveCentralAINewWindowMode(c.homeDir, c.lookupEnv, aiModeShell); err != nil {
		t.Fatal(err)
	}
	if len(reads) != 0 {
		t.Fatalf("central load/save front reads = %+v, want none", reads)
	}

	if got := c.getMode(); got != aiModeShell {
		t.Fatalf("getMode() = %q, want shell", got)
	}
	if len(reads) != 1 || reads[0].Item.Name != config.TmuxAISplitModeFileName {
		t.Fatalf("getMode front reads = %+v, want exactly %s", reads, config.TmuxAISplitModeFileName)
	}
}
