package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// nonSettingPathSymbols are the path symbols in this package that are not
// settings, each with the reason. Every other *FileName, *DirName and
// *FilePrefix constant must be tied to a row of the declaration.
var nonSettingPathSymbols = map[string]string{
	"AppName":                     "the projmux directory name itself, not a setting",
	"PreviewStateFileName":        "state under StateDir, not a setting",
	"LiveResourcesSampleFileName": "state under StateDir, not a setting",
	"PostCreateHookFileName":      "a script inside the declared hooks/ directory",
	"PostAttachHookFileName":      "a script inside the declared hooks/ directory",
	"PreCreateHookFileName":       "a script inside the declared hooks/ directory",
}

// packagePathSymbols returns every string constant of this package's non-test
// sources, by name.
func packagePathSymbols(t *testing.T) (map[string]string, []*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	consts := map[string]string{}
	var files []*ast.File
	for _, name := range matches {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value := spec.(*ast.ValueSpec)
				for i, ident := range value.Names {
					if i >= len(value.Values) {
						continue
					}
					if lit, ok := value.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						text, err := strconv.Unquote(lit.Value)
						if err != nil {
							t.Fatal(err)
						}
						consts[ident.Name] = text
					}
				}
			}
		}
	}
	return consts, files
}

func isPathSymbolName(name string) bool {
	return strings.HasSuffix(name, "FileName") || strings.HasSuffix(name, "DirName") || strings.HasSuffix(name, "FilePrefix")
}

// declaredFiles are the File values of the declared file, family and
// directory items.
func declaredFiles() map[string]SettingItem {
	out := map[string]SettingItem{}
	for _, item := range settingItems {
		switch item.Shape {
		case SettingFile, SettingFileFamily, SettingDir, SettingConfigKeys:
			out[item.File] = item
		}
	}
	return out
}

// TestEveryConfigPathSymbolDeclaresItsLayer holds the declaration complete:
// every path symbol of this package is either a declared setting or named in
// nonSettingPathSymbols, and every path this package joins onto ConfigDir (or
// onto AppName under a config home) names a declared symbol rather than a
// literal.
func TestEveryConfigPathSymbolDeclaresItsLayer(t *testing.T) {
	consts, files := packagePathSymbols(t)
	declared := declaredFiles()

	for name, value := range consts {
		if !isPathSymbolName(name) {
			continue
		}
		if _, ok := nonSettingPathSymbols[name]; ok {
			continue
		}
		if _, ok := declared[value]; !ok {
			t.Errorf("path symbol %s = %q has no row in settingItems; declare its layer in layers.go (or list it in nonSettingPathSymbols with the reason)", name, value)
		}
	}
	for name := range nonSettingPathSymbols {
		if _, ok := consts[name]; !ok {
			t.Errorf("nonSettingPathSymbols names %s, which is not a constant of this package", name)
		}
	}
	for _, item := range settingItems {
		if item.Shape == SettingBrowserKeys {
			continue
		}
		tied := false
		for name, value := range consts {
			if value == item.File && (isPathSymbolName(name) || name == "GlobalConfigFileName") {
				tied = true
			}
		}
		if !tied {
			t.Errorf("declared setting %q (File %q) is not tied to a path symbol constant", item.Name, item.File)
		}
	}

	stateOnly := map[string]bool{"PreviewStateFileName": true, "LiveResourcesSampleFileName": true}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isFilepathJoin(call) {
				return true
			}
			for i, arg := range call.Args {
				base := ""
				switch expr := arg.(type) {
				case *ast.SelectorExpr:
					base = expr.Sel.Name
				case *ast.Ident:
					base = expr.Name
				}
				if base != "ConfigDir" && base != "AppName" && base != "StateDir" {
					continue
				}
				if i+1 >= len(call.Args) {
					continue
				}
				symbol := leadingIdent(call.Args[i+1])
				if symbol == "" {
					t.Errorf("filepath.Join onto %s names a setting by a literal; give it a path symbol and a row in settingItems", base)
					break
				}
				if base == "StateDir" {
					if !stateOnly[symbol] {
						t.Errorf("filepath.Join onto StateDir names %s, which is not a known state file", symbol)
					}
					break
				}
				value, ok := consts[symbol]
				if !ok {
					t.Errorf("filepath.Join onto %s names %s, which is not a string constant of this package", base, symbol)
					break
				}
				if _, ok := declared[value]; !ok {
					t.Errorf("filepath.Join onto %s names %s = %q, which has no row in settingItems", base, symbol, value)
				}
				break
			}
			return true
		})
	}
}

func isFilepathJoin(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Join" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "filepath"
}

// leadingIdent returns the constant a path element starts with: the
// identifier itself, or the left operand of `Prefix + suffix`.
func leadingIdent(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.BinaryExpr:
		return leadingIdent(e.X)
	}
	return ""
}

func TestSettingDeclarationIsWellFormed(t *testing.T) {
	names := map[string]bool{}
	for _, item := range settingItems {
		if item.Name == "" || item.File == "" || item.Doc == "" {
			t.Errorf("incomplete setting row %+v", item)
		}
		if names[item.Name] {
			t.Errorf("setting %q is declared twice", item.Name)
		}
		names[item.Name] = true
		switch item.Layer {
		case LayerCentral, LayerTUI, LayerWeb, LayerBrowser:
		default:
			t.Errorf("setting %q has unknown layer %q", item.Name, item.Layer)
		}
		if got, ok := LookupSetting(item.Name); !ok || got != item {
			t.Errorf("LookupSetting(%q) = %+v, %v", item.Name, got, ok)
		}
		switch item.Shape {
		case SettingFile:
			if got, ok := SettingForFile(item.File); !ok || got != item {
				t.Errorf("SettingForFile(%q) = %q, %v; want %q", item.File, got.Name, ok, item.Name)
			}
		case SettingDir:
			if got, ok := SettingForFile(item.File + "/x.json"); !ok || got != item {
				t.Errorf("SettingForFile(%q/x.json) = %q, %v; want %q", item.File, got.Name, ok, item.Name)
			}
		case SettingFileFamily:
			if got, ok := SettingForFile(item.File + "claude"); !ok || got != item {
				t.Errorf("SettingForFile(%qclaude) = %q, %v; want %q", item.File, got.Name, ok, item.Name)
			}
		case SettingConfigKeys:
			if !strings.HasPrefix(item.Name, GlobalConfigFileName+" [") {
				t.Errorf("config key set %q must be spelled `config.toml [section] ...`", item.Name)
			}
		}
	}
	if _, ok := SettingForFile(GlobalConfigFileName); ok {
		t.Errorf("config.toml resolved to one layer; its layer is declared per key set")
	}
}

// TestConfigFrontLoadersReportEveryRead proves the seam is wired in the
// loaders of this package: each front loader reports the declared item before
// it reads, and a central loader reports nothing.
//
// No t.Parallel: it installs the package-level observer.
func TestConfigFrontLoadersReportEveryRead(t *testing.T) {
	paths := DefaultPaths(filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "state"))
	var reads []FrontRead
	restore := ObserveFrontReads(func(read FrontRead) { reads = append(reads, read) })
	defer restore()

	expect := func(label string, want ...string) {
		t.Helper()
		var got []string
		for _, read := range reads {
			if read.Item.Shape == 0 || (read.Item.Layer != LayerTUI && read.Item.Layer != LayerWeb) {
				t.Errorf("%s: reported %q, which is not a declared front setting", label, read.Item.Name)
			}
			got = append(got, read.Item.Name)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: front reads = %q, want %q", label, got, want)
		}
		reads = nil
	}

	_, _ = LoadStatusbarVisibilityFile(paths.StatusbarGitVisibilityFile())
	expect("statusbar git visibility", StatusbarGitVisibilityFileName)
	_, _ = LoadStatusbarVisibilityFileWithDefault(paths.StatusbarAgentUsageWindowVisibilityFile("claude", "5h"), StatusbarVisibilityOff)
	expect("usage window visibility", StatusbarAgentUsageWindowVisibilityFilePrefix+"<provider>-<window>")
	_, _ = LoadStatusbarVisibilityFile(paths.StatusbarAgentUsageProviderVisibilityFile("codex"))
	expect("usage provider visibility", StatusbarAgentUsageProviderVisibilityFilePrefix+"<provider>")
	_, _ = LoadStatusbarDecorationFile(paths.StatusbarDecorationFile())
	_, _ = LoadStatusbarDecorationFile(paths.StatusbarDecorationNotifyFile())
	expect("decoration", StatusbarDecorationFileName, StatusbarDecorationNotifyFileName)
	_, _ = LoadAIBadgeStyleFile(paths.AIBadgeStyleFile())
	expect("badge", AIBadgeStyleFileName)
	_, _, _ = LoadRuntimeDiagnosticsVisibilityFile(paths.RuntimeDiagnosticsVisibilityFile())
	expect("runtime diagnostics", RuntimeDiagnosticsVisibilityFileName)
	_, _ = LoadWebSettingsFile(paths.WebSettingsFile(), nil)
	expect("web settings", WebSettingsFileName)

	NoteFrontRead(KeymapFileName, paths.KeymapFile())
	NoteFrontRead(SettingConfigTheme, paths.GlobalConfigFile())
	expect("app-side names", KeymapFileName, SettingConfigTheme)

	_, _ = LoadAIEnabledAgentsFile(paths.AIEnabledAgentsFile())
	_, _ = LoadLiveResourcesFile(paths.LiveResourcesFile())
	_, _ = LoadDesktopNotifyModeFile(paths.DesktopNotifyModeFile())
	_, _ = LoadProjectHooksFile(paths.ProjectHooksFile())
	_, _ = LoadAINotifyDedupeSecondsFileDefault(paths.AINotifyDedupeSecondsFile(), 0)
	_, _ = LoadAIHookActionsFile(paths.AIHookActionsFile())
	_, _ = LoadAISemanticPoliciesFile(paths.AISemanticPoliciesFile())
	expect("central loaders")

	restore()
	_, _ = LoadStatusbarVisibilityFile(paths.StatusbarGitVisibilityFile())
	if len(reads) != 0 {
		t.Errorf("a restored observer still received %d read(s)", len(reads))
	}
}

// TestPickerDisplayPurposeCoversOnlyThemeAndKeymap holds the seam side of
// the picker exemption: NotePickerDisplayRead keeps the picker display
// purpose only for `[theme]` and keymap.toml and reports every other front
// setting as a setting read.
//
// No t.Parallel: it installs the package-level observer.
func TestPickerDisplayPurposeCoversOnlyThemeAndKeymap(t *testing.T) {
	var reads []FrontRead
	restore := ObserveFrontReads(func(read FrontRead) { reads = append(reads, read) })
	defer restore()

	for _, item := range settingItems {
		if item.Layer != LayerTUI && item.Layer != LayerWeb {
			continue
		}
		reads = nil
		NotePickerDisplayRead(item.Name, "/x/"+item.File)
		NoteFrontRead(item.Name, "/x/"+item.File)
		if len(reads) != 2 {
			t.Fatalf("%s: %d reads, want 2", item.Name, len(reads))
		}
		want := FrontReadSetting
		if item.Name == SettingConfigTheme || item.Name == KeymapFileName {
			want = FrontReadPickerDisplay
		}
		if reads[0].Purpose != want {
			t.Errorf("NotePickerDisplayRead(%s) purpose = %d, want %d", item.Name, reads[0].Purpose, want)
		}
		if reads[1].Purpose != FrontReadSetting {
			t.Errorf("NoteFrontRead(%s) purpose = %d, want a setting read", item.Name, reads[1].Purpose)
		}
	}
	reads = nil
	_, _ = LoadStatusbarVisibilityFile("/x/" + StatusbarGitVisibilityFileName)
	if len(reads) != 1 || reads[0].Purpose != FrontReadSetting {
		t.Errorf("a config loader read = %+v, want one setting read", reads)
	}
}

// TestPathSymbolsKeepTheirPaths pins the path symbols that replaced inline
// paths to the exact bytes they replaced.
func TestPathSymbolsKeepTheirPaths(t *testing.T) {
	paths := DefaultPaths("/x/config", "/x/state")
	for _, tc := range []struct{ got, want string }{
		{paths.TmuxAISplitModeFile(), "/x/config/projmux/tmux-ai-split-mode"},
		{DefaultPaths(".config", "").TmuxAISplitModeFile(), ".config/projmux/tmux-ai-split-mode"},
		{paths.StatusbarDecorationCwdFile(), "/x/config/projmux/statusbar-decoration-cwd"},
		{paths.StatusbarDecorationGitFile(), "/x/config/projmux/statusbar-decoration-git"},
		{paths.StatusbarDecorationNotifyFile(), "/x/config/projmux/statusbar-decoration-notify"},
		{paths.AIHookCatalogOverrideFile("codex"), "/x/config/projmux/ai-hooks.d/codex.json"},
		{DefaultPaths("/x/config/", "").AIHookCatalogOverrideFile("c"), "/x/config/projmux/ai-hooks.d/c.json"},
	} {
		if tc.got != tc.want {
			t.Errorf("path = %q, want %q", tc.got, tc.want)
		}
	}
}

// docsLayerNames maps the docs table's Layer cell to a layer.
var docsLayerNames = map[string]SettingLayer{
	"central": LayerCentral,
	"TUI":     LayerTUI,
	"WEB":     LayerWeb,
	"browser": LayerBrowser,
}

// docsLayerQualifiers are code spans in the Where column that qualify the
// names after them instead of naming a setting: config.toml holds keys of
// the central and TUI layers, and the browser keys live in localStorage.
var docsLayerQualifiers = map[string]bool{GlobalConfigFileName: true, "localStorage": true}

// TestConfigurationDocsLayerTableMatchesTheDeclaration holds the "Settings live
// in four layers" table in docs/configuration.md to settingItems, both ways:
// every declared setting appears in its layer's row, and every name in a row
// is declared with that layer.
func TestConfigurationDocsLayerTableMatchesTheDeclaration(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "configuration.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	_, after, ok := strings.Cut(text, "Settings live in four layers:")
	if !ok {
		t.Fatal(`docs/configuration.md has no "Settings live in four layers:" table`)
	}
	rows := map[SettingLayer][]string{}
	span := regexp.MustCompile("`([^`]+)`")
	started := false
	for line := range strings.SplitSeq(after, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			if started {
				break
			}
			continue
		}
		started = true
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 3 {
			t.Fatalf("layer table row %q does not have Layer | Where | What", line)
		}
		layer, ok := docsLayerNames[strings.TrimSpace(cells[0])]
		if !ok {
			continue // header and separator
		}
		if _, dup := rows[layer]; dup {
			t.Errorf("layer %s has two rows", layer)
		}
		rows[layer] = []string{}
		for _, match := range span.FindAllStringSubmatch(cells[1], -1) {
			rows[layer] = append(rows[layer], match[1])
		}
	}
	for name, layer := range docsLayerNames {
		if _, ok := rows[layer]; !ok {
			t.Errorf("layer table has no %s row", name)
		}
	}
	for _, item := range settingItems {
		if !slices.Contains(rows[item.Layer], item.Doc) {
			t.Errorf("setting %q is declared %s but the docs %s row does not list `%s`", item.Name, item.Layer, item.Layer, item.Doc)
		}
	}
	for layer, names := range rows {
		for _, name := range names {
			if docsLayerQualifiers[name] {
				continue
			}
			found := false
			for _, item := range settingItems {
				if item.Doc == name && item.Layer == layer {
					found = true
				}
			}
			if !found {
				t.Errorf("the docs %s row lists `%s`, which is not declared with layer %s in settingItems", layer, name, layer)
			}
		}
	}
}
