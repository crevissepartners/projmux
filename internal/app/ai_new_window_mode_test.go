package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

// TestAIModeSetMatchesTheCentralNewWindowModeSet guards drift between
// config.AINewWindowModes, the one product-code list of AI modes, and the app
// layer: every central mode must survive normalizeAIMode and validAIMode
// unchanged, and every aiMode* string constant, `ai settings` picker row, and
// Settings AI default-mode row must name a mode in the central list. A mode
// added on only one side fails here.
func TestAIModeSetMatchesTheCentralNewWindowModeSet(t *testing.T) {
	for _, mode := range config.AINewWindowModes {
		if got := normalizeAIMode(mode); got != mode {
			t.Errorf("normalizeAIMode(%q) = %q, want it unchanged", mode, got)
		}
		if got, ok := validAIMode(mode); !ok || got != mode {
			t.Errorf("validAIMode(%q) = %q, %v, want %q, true", mode, got, ok, mode)
		}
	}

	constants := aiModeStringConstants(t)
	if len(constants) < 6 || constants["aiModeSelective"] != aiModeSelective {
		t.Fatalf("discovered aiMode* constants %v, want at least the six known modes including aiModeSelective", constants)
	}
	for name, mode := range constants {
		assertCentralAIMode(t, mode, "constant "+name)
	}

	home := t.TempDir()
	ai := testAICommand(home)
	paths, err := configPaths(ai.homeDir, ai.lookupEnv)
	if err != nil {
		t.Fatalf("configPaths() error = %v", err)
	}
	if err := config.SaveAIEnabledAgentsFile(paths.AIEnabledAgentsFile(), config.KnownAIAgentProviders()); err != nil {
		t.Fatalf("SaveAIEnabledAgentsFile() error = %v", err)
	}

	pickerModes := 0
	sawProvider := false
	for _, row := range ai.settingsRows() {
		if row.Value == "" {
			continue
		}
		pickerModes++
		if _, ok := aiModeProvider(row.Value); ok {
			sawProvider = true
		}
		assertCentralAIMode(t, row.Value, "settingsRows")
	}
	if pickerModes == 0 || !sawProvider {
		t.Fatalf("settingsRows gave %d mode rows (provider row: %v), want mode rows including a provider", pickerModes, sawProvider)
	}

	settings := &settingsCommand{
		ai:        ai,
		homeDir:   ai.homeDir,
		lookupEnv: ai.lookupEnv,
	}
	settingsModes := 0
	for _, entry := range settings.aiEntries() {
		mode, ok := strings.CutPrefix(entry.Value, settingsActionPrefixAI)
		if !ok {
			continue
		}
		settingsModes++
		assertCentralAIMode(t, mode, "aiEntries")
	}
	if settingsModes == 0 {
		t.Fatal("aiEntries gave no mode rows")
	}
}

func assertCentralAIMode(t *testing.T, mode, source string) {
	t.Helper()
	if !slices.Contains(config.AINewWindowModes, mode) {
		t.Errorf("%s names AI mode %q, which is not in config.AINewWindowModes %v", source, mode, config.AINewWindowModes)
	}
}

// aiModeStringConstants parses the package's non-test sources and returns
// every string constant whose name starts with aiMode, keyed by name. Types
// and funcs with that prefix, such as aiModeController, are not constants.
func aiModeStringConstants(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package sources: %v", err)
	}
	fset := token.NewFileSet()
	constants := map[string]string{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			decl, ok := node.(*ast.GenDecl)
			if !ok || decl.Tok != token.CONST {
				return true
			}
			for _, spec := range decl.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, ident := range value.Names {
					if !strings.HasPrefix(ident.Name, "aiMode") || i >= len(value.Values) {
						continue
					}
					lit, ok := value.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					mode, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("unquote %s in %s: %v", ident.Name, name, err)
					}
					constants[ident.Name] = mode
				}
			}
			return true
		})
	}
	return constants
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
		{name: "no resolvers"},
		{name: "home error", homeDir: failing},
		{name: "home", homeDir: func() (string, error) { return "/h", nil }, want: "/h/.config"},
		{name: "XDG wins", homeDir: func() (string, error) { return "/h", nil }, lookupEnv: func(string) string { return " /x " }, want: "/x"},
		{name: "blank XDG is unset", homeDir: func() (string, error) { return "/h", nil }, lookupEnv: func(string) string { return " " }, want: "/h/.config"},
		{name: "blank XDG and empty home", homeDir: func() (string, error) { return "", nil }, lookupEnv: func(string) string { return " " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := aiConfigHome(tc.homeDir, tc.lookupEnv)
			c := &aiCommand{homeDir: tc.homeDir, lookupEnv: tc.lookupEnv}
			file, fileErr := c.configFile()
			if tc.want == "" {
				// Without a config home both refuse with the missing-HOME
				// reason instead of a path relative to the working directory.
				if got != "" || !isMissingHome(err) || file != "" || !isMissingHome(fileErr) {
					t.Fatalf("aiConfigHome() = %q, %v; configFile() = %q, %v; want the missing-HOME reason", got, err, file, fileErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("aiConfigHome() = %q, %v; want %q", got, err, tc.want)
			}
			central := config.DefaultPaths(tc.want, "").AINewWindowModeFile()
			if fileErr != nil || filepath.Dir(file) != filepath.Dir(central) {
				t.Fatalf("configFile() %q, %v and central %q differ in directory", file, fileErr, central)
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
