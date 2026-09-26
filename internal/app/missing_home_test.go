package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

// mustAIConfigFile is the AI split default file of c, which a test that set
// up a home expects to resolve.
func mustAIConfigFile(t *testing.T, c *aiCommand) string {
	t.Helper()
	path, err := c.configFile()
	if err != nil {
		t.Fatalf("configFile() error = %v", err)
	}
	return path
}

// noHomeDir and noXDG stand for a process with no HOME and no XDG homes.
func noHomeDir() (string, error) { return "", errors.New("$HOME is not defined") }
func noXDG(string) string        { return "" }

// chdirEmpty moves the test into an empty working directory and returns a
// check that it is still empty, so no site may fall back to a path relative
// to the working directory.
func chdirEmpty(t *testing.T) func() {
	t.Helper()
	cwd := t.TempDir()
	t.Chdir(cwd)
	return func() {
		t.Helper()
		entries, err := os.ReadDir(cwd)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			t.Fatalf("working directory gained %v, want it untouched", names)
		}
	}
}

// requireMissingHomeReason fails unless err is the shared missing-HOME
// reason naming HOME and xdgVar on one line.
func requireMissingHomeReason(t *testing.T, what string, err error, xdgVar string) {
	t.Helper()
	if !isMissingHome(err) {
		t.Fatalf("%s error = %v, want the missing-HOME reason", what, err)
	}
	if text := err.Error(); !strings.Contains(text, "HOME or an absolute "+xdgVar+" is required") || strings.Contains(text, "\n") {
		t.Fatalf("%s error = %q, want one line naming HOME and %s", what, text, xdgVar)
	}
}

// TestMissingHomeRefusesWritesAndKeepsReadsAtDefaults runs every site that
// needs a HOME fallback with neither HOME nor an XDG home: a read keeps its
// built-in default, a write refuses with the reason, a display shows the
// reason, and nothing lands in the working directory.
func TestMissingHomeRefusesWritesAndKeepsReadsAtDefaults(t *testing.T) {
	// A nil resolver is not in the table: several sites default it to
	// os.UserHomeDir.
	for _, homeDir := range map[string]func() (string, error){
		"home error": noHomeDir,
		"empty home": func() (string, error) { return "", nil },
	} {
		assertCwdEmpty := chdirEmpty(t)
		ai := &aiCommand{homeDir: homeDir, lookupEnv: noXDG}

		// Reads keep their built-in defaults.
		if got := ai.getMode(); got != aiModeSelective {
			t.Fatalf("getMode() = %q, want the built-in %q", got, aiModeSelective)
		}
		if mode, saved, err := loadCentralAINewWindowMode(homeDir, noXDG); mode != "" || saved || err != nil {
			t.Fatalf("loadCentralAINewWindowMode() = %q, %v, %v; want unset", mode, saved, err)
		}
		want, err := defaultAIHookCatalog(aiHookProviderClaude)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := ai.loadAIHookCatalog(aiHookProviderClaude); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("loadAIHookCatalog() = %+v, %v; want the built-in catalog", got, err)
		}
		if got := ai.ensureNotificationPNG("projmux.png", projmuxNotificationIconPNG); got != "dialog-information" {
			t.Fatalf("ensureNotificationPNG() = %q, want the stock icon", got)
		}
		var listed bytes.Buffer
		if err := (&profileCommand{homeDir: homeDir, lookupEnv: noXDG}).Run([]string{"list"}, &listed, &bytes.Buffer{}); err != nil {
			t.Fatalf("profile list error = %v, want the builtins", err)
		}
		if !strings.Contains(listed.String(), "readonly") {
			t.Fatalf("profile list = %q, want the builtin readonly", listed.String())
		}

		// Writes refuse with the reason and write nothing.
		requireMissingHomeReason(t, "config edit --set", ai.setMode(aiModeShell), config.XDGConfigHomeVar)
		requireMissingHomeReason(t, "saveCentralAINewWindowMode", saveCentralAINewWindowMode(homeDir, noXDG, aiModeShell), config.XDGConfigHomeVar)
		_, err = (&shellCommand{homeDir: homeDir, lookupEnv: noXDG}).defaultConfigPath()
		requireMissingHomeReason(t, "shell defaultConfigPath", err, config.XDGConfigHomeVar)
		profiles := &profileCommand{homeDir: homeDir, lookupEnv: noXDG, stdin: strings.NewReader("model = \"m\"\n")}
		requireMissingHomeReason(t, "profile set", profiles.Run([]string{"set", "mine"}, &bytes.Buffer{}, &bytes.Buffer{}), config.XDGConfigHomeVar)
		requireMissingHomeReason(t, "profile delete", profiles.Run([]string{"delete", "mine", "--yes"}, &bytes.Buffer{}, &bytes.Buffer{}), config.XDGConfigHomeVar)
		_, err = (&aiCommand{homeDir: homeDir, lookupEnv: noXDG}).notificationIconDir()
		requireMissingHomeReason(t, "notificationIconDir", err, config.XDGDataHomeVar)

		// Displays show the reason instead of an empty path.
		hook := &hookCommand{homeDir: homeDir, lookupEnv: noXDG, getwd: os.Getwd}
		var stdout, stderr bytes.Buffer
		if err := hook.Run([]string{"list"}, &stdout, &stderr); err != nil {
			t.Fatalf("hook list error = %v", err)
		}
		if want := "global config: (HOME or an absolute XDG_CONFIG_HOME is required)\n"; !strings.Contains(stdout.String(), want) {
			t.Fatalf("hook list stdout = %q, want %q", stdout.String(), want)
		}
		if stderr.Len() != 0 {
			t.Fatalf("hook list stderr = %q, want no bogus parse error", stderr.String())
		}

		assertCwdEmpty()
	}
}

// TestConfigPathResolversNeverReturnRelativePaths guards the resolvers that
// once fell back to a working-directory .config or an empty path. Without a
// home (none, failing, empty, or blank) every XDG value gives an absolute
// path or the missing-HOME reason; with an absolute home it is always an
// absolute path. A relative HOME keeps its existing behavior and is not
// covered here.
func TestConfigPathResolversNeverReturnRelativePaths(t *testing.T) {
	t.Parallel()

	resolvers := map[string]func(homeDir func() (string, error), lookupEnv func(string) string) (string, error){
		"aiConfigHome": aiConfigHome,
		"aiCommand.configFile": func(homeDir func() (string, error), lookupEnv func(string) string) (string, error) {
			return (&aiCommand{homeDir: homeDir, lookupEnv: lookupEnv}).configFile()
		},
		"aiCommand.aiConfigPaths": func(homeDir func() (string, error), lookupEnv func(string) string) (string, error) {
			paths, err := (&aiCommand{homeDir: homeDir, lookupEnv: lookupEnv}).aiConfigPaths()
			return paths.ConfigDir, err
		},
		"aiCommand.notificationIconDir": func(homeDir func() (string, error), lookupEnv func(string) string) (string, error) {
			return (&aiCommand{homeDir: homeDir, lookupEnv: lookupEnv}).notificationIconDir()
		},
		"shellCommand.defaultConfigPath": func(homeDir func() (string, error), lookupEnv func(string) string) (string, error) {
			return (&shellCommand{homeDir: homeDir, lookupEnv: lookupEnv}).defaultConfigPath()
		},
		"savedSettingsPaths": func(homeDir func() (string, error), lookupEnv func(string) string) (string, error) {
			paths, err := savedSettingsPaths(homeDir, lookupEnv)
			return paths.ConfigDir, err
		},
	}
	homes := map[string]func() (string, error){
		"nil":      nil,
		"error":    noHomeDir,
		"empty":    func() (string, error) { return "", nil },
		"blank":    func() (string, error) { return " ", nil },
		"absolute": func() (string, error) { return "/h", nil },
	}
	for resolverName, resolve := range resolvers {
		for homeName, homeDir := range homes {
			for _, xdg := range []string{"", " ", "rel", "./rel", "~/x", "/x"} {
				lookupEnv := func(name string) string {
					if strings.HasPrefix(name, "XDG_") {
						return xdg
					}
					return ""
				}
				got, err := resolve(homeDir, lookupEnv)
				if err != nil {
					if !isMissingHome(err) || got != "" {
						t.Errorf("%s(home %s, XDG %q) = %q, %v; want the missing-HOME reason and no path", resolverName, homeName, xdg, got, err)
					}
					if homeName == "absolute" || xdg == "/x" {
						t.Errorf("%s(home %s, XDG %q) error = %v, want a path", resolverName, homeName, xdg, err)
					}
					continue
				}
				if !filepath.IsAbs(got) {
					t.Errorf("%s(home %s, XDG %q) = %q, want an absolute path", resolverName, homeName, xdg, got)
				}
			}
		}
	}
}

// TestCreateAgentWithoutHomeAsksForTheProviderFirst is owner ruling T2-0:
// the profile store is a read, so without HOME or an XDG home a bare
// `create agent` (and one whose role label no builtin claims) selects no
// profile and is refused for the missing --provider, not for the store path.
func TestCreateAgentWithoutHomeAsksForTheProviderFirst(t *testing.T) {
	for _, args := range [][]string{
		{"agent", "--project", "alpha"},
		{"agent", "--project", "alpha", "--label", "role=reviewer"},
	} {
		assertCwdEmpty := chdirEmpty(t)
		store := newFakeResourceStore(t)
		create, launcher := newTestAgentCreateCommand(t, store, newFakeTmux())
		create.homeDir = noHomeDir
		create.lookupEnv = noXDG

		_, _, err := runRoute(t, create, args...)
		if err == nil || !IsUsageError(err) || !strings.Contains(err.Error(), "requires --provider") {
			t.Fatalf("create %v error = %v, want the --provider usage reason", args, err)
		}
		if strings.Contains(err.Error(), "HOME") {
			t.Fatalf("create %v error = %q, want no profile store path error", args, err)
		}
		if len(launcher.plans) != 0 {
			t.Fatalf("create %v planned launches %v", args, launcher.plans)
		}
		assertCwdEmpty()
	}

	// An explicit profile has to be read, so it still refuses with the reason.
	create, _ := newTestAgentCreateCommand(t, newFakeResourceStore(t), newFakeTmux())
	create.homeDir = noHomeDir
	create.lookupEnv = noXDG
	_, _, err := runRoute(t, create, "agent", "--project", "alpha", "--profile", "mine")
	requireMissingHomeReason(t, "create agent --profile mine", err, config.XDGConfigHomeVar)
}
