package app

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

// tmux 3.6's compiled-in stock menus, exactly as `list-keys` prints the
// display-menu of `prefix <`, MouseDown3Status and M-MouseDown3Status (one item
// list) and of `prefix >` and M-MouseDown3Pane (another) on an isolated
// `tmux -f /dev/null` server. They are spelled out here instead of read from
// the renderer so the generated menus are held to tmux rather than to
// themselves.
const (
	tmux36StockWindowMenuItems = `"#{?#{>:#{session_windows},1},,-}Swap Left" l { swap-window -t :-1 } "#{?#{>:#{session_windows},1},,-}Swap Right" r { swap-window -t :+1 } "#{?pane_marked_set,,-}Swap Marked" s { swap-window } '' Kill X { kill-window } Respawn R { respawn-window -k } "#{?pane_marked,Unmark,Mark}" m { select-pane -m } Rename n { command-prompt -F -I "#W" { rename-window -t "#{window_id}" "%%" } } '' "New After" w { new-window -a } "New At End" W { new-window }`
	tmux36StockPaneMenuItems   = `"#{?#{m/r:(copy|view)-mode,#{pane_mode}},Go To Top,}" < { send-keys -X history-top } "#{?#{m/r:(copy|view)-mode,#{pane_mode}},Go To Bottom,}" > { send-keys -X history-bottom } '' "#{?mouse_word,Search For #[underscore]#{=/9/...:mouse_word},}" C-r { if-shell -F "#{?#{m/r:(copy|view)-mode,#{pane_mode}},0,1}" "copy-mode -t=" ; send-keys -X -t = search-backward -- "#{q:mouse_word}" } "#{?mouse_word,Type #[underscore]#{=/9/...:mouse_word},}" C-y { copy-mode -q ; send-keys -l "#{q:mouse_word}" } "#{?mouse_word,Copy #[underscore]#{=/9/...:mouse_word},}" c { copy-mode -q ; set-buffer "#{q:mouse_word}" } "#{?mouse_line,Copy Line,}" l { copy-mode -q ; set-buffer "#{q:mouse_line}" } '' "#{?mouse_hyperlink,Type #[underscore]#{=/9/...:mouse_hyperlink},}" C-h { copy-mode -q ; send-keys -l "#{q:mouse_hyperlink}" } "#{?mouse_hyperlink,Copy #[underscore]#{=/9/...:mouse_hyperlink},}" h { copy-mode -q ; set-buffer "#{q:mouse_hyperlink}" } '' "Horizontal Split" h { split-window -h } "Vertical Split" v { split-window -v } '' "#{?#{>:#{window_panes},1},,-}Swap Up" u { swap-pane -U } "#{?#{>:#{window_panes},1},,-}Swap Down" d { swap-pane -D } "#{?pane_marked_set,,-}Swap Marked" s { swap-pane } '' Kill X { kill-pane } Respawn R { respawn-pane -k } "#{?pane_marked,Unmark,Mark}" m { select-pane -m } "#{?#{>:#{window_panes},1},,-}#{?window_zoomed_flag,Unzoom,Zoom}" z { resize-pane -Z }`

	tmux36StockKeyWindowMenu   = `display-menu -T "#[align=centre]#{window_index}:#{window_name}" -x W -y W ` + tmux36StockWindowMenuItems
	tmux36StockMouseWindowMenu = `display-menu -T "#[align=centre]#{window_index}:#{window_name}" -t = -x W -y W ` + tmux36StockWindowMenuItems
	tmux36StockKeyPaneMenu     = `display-menu -T "#[align=centre]#{pane_index} (#{pane_id})" -x P -y P ` + tmux36StockPaneMenuItems
	tmux36StockMousePaneMenu   = `display-menu -T "#[align=centre]#{pane_index} (#{pane_id})" -t = -x M -y M ` + tmux36StockPaneMenuItems
)

// managedStockMenuBinding is one stock tmux menu binding the app config
// replaces, with the unbind line that must precede its generated bind.
type managedStockMenuBinding struct {
	Name, Bind, Unbind string
}

var managedStockMenuBindings = []managedStockMenuBinding{
	{Name: "prefix <", Bind: "bind-key < ", Unbind: "unbind-key -q <"},
	{Name: "MouseDown3Status", Bind: "bind-key -n MouseDown3Status ", Unbind: "unbind-key -q -n MouseDown3Status"},
	{Name: "M-MouseDown3Status", Bind: "bind-key -n M-MouseDown3Status ", Unbind: "unbind-key -q -n M-MouseDown3Status"},
	{Name: "prefix >", Bind: "bind-key > ", Unbind: "unbind-key -q >"},
	{Name: "M-MouseDown3Pane", Bind: "bind-key -n M-MouseDown3Pane ", Unbind: "unbind-key -q -n M-MouseDown3Pane"},
}

const mouseDown3PaneBind = "bind-key -n MouseDown3Pane "

// tmuxMenuItem is one display-menu item. A separator is the zero value.
type tmuxMenuItem struct {
	Name, Key, Command string
}

type tmuxMenu struct {
	Flags []string
	Items []tmuxMenuItem
}

func (m tmuxMenu) item(t *testing.T, name string) tmuxMenuItem {
	t.Helper()
	var found []tmuxMenuItem
	for _, item := range m.Items {
		if item.Name == name {
			found = append(found, item)
		}
	}
	if len(found) != 1 {
		t.Fatalf("menu has %d item(s) named %q, want 1: %#v", len(found), name, m.Items)
	}
	return found[0]
}

// tmuxConfigWords splits one tmux command into its top-level words the way the
// tmux parser groups them: a double- or single-quoted string is one word with
// its quotes removed, a `{ ... }` block is one word kept verbatim with its
// braces, and anything else runs to the next space.
func tmuxConfigWords(t *testing.T, line string) []string {
	t.Helper()
	// skipQuoted returns the index of the quote that closes the one at start.
	skipQuoted := func(start int) int {
		quote := line[start]
		for i := start + 1; i < len(line); i++ {
			if quote == '"' && line[i] == '\\' {
				i++
				continue
			}
			if line[i] == quote {
				return i
			}
		}
		t.Fatalf("unterminated %c quote at %d in %q", quote, start, line)
		return -1
	}
	var words []string
	for i := 0; i < len(line); {
		switch line[i] {
		case ' ':
			i++
		case '"':
			end := skipQuoted(i)
			words = append(words, regexp.MustCompile(`\\(.)`).ReplaceAllString(line[i+1:end], "$1"))
			i = end + 1
		case '\'':
			end := skipQuoted(i)
			words = append(words, line[i+1:end])
			i = end + 1
		case '{':
			depth, end := 0, -1
			for j := i; j < len(line) && end < 0; j++ {
				switch line[j] {
				case '"', '\'':
					j = skipQuoted(j)
				case '{':
					depth++
				case '}':
					if depth--; depth == 0 {
						end = j
					}
				}
			}
			if end < 0 {
				t.Fatalf("unterminated block at %d in %q", i, line)
			}
			words = append(words, line[i:end+1])
			i = end + 1
		default:
			end := strings.IndexByte(line[i:], ' ')
			if end < 0 {
				end = len(line) - i
			}
			words = append(words, line[i:i+end])
			i += end
		}
	}
	return words
}

func tmuxBlockCommand(t *testing.T, block string) string {
	t.Helper()
	if !strings.HasPrefix(block, "{ ") || !strings.HasSuffix(block, " }") {
		t.Fatalf("word %q is not a { ... } command block", block)
	}
	return block[2 : len(block)-2]
}

func parseTmuxDisplayMenu(t *testing.T, command string) tmuxMenu {
	t.Helper()
	words := tmuxConfigWords(t, command)
	if len(words) == 0 || words[0] != "display-menu" {
		t.Fatalf("command is not a display-menu: %q", command)
	}
	var menu tmuxMenu
	i := 1
	for i+1 < len(words) && len(words[i]) == 2 && words[i][0] == '-' {
		menu.Flags = append(menu.Flags, words[i], words[i+1])
		i += 2
	}
	for i < len(words) {
		if words[i] == "" {
			menu.Items = append(menu.Items, tmuxMenuItem{})
			i++
			continue
		}
		if i+2 >= len(words) {
			t.Fatalf("truncated menu item at word %d of %q", i, command)
		}
		menu.Items = append(menu.Items, tmuxMenuItem{Name: words[i], Key: words[i+1], Command: tmuxBlockCommand(t, words[i+2])})
		i += 3
	}
	return menu
}

func configLinesWithPrefix(rendered, prefix string) []string {
	var lines []string
	for line := range strings.SplitSeq(rendered, "\n") {
		if strings.HasPrefix(line, prefix) {
			lines = append(lines, line)
		}
	}
	return lines
}

func configLineIndexWithPrefix(rendered, prefix string) int {
	for index, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(line, prefix) {
			return index
		}
	}
	return -1
}

// generatedMenu parses the one generated menu bound by bind in a rendered
// config. MouseDown3Pane wraps its menu in the mouse-forwarding guard, so its
// display-menu is the guard's else block.
func generatedMenu(t *testing.T, rendered, bind string) tmuxMenu {
	t.Helper()
	lines := configLinesWithPrefix(rendered, bind)
	if len(lines) != 1 {
		t.Fatalf("rendered config has %d %q line(s), want 1", len(lines), bind)
	}
	command := strings.TrimPrefix(lines[0], bind)
	if bind == mouseDown3PaneBind {
		words := tmuxConfigWords(t, command)
		command = tmuxBlockCommand(t, words[len(words)-1])
	}
	return parseTmuxDisplayMenu(t, command)
}

// expandTmuxFormatsForTest does what tmux 3.6 does to a menu item command when
// the menu opens: `##` becomes `#` and every `#{name}` becomes its value for
// the menu target.
func expandTmuxFormatsForTest(t *testing.T, command string, values map[string]string) string {
	t.Helper()
	var out strings.Builder
	for i := 0; i < len(command); i++ {
		switch {
		case strings.HasPrefix(command[i:], "##"):
			out.WriteByte('#')
			i++
		case strings.HasPrefix(command[i:], "#{"):
			end := strings.IndexByte(command[i:], '}')
			name := command[i+2 : i+end]
			value, ok := values[name]
			if !ok {
				t.Fatalf("no test value for format #{%s} in %q", name, command)
			}
			out.WriteString(value)
			i += end
		default:
			out.WriteByte(command[i])
		}
	}
	return out.String()
}

func TestManagedStockMenusRenderInAppConfigOnlyAfterUnbindingTmuxStock(t *testing.T) {
	t.Parallel()

	const bin = "/usr/local/bin/projmux"
	app := tmuxAppConfig(bin, "/bin/sh", config.StatusbarDecorationOff)
	standalone := tmuxStandaloneConfig(bin, config.StatusbarDecorationOff)
	for _, binding := range managedStockMenuBindings {
		if got := len(configLinesWithPrefix(app, binding.Bind+"display-menu ")); got != 1 {
			t.Fatalf("app config has %d generated %s menu line(s), want 1", got, binding.Name)
		}
		if got := len(configLinesWithPrefix(app, binding.Bind)); got != 1 {
			t.Fatalf("app config binds %s %d time(s), want only the generated menu", binding.Name, got)
		}
		if got := countConfigLines(app, binding.Unbind); got != 1 {
			t.Fatalf("app config has %d %q line(s), want 1", got, binding.Unbind)
		}
		if unbind, bind := configLineIndex(app, binding.Unbind), configLineIndexWithPrefix(app, binding.Bind); unbind > bind {
			t.Fatalf("app config must unbind tmux's stock %s before binding the generated menu (unbind=%d bind=%d)", binding.Name, unbind, bind)
		}
		// The standalone ~/.tmux.conf snippet runs on every server the operator
		// starts, so it never touches tmux's stock menu for this key.
		if len(configLinesWithPrefix(standalone, binding.Bind)) != 0 || countConfigLines(standalone, binding.Unbind) != 0 {
			t.Fatalf("standalone config replaces tmux's stock %s menu; it must stay stock", binding.Name)
		}
	}
	// MouseDown3Pane keeps rendering where it did: once in each config, the same
	// bytes in both.
	appPane, standalonePane := configLinesWithPrefix(app, mouseDown3PaneBind), configLinesWithPrefix(standalone, mouseDown3PaneBind)
	if len(appPane) != 1 || len(standalonePane) != 1 || appPane[0] != standalonePane[0] {
		t.Fatalf("MouseDown3Pane menu app=%d standalone=%d identical=%t, want one identical line in each", len(appPane), len(standalonePane),
			len(appPane) == 1 && len(standalonePane) == 1 && appPane[0] == standalonePane[0])
	}

	// A keymap that puts a managed action on `prefix <` still owns the key: the
	// menu renders first and the managed bind replaces it.
	parsed, err := parseKeymapFile("keymap.toml", "schema_version = 2\n\n[bindings.\"window.delete\"]\nprefix = \"<\"\n")
	if err != nil {
		t.Fatalf("parse keymap: %v", err)
	}
	merged, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), parsed)
	if err != nil {
		t.Fatalf("merge keymap: %v", err)
	}
	remapped := tmuxAppConfigWithKeymap(bin, "/bin/sh", statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff), merged, true)
	menuAt, managedAt := configLineIndexWithPrefix(remapped, "bind-key < display-menu "), configLineIndexWithPrefix(remapped, "bind-key < if-shell ")
	if menuAt < 0 || managedAt < 0 || menuAt > managedAt {
		t.Fatalf("a keymap-assigned prefix < must win over the generated Window menu (menu=%d managed=%d)", menuAt, managedAt)
	}
}

// managedMenuItemCommands are the exact commands the generated menus carry for
// the items that do not stay stock.
func managedMenuItemCommands(bin string) (window, pane map[string]string) {
	quoted := "'" + bin + "'"
	windowIntent := func(route string) string {
		return `run-shell "TMUX_PANE=#{pane_id} PROJMUX_POPUP_TARGET_CLIENT=#{client_tty} ` + quoted + ` internal tmux ` + route + `"`
	}
	paneIntent := func(action string) string {
		return `run-shell "` + quoted + ` internal tmux pane-menu --client #{client_tty} ` + action + ` #{pane_id}"`
	}
	window = map[string]string{
		// No delete-confirm: selecting Kill in a menu is the confirmation.
		"Kill":       `if-shell -F "#{@projmux_window_uid}" { ` + windowIntent("window-delete --client #{client_tty} --anchor #{pane_id}") + ` } { kill-window }`,
		"Rename":     `command-prompt -I "#{window_name}" "run-shell \"TMUX_PANE=##{pane_id} PROJMUX_POPUP_TARGET_CLIENT=##{client_tty} ` + quoted + ` internal tmux window-rename --client ##{client_tty} --anchor ##{pane_id} -- '%%'\""`,
		"New At End": windowIntent("window-create --client #{client_tty} --anchor #{pane_id}"),
	}
	pane = map[string]string{
		"Horizontal Split": paneIntent("split-right"),
		"Vertical Split":   paneIntent("split-down"),
		// The managed branch is the Pane menu Kill route byte for byte.
		"Kill": `if-shell -F "#{@projmux_pane_uid}" { ` + paneIntent("kill") + ` } { kill-pane }`,
	}
	return window, pane
}

func TestManagedStockMenusKeepTmux36ItemsExceptManagedRoutes(t *testing.T) {
	t.Parallel()

	const bin = "/usr/local/bin/projmux"
	app := tmuxAppConfig(bin, "/bin/sh", config.StatusbarDecorationOff)
	window, pane := managedMenuItemCommands(bin)
	stock := map[string]string{
		"prefix <": tmux36StockKeyWindowMenu, "MouseDown3Status": tmux36StockMouseWindowMenu, "M-MouseDown3Status": tmux36StockMouseWindowMenu,
		"prefix >": tmux36StockKeyPaneMenu, "M-MouseDown3Pane": tmux36StockMousePaneMenu,
	}
	managed := map[string]map[string]string{
		"prefix <": window, "MouseDown3Status": window, "M-MouseDown3Status": window,
		"prefix >": pane, "M-MouseDown3Pane": pane,
	}
	for _, binding := range managedStockMenuBindings {
		stockMenu := parseTmuxDisplayMenu(t, stock[binding.Name])
		got := generatedMenu(t, app, binding.Bind)
		if !reflect.DeepEqual(got.Flags, stockMenu.Flags) {
			t.Fatalf("%s title/target/position = %q, want tmux stock %q", binding.Name, got.Flags, stockMenu.Flags)
		}
		var want []tmuxMenuItem
		replaced := 0
		for _, item := range stockMenu.Items {
			switch {
			case item.Name == "Respawn" || item.Name == "New After":
				// No managed intent preserves their contract, so they are removed.
				continue
			case managed[binding.Name][item.Name] != "":
				item.Command = managed[binding.Name][item.Name]
				replaced++
			}
			want = append(want, item)
		}
		if replaced != len(managed[binding.Name]) {
			t.Fatalf("%s replaced %d stock item(s), want %d: the stock fixture no longer names every managed item", binding.Name, replaced, len(managed[binding.Name]))
		}
		if len(got.Items) != len(want) {
			t.Fatalf("%s has %d item(s), want %d\n got: %#v\nwant: %#v", binding.Name, len(got.Items), len(want), got.Items, want)
		}
		for i := range want {
			if got.Items[i] != want[i] {
				t.Errorf("%s item %d = %#v, want %#v", binding.Name, i, got.Items[i], want[i])
			}
		}
	}
}

func TestManagedMenuKillCarriesTmuxKillOnlyAsTheMirrorAbsentBranch(t *testing.T) {
	t.Parallel()

	const bin = "/usr/local/bin/projmux"
	_, pane := managedMenuItemCommands(bin)
	// The guards are the ones the managed close keys use.
	if managedDeletePaneGuard != "#{@projmux_pane_uid}" || managedDeleteWindowGuard != "#{@projmux_window_uid}" {
		t.Fatalf("managed delete guards = %q/%q, want the Pane/Window uid mirrors", managedDeletePaneGuard, managedDeleteWindowGuard)
	}
	configs := map[string]string{
		"standalone": tmuxStandaloneConfig(bin, config.StatusbarDecorationOff),
		"app":        tmuxAppConfig(bin, "/bin/sh", config.StatusbarDecorationOff),
	}
	for kind, rendered := range configs {
		kill := generatedMenu(t, rendered, mouseDown3PaneBind).item(t, "Kill")
		// A mirrored Pane keeps exactly the Pane menu Kill route it had; a Pane
		// without the mirror now gets tmux's own kill instead of a refusal.
		if kill.Key != "X" || kill.Command != pane["Kill"] {
			t.Fatalf("%s MouseDown3Pane Kill = %#v, want %q", kind, kill, pane["Kill"])
		}
	}
	app := configs["app"]
	for _, bind := range []string{mouseDown3PaneBind, "bind-key > ", "bind-key -n M-MouseDown3Pane ", "bind-key < ", "bind-key -n MouseDown3Status ", "bind-key -n M-MouseDown3Status "} {
		kill := generatedMenu(t, app, bind).item(t, "Kill")
		words := tmuxConfigWords(t, kill.Command)
		if len(words) != 5 || words[0] != "if-shell" || words[1] != "-F" {
			t.Fatalf("%s Kill is not one if-shell -F guard with two branches: %q", bind, kill.Command)
		}
		managedBranch, elseBranch := tmuxBlockCommand(t, words[3]), tmuxBlockCommand(t, words[4])
		wantElse := map[string]string{"#{@projmux_pane_uid}": "kill-pane", "#{@projmux_window_uid}": "kill-window"}[words[2]]
		if wantElse == "" || elseBranch != wantElse {
			t.Fatalf("%s Kill guard %q else branch %q, want an identity mirror guarding tmux's stock %q", bind, words[2], elseBranch, wantElse)
		}
		if strings.Contains(managedBranch, "kill-") || strings.Contains(managedBranch, "delete-confirm") || !strings.HasPrefix(managedBranch, "run-shell ") {
			t.Fatalf("%s Kill managed branch %q must be one projmux intent with no raw kill and no second confirmation", bind, managedBranch)
		}
	}
}

func TestManagedWindowMenuRunsTheCatalogCreateAndRenameCommands(t *testing.T) {
	t.Parallel()

	const bin = "/usr/local/bin/projmux"
	app := tmuxAppConfig(bin, "/bin/sh", config.StatusbarDecorationOff)
	catalog := defaultKeyBindingCatalog()
	create, _ := keyBindingActionByID(catalog, "new-window")
	rename, _ := keyBindingActionByID(catalog, "rename-window")
	createBinding, renameBinding := renderTmuxBindingBody(bin, create), renderTmuxBindingBody(bin, rename)
	promptHead := `command-prompt -I "#{window_name}" `
	template, ok := strings.CutPrefix(renameBinding, promptHead)
	if !ok {
		t.Fatalf("catalog rename binding %q no longer starts with %q", renameBinding, promptHead)
	}
	// What tmux 3.6 substitutes when a mouse menu opens on a non-current Window
	// whose active Pane is %1.
	target := map[string]string{"window_name": "clicked-window", "pane_id": "%1", "client_tty": "/dev/pts/7"}
	responsePlaceholder := regexp.MustCompile(`%[1-9]`)

	for _, bind := range []string{"bind-key < ", "bind-key -n MouseDown3Status ", "bind-key -n M-MouseDown3Status "} {
		menu := generatedMenu(t, app, bind)
		if got := menu.item(t, "New At End").Command; got != createBinding {
			t.Fatalf("%s New At End = %q, want the catalog window.create binding %q", bind, got, createBinding)
		}
		item := menu.item(t, "Rename").Command
		opened := expandTmuxFormatsForTest(t, item, target)
		// At menu open the initial text names the menu-target Window, and the
		// template is left exactly as the catalog key binding carries it.
		if opened != `command-prompt -I "clicked-window" `+template {
			t.Fatalf("%s Rename after menu open = %q, want the catalog template %q", bind, opened, template)
		}
		if responsePlaceholder.MatchString(strings.TrimPrefix(opened, `command-prompt -I "clicked-window" `)) {
			t.Fatalf("%s Rename puts an expanded Pane handle into the prompt template: %q", bind, opened)
		}
	}
	// The catalog binding embedded unescaped is the measured failure: menu open
	// turns #{pane_id} into %1, which command-prompt then replaces with the name.
	if naive := expandTmuxFormatsForTest(t, renameBinding, target); !responsePlaceholder.MatchString(strings.TrimPrefix(naive, `command-prompt -I "clicked-window" `)) {
		t.Fatalf("fixture no longer reproduces the %%N placeholder collision: %q", naive)
	}
}
